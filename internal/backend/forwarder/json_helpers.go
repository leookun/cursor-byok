package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

func cloneStringAnyMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil
	}
	return decoded
}

func parseRFC3339Time(value string) time.Time {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, trimmed)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func marshalCarryForwardMessage(message modeladapter.Message) ([]byte, error) {
	payload := map[string]any{
		"role":    message.Role,
		"content": message.Content,
	}
	if len(message.ContentParts) > 0 {
		payload["content_parts"] = message.ContentParts
	}
	if strings.TrimSpace(message.ReasoningContent) != "" || (strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0) {
		payload["reasoning_content"] = message.ReasoningContent
	}
	if strings.TrimSpace(message.ReasoningSignature) != "" {
		payload["reasoning_signature"] = message.ReasoningSignature
	}
	if strings.TrimSpace(message.ReasoningSignatureSource) != "" {
		payload["reasoning_signature_source"] = strings.TrimSpace(message.ReasoningSignatureSource)
	}
	if len(message.ToolCalls) > 0 {
		payload["tool_calls"] = message.ToolCalls
	}
	if strings.TrimSpace(message.ToolCallID) != "" {
		payload["tool_call_id"] = message.ToolCallID
	}
	if strings.TrimSpace(message.Name) != "" {
		payload["name"] = message.Name
	}
	return json.Marshal(payload)
}

func shortSHA256(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}
