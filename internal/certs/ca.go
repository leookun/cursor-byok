package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Manager 定义了当前模块中的 Manager 类型。
type Manager struct {
	// caCert 表示当前声明中的 caCert。
	caCert *x509.Certificate
	// caKey 表示当前声明中的 caKey。
	caKey crypto.PrivateKey
	// caCertPEM 缓存构造 Manager 时使用的 CA 证书 PEM（可选），
	// 供宿主信任注入（EnsureCA / loadCAFromDisk 会写入）。
	caCertPEM []byte

	// mu guards cache and caCertPEM. Read-most: leaf cert lookup is hot path
	// on every TLS handshake; use RWMutex so cache hits don't block on a
	// writer that may be in the middle of a slow RSA keygen for another host.
	mu sync.RWMutex
	// cache 表示当前声明中的 cache。
	cache map[string]*tls.Certificate

	// genInFlight counts leaf cert generations currently in progress (atomic).
	// Exposed for concurrency tests to prove parallel generation across
	// distinct hosts. Zero-cost in production.
	genInFlight int32
	// genProbe, if non-nil, is invoked at the start of leaf generation. Used
	// by tests to inject delays / counts. nil-checked so production pays only
	// a single nil-compare.
	genProbe func()
	// sf coalesces concurrent CertificateForServerName calls for the same
	// host: only one goroutine performs the expensive RSA keygen + x509 sign
	// per host; duplicates wait and reuse the result. Calls for DIFFERENT
	// hosts run in parallel because the cache mutex is not held during the
	// expensive crypto work.
	sf singleflight.Group
}

// setGenProbe installs a leaf-generation probe used by concurrency tests.
// Must not be called concurrently with CertificateForServerName. Production
// callers never need this; it exists so tests can observe the generation
// critical section without reflection.
func (m *Manager) setGenProbe(fn func()) { m.genProbe = fn }

