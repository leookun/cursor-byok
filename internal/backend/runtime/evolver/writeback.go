// writeback.go implements safe, deterministic handbook auto-writeback (ADR-030/031/032).
// It only repairs index/catalog drift in chapters 04/05/09/10/15/16/24/27/28. It never modifies source code.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AutoWritebackResult records what safe writeback applied.
type AutoWritebackResult struct {
	Applied []WritebackItem `json:"applied"`
	Skipped []WritebackItem `json:"skipped"`
}

var (
	adrTitlePattern      = regexp.MustCompile(`(?m)^#\s*ADR-(\d{3})\s*:\s*(.+)$`)
	adrIndexRowPattern   = regexp.MustCompile(`(?m)^\|\s*ADR-(\d{3})\s*\|`)
	researchListPattern  = regexp.MustCompile("(?m)^- `([a-zA-Z0-9_-]+\\.md)`")
	researchTitlePattern = regexp.MustCompile(`(?m)^#\s+(.+)$`)
)

const (
	benchmarkBaselineBegin       = "<!-- EVOLVER:BENCHMARK_BASELINE -->"
	benchmarkBaselineEnd         = "<!-- /EVOLVER:BENCHMARK_BASELINE -->"
	runtimeMetricsBaselineBegin  = "<!-- EVOLVER:RUNTIME_METRICS_BASELINE -->"
	runtimeMetricsBaselineEnd    = "<!-- /EVOLVER:RUNTIME_METRICS_BASELINE -->"
)

