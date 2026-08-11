package modeladapter

import (
	"context"
	"reflect"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
)

type recordingModelAdapter struct {
	request StreamRequest
}

func (adapter *recordingModelAdapter) Stream(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
	adapter.request = req
	return nil
}

type staticChannelResolver struct {
	channel *legacyruntime.ResolvedChannel
}

func (resolver staticChannelResolver) SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error) {
	return resolver.channel, nil
}

func (staticChannelResolver) ProviderStreamIdleTimeout(context.Context) time.Duration {
	return time.Second
}

func TestRouterRuntimeDisabledClearsReasoningEffort(t *testing.T) {
	openAI := &recordingModelAdapter{}
	router := &Router{
		openai: openAI,
		resolver: staticChannelResolver{channel: &legacyruntime.ResolvedChannel{
			ID:              "channel-a",
			Provider:        "openai",
			Model:           "grok-composer-2.5-fast",
			ReasoningEffort: "medium",
		}},
	}
	requestKnobs := map[string]any{"reasoning_effort": "medium"}

	err := router.Stream(context.Background(), StreamRequest{
		ModelID:        "channel-a",
		ThinkingEffort: "disabled",
		RequestKnobs:   requestKnobs,
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	if got := openAI.request.ReasoningEffort; got != "" {
		t.Fatalf("ReasoningEffort = %q, want blank", got)
	}
	if _, exists := openAI.request.RequestKnobs["reasoning_effort"]; exists {
		t.Fatalf("reasoning_effort knob should be removed: %#v", openAI.request.RequestKnobs)
	}
}

func TestSanitizeProviderMessagesMergesLegacyAssistantTextAndToolCallTurnsIdempotently(t *testing.T) {
	input := []Message{
		{
			Role:             "assistant",
			Content:          "Now let me pass stream.Mode in service.go.",
			ReasoningContent: "I need to update service.go.",
		},
		{
			Role:             "assistant",
			Content:          "",
			ReasoningContent: "I need to update service.go.",
			ToolCalls: []ToolCallDescriptor{
				{
					ID:   "call_1",
					Type: "function",
					Function: ToolCallFunctionShape{
						Name:      "PatchEdit",
						Arguments: `{"path":"/workspace/service.go"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			Content:    `{"success":{"path":"/workspace/service.go"}}`,
			ToolCallID: "call_1",
			Name:       "PatchEdit",
		},
	}

	first := sanitizeProviderMessages(input)
	if len(first) != 2 {
		t.Fatalf("message count = %d, want 2: %#v", len(first), first)
	}

	assistant := first[0]
	if assistant.Content != input[0].Content {
		t.Fatalf("assistant content = %q", assistant.Content)
	}
	if assistant.ReasoningContent != input[0].ReasoningContent {
		t.Fatalf("assistant reasoning = %q, want one copy of %q", assistant.ReasoningContent, input[0].ReasoningContent)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Fatalf("assistant tool calls = %#v", assistant.ToolCalls)
	}

	second := sanitizeProviderMessages(first)
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("sanitizing normalized messages changed them:\nfirst: %#v\nsecond: %#v", first, second)
	}
}
