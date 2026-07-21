package aos

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// minTeamIdentityLen skips very short IDs/names (e.g. "go", "ab") that would
// over-redact legitimate prose. Path #5 defense-in-depth.
const minTeamIdentityLen = 3

// SanitizeUserFacingText strips AOS/system/orchestration leakage from text
// that will be shown to Cursor/end user. Returns "" when content is internal-only.
//
// ponytail: does NOT try to parse text as Leader planning JSON — that caused
// false positives on legitimate user content containing an "intent" field.
// JSON plan-shell detection is handled explicitly by looksLikeLeaderPlanShell
// which requires BOTH "intent" AND ("tasks" OR "reply") fields, and is only
// applied when the entire trimmed text is a single JSON object.
func SanitizeUserFacingText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}

	// Only strip a JSON shell when the WHOLE text is a single JSON object
	// that looks exactly like a Leader plan output (intent + tasks/reply).
	// This avoids clobbering legitimate user JSON that happens to contain "intent".
	if looksLikeLeaderPlanShell(trimmed) {
		// Try to extract a usable "reply" first (simple intent with real content).
		if out, ok := parseLeaderPlanOutput(trimmed); ok {
			intent := strings.ToLower(strings.TrimSpace(out.Intent))
			if intent == "simple" {
				reply := strings.TrimSpace(out.Reply)
				if reply == "" {
					return ""
				}
				return SanitizeUserFacingText(reply)
			}
			// complex or other intent with tasks/architecture → not user-facing
			return ""
		}
		return ""
	}

	// Check for prompt/system leak markers
	if looksLikePromptLeak(trimmed) {
		return ""
	}

	return trimmed
}

// looksLikeLeaderPlanShell returns true only when text starts with '{' and
// ends with '}' AND contains both "intent" and one of ("tasks"|"reply") as
// JSON field names. Conservative — avoids matching arbitrary user JSON.
func looksLikeLeaderPlanShell(text string) bool {
	if len(text) < 12 || text[0] != '{' || text[len(text)-1] != '}' {
		return false
	}
	if !strings.Contains(text, "\"intent\"") {
		return false
	}
	return strings.Contains(text, "\"tasks\"") || strings.Contains(text, "\"reply\"")
}

// SanitizeUserFacingTextWithTeam applies SanitizeUserFacingText then redacts
// configured team member IDs/names (path #5: merge/member identity leakage).
func SanitizeUserFacingTextWithTeam(text string, team *TeamProfile) string {
	return RedactTeamIdentity(SanitizeUserFacingText(text), team)
}

// SanitizePhaseText strips AOS internal phase status from text that would be
// streamed to Cursor before the final result. AOS currently emits no
// user-visible phase text — return "" for all inputs. If future phases need
// to surface progress, implement real filtering here (e.g., allow only
// user-facing status prefixes, strip [AOS] internal markers).
func SanitizePhaseText(text string) string { return "" }

// RedactTeamIdentity replaces configured member IDs and display names with a
// neutral placeholder so merge/member outputs do not expose team roster identity.
// Short tokens (< minTeamIdentityLen) are skipped to avoid false positives.
func RedactTeamIdentity(text string, team *TeamProfile) string {
	if team == nil || text == "" {
		return text
	}
	tokens := teamIdentityTokens(team)
	if len(tokens) == 0 {
		return text
	}
	// Longest first so multi-word names win over shorter substrings.
	sort.Slice(tokens, func(i, j int) bool {
		return utf8.RuneCountInString(tokens[i]) > utf8.RuneCountInString(tokens[j])
	})
	out := text
	for _, tok := range tokens {
		out = redactWholeToken(out, tok, "teammate")
	}
	return out
}

func teamIdentityTokens(team *TeamProfile) []string {
	if team == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if utf8.RuneCountInString(s) < minTeamIdentityLen {
			return
		}
		// Skip pure generic labels that would over-strip English prose.
		low := strings.ToLower(s)
		if low == "leader" || low == "member" || low == "team" {
			return
		}
		if _, ok := seen[low]; ok {
			return
		}
		seen[low] = struct{}{}
		out = append(out, s)
	}
	for _, m := range team.Members {
		add(m.ID)
		add(m.Name)
	}
	return out
}

// redactWholeToken replaces whole-token occurrences of old (case-insensitive)
// with replacement. Uses word-ish boundaries so "architect" does not match
// inside "architecture".
func redactWholeToken(text, old, replacement string) string {
	if old == "" || text == "" {
		return text
	}
	// Boundary: start/end or non letter/number/underscore (allows hyphenated IDs as whole tokens).
	// For tokens that contain spaces (display names), match the full phrase with loose edges.
	escaped := regexp.QuoteMeta(old)
	var pat string
	if strings.ContainsAny(old, " \t") {
		pat = `(?i)(^|[^[:alnum:]_])` + escaped + `([^[:alnum:]_]|$)`
	} else {
		// Include hyphen as part of token body for IDs like backend-eng
		pat = `(?i)(^|[^[:alnum:]_-])` + escaped + `([^[:alnum:]_-]|$)`
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		// Fail open: leave text unchanged rather than risk panic
		return text
	}
	return re.ReplaceAllStringFunc(text, func(match string) string {
		// Preserve surrounding delimiters captured in the match
		if len(match) == 0 {
			return match
		}
		// Rebuild: keep prefix/suffix non-token chars if present
		// Pattern always has optional prefix group and optional suffix group when matched via ReplaceAllStringFunc
		// Easier: find token inside match case-insensitively and replace only that span.
		lowMatch := strings.ToLower(match)
		lowOld := strings.ToLower(old)
		idx := strings.Index(lowMatch, lowOld)
		if idx < 0 {
			return replacement
		}
		return match[:idx] + replacement + match[idx+len(old):]
	})
}

// looksLikePromptLeak checks for high-confidence AOS internal markers.
// Uses strings.Contains for safety (avoids false negatives from formatting diffs).
// Keep markers specific enough to avoid destroying legitimate user content.
func looksLikePromptLeak(text string) bool {
	markers := []string{
		"You are an AI Team Leader (AOS Leader)",
		"## IntentGate — Classify Before Planning",
		"[System Constraint] You are an AOS team member",
		"AOS re-entry is strictly forbidden",
		"You are reviewing your team members' task outputs",
		"You are merging your team members' outputs",
		"[AOS] completed in",
		"[AOS Member ", // trailing space — see note below
	}
	// Note on "[AOS Member " marker: matches the start of internal spawn messages
	// like "[AOS Member backend spawned via Cursor Task, execID=...]". To avoid
	// false positives on legitimate user prose such as "I added [AOS Member backend]
	// to my config", we require the next chars to look like a spawn message — i.e.
	// the substring " spawned " must appear shortly after "[AOS Member ".
	// This is enforced in looksLikePromptLeak via a second check below.
	for _, m := range markers {
		if !strings.Contains(text, m) {
			continue
		}
		if m == "[AOS Member " {
			// Require spawn-message template, not bare user prose.
			if !looksLikeAOSSpawnMessage(text) {
				continue
			}
			return true
		}
		return true
	}
	return false
}

// looksLikeAOSSpawnMessage returns true when text contains a substring
// matching the internal resolveMemberTask spawn placeholder template:
//   [AOS Member <id> spawned via Cursor Task, execID=...]
// Conservative: require both " spawned via Cursor Task" and "execID=".
func looksLikeAOSSpawnMessage(text string) bool {
	return strings.Contains(text, " spawned via Cursor Task") &&
		strings.Contains(text, "execID=")
}
