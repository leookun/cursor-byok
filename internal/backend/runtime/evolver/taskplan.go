// taskplan.go compiles Evolution proposals into an executable next-slice plan (ADR-038).
//
// The living loop must not stop at free-text recommendations. This stage turns
// Propose() output into a deterministic TaskPlan that AOS Leader / agents can
// consume as the next Research?ADR?Implementation slice.
package evolver

import (
	"fmt"
	"strings"
	"time"
)

// EvolutionTask is one concrete, ordered work item derived from a Proposal.
type EvolutionTask struct {
	ID           string `json:"id"`
	Role         string `json:"role"` // research | architecture | implementation | test | docs
	Title        string `json:"title"`
	Category     string `json:"category"`
	Priority     int    `json:"priority"`
	Rationale    string `json:"rationale"`
	SuggestedADR string `json:"suggestedADR,omitempty"`
	Acceptance   string `json:"acceptance"`
	// Action is the allowlisted autonomous action for this task (ADR-039).
	// auto-writeback | run-tests | scaffold-adr | scaffold-research | scaffold-report | bounded-code-fix | manual
	Action string `json:"action"`
}

// EvolutionTaskPlan is the executable next-slice plan for the autonomous loop.
type EvolutionTaskPlan struct {
	GeneratedAt time.Time       `json:"generatedAt"`
	Source      string          `json:"source"` // "proposal"
	Summary     string          `json:"summary"`
	Tasks       []EvolutionTask `json:"tasks"`
}

// CompileTaskPlan converts a Proposal into a bounded, deterministic task plan.
// It never mutates the repo; callers may feed the plan into AOS planning or reports.
func (e *Evolver) CompileTaskPlan(proposal *Proposal) *EvolutionTaskPlan {
	plan := &EvolutionTaskPlan{
		GeneratedAt: time.Now().UTC(),
		Source:      "proposal",
	}
	if proposal == nil || len(proposal.Priorities) == 0 {
		plan.Summary = "No proposal items; continue roadmap-driven research."
		plan.Tasks = []EvolutionTask{{
			ID:         "task-1",
			Role:       "research",
			Title:      "Plan next evolution cycle from roadmap priorities",
			Category:   "implement",
			Priority:   1,
			Rationale:  "Empty proposal still requires proactive next-slice planning.",
			Acceptance: "Next ADR/research/report slice identified with evidence path.",
			Action:     "manual",
		}}
		return plan
	}

	// Keep the plan actionable: top N items only.
	const maxTasks = 5
	for i, item := range proposal.Priorities {
		if i >= maxTasks {
			break
		}
		role := roleForProposalCategory(item.Category, item.Title)
		task := EvolutionTask{
			ID:         fmt.Sprintf("task-%d", i+1),
			Role:       role,
			Title:      strings.TrimSpace(item.Title),
			Category:   item.Category,
			Priority:   item.Priority,
			Rationale:  strings.TrimSpace(item.Rationale),
			Acceptance: acceptanceForRole(role, item.Title),
			Action:     actionForTask(role, item.Title),
		}
		if role == "architecture" || strings.Contains(strings.ToLower(item.Title), "adr") {
			task.SuggestedADR = "next"
		}
		plan.Tasks = append(plan.Tasks, task)
	}
	plan.Summary = fmt.Sprintf("Compiled %d executable task(s) from proposal priorities", len(plan.Tasks))
	return plan
}

