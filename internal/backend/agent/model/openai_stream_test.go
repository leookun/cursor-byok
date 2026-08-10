package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesRequestsReasoningSummary(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"check the request\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"model\":\"gpt-5.6\",\"status\":\"completed\",\"output_text\":\"done\"}}\n\n")
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 4)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-5.6",
		OpenAIEndpoint:  "/v1/responses",
		ReasoningEffort: "high",
		Messages:        []Message{{Role: "user", Content: "hello"}},
		MaxTokens:       128,
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning request body missing: %#v", requestBody)
	}
	if got := reasoning["effort"]; got != "high" {
		t.Fatalf("reasoning.effort = %#v, want high", got)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("reasoning.summary = %#v, want auto", got)
	}
	include, ok := requestBody["include"].([]any)
	if !ok || len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("reasoning include = %#v, want encrypted content", requestBody["include"])
	}
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingDelta, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTextDelta, 1)
}

func TestOpenAIChatCompletionsIgnoresBlankFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":"first"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"reasoning_content":" second"},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Ls","arguments":""}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"{\"path\":"}}]},"finish_reason":""}]}`,
			`{"model":"deepseek-v4-flash","choices":[{"delta":{"tool_calls":[{"index":0,"id":"","type":"function","function":{"name":"","arguments":"\"/tmp\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"model":"deepseek-v4-flash","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{client: server.Client()}
	events := make([]ModelEvent, 0, 8)
	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "request-1",
		RunID:           "run-1",
		ModelCallID:     "model-call-1",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "deepseek-v4-flash",
		OpenAIEndpoint:  "/v1/chat/completions",
		Messages:        []Message{{Role: "user", Content: "list files"}},
		MaxTokens:       128,
		RequestKnobs:    map[string]any{},
	}, func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}

	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingDelta, 2)
	assertOpenAIEventKindCount(t, events, ModelEventKindThinkingCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindToolLikeCompleted, 1)
	assertOpenAIEventKindCount(t, events, ModelEventKindTurnFinished, 1)

	toolEvent := firstOpenAIEventForTest(events, ModelEventKindToolLikeCompleted)
	if toolEvent == nil || toolEvent.ToolInvocation == nil {
		t.Fatalf("completed tool invocation missing: %#v", toolEvent)
	}
	if toolEvent.ToolInvocation.ToolName != "Ls" {
		t.Fatalf("tool name = %q, want Ls", toolEvent.ToolInvocation.ToolName)
	}
	if got := string(toolEvent.ToolInvocation.ArgsJSON); got != `{"path":"/tmp"}` {
		t.Fatalf("tool args = %q, want %q", got, `{"path":"/tmp"}`)
	}

	finished := firstOpenAIEventForTest(events, ModelEventKindTurnFinished)
	if finished == nil || finished.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", finished.FinishReason)
	}
	if finished.InputTokens != 12 || finished.OutputTokens != 7 {
		t.Fatalf("usage = input:%d output:%d, want input:12 output:7", finished.InputTokens, finished.OutputTokens)
	}
}

func assertOpenAIEventKindCount(t *testing.T, events []ModelEvent, kind ModelEventKind, want int) {
	t.Helper()
	got := 0
	for _, event := range events {
		if event.Kind == kind {
			got++
		}
	}
	if got != want {
		t.Fatalf("event kind %s count = %d, want %d; events=%#v", kind, got, want, events)
	}
}

func firstOpenAIEventForTest(events []ModelEvent, kind ModelEventKind) *ModelEvent {
	for index := range events {
		if events[index].Kind == kind {
			return &events[index]
		}
	}
	return nil
}
