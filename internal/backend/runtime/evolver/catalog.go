// catalog.go implements Runtime Catalog inventory and maturity inference for
// handbook/code co-evolution (ADR-032).
//
// It answers: which Runtimes exist on disk, whether they have tests, whether
// Host wires them, and what status chapter 04 §4.2 currently claims.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// RuntimeMaturity is a normalized, deterministic maturity label.
// Free-form handbook prose is mapped into these buckets for comparison.
type RuntimeMaturity string

const (
	MaturityMissing     RuntimeMaturity = "待建设"
	MaturityFramework   RuntimeMaturity = "框架就绪"
	MaturitySkeleton    RuntimeMaturity = "骨架就绪"
	MaturityProduction  RuntimeMaturity = "生产可用"
	MaturityDesigning   RuntimeMaturity = "设计中"
	MaturityUnknown     RuntimeMaturity = "未知"
)

// RuntimeSpec is the canonical registry entry for one Runtime in the living catalog.
type RuntimeSpec struct {
	// Name matches the Runtime column in docs/handbook/04_Runtime_Architecture.md §4.2.
	Name string
	// Paths are candidate code roots relative to repo root (any existing is enough).
	Paths []string
	// HostMarkers are substrings that, if present in host.go, mark production wiring.
	HostMarkers []string
	// Optional whether absence of code is expected (still listed in matrix).
	Optional bool
}

// RuntimeInventory is the on-disk + handbook observation for one Runtime.
type RuntimeInventory struct {
	Name            string          `json:"name"`
	PrimaryPath     string          `json:"primaryPath"`
	PathExists      bool            `json:"pathExists"`
	HasGoFiles      bool            `json:"hasGoFiles"`
	HasTests        bool            `json:"hasTests"`
	HostWired       bool            `json:"hostWired"`
	Inferred        RuntimeMaturity `json:"inferred"`
	HandbookStatus  string          `json:"handbookStatus,omitempty"`
	HandbookPresent bool            `json:"handbookPresent"`
	// Drift is a short machine-readable reason when handbook and code disagree.
	Drift string `json:"drift,omitempty"`
}

// RuntimeCatalog is the full inventory used by Diagnose / AutoWriteback / reports.
type RuntimeCatalog struct {
	Entries []RuntimeInventory `json:"entries"`
	// MatrixPath is the handbook file that owns the responsibility matrix.
	MatrixPath string `json:"matrixPath"`
}

// CanonicalRuntimeSpecs is the closed-world registry for catalog diagnosis.
// Keep names identical to chapter 04 §4.2 Runtime column.
var CanonicalRuntimeSpecs = []RuntimeSpec{
	{
		Name:        "Organization Runtime",
		Paths:       []string{"internal/backend/virtualmodel/aos", "internal/backend/virtualmodel/moa"},
		HostMarkers: []string{"virtualmodel", "buildVirtualModelManager", "NewMOAModel", "aos."},
	},
	{
		Name:        "Context Runtime",
		Paths:       []string{"internal/backend/runtime/context"},
		HostMarkers: []string{"contextruntime.NewRuntime", "context.NewRuntime"},
	},
	{
		Name:  "Memory Runtime",
		Paths: []string{"internal/backend/runtime/memory"},
		// Host constructs Context Runtime which embeds Memory Runtime.
		HostMarkers: []string{"memruntime.NewRuntime", "memory.NewRuntime", "contextruntime.NewRuntime"},
	},
	{
		Name:        "Cache Runtime",
		Paths:       []string{"internal/backend/runtime/cache"},
		HostMarkers: []string{"cacheruntime.NewRuntime", "cache.NewRuntime"},
	},
	{
		Name:        "Optimization Runtime",
		Paths:       []string{"internal/backend/runtime/optimize"},
		HostMarkers: []string{"optimize.NewRuntime", "NewRuntimeWithStore"},
	},
	{
		Name:        "Tool Runtime",
		Paths:       []string{"internal/backend/runtime/tool"},
		HostMarkers: []string{"toolruntime.NewRuntime", "tool.NewRuntime"},
	},
	{
		Name:        "Streaming Runtime",
		Paths:       []string{"internal/backend/runtime/streaming"},
		HostMarkers: []string{"streaming.NewRuntime"},
		Optional:    true,
	},
	{
		Name:        "Telemetry Runtime",
		Paths:       []string{"internal/backend/runtime/telemetry"},
		HostMarkers: []string{"telemetry.NewRuntime"},
	},
	{
		Name:        "Workflow Runtime",
		// Dedicated package only. AOS/MOA workflow helpers do not count as Workflow Runtime package.
		Paths:       []string{"internal/backend/runtime/workflow"},
		HostMarkers: []string{"workflowruntime.NewRuntime", "runtime/workflow"},
		Optional:    true,
	},
	{
		Name:        "Plugin Runtime",
		Paths:       []string{"internal/plugin", "internal/backend/runtime/plugin"},
		HostMarkers: []string{"plugin.", "internal/plugin"},
	},
	{
		Name:        "Evolver Runtime",
		Paths:       []string{"internal/backend/runtime/evolver"},
		HostMarkers: []string{"runBackgroundEvolutionCheck", "runtime/evolver", "evolver.NewEvolver"},
	},
}

