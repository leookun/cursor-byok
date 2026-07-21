// Package docguard validates living handbook / ADR / research index consistency.
// Used by agents and CI so process constitution stays aligned with on-disk artifacts.
package docguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RequiredHandbookChapters is the canonical handbook file set (README + 00–30).
// ponytail: no YAML frontmatter Version field in most files, so version consistency
// is skipped. The Version line ("> Version: vX.Y") exists in only 11/32 chapters and
// is embedded in markdown text, not structured metadata. A proper upgrade would
// add YAML frontmatter to all chapters, then validate version monotonicity.
var RequiredHandbookChapters = []string{
	"README.md",
	"00_Project_Constitution.md",
	"01_Project_Vision.md",
	"02_Core_Principles.md",
	"03_System_Architecture.md",
	"04_Runtime_Architecture.md",
	"05_Organization_Runtime.md",
	"06_Leader_Runtime.md",
	"07_Member_Runtime.md",
	"08_Workspace_Runtime.md",
	"09_Workflow_Runtime.md",
	"10_Context_Runtime.md",
	"11_Memory_Runtime.md",
	"12_Cache_Runtime.md",
	"13_Optimization_Runtime.md",
	"14_Tool_Runtime.md",
	"15_Streaming_Runtime.md",
	"16_Telemetry_Runtime.md",
	"17_Frontend_Architecture.md",
	"18_Backend_Architecture.md",
	"19_Config_System.md",
	"20_Virtual_Model.md",
	"21_Team_Protocol.md",
	"22_Prompt_Engineering.md",
	"23_Context_Engineering.md",
	"24_Research_Charter.md",
	"25_Engineering_Standards.md",
	"26_Testing_Standards.md",
	"27_Benchmark.md",
	"28_ADR_Guide.md",
	"29_Roadmap.md",
	"30_References.md",
}

// HardConstraintMarkers must appear in constitution or engineering standards materials.
var HardConstraintMarkers = []string{
	"ChannelService",
	"non-nil",
	"ModelAdapter",
	"MITM",
}

// WritebackMarkers must appear in process chapters / README (task-loop writeback rules).
var WritebackMarkers = []string{
	"docs/research/",
	"docs/adr/",
	"docs/reports/",
	"writeback",
}

// RepoRoot walks up from start until go.mod is found.
func RepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", start)
		}
		dir = parent
	}
}

// ListMarkdownBasenames returns sorted basenames of *.md files in dir.
// A missing directory is treated as empty so callers can handle staged docs deletion.
func ListMarkdownBasenames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// markdownEmpty reports whether dir contains no *.md files.
func markdownEmpty(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".md") {
			return false
		}
	}
	return true
}

// ADRIDsFromFilenames extracts numeric IDs like "001" from "001-title.md".
func ADRIDsFromFilenames(names []string) []string {
	re := regexp.MustCompile(`^(\d{3})-`)
	var ids []string
	for _, n := range names {
		m := re.FindStringSubmatch(n)
		if m != nil {
			ids = append(ids, m[1])
		}
	}
	sort.Strings(ids)
	return ids
}

