// memory.go implements Evolution Memory: load recent evolution JSON snapshots
// and derive recurring findings / health trends (ADR-033).
//
// This closes the Reflexion-style loop described in
// docs/research/self-evolution-runtime.md: each Evolve cycle should learn from
// prior evolution reflections stored under docs/reports/.baselines/.
package evolver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultMemoryWindow is how many historical evolution snapshots to load.
const DefaultMemoryWindow = 12

// RecurringFinding is a diagnosis finding that appeared across multiple
// historical evolution reports.
type RecurringFinding struct {
	Category   string `json:"category"`
	Message    string `json:"message"`
	Count      int    `json:"count"`      // number of historical reports containing this finding
	WindowSize int    `json:"windowSize"` // how many reports were examined
	// Severity is the highest severity observed (error > warning > info).
	Severity Severity `json:"severity"`
}

// EvolutionMemory summarizes trends across recent evolution snapshots.
type EvolutionMemory struct {
	// Window is the number of historical reports successfully loaded.
	Window int `json:"window"`
	// Examined is the requested window size.
	Examined int `json:"examined"`
	// Recurring lists findings seen in >= MinRecurrence reports.
	Recurring []RecurringFinding `json:"recurring,omitempty"`
	// WarningTrend is newest-first warning counts for the loaded window.
	WarningTrend []int `json:"warningTrend,omitempty"`
	// ErrorTrend is newest-first error counts for the loaded window.
	ErrorTrend []int `json:"errorTrend,omitempty"`
	// ArtifactTrend is newest-first TotalArtifacts counts when available.
	ArtifactTrend []int `json:"artifactTrend,omitempty"`
	// HealthyStreak is how many newest consecutive reports had 0 errors and 0 warnings.
	HealthyStreak int `json:"healthyStreak"`
	// Improving is true when the newest warning count is lower than the oldest
	// in the window (and window >= 2).
	Improving bool `json:"improving"`
	// Worsening is true when newest warnings/errors exceed the oldest.
	Worsening bool `json:"worsening"`
}

// MinRecurrence is the minimum historical hit count for a finding to be "recurring".
const MinRecurrence = 3

// historicalSnapshot is the subset of EvolutionReport needed for trend analysis.
// It intentionally ignores fields that are expensive or unstable across schema
// evolution (full sediment inventories, proposal text).
type historicalSnapshot struct {
	Timestamp  string `json:"timestamp"`
	DurationMS int64  `json:"durationMS"`
	Diagnosis  *struct {
		OK       bool `json:"ok"`
		Errors   int  `json:"errors"`
		Warnings int  `json:"warnings"`
		Infos    int  `json:"infos"`
		Findings []struct {
			Severity Severity `json:"severity"`
			Category string   `json:"category"`
			Message  string   `json:"message"`
		} `json:"findings"`
	} `json:"diagnosis"`
	Sediment *struct {
		TotalArtifacts int `json:"totalArtifacts"`
	} `json:"sediment"`
}

