package certs

import (
	"crypto/tls"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCertificateForServerName_ParallelNonBlocking verifies that generating
// leaf certs for distinct hosts does NOT serialize on the manager's mutex.
// Before Round 8 the manager held a sync.Mutex across RSA keygen + x509 sign,
// so cache-miss generation for one host blocked cert lookup for all other
// hosts. After Round 8 the slow path holds only a brief write lock while the
// expensive crypto runs unguarded, allowing concurrent generation across
// distinct hosts.
//
// Signal: an atomic in-flight counter captures the peak concurrency of the
// generation code path. If generation serializes, peak is 1. If it overlaps,
// peak >= 2.
func TestCertificateForServerName_ParallelNonBlocking(t *testing.T) {
	mgr := newTestManager(t)

	const hosts = 8
	var peak int32
	mgr.setGenProbe(func() {
		n := atomic.AddInt32(&mgr.genInFlight, 1)
		defer atomic.AddInt32(&mgr.genInFlight, -1)
		for {
			old := atomic.LoadInt32(&peak)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&peak, old, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
	})

	var wg sync.WaitGroup
	for i := 0; i < hosts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			host := "host-" + string(rune('a'+idx)) + ".cursor.sh"
			if _, err := mgr.CertificateForServerName(host); err != nil {
				t.Errorf("host %s: %v", host, err)
			}
		}(i)
	}

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("parallel generation timed out")
	}

	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("generation serialized: peak in-flight=%d, want >=2", got)
	}
}

// TestCertificateForServerName_SameHostSingleGen verifies that 100 concurrent
// requests for the SAME host trigger exactly one leaf-cert generation; all
// others must hit the cache (or be coalesced by singleflight) rather than
// duplicate the RSA keygen.
func TestCertificateForServerName_SameHostSingleGen(t *testing.T) {
	mgr := newTestManager(t)

	var genCount int32
	mgr.setGenProbe(func() {
		atomic.AddInt32(&genCount, 1)
		time.Sleep(15 * time.Millisecond)
	})

	const n = 100
	host := "duplicate.cursor.sh"
	var wg sync.WaitGroup
	results := make([]*tls.Certificate, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cert, err := mgr.CertificateForServerName(host)
			results[idx] = cert
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&genCount); got != 1 {
		t.Fatalf("expected exactly 1 generation for same host, got %d", got)
	}
	var firstDER []byte
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("call %d: %v", i, errs[i])
		}
		if r == nil || len(r.Certificate) == 0 {
			t.Fatalf("call %d: empty cert", i)
		}
		if i == 0 {
			firstDER = r.Certificate[0]
			continue
		}
		if !equalBytes(r.Certificate[0], firstDER) {
			t.Fatalf("call %d returned a different leaf cert than call 0", i)
		}
	}
}

// TestCertificateForServerName_HitAfterMissReturnsCached confirms that a
// second call for an already-generated host is served from cache (no extra
// generation).
func TestCertificateForServerName_HitAfterMissReturnsCached(t *testing.T) {
	mgr := newTestManager(t)

	var genCount int32
	mgr.setGenProbe(func() {
		atomic.AddInt32(&genCount, 1)
	})

	host := "cached.cursor.sh"
	if _, err := mgr.CertificateForServerName(host); err != nil {
		t.Fatalf("first: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := mgr.CertificateForServerName(host); err != nil {
			t.Fatalf("hit %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&genCount); got != 1 {
		t.Fatalf("expected 1 generation total, got %d", got)
	}
}

// newTestManager returns a Manager backed by a freshly generated CA in a
// per-test temp dir (no host trust injection required).
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "ca.crt")
	keyPath := filepath.Join(tmp, "ca.key")
	mgr, err := EnsureCA(certPath, keyPath)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	return mgr
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