// AutoWriteback applies only deterministic index repairs from guidance.
// Supported actions: add-adr-index, add-research-index, convert-or-link-research
// (index-only append), add-report-index (ch.27 catalog). All other actions are skipped.
func (e *Evolver) AutoWriteback(repoRoot string, guidance []WritebackItem) (*AutoWritebackResult, error) {
	result := &AutoWritebackResult{}
	if len(guidance) == 0 {
		// Even without guidance, reconcile indexes against disk so the loop can
		// self-heal the most common drift classes.
		guidance = e.defaultIndexGuidance(repoRoot)
	}

	// Always reconcile all safe indexes. Guidance only classifies non-deterministic
	// actions as skipped; mechanical index/catalog drift is self-healed on every writeback.
	needADR := true
	needResearch := true
	needReports := true
	needRuntimeCatalog := true
	needFoundations := true
	needBulletFoundations := true
	for _, item := range guidance {
		switch item.Action {
		case "add-adr-index", "add-research-index", "add-report-index", "convert-or-link-research",
			"sync-runtime-catalog", "add-runtime-catalog", "sync-runtime-status",
			"sync-foundation-tables", "sync-bullet-foundations":
			// handled by full index reconcile below
		default:
			result.Skipped = append(result.Skipped, item)
		}
	}

	if needADR {
		applied, err := e.writebackADRIndex(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
	}
	if needResearch {
		applied, err := e.writebackResearchIndex(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
	}
	if needReports {
		applied, err := e.writebackReportIndex(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
		// Also update the benchmark baseline evidence block in §27.8 (ADR-XXX).
		blApplied, blErr := e.writebackBenchmarkBaseline(repoRoot)
		if blErr != nil {
			return result, blErr
		}
		result.Applied = append(result.Applied, blApplied...)
		// Also update the runtime metrics baseline evidence block in §27.8 (ADR-045).
		rmApplied, rmErr := e.writebackRuntimeMetricsBaseline(repoRoot)
		if rmErr != nil {
			return result, rmErr
		}
		result.Applied = append(result.Applied, rmApplied...)
	}
	if needRuntimeCatalog {
		applied, err := e.writebackRuntimeCatalog(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
	}
	if needFoundations {
		applied, err := e.writebackFoundationTables(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
	}
	if needBulletFoundations {
		applied, err := e.writebackBulletFoundations(repoRoot)
		if err != nil {
			return result, err
		}
		result.Applied = append(result.Applied, applied...)
	}
	return result, nil
}

func (e *Evolver) defaultIndexGuidance(repoRoot string) []WritebackItem {
	// Reuse diagnosis-derived guidance when available; otherwise empty.
	diag := e.Diagnose(repoRoot)
	kg := e.Sediment(repoRoot)
	return e.computeWritebackGuidance(diag, kg)
}

func (e *Evolver) writebackADRIndex(repoRoot string) ([]WritebackItem, error) {
	guidePath := filepath.Join(repoRoot, "docs", "handbook", "28_ADR_Guide.md")
	content, err := os.ReadFile(guidePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 28_ADR_Guide.md: %w", err)
	}
	text := string(content)

	// Collect IDs already present in the guide table.
	present := map[string]bool{}
	for _, m := range adrIndexRowPattern.FindAllStringSubmatch(text, -1) {
		present[m[1]] = true
	}

	// Discover on-disk ADRs.
	adrDir := filepath.Join(repoRoot, "docs", "adr")
	entries, err := os.ReadDir(adrDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list docs/adr: %w", err)
	}

	type missingADR struct {
		id       string
		filename string
		title    string
		status   string
	}
	var missing []missingADR
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if len(entry.Name()) < 3 {
			continue
		}
		id := entry.Name()[:3]
		if present[id] {
			continue
		}
		body, err := os.ReadFile(filepath.Join(adrDir, entry.Name()))
		if err != nil {
			continue
		}
		title := strings.TrimSuffix(entry.Name(), ".md")
		if m := adrTitlePattern.FindStringSubmatch(string(body)); m != nil {
			title = strings.TrimSpace(m[2])
		}
		status := "Accepted"
		if strings.Contains(string(body), "## Status") {
			// Best-effort status line extraction.
			for _, line := range strings.Split(string(body), "\n") {
				trim := strings.TrimSpace(line)
				if trim == "Accepted" || trim == "Proposed" || trim == "Deprecated" {
					status = trim
					break
				}
			}
		}
		missing = append(missing, missingADR{
			id:       id,
			filename: entry.Name(),
			title:    title,
			status:   status,
		})
	}
	if len(missing) == 0 {
		return nil, nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].id < missing[j].id })

	// Insert missing rows after the last ADR table row.
	lines := strings.Split(text, "\n")
	lastADRRow := -1
	for i, line := range lines {
		if adrIndexRowPattern.MatchString(line) {
			lastADRRow = i
		}
	}
	if lastADRRow < 0 {
		return nil, fmt.Errorf("no ADR index rows found in 28_ADR_Guide.md")
	}

	var insert []string
	var applied []WritebackItem
	for _, m := range missing {
		row := fmt.Sprintf("| ADR-%s | %s | %s | docs/adr/%s |", m.id, m.title, m.status, m.filename)
		insert = append(insert, row)
		applied = append(applied, WritebackItem{
			Chapter: "28_ADR_Guide.md",
			Action:  "add-adr-index",
			Detail:  fmt.Sprintf("inserted ADR-%s -> docs/adr/%s", m.id, m.filename),
		})
	}

	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:lastADRRow+1]...)
	out = append(out, insert...)
	out = append(out, lines[lastADRRow+1:]...)
	if err := os.WriteFile(guidePath, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return nil, fmt.Errorf("write 28_ADR_Guide.md: %w", err)
	}
	return applied, nil
}

