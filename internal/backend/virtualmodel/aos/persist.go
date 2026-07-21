package aos

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"cursor/internal/appdata"
)

// TraceFile is the on-disk representation of a persisted AOS execution trace.
// It mirrors ExecutionTrace but has an explicit UserPrompt field so a later
// replay can re-trigger AOS with the original input (Phase 9 Replay slice).
//
// ponytail: single flat file per session under telemetry/traces/{sessionID}.json.
// No indexing/ring buffer; the UI only lists by session ID. Upgrade to a
// directory scan + manifest if multi-session browsing is needed.
type TraceFile struct {
	SessionID        string      `json:"sessionID"`
	RequestID        string      `json:"requestID"`
	TurnID           string      `json:"turnID"`
	ModelID          string      `json:"modelID"`
	StartTime        time.Time   `json:"startTime"`
	EndTime          time.Time   `json:"endTime"`
	Nodes            []TraceNode `json:"nodes"`
	Sprints          int         `json:"sprints"`
	TasksTotal       int         `json:"tasksTotal"`
	TasksDone        int         `json:"tasksDone"`
	PromptTokens     int         `json:"promptTokens"`
	CompletionTokens int         `json:"completionTokens"`
	TotalTokens      int         `json:"totalTokens"`
	// UserPrompt is the requirement text captured at AOS Execute start.
	// Stored so GetAOSExecutionTree / Replay can surface the original input.
	UserPrompt string `json:"userPrompt,omitempty"`
}

// traceDirResolver resolves the directory where trace files live.
// It is a variable (not a func) so tests can point it at a temp dir.
//
// ponytail: default uses appdata.DataRootPath(); no env override by design.
var traceDirResolver = func() string {
	return filepath.Join(appdata.DataRootPath(), "telemetry", "traces")
}

// tracesDir returns the directory where trace files live.
func tracesDir() string {
	return traceDirResolver()
}

// SaveTrace persists the trace to disk under telemetry/traces/{sessionID}.json.
// It is fail-open: any error is logged but never returned so callers (AOS
// Execute) never break on a persistence failure.
func SaveTrace(trace *ExecutionTrace, userPrompt string) {
	if trace == nil {
		return
	}
	// Snapshot under lock to avoid racing with concurrent AddNode.
	trace.mu.Lock()
	file := TraceFile{
		SessionID:        trace.SessionID,
		RequestID:        trace.RequestID,
		TurnID:           trace.TurnID,
		ModelID:          trace.ModelID,
		StartTime:        trace.StartTime,
		EndTime:          trace.EndTime,
		Nodes:            append([]TraceNode(nil), trace.Nodes...),
		Sprints:          trace.Sprints,
		TasksTotal:       trace.TasksTotal,
		TasksDone:       trace.TasksDone,
		PromptTokens:     trace.PromptTokens,
		CompletionTokens: trace.CompletionTokens,
		TotalTokens:      trace.TotalTokens,
		UserPrompt:       userPrompt,
	}
	trace.mu.Unlock()

	dir := tracesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	// Sanitize sessionID into a filename (it is already a safe "aos-<unixnano>").
	name := filepath.Join(dir, trace.SessionID+".json")
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return
	}
	// Best-effort atomic-ish write: write then ignore error.
	_ = os.WriteFile(name, data, 0o644)
}

// LoadTrace reads a persisted trace by session ID.
// Returns (nil, nil) when the file does not exist so callers can fall back
// gracefully (e.g. UI shows "no trace").
func LoadTrace(sessionID string) (*TraceFile, error) {
	if sessionID == "" {
		return nil, nil
	}
	path := filepath.Join(tracesDir(), sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var file TraceFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	return &file, nil
}