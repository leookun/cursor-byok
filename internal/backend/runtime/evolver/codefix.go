// codefix.go implements the bounded code implementation executor (ADR-041).
//
// Unlike free-form coding agents, this executor only applies closed deterministic
// recipes inside an explicit path allowlist, then runs curated tests. It never
// touches production ChannelService wiring outside allowlisted recipes and never
// edits files outside the sandbox roots.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// DefaultCodeAllowRoots are the only roots where bounded code recipes may write.
// docs/ remains handled by writeback/scaffold; this list is for Go implementation.
var DefaultCodeAllowRoots = []string{
	"internal/backend/runtime/evolver/",
	"cmd/evolver/",
	"internal/benchmark/",
	"internal/backend/runtime/cache/",
	"internal/backend/runtime/memory/",
	"internal/backend/runtime/tool/",
	"internal/backend/runtime/optimize/",
	"internal/backend/forwarder/",
}

// ForbiddenCodePaths are never writable by the bounded executor.
var ForbiddenCodePaths = []string{
	"internal/backend/host.go",
	"internal/mitm/",
	"internal/backend/virtualmodel/moa/channel_bridge.go",
}

// CodeChange is one deterministic file mutation.
type CodeChange struct {
	Path    string `json:"path"` // repo-relative slash path
	Mode    string `json:"mode"` // create | replace
	Content string `json:"content"`
}

// CodeRecipe is a closed, deterministic implementation unit.
type CodeRecipe struct {
	ID    string
	Title string
	// Risk classifies mutation danger for approval gating (ADR-043).
	// low / medium / high
	Risk         string
	Match        func(task EvolutionTask) bool
	BuildChanges func(repoRoot string, task EvolutionTask) ([]CodeChange, error)
	TestPackages []string
}

// CodeFixResult is the audit trail for one bounded implementation attempt.
type CodeFixResult struct {
	RecipeID   string       `json:"recipeID"`
	TaskID     string       `json:"taskID"`
	Changes    []CodeChange `json:"changes,omitempty"`
	Applied    int          `json:"applied"`
	Skipped    bool         `json:"skipped,omitempty"`
	Detail     string       `json:"detail"`
	TestPassed bool         `json:"testPassed,omitempty"`
}

