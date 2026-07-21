// bullet_foundations.go co-evolves bullet-style ?????? sections (ADR-036).
//
// Table foundations remain in foundations.go (ADR-034). This file handles prose
// bullet lists in chapters 11/12/13/14 where components are written as:
//
//   - `internal/...` ? description
//   - **Component?????**?...
//
// Safe writeback is intentionally narrow: only remove orphan fragment lines that
// are not valid bullets/headings and look like truncated leftovers.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// BulletFoundationSection is one handbook section using bullet prose foundations.
type BulletFoundationSection struct {
	Chapter     string
	Heading     string
	Description string
}

// CanonicalBulletFoundationSections is the closed list of bullet-style foundations.
var CanonicalBulletFoundationSections = []BulletFoundationSection{
	{Chapter: "11_Memory_Runtime.md", Heading: "## 11.3", Description: "Memory foundations"},
	{Chapter: "12_Cache_Runtime.md", Heading: "## 12.5", Description: "Cache foundations"},
	{Chapter: "13_Optimization_Runtime.md", Heading: "## 13.3", Description: "Optimization foundations"},
	{Chapter: "14_Tool_Runtime.md", Heading: "## 14.3", Description: "Tool foundations"},
}

// BulletFoundationItem is one parsed bullet or orphan line inside a foundation section.
type BulletFoundationItem struct {
	Chapter      string
	RawLine      string
	LineIndex    int
	IsBullet     bool
	IsOrphan     bool
	Component    string
	Paths        []string
	MissingPaths []string
	StatusHint   string
	Drift        string
}

// BulletFoundationInventory is the scan result for bullet foundations.
type BulletFoundationInventory struct {
	Items  []BulletFoundationItem `json:"items"`
	Drifts int                    `json:"drifts"`
}

var (
	bulletLinePattern = regexp.MustCompile(`^\s*[-*+]\s+`)
	inlineCodePattern = regexp.MustCompile("`([^`]+)`")
	// Status vocabulary uses rune concatenation so source encoding cannot corrupt patterns.
	statusDonePattern    = regexp.MustCompile(statusDoneRegex())
	statusPendingPattern = regexp.MustCompile(statusPendingRegex())
)

// InventoryBulletFoundations scans all canonical bullet foundation sections.
func (e *Evolver) InventoryBulletFoundations(repoRoot string) *BulletFoundationInventory {
	inv := &BulletFoundationInventory{}
	for _, sec := range CanonicalBulletFoundationSections {
		items := e.scanBulletFoundationSection(repoRoot, sec)
		for _, item := range items {
			if item.Drift != "" {
				inv.Drifts++
			}
			inv.Items = append(inv.Items, item)
		}
	}
	return inv
}

func (e *Evolver) scanBulletFoundationSection(repoRoot string, sec BulletFoundationSection) []BulletFoundationItem {
	path := filepath.Join(repoRoot, "docs", "handbook", sec.Chapter)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(content), "\n")
	start, end := sectionRange(lines, sec.Heading)
	if start < 0 {
		return nil
	}

	var items []BulletFoundationItem
	for i := start + 1; i < end; i++ {
		raw := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "---" || strings.HasPrefix(trimmed, "????") {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			// Skip fenced blocks if any appear inside foundations.
			i = skipFence(lines, i, end)
			continue
		}

		item := BulletFoundationItem{
			Chapter:   sec.Chapter,
			RawLine:   raw,
			LineIndex: i,
			IsBullet:  bulletLinePattern.MatchString(raw),
		}
		if !item.IsBullet {
			// Headings / section labels inside foundations are intentional prose.
			if strings.HasPrefix(trimmed, "#") ||
				strings.HasPrefix(trimmed, "**Phase") ||
				strings.HasPrefix(trimmed, "**Benchmark") ||
				(strings.HasPrefix(trimmed, "**") && strings.Contains(trimmed, "**")) {
				// Keep emphasis-only labels (implemented/pending/phase notes).
				// Only orphan fragments without markdown structure are repaired.
				if !looksLikeOrphanFragment(trimmed) {
					continue
				}
			}
			item.IsOrphan = looksLikeOrphanFragment(trimmed)
			if item.IsOrphan {
				item.Drift = "orphan-fragment"
			}
			items = append(items, item)
			continue
		}

		item.Component = bulletComponent(trimmed)
		item.Paths = extractPathClaims(trimmed)
		item.StatusHint = bulletStatusHint(trimmed)
		for _, p := range item.Paths {
			full := filepath.Join(repoRoot, filepath.FromSlash(p))
			if _, err := os.Stat(full); os.IsNotExist(err) {
				item.MissingPaths = append(item.MissingPaths, p)
			}
		}
		item.Drift = bulletDrift(item)
		items = append(items, item)
	}
	return items
}