func (e *Evolver) writebackResearchIndex(repoRoot string) ([]WritebackItem, error) {
	charterPath := filepath.Join(repoRoot, "docs", "handbook", "24_Research_Charter.md")
	content, err := os.ReadFile(charterPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 24_Research_Charter.md: %w", err)
	}
	text := string(content)

	present := map[string]bool{}
	for _, m := range researchListPattern.FindAllStringSubmatch(text, -1) {
		present[m[1]] = true
	}
	// Also treat bare basename mentions as present (docguard does the same).
	researchDir := filepath.Join(repoRoot, "docs", "research")
	entries, err := os.ReadDir(researchDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list docs/research: %w", err)
	}

	type missingNote struct {
		filename string
		title    string
	}
	var missing []missingNote
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := entry.Name()
		if present[name] || strings.Contains(text, name) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(researchDir, name))
		title := name
		if err == nil {
			if m := researchTitlePattern.FindStringSubmatch(string(body)); m != nil {
				title = strings.TrimSpace(m[1])
			}
		}
		missing = append(missing, missingNote{filename: name, title: title})
	}
	if len(missing) == 0 {
		return nil, nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].filename < missing[j].filename })

	// Insert after the last research list item under 24.7 if possible.
	lines := strings.Split(text, "\n")
	lastList := -1
	for i, line := range lines {
		if researchListPattern.MatchString(line) {
			lastList = i
		}
	}
	if lastList < 0 {
		return nil, fmt.Errorf("no research list items found in 24_Research_Charter.md")
	}

	var insert []string
	var applied []WritebackItem
	for _, m := range missing {
		insert = append(insert, fmt.Sprintf("- `%s` — %s", m.filename, m.title))
		applied = append(applied, WritebackItem{
			Chapter: "24_Research_Charter.md",
			Action:  "add-research-index",
			Detail:  fmt.Sprintf("inserted research note %s", m.filename),
		})
	}

	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:lastList+1]...)
	out = append(out, insert...)
	out = append(out, lines[lastList+1:]...)
	if err := os.WriteFile(charterPath, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return nil, fmt.Errorf("write 24_Research_Charter.md: %w", err)
	}
	return applied, nil
}

func (e *Evolver) writebackReportIndex(repoRoot string) ([]WritebackItem, error) {
	benchPath := filepath.Join(repoRoot, "docs", "handbook", "27_Benchmark.md")
	content, err := os.ReadFile(benchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 27_Benchmark.md: %w", err)
	}
	text := string(content)

	reportsDir := filepath.Join(repoRoot, "docs", "reports")
	entries, err := os.ReadDir(reportsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list docs/reports: %w", err)
	}

	type missingReport struct {
		filename string
		title    string
	}
	var missing []missingReport
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := entry.Name()
		// Only auto-index evolution/baseline evidence produced by the loop.
		if !strings.Contains(name, "evolution") && !strings.Contains(name, "evolver") && !strings.Contains(name, "baseline") {
			continue
		}
		if strings.Contains(text, name) {
			continue
		}
		title := "Evolution evidence (auto-indexed)"
		if strings.Contains(name, "baseline") {
			title = "Evolver baseline report (auto-indexed)"
		} else if strings.Contains(name, "evolution") {
			title = "Evolver evolution report (auto-indexed)"
		}
		missing = append(missing, missingReport{filename: name, title: title})
	}
	if len(missing) == 0 {
		return nil, nil
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i].filename < missing[j].filename })

	// Insert only inside §27.8 (between heading and next ## heading).
	lines := strings.Split(text, "\n")
	sectionStart := -1
	sectionEnd := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if sectionStart < 0 {
			if strings.Contains(trim, "27.8") && strings.Contains(trim, "现有报告") {
				sectionStart = i
			}
			continue
		}
		if strings.HasPrefix(trim, "## ") && i > sectionStart {
			sectionEnd = i
			break
		}
	}
	if sectionStart < 0 {
		return nil, fmt.Errorf("section 27.8 not found in 27_Benchmark.md")
	}
	if sectionEnd < 0 {
		sectionEnd = len(lines)
	}

	// Prefer last catalog bullet inside the section body (before horizontal rules
	// or the Auto-writeback note). Keep §27.8 structure stable.
	insertAt := -1
	for i := sectionStart + 1; i < sectionEnd; i++ {
		trim := strings.TrimSpace(lines[i])
		if trim == "---" || strings.HasPrefix(trim, "**Auto-writeback") {
			break
		}
		if strings.HasPrefix(trim, "-") {
			insertAt = i
		}
	}
	if insertAt < 0 {
		// No bullets yet: insert immediately after the heading.
		insertAt = sectionStart
	}

	var insert []string
	var applied []WritebackItem
	for _, m := range missing {
		insert = append(insert, fmt.Sprintf("- docs/reports/%s — %s", m.filename, m.title))
		applied = append(applied, WritebackItem{
			Chapter: "27_Benchmark.md",
			Action:  "add-report-index",
			Detail:  fmt.Sprintf("inserted report catalog entry for %s", m.filename),
		})
	}

	out := make([]string, 0, len(lines)+len(insert))
	out = append(out, lines[:insertAt+1]...)
	out = append(out, insert...)
	out = append(out, lines[insertAt+1:]...)
	if err := os.WriteFile(benchPath, []byte(strings.Join(out, "\n")), 0o644); err != nil {
		return nil, fmt.Errorf("write 27_Benchmark.md: %w", err)
	}
	return applied, nil
}

