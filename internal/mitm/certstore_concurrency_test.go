package mitm

import (
	"crypto/tls"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// genCertStub returns a gen() callback that produces a unique tls.Certificate
// (with a distinct DER slot so callers can compare identity) after a short
// sleep, while counting invocations and tracking peak concurrency.
func genCertStub(der []byte, genCount *int32, peak *int32) func() (*tls.Certificate, error) {
	return func() (*tls.Certificate, error) {
		n := atomic.AddInt32(genCount, 1)
		// Track peak in-flight.
		for {
			old := atomic.LoadInt32(peak)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(peak, old, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(genCount, -1)
		return &tls.Certificate{Certificate: [][]byte{der}}, nil
	}
}

func TestMITMCertStore_ParallelHostsDoNotSerialize(t *testing.T) {
	store := newMITMCertStore()

	const hosts = 8
	var genCount, peak int32
	var wg sync.WaitGroup
	for i := 0; i < hosts; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			host := "host-" + string(rune('a'+idx)) + ".cursor.sh"
			der := []byte{byte('a' + idx)}
			_, err := store.Fetch(host, genCertStub(der, &genCount, &peak))
			if err != nil {
				t.Errorf("host %d: %v", idx, err)
			}
		}(i)
	}

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("parallel Fetch timed out")
	}

	if got := atomic.LoadInt32(&peak); got < 2 {
		t.Fatalf("Fetch serialized gen(): peak in-flight=%d, want >=2", got)
	}
}

func TestMITMCertStore_SameHostSingleGen(t *testing.T) {
	store := newMITMCertStore()

	var genCount, peak int32
	host := "duplicate.cursor.sh"
	der := []byte{0x42}

	const n = 100
	var wg sync.WaitGroup
	results := make([]*tls.Certificate, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cert, err := store.Fetch(host, genCertStub(der, &genCount, &peak))
			results[idx] = cert
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	// singleflight must coalesce: only the leader runs gen(); waiters reuse.
	// Since genCount is bumped-then-decremented, a fully-coalesced run sees a
	// max observed value of 1.
	if got := atomic.LoadInt32(&peak); got != 1 {
		t.Fatalf("expected peak in-flight=1 (singleflight coalesce), got %d", got)
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
		if !equalBytesMITM(r.Certificate[0], firstDER) {
			t.Fatalf("call %d returned a different leaf cert than call 0", i)
		}
	}
}

func TestMITMCertStore_HitAfterMissReturnsCached(t *testing.T) {
	store := newMITMCertStore()

	host := "cached.cursor.sh"
	der := []byte{0x43}

	var missCount, peak int32
	missGen := genCertStub(der, &missCount, &peak)
	if _, err := store.Fetch(host, missGen); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Subsequent calls must NOT invoke gen() — should hit cache. Use a fresh
	// counter so the miss call above doesn't pollute the signal.
	var hitCount, hitPeak int32
	hitGen := genCertStub(der, &hitCount, &hitPeak)
	for i := 0; i < 10; i++ {
		if _, err := store.Fetch(host, hitGen); err != nil {
			t.Fatalf("hit %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hitCount); got != 0 {
		t.Fatalf("cache hits should not invoke gen(), gen ran %d times", got)
	}
}

func TestMITMCertStore_GenErrorNotCached(t *testing.T) {
	store := newMITMCertStore()
	host := "err.cursor.sh"

	errGen := func() (*tls.Certificate, error) {
		return nil, errors.New("boom")
	}
	if _, err := store.Fetch(host, errGen); err == nil {
		t.Fatal("expected error from gen")
	}
	// A later successful gen must still be invoked (error not cached).
	var calls int32
	okGen := func() (*tls.Certificate, error) {
		atomic.AddInt32(&calls, 1)
		return &tls.Certificate{Certificate: [][]byte{{1}}}, nil
	}
	if _, err := store.Fetch(host, okGen); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected okGen to run once, got %d", got)
	}
}

func equalBytesMITM(a, b []byte) bool {
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
