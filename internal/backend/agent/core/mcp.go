package runtimecore

import (
	"encoding/json"
	"strings"
)

// MCPToolPayload 表示 CallMcpTool 的宽容解码结果。
type MCPToolPayload struct {
	Server             string
	ProviderIdentifier string
	ToolName           string
	Name               string
	Arguments          map[string]any
}

// DecodeMCPToolPayload 解析 CallMcpTool 参数，并兼容字符串化的 arguments 对象。
func DecodeMCPToolPayload(raw []byte) (MCPToolPayload, error) {
	payload := MCPToolPayload{
		Arguments: make(map[string]any),
	}
	if len(raw) == 0 {
		return payload, nil
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return payload, err
	}
	if decoded == nil {
		return payload, nil
	}

	payload.Server = decodeJSONStringValue(decoded["server"])
	payload.ProviderIdentifier = decodeJSONStringValue(decoded["providerIdentifier"])
	payload.ToolName = decodeJSONStringValue(decoded["toolName"])
	payload.Name = decodeJSONStringValue(decoded["name"])
	payload.Arguments = decodeJSONObjectLike(decoded["arguments"])
	if len(payload.Arguments) == 0 {
		payload.Arguments = decodeJSONObjectLike(decoded["args"])
	}
	return payload, nil
}

// InferMCPServerIdentifier 从 canonical lookup name 中反推出 server identifier。
func InferMCPServerIdentifier(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	if index := strings.Index(trimmed, "-"); index > 0 {
		return strings.TrimSpace(trimmed[:index])
	}
	return ""
}

// InferMCPToolName 从 canonical lookup name 中反推出 tool name。
func InferMCPToolName(serverIdentifier string, name string) string {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" {
		return ""
	}
	trimmedServer := strings.TrimSpace(serverIdentifier)
	if trimmedServer != "" && strings.HasPrefix(trimmedName, trimmedServer+"-") {
		return strings.TrimSpace(strings.TrimPrefix(trimmedName, trimmedServer+"-"))
	}
	return trimmedName
}

func decodeJSONStringValue(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func decodeJSONObjectLike(value any) map[string]any {
	switch item := value.(type) {
	case map[string]any:
		if item == nil {
			return make(map[string]any)
		}
		return item
	case string:
		return decodeJSONObjectBytes([]byte(item))
	case []byte:
		return decodeJSONObjectBytes(item)
	case json.RawMessage:
		return decodeJSONObjectBytes([]byte(item))
	default:
		return make(map[string]any)
	}
}

func decodeJSONObjectBytes(raw []byte) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return make(map[string]any)
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return make(map[string]any)
	}
	object, ok := decoded.(map[string]any)
	if !ok || object == nil {
		return make(map[string]any)
	}
	return object
}