// writebackBenchmarkBaseline maintains a deterministic evidence block in §27.8
// of the Benchmark handbook (27_Benchmark.md).  It reads the latest persisted
// baseline from docs/reports/.baselines/latest-benchmark.json and writes it
// inside <!-- EVOLVER:BENCHMARK_BASELINE --> ... <!-- /EVOLVER:BENCHMARK_BASELINE -->
// markers.  Idempotent: no-op when the block already matches the current baseline.
func (e *Evolver) writebackBenchmarkBaseline(repoRoot string) ([]WritebackItem, error) {
	bl := e.LoadBaseline(repoRoot)
	if bl == nil {
		return nil, nil
	}

	benchPath := filepath.Join(repoRoot, "docs", "handbook", "27_Benchmark.md")
	content, err := os.ReadFile(benchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 27_Benchmark.md: %w", err)
	}
	text := string(content)

	newBlock := formatBenchmarkBaselineBlock(bl)

	beginIdx := strings.Index(text, benchmarkBaselineBegin)
	endIdx := strings.Index(text, benchmarkBaselineEnd)

	if beginIdx >= 0 && endIdx > beginIdx {
		// Replace content between markers.
		oldBlock := text[beginIdx : endIdx+len(benchmarkBaselineEnd)]
		if oldBlock == newBlock {
			return nil, nil // idempotent
		}
		text = text[:beginIdx] + newBlock + text[endIdx+len(benchmarkBaselineEnd):]
	} else {
		// Insert before the next ## heading after §27.8.
		lines := strings.Split(text, "\n")
		insertAt := len(lines)
		for i, line := range lines {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "## ") && i > 0 {
				// Skip the §27.8 heading itself.
				lower := strings.ToLower(trim)
				if strings.Contains(lower, "27.8") || strings.Contains(lower, "现有报告") {
					continue
				}
				insertAt = i
				break
			}
		}
		out := make([]string, 0, len(lines)+3)
		out = append(out, lines[:insertAt]...)
		out = append(out, "")
		out = append(out, newBlock)
		out = append(out, "")
		out = append(out, lines[insertAt:]...)
		text = strings.Join(out, "\n")
	}

	if err := os.WriteFile(benchPath, []byte(text), 0o644); err != nil {
		return nil, fmt.Errorf("write 27_Benchmark.md: %w", err)
	}

	return []WritebackItem{{
		Chapter: "27_Benchmark.md",
		Action:  "update-benchmark-baseline",
		Detail:  fmt.Sprintf("baseline %s: %dms %dtokens %d/%d pass", bl.SuiteName, bl.AvgLatencyMS, bl.TotalTokens, bl.TasksSucceeded, bl.TasksTotal),
	}}, nil
}

