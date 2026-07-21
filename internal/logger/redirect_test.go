package logger

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"log/slog"
)

// TestRedirectStdLog_ForwardsToSlog verifies that stdlib log.Printf output
// is forwarded through the slog facade as an INFO entry, preserving the
// original message text (e.g. "[Pet] foo").
func TestRedirectStdLog_ForwardsToSlog(t *testing.T) {
	// Ensure Init's sync.Once is consumed first so it doesn't reset our
	// test handler when RedirectStdLog calls Init internally.
	Init()
	var buf bytes.Buffer
	restore := installTestSlogHandler(&buf, slog.LevelInfo)
	defer restore()

	// Reset stdlib log flags so RedirectStdLog can take effect cleanly.
	log.SetFlags(0)
	defer log.SetFlags(0)

	RedirectStdLog()
	defer RestoreStdLog()

	log.Printf("[Pet] test message")

	out := buf.String()
	if !strings.Contains(out, "test message") {
		t.Fatalf("slog did not receive stdlib log message: %q", out)
	}
	if !strings.Contains(out, "[Pet] test message") {
		t.Fatalf("slog entry did not preserve the full message: %q", out)
	}
	if !strings.Contains(out, "INFO") {
		t.Fatalf("slog entry was not INFO level: %q", out)
	}
}

// TestRedirectStdLog_RestoreReverts verifies RestoreStdLog undoes the redirect
// so subsequent log.Printf calls do NOT hit the slog buffer.
func TestRedirectStdLog_RestoreReverts(t *testing.T) {
	var buf bytes.Buffer
	restore := installTestSlogHandler(&buf, slog.LevelInfo)
	defer restore()

	log.SetFlags(0)
	defer log.SetFlags(0)

	RedirectStdLog()
	RestoreStdLog()

	// After restore, log output should go to the original (stderr) not buf.
	buf.Reset()
	log.Printf("should-not-appear-in-slog")

	if strings.Contains(buf.String(), "should-not-appear-in-slog") {
		t.Fatalf("RestoreStdLog did not revert stdlog redirect: %q", buf.String())
	}
}

// TestRedirectStdLog_StressQuickStress ensures repeated redirect/restore cycles
// don't leak state across goroutines (best-effort sanity check).
func TestRedirectStdLog_StressQuickStress(t *testing.T) {
	Init()
	var buf bytes.Buffer
	restore := installTestSlogHandler(&buf, slog.LevelInfo)
	defer restore()

	log.SetFlags(0)
	defer log.SetFlags(0)

	for i := 0; i < 25; i++ {
		RedirectStdLog()
		log.Printf("[iter] message %d", i)
		RestoreStdLog()
	}
	out := buf.String()
	if !strings.Contains(out, "[iter] message 0") {
		t.Fatalf("expected iter 0 in slog output: %q", out)
	}
	if !strings.Contains(out, "[iter] message 24") {
		t.Fatalf("expected iter 24 in slog output: %q", out)
	}
}

// installTestSlogHandler installs a text-handler writing to buf at the given
// level as the package default slog, returning a restore func.
func installTestSlogHandler(buf *bytes.Buffer, level slog.Level) func() {
	prev := slog.Default()
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	return func() { slog.SetDefault(prev) }
}
