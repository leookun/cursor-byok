package virtualmodel

import (
	"encoding/json"
	"errors"
	"strings"
)

// ErrNoJSONObject is returned by ExtractJSONObject when the input text does
// not contain a parseable JSON object (after stripping fenced markdown and
// leading prose).
var ErrNoJSONObject = errors.New("virtualmodel: no JSON object found in text")

// ExtractJSONObject locates and returns the first valid JSON object in the
// given text.
//
// Behaviour:
//   - Markdown fenced blocks (```json ... ``` or ``` ... ```) are unwrapped
//     first so model outputs that wrap JSON in a code fence parse correctly.
//   - Leading prose before the first '{' is skipped.
//   - Brace counting is string-aware: '{' and '}' inside quoted strings do
//     not affect object depth, and escaped quotes inside strings are handled.
//   - If a candidate object fails json.Valid (e.g. truncated), the scan
//     continues from the next '{' so surrounding prose cannot hide a later
//     valid object.
//
// Returns the substring of the first valid JSON object, or
// ( "", ErrNoJSONObject ) if none is found.
//
// This is the shared, robust replacement for the two previously divergent
// extractJSON helpers that lived in aos/provider.go (string-aware scan) and
// moa/provider.go (buggy strings.Index/strings.LastIndex implementation that
// broke whenever the JSON contained a '}' character inside a string value).
func ExtractJSONObject(s string) (string, error) {
	if candidate, ok := extractFromFencedMarkdown(s); ok {
		if json.Valid([]byte(candidate)) {
			return candidate, nil
		}
	}
	for start := 0; start < len(s); start++ {
		if s[start] != '{' {
			continue
		}
		candidate, ok := scanJSONObject(s, start)
		if ok && json.Valid([]byte(candidate)) {
			return candidate, nil
		}
	}
	return "", ErrNoJSONObject
}

// extractFromFencedMarkdown unwraps a ```json ... ``` (or ``` ... ```) block
// when present. Returns ("", false) when no fenced block is detected.
func extractFromFencedMarkdown(s string) (string, bool) {
	idx := strings.Index(s, "```")
	if idx < 0 {
		return "", false
	}
	rest := s[idx+3:]
	// Optional "json" language tag right after the opening fence.
	if strings.HasPrefix(rest, "json") {
		rest = rest[4:]
	} else if lineBreak := strings.IndexAny(rest, "\r\n"); lineBreak >= 0 {
		// Allow any other language tag up to the line break.
		first := rest[:lineBreak]
		_ = first
	}
	// Trim any whitespace immediately after the language tag.
	rest = strings.TrimLeft(rest, " \t\r\n")
	end := strings.Index(rest, "```")
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:end]), true
}

// scanJSONObject scans forward from start (which must point at '{') returning
// the substring covering the balanced object, aware of quoted strings and
// escaped quotes. Returns ("", false) if no balanced close brace is found.
func scanJSONObject(s string, start int) (string, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		ch := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
