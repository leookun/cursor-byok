// Package aos implements the AOS (AI Organization System) virtual model.
//
// AOS simulates a real software R&D organization: a Leader (Tech Lead + Architect)
// coordinates Members (Prompt + ModelAdapter) through a shared Workspace, with
// Sprint iteration and Discussion-based collaboration.
//
// Design: docs/handbook/05-09, ADR-013.
// Research: docs/research/aos-organization-runtime.md.
package aos

import (
	"fmt"
	"sync"
	"strings"
)

// ModelID is the virtual model identifier exposed to Cursor.
const ModelID = "aos"

// DisplayName is the user-facing name shown in Cursor model list.
const DisplayName = "AOS"

// Execution modes for AOS member tasks.
const (
	// ExecutionModeInternal is the legacy direct-adapter mode: members execute via callAdapter.
	ExecutionModeInternal = "internal"
	// ExecutionModeCursorTask (default): members execute via Cursor-native Task tool call spawn.
	ExecutionModeCursorTask = "cursor_task"
)

// LeaderConfig defines the team leader (always present, uses the smartest model).
type LeaderConfig struct {
	AdapterID string `yaml:"adapterID" json:"adapterID"`
}

// MemberConfig defines a team member (Prompt + ModelAdapter binding).
type MemberConfig struct {
	ID           string   `yaml:"id" json:"id"`
	Name         string   `yaml:"name" json:"name"`
	AdapterID    string   `yaml:"adapterID" json:"adapterID"`
	SystemPrompt string   `yaml:"systemPrompt" json:"systemPrompt"`
	Tags         []string `yaml:"tags" json:"tags"`
	Capabilities []string `yaml:"capabilities" json:"capabilities"`
	MaxContext   int      `yaml:"maxContext" json:"maxContext"`
	Multimodal   bool     `yaml:"multimodal" json:"multimodal"`
	Cost         string   `yaml:"cost" json:"cost"`
	Speed        string   `yaml:"speed" json:"speed"`
}

// WorkflowConfig defines how tasks are scheduled.
type WorkflowConfig struct {
	Mode        string `yaml:"mode" json:"mode"`
	MaxParallel int    `yaml:"maxParallel" json:"maxParallel"`
	Timeout     string `yaml:"timeout" json:"timeout"`
	Retry       int    `yaml:"retry" json:"retry"`
}

// SprintConfig controls iterative review cycles.
type SprintConfig struct {
	MaxIterations int `yaml:"maxIterations" json:"maxIterations"`
}

// TeamProfile is the complete team definition.
type TeamProfile struct {
	Leader        LeaderConfig    `yaml:"leader" json:"leader"`
	Members       []MemberConfig  `yaml:"members" json:"members"`
	Workflow      WorkflowConfig  `yaml:"workflow" json:"workflow"`
	Sprints       SprintConfig    `yaml:"sprints" json:"sprints"`
	ExecutionMode string          `yaml:"executionMode,omitempty" json:"executionMode,omitempty"`
}

// Task represents a unit of work assigned to a member.
type Task struct {
	ID           string   `json:"id"`
	Role         string   `json:"role"`
	Description  string   `json:"description"`
	AssigneeID   string   `json:"assigneeID"`
	Dependencies []string `json:"dependencies"`
	Priority     string   `json:"priority"`
	Status       string   `json:"status"`
	Result       string   `json:"result"`
}

// TaskPlan is the Leader output after requirement analysis.
type TaskPlan struct {
	Tasks       []Task `json:"tasks"`
	Architecture string `json:"architecture"`
}

// DiscussionThread is a conversation between members.
type DiscussionThread struct {
	ID       string           `json:"id"`
	Subject  string           `json:"subject"`
	Messages []DiscussionMsg `json:"messages"`
}

// DiscussionMsg is a single message in a discussion thread.
type DiscussionMsg struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Workspace is the shared state for the entire team.
type Workspace struct {
	mu              sync.Mutex          `json:"-"`
	SessionID       string              `json:"sessionID"`
	Requirement     string              `json:"requirement"`
	Architecture    string              `json:"architecture"`
	Tasks           []Task              `json:"tasks"`
	Decisions       []string            `json:"decisions"`
	Implementations map[string]string   `json:"implementations"`
	Discussions     []DiscussionThread  `json:"discussions"`
	Artifacts       []string            `json:"artifacts"`
}

// NewWorkspace creates a fresh workspace for a session.
func NewWorkspace(sessionID string) *Workspace {
	return &Workspace{
		SessionID:       sessionID,
		Implementations: make(map[string]string),
	}
}

