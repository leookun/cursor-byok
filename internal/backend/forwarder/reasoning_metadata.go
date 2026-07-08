// reasoning_metadata.go 保存 history/replay 共用的 reasoning metadata 判定。
package forwarder

import (
	"strings"

	modeladapter "cursor/internal/backend/agent/model"
)

func hasReplayableReasoningPayload(reasoningContent string, reasoningSignature string, reasoningSignatureSource string) bool {
	if strings.TrimSpace(reasoningContent) != "" {
		return true
	}
	return strings.TrimSpace(reasoningSignature) != "" &&
		strings.TrimSpace(reasoningSignatureSource) == modeladapter.ReasoningSignatureSourceOpenAIResponses
}