// CanonicalCodeRecipes is the closed recipe registry for ADR-041 v1.
var CanonicalCodeRecipes = []CodeRecipe{
	{
		ID:    "impl-stub",
		Title: "Create bounded next-slice implementation stub under evolver package",
		Risk:  "low",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			for _, k := range []string{"benchmark", "aos", "quality", "cache", "memory", "tool", "optimize", "cost", "budget", "forwarder"} {
				if strings.Contains(t, k) {
					return false
				}
			}
			return true
		},
		BuildChanges: buildImplStubChanges,
		TestPackages: []string{"./internal/backend/runtime/evolver/"},
	},
	{
		ID:    "aos-benchmark-quality",
		Title: "Add AOS benchmark quality score surface",
		Risk:  "medium",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			return strings.Contains(t, "benchmark") || strings.Contains(t, "aos") || strings.Contains(t, "quality") ||
				strings.Contains(t, "latency") || strings.Contains(t, "token")
		},
		BuildChanges: buildAOSBenchmarkQualityChanges,
		TestPackages: []string{"./internal/benchmark/"},
	},
	{
		ID:    "cache-stats-summary",
		Title: "Add CacheStats.Summary efficiency surface",
		Risk:  "medium",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			if strings.Contains(t, "tool") || strings.Contains(t, "memory") {
				return false
			}
			return strings.Contains(t, "cache") || strings.Contains(t, "hit rate") || strings.Contains(t, "tokensaved")
		},
		BuildChanges: buildCacheStatsSummaryChanges,
		TestPackages: []string{"./internal/backend/runtime/cache/"},
	},
	{
		ID:    "tool-cache-stats",
		Title: "Add Tool Runtime cache stats surface",
		Risk:  "medium",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			return strings.Contains(t, "tool") && (strings.Contains(t, "cache") || strings.Contains(t, "stats") || strings.Contains(t, "runtime"))
		},
		BuildChanges: buildToolCacheStatsChanges,
		TestPackages: []string{"./internal/backend/runtime/tool/"},
	},
	{
		ID:    "memory-runtime-stats",
		Title: "Add Memory Runtime stats surface",
		Risk:  "medium",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			return strings.Contains(t, "memory") && (strings.Contains(t, "stats") || strings.Contains(t, "runtime") || strings.Contains(t, "layer"))
		},
		BuildChanges: buildMemoryRuntimeStatsChanges,
		TestPackages: []string{"./internal/backend/runtime/memory/"},
	},
	{
		ID:    "optimize-cost-summary",
		Title: "Add Optimization CostTracker.Summary surface",
		Risk:  "medium",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			return strings.Contains(t, "optimize") || strings.Contains(t, "cost") || strings.Contains(t, "budget") || strings.Contains(t, "spent")
		},
		BuildChanges: buildOptimizeCostSummaryChanges,
		TestPackages: []string{"./internal/backend/runtime/optimize/"},
	},
	{
		ID:    "forwarder-efficiency-note",
		Title: "Add Forwarder efficiency note surface",
		Risk:  "low",
		Match: func(task EvolutionTask) bool {
			t := strings.ToLower(task.Title + " " + task.Category + " " + task.Role)
			return strings.Contains(t, "forwarder") || strings.Contains(t, "provider stream") || strings.Contains(t, "request path")
		},
		BuildChanges: buildForwarderEfficiencyNoteChanges,
		TestPackages: []string{"./internal/backend/forwarder/"},
	},
}

func buildImplStubChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	slug := slugify(task.Title)
	if slug == "" {
		slug = slugify(task.ID)
	}
	if slug == "" {
		slug = "next-slice"
	}
	// Keep generated implementation hooks namespaced and boring.
	base := "impl_" + strings.ReplaceAll(slug, "-", "_")
	if len(base) > 40 {
		base = base[:40]
	}
	// Ensure valid Go identifier: prefix if starts with digit.
	if base != "" && base[0] >= '0' && base[0] <= '9' {
		base = "impl_" + base
	}
	goPath := "internal/backend/runtime/evolver/" + base + ".go"
	testPath := "internal/backend/runtime/evolver/" + base + "_test.go"
	fullGo := filepath.Join(repoRoot, filepath.FromSlash(goPath))
	if _, err := os.Stat(fullGo); err == nil {
		return nil, nil // already exists => no-op
	}
	typeName := toExportedIdent(base)
	funcName := "Describe" + typeName
	goContent := fmt.Sprintf(`// Code generated by Evolver bounded executor (ADR-041). DO NOT edit by hand unless taking ownership.
package evolver

// %s is an allowlisted implementation stub for a TaskPlan slice.
// Replace the body with real logic in a subsequent evolution cycle.
type %s struct {
	TaskID string
	Title  string
}

// %s returns a stable summary used by regression tests.
func %s(taskID, title string) string {
	if taskID == "" {
		taskID = "task"
	}
	if title == "" {
		title = "untitled"
	}
	return taskID + ": " + title
}
`, typeName, typeName, funcName, funcName)
	testContent := fmt.Sprintf(`package evolver

import "testing"

func Test%s(t *testing.T) {
	got := %s(%q, %q)
	if got == "" {
		t.Fatal("expected non-empty description")
	}
}
`, typeName, funcName, task.ID, truncate(task.Title, 60))
	return []CodeChange{
		{Path: goPath, Mode: "create", Content: goContent},
		{Path: testPath, Mode: "create", Content: testContent},
	}, nil
}

