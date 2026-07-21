// Package app — lifecycle.go provides a tiny helper for spawning background
// goroutines that are derived from AppContext and never panic-crash the
// process.
//
// R17: lifecycle unification. Replaces naked `go func(){...}()` patterns in
// runner.go that lacked both recover() and a context-derived cancellation
// hook. Each goroutine spawned via LifecycleGo observes the AppContext so
// OnShutdown (which cancels AppContext) propagates to every background
// worker.
package app

import (
	"context"

	"cursor/internal/logger"
)

// LifecycleGo spawns fn in a new goroutine whose context is derived from
// appCtx. The goroutine is wrapped in a recover() so a panic is logged
// rather than crashing the process. Returns immediately.
//
// If appCtx is nil, fn runs with context.Background().
//
// R17: lifecycle unification.
func LifecycleGo(appCtx *AppContext, name string, fn func(ctx context.Context)) {
	ctx := context.Background()
	if appCtx != nil {
		ctx = appCtx.Context()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorf("app: lifecycle goroutine %q panicked: %v", name, r)
			}
		}()
		fn(ctx)
	}()
}
