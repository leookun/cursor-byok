package modeladapter

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	maxProviderToolCallIDLen = 64
	toolCallNamespaceHashLen = 12
	toolCallValueHashLen     = 12
)

// namespaceToolCallID 为 provider 原始 tool call id 增加 model-call 级别命名空间，
// 避免像 functions.Shell:0 这类跨轮复用的 id 在客户端被误判为同一个 bubble。
//
// OpenAI 等 provider 对 tool_call_id 长度有限制，因此这里使用 model_call_id 的短哈希
// 而不是完整 UUID，保证内部存储的 tool_call_id 既稳定又能安全回放给 provider。
func namespaceToolCallID(modelCallID string, rawToolCallID string) string {
	raw := strings.TrimSpace(rawToolCallID)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "::") {
		return providerToolCallID(raw)
	}
	model := strings.TrimSpace(modelCallID)
	if model == "" {
		return providerToolCallID(raw)
	}
	return buildProviderSafeToolCallID(shortToolCallHash(model, toolCallNamespaceHashLen), raw)
}

// providerToolCallID 把内部持久化的 tool_call_id 规整成 provider 可接受的安全长度。
// 这样旧会话里已经落盘的 legacy "<modelCallID>::<rawID>" 也能继续回放。
func providerToolCallID(toolCallID string) string {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return ""
	}
	namespace, raw, ok := splitLegacyToolCallID(trimmed)
	if ok {
		return buildProviderSafeToolCallID(shortToolCallHash(namespace, toolCallNamespaceHashLen), raw)
	}
	if len(trimmed) <= maxProviderToolCallIDLen {
		return trimmed
	}
	return buildProviderSafeToolCallID("", trimmed)
}

type providerToolCallDescriptor struct {
	ID       string                `json:"id"`
	Index    int                   `json:"index,omitempty"`
	Type     string                `json:"type"`
	Function ToolCallFunctionShape `json:"function"`
}

func normalizeToolCallDescriptors(toolCalls []ToolCallDescriptor) []providerToolCallDescriptor {
	if len(toolCalls) == 0 {
		return nil
	}
	normalized := make([]providerToolCallDescriptor, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		item := providerToolCallDescriptor{
			ID:       providerToolCallID(toolCall.ID),
			Index:    toolCall.Index,
			Type:     toolCall.Type,
			Function: toolCall.Function,
		}
		normalized = append(normalized, item)
	}
	return normalized
}

func buildProviderSafeToolCallID(namespace string, raw string) string {
	trimmedRaw := strings.TrimSpace(raw)
	if trimmedRaw == "" {
		return ""
	}
	if namespace == "" && len(trimmedRaw) <= maxProviderToolCallIDLen && !strings.Contains(trimmedRaw, "::") {
		return trimmedRaw
	}

	prefix := "tc"
	if namespace != "" {
		prefix += "_" + namespace
	}
	candidate := prefix + "_" + trimmedRaw
	if len(candidate) <= maxProviderToolCallIDLen {
		return candidate
	}

	rawHash := shortToolCallHash(trimmedRaw, toolCallValueHashLen)
	remaining := maxProviderToolCallIDLen - len(prefix) - len(rawHash) - 2
	if remaining <= 0 {
		return prefix + "_" + rawHash
	}
	suffix := trimmedRaw
	if len(suffix) > remaining {
		suffix = suffix[len(suffix)-remaining:]
	}
	return prefix + "_" + rawHash + "_" + suffix
}

func splitLegacyToolCallID(value string) (namespace string, raw string, ok bool) {
	namespace, raw, ok = strings.Cut(strings.TrimSpace(value), "::")
	if !ok {
		return "", "", false
	}
	namespace = strings.TrimSpace(namespace)
	raw = strings.TrimSpace(raw)
	if namespace == "" || raw == "" {
		return "", "", false
	}
	return namespace, raw, true
}

func shortToolCallHash(value string, size int) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	encoded := hex.EncodeToString(sum[:])
	if size <= 0 || size > len(encoded) {
		return encoded
	}
	return encoded[:size]
}
