package forwarder

import (
	"strings"
	"testing"
)

func TestValidateConversationID_RejectsTraversal(t *testing.T) {
	bad := []string{
		"", ".", "..", "...",
		"./x", "../x", "x/..", "x/y",
		`..\x`, `x\..`,
		"/etc/passwd", "/abs/path",
		".hidden",
		`has\backslash`,
	}
	for _, in := range bad {
		_, err := validateConversationID(in)
		if err == nil {
			t.Fatalf("expected error for %q, got nil", in)
		}
	}
}

func TestValidateConversationID_AcceptsValid(t *testing.T) {
	good := []string{
		"abc123", "conv-1", "session_2",
		"550e8400-e29b-41d4-a716-446655440000", // UUID
		strings.Repeat("a", 128), // up to 128 chars
	}
	for _, in := range good {
		got, err := validateConversationID(in)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", in, err)
		}
		if got != in {
			t.Fatalf("mutated valid id: in=%q got=%q", in, got)
		}
	}
}
