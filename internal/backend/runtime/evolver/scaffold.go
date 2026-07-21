// scaffold.go creates allowlisted documentation scaffolds for TaskPlan execution (ADR-040).
//
// Supported autonomous scaffolds:
//   - draft ADR under docs/adr/
//   - draft research note under docs/research/
//   - draft phase report under docs/reports/
//
// Never writes Go source. Never overwrites existing files. Always deterministic.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// ScaffoldADRFromResearch reads a research note and creates a Proposed ADR draft
// with Context extracted from the research body and a back-link to the source.
// Deterministic, no LLM. Returns (nil, nil) when ADR for that slug already exists.
// The researchPath can be absolute, relative to repoRoot, or a bare filename
// resolved under docs/research/.
func (e *Evolver) ScaffoldADRFromResearch(repoRoot, researchPath string) (*ScaffoldResult, error) {
	fullPath := resolveResearchPath(repoRoot, researchPath)
	body, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, fmt.Errorf("read research note %s: %w", researchPath, err)
	}

	title := extractResearchTitle(string(body))
	contextText := extractResearchContext(string(body))
	if title == "" {
		title = "ADR from Research"
	}
	if contextText == "" {
		contextText = "Scaffolded from research note; no structured context extracted."
	}
	if len(contextText) > 2000 {
		contextText = contextText[:2000] + "…"
	}

	slug := slugify(title)
	if slug == "" {
		slug = "from-research"
	}
	if existing := findADRBySlug(repoRoot, slug); existing != "" {
		return nil, nil // idempotent: already scaffolded
	}

	id, err := nextADRID(repoRoot)
	if err != nil {
		return nil, err
	}

	rel := filepath.ToSlash(filepath.Join("docs", "adr", fmt.Sprintf("%s-%s.md", id, slug)))
	full := filepath.Join(repoRoot, filepath.FromSlash(rel))

	// Compute a relative research path for the References section.
	var researchRel string
	if abs, err := filepath.Abs(fullPath); err == nil {
		if r, err := filepath.Rel(repoRoot, abs); err == nil {
			researchRel = filepath.ToSlash(r)
		} else {
			researchRel = researchPath
		}
	} else {
		researchRel = researchPath
	}

	date := time.Now().Format("2006-01-02")
	adrBody := fmt.Sprintf(`# ADR-%s: %s

## Status
Proposed

## Date
%s

## Context
%s

## Decision
TBD — research identifies the gap; formal decision deferred until ADR is reviewed.

## Consequences
TBD

## References
- %s
`, id, title, date, contextText, researchRel)

	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(full, []byte(adrBody), 0o644); err != nil {
		return nil, err
	}
	return &ScaffoldResult{Kind: "adr", Path: rel}, nil
}

// resolveResearchPath turns a user-supplied research path into an absolute path.
// Supports: bare filename ("foo.md"), basename-only ("foo"), docs/research/foo.md, absolute path.
func resolveResearchPath(repoRoot, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	// Try bare filename or relative under repoRoot.
	candidates := []string{
		p,
		filepath.Join(repoRoot, p),
		filepath.Join(repoRoot, "docs", "research", p),
	}
	// If no .md extension, try with it.
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		candidates = append(candidates,
			filepath.Join(repoRoot, p+".md"),
			filepath.Join(repoRoot, "docs", "research", p+".md"),
		)
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	// Return the most likely candidate for a useful error message downstream.
	return filepath.Join(repoRoot, "docs", "research", p)
}

// extractResearchTitle extracts the first h1 heading from a markdown body.
func extractResearchTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "# ") {
			return strings.TrimPrefix(trim, "# ")
		}
	}
	return ""
}

// extractResearchContext extracts the best available context paragraph(s) from
// a research note. Priority: ## Problem → ## Verified gap → ## Context →
// first substantial paragraph after the title. Deterministic, no LLM.
func extractResearchContext(body string) string {
	lines := strings.Split(body, "\n")
	targetSections := []string{"## Problem", "## Verified gap", "## Context", "## Decision"}
	for _, target := range targetSections {
		for i, line := range lines {
			if strings.TrimSpace(line) == target {
				var parts []string
				for j := i + 1; j < len(lines); j++ {
					if strings.HasPrefix(strings.TrimSpace(lines[j]), "## ") {
						break
					}
					t := strings.TrimSpace(lines[j])
					if t != "" {
						parts = append(parts, t)
					}
				}
				if len(parts) > 0 {
					return strings.Join(parts, " ")
				}
			}
		}
	}
	// Fallback: first non-empty, non-heading paragraph.
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "---") {
			return t
		}
	}
	return ""
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ScaffoldResult records one created documentation artifact.
type ScaffoldResult struct {
	Kind string `json:"kind"` // adr | research | report
	Path string `json:"path"`
}