var (
	// matrixRowPattern captures: | Runtime Name | duty | slogan | status |
	matrixRowPattern = regexp.MustCompile(`(?m)^\|\s*([^|]+?)\s*\|\s*([^|]*?)\s*\|\s*([^|]*?)\s*\|\s*([^|]*?)\s*\|\s*$`)
)

// InventoryRuntimes builds the Runtime Catalog from disk + host.go + chapter 04.
func (e *Evolver) InventoryRuntimes(repoRoot string) *RuntimeCatalog {
	cat := &RuntimeCatalog{
		MatrixPath: filepath.Join("docs", "handbook", "04_Runtime_Architecture.md"),
	}
	hostText := readFileString(filepath.Join(repoRoot, "internal", "backend", "host.go"))
	handbookStatus := e.parseChapter04Status(repoRoot)

	for _, spec := range CanonicalRuntimeSpecs {
		inv := RuntimeInventory{
			Name:            spec.Name,
			HandbookStatus:  handbookStatus[spec.Name],
			HandbookPresent: handbookStatus[spec.Name] != "",
		}
		// Resolve first existing path as primary.
		for _, rel := range spec.Paths {
			full := filepath.Join(repoRoot, filepath.FromSlash(rel))
			if st, err := os.Stat(full); err == nil && st.IsDir() {
				inv.PrimaryPath = rel
				inv.PathExists = true
				inv.HasGoFiles, inv.HasTests = scanGoPackage(full)
				break
			}
		}
		if inv.PrimaryPath == "" && len(spec.Paths) > 0 {
			inv.PrimaryPath = spec.Paths[0]
		}
		if hostText != "" {
			for _, m := range spec.HostMarkers {
				if m != "" && strings.Contains(hostText, m) {
					inv.HostWired = true
					break
				}
			}
		}
		inv.Inferred = inferMaturity(inv)
		inv.Drift = catalogDrift(inv)
		cat.Entries = append(cat.Entries, inv)
	}
	return cat
}

func readFileString(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func scanGoPackage(dir string) (hasGo, hasTests bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		hasGo = true
		if strings.HasSuffix(name, "_test.go") {
			hasTests = true
		}
	}
	// Also check one level of subdirs for packages that split files.
	if !hasTests {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			sub := filepath.Join(dir, entry.Name())
			subs, err := os.ReadDir(sub)
			if err != nil {
				continue
			}
			for _, s := range subs {
				if !s.IsDir() && strings.HasSuffix(s.Name(), ".go") {
					hasGo = true
					if strings.HasSuffix(s.Name(), "_test.go") {
						hasTests = true
					}
				}
			}
		}
	}
	return hasGo, hasTests
}

func inferMaturity(inv RuntimeInventory) RuntimeMaturity {
	if !inv.PathExists || !inv.HasGoFiles {
		return MaturityMissing
	}
	if inv.HostWired && inv.HasTests {
		return MaturityProduction
	}
	if inv.HostWired {
		return MaturityProduction
	}
	if inv.HasTests {
		return MaturitySkeleton
	}
	return MaturityFramework
}

func normalizeHandbookStatus(status string) RuntimeMaturity {
	s := strings.TrimSpace(status)
	switch {
	case s == "":
		return MaturityUnknown
	case strings.Contains(s, "待建设"):
		return MaturityMissing
	case strings.Contains(s, "设计中"):
		return MaturityDesigning
	case strings.Contains(s, "生产可用") || strings.Contains(s, "主链路") || strings.Contains(s, "精确缓存可用"):
		return MaturityProduction
	case strings.Contains(s, "骨架就绪"):
		return MaturitySkeleton
	case strings.Contains(s, "框架就绪"):
		return MaturityFramework
	default:
		// Phase N 生产可用 / longer production notes.
		if strings.Contains(s, "可用") || strings.Contains(s, "完成") {
			return MaturityProduction
		}
		return MaturityUnknown
	}
}

