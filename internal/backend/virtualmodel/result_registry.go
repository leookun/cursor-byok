// Package virtualmodel provides shared types for AOS member task spawning and
// result collection across the forwarder ↔ AOS boundary.
//
// Phase 26a/b: AOSMemberSpawnerFunc + context injection for Cursor-native Task
// tool call emission.
//
// Phase 26c: AOSResultRegistry — goroutine-safe result channel registry that
// allows executeMemberTask to block until the spawned Task tool result arrives
// via the forwarder's tool result handling path.
package virtualmodel

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// AOSMemberResult carries the execution result of a spawned AOS member task.
type AOSMemberResult struct {
	Text  string
	Error error
}

// AOSResultRegistry maps AOS member result-correlation keys to waiting channels.
// For Cursor-native AOS Tasks, the key is the generated Task ToolCallId, not
// the ExecServerMessage exec ID. The method parameter remains named execID for
// compatibility with existing callers.
// Used by executeMemberTask (producer side) to wait for results, and by the
// forwarder's handleExecResult (consumer side) to deliver results.
//
// Goroutine-safe: all exported methods acquire the internal mutex.
//
// Concurrency contract (fixed Phase 2):
//   - Expect registers a buffered channel (cap 1).
//   - Resolve delivers into that channel and removes the pending entry so Count()
//     drops immediately (matches unit tests and avoids leak).
//   - Wait must not delete the pending entry before receiving; otherwise Resolve
//     races and becomes a no-op (historical bug: Wait deleted first → timeout).
//   - If Resolve arrives before Wait, the result is parked in completed until Wait.
type AOSResultRegistry struct {
	mu        sync.Mutex
	pending   map[string]chan AOSMemberResult
	completed map[string]AOSMemberResult
}

// NewAOSResultRegistry creates an empty result registry.
func NewAOSResultRegistry() *AOSResultRegistry {
	return &AOSResultRegistry{
		pending:   make(map[string]chan AOSMemberResult),
		completed: make(map[string]AOSMemberResult),
	}
}

// Expect registers a pending result for the given execID and returns
// a receive-only channel that will receive the result when Resolve is called.
// Must be called before (or concurrently with) the spawn that produces execID.
// The channel is buffered (cap 1) so Resolve never blocks.
func (r *AOSResultRegistry) Expect(execID string) <-chan AOSMemberResult {
	if r == nil {
		return nil
	}
	ch := make(chan AOSMemberResult, 1)
	r.mu.Lock()
	// Replace any stale entry for this execID.
	r.pending[execID] = ch
	delete(r.completed, execID)
	r.mu.Unlock()
	return ch
}

// Resolve delivers a result to the waiting channel identified by execID.
// If no Expect call was made for execID, Resolve is a no-op.
func (r *AOSResultRegistry) Resolve(execID, text string, err error) {
	if r == nil {
		return
	}
	result := AOSMemberResult{Text: text, Error: err}
	r.mu.Lock()
	ch, ok := r.pending[execID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.pending, execID)
	// Park for Wait that races after pending removal (Resolve-before-Wait).
	r.completed[execID] = result
	r.mu.Unlock()
	// Buffered send never blocks.
	ch <- result
}

// Remove deletes a pending expectation without delivering a result.
// Useful for cleanup when spawn fails or caller no longer needs the result.
func (r *AOSResultRegistry) Remove(execID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.pending, execID)
	delete(r.completed, execID)
	r.mu.Unlock()
}

// Wait blocks until the result for execID is available, up to the given
// timeout. The execID must have been registered via Expect() before Wait
// is called — either by the same goroutine (sequential spawn→wait) or by
// a different goroutine (batch spawn phase → batch resolve phase).
//
// Returns the result on success, or an error on timeout.
// This method is the primary mechanism for Phase 26e batch resolve: the
// spawn phase calls Expect(), the resolve phase calls Wait().
func (r *AOSResultRegistry) Wait(execID string, timeout time.Duration) (AOSMemberResult, error) {
	return r.WaitCtx(context.Background(), execID, timeout)
}

// [修复] 资源泄漏: WaitCtx 响应 context 取消，避免 goroutine 泄漏
func (r *AOSResultRegistry) WaitCtx(ctx context.Context, execID string, timeout time.Duration) (AOSMemberResult, error) {
	if r == nil {
		return AOSMemberResult{}, fmt.Errorf("AOSResultRegistry is nil")
	}
	r.mu.Lock()
	if result, ok := r.completed[execID]; ok {
		delete(r.completed, execID)
		r.mu.Unlock()
		return result, nil
	}
	ch, ok := r.pending[execID]
	r.mu.Unlock()
	if !ok {
		return AOSMemberResult{}, fmt.Errorf("no pending expectation for execID=%s", execID)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-ch:
		r.mu.Lock()
		delete(r.pending, execID)
		delete(r.completed, execID)
		r.mu.Unlock()
		return result, nil
	case <-timer.C:
		r.mu.Lock()
		delete(r.pending, execID)
		delete(r.completed, execID)
		r.mu.Unlock()
		return AOSMemberResult{}, fmt.Errorf("timed out waiting for execID=%s after %v", execID, timeout)
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.pending, execID)
		delete(r.completed, execID)
		r.mu.Unlock()
		return AOSMemberResult{}, fmt.Errorf("context cancelled waiting for execID=%s: %w", execID, ctx.Err())
	}
}

// Count returns the number of pending (unresolved) expectations.
func (r *AOSResultRegistry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	return n
}

// ---------------------------------------------------------------------------
// Context injection
// ---------------------------------------------------------------------------

type aosResultRegistryKey struct{}

// WithAOSResultRegistry stores an *AOSResultRegistry in context.
// Called by the forwarder in runProviderStream alongside
// WithAOSMemberSpawner.
func WithAOSResultRegistry(ctx context.Context, reg *AOSResultRegistry) context.Context {
	return context.WithValue(ctx, aosResultRegistryKey{}, reg)
}

// GetAOSResultRegistry extracts an *AOSResultRegistry from context.
// Called by AOSModel.executeMemberTask to register a waiting channel
// and by tests to directly inject mock results.
// Returns nil if no registry is present.
func GetAOSResultRegistry(ctx context.Context) *AOSResultRegistry {
	if r, ok := ctx.Value(aosResultRegistryKey{}).(*AOSResultRegistry); ok {
		return r
	}
	return nil
}
