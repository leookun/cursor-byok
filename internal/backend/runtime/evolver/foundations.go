// foundations.go implements per-chapter 「现有基础」 table co-evolution (ADR-034).
//
// It repairs deterministic path-cell corruption and conservative status drift in
// table-format foundation sections only. Bullet prose foundations are out of scope.
package evolver

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// FoundationSection describes one handbook foundation table to scan.
type FoundationSection struct {
	Chapter     string // e.g. 05_Organization_Runtime.md
	Heading     string // section heading substring, e.g. "## 5.7"
	PathCol     int    // 0-based path-like column
	StatusCol   int    // 0-based status column
	MinCols     int
	Description string
}

// CanonicalFoundationSections is the closed list of table-format foundations.
var CanonicalFoundationSections = []FoundationSection{
	{Chapter: "05_Organization_Runtime.md", Heading: "## 5.7", PathCol: 1, StatusCol: 2, MinCols: 3, Description: "Organization foundations"},
	{Chapter: "09_Workflow_Runtime.md", Heading: "## 9.1", PathCol: 1, StatusCol: 2, MinCols: 3, Description: "Workflow foundations"},
	{Chapter: "10_Context_Runtime.md", Heading: "## 10.2", PathCol: 0, StatusCol: 3, MinCols: 4, Description: "Context foundations"},
	{Chapter: "15_Streaming_Runtime.md", Heading: "## 15.1", PathCol: 1, StatusCol: 2, MinCols: 3, Description: "Streaming foundations"},
	{Chapter: "16_Telemetry_Runtime.md", Heading: "## 16.1", PathCol: 1, StatusCol: 2, MinCols: 3, Description: "Telemetry foundations"},
}

// FoundationRow is one parsed path-bearing row from a foundation table.
type FoundationRow struct {
	Chapter        string
	Component      string
	RawPathCell    string
	NormalizedPath string
	Status         string
	PathExists     bool
	HasControl     bool
	IsPathLike     bool
	Drift          string
	LineIndex      int // absolute line index in chapter file
}

// FoundationInventory is the full scan result.
type FoundationInventory struct {
	Rows   []FoundationRow `json:"rows"`
	Drifts int             `json:"drifts"`
}

// InventoryFoundations scans all canonical foundation tables.
func (e *Evolver) InventoryFoundations(repoRoot string) *FoundationInventory {
	inv := &FoundationInventory{}
	for _, sec := range CanonicalFoundationSections {
		rows := e.scanFoundationSection(repoRoot, sec)
		for _, r := range rows {
			if r.Drift != "" {
				inv.Drifts++
			}
			inv.Rows = append(inv.Rows, r)
		}
	}
	return inv
}

