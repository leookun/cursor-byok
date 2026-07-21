package pet

import (
	"strings"
	"testing"
)

func TestValidatePetID_RejectsTraversal(t *testing.T) {
	bad := []string{
		"", ".", "..", "...",
		"./x", "../x", "x/..", "x/y",
		`..\x`, `x\..`,
		"/etc/passwd", "/abs/path",
		".hidden", "has space",
		"has/slash", "has\\backslash",
		"very-long-name-" + strings.Repeat("a", 70),
		"name with;semicolon",
		"name;with;pipe",
	}
	for _, in := range bad {
		if _, err := ValidatePetID(in); err == nil {
			t.Fatalf("expected error for %q, got nil", in)
		}
	}
}

func TestValidatePetID_AcceptsValid(t *testing.T) {
	good := []string{"cat", "embedded", "nezukocoder", "pet_1", "PET-2", "abc123", "a", strings.Repeat("a", 64)}
	for _, in := range good {
		got, err := ValidatePetID(in)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if got != in {
			t.Fatalf("ValidatePetID mutated valid id: in=%q got=%q", in, got)
		}
	}
}

func TestValidatePetID_TrimsWhitespace(t *testing.T) {
	got, err := ValidatePetID("  cat  ")
	if err != nil || got != "cat" {
		t.Fatalf("expected trim to 'cat', got %q err=%v", got, err)
	}
}