// formatBenchmarkBaselineBlock renders the marker-delimited evidence block.
func formatBenchmarkBaselineBlock(bl *BenchmarkBaseline) string {
	var sb strings.Builder
	sb.WriteString(benchmarkBaselineBegin)
	sb.WriteString("\n\n")
	sb.WriteString("| Suite | Date | Latency (ms) | Tokens | Tasks | ✅ Passed | ❌ Failed |\n")
	sb.WriteString("|---|---|---|---|---|---|---|\n")

	ts := bl.Timestamp
	if t, err := time.Parse(time.RFC3339, bl.Timestamp); err == nil {
		ts = t.Format("2006-01-02 15:04:05")
	}

	sb.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %d | %d |\n",
		bl.SuiteName, ts, bl.AvgLatencyMS, bl.TotalTokens,
		bl.TasksTotal, bl.TasksSucceeded, bl.TasksFailed))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("_Baseline updated: %s_\n", bl.Timestamp))
	sb.WriteString("\n")
	sb.WriteString(benchmarkBaselineEnd)
	return sb.String()
}

// writebackRuntimeMetricsBaseline maintains a deterministic evidence block for
// runtime efficiency metrics (cache hit-rate, tool cache, optimize spend) in
// 27_Benchmark.md. Same pattern as writebackBenchmarkBaseline.
// Idempotent: no-op when the block already matches the current baseline,
// or when no baseline exists on disk.
func (e *Evolver) writebackRuntimeMetricsBaseline(repoRoot string) ([]WritebackItem, error) {
	bl := e.LoadRuntimeMetricBaseline(repoRoot)
	if bl == nil {
		return nil, nil
	}

	benchPath := filepath.Join(repoRoot, "docs", "handbook", "27_Benchmark.md")
	content, err := os.ReadFile(benchPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 27_Benchmark.md: %w", err)
	}
	text := string(content)

	newBlock := formatRuntimeMetricsBaselineBlock(bl)

	beginIdx := strings.Index(text, runtimeMetricsBaselineBegin)
	endIdx := strings.Index(text, runtimeMetricsBaselineEnd)

	if beginIdx >= 0 && endIdx > beginIdx {
		// Replace content between markers.
		oldBlock := text[beginIdx : endIdx+len(runtimeMetricsBaselineEnd)]
		if oldBlock == newBlock {
			return nil, nil // idempotent
		}
		text = text[:beginIdx] + newBlock + text[endIdx+len(runtimeMetricsBaselineEnd):]
	} else {
		// Insert after the benchmark baseline block if present, otherwise before the next ## heading.
		insertAfter := strings.Index(text, benchmarkBaselineEnd)
		if insertAfter < 0 {
			// No benchmark baseline block either; find the next ## after §27.8.
			lines := strings.Split(text, "\n")
			insertAt := len(lines)
			for i, line := range lines {
				trim := strings.TrimSpace(line)
				if strings.HasPrefix(trim, "## ") && i > 0 {
					lower := strings.ToLower(trim)
					if strings.Contains(lower, "27.8") || strings.Contains(lower, "现有报告") {
						continue
					}
					if strings.Contains(lower, benchmarkBaselineBegin) || strings.Contains(lower, benchmarkBaselineEnd) {
						continue
					}
					insertAt = i
					break
				}
			}
			out := make([]string, 0, len(lines)+3)
			out = append(out, lines[:insertAt]...)
			out = append(out, "")
			out = append(out, newBlock)
			out = append(out, "")
			out = append(out, lines[insertAt:]...)
			text = strings.Join(out, "\n")
		} else {
			// Insert right after the benchmark baseline end marker.
			insertAt := insertAfter + len(benchmarkBaselineEnd)
			text = text[:insertAt] + "\n\n" + newBlock + text[insertAt:]
		}
	}

	if err := os.WriteFile(benchPath, []byte(text), 0o644); err != nil {
		return nil, fmt.Errorf("write 27_Benchmark.md: %w", err)
	}

	return []WritebackItem{{
		Chapter: "27_Benchmark.md",
		Action:  "update-runtime-metrics-baseline",
		Detail:  fmt.Sprintf("runtime metrics baseline: cacheHitRate=%.2f toolCacheHitRate=%.2f optimizeSpent=%.2f",
			bl.CacheHitRate, bl.ToolCacheHitRate, bl.OptimizeSpentUSD),
	}}, nil
}

