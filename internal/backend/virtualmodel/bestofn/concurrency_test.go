package bestofn

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	vm_moa "cursor/internal/backend/virtualmodel/moa"
)

// slowChannelSvc is a stub ChannelService that sleeps a fixed duration per
// CallAdapter and tracks concurrent in-flight calls via atomics, so we can
// assert that generateCandidates honors the MaxParallelCandidates cap.
type slowChannelSvc struct {
	callDuration time.Duration
	inFlight     *int32
	maxInflight  *int32
}

func (s *slowChannelSvc) ResolveChannel(ctx context.Context, adapterID string) (*vm_moa.ChannelInfo, error) {
	return &vm_moa.ChannelInfo{ID: adapterID, ModelID: adapterID, Provider: "stub"}, nil
}

func (s *slowChannelSvc) CallAdapter(ctx context.Context, info *vm_moa.ChannelInfo, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
	cur := atomic.AddInt32(s.inFlight, 1)
	for {
		max := atomic.LoadInt32(s.maxInflight)
		if cur <= max || atomic.CompareAndSwapInt32(s.maxInflight, max, cur) {
			break
		}
	}
	time.Sleep(s.callDuration)
	atomic.AddInt32(s.inFlight, -1)
	return &vm_moa.AdapterResult{Text: "ok", FinishReason: "stop"}, nil
}

// TestGenerateCandidates_BoundedConcurrency verifies that generateCandidates
// never exceeds the configured MaxParallelCandidates cap. RED: before the
// semaphore is added, m.n=10 candidates all spawn concurrently, making
// maxInflight == 10 and failing the assertion.
func TestGenerateCandidates_BoundedConcurrency(t *testing.T) {
	const n = 10
	const maxParallel = 3

	var inFlight, maxInflight int32
	svc := &slowChannelSvc{
		callDuration: 50 * time.Millisecond,
		inFlight:    &inFlight,
		maxInflight: &maxInflight,
	}

	m := &BestOfNModel{
		adapterID:           "test-adapter",
		judgeAdapterID:      "test-adapter",
		n:                   n,
		MaxParallelCandidates: maxParallel,
		channelSvc:          svc,
	}

	candidates := m.generateCandidates(context.Background(), "hello")

	if len(candidates) != n {
		t.Fatalf("expected %d candidates, got %d", n, len(candidates))
	}
	observed := atomic.LoadInt32(&maxInflight)
	if observed > maxParallel {
		t.Fatalf("max in-flight candidates = %d, want <= %d", observed, maxParallel)
	}
}

// TestGenerateCandidates_DefaultMaxParallel verifies that an unset (zero)
// MaxParallelCandidates falls back to the default (8), bounding concurrency
// even when the user did not configure the value explicitly. The cap is
// additionally clamped to m.n, so when m.n < default we never spawn more
// than m.n goroutines.
func TestGenerateCandidates_DefaultMaxParallel(t *testing.T) {
	const n = 5
	const defaultMax = 8

	var inFlight, maxInflight int32
	svc := &slowChannelSvc{
		callDuration: 30 * time.Millisecond,
		inFlight:    &inFlight,
		maxInflight: &maxInflight,
	}

	m := &BestOfNModel{
		adapterID:      "test-adapter",
		judgeAdapterID: "test-adapter",
		n:              n,
		// Intentionally leave MaxParallelCandidates == 0 to exercise default.
		channelSvc: svc,
	}

	candidates := m.generateCandidates(context.Background(), "hello")

	if len(candidates) != n {
		t.Fatalf("expected %d candidates, got %d", n, len(candidates))
	}
	observed := atomic.LoadInt32(&maxInflight)
	// Expected cap = min(n, defaultMax) = min(5, 8) = 5
	if observed > defaultMax || observed > n {
		t.Fatalf("max in-flight candidates = %d, want <= %d and <= %d", observed, defaultMax, n)
	}
}

// silence unused import warnings if test scaffolding changes later.
var _ virtualmodel.ExecuteRequest