// AddDiscussion adds a discussion message to the workspace.
func (ws *Workspace) AddDiscussion(threadID, subject, from, to, msgType, content string) {
	if ws == nil {
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for i := range ws.Discussions {
		if ws.Discussions[i].ID == threadID {
			ws.Discussions[i].Messages = append(ws.Discussions[i].Messages, DiscussionMsg{
				From: from, To: to, Type: msgType, Content: content,
			})
			return
		}
	}
	ws.Discussions = append(ws.Discussions, DiscussionThread{
		ID:      threadID,
		Subject: subject,
		Messages: []DiscussionMsg{{From: from, To: to, Type: msgType, Content: content}},
	})
}

// SetTaskResult records a member output for a task.
func (ws *Workspace) SetTaskResult(taskID, memberID, result string) {
	if ws == nil {
		return
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	for i := range ws.Tasks {
		if ws.Tasks[i].ID == taskID {
			ws.Tasks[i].Status = "done"
			ws.Tasks[i].Result = result
		}
	}
	ws.Implementations[memberID] = result
}

// MembersInfo returns a formatted description of all members for the Leader prompt,
// with explicit tag-based routing instructions.
//
// IMPORTANT: this is the compact "roster card" the Leader reads on every Sprint
// dispatch. It deliberately omits each member's full SystemPrompt so the Leader
// can route by short tags instead of re-reading long role descriptions every turn.
// Tags are populated either by the user or by RecognizeMembers() (Leader reads
// each member's name+prompt once, then fixes the tags).
func (tp *TeamProfile) MembersInfo() string {
	if tp == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## Team Members (assign tasks by Tags)\n")
	sb.WriteString("Use member Tags to match tasks — same tag = best fit.\n")
	for _, m := range tp.Members {
		sb.WriteString(fmt.Sprintf("\n### %s (%s)\n", m.Name, m.ID))
		sb.WriteString(fmt.Sprintf("- Adapter: %s\n", m.AdapterID))
		if len(m.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("- Tags: %s <- match tasks here\n", strings.Join(m.Tags, ", ")))
		} else {
			sb.WriteString("- Tags: (not yet recognized — run \"Recognize Members\" before starting work)\n")
		}
		if len(m.Capabilities) > 0 {
			sb.WriteString(fmt.Sprintf("- Capabilities: %s\n", strings.Join(m.Capabilities, ", ")))
		}
		if m.MaxContext > 0 {
			sb.WriteString(fmt.Sprintf("- Context Window: %d\n", m.MaxContext))
		}
		if m.Multimodal {
			sb.WriteString("- Multimodal: yes\n")
		}
	}
	return sb.String()
}

// FindMember returns a member by ID or name (case-insensitive).
// Returns false when id is empty or no match found.
func (tp *TeamProfile) FindMember(id string) (MemberConfig, bool) {
	id = strings.TrimSpace(id)
	if id == "" || tp == nil {
		return MemberConfig{}, false
	}
	for _, m := range tp.Members {
		if m.ID == id || strings.EqualFold(m.Name, id) {
			return m, true
		}
	}
	return MemberConfig{}, false
}

// ResolveMemberByTags finds the best member for a task based on tag matching.
// Priority: exact tag match > partial tag match > first available member.
// Returns the matched member and a score (higher = better match).
func (tp *TeamProfile) ResolveMemberByTags(taskTags []string) (MemberConfig, int, bool) {
	if tp == nil || len(tp.Members) == 0 {
		return MemberConfig{}, 0, false
	}
	if len(taskTags) == 0 {
		return tp.Members[0], 0, true
	}

	bestScore := -1
	bestMember := tp.Members[0]
	for _, m := range tp.Members {
		score := 0
		for _, tt := range taskTags {
			ttLower := strings.ToLower(strings.TrimSpace(tt))
			for _, mt := range m.Tags {
				mtLower := strings.ToLower(strings.TrimSpace(mt))
				if ttLower == mtLower {
					score += 2 // exact match
				} else if strings.Contains(mtLower, ttLower) || strings.Contains(ttLower, mtLower) {
					score += 1 // partial match
				}
			}
			// Also check member name as a fallback tag
			nameLower := strings.ToLower(strings.TrimSpace(m.Name))
			if strings.Contains(nameLower, ttLower) || strings.Contains(ttLower, nameLower) {
				score += 1
			}
		}
		if score > bestScore {
			bestScore = score
			bestMember = m
		}
	}
	if bestScore <= 0 {
		return MemberConfig{}, 0, false // no valid match, caller must handle
	}
	return bestMember, bestScore, true
}
