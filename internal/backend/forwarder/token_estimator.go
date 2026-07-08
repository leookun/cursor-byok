package forwarder

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

const (
	estimatedTokensPerMessageOverhead  = int64(8)
	estimatedTokensPerToolCallOverhead = int64(6)
	estimatedTokensPerImagePart        = int64(1024)
)

func estimateCompiledPromptTokens(compiled CompiledConversation) int64 {
	return estimateModelMessagesTokens(compiled.Messages) + estimateToolDescriptorsTokens(compiled.Tools)
}

func estimateModelMessagesTokens(messages []modeladapter.Message) int64 {
	total := int64(0)
	for _, item := range messages {
		total += estimateModelMessageTokens(item)
	}
	return total
}

func estimateModelMessageTokens(item modeladapter.Message) int64 {
	total := estimatedTokensPerMessageOverhead
	total += estimateTextTokens(item.Role)
	total += estimateTextTokens(item.Content)
	total += estimateModelContentPartsTokens(item.Content, item.ContentParts)
	total += estimateTextTokens(item.ReasoningContent)
	total += estimateTextTokens(item.ReasoningSignature)
	total += estimateTextTokens(item.ToolCallID)
	total += estimateTextTokens(item.Name)
	for _, toolCall := range item.ToolCalls {
		total += estimatedTokensPerToolCallOverhead
		total += estimateTextTokens(toolCall.ID)
		total += estimateTextTokens(toolCall.Type)
		total += estimateTextTokens(toolCall.Function.Name)
		total += estimateTextTokens(toolCall.Function.Arguments)
	}
	return total
}

func estimateToolDescriptorsTokens(tools []json.RawMessage) int64 {
	total := int64(0)
	for _, item := range tools {
		total += estimateTextTokens(string(item))
	}
	return total
}

func estimateContextItemTokens(item *aiserverv1.ContextItem) int64 {
	if item == nil {
		return 0
	}
	body, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(item)
	if err != nil {
		return estimateTextTokens(item.String())
	}
	return estimateTextTokens(string(body))
}

func estimateTextTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func estimateModelContentPartsTokens(content string, parts []modeladapter.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

func estimatePromptContentPartsTokens(content string, parts []promptengine.ContentPart) int64 {
	if len(parts) == 0 {
		return 0
	}
	total := int64(0)
	countText := strings.TrimSpace(content) == ""
	for _, part := range parts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "", "text":
			if countText {
				total += estimateTextTokens(part.Text)
			}
		case "image":
			total += estimatedTokensPerImagePart
			if part.Image != nil {
				total += estimateTextTokens(part.Image.MIMEType)
				total += estimateTextTokens(part.Image.Path)
			}
		}
	}
	return total
}

func estimateCheckpointPromptTokenBreakdown(compiled CompiledConversation, hasCompiled bool, usedTokens uint32, maxTokens uint32) *agentv1.PromptTokenBreakdownSnapshot {
	if maxTokens == 0 {
		return nil
	}
	categories := make([]*agentv1.PromptTokenBreakdownCategory, 0, 4)
	if hasCompiled {
		systemTokens, summaryTokens, conversationTokens := estimateMessageBreakdownTokens(compiled.Messages)
		categories = appendPromptTokenBreakdownCategory(categories, "system_prompt", "System Prompt", systemTokens)
		categories = appendPromptTokenBreakdownCategory(categories, "tools", "Tools", estimateToolDescriptorsTokens(compiled.Tools))
		categories = appendPromptTokenBreakdownCategory(categories, "summarized_conversation", "Summarized Conversation", summaryTokens)
		categories = appendPromptTokenBreakdownCategory(categories, "conversation", "Conversation", conversationTokens)
	} else if usedTokens > 0 {
		categories = appendPromptTokenBreakdownCategory(categories, "conversation", "Conversation", int64(usedTokens))
	}
	categoryTotal := int64(0)
	for _, category := range categories {
		categoryTotal += int64(category.GetEstimatedTokens())
	}
	totalUsedTokens := usedTokens
	if categoryTotal > int64(totalUsedTokens) {
		totalUsedTokens = clampInt64ToUint32(categoryTotal)
	}
	return &agentv1.PromptTokenBreakdownSnapshot{
		TotalUsedTokens: totalUsedTokens,
		MaxTokens:       maxTokens,
		Categories:      categories,
	}
}

func estimateMessageBreakdownTokens(messages []modeladapter.Message) (int64, int64, int64) {
	systemTokens := int64(0)
	summaryTokens := int64(0)
	conversationTokens := int64(0)
	for _, message := range messages {
		tokens := estimateModelMessageTokens(message)
		switch {
		case strings.TrimSpace(message.Role) == "system":
			systemTokens += tokens
		case isConversationSummaryMessage(message):
			summaryTokens += tokens
		default:
			conversationTokens += tokens
		}
	}
	return systemTokens, summaryTokens, conversationTokens
}

func isConversationSummaryMessage(message modeladapter.Message) bool {
	return strings.Contains(message.Content, "<conversation_summary>") || strings.Contains(message.Content, "</conversation_summary>")
}

func appendPromptTokenBreakdownCategory(categories []*agentv1.PromptTokenBreakdownCategory, id string, label string, estimatedTokens int64) []*agentv1.PromptTokenBreakdownCategory {
	if estimatedTokens <= 0 {
		return categories
	}
	return append(categories, &agentv1.PromptTokenBreakdownCategory{
		Id:              id,
		Label:           label,
		EstimatedTokens: clampInt64ToUint32(estimatedTokens),
	})
}

func clampInt64ToInt32(value int64) int32 {
	if value <= 0 {
		return 0
	}
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}