// CreateAllowlistedScaffolds inspects the task plan and creates missing draft docs.
func (e *Evolver) CreateAllowlistedScaffolds(repoRoot string, plan *EvolutionTaskPlan) ([]ScaffoldResult, error) {
	var out []ScaffoldResult
	if plan == nil {
		return out, nil
	}
	needADR, needResearch, needReport := false, false, false
	var adrTitle, researchTitle, reportTitle string
	for _, task := range plan.Tasks {
		switch task.Action {
		case "scaffold-adr":
			needADR = true
			if adrTitle == "" {
				adrTitle = cleanTitle(task.Title, "Next Evolution Slice")
			}
		case "scaffold-research":
			needResearch = true
			if researchTitle == "" {
				researchTitle = cleanTitle(task.Title, "Next Evolution Research")
			}
		case "scaffold-report":
			needReport = true
			if reportTitle == "" {
				reportTitle = cleanTitle(task.Title, "Next Evolution Report")
			}
		}
	}
	date := time.Now().Format("2006-01-02")
	if needADR {
		slug := slugify(adrTitle)
		if slug == "" {
			slug = "next-slice"
		}
		if existing := findADRBySlug(repoRoot, slug); existing == "" {
			id, err := nextADRID(repoRoot)
			if err != nil {
				return out, err
			}
			rel := filepath.ToSlash(filepath.Join("docs", "adr", fmt.Sprintf("%s-%s.md", id, slug)))
			full := filepath.Join(repoRoot, filepath.FromSlash(rel))
			body := fmt.Sprintf(`# ADR-%s: %s

## Status
Proposed

## Date
%s

## Context
Auto-scaffolded by Evolver TaskPlan executor (ADR-040) from proposal priorities.
Replace this draft with evidence-based decision content before acceptance.

## Decision
TBD

## Consequences
TBD

## References
- docs/research/
- docs/handbook/00_Project_Constitution.md
`, id, adrTitle, date)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				return out, err
			}
			out = append(out, ScaffoldResult{Kind: "adr", Path: rel})
		}
	}
	if needResearch {
		slug := slugify(researchTitle)
		if slug == "" {
			slug = "next-research"
		}
		rel := filepath.ToSlash(filepath.Join("docs", "research", slug+".md"))
		full := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			body := fmt.Sprintf(`# %s

## Basic Info
- Date: %s
- Module: Auto-scaffolded by Evolver TaskPlan executor (ADR-040)

## Problem
TBD

## References
TBD

## Decision
TBD
`, researchTitle, date)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				return out, err
			}
			out = append(out, ScaffoldResult{Kind: "research", Path: rel})
		}
	}
	if needReport {
		slug := slugify(reportTitle)
		if slug == "" {
			slug = "next-slice"
		}
		rel := filepath.ToSlash(filepath.Join("docs", "reports", fmt.Sprintf("%s-phase-draft-%s.md", date, slug)))
		full := filepath.Join(repoRoot, filepath.FromSlash(rel))
		if _, err := os.Stat(full); os.IsNotExist(err) {
			body := fmt.Sprintf(`# Benchmark Report: %s %s

## Status
Draft scaffold (ADR-040)

## Results
| Check | Result |
|---|---|
| Draft created | PASS |

## Reproduction
`+"```bash"+`
go run ./cmd/evolver/ -execute
go run ./cmd/evolver/ -ci
`+"```"+`
`, date, reportTitle)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return out, err
			}
			if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
				return out, err
			}
			out = append(out, ScaffoldResult{Kind: "report", Path: rel})
		}
	}
	return out, nil
}

func nextADRID(repoRoot string) (string, error) {
	dir := filepath.Join(repoRoot, "docs", "adr")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "040", nil
		}
		return "", err
	}
	maxID := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 3 {
			continue
		}
		n := 0
		for i := 0; i < len(name) && i < 3; i++ {
			if name[i] < '0' || name[i] > '9' {
				n = -1
				break
			}
			n = n*10 + int(name[i]-'0')
		}
		if n > maxID {
			maxID = n
		}
	}
	return fmt.Sprintf("%03d", maxID+1), nil
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Keep ascii-ish slug; drop other runes.
	var b strings.Builder
	for _, r := range s {
		if r > unicode.MaxASCII {
			continue
		}
		b.WriteRune(r)
	}
	s = nonSlug.ReplaceAllString(b.String(), "-")
	s = strings.Trim(s, "-")
	if len(s) > 48 {
		s = s[:48]
		s = strings.Trim(s, "-")
	}
	return s
}

func cleanTitle(title, fallback string) string {
	title = strings.TrimSpace(title)
	// Strip common proposal prefixes.
	for _, p := range []string{
		"Resolve docguard drift:",
		"Resolve report-index drift:",
		"Fix",
		"Plan next evolution cycle based on Roadmap priorities",
	} {
		if strings.HasPrefix(title, p) && p != title {
			title = strings.TrimSpace(strings.TrimPrefix(title, p))
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fallback
	}
	// Avoid extremely long titles.
	if len(title) > 80 {
		title = title[:80]
	}
	return title
}

func findADRBySlug(repoRoot, slug string) string {
	dir := filepath.Join(repoRoot, "docs", "adr")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	suffix := "-" + slug + ".md"
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, suffix) {
			return filepath.ToSlash(filepath.Join("docs", "adr", name))
		}
	}
	return ""
}