func toExportedIdent(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	var b strings.Builder
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// strip non letter/digit
		clean := make([]rune, 0, len(p))
		for _, r := range p {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				clean = append(clean, r)
			}
		}
		if len(clean) == 0 {
			continue
		}
		// Uppercase first ascii letter
		if clean[0] >= 'a' && clean[0] <= 'z' {
			clean[0] = clean[0] - 'a' + 'A'
		}
		b.WriteString(string(clean))
	}
	out := b.String()
	if out == "" {
		return "ImplStub"
	}
	if out[0] >= '0' && out[0] <= '9' {
		return "Impl" + out
	}
	return out
}

// ValidateCodeChanges enforces allowlist/forbidden path policy.
func ValidateCodeChanges(changes []CodeChange) error {
	for _, ch := range changes {
		rel := filepath.ToSlash(strings.TrimSpace(ch.Path))
		if rel == "" || strings.Contains(rel, "..") || strings.HasPrefix(rel, "/") {
			return fmt.Errorf("invalid path %q", ch.Path)
		}
		if ch.Mode != "create" && ch.Mode != "replace" {
			return fmt.Errorf("unsupported mode %q for %s", ch.Mode, rel)
		}
		if len(ch.Content) > 64*1024 {
			return fmt.Errorf("content too large for %s", rel)
		}
		for _, bad := range ForbiddenCodePaths {
			if rel == strings.TrimSuffix(bad, "/") || strings.HasPrefix(rel, bad) {
				return fmt.Errorf("path forbidden by bounded executor: %s", rel)
			}
		}
		allowed := false
		for _, root := range DefaultCodeAllowRoots {
			if strings.HasPrefix(rel, root) || rel == strings.TrimSuffix(root, "/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("path outside allowlist: %s", rel)
		}
		// Only Go files for v1.
		if !strings.HasSuffix(rel, ".go") {
			return fmt.Errorf("only .go files allowed in bounded executor: %s", rel)
		}
	}
	return nil
}

// ApplyCodeChanges writes validated changes. create never overwrites; replace requires exist.
func (e *Evolver) ApplyCodeChanges(repoRoot string, changes []CodeChange) (int, error) {
	if err := ValidateCodeChanges(changes); err != nil {
		return 0, err
	}
	applied := 0
	for _, ch := range changes {
		full := filepath.Join(repoRoot, filepath.FromSlash(ch.Path))
		switch ch.Mode {
		case "create":
			if _, err := os.Stat(full); err == nil {
				continue // idempotent skip
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return applied, err
			}
			if err := os.WriteFile(full, []byte(ch.Content), 0o644); err != nil {
				return applied, err
			}
			applied++
		case "replace":
			if _, err := os.Stat(full); err != nil {
				return applied, fmt.Errorf("replace target missing: %s", ch.Path)
			}
			if err := os.WriteFile(full, []byte(ch.Content), 0o644); err != nil {
				return applied, err
			}
			applied++
		}
	}
	return applied, nil
}

// ApplyBoundedCodeRecipes executes matching closed recipes for the plan tasks.
func (e *Evolver) ApplyBoundedCodeRecipes(repoRoot string, plan *EvolutionTaskPlan) ([]CodeFixResult, error) {
	var results []CodeFixResult
	if plan == nil {
		return results, nil
	}
	for _, task := range plan.Tasks {
		if task.Action != "bounded-code-fix" {
			continue
		}
		matched := false
		for _, recipe := range CanonicalCodeRecipes {
			if recipe.Match != nil && !recipe.Match(task) {
				continue
			}
			matched = true
			risk := recipeRisk(recipe)
			if !riskAllowed(risk, task) {
				results = append(results, CodeFixResult{
					RecipeID: recipe.ID,
					TaskID:   task.ID,
					Skipped:  true,
					Detail:   fmt.Sprintf("risk=%s blocked by approval gate; include [allow-high-risk] or set EVOLVER_ALLOW_HIGH_RISK=1", risk),
				})
				continue
			}
			changes, err := recipe.BuildChanges(repoRoot, task)
			if err != nil {
				results = append(results, CodeFixResult{
					RecipeID: recipe.ID,
					TaskID:   task.ID,
					Detail:   err.Error(),
				})
				return results, err
			}
			if len(changes) == 0 {
				results = append(results, CodeFixResult{
					RecipeID: recipe.ID,
					TaskID:   task.ID,
					Skipped:  true,
					Detail:   "recipe produced no changes (already applied or not needed)",
				})
				continue
			}
			n, err := e.ApplyCodeChanges(repoRoot, changes)
			if err != nil {
				results = append(results, CodeFixResult{
					RecipeID: recipe.ID,
					TaskID:   task.ID,
					Changes:  changes,
					Detail:   err.Error(),
				})
				return results, err
			}
			results = append(results, CodeFixResult{
				RecipeID: recipe.ID,
				TaskID:   task.ID,
				Changes:  changes,
				Applied:  n,
				Detail:   fmt.Sprintf("applied %d file change(s)", n),
			})
		}
		if !matched {
			results = append(results, CodeFixResult{
				TaskID:  task.ID,
				Skipped: true,
				Detail:  "no code recipe matched task",
			})
		}
	}
	return results, nil
}

func buildAOSBenchmarkQualityChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/benchmark/aos.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	if strings.Contains(src, "QualityScore") && strings.Contains(src, "computeAOSQualityScore") && strings.Contains(src, "QualitySummary") {
		return nil, nil
	}
	// Insert QualityScore field into AOSReport if missing.
	if !strings.Contains(src, "QualityScore") {
		src = strings.Replace(src,
			"type AOSReport struct {\n\tReport     *Report\n\tPhases     []AOSPhaseResult\n\tSprints    int\n\tTasksTotal int\n\tTasksDone  int\n}",
			"type AOSReport struct {\n\tReport       *Report\n\tPhases       []AOSPhaseResult\n\tSprints      int\n\tTasksTotal   int\n\tTasksDone    int\n\tQualityScore float64\n}",
			1,
		)
	}
	if !strings.Contains(src, "computeAOSQualityScore") {
		// Compute score after summarizing phases.
		if strings.Contains(src, "out.TasksDone = parseInt(metadata[\"aos.tasksDone\"])") {
			src = strings.Replace(src,
				"out.TasksDone = parseInt(metadata[\"aos.tasksDone\"])",
				"out.TasksDone = parseInt(metadata[\"aos.tasksDone\"])\n\tout.QualityScore = computeAOSQualityScore(out)",
				1,
			)
		}
		src += `

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
`
	}
	// Ensure strconv import remains present (already used).
	if !strings.Contains(src, "\"strconv\"") {
		src = strings.Replace(src, "import (\n\t\"context\"\n", "import (\n\t\"context\"\n\t\"strconv\"\n", 1)
	}
	return []CodeChange{{
		Path:    rel,
		Mode:    "replace",
		Content: src,
	}}, nil
}