func sectionRange(lines []string, headingPrefix string) (int, int) {
	start := -1
	for i, line := range lines {
		trim := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(trim, headingPrefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trim := strings.TrimSpace(strings.TrimRight(lines[i], "\r"))
		if strings.HasPrefix(trim, "## ") {
			end = i
			break
		}
	}
	return start, end
}

func skipFence(lines []string, i, end int) int {
	for j := i + 1; j < end; j++ {
		if strings.HasPrefix(strings.TrimSpace(strings.TrimRight(lines[j], "\r")), "```") {
			return j
		}
	}
	return end - 1
}

func looksLikeOrphanFragment(line string) bool {
	if line == "" {
		return false
	}
	// Valid bullets / list continuations are not orphans.
	if bulletLinePattern.MatchString(line) {
		return false
	}
	// Typical corruption: truncated method/path prose without list marker.
	if strings.HasPrefix(line, "ecord") || strings.HasPrefix(line, "orwarder") || strings.HasPrefix(line, "irtualmodel") {
		return true
	}
	// Line starts with lowercase letter and contains path-like or code tokens.
	r := []rune(line)
	if unicode.IsLower(r[0]) && (strings.Contains(line, "/") || strings.Contains(line, "(") || strings.Contains(line, "Memory") || strings.Contains(line, "ADR-")) {
		return true
	}
	return false
}

func bulletComponent(line string) string {
	line = bulletLinePattern.ReplaceAllString(line, "")
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "**") {
		if end := strings.Index(line[2:], "**"); end >= 0 {
			return strings.TrimSpace(line[2 : 2+end])
		}
	}
	if m := inlineCodePattern.FindStringSubmatch(line); len(m) == 2 {
		return m[1]
	}
	if parts := strings.SplitN(line, "?", 2); len(parts) > 0 {
		return strings.TrimSpace(parts[0])
	}
	return line
}

func extractPathClaims(line string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range inlineCodePattern.FindAllStringSubmatch(line, -1) {
		raw := strings.TrimSpace(m[1])
		if raw == "" {
			continue
		}
		// Skip non-path code tokens.
		if strings.Contains(raw, " ") || strings.HasPrefix(raw, "{") || strings.Contains(raw, "*") {
			continue
		}
		if !(strings.Contains(raw, "/") || strings.HasSuffix(raw, ".go") || strings.HasPrefix(raw, "internal/")) {
			continue
		}
		norm, ok := normalizeBulletPath(raw)
		if !ok || seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out
}

func normalizeBulletPath(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "`")
	s = strings.TrimSuffix(s, "/")
	s = strings.ReplaceAll(s, "\\", "/")
	if s == "" {
		return "", false
	}
	if strings.HasPrefix(s, "internal/") || strings.HasPrefix(s, "docs/") || strings.HasPrefix(s, "cmd/") {
		return s, true
	}
	// Relative backend paths in foundation bullets.
	if strings.HasPrefix(s, "runtime/") || strings.HasPrefix(s, "forwarder/") || strings.HasPrefix(s, "virtualmodel/") {
		return "internal/backend/" + s, true
	}
	if strings.HasSuffix(s, ".go") && !strings.Contains(s, "/") {
		return "", false
	}
	return "", false
}

