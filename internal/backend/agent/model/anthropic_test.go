package modeladapter

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestAnthropicMessageCacheBreakpointsPreserveAppendOnlyHistory(t *testing.T) {
	for size := 1; size <= 32; size++ {
		t.Run(fmt.Sprintf("size_%02d", size), func(t *testing.T) {
			previous := anthropicMessagesForAppendOnlyTest(size)
			current := anthropicMessagesForAppendOnlyTest(size + 1)

			applyAnthropicMessageCacheBreakpoints(previous)
			applyAnthropicMessageCacheBreakpoints(current)

			want := mustMarshalAnthropicMessagesForTest(t, previous)
			got := mustMarshalAnthropicMessagesForTest(t, current[:len(previous)])
			if got != want {
				t.Fatalf("historical message prefix changed after append\nwant: %s\ngot:  %s", want, got)
			}
		})
	}
}

func anthropicMessagesForAppendOnlyTest(count int) []anthropicMessage {
	messages := make([]anthropicMessage, 0, count)
	for index := 0; index < count; index++ {
		messages = append(messages, anthropicMessage{
			Role: "user",
			Content: []map[string]any{{
				"type": "text",
				"text": fmt.Sprintf("message-%02d", index),
			}},
		})
	}
	return messages
}

func mustMarshalAnthropicMessagesForTest(t *testing.T, messages []anthropicMessage) string {
	t.Helper()
	payload, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal anthropic messages: %v", err)
	}
	return string(payload)
}
