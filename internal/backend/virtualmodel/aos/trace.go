package aos

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TraceNode records a single step in the AOS execution tree.
type TraceNode struct {
	Role          string        `json:"role"`   // leader, backend, frontend, testing...
	Action        string        `json:"action"` // planning, execution, review, merge, spawn, resolve
	ExecutionMode string        `json:"executionMode,omitempty"` // internal | cursor_task (Phase 26f)
	Spawned       bool          `json:"spawned,omitempty"`       // true if member was spawned via Task tool call
	AdapterID     string        `json:"adapterID"`
	TaskID        string        `json:"taskID,omitempty"`
	ExecID        string        `json:"execID,omitempty"`        // spawn execution ID (Phase 26f)
	Prompt        string        `json:"prompt,omitempty"`
	Response      string        `json:"response,omitempty"`
	Tokens        int           `json:"tokens,omitempty"`
	Duration      time.Duration `json:"durationMS"`
	Status        string        `json:"status"` // ok, error
	Error         string        `json:"error,omitempty"`
	StartTime     time.Time     `json:"startTime"`
}

// ExecutionTrace is the full execution tree for one AOS turn.
type ExecutionTrace struct {
	mu               sync.Mutex  `json:"-"`
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
}

// NewExecutionTrace creates a trace for a new AOS turn.
func NewExecutionTrace(sessionID, requestID, modelID string) *ExecutionTrace {
	return &ExecutionTrace{
		SessionID: sessionID,
		RequestID: requestID,
		TurnID:    sessionID,
		ModelID:   modelID,
		StartTime: time.Now(),
	}
}

// AddNode records a completed step in the trace.
func (t *ExecutionTrace) AddNode(node TraceNode) {
	if t == nil {
		return
	}
	if node.StartTime.IsZero() {
		node.StartTime = time.Now()
	}
	if node.Duration == 0 {
		node.Duration = time.Since(node.StartTime)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Nodes = append(t.Nodes, node)
	t.TotalTokens += node.Tokens
	if node.Status == "ok" {
		t.TasksDone++
	}
}

// AddUsage records adapter token usage. It is safe for concurrent sprint tasks.
func (t *ExecutionTrace) AddUsage(promptTokens, completionTokens int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.PromptTokens += promptTokens
	t.CompletionTokens += completionTokens
	t.TotalTokens = t.PromptTokens + t.CompletionTokens
}

// Usage returns the accumulated prompt, completion, and total token counts.
func (t *ExecutionTrace) Usage() (int, int, int) {
	if t == nil {
		return 0, 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.PromptTokens, t.CompletionTokens, t.TotalTokens
}

// Finalize marks the trace as complete.
func (t *ExecutionTrace) Finalize() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.EndTime = time.Now()
	t.mu.Unlock()
}

// Metadata returns a compact, stable observation map for benchmark consumers.
func (t *ExecutionTrace) Metadata() map[string]string {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	sessionID := t.SessionID
	sprints := t.Sprints
	tasksTotal := t.TasksTotal
	tasksDone := t.TasksDone
	promptTokens := t.PromptTokens
	completionTokens := t.CompletionTokens
	totalTokens := t.TotalTokens
	nodes := append([]TraceNode(nil), t.Nodes...)
	startTime := t.StartTime
	endTime := t.EndTime
	t.mu.Unlock()

	metadata := map[string]string{
		"aos.workflow":         "aos",
		"aos.phases":           strings.Join([]string{"planning", "sprint", "review", "merge"}, ","),
		"aos.sessionID":        sessionID,
		"aos.sprints":          strconv.Itoa(sprints),
		"aos.tasksTotal":       strconv.Itoa(tasksTotal),
		"aos.tasksDone":        strconv.Itoa(tasksDone),
		"aos.promptTokens":     strconv.Itoa(promptTokens),
		"aos.completionTokens": strconv.Itoa(completionTokens),
		"aos.totalTokens":      strconv.Itoa(totalTokens),
	}
	if !startTime.IsZero() && !endTime.IsZero() {
		metadata["aos.durationMS"] = strconv.FormatInt(endTime.Sub(startTime).Milliseconds(), 10)
	}

	stageDurations := map[string]int64{}
	stageStatuses := map[string]string{}
	stageSeen := map[string]bool{}
	for _, node := range nodes {
		phase := node.Action
		if phase == "execution" {
			phase = "sprint"
		}
		if phase != "planning" && phase != "sprint" && phase != "review" && phase != "merge" {
			continue
		}
		stageSeen[phase] = true
		stageDurations[phase] += node.Duration.Milliseconds()
		if node.Status == "error" {
			stageStatuses[phase] = "error"
		} else if stageStatuses[phase] == "" {
			stageStatuses[phase] = "ok"
		}
	}
	for _, phase := range []string{"planning", "sprint", "review", "merge"} {
		if !stageSeen[phase] {
			continue
		}
		metadata["aos.phase."+phase+".observed"] = "true"
		metadata["aos.phase."+phase+".status"] = stageStatuses[phase]
		metadata["aos.phase."+phase+".durationMS"] = strconv.FormatInt(stageDurations[phase], 10)
	}
	return metadata
}

// Summary returns a human-readable summary of the trace.
func (t *ExecutionTrace) Summary() string {
	if t == nil || len(t.Nodes) == 0 {
		return "empty trace"
	}
	s := t.SessionID + "\n"
	for _, n := range t.Nodes {
		status := n.Status
		if status == "" {
			status = "?"
		}
		s += "  " + n.Role + "/" + n.Action + " " + status + " " + n.Duration.String()
		if n.Tokens > 0 {
			s += " " + itoa(n.Tokens) + "t"
		}
		s += "\n"
	}
	return s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

type traceContextKey struct{}

func withExecutionTrace(ctx context.Context, trace *ExecutionTrace) context.Context {
	return context.WithValue(ctx, traceContextKey{}, trace)
}

func executionTraceFromContext(ctx context.Context) *ExecutionTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(traceContextKey{}).(*ExecutionTrace)
	return trace
}