func catalogDrift(inv RuntimeInventory) string {
	hb := normalizeHandbookStatus(inv.HandbookStatus)
	if !inv.HandbookPresent {
		return "missing-from-matrix"
	}
	if inv.Inferred == MaturityMissing && hb != MaturityMissing && hb != MaturityDesigning {
		return "code-missing-status-claims-present"
	}
	if inv.Inferred != MaturityMissing && hb == MaturityMissing {
		return "code-present-status-still-missing"
	}
	// Production code but handbook still says framework/skeleton/designing.
	if inv.Inferred == MaturityProduction && (hb == MaturityFramework || hb == MaturitySkeleton || hb == MaturityDesigning) {
		return "status-understates-production"
	}
	// Handbook says production but code missing.
	if hb == MaturityProduction && inv.Inferred == MaturityMissing {
		return "status-overstates-missing-code"
	}
	return ""
}

// parseChapter04Status extracts Runtime -> status from §4.2 table.
func (e *Evolver) parseChapter04Status(repoRoot string) map[string]string {
	out := map[string]string{}
	path := filepath.Join(repoRoot, "docs", "handbook", "04_Runtime_Architecture.md")
	content, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	text := string(content)
	// Restrict to section 4.2 when possible.
	start := strings.Index(text, "## 4.2")
	end := strings.Index(text, "## 4.3")
	section := text
	if start >= 0 {
		if end > start {
			section = text[start:end]
		} else {
			section = text[start:]
		}
	}
	for _, m := range matrixRowPattern.FindAllStringSubmatch(section, -1) {
		name := strings.TrimSpace(m[1])
		status := strings.TrimSpace(m[4])
		if name == "" || name == "Runtime" || strings.HasPrefix(name, "---") {
			continue
		}
		// Skip separator rows like |---|
		if strings.HasPrefix(name, "---") || strings.Contains(name, "---") {
			continue
		}
		out[name] = status
	}
	return out
}

// diagnoseRuntimeCatalog appends catalog drift findings to the diagnosis report.
func (e *Evolver) diagnoseRuntimeCatalog(repoRoot string, report *DiagnosisReport) {
	cat := e.InventoryRuntimes(repoRoot)
	if cat == nil || len(cat.Entries) == 0 {
		report.add(SeverityInfo, "runtime-catalog", "runtime catalog inventory empty")
		return
	}
	drifts := 0
	for _, inv := range cat.Entries {
		if inv.Drift == "" {
			continue
		}
		drifts++
		msg := fmt.Sprintf("%s: %s (path=%s inferred=%s handbook=%q)",
			inv.Name, inv.Drift, inv.PrimaryPath, inv.Inferred, inv.HandbookStatus)
		// Missing matrix rows / status understating production are warnings.
		sev := SeverityWarning
		if inv.Drift == "status-overstates-missing-code" || inv.Drift == "code-missing-status-claims-present" {
			sev = SeverityWarning
		}
		report.add(sev, "runtime-catalog", msg)
	}
	if drifts == 0 {
		report.add(SeverityInfo, "runtime-catalog",
			fmt.Sprintf("runtime catalog consistent: %d runtimes inventoried", len(cat.Entries)))
	}
}

// FormatRuntimeCatalog returns a human-readable catalog section for evolution reports.
func (c *RuntimeCatalog) FormatRuntimeCatalog() string {
	if c == nil {
		return "=== Runtime Catalog ===\n(nil)\n"
	}
	var sb strings.Builder
	sb.WriteString("=== Runtime Catalog ===\n")
	sb.WriteString(fmt.Sprintf("Entries: %d\n", len(c.Entries)))
	for _, e := range c.Entries {
		flag := "OK"
		if e.Drift != "" {
			flag = "DRIFT"
		}
		sb.WriteString(fmt.Sprintf("  [%s] %s path=%s exists=%v tests=%v host=%v inferred=%s handbook=%q\n",
			flag, e.Name, e.PrimaryPath, e.PathExists, e.HasTests, e.HostWired, e.Inferred, e.HandbookStatus))
	}
	return sb.String()
}
