package bridge

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestProxyServiceMultiCursorActivityCallback verifies that
// SetCursorActivityCallback supports multiple subscriptions instead of
// overwriting the previous one.
//
// Regression: previously onCursorActivity was a single-slot func, so the
// second SetCursorActivityCallback call (runner.go:153, emitting the Wails
// cursor:activity event) silently replaced the first one (runner.go:78,
// bridging MITM activity into PetService FSM). The MITM → PetService path
// was therefore dead and the pet never reacted to cursor requests.
//
// This test registers two callbacks, fires one activity event, and asserts
// BOTH were invoked with the same (method, path) pair.
func TestProxyServiceMultiCursorActivityCallback(t *testing.T) {
	// ProxyService zero value is sufficient: SetCursorActivityCallback and
	// FireCursorActivity only touch the onCursorActivity slice, never core.
	svc := &ProxyService{}

	var (
		firstCalls  atomic.Int64
		secondCalls atomic.Int64
		firstMux    sync.Mutex
		secondMux   sync.Mutex
		firstArgs   []struct{ method, path string }
		secondArgs  []struct{ method, path string }
	)

	var wg sync.WaitGroup
	wg.Add(2)

	// Callback #1 — emulates the MITM → PetService FSM bridge (runner.go:78).
	svc.SetCursorActivityCallback(func(method, path string) {
		defer wg.Done()
		firstCalls.Add(1)
		firstMux.Lock()
		firstArgs = append(firstArgs, struct{ method, path string }{method, path})
		firstMux.Unlock()
	})

	// Callback #2 — emulates the MITM → Wails frontend emit (runner.go:153).
	svc.SetCursorActivityCallback(func(method, path string) {
		defer wg.Done()
		secondCalls.Add(1)
		secondMux.Lock()
		secondArgs = append(secondArgs, struct{ method, path string }{method, path})
		secondMux.Unlock()
	})

	const (
		wantMethod = "POST"
		wantPath   = "/v1/chat/completions"
	)
	svc.FireCursorActivity(wantMethod, wantPath)

	wg.Wait()

	if got := firstCalls.Load(); got != 1 {
		t.Fatalf("callback #1 (PetService FSM bridge) expected 1 call, got %d", got)
	}
	if got := secondCalls.Load(); got != 1 {
		t.Fatalf("callback #2 (Wails emit) expected 1 call, got %d", got)
	}

	firstMux.Lock()
	if len(firstArgs) != 1 || firstArgs[0].method != wantMethod || firstArgs[0].path != wantPath {
		t.Fatalf("callback #1 received wrong args: %+v", firstArgs)
	}
	firstMux.Unlock()

	secondMux.Lock()
	if len(secondArgs) != 1 || secondArgs[0].method != wantMethod || secondArgs[0].path != wantPath {
		t.Fatalf("callback #2 received wrong args: %+v", secondArgs)
	}
	secondMux.Unlock()
}

// TestProxyServiceFireCursorActivity_NoCallbacks ensures FireCursorActivity
// is a safe no-op when no callback has been registered (defensive: avoids
// nil-call panics on a freshly constructed service).
func TestProxyServiceFireCursorActivity_NoCallbacks(t *testing.T) {
	svc := &ProxyService{}

	// Must not panic.
	svc.FireCursorActivity("GET", "/health")
}

// TestProxyServiceFireCursorActivity_NilCallbackSkipped ensures a nil
// callback inside the slice is skipped without breaking the remaining ones.
// (Defensive against callers who append nil through the public API — currently
// impossible because the signature takes a non-nil func, but the slice copy
// semantics make this cheap to guard.)
func TestProxyServiceFireCursorActivity_NilCallbackSkipped(t *testing.T) {
	svc := &ProxyService{}

	var called atomic.Int64
	svc.SetCursorActivityCallback(func(method, path string) {
		called.Add(1)
	})
	// Inject a nil entry directly to simulate a degenerate slot.
	svc.onCursorActivityMu.Lock()
	svc.onCursorActivity = append(svc.onCursorActivity, nil)
	svc.onCursorActivityMu.Unlock()
	svc.SetCursorActivityCallback(func(method, path string) {
		called.Add(1)
	})

	svc.FireCursorActivity("POST", "/v1/responses")

	if got := called.Load(); got != 2 {
		t.Fatalf("expected 2 real callbacks invoked, got %d", got)
	}
}
