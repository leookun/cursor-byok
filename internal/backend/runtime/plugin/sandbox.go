package plugin

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// DefaultLoadTimeout bounds how long a plugin's Init may run when loaded.
const DefaultLoadTimeout = 10 * time.Second

// DefaultCallTimeout bounds how long a single CallPlugin invocation may run.
const DefaultCallTimeout = 5 * time.Second

// runSandboxed executes fn with a timeout and panic recovery inside a
// constrained goroutine. If fn panics, the panic is captured as an error
// instead of crashing the host. If fn overruns the timeout (or the parent ctx
// is cancelled), the timeout error is returned and fn's goroutine is abandoned.
//
// ponytail: Go cannot forcibly kill a goroutine, so the isolation ceiling is
// timeout + panic recovery within a goroutine. Strong isolation (memory/files
// boundaries, syscall filtering) needs WASM or a separate process — tracked in
// ADR-047. MVP executes trusted, built-in plugin code with this bounded sandbox.
func runSandboxed(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) error) error {
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	sandboxedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("plugin panicked: %v\n%s", r, debug.Stack())
			}
		}()
		done <- fn(sandboxedCtx)
	}()

	select {
	case err := <-done:
		return err
	case <-sandboxedCtx.Done():
		// Result of the abandoned goroutine is discarded; we report timeout.
		return fmt.Errorf("plugin operation timed out after %s", timeout)
	}
}

// callSandboxed runs a callable plugin invocation, returning its result or an
// error, with the same timeout + panic-recovery guarantees as runSandboxed.
func callSandboxed(ctx context.Context, timeout time.Duration, fn func(ctx context.Context) (map[string]any, error)) (map[string]any, error) {
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	sandboxedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{err: fmt.Errorf("plugin panicked: %v\n%s", r, debug.Stack())}
			}
		}()
		out, err := fn(sandboxedCtx)
		done <- result{out: out, err: err}
	}()

	select {
	case res := <-done:
		return res.out, res.err
	case <-sandboxedCtx.Done():
		return nil, fmt.Errorf("plugin call timed out after %s", timeout)
	}
}