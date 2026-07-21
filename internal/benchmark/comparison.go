// comparison.go implements A/B comparison for Virtual Models (Phase 9).
// Runs the same Suite against two models and produces a head-to-head comparison.
// Design: extends ADR-020 benchmark framework.
package benchmark

import (
	"context"
	"fmt"
	"time"

	virtualmodel "cursor/internal/backend/virtualmodel"
)

// ComparisonConfig configures an A/B comparison run.
type ComparisonConfig struct {
	LabelA string // display name for model A (e.g., "MOA")
	LabelB string // display name for model B (e.g., "GPT-4o")
}

// ComparisonReport holds the results of running the same suite against two models.
type ComparisonReport struct {
	SuiteName    string
	LabelA       string
	LabelB       string
	ReportA      *Report
	ReportB      *Report
	StartTime    time.Time
	EndTime      time.Time
	Winner       string // "A", "B", or "tie"
	Diff         ComparisonDiff
}

// ComparisonDiff shows the delta between A and B.
type ComparisonDiff struct {
	LatencyDeltaMS   int64   // B - A (negative = A faster)
	TokenDelta       int     // B - A (negative = A fewer tokens)
	SuccessDelta     int     // B - A (negative = A more successes)
	LatencyWinner    string  // "A", "B", or "tie"
	TokenWinner      string  // "A", "B", or "tie"
	SuccessWinner    string  // "A", "B", or "tie"
}

// Compare runs the given suite against both models A and B and returns a ComparisonReport.
// Models are run sequentially (A first, then B) to avoid resource contention.
func Compare(ctx context.Context, suite *Suite, modelA, modelB virtualmodel.VirtualModel, cfg ComparisonConfig) *ComparisonReport {
	report := &ComparisonReport{
		SuiteName: suite.Name,
		LabelA:    cfg.LabelA,
		LabelB:    cfg.LabelB,
		StartTime: time.Now(),
	}

	// Run model A
	report.ReportA = suite.Run(ctx, modelA)

	// Run model B
	report.ReportB = suite.Run(ctx, modelB)

	report.EndTime = time.Now()
	report.Diff = computeDiff(report.ReportA.Summary, report.ReportB.Summary)
	report.Winner = determineWinner(report.Diff)

	return report
}

// computeDiff calculates the delta between two summaries.
func computeDiff(a, b Summary) ComparisonDiff {
	latencyDelta := b.AvgLatencyMS - a.AvgLatencyMS
	tokenDelta := b.TotalTokens - a.TotalTokens
	successDelta := b.TasksSucceeded - a.TasksSucceeded

	return ComparisonDiff{
		LatencyDeltaMS: latencyDelta,
		TokenDelta:     tokenDelta,
		SuccessDelta:   successDelta,
		LatencyWinner:  winnerByLower(latencyDelta),
		TokenWinner:    winnerByLower(int64(tokenDelta)),
		SuccessWinner:  winnerByHigher(successDelta),
	}
}

// determineWinner returns the overall winner based on latency + tokens + success.
func determineWinner(diff ComparisonDiff) string {
	scoreA := 0
	scoreB := 0
	if diff.LatencyWinner == "A" { scoreA++ } else if diff.LatencyWinner == "B" { scoreB++ }
	if diff.TokenWinner == "A" { scoreA++ } else if diff.TokenWinner == "B" { scoreB++ }
	if diff.SuccessWinner == "A" { scoreA++ } else if diff.SuccessWinner == "B" { scoreB++ }
	if scoreA > scoreB { return "A" }
	if scoreB > scoreA { return "B" }
	return "tie"
}

func winnerByLower(delta int64) string {
	if delta < 0 { return "B" } // B is lower
	if delta > 0 { return "A" } // A is lower
	return "tie"
}

func winnerByHigher(delta int) string {
	if delta > 0 { return "B" } // B is higher
	if delta < 0 { return "A" } // A is higher
	return "tie"
}

// FormatComparison returns a human-readable comparison report.
func (r *ComparisonReport) FormatComparison() string {
	output := fmt.Sprintf("A/B Comparison: %s\n", r.SuiteName)
	output += fmt.Sprintf("  A: %s vs B: %s\n", r.LabelA, r.LabelB)
	output += fmt.Sprintf("  Overall Winner: %s\n\n", r.Winner)

	output += fmt.Sprintf("Latency (avg ms):\n")
	output += fmt.Sprintf("  %s: %dms  |  %s: %dms  |  delta: %dms  |  winner: %s\n",
		r.LabelA, r.ReportA.Summary.AvgLatencyMS,
		r.LabelB, r.ReportB.Summary.AvgLatencyMS,
		r.Diff.LatencyDeltaMS, r.Diff.LatencyWinner)

	output += fmt.Sprintf("Tokens (total):\n")
	output += fmt.Sprintf("  %s: %d  |  %s: %d  |  delta: %d  |  winner: %s\n",
		r.LabelA, r.ReportA.Summary.TotalTokens,
		r.LabelB, r.ReportB.Summary.TotalTokens,
		r.Diff.TokenDelta, r.Diff.TokenWinner)

	output += fmt.Sprintf("Success rate:\n")
	output += fmt.Sprintf("  %s: %d/%d  |  %s: %d/%d  |  delta: %d  |  winner: %s\n",
		r.LabelA, r.ReportA.Summary.TasksSucceeded, r.ReportA.Summary.TasksTotal,
		r.LabelB, r.ReportB.Summary.TasksSucceeded, r.ReportB.Summary.TasksTotal,
		r.Diff.SuccessDelta, r.Diff.SuccessWinner)

	return output
}