// ADRIDsFromGuide extracts ADR-NNN ids referenced in 28_ADR_Guide content for existing files.
func ADRIDsFromGuide(content string) []string {
	re := regexp.MustCompile(`docs/adr/(\d{3})-`)
	seen := map[string]struct{}{}
	var ids []string
	for _, m := range re.FindAllStringSubmatch(content, -1) {
		id := m[1]
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ResearchNotesFromCharter extracts docs/research/*.md basenames mentioned in chapter 24.
func ResearchNotesFromCharter(content string) []string {
	re := regexp.MustCompile(`docs/research/([a-zA-Z0-9_-]+\.md)`)
	seen := map[string]struct{}{}
	var names []string
	for _, m := range re.FindAllStringSubmatch(content, -1) {
		name := m[1]
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	// Also match backtick-only notes in 24.7 list: `moa-together-ai.md`
	re2 := regexp.MustCompile("`([a-zA-Z0-9_-]+\\.md)`")
	for _, m := range re2.FindAllStringSubmatch(content, -1) {
		name := m[1]
		if strings.HasPrefix(name, "ADR") {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		// Only count research-style note names (skip handbook chapter names if any)
		if strings.Contains(name, "_") && strings.HasPrefix(name, "0") {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ContainsAll reports which markers are missing from text (case-sensitive substring).
func ContainsAll(text string, markers []string) (missing []string) {
	for _, m := range markers {
		if !strings.Contains(text, m) {
			missing = append(missing, m)
		}
	}
	return missing
}

// CheckResult is a structured handbook consistency report.
type CheckResult struct {
	OK       bool
	Problems []string
}

// CheckHandbookConsistency validates ADR/research indexes and process markers against disk.
func CheckHandbookConsistency(repoRoot string) CheckResult {
	var problems []string

	handbookDir := filepath.Join(repoRoot, "docs", "handbook")
	adrDir := filepath.Join(repoRoot, "docs", "adr")
	researchDir := filepath.Join(repoRoot, "docs", "research")

	// ponytail: if the docs tree is intentionally empty, docguard has nothing to validate.
	if markdownEmpty(handbookDir) && markdownEmpty(adrDir) && markdownEmpty(researchDir) {
		return CheckResult{OK: true}
	}

	for _, name := range RequiredHandbookChapters {
		path := filepath.Join(handbookDir, name)
		if _, err := os.Stat(path); err != nil {
			problems = append(problems, fmt.Sprintf("missing handbook chapter: %s", name))
		}
	}

	// Reverse check: every .md file on disk must be listed as required.
	diskFiles, err := ListMarkdownBasenames(handbookDir)
	if err != nil {
		problems = append(problems, "list docs/handbook: "+err.Error())
	} else {
		requiredSet := make(map[string]struct{}, len(RequiredHandbookChapters))
		for _, name := range RequiredHandbookChapters {
			requiredSet[name] = struct{}{}
		}
		for _, f := range diskFiles {
			if _, ok := requiredSet[f]; !ok {
				problems = append(problems, fmt.Sprintf("handbook file %s on disk but not in RequiredHandbookChapters", f))
			}
		}
	}

	// Hard constraints: constitution + engineering standards combined
	constitution, err := os.ReadFile(filepath.Join(handbookDir, "00_Project_Constitution.md"))
	if err != nil {
		problems = append(problems, "cannot read 00_Project_Constitution.md: "+err.Error())
	}
	eng, err := os.ReadFile(filepath.Join(handbookDir, "25_Engineering_Standards.md"))
	if err != nil {
		problems = append(problems, "cannot read 25_Engineering_Standards.md: "+err.Error())
	}
	constText := string(constitution) + "\n" + string(eng)
	if missing := ContainsAll(constText, HardConstraintMarkers); len(missing) > 0 {
		problems = append(problems, "hard constraint markers missing from 00+25: "+strings.Join(missing, ", "))
	}
	// Explicit production ChannelService rule
	if !strings.Contains(constText, "ChannelService") || !strings.Contains(strings.ToLower(constText), "non-nil") {
		problems = append(problems, "ChannelService production non-nil rule missing from 00+25")
	}

	// Writeback markers across README + process chapters
	var processBlob strings.Builder
	for _, name := range []string{
		"README.md",
		"00_Project_Constitution.md",
		"24_Research_Charter.md",
		"27_Benchmark.md",
		"28_ADR_Guide.md",
		"29_Roadmap.md",
		"30_References.md",
	} {
		b, err := os.ReadFile(filepath.Join(handbookDir, name))
		if err != nil {
			problems = append(problems, "cannot read "+name+": "+err.Error())
			continue
		}
		processBlob.Write(b)
		processBlob.WriteByte('\n')
	}
	if missing := ContainsAll(processBlob.String(), WritebackMarkers); len(missing) > 0 {
		problems = append(problems, "writeback markers missing from process chapters: "+strings.Join(missing, ", "))
	}

	// ADR disk vs 28 index: only validate when there are ADRs on disk.
	if !markdownEmpty(adrDir) {
		adrFiles, err := ListMarkdownBasenames(adrDir)
		if err != nil {
			problems = append(problems, "list docs/adr: "+err.Error())
		} else {
			guide, err := os.ReadFile(filepath.Join(handbookDir, "28_ADR_Guide.md"))
			if err != nil {
				problems = append(problems, "read 28_ADR_Guide.md: "+err.Error())
			} else {
				diskIDs := ADRIDsFromFilenames(adrFiles)
				guideIDs := ADRIDsFromGuide(string(guide))
				// Every on-disk ADR must appear in guide paths
				guideSet := map[string]struct{}{}
				for _, id := range guideIDs {
					guideSet[id] = struct{}{}
				}
				for _, id := range diskIDs {
					if _, ok := guideSet[id]; !ok {
						problems = append(problems, fmt.Sprintf("ADR-%s on disk but missing from 28 index paths", id))
					}
				}
				// Guide paths must exist on disk
				diskSet := map[string]struct{}{}
				for _, id := range diskIDs {
					diskSet[id] = struct{}{}
				}
				for _, id := range guideIDs {
					if _, ok := diskSet[id]; !ok {
						problems = append(problems, fmt.Sprintf("ADR-%s in 28 index but file missing under docs/adr/", id))
					}
				}
			}
		}
	}

	// Research notes on disk must be indexed in chapter 24; skip if docs/research is empty.
	if !markdownEmpty(researchDir) {
		researchFiles, err := ListMarkdownBasenames(researchDir)
		if err != nil {
			problems = append(problems, "list docs/research: "+err.Error())
		} else {
			charter, err := os.ReadFile(filepath.Join(handbookDir, "24_Research_Charter.md"))
			if err != nil {
				problems = append(problems, "read 24_Research_Charter.md: "+err.Error())
			} else {
				indexed := ResearchNotesFromCharter(string(charter))
				idxSet := map[string]struct{}{}
				for _, n := range indexed {
					idxSet[n] = struct{}{}
				}
				// Also accept bare basename mentions without path
				charterText := string(charter)
				for _, f := range researchFiles {
					if _, ok := idxSet[f]; ok {
						continue
					}
					if strings.Contains(charterText, f) {
						continue
					}
					problems = append(problems, fmt.Sprintf("research note %s on disk but not listed in 24", f))
				}
			}
		}
	}

	return CheckResult{
		OK:       len(problems) == 0,
		Problems: problems,
	}
}
