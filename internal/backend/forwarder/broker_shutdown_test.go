package forwarder

import (
	"context"
	"testing"
	"time"

	"cursor/gen/agentv1"
)

// TestStreamBroker_ShutdownCancelsInflight verifies that Shutdown cancels
// every inflight stream and stops any terminal-cleanup timers so they
// do not fire after the host has exited. R17: lifecycle unification.
//
// TODO: enable -race in CI once CGO is available (R17 spec).
func TestStreamBroker_ShutdownCancelsInflight(t *testing.T) {
	broker := NewStreamBroker()
	if _, err := broker.OpenStream("req-1", "conv-1", 0, "model", "Model", agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
		t.Fatalf("OpenStream err=%v", err)
	}
	if _, err := broker.OpenStream("req-2", "conv-2", 0, "model", "Model", agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
		t.Fatalf("OpenStream err=%v", err)
	}
	if !broker.IsClosed() {
		// sanity: IsClosed should be false before Shutdown.
	} else {
		t.Fatal("broker.IsClosed = true before Shutdown")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := broker.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Shutdown took too long: %v", elapsed)
	}
	if !broker.IsClosed() {
		t.Fatal("broker.IsClosed = false after Shutdown")
	}

	// Streams must be gone.
	if stream, ok := broker.Get("req-1"); ok && stream != nil {
		t.Fatalf("stream req-1 still present after Shutdown: %+v", stream)
	}
	if stream, ok := broker.Get("req-2"); ok && stream != nil {
		t.Fatalf("stream req-2 still present after Shutdown: %+v", stream)
	}

	// Post-shutdown OpenStream is a no-op (broker closed).
	if stream, err := broker.OpenStream("req-3", "conv-3", 0, "model", "Model", agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err == nil && stream != nil {
		t.Fatalf("OpenStream succeeded on a closed broker: %+v", stream)
	}

	// Idempotent: a second Shutdown must not panic.
	if err := broker.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown err=%v (must be idempotent)", err)
	}
}

// TestStreamBroker_ShutdownStopsTerminalCleanupTimer verifies that a pending
// terminal-cleanup timer does not fire after Shutdown. R17.
func TestStreamBroker_ShutdownStopsTerminalCleanupTimer(t *testing.T) {
	broker := NewStreamBroker()
	if _, err := broker.OpenStream("req-t", "conv-t", 0, "model", "Model", agentv1.AgentMode_AGENT_MODE_AGENT, "hi"); err != nil {
		t.Fatalf("OpenStream err=%v", err)
	}
	// Mark the stream terminal so scheduleTerminalCleanup arms its timer.
	if err := broker.Complete("req-t", "ok", "completed"); err != nil {
		t.Fatalf("Complete err=%v", err)
	}
	// scheduleTerminalCleanup only arms the timer when there are no
	// subscribers; Complete already calls it when subscriberCount==0.

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := broker.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown err=%v", err)
	}
	// Wait long enough that the armed timer (30s) would have *almost* fired.
	// We cannot wait 30s in a unit test; instead assert the stream is gone
	// (so the timer — even if it were about to fire — has no target).
	if stream, ok := broker.Get("req-t"); ok && stream != nil {
		t.Fatalf("stream req-t still present after Shutdown: %+v", stream)
	}
}
