package virtualmodel

import (
	"errors"
	"strings"
	"testing"
)

// TestExtractJSONObject_FencedMarkdown unwraps a ```json fenced block.
func TestExtractJSONObject_FencedMarkdown(t *testing.T) {
	input := "Here is the plan:\n```json\n{\"tasks\":[{\"id\":\"t1\"}]}\n```\nend."
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"tasks":[{"id":"t1"}]}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractJSONObject_LeadingProse skips prose before the first '{'.
func TestExtractJSONObject_LeadingProse(t *testing.T) {
	input := "Sure, here is your JSON: {\"a\":1} trailing text"
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"a":1}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractJSONObject_MalformedReturnsError ensures we surface an error when
// no valid JSON object can be found.
func TestExtractJSONObject_MalformedReturnsError(t *testing.T) {
	input := "no json here at all"
	if _, err := ExtractJSONObject(input); !errors.Is(err, ErrNoJSONObject) {
		t.Fatalf("expected ErrNoJSONObject, got %v", err)
	}
}

// TestExtractJSONObject_BraceInsideStringValue ensures '}' characters inside
// string values do not break brace counting (the MOA bug).
func TestExtractJSONObject_BraceInsideStringValue(t *testing.T) {
	input := `{"note":"this has a } in it","ok":true}`
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"note":"this has a } in it","ok":true}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractJSONObject_MoaFencedMarkdownPreviouslyBroke reproduces the bug
// that the old moa/provider.go extractJSON had: its strings.Index("{") /
// strings.LastIndex("}") implementation would return the substring from the
// first '{' to the LAST '}' in the text, corrupting output whenever prose or
// a fenced block contained an extra '}'. The shared implementation must
// succeed on this input and return the inner JSON object verbatim.
func TestExtractJSONObject_MoaFencedMarkdownPreviouslyBroke(t *testing.T) {
	// Fenced block with trailing prose containing '}' that previously fooled
	// the buggy MOA implementation.
	input := "```json\n{\"role\":\"leader\"}\n```\nNote: closing brace looks like }"
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("shared ExtractJSONObject must succeed on fenced input that previously broke MOA: %v", err)
	}
	want := `{"role":"leader"}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractJSONObject_SkipsTruncatedCandidateAndFindsValidObject verifies
// the scan continues after a truncated candidate to find a later valid one.
func TestExtractJSONObject_SkipsTruncatedCandidateAndFindsValidObject(t *testing.T) {
	input := `{ broken {"ok":1}`
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"ok":1}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestExtractJSONObject_EmptyInput returns the sentinel error for empty text.
func TestExtractJSONObject_EmptyInput(t *testing.T) {
	if _, err := ExtractJSONObject(""); !errors.Is(err, ErrNoJSONObject) {
		t.Fatalf("expected ErrNoJSONObject for empty input, got %v", err)
	}
}

// TestExtractJSONObject_PlainFenceWithoutLanguageTag handles a ``` block
// without a "json" language tag.
func TestExtractJSONObject_PlainFenceWithoutLanguageTag(t *testing.T) {
	input := "```\n{\"x\":2}\n```"
	got, err := ExtractJSONObject(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := `{"x":2}`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(input, "json") {
		t.Fatalf("test input should not contain 'json' tag for this case")
	}
}