// LoadEvolutionMemory reads up to limit newest evolution-*.json baselines and
// computes recurring findings + simple health trends. limit <= 0 uses DefaultMemoryWindow.
func (e *Evolver) LoadEvolutionMemory(repoRoot string, limit int) *EvolutionMemory {
	if limit <= 0 {
		limit = DefaultMemoryWindow
	}
	mem := &EvolutionMemory{Examined: limit}
	dir := filepath.Join(repoRoot, baselinesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return mem
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "evolution-") && strings.HasSuffix(name, ".json") {
			names = append(names, name)
		}
	}
	// Newest last in lexical order for date-seq naming; reverse for newest-first.
	sort.Strings(names)
	if len(names) == 0 {
		return mem
	}
	// Take the last `limit` names (newest).
	if len(names) > limit {
		names = names[len(names)-limit:]
	}
	// Reverse to newest-first.
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}

	type key struct {
		cat string
		msg string
	}
	counts := map[key]int{}
	sevMax := map[key]Severity{}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var snap historicalSnapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			continue
		}
		mem.Window++
		warns, errs, arts := 0, 0, 0
		if snap.Diagnosis != nil {
			warns = snap.Diagnosis.Warnings
			errs = snap.Diagnosis.Errors
			// Count unique finding keys in this report (normalize message lightly).
			seenInReport := map[key]bool{}
			for _, f := range snap.Diagnosis.Findings {
				if f.Severity == SeverityInfo {
					continue // info noise (roadmap counts) is not trend-actionable
				}
				k := key{cat: f.Category, msg: normalizeFindingMessage(f.Message)}
				if seenInReport[k] {
					continue
				}
				seenInReport[k] = true
				counts[k]++
				// lower sevRank = more severe
				if prev, ok := sevMax[k]; !ok || sevRank(f.Severity) < sevRank(prev) {
					sevMax[k] = f.Severity
				}
			}
		}
		if snap.Sediment != nil {
			arts = snap.Sediment.TotalArtifacts
		}
		mem.WarningTrend = append(mem.WarningTrend, warns)
		mem.ErrorTrend = append(mem.ErrorTrend, errs)
		if arts > 0 {
			mem.ArtifactTrend = append(mem.ArtifactTrend, arts)
		}
	}

	// Healthy streak from newest.
	for i := 0; i < len(mem.WarningTrend); i++ {
		if mem.WarningTrend[i] == 0 && mem.ErrorTrend[i] == 0 {
			mem.HealthyStreak++
		} else {
			break
		}
	}
	if mem.Window >= 2 {
		newestW, oldestW := mem.WarningTrend[0], mem.WarningTrend[len(mem.WarningTrend)-1]
		newestE, oldestE := mem.ErrorTrend[0], mem.ErrorTrend[len(mem.ErrorTrend)-1]
		if newestW+newestE < oldestW+oldestE {
			mem.Improving = true
		}
		if newestW+newestE > oldestW+oldestE {
			mem.Worsening = true
		}
	}

	// Build recurring list.
	for k, c := range counts {
		if c < MinRecurrence {
			continue
		}
		mem.Recurring = append(mem.Recurring, RecurringFinding{
			Category:   k.cat,
			Message:    k.msg,
			Count:      c,
			WindowSize: mem.Window,
			Severity:   sevMax[k],
		})
	}
	sort.Slice(mem.Recurring, func(i, j int) bool {
		if mem.Recurring[i].Count != mem.Recurring[j].Count {
			return mem.Recurring[i].Count > mem.Recurring[j].Count
		}
		if mem.Recurring[i].Category != mem.Recurring[j].Category {
			return mem.Recurring[i].Category < mem.Recurring[j].Category
		}
		return mem.Recurring[i].Message < mem.Recurring[j].Message
	})
	return mem
}

func normalizeFindingMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	// Collapse volatile path suffixes / counts that change every run.
	// Keep category-level recurrence useful.
	if len(msg) > 160 {
		msg = msg[:160]
	}
	return msg
}

// FormatEvolutionMemory returns a human-readable memory/trend section.
func (m *EvolutionMemory) FormatEvolutionMemory() string {
	if m == nil || m.Window == 0 {
		return "=== Evolution Memory ===\nNo historical evolution snapshots loaded.\n"
	}
	var sb strings.Builder
	sb.WriteString("=== Evolution Memory ===\n")
	sb.WriteString(fmt.Sprintf("Window: %d / %d reports\n", m.Window, m.Examined))
	sb.WriteString(fmt.Sprintf("Healthy streak: %d\n", m.HealthyStreak))
	if m.Improving {
		sb.WriteString("Trend: improving (newest issues < oldest in window)\n")
	} else if m.Worsening {
		sb.WriteString("Trend: worsening (newest issues > oldest in window)\n")
	} else {
		sb.WriteString("Trend: stable\n")
	}
	if len(m.WarningTrend) > 0 {
		sb.WriteString(fmt.Sprintf("Warnings (newest→oldest): %v\n", m.WarningTrend))
	}
	if len(m.ErrorTrend) > 0 {
		sb.WriteString(fmt.Sprintf("Errors   (newest→oldest): %v\n", m.ErrorTrend))
	}
	if len(m.Recurring) == 0 {
		sb.WriteString("Recurring findings: none\n")
	} else {
		sb.WriteString(fmt.Sprintf("Recurring findings (>=%d hits):\n", MinRecurrence))
		limit := len(m.Recurring)
		if limit > 8 {
			limit = 8
		}
		for i := 0; i < limit; i++ {
			r := m.Recurring[i]
			sb.WriteString(fmt.Sprintf("  - [%s] %s (%d/%d)\n", r.Category, truncate(r.Message, 90), r.Count, r.WindowSize))
		}
		if len(m.Recurring) > limit {
			sb.WriteString(fmt.Sprintf("  ... +%d more\n", len(m.Recurring)-limit))
		}
	}
	return sb.String()
}