// formatRuntimeMetricsBaselineBlock renders the marker-delimited evidence block.
func formatRuntimeMetricsBaselineBlock(bl *RuntimeMetricSnapshot) string {
	var sb strings.Builder
	sb.WriteString(runtimeMetricsBaselineBegin)
	sb.WriteString("\n\n")
	sb.WriteString("| Metric | Value |\n")
	sb.WriteString("|---|---|\n")

	if bl.HasCache {
		sb.WriteString(fmt.Sprintf("| Cache Hit Rate | %.2f%% |\n", bl.CacheHitRate*100))
		sb.WriteString(fmt.Sprintf("| Cache Tokens Saved | %d |\n", bl.CacheTokensSaved))
		sb.WriteString(fmt.Sprintf("| Cache Exact Hits | %d |\n", bl.CacheExactHits))
		sb.WriteString(fmt.Sprintf("| Cache Semantic Hits | %d |\n", bl.CacheSemanticHits))
	}
	if bl.HasToolCache {
		sb.WriteString(fmt.Sprintf("| Tool Cache Hit Rate | %.2f%% |\n", bl.ToolCacheHitRate*100))
		sb.WriteString(fmt.Sprintf("| Tool Cache Hits | %d |\n", bl.ToolCacheHits))
		sb.WriteString(fmt.Sprintf("| Tool Cache Misses | %d |\n", bl.ToolCacheMisses))
	}
	if bl.HasOptimize {
		sb.WriteString(fmt.Sprintf("| Optimize Spent (USD) | %.4f |\n", bl.OptimizeSpentUSD))
		sb.WriteString(fmt.Sprintf("| Optimize Turns | %d |\n", bl.OptimizeTurns))
		sb.WriteString(fmt.Sprintf("| Optimize Budget (USD) | %.4f |\n", bl.OptimizeBudgetUSD))
	}

	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("_Baseline updated: %s_\n", bl.Timestamp))
	sb.WriteString("\n")
	sb.WriteString(runtimeMetricsBaselineEnd)
	return sb.String()
}

