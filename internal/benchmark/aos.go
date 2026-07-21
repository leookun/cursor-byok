// Package benchmark contains benchmark helpers for the AOS workflow.
package benchmark

import (
	"context"
	"strconv"
	"strings"

	virtualmodel "cursor/internal/backend/virtualmodel"
)

// AOSPhases is the stable phase vocabulary emitted by the AOS trace.
var AOSPhases = []string{"planning", "sprint", "review", "merge"}

// AOSPhaseResult is one observed AOS workflow phase.
type AOSPhaseResult struct {
	Name       string
	Observed   bool
	Status     string
	DurationMS int64
}

// AOSReport combines the standard benchmark report with AOS phase observations.
type AOSReport struct {
	Report       *Report
	Phases       []AOSPhaseResult
	Sprints      int
	TasksTotal   int
	TasksDone    int
	QualityScore float64
}

// NewAOSSuite creates the canonical single-turn AOS benchmark suite.
func NewAOSSuite(requirement string) *Suite {
	suite := NewSuite("aos-workflow")
	suite.AddTask(Task{
		Name:        "aos.workflow",
		Messages:    []virtualmodel.Message{{Role: "user", Content: strings.TrimSpace(requirement)}},
		Description: "AOS Leader planning, Sprint execution, Review, and Merge.",
	})
	return suite
}

// RunAOS runs the canonical AOS suite through the existing VirtualModel interface.
func RunAOS(ctx context.Context, model virtualmodel.VirtualModel, requirement string) *AOSReport {
	return SummarizeAOS(NewAOSSuite(requirement).Run(ctx, model))
}

// SummarizeAOS extracts AOS observations from a standard benchmark report.
func SummarizeAOS(report *Report) *AOSReport {
	out := &AOSReport{Report: report}
	if report == nil || len(report.Results) == 0 {
		return out
	}
	metadata := report.Results[0].Metadata
	out.Sprints = parseInt(metadata["aos.sprints"])
	out.TasksTotal = parseInt(metadata["aos.tasksTotal"])
	out.TasksDone = parseInt(metadata["aos.tasksDone"])
	out.QualityScore = computeAOSQualityScore(out)
	for _, phase := range AOSPhases {
		result := AOSPhaseResult{
			Name:       phase,
			Observed:   metadata["aos.phase."+phase+".observed"] == "true",
			Status:     metadata["aos.phase."+phase+".status"],
			DurationMS: parseInt64(metadata["aos.phase."+phase+".durationMS"]),
		}
		out.Phases = append(out.Phases, result)
	}
	return out
}

// PhasesComplete reports whether all required AOS workflow phases were observed.
func (r *AOSReport) PhasesComplete() bool {
	if r == nil || len(r.Phases) != len(AOSPhases) {
		return false
	}
	for _, phase := range r.Phases {
		if !phase.Observed || phase.Status != "ok" {
			return false
		}
	}
	return true
}

func parseInt(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

func parseInt64(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

// computeAOSQualityScore derives a deterministic 0..1 score from phase health.
// planning/sprint/review/merge each contribute 0.25 when observed with status=ok.
func computeAOSQualityScore(report *AOSReport) float64 {
	if report == nil || len(report.Phases) == 0 {
		return 0
	}
	ok := 0
	for _, phase := range report.Phases {
		if phase.Observed && phase.Status == "ok" {
			ok++
		}
	}
	return float64(ok) / float64(len(AOSPhases))
}

// QualitySummary returns a compact quality line for reports and evolution evidence.
func (r *AOSReport) QualitySummary() string {
	if r == nil {
		return "aos quality: n/a"
	}
	return "aos quality: " + strconv.FormatFloat(r.QualityScore, 'f', 2, 64) +
		" phases=" + strconv.Itoa(len(r.Phases)) +
		" complete=" + strconv.FormatBool(r.PhasesComplete())
}
