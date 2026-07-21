package aos

import "sync"

// lastTraceStore keeps the most recent finalized AOS execution summary in-process
// for minimal UI observability (Phase 26f / Phase 9 slice).
// ponytail: single slot only; upgrade to ring buffer if multi-session UI needs it.
var (
	lastTraceMu      sync.RWMutex
	lastTraceSummary string
	lastTraceMeta    map[string]string
)

// RememberLastTrace stores a compact snapshot after AOS Execute finishes.
func RememberLastTrace(trace *ExecutionTrace) {
	if trace == nil {
		return
	}
	summary := trace.Summary()
	meta := trace.Metadata()
	lastTraceMu.Lock()
	lastTraceSummary = summary
	lastTraceMeta = meta
	lastTraceMu.Unlock()
}

// LastTraceSnapshot is a UI-friendly DTO for the last AOS turn.
type LastTraceSnapshot struct {
	Summary  string            `json:"summary"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// GetLastTraceSnapshot returns the last remembered AOS trace (may be empty).
func GetLastTraceSnapshot() LastTraceSnapshot {
	lastTraceMu.RLock()
	defer lastTraceMu.RUnlock()
	metaCopy := make(map[string]string, len(lastTraceMeta))
	for k, v := range lastTraceMeta {
		metaCopy[k] = v
	}
	return LastTraceSnapshot{
		Summary:  lastTraceSummary,
		Metadata: metaCopy,
	}
}