func (e *Evolver) scanFoundationSection(repoRoot string, sec FoundationSection) []FoundationRow {
	path := filepath.Join(repoRoot, "docs", "handbook", sec.Chapter)
	content, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := string(content)
	lines := strings.Split(text, "\n")

	// Locate section range.
	start := -1
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, sec.Heading) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trim := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trim, "## ") {
			end = i
			break
		}
	}

	var out []FoundationRow
	headerSeen := false
	for i := start; i < end; i++ {
		trim := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trim, "|") {
			continue
		}
		if strings.Contains(trim, "---") {
			headerSeen = true
			continue
		}
		// Use control-preserving split: strings.TrimSpace removes \f/\v and eats letters.
		cols := splitTableRowPreserveControls(lines[i])
		if len(cols) <= sec.StatusCol || len(cols) < sec.MinCols {
			continue
		}
		// Skip header row labels.
		if !headerSeen {
			if cols[0] == "组件" || cols[0] == "文件" || cols[0] == "Runtime" {
				continue
			}
		}
		if cols[0] == "组件" || cols[0] == "文件" {
			continue
		}

		rawPath := cols[sec.PathCol]
		status := cols[sec.StatusCol]
		component := cols[0]
		hasCtrl := containsControl(rawPath) || strings.Contains(rawPath, `\`)
		norm, pathLike := normalizeFoundationPath(rawPath)
		// Keep control-corrupted basename cells so writeback can repair letters
		// even when the path is not unique enough for existence checks.
		if !pathLike && !hasCtrl {
			continue
		}
		if !pathLike && hasCtrl {
			// Best-effort recovered token for display repair only.
			norm = stripToPathToken(rawPath)
		}
		exists := false
		if norm != "" {
			full := filepath.Join(repoRoot, filepath.FromSlash(norm))
			if _, err := os.Stat(full); err == nil {
				exists = true
			}
		}
		row := FoundationRow{
			Chapter:        sec.Chapter,
			Component:      component,
			RawPathCell:    rawPath,
			NormalizedPath: norm,
			Status:         status,
			PathExists:     exists,
			HasControl:     hasCtrl,
			IsPathLike:     pathLike,
			LineIndex:      i,
		}
		row.Drift = foundationDrift(row)
		out = append(out, row)
	}
	return out
}

// splitTableRowPreserveControls splits a markdown table row without using
// strings.TrimSpace (which strips form-feed/vertical-tab and destroys
// recoverable path letters from accidental C-escapes).
func splitTableRowPreserveControls(line string) []string {
	// Trim only CR/LF/space/tab from line edges (not form-feed/vertical-tab).
	for len(line) > 0 {
		switch line[0] {
		case ' ', '\t', '\n', '\r':
			line = line[1:]
			continue
		}
		break
	}
	for len(line) > 0 {
		switch line[len(line)-1] {
		case ' ', '\t', '\n', '\r':
			line = line[:len(line)-1]
			continue
		}
		break
	}
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		p := parts[i]
		// Only trim plain spaces. Keep leading tabs/form-feeds so C-escape
		// recovery can restore eaten letters (e.g. \t + "race_test.go").
		for len(p) > 0 && p[0] == ' ' {
			p = p[1:]
		}
		for len(p) > 0 && p[len(p)-1] == ' ' {
			p = p[:len(p)-1]
		}
		parts[i] = p
	}
	return parts
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 && r != '\t' {
			return true
		}
	}
	return false
}

// normalizeFoundationPath returns a repo-relative internal path if the cell is path-like.
func normalizeFoundationPath(raw string) (string, bool) {
	// Recover letters eaten by accidental C-escapes (e.g. "\forwarder" -> form-feed + "orwarder").
	var b strings.Builder
	for _, r := range raw {
		switch r {
		case '\a':
			b.WriteByte('a')
		case '\b':
			b.WriteByte('b')
		case '\t':
			b.WriteByte('t')
		case '\n':
			b.WriteByte('n')
		case '\v':
			b.WriteByte('v')
		case '\f':
			b.WriteByte('f')
		case '\r':
			b.WriteByte('r')
		default:
			if r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, "`")
	s = strings.TrimSpace(s)
	// Literal backslash prefixes from unescaped markdown/C sources (e.g. "\trace_test.go").
	for strings.HasPrefix(s, `\`) {
		s = strings.TrimPrefix(s, `\`)
		s = strings.TrimSpace(s)
	}
	if s == "" {
		return "", false
	}
	// Take first token / drop trailing notes ("— PromptCompiler", function suffixes).
	if i := strings.IndexAny(s, " \t("); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	s = strings.Trim(s, "`\"'")
	if s == "" {
		return "", false
	}
	// Must look like a path.
	if !strings.Contains(s, "/") && !strings.HasSuffix(s, ".go") && !strings.Contains(s, `\`) {
		return "", false
	}
	s = filepath.ToSlash(s)
	s = strings.TrimLeft(s, "./")
	if strings.HasPrefix(s, "internal/") {
		return s, true
	}
	// Relative backend paths commonly used in tables.
	if strings.HasPrefix(s, "forwarder/") || strings.HasPrefix(s, "virtualmodel/") ||
		strings.HasPrefix(s, "runtime/") || strings.HasPrefix(s, "server/") ||
		strings.HasPrefix(s, "host.go") {
		return "internal/backend/" + s, true
	}
	// Basename-only .go files: keep as display path (not repo-rooted) so writeback
	// can repair corrupted basenames like "\trace_test.go" -> "trace_test.go".
	if !strings.Contains(s, "/") {
		if strings.HasSuffix(s, ".go") {
			return s, true
		}
		return "", false
	}
	if strings.Contains(s, ".go") {
		return "internal/backend/" + s, true
	}
	return "", false
}

// displayFoundationPath chooses a table-friendly path string for writeback.
func displayFoundationPath(raw, normalized string) string {
	if normalized == "" {
		return raw
	}
	// Prefer relative form under internal/backend when the table historically used it.
	if strings.HasPrefix(normalized, "internal/backend/") {
		rel := strings.TrimPrefix(normalized, "internal/backend/")
		// If original (after control recovery) looked relative, keep relative.
		recovered, _ := normalizeFoundationPath(raw)
		_ = recovered
		rawRec := stripToPathToken(raw)
		if !strings.HasPrefix(rawRec, "internal/") {
			return rel
		}
	}
	return normalized
}

func stripToPathToken(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch r {
		case '\a':
			b.WriteByte('a')
		case '\b':
			b.WriteByte('b')
		case '\t':
			b.WriteByte('t')
		case '\n':
			b.WriteByte('n')
		case '\v':
			b.WriteByte('v')
		case '\f':
			b.WriteByte('f')
		case '\r':
			b.WriteByte('r')
		default:
			if r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	s := strings.TrimSpace(b.String())
	s = strings.Trim(s, "`")
	if i := strings.IndexAny(s, " \t("); i > 0 {
		s = s[:i]
	}
	return strings.Trim(s, "`\"'")
}

func foundationDrift(row FoundationRow) string {
	if row.HasControl {
		return "path-control-chars"
	}
	if !row.IsPathLike {
		return ""
	}
	// Only enforce path existence for repo-rooted internal paths.
	if !strings.HasPrefix(row.NormalizedPath, "internal/") {
		return ""
	}
	status := strings.TrimSpace(row.Status)
	implementedLike := statusLooksImplemented(status)
	missingLike := statusLooksMissing(status)
	if row.NormalizedPath != "" && !row.PathExists && implementedLike {
		return "path-missing-status-claims-present"
	}
	if row.NormalizedPath != "" && row.PathExists && missingLike {
		return "path-present-status-claims-missing"
	}
	return ""
}

func statusLooksImplemented(status string) bool {
	s := status
	switch {
	case s == "":
		return false
	case strings.Contains(s, "待建设"), strings.Contains(s, "缺失"), strings.Contains(s, "未实现"):
		return false
	case strings.Contains(s, "已实现"), strings.Contains(s, "已集成"), strings.Contains(s, "生产"),
		strings.Contains(s, "PASS"), strings.Contains(s, "框架就绪"), strings.Contains(s, "骨架就绪"),
		strings.Contains(s, "v1 已实现"), strings.Contains(s, "Default/"):
		return true
	default:
		// Conservative: non-empty non-missing statuses treated as claiming presence.
		return true
	}
}

func statusLooksMissing(status string) bool {
	s := status
	return strings.Contains(s, "待建设") || strings.Contains(s, "缺失") || strings.Contains(s, "未实现")
}

// diagnoseFoundationTables appends foundation-table findings.
func (e *Evolver) diagnoseFoundationTables(repoRoot string, report *DiagnosisReport) {
	inv := e.InventoryFoundations(repoRoot)
	if inv == nil || len(inv.Rows) == 0 {
		report.add(SeverityInfo, "foundation-table", "no foundation table path rows inventoried")
		return
	}
	shown := 0
	for _, row := range inv.Rows {
		if row.Drift == "" {
			continue
		}
		shown++
		msg := fmt.Sprintf("%s [%s]: %s raw=%q norm=%s status=%q",
			row.Chapter, row.Component, row.Drift, sanitizeForMsg(row.RawPathCell), row.NormalizedPath, row.Status)
		report.add(SeverityWarning, "foundation-table", msg)
		if shown >= 12 {
			break
		}
	}
	if inv.Drifts == 0 {
		report.add(SeverityInfo, "foundation-table",
			fmt.Sprintf("foundation tables consistent: %d path rows across %d chapters",
				len(inv.Rows), len(CanonicalFoundationSections)))
	} else if shown < inv.Drifts {
		report.add(SeverityWarning, "foundation-table",
			fmt.Sprintf("%d additional foundation-table drifts truncated", inv.Drifts-shown))
	}
}

func sanitizeForMsg(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return unicode.ReplacementChar
		}
		return r
	}, s)
}