func (e *Evolver) writebackRuntimeCatalog(repoRoot string) ([]WritebackItem, error) {
	path := filepath.Join(repoRoot, "docs", "handbook", "04_Runtime_Architecture.md")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read 04_Runtime_Architecture.md: %w", err)
	}
	text := string(content)
	cat := e.InventoryRuntimes(repoRoot)
	if cat == nil {
		return nil, nil
	}

	// Locate §4.2 table region.
	start := strings.Index(text, "## 4.2")
	if start < 0 {
		return nil, nil
	}
	end := strings.Index(text[start+1:], "## ")
	if end < 0 {
		end = len(text)
	} else {
		end = start + 1 + end
	}
	section := text[start:end]
	lines := strings.Split(section, "\n")

	// Map runtime name -> line index within section.
	rowIdx := map[string]int{}
	lastTableRow := -1
	headerSeen := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "|") {
			continue
		}
		// Skip separator.
		if strings.Contains(trim, "---") {
			headerSeen = true
			continue
		}
		cols := splitTableRow(trim)
		if len(cols) < 4 {
			continue
		}
		name := strings.TrimSpace(cols[0])
		if name == "" || name == "Runtime" {
			continue
		}
		rowIdx[name] = i
		if headerSeen {
			lastTableRow = i
		}
	}
	if lastTableRow < 0 {
		return nil, nil
	}

	var applied []WritebackItem
	changed := false

	// 1) Update status cells for deterministic drift.
	for _, inv := range cat.Entries {
		idx, ok := rowIdx[inv.Name]
		if !ok {
			continue
		}
		if inv.Drift == "" {
			continue
		}
		// Only rewrite status when policy says so.
		newStatus, ok := statusRewrite(inv)
		if !ok {
			continue
		}
		cols := splitTableRow(strings.TrimSpace(lines[idx]))
		if len(cols) < 4 {
			continue
		}
		oldStatus := strings.TrimSpace(cols[3])
		if normalizeHandbookStatus(oldStatus) == normalizeHandbookStatus(newStatus) && oldStatus == newStatus {
			continue
		}
		// Preserve long production notes if already production-class and inferred production.
		if normalizeHandbookStatus(oldStatus) == MaturityProduction && inv.Inferred == MaturityProduction {
			continue
		}
		cols[3] = " " + newStatus + " "
		// Rebuild row with consistent spacing.
		lines[idx] = fmt.Sprintf("| %s | %s | %s | %s |",
			strings.TrimSpace(cols[0]),
			strings.TrimSpace(cols[1]),
			strings.TrimSpace(cols[2]),
			strings.TrimSpace(cols[3]),
		)
		applied = append(applied, WritebackItem{
			Chapter: "04_Runtime_Architecture.md",
			Action:  "sync-runtime-catalog",
			Detail:  fmt.Sprintf("updated %s status: %q -> %q", inv.Name, oldStatus, newStatus),
		})
		changed = true
	}

	// 2) Insert missing runtime rows (canonical order).
	var inserts []string
	for _, inv := range cat.Entries {
		if _, ok := rowIdx[inv.Name]; ok {
			continue
		}
		// Default duty/slogan placeholders for missing rows.
		duty, slogan := defaultRuntimeProse(inv.Name)
		status := string(inv.Inferred)
		row := fmt.Sprintf("| %s | %s | %s | %s |", inv.Name, duty, slogan, status)
		inserts = append(inserts, row)
		applied = append(applied, WritebackItem{
			Chapter: "04_Runtime_Architecture.md",
			Action:  "sync-runtime-catalog",
			Detail:  fmt.Sprintf("inserted matrix row for %s (%s)", inv.Name, status),
		})
	}
	if len(inserts) > 0 {
		outLines := make([]string, 0, len(lines)+len(inserts))
		outLines = append(outLines, lines[:lastTableRow+1]...)
		outLines = append(outLines, inserts...)
		outLines = append(outLines, lines[lastTableRow+1:]...)
		lines = outLines
		changed = true
	}

	// 3) Regenerate §4.6 auto inventory between markers (create if missing).
	newSection := strings.Join(lines, "\n")
	newText := text[:start] + newSection + text[end:]
	block := renderRuntimeCatalogBlock(cat)
	begin := "<!-- runtime-catalog:begin -->"
	endMark := "<!-- runtime-catalog:end -->"
	if strings.Contains(newText, begin) && strings.Contains(newText, endMark) {
		b := strings.Index(newText, begin)
		eidx := strings.Index(newText, endMark)
		if b >= 0 && eidx > b {
			eidx += len(endMark)
			replacement := begin + "\n" + block + "\n" + endMark
			if newText[b:eidx] != replacement {
				newText = newText[:b] + replacement + newText[eidx:]
				applied = append(applied, WritebackItem{
					Chapter: "04_Runtime_Architecture.md",
					Action:  "sync-runtime-catalog",
					Detail:  "regenerated §4.6 runtime-catalog auto block",
				})
				changed = true
			}
		}
	} else {
		// Append before EOF as new section 4.6.
		if !strings.Contains(newText, "## 4.6") {
			newText = strings.TrimRight(newText, "\n") + "\n\n## 4.6 Runtime Catalog（自动维护）\n\n" +
				begin + "\n" + block + "\n" + endMark + "\n"
			applied = append(applied, WritebackItem{
				Chapter: "04_Runtime_Architecture.md",
				Action:  "sync-runtime-catalog",
				Detail:  "created §4.6 runtime-catalog auto block",
			})
			changed = true
		}
	}

	if !changed {
		return nil, nil
	}
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		return nil, fmt.Errorf("write 04_Runtime_Architecture.md: %w", err)
	}
	return applied, nil
}

