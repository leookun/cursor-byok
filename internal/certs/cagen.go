package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// caValidity controls how long a generated CA cert is valid.
const caValidity = 10 * 365 * 24 * time.Hour

// EnsureCA loads an existing CA from certPath/keyPath, or generates a new
// self-signed CA if either file is missing. The private key is always written
// with mode 0o600; the cert with 0o644. Idempotent.
//
// This replaces NewEmbeddedManager — the CA private key MUST NOT be embedded
// in the binary. Each installation generates its own CA on first run.
func EnsureCA(certPath, keyPath string) (*Manager, error) {
	// Try load first — if both files exist and parse, reuse them.
	if mgr, err := loadCAFromDisk(certPath, keyPath); err == nil {
		return mgr, nil
	}

	// Generate a fresh CA.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir ca key dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir ca cert dir: %w", err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate ca key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"Cursor BYOK Local CA"},
			CommonName:   "Cursor BYOK Local CA",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create ca cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEMBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	// Write key first with restrictive perms; if cert write fails later the
	// key file is still private and we'll regenerate on next start.
	if err := os.WriteFile(keyPath, keyPEMBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write ca key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write ca cert: %w", err)
	}

	// Re-load via the existing constructor to ensure consistent state with
	// the file-on-disk path used elsewhere.
	return loadCAFromDisk(certPath, keyPath)
}

// loadCAFromDisk loads CA cert+key from disk and returns a Manager.
// Returns error if either file is missing or unparseable.
func loadCAFromDisk(certPath, keyPath string) (*Manager, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("read ca cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ca key: %w", err)
	}
	mgr, err := NewManagerFromPEM(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse ca: %w", err)
	}
	// Stash the PEM bytes for callers that need to inject the CA cert into
	// the host (Cursor) trust store.
	mgr.caCertPEM = cloneBytes(certPEM)
	return mgr, nil
}

// CACertPEM returns a copy of the loaded CA certificate in PEM form.
// Returns nil if the Manager was not constructed via EnsureCA / loadCAFromDisk.
func (m *Manager) CACertPEM() []byte {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneBytes(m.caCertPEM)
}

// caCertPEMExists reports whether a CA cert file already exists at certPath.
// Helper for callers that want to decide whether to log "generated" vs "loaded".
func caCertPEMExists(certPath string) bool {
	_, err := os.Stat(certPath)
	return err == nil
}