// writebackFoundationTables repairs path control chars and conservative status cells.
func (e *Evolver) writebackFoundationTables(repoRoot string) ([]WritebackItem, error) {
	var applied []WritebackItem
	// Group rows by chapter for single write per file.
	type change struct {
		lineIndex int
		newLine   string
		detail    string
	}
	byChapter := map[string][]change{}

	for _, sec := range CanonicalFoundationSections {
		rows := e.scanFoundationSection(repoRoot, sec)
		if len(rows) == 0 {
			continue
		}
		path := filepath.Join(repoRoot, "docs", "handbook", sec.Chapter)
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return applied, fmt.Errorf("read %s: %w", sec.Chapter, err)
		}
		lines := strings.Split(string(content), "\n")
		for _, row := range rows {
			if row.LineIndex < 0 || row.LineIndex >= len(lines) {
				continue
			}
			cols := splitTableRowPreserveControls(lines[row.LineIndex])
			if len(cols) <= sec.StatusCol {
				continue
			}
			changed := false
			detailParts := []string{}

			// 1) Repair path control chars / normalize path cell display.
			if row.HasControl {
				display := row.NormalizedPath
				if display == "" {
					display = stripToPathToken(row.RawPathCell)
				} else {
					display = displayFoundationPath(row.RawPathCell, row.NormalizedPath)
				}
				if display != "" && cols[sec.PathCol] != display {
					cols[sec.PathCol] = display
					changed = true
					detailParts = append(detailParts, "repaired path cell")
				}
			}

			// 2) Status rewrite under conservative policy.
			if row.Drift == "path-missing-status-claims-present" {
				if cols[sec.StatusCol] != "待建设" {
					cols[sec.StatusCol] = "待建设"
					changed = true
					detailParts = append(detailParts, "status -> 待建设 (path missing)")
				}
			}
			if row.Drift == "path-present-status-claims-missing" {
				if cols[sec.StatusCol] != "已实现" {
					cols[sec.StatusCol] = "已实现"
					changed = true
					detailParts = append(detailParts, "status -> 已实现 (path present)")
				}
			}

			if !changed {
				continue
			}
			// Rebuild markdown row.
			var sb strings.Builder
			sb.WriteString("|")
			for _, c := range cols {
				sb.WriteString(" ")
				sb.WriteString(strings.TrimSpace(c))
				sb.WriteString(" |")
			}
			newLine := sb.String()
			// Preserve original line ending style later via Join.
			byChapter[sec.Chapter] = append(byChapter[sec.Chapter], change{
				lineIndex: row.LineIndex,
				newLine:   newLine,
				detail:    fmt.Sprintf("%s: %s", row.Component, strings.Join(detailParts, "; ")),
			})
		}

		// Apply chapter changes if any.
		chgs := byChapter[sec.Chapter]
		if len(chgs) == 0 {
			continue
		}
		for _, ch := range chgs {
			lines[ch.lineIndex] = ch.newLine
			applied = append(applied, WritebackItem{
				Chapter: sec.Chapter,
				Action:  "sync-foundation-tables",
				Detail:  ch.detail,
			})
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return applied, fmt.Errorf("write %s: %w", sec.Chapter, err)
		}
	}
	return applied, nil
}