func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func statusRewrite(inv RuntimeInventory) (string, bool) {
	hb := normalizeHandbookStatus(inv.HandbookStatus)
	switch inv.Drift {
	case "code-present-status-still-missing":
		return string(inv.Inferred), true
	case "status-understates-production":
		return string(MaturityProduction), true
	case "code-missing-status-claims-present", "status-overstates-missing-code":
		return string(MaturityMissing), true
	case "missing-from-matrix":
		return "", false // handled by insert
	default:
		// If handbook unknown/empty but code present.
		if hb == MaturityUnknown && inv.Inferred != MaturityMissing {
			return string(inv.Inferred), true
		}
		return "", false
	}
}

func defaultRuntimeProse(name string) (duty, slogan string) {
	switch name {
	case "Organization Runtime":
		return "AOS 组织编排：Leader/Members/Workspace", "\"谁来干，怎么协作\""
	case "Context Runtime":
		return "上下文构建/压缩/排序/窗口管理", "\"给模型看什么\""
	case "Memory Runtime":
		return "五层记忆（Working/Session/Long/Project/User）", "\"记住什么\""
	case "Cache Runtime":
		return "精确缓存+语义缓存", "\"能不能不调模型\""
	case "Optimization Runtime":
		return "Token Budget+Cost Optimizer", "\"怎么省钱省Token\""
	case "Tool Runtime":
		return "MCP/Shell/Git/Browser/Filesystem", "\"能用什么工具\""
	case "Streaming Runtime":
		return "缓冲+增量聚合+单流输出", "\"怎么流式输出\""
	case "Telemetry Runtime":
		return "Token/Cost/Latency/执行树", "\"发生了什么\""
	case "Workflow Runtime":
		return "DAG调度/并行/顺序/重试", "\"怎么调度任务\""
	case "Plugin Runtime":
		return "第三方插件SDK", "\"怎么扩展\""
	case "Evolver Runtime":
		return "闭环诊断/沉淀/测试/评测/提案/持久化/安全自修复", "\"怎么自我进化\""
	default:
		return "TBD", "\"TBD\""
	}
}

func renderRuntimeCatalogBlock(cat *RuntimeCatalog) string {
	var sb strings.Builder
	sb.WriteString("| Runtime | Path | Exists | Tests | HostWired | Inferred | Handbook Status |\n")
	sb.WriteString("|---|---|---|---|---|---|---|\n")
	for _, e := range cat.Entries {
		hb := e.HandbookStatus
		if hb == "" {
			hb = "(missing)"
		}
		// Avoid backtick-wrapping missing paths so diagnoseCodePaths does not
		// flag planned packages as broken handbook references.
		pathCell := e.PrimaryPath
		if e.PathExists {
			pathCell = "`" + e.PrimaryPath + "`"
		} else if pathCell == "" {
			pathCell = "(none)"
		}
		sb.WriteString(fmt.Sprintf("| %s | %s | %v | %v | %v | %s | %s |\n",
			e.Name, pathCell, e.PathExists, e.HasTests, e.HostWired, e.Inferred, hb))
	}
	sb.WriteString("\n> Auto-generated by Evolver Runtime Catalog (ADR-032). Do not hand-edit between markers.\n")
	return strings.TrimRight(sb.String(), "\n")
}

// FormatAutoWriteback returns a human-readable summary of applied/skipped items.
func (r *AutoWritebackResult) FormatAutoWriteback() string {
	if r == nil {
		return "No auto-writeback result.\n"
	}
	var sb strings.Builder
	if len(r.Applied) == 0 && len(r.Skipped) == 0 {
		sb.WriteString("Auto-writeback: nothing to apply.\n")
		return sb.String()
	}
	if len(r.Applied) > 0 {
		sb.WriteString("Auto-writeback applied:\n")
		for _, item := range r.Applied {
			sb.WriteString(fmt.Sprintf("  - %s [%s]: %s\n", item.Chapter, item.Action, item.Detail))
		}
	}
	if len(r.Skipped) > 0 {
		sb.WriteString("Auto-writeback skipped (non-deterministic):\n")
		for _, item := range r.Skipped {
			sb.WriteString(fmt.Sprintf("  - %s [%s]: %s\n", item.Chapter, item.Action, item.Detail))
		}
	}
	return sb.String()
}
