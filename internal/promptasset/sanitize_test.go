package promptasset

import (
	"strings"
	"testing"
)

// TestSanitize_StripsHeadersAndSeparators verifies that documentation-only
// section headers and horizontal rules are dropped from the asset body while
// the real prompt text is preserved and rendered.
func TestSanitize_StripsHeadersAndSeparators(t *testing.T) {
	const asset = "# 通用系统提示词\n\nYou are a helpful assistant.\n\n---\n\n# 模式静态补充\nBe concise.\n"
	got := Sanitize(asset, "gpt-4o")

	if strings.Contains(got, "# 通用系统提示词") {
		t.Fatalf("expected header '# 通用系统提示词' to be stripped, got: %q", got)
	}
	if strings.Contains(got, "# 模式静态补充") {
		t.Fatalf("expected header '# 模式静态补充' to be stripped, got: %q", got)
	}
	if strings.Contains(got, "\n---\n") {
		t.Fatalf("expected '---' separator to be stripped, got: %q", got)
	}
	if !strings.Contains(got, "You are a helpful assistant.") {
		t.Fatalf("expected body text preserved, got: %q", got)
	}
	if !strings.Contains(got, "Be concise.") {
		t.Fatalf("expected trailing body preserved, got: %q", got)
	}
}

// TestSanitize_PreservesBodyWithoutHeaders confirms an asset that already
// contains only prompt text passes through essentially unchanged (modulo
// template rendering).
func TestSanitize_PreservesBodyWithoutHeaders(t *testing.T) {
	const asset = "Answer the user's question."
	got := Sanitize(asset, "claude-3-5-sonnet")
	if !strings.Contains(got, "Answer the user's question.") {
		t.Fatalf("expected body preserved, got: %q", got)
	}
}

// TestSanitize_EmptyAsset ensures an empty input does not panic and yields a
// deterministic (possibly template-wrapped) non-error result.
func TestSanitize_EmptyAsset(t *testing.T) {
	got := Sanitize("", "gpt-4o")
	// Result may be a template wrapper around empty content; just ensure no panic.
	_ = got
}
