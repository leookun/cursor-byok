package moa

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
	vmconfig "cursor/internal/backend/virtualmodel/config"
)

// slowChannelSvc is a stub ChannelService whose CallAdapter sleeps a fixed
// duration and tracks concurrent in-flight calls via atomics. It is used to
// verify that executeExperts honors the configured MaxParallelExperts cap.
type slowChannelSvc struct {
	callDuration time.Duration
	inFlight     *int32
	maxInflight  *int32
	mu           sync.Mutex // guards maxInflight updates
}

func (s *slowChannelSvc) ResolveChannel(ctx context.Context, adapterID string) (*ChannelInfo, error) {
	return &ChannelInfo{ID: adapterID, ModelID: adapterID, Provider: "stub"}, nil
}

func (s *slowChannelSvc) CallAdapter(ctx context.Context, info *ChannelInfo, messages []Message, systemPrompt string) (*AdapterResult, error) {
	cur := atomic.AddInt32(s.inFlight, 1)
	for {
		max := atomic.LoadInt32(s.maxInflight)
		if cur <= max || atomic.CompareAndSwapInt32(s.maxInflight, max, cur) {
			break
		}
	}
	time.Sleep(s.callDuration)
	atomic.AddInt32(s.inFlight, -1)
	return &AdapterResult{Text: "ok", FinishReason: "stop"}, nil
}

// buildWorkflowWithNExperts constructs a workflow containing n conditional
// expert nodes (in addition to the planner/critic/judge/aggregator that
// executeExperts skips). Each expert is enabled and conditional so that
// passing their roles via activeRoles will activate them.
func buildWorkflowWithNExperts(n int) *vmconfig.WorkflowConfig {
	wf := &vmconfig.WorkflowConfig{
		ID:   "test-bounded",
		Name: "Bounded Concurrency Test",
	}
	wf.Nodes = append(wf.Nodes, vmconfig.WorkflowNodeConfig{
		ID: "planner", Role: vmconfig.RolePlanner, ExecutionMode: vmconfig.ModeAlways, Enabled: true,
	})
	for i := 0; i < n; i++ {
		role := vmconfig.NodeRole("expert-" + string(rune('a'+i)))
		wf.Nodes = append(wf.Nodes, vmconfig.WorkflowNodeConfig{
			ID:            string(rune('a' + i)),
			Role:          role,
			ExecutionMode: vmconfig.ModeConditional,
			Enabled:       true,
		})
	}
	return wf
}

// TestExecuteExperts_BoundedConcurrency verifies that executeExperts never
// exceeds the configured MaxParallelExperts when invoking adapters in
// parallel. RED: before the semaphore is added, 6 experts will all run
// concurrently, making maxInflight == 6 and failing the assertion.
func TestExecuteExperts_BoundedConcurrency(t *testing.T) {
	const numExperts = 6
	const maxParallel = 2

	var inFlight, maxInflight int32
	svc := &slowChannelSvc{
		callDuration: 50 * time.Millisecond,
		inFlight:    &inFlight,
		maxInflight: &maxInflight,
	}

	wf := buildWorkflowWithNExperts(numExperts)
	cfg := vmconfig.DefaultMOAConfig()
	cfg.Enabled = true
	// Set the new field under test (RED: field does not exist yet).
	cfg.MaxParallelExperts = maxParallel

	m := &MOAModel{
		config:     cfg,
		workflow:  wf,
		channelSvc: svc,
	}

	activeRoles := make([]vmconfig.NodeRole, 0, numExperts)
	for _, node := range wf.Nodes {
		if node.Role == vmconfig.RolePlanner {
			continue
		}
		activeRoles = append(activeRoles, node.Role)
	}

	req := &virtualmodel.ExecuteRequest{LatestUserText: "hello"}
	results := m.executeExperts(context.Background(), req, activeRoles, "plan")

	if len(results) != numExperts {
		t.Fatalf("expected %d expert results, got %d", numExperts, len(results))
	}
	observed := atomic.LoadInt32(&maxInflight)
	if observed > maxParallel {
		t.Fatalf("max in-flight experts = %d, want <= %d", observed, maxParallel)
	}
}

// TestExecuteExperts_DefaultMaxParallel verifies that an unset (zero)
// MaxParallelExperts falls back to the default (4), bounding concurrency
// even when the user did not configure the value explicitly.
func TestExecuteExperts_DefaultMaxParallel(t *testing.T) {
	const numExperts = 8
	const defaultMax = 4

	var inFlight, maxInflight int32
	svc := &slowChannelSvc{
		callDuration: 30 * time.Millisecond,
		inFlight:    &inFlight,
		maxInflight: &maxInflight,
	}

	wf := buildWorkflowWithNExperts(numExperts)
	cfg := vmconfig.DefaultMOAConfig()
	cfg.Enabled = true
	// Intentionally leave MaxParallelExperts == 0 to exercise the default.

	m := &MOAModel{
		config:     cfg,
		workflow:   wf,
		channelSvc: svc,
	}

	activeRoles := make([]vmconfig.NodeRole, 0, numExperts)
	for _, node := range wf.Nodes {
		if node.Role == vmconfig.RolePlanner {
			continue
		}
		activeRoles = append(activeRoles, node.Role)
	}

	req := &virtualmodel.ExecuteRequest{LatestUserText: "hello"}
	_ = m.executeExperts(context.Background(), req, activeRoles, "plan")

	observed := atomic.LoadInt32(&maxInflight)
	if observed > defaultMax {
		t.Fatalf("max in-flight experts = %d, want <= default %d", observed, defaultMax)
	}
}
