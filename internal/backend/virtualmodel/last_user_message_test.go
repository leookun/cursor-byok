package virtualmodel

import "testing"

// TestLastUserMessage_MixedList returns the most recent user message even when
// the conversation contains system and assistant turns interleaved with user
// turns.
func TestLastUserMessage_MixedList(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "  follow up question  "},
	}
	got := LastUserMessage(messages)
	want := "follow up question"
	if got != want {
		t.Fatalf("LastUserMessage = %q, want %q", got, want)
	}
}

// TestLastUserMessage_EmptyList returns "" for a zero-length slice.
func TestLastUserMessage_EmptyList(t *testing.T) {
	if got := LastUserMessage(nil); got != "" {
		t.Fatalf("LastUserMessage(nil) = %q, want empty", got)
	}
}

// TestLastUserMessage_OnlyAssistant returns "" when no user turn is present.
func TestLastUserMessage_OnlyAssistant(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "system"},
		{Role: "assistant", Content: "answer"},
	}
	if got := LastUserMessage(messages); got != "" {
		t.Fatalf("LastUserMessage = %q, want empty when only assistant present", got)
	}
}

// TestLastUserMessage_SkipsEmptyUserContent ensures whitespace-only user turns
// are not treated as the latest user message.
func TestLastUserMessage_SkipsEmptyUserContent(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "real question"},
		{Role: "user", Content: "   "},
	}
	if got := LastUserMessage(messages); got != "real question" {
		t.Fatalf("LastUserMessage = %q, want %q", got, "real question")
	}
}