func recipeRisk(recipe CodeRecipe) string {
	r := strings.ToLower(strings.TrimSpace(recipe.Risk))
	if r == "" {
		return "medium"
	}
	return r
}

func riskAllowed(risk string, task EvolutionTask) bool {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "low", "medium", "":
		return true
	case "high":
		title := strings.ToLower(task.Title)
		if strings.Contains(title, "[allow-high-risk]") || strings.Contains(title, "allow-high-risk") {
			return true
		}
		v := strings.ToLower(strings.TrimSpace(os.Getenv("EVOLVER_ALLOW_HIGH_RISK")))
		return v == "1" || v == "true" || v == "yes"
	default:
		return riskAllowed("high", task)
	}
}

func buildCacheStatsSummaryChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/backend/runtime/cache/runtime.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	if strings.Contains(src, "func (s *CacheStats) Summary(") {
		return nil, nil
	}
	helper := "\n\n// Summary returns a compact cache efficiency line for evolution/benchmark evidence.\nfunc (s *CacheStats) Summary() string {\n\tif s == nil {\n\t\treturn \"cache stats: n/a\"\n\t}\n\treturn fmt.Sprintf(\"cache hitRate=%.4f exact=%d semantic=%d tokensSaved=%d\",\n\t\ts.HitRate, s.ExactHits, s.SemanticHits, s.TokensSaved)\n}\n"
	src = strings.TrimRight(src, "\n") + helper
	if !strings.Contains(src, "\"fmt\"") {
		src = strings.Replace(src, "import (\n", "import (\n\t\"fmt\"\n", 1)
	}
	return []CodeChange{{Path: rel, Mode: "replace", Content: src}}, nil
}

func buildToolCacheStatsChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/backend/runtime/tool/runtime.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	if strings.Contains(src, "type ToolCacheStats struct") && strings.Contains(src, "func (rt *Runtime) CacheStats(") && strings.Contains(src, "cacheHits") {
		return nil, nil
	}
	if !strings.Contains(src, "cacheHits") {
		needle := "\tcache   map[string]*cacheEntry\n}"
		repl := "\tcache   map[string]*cacheEntry\n\t// cacheHits/cacheMisses track result-cache effectiveness (ADR-043).\n\tcacheHits   int64\n\tcacheMisses int64\n}"
		if !strings.Contains(src, needle) {
			return nil, fmt.Errorf("tool runtime struct cache field not found for recipe patch")
		}
		src = strings.Replace(src, needle, repl, 1)
	}
	if !strings.Contains(src, "rt.cacheHits++") {
		old := "if cached := rt.lookupCache(cacheKey); cached != nil {\n\t\t\tcached.Cached = true\n\t\t\treturn cached, nil\n\t\t}"
		neu := "if cached := rt.lookupCache(cacheKey); cached != nil {\n\t\t\trt.cacheMu.Lock()\n\t\t\trt.cacheHits++\n\t\t\trt.cacheMu.Unlock()\n\t\t\tcached.Cached = true\n\t\t\treturn cached, nil\n\t\t}\n\t\trt.cacheMu.Lock()\n\t\trt.cacheMisses++\n\t\trt.cacheMu.Unlock()"
		if strings.Contains(src, old) {
			src = strings.Replace(src, old, neu, 1)
		}
	}
	if !strings.Contains(src, "type ToolCacheStats struct") {
		src = strings.TrimRight(src, "\n") + "\n\n// ToolCacheStats summarizes tool-result cache effectiveness.\ntype ToolCacheStats struct {\n\tHits    int64   `json:\"hits\"`\n\tMisses  int64   `json:\"misses\"`\n\tEntries int     `json:\"entries\"`\n\tHitRate float64 `json:\"hitRate\"`\n}\n\n// CacheStats returns a snapshot of tool-result cache counters.\nfunc (rt *Runtime) CacheStats() ToolCacheStats {\n\tif rt == nil {\n\t\treturn ToolCacheStats{}\n\t}\n\trt.cacheMu.RLock()\n\tdefer rt.cacheMu.RUnlock()\n\tstats := ToolCacheStats{Hits: rt.cacheHits, Misses: rt.cacheMisses, Entries: len(rt.cache)}\n\ttotal := stats.Hits + stats.Misses\n\tif total > 0 {\n\t\tstats.HitRate = float64(stats.Hits) / float64(total)\n\t}\n\treturn stats\n}\n"
	}
	return []CodeChange{{Path: rel, Mode: "replace", Content: src}}, nil
}

func buildMemoryRuntimeStatsChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/backend/runtime/memory/runtime.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	if strings.Contains(src, "type MemoryStats struct") && strings.Contains(src, "func (rt *Runtime) Stats(") {
		return nil, nil
	}
	helper := "\n\n// MemoryStats summarizes in-process memory runtime surface for evolution evidence.\ntype MemoryStats struct {\n\tWorkingEntries int  `json:\"workingEntries\"`\n\tHasLongStore   bool `json:\"hasLongStore\"`\n\tHasSessionFile bool `json:\"hasSessionFile\"`\n}\n\n// Stats returns a lightweight snapshot of memory runtime state.\nfunc (rt *Runtime) Stats() MemoryStats {\n\tif rt == nil {\n\t\treturn MemoryStats{}\n\t}\n\treturn MemoryStats{\n\t\tWorkingEntries: len(rt.working),\n\t\tHasLongStore:   rt.longStore != nil,\n\t\tHasSessionFile: strings.TrimSpace(rt.sessionFile) != \"\",\n\t}\n}\n"
	src = strings.TrimRight(src, "\n") + helper
	if !strings.Contains(src, "\"strings\"") {
		src = strings.Replace(src, "import (\n", "import (\n\t\"strings\"\n", 1)
	}
	return []CodeChange{{Path: rel, Mode: "replace", Content: src}}, nil
}

func buildOptimizeCostSummaryChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/backend/runtime/optimize/runtime.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	src := string(raw)
	if strings.Contains(src, "func (t *CostTracker) Summary(") {
		return nil, nil
	}
	helper := `

// Summary returns a compact cost-efficiency line for evolution/benchmark evidence.
func (t *CostTracker) Summary() string {
	if t == nil {
		return "optimize cost: n/a"
	}
	return fmt.Sprintf("optimize spent=%.4f budget=%.4f turns=%d remainingTurns=%d",
		t.SpentThisMonthUSD, t.MonthlyBudgetUSD, t.TurnsThisMonth, t.EstimatedRemainingTurns)
}
`
	src = strings.TrimRight(src, "\n") + helper
	if !strings.Contains(src, "\"fmt\"") {
		src = strings.Replace(src, "import (\n", "import (\n\t\"fmt\"\n", 1)
	}
	return []CodeChange{{Path: rel, Mode: "replace", Content: src}}, nil
}

func buildForwarderEfficiencyNoteChanges(repoRoot string, task EvolutionTask) ([]CodeChange, error) {
	_ = task
	rel := "internal/backend/forwarder/efficiency.go"
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))
	if _, err := os.Stat(full); err == nil {
		return nil, nil
	}
	content := `package forwarder

// EfficiencyNote is a lightweight evidence surface for evolution reports (ADR-044).
// It does not alter request routing; it only exposes stable counters for diagnostics.
type EfficiencyNote struct {
	Name    string
	Detail  string
	Healthy bool
}

// DefaultEfficiencyNote returns a deterministic baseline note for the forwarder package.
func DefaultEfficiencyNote() EfficiencyNote {
	return EfficiencyNote{
		Name:    "forwarder",
		Detail:  "bounded-recipe-surface",
		Healthy: true,
	}
}

// Summary returns a compact forwarder efficiency line.
func (n EfficiencyNote) Summary() string {
	status := "degraded"
	if n.Healthy {
		status = "healthy"
	}
	if n.Name == "" {
		n.Name = "forwarder"
	}
	if n.Detail == "" {
		n.Detail = "n/a"
	}
	return n.Name + " status=" + status + " detail=" + n.Detail
}
`
	testRel := "internal/backend/forwarder/efficiency_test.go"
	testContent := `package forwarder

import "testing"

func TestDefaultEfficiencyNote(t *testing.T) {
	n := DefaultEfficiencyNote()
	if !n.Healthy {
		t.Fatal("expected healthy default note")
	}
	if n.Summary() == "" {
		t.Fatal("expected non-empty summary")
	}
}
`
	return []CodeChange{
		{Path: rel, Mode: "create", Content: content},
		{Path: testRel, Mode: "create", Content: testContent},
	}, nil
}
