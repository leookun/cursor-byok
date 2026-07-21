// Package app — AppContext provides a process-wide cancellable context that
// every background goroutine can derive from. OnShutdown cancels it so all
// derived work drains deterministically before the process exits.
//
// R16: lifecycle unification. Previously each background goroutine owned a
// private context.WithCancel(context.Background()) and joined shutdown by
// convention; AppContext makes the contract explicit and testable.
package app

import "context"

// AppContext wraps a root context.Context + CancelFunc so the application
// lifecycle can cancel every derived goroutine in one call.
type AppContext struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// NewAppContext returns an AppContext whose Context() is derived from
// context.Background(). Call Cancel() (idempotent) on shutdown.
func NewAppContext() *AppContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &AppContext{ctx: ctx, cancel: cancel}
}

// Context returns the root context. The same instance is returned across
// calls so subscribers can store it once and observe Cancel().
func (a *AppContext) Context() context.Context {
	if a == nil {
		return context.Background()
	}
	return a.ctx
}

// Cancel signals every derived context to exit. Idempotent.
func (a *AppContext) Cancel() {
	if a == nil || a.cancel == nil {
		return
	}
	a.cancel()
}