func bulletStatusHint(line string) string {
	switch {
	case statusDonePattern.MatchString(line):
		return "done"
	case statusPendingPattern.MatchString(line):
		return "pending"
	default:
		return ""
	}
}

func bulletDrift(item BulletFoundationItem) string {
	if item.IsOrphan {
		return "orphan-fragment"
	}
	if len(item.MissingPaths) > 0 && item.StatusHint == "done" {
		return "path-missing-status-claims-present"
	}
	if len(item.MissingPaths) > 0 {
		return "path-missing"
	}
	// Pending status on an existing path is intentional for bullet foundations
	// (e.g. scaffold present, wiring still open). Do not treat it as drift.
	return ""
}

// diagnoseBulletFoundations emits foundation-bullet findings.
func (e *Evolver) diagnoseBulletFoundations(repoRoot string, report *DiagnosisReport) {
	inv := e.InventoryBulletFoundations(repoRoot)
	if inv == nil {
		return
	}
	for _, item := range inv.Items {
		if item.Drift == "" {
			continue
		}
		msg := fmt.Sprintf("%s:%d %s", item.Chapter, item.LineIndex+1, item.Drift)
		switch item.Drift {
		case "orphan-fragment":
			msg += ": " + sanitizeForMsg(strings.TrimSpace(item.RawLine))
		case "path-missing", "path-missing-status-claims-present":
			msg += ": " + strings.Join(item.MissingPaths, ", ")
		case "path-present-status-claims-missing":
			msg += ": " + strings.Join(item.Paths, ", ")
		}
		report.add(SeverityWarning, "foundation-bullet", msg)
	}
	if inv.Drifts == 0 {
		report.add(SeverityInfo, "foundation-bullet",
			fmt.Sprintf("bullet foundations consistent: %d items across %d chapters",
				len(inv.Items), len(CanonicalBulletFoundationSections)))
	}
}

// writebackBulletFoundations removes orphan fragment lines only.
func (e *Evolver) writebackBulletFoundations(repoRoot string) ([]WritebackItem, error) {
	var applied []WritebackItem
	for _, sec := range CanonicalBulletFoundationSections {
		path := filepath.Join(repoRoot, "docs", "handbook", sec.Chapter)
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return applied, fmt.Errorf("read %s: %w", sec.Chapter, err)
		}
		lines := strings.Split(string(content), "\n")
		items := e.scanBulletFoundationSection(repoRoot, sec)
		remove := map[int]bool{}
		for _, item := range items {
			if item.Drift == "orphan-fragment" {
				remove[item.LineIndex] = true
			}
		}
		if len(remove) == 0 {
			continue
		}
		var out []string
		removed := 0
		for i, line := range lines {
			if remove[i] {
				removed++
				continue
			}
			out = append(out, line)
		}
		if removed == 0 {
			continue
		}
		if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644); err != nil {
			return applied, fmt.Errorf("write %s: %w", sec.Chapter, err)
		}
		applied = append(applied, WritebackItem{
			Chapter: sec.Chapter,
			Action:  "sync-bullet-foundations",
			Detail:  fmt.Sprintf("removed %d orphan foundation fragment line(s)", removed),
		})
	}
	return applied, nil
}

func statusDoneRegex() string {
	// ???|???|????|PASS
	return string([]rune{0x5df2, 0x5b8c, 0x6210}) + "|" +
		string([]rune{0x5df2, 0x5b9e, 0x73b0}) + "|" +
		string([]rune{0x751f, 0x4ea7, 0x53ef, 0x7528}) + "|PASS"
}

func statusPendingRegex() string {
	// ??|???|???|???|??
	return string([]rune{0x5f85, 0x63a5}) + "|" +
		string([]rune{0x5f85, 0x5efa, 0x8bbe}) + "|" +
		string([]rune{0x5f85, 0x5b8c, 0x6210}) + "|" +
		string([]rune{0x672a, 0x5b9e, 0x73b0}) + "|" +
		string([]rune{0x7f3a, 0x5931})
}
