package modeladapter

import (
	"strings"
	"testing"
)

func TestToolImageProviderEncodings(t *testing.T) {
	message := toolImageMessageForTest()

	t.Run("openai_chat", func(t *testing.T) {
		items, err := normalizeOpenAIProviderMessages([]Message{message}, false)
		if err != nil {
			t.Fatalf("normalizeOpenAIProviderMessages() error = %v", err)
		}
		if len(items) != 1 || items[0]["role"] != "tool" || items[0]["tool_call_id"] != "call-1" {
			t.Fatalf("openai chat tool message = %#v", items)
		}
		content, ok := items[0]["content"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("openai chat content = %#v", items[0]["content"])
		}
		imageURL, ok := content[1]["image_url"].(map[string]any)
		if content[1]["type"] != "image_url" || !ok || !strings.HasPrefix(imageURL["url"].(string), "data:image/png;base64,") {
			t.Fatalf("openai chat image part = %#v", content[1])
		}
	})

	t.Run("openai_responses", func(t *testing.T) {
		_, items, err := normalizeOpenAIResponsesInput([]Message{message})
		if err != nil {
			t.Fatalf("normalizeOpenAIResponsesInput() error = %v", err)
		}
		if len(items) != 1 || items[0]["type"] != "function_call_output" {
			t.Fatalf("openai responses items = %#v", items)
		}
		content, ok := items[0]["output"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("openai responses output = %#v", items[0]["output"])
		}
		if content[0]["type"] != "input_text" || content[1]["type"] != "input_image" {
			t.Fatalf("openai responses content = %#v", content)
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		_, messages, err := normalizeAnthropicProviderMessages([]Message{message}, false, false)
		if err != nil {
			t.Fatalf("normalizeAnthropicProviderMessages() error = %v", err)
		}
		if len(messages) != 1 || messages[0].Role != "user" || len(messages[0].Content) != 1 {
			t.Fatalf("anthropic messages = %#v", messages)
		}
		toolResult := messages[0].Content[0]
		if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call-1" {
			t.Fatalf("anthropic tool result = %#v", toolResult)
		}
		content, ok := toolResult["content"].([]map[string]any)
		if !ok || len(content) != 2 {
			t.Fatalf("anthropic tool content = %#v", toolResult["content"])
		}
		if content[0]["type"] != "text" || content[1]["type"] != "image" {
			t.Fatalf("anthropic content blocks = %#v", content)
		}
	})
}

func toolImageMessageForTest() Message {
	return Message{
		Role:       "tool",
		Content:    "read binary bytes=16",
		ToolCallID: "call-1",
		Name:       "Read",
		ContentParts: []ContentPart{
			{Type: "text", Text: "read binary bytes=16"},
			{
				Type: "image",
				Image: &ImageContent{
					MIMEType: "image/png",
					Path:     "diagram.png",
					Data:     []byte("\x89PNG\r\n\x1a\nimage"),
				},
			},
		},
	}
}