// addMetricRemediationTasks carries runtime regressions into the existing plan
// while keeping proposal order and the same five-task bound.
func (e *Evolver) addMetricRemediationTasks(plan *EvolutionTaskPlan, report *RuntimeMetricReport) {
	if plan == nil || len(plan.Tasks) >= 5 {
		return
	}
	remediations := e.BuildMetricRemediationTasks(report)
	added := false
	for _, remediation := range remediations {
		if len(plan.Tasks) >= 5 {
			break
		}
		duplicate := false
		for _, task := range plan.Tasks {
			if remediation.ID != "" && remediation.ID == task.ID ||
				remediation.Action == task.Action && remediation.Title == task.Title {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		plan.Tasks = append(plan.Tasks, remediation)
		added = true
	}
	if !added {
		return
	}
	plan.Summary = fmt.Sprintf("Compiled %d executable task(s) from proposal priorities", len(plan.Tasks))
}

func roleForProposalCategory(category, title string) string {
	c := strings.ToLower(strings.TrimSpace(category))
	t := strings.ToLower(title)
	switch c {
	case "research":
		return "research"
	case "optimize":
		return "implementation"
	case "implement":
		if strings.Contains(t, "adr") || strings.Contains(t, "architecture") {
			return "architecture"
		}
		return "implementation"
	case "fix":
		if strings.Contains(t, "test") || strings.Contains(t, "package") {
			return "test"
		}
		if strings.Contains(t, "index") || strings.Contains(t, "handbook") || strings.Contains(t, "report") ||
			strings.Contains(t, "docguard") || strings.Contains(t, "foundation") || strings.Contains(t, "catalog") ||
			strings.Contains(t, "research note") || strings.Contains(t, "adr-") {
			return "docs"
		}
		return "implementation"
	default:
		return "implementation"
	}
}

func acceptanceForRole(role, title string) string {
	switch role {
	case "research":
		return "Research note written under docs/research/ and indexed in chapter 24."
	case "architecture":
		return "ADR written under docs/adr/ and indexed in chapter 28."
	case "test":
		return "Targeted package tests pass; CI gate green."
	case "docs":
		return "Handbook indexes/reports updated; evolver diagnose warnings for this drift cleared."
	default:
		return "Implementation lands with tests + report evidence; no production nil ChannelService."
	}
}

// FormatTaskPlan returns a human-readable plan for reports / AOS advisory prompts.
func (p *EvolutionTaskPlan) FormatTaskPlan() string {
	if p == nil {
		return "No task plan.\n"
	}
	var b strings.Builder
	b.WriteString("=== Evolution Task Plan ===\n")
	b.WriteString(p.Summary)
	b.WriteString("\n")
	for _, task := range p.Tasks {
		b.WriteString(fmt.Sprintf("  %s [%s/%s] %s\n", task.ID, task.Role, task.Category, task.Title))
		if task.Rationale != "" {
			b.WriteString("    why: " + task.Rationale + "\n")
		}
		b.WriteString("    accept: " + task.Acceptance + "\n")
	}
	return b.String()
}

// AdvisoryText renders the plan as compact Leader planning context.
func (p *EvolutionTaskPlan) AdvisoryText() string {
	if p == nil || len(p.Tasks) == 0 {
		return ""
	}
	var parts []string
	parts = append(parts, "Next evolution slice (compiled from Evolver proposal):")
	for _, task := range p.Tasks {
		parts = append(parts, fmt.Sprintf("- %s (%s): %s", task.ID, task.Role, task.Title))
	}
	return strings.Join(parts, "\n")
}

func actionForTask(role, title string) string {
	t := strings.ToLower(title)
	switch role {
	case "docs":
		// Index/foundation/report catalog repairs are allowlisted.
		if strings.Contains(t, "index") || strings.Contains(t, "handbook") || strings.Contains(t, "report") ||
			strings.Contains(t, "foundation") || strings.Contains(t, "catalog") || strings.Contains(t, "docguard") ||
			strings.Contains(t, "research note") || strings.Contains(t, "adr") {
			return "auto-writeback"
		}
		return "manual"
	case "test":
		return "run-tests"
	case "research":
		return "scaffold-research"
	case "architecture":
		return "scaffold-adr"
	case "implementation":
		// Bounded deterministic code recipes under allowlisted roots (ADR-041).
		return "bounded-code-fix"
	default:
		// Fallbacks for free-form titles.
		titleLower := t
		if strings.Contains(titleLower, "research") {
			return "scaffold-research"
		}
		if strings.Contains(titleLower, "adr") || strings.Contains(titleLower, "architecture") {
			return "scaffold-adr"
		}
		if strings.Contains(titleLower, "benchmark report") || strings.Contains(titleLower, "phase report") {
			return "scaffold-report"
		}
		if strings.Contains(titleLower, "implement") || strings.Contains(titleLower, "code") || strings.Contains(titleLower, "runtime") {
			return "bounded-code-fix"
		}
		return "manual"
	}
}
