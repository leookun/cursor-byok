package modeladapter

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	defaultProviderStreamIdleTimeout = 4 * time.Minute
	minProviderStreamIdleTimeout     = 30 * time.Second
)

type providerStreamIdleWatchdog struct {
	ctx     context.Context
	cancel  context.CancelCauseFunc
	timeout time.Duration
	timer   *time.Timer

	mu       sync.Mutex
	body     io.Closer
	stopped  bool
	timedOut bool
	err      error
}

func newProviderStreamIdleWatchdog(parent context.Context, timeout time.Duration) (context.Context, *providerStreamIdleWatchdog) {
	if parent == nil {
		parent = context.Background()
	}
	timeout = normalizeProviderStreamIdleTimeoutDuration(timeout)
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := &providerStreamIdleWatchdog{
		ctx:     ctx,
		cancel:  cancel,
		timeout: timeout,
		err:     providerStreamIdleTimeoutError(timeout),
	}
	watchdog.timer = time.AfterFunc(watchdog.timeout, watchdog.expire)
	return ctx, watchdog
}

func (watchdog *providerStreamIdleWatchdog) AttachBody(body io.Closer) {
	if watchdog == nil || body == nil {
		return
	}
	watchdog.mu.Lock()
	watchdog.body = body
	shouldClose := watchdog.timedOut || watchdog.stopped
	watchdog.mu.Unlock()
	if shouldClose {
		_ = body.Close()
	}
}

func (watchdog *providerStreamIdleWatchdog) MarkEffectiveContent() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.stopped || watchdog.timedOut || watchdog.timer == nil {
		return
	}
	watchdog.timer.Reset(watchdog.timeout)
}

func (watchdog *providerStreamIdleWatchdog) Stop() {
	if watchdog == nil {
		return
	}
	watchdog.mu.Lock()
	if watchdog.stopped {
		watchdog.mu.Unlock()
		return
	}
	watchdog.stopped = true
	watchdog.body = nil
	if watchdog.timer != nil {
		watchdog.timer.Stop()
	}
	watchdog.mu.Unlock()
	watchdog.cancel(nil)
}

func (watchdog *providerStreamIdleWatchdog) Err() error {
	if watchdog == nil {
		return nil
	}
	watchdog.mu.Lock()
	defer watchdog.mu.Unlock()
	if watchdog.timedOut {
		return watchdog.err
	}
	return nil
}

func (watchdog *providerStreamIdleWatchdog) expire() {
	watchdog.mu.Lock()
	if watchdog.stopped || watchdog.timedOut {
		watchdog.mu.Unlock()
		return
	}
	watchdog.timedOut = true
	body := watchdog.body
	err := watchdog.err
	watchdog.mu.Unlock()

	watchdog.cancel(err)
	if body != nil {
		_ = body.Close()
	}
}

func normalizeProviderStreamIdleTimeoutDuration(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultProviderStreamIdleTimeout
	}
	if timeout < minProviderStreamIdleTimeout {
		return minProviderStreamIdleTimeout
	}
	return timeout
}

func providerStreamIdleTimeoutError(timeout time.Duration) error {
	seconds := int(timeout / time.Second)
	if seconds > 0 && timeout == time.Duration(seconds)*time.Second {
		return fmt.Errorf("provider stream idle timeout after %ds without effective content", seconds)
	}
	return fmt.Errorf("provider stream idle timeout after %s without effective content", timeout)
}