// NewManager 用于处理与 NewManager 相关的逻辑。
func NewManager(caCertPath, caKeyPath string) (*Manager, error) {
	certPEM, keyPEM, err := loadCAPEMFromFiles(caCertPath, caKeyPath)
	if err != nil {
		return nil, err
	}
	mgr, err := NewManagerFromPEM(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	mgr.caCertPEM = cloneBytes(certPEM)
	return mgr, nil
}

// NewManagerFromPEM 用于处理与 NewManagerFromPEM 相关的逻辑。
func NewManagerFromPEM(caCertPEM, caKeyPEM []byte) (*Manager, error) {
	caCert, caKey, err := loadCAFromPEM(caCertPEM, caKeyPEM)
	if err != nil {
		return nil, err
	}
	return &Manager{caCert: caCert, caKey: caKey, cache: make(map[string]*tls.Certificate)}, nil
}

// CATLSCertificate 用于处理与 CATLSCertificate 相关的逻辑。
func (m *Manager) CATLSCertificate() (*tls.Certificate, error) {
	if m == nil || m.caCert == nil || m.caKey == nil {
		return nil, errors.New("CA is not initialized")
	}
	return &tls.Certificate{
		Certificate: [][]byte{append([]byte(nil), m.caCert.Raw...)},
		PrivateKey:  m.caKey,
		Leaf:        m.caCert,
	}, nil
}

// CertificateForServerName 用于处理与 CertificateForServerName 相关的逻辑。
func (m *Manager) CertificateForServerName(serverName string) (*tls.Certificate, error) {
	host := normalizeHost(serverName)
	if host == "" {
		return nil, errors.New("empty server name")
	}

	// Fast path: read lock, check cache. RWMutex means concurrent cache hits
	// (the common case on every TLS handshake after first-touch) do not block
	// on a writer that may be storing a freshly generated cert for another host.
	m.mu.RLock()
	if cert, ok := m.cache[host]; ok {
		m.mu.RUnlock()
		return cert, nil
	}
	m.mu.RUnlock()

	// Slow path: singleflight per host. Only one goroutine generates per host;
	// concurrent callers for the SAME host wait and reuse the result. Calls for
	// DIFFERENT hosts run in parallel because the expensive RSA keygen + x509
	// sign below runs WITHOUT holding m.mu.
	v, err, _ := m.sf.Do(host, func() (any, error) {
		// Double-check cache: another goroutine may have generated this host
		// while we were waiting on the singleflight leader.
		m.mu.RLock()
		if cert, ok := m.cache[host]; ok {
			m.mu.RUnlock()
			return cert, nil
		}
		m.mu.RUnlock()

		if m.genProbe != nil {
			m.genProbe()
		}

		cert, err := m.generateLeafCert(host)
		if err != nil {
			return nil, err
		}

		// Store under a brief write lock. Generation happened outside the lock
		// so concurrent generations for other hosts are not blocked by this
		// mutation.
		m.mu.Lock()
		// Avoid clobbering if a concurrent singleflight for the same host raced
		// (singleflight should prevent this, but the guard is cheap and safe).
		if existing, ok := m.cache[host]; ok {
			m.mu.Unlock()
			return existing, nil
		}
		m.cache[host] = cert
		m.mu.Unlock()
		return cert, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*tls.Certificate), nil
}

// generateLeafCert performs the expensive RSA keygen + x509 signing for a
// single host. Callers MUST NOT hold m.mu while invoking this — the RSA
// keygen alone takes ~5-20ms and serializing it across distinct hosts would
// bottleneck first-touch TLS handshakes under load.
func (m *Manager) generateLeafCert(host string) (*tls.Certificate, error) {
	atomic.AddInt32(&m.genInFlight, 1)
	defer atomic.AddInt32(&m.genInFlight, -1)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	leaf := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   host,
			Organization: []string{"Cursor Local Proxy"},
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if len(m.caCert.SubjectKeyId) > 0 {
		leaf.AuthorityKeyId = append([]byte(nil), m.caCert.SubjectKeyId...)
	}

	if ip := net.ParseIP(host); ip != nil {
		leaf.IPAddresses = []net.IP{ip}
	} else {
		leaf.DNSNames = []string{host}
	}

	leafPrivateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	leafPublicKey := &leafPrivateKey.PublicKey

	der, err := x509.CreateCertificate(rand.Reader, leaf, m.caCert, leafPublicKey, m.caKey)
	if err != nil {
		return nil, err
	}

	leafCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chainPEM := append([]byte(nil), leafCertPEM...)
	chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.caCert.Raw})...)

	keyPEM, err := marshalPrivateKeyPEM(leafPrivateKey)
	if err != nil {
		return nil, err
	}

	pair, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	parsedLeaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pair.Leaf = parsedLeaf

	return &pair, nil
}

// marshalPrivateKeyPEM 用于处理与 marshalPrivateKeyPEM 相关的逻辑。
func marshalPrivateKeyPEM(key any) ([]byte, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), nil
	case *ecdsa.PrivateKey:
		der, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
	case ed25519.PrivateKey:
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
	default:
		return nil, errors.New("unsupported private key type")
	}
}

// loadCAPEMFromFiles 用于处理与 loadCAPEMFromFiles 相关的逻辑。
func loadCAPEMFromFiles(certPath, keyPath string) ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// loadCAFromPEM 用于处理与 loadCAFromPEM 相关的逻辑。
func loadCAFromPEM(certPEM, keyPEM []byte) (*x509.Certificate, crypto.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		return nil, nil, errors.New("invalid CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, errors.New("invalid CA key PEM")
	}

	switch keyBlock.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			return nil, nil, err
		}
		return caCert, key, nil
	default:
		return nil, nil, errors.New("unsupported CA key format")
	}
}

// normalizeHost 用于处理与 normalizeHost 相关的逻辑。
func normalizeHost(serverName string) string {
	serverName = strings.TrimSpace(serverName)
	if strings.Contains(serverName, ":") {
		h, _, err := net.SplitHostPort(serverName)
		if err == nil {
			serverName = h
		}
	}
	return serverName
}

// cloneBytes 用于处理与 cloneBytes 相关的逻辑。
func cloneBytes(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
