package forwarder

import (
	"strings"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestApplyConfiguredSystemPromptAfterBuiltInPrompt(t *testing.T) {
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{
			{Role: "system", Content: "built-in prompt"},
			{Role: "user", Content: "hello"},
		},
		StableMessageCount: 1,
		CompileSummary:     "mode=agent",
	}

	result := applyConfiguredSystemPrompt(compiled, true, "channel instructions", "after")

	if got, want := result.Messages[0].Content, "built-in prompt\n\nchannel instructions"; got != want {
		t.Fatalf("system prompt mismatch: got %q want %q", got, want)
	}
	if !result.ConfiguredSystemPromptApplied {
		t.Fatal("expected configured system prompt to be marked as applied")
	}
	if result.StableMessageCount != compiled.StableMessageCount {
		t.Fatalf("stable replay count changed: got %d want %d", result.StableMessageCount, compiled.StableMessageCount)
	}
	if strings.Contains(result.CompileSummary, "channel instructions") {
		t.Fatal("compile summary leaked configured system prompt")
	}
	if !strings.Contains(result.CompileSummary, "model_system_prompt_position=after") {
		t.Fatalf("compile summary missing safe prompt metadata: %q", result.CompileSummary)
	}
}

func TestApplyConfiguredSystemPromptBeforeBuiltInPrompt(t *testing.T) {
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{Role: "system", Content: "built-in prompt"}},
	}

	result := applyConfiguredSystemPrompt(compiled, true, "channel instructions", "before")

	if got, want := result.Messages[0].Content, "channel instructions\n\nbuilt-in prompt"; got != want {
		t.Fatalf("system prompt mismatch: got %q want %q", got, want)
	}
}

func TestApplyConfiguredSystemPromptCreatesSystemMessageAndIsIdempotent(t *testing.T) {
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{Role: "user", Content: "hello"}},
	}

	result := applyConfiguredSystemPrompt(compiled, true, "channel instructions", "")
	result = applyConfiguredSystemPrompt(result, true, "channel instructions", "after")

	if len(result.Messages) != 2 {
		t.Fatalf("message count mismatch: got %d want 2", len(result.Messages))
	}
	if got := result.Messages[0]; got.Role != "system" || got.Content != "channel instructions" {
		t.Fatalf("unexpected inserted system message: %#v", got)
	}
	if count := strings.Count(result.Messages[0].Content, "channel instructions"); count != 1 {
		t.Fatalf("configured prompt applied %d times", count)
	}
}

func TestApplyConfiguredSystemPromptDisabledDoesNotMutateInput(t *testing.T) {
	compiled := CompiledConversation{
		Messages: []modeladapter.Message{{Role: "system", Content: "built-in prompt"}},
	}

	result := applyConfiguredSystemPrompt(compiled, false, "channel instructions", "after")

	if result.Messages[0].Content != "built-in prompt" {
		t.Fatalf("disabled prompt changed messages: %#v", result.Messages)
	}
	if result.ConfiguredSystemPromptApplied {
		t.Fatal("disabled prompt was marked as applied")
	}
}
