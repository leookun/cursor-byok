package forwarder

import (
	"context"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/internal/appdata"
)

// TestService_Shutdown_StopsMaintenanceGoroutine verifies that Service.Shutdown
// signals the history-maintenance goroutine to exit and waits for it. R15.
//
// TODO: enable -race in CI once CGO is available (R17 spec).
func TestService_Shutdown_StopsMaintenanceGoroutine(t *testing.T) {
	tempRoot := t.TempDir()
	// NewServiceWithRuntimes starts history maintenance in a naked goroutine.
	// We construct it directly so we can assert lifecycle wiring in isolation.
	service := NewServiceWithRuntimes(
		tempRoot,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	if service == nil {
		t.Fatal("NewServiceWithRuntimes returned nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("Service.Shutdown err=%v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("Service.Shutdown took too long: %v (maintenance goroutine may not be selecting on stopCh)", elapsed)
	}
	if !service.IsShutdown() {
		t.Fatal("Service.IsShutdown = false after Shutdown")
	}

	// Calling Shutdown again must be idempotent.
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("second Service.Shutdown err=%v (must be idempotent)", err)
	}
}

// TestService_Shutdown_ReleasesBroker verifies the StreamBroker's inflight
// streams are cancelled on shutdown (R15 wiring; covered fully by R17's broker
// shutdown test, but asserted here for Service-level integration).
func TestService_Shutdown_ReleasesBroker(t *testing.T) {
	tempRoot := t.TempDir()
	service := NewServiceWithRuntimes(
		tempRoot,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	_ = appdata.HistoryRootPath
	if service == nil {
		t.Fatal("NewServiceWithRuntimes returned nil")
	}
	if service.broker == nil {
		t.Fatal("service.broker nil; cannot test shutdown integration")
	}
	if _, err := service.broker.OpenStream("req-1", "conv-1", 0, "model", "Model", agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
		t.Fatalf("OpenStream err=%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := service.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown err=%v", err)
	}
	if stream, ok := service.broker.Get("req-1"); ok && stream != nil {
		t.Fatalf("stream still present after Shutdown: %+v", stream)
	}
}
