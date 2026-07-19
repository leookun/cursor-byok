// compiler.go 负责把固定 prompt、自然历史和 tool catalog 编译成 provider 请求。
package forwarder

import (
	"context"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

type PromptCompiler interface {
	Compile(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string, modelName string) (CompiledConversation, error)
	DerivePromptContexts(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string) ([]PromptContextMessage, error)
}

func (service *Service) compileConversation(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string, modelID string, modelName string) (CompiledConversation, error) {
	if service == nil || service.compiler == nil {
		return CompiledConversation{}, fmt.Errorf("prompt compiler is unavailable")
	}
	compiled, err := service.compiler.Compile(conversation, mode, latestUserText, modelName)
	if err != nil {
		return CompiledConversation{}, err
	}
	if service.resolver == nil {
		return compiled, nil
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil {
		return CompiledConversation{}, err
	}
	if channel == nil {
		return CompiledConversation{}, fmt.Errorf("no available channel for model %q", strings.TrimSpace(modelID))
	}
	return applyConfiguredSystemPrompt(compiled, channel.SystemPromptEnabled, channel.SystemPrompt, channel.SystemPromptPosition), nil
}

func applyConfiguredSystemPrompt(compiled CompiledConversation, enabled bool, promptText string, position string) CompiledConversation {
	promptText = strings.TrimSpace(promptText)
	if !enabled || promptText == "" || compiled.ConfiguredSystemPromptApplied {
		return compiled
	}

	messages := append([]modeladapter.Message(nil), compiled.Messages...)
	systemIndex := -1
	for index := range messages {
		if strings.EqualFold(strings.TrimSpace(messages[index].Role), "system") {
			systemIndex = index
			break
		}
	}

	position = strings.ToLower(strings.TrimSpace(position))
	if position != "before" {
		position = "after"
	}
	if systemIndex < 0 {
		messages = append([]modeladapter.Message{{Role: "system", Content: promptText}}, messages...)
	} else {
		systemMessage := messages[systemIndex]
		baseText := strings.TrimSpace(systemMessage.Content)
		if position == "before" {
			systemMessage.Content = strings.TrimSpace(strings.Join(filterNonEmpty([]string{promptText, baseText}), "\n\n"))
		} else {
			systemMessage.Content = strings.TrimSpace(strings.Join(filterNonEmpty([]string{baseText, promptText}), "\n\n"))
		}
		messages[systemIndex] = systemMessage
	}

	compiled.Messages = messages
	compiled.ConfiguredSystemPromptApplied = true
	compiled.CompileSummary = strings.TrimSpace(fmt.Sprintf(
		"%s model_system_prompt=true model_system_prompt_position=%s model_system_prompt_bytes=%d",
		compiled.CompileSummary,
		position,
		len([]byte(promptText)),
	))
	return compiled
}

type DefaultPromptCompiler struct {
	projector *HistoryProjector
	catalog   ToolCatalog
	reminders ReminderInjector
	rules     *UserRuleStore
}

// NewPromptCompiler 创建默认 prompt 编译器。
func NewPromptCompiler(projector *HistoryProjector, catalog ToolCatalog, reminders ReminderInjector, rules *UserRuleStore) *DefaultPromptCompiler {
	return &DefaultPromptCompiler{
		projector: projector,
		catalog:   catalog,
		reminders: reminders,
		rules:     rules,
	}
}

// Compile 生成当前 turn 应发送给 provider 的消息和工具集合。
func (compiler *DefaultPromptCompiler) Compile(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string, modelName string) (CompiledConversation, error) {
	if compiler == nil || compiler.projector == nil || compiler.catalog == nil {
		return CompiledConversation{}, fmt.Errorf("prompt compiler dependencies are not initialized")
	}
	normalizedMode, err := validateSupportedActiveMode(mode)
	if err != nil {
		return CompiledConversation{}, err
	}
	subagentTypeName := ""
	if conversation != nil {
		subagentTypeName = conversation.SubagentTypeName
	}
	assetMode, err := promptAssetModeForConversation(normalizedMode, subagentTypeName)
	if err != nil {
		return CompiledConversation{}, err
	}
	systemPrompt, err := promptassets.ReadPrompt(assetMode)
	if err != nil {
		return CompiledConversation{}, err
	}
	tools, _, err := compiler.catalog.Load(normalizedMode, subagentTypeName)
	if err != nil {
		return CompiledConversation{}, err
	}
	replayMessages, err := compiler.projector.ProjectPromptReplay(conversation)
	if err != nil {
		return CompiledConversation{}, err
	}
	sharedRulesPrompt := ""
	sharedRuleCount := 0
	sharedRuleTotal := 0
	if compiler.rules != nil && normalizedMode != agentv1.AgentMode_AGENT_MODE_DEBUG {
		sharedRulesPrompt, sharedRuleTotal, sharedRuleCount, err = compiler.rules.BuildSystemPromptSection()
		if err != nil {
			return CompiledConversation{}, err
		}
	}
	messages := make([]modeladapter.Message, 0, len(replayMessages)+1)
	systemParts := []string{sanitizePromptAsset(systemPrompt, modelName)}
	if strings.TrimSpace(sharedRulesPrompt) != "" {
		systemParts = append(systemParts, sharedRulesPrompt)
	}
	systemText := strings.TrimSpace(strings.Join(filterNonEmpty(systemParts), "\n\n"))
	if systemText != "" {
		messages = append(messages, modeladapter.Message{
			Role:    "system",
			Content: systemText,
		})
	}
	stableReplayCount, err := compiler.stableReplayMessageCount(conversation, replayMessages)
	if err != nil {
		return CompiledConversation{}, err
	}
	messages = append(messages, replayMessages...)
	return CompiledConversation{
		Mode:               normalizedMode,
		Messages:           messages,
		StableMessageCount: stableReplayCount,
		Tools:              tools,
		CompileSummary:     fmt.Sprintf("mode=%s asset_mode=%s child=%t messages=%d tools=%d shared_rules_total=%d shared_rules_deduped=%d", normalizedMode.String(), string(assetMode), isChildConversationSubagentTypeName(subagentTypeName), len(messages), len(tools), sharedRuleTotal, sharedRuleCount),
	}, nil
}

func (compiler *DefaultPromptCompiler) DerivePromptContexts(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string) ([]PromptContextMessage, error) {
	if compiler == nil || compiler.projector == nil || compiler.catalog == nil || compiler.reminders == nil {
		return nil, fmt.Errorf("prompt compiler dependencies are not initialized")
	}
	normalizedMode, err := validateSupportedActiveMode(mode)
	if err != nil {
		return nil, err
	}
	subagentTypeName := ""
	if conversation != nil {
		subagentTypeName = conversation.SubagentTypeName
	}
	_, toolNames, err := compiler.catalog.Load(normalizedMode, subagentTypeName)
	if err != nil {
		return nil, err
	}
	replayMessages, err := compiler.projector.ProjectPromptReplay(conversation)
	if err != nil {
		return nil, err
	}
	structuredStatePromptContexts, structuredStateTailMessages, err := buildStructuredStatePromptContexts(conversation)
	if err != nil {
		return nil, err
	}
	promptReminders := compiler.reminders.Inject(normalizedMode, conversation, replayMessages, latestUserText, toolNames)
	candidates := make([]PromptContextMessage, 0, len(structuredStatePromptContexts)+len(structuredStateTailMessages)+len(promptReminders.PromptContexts)+len(promptReminders.TailMessages))
	candidates = append(candidates, structuredStatePromptContexts...)
	for _, message := range structuredStateTailMessages {
		candidates = append(candidates, newPromptContextMessage(promptContextSourceStructuredTodoReminder, message, true))
	}
	candidates = append(candidates, promptReminders.PromptContexts...)
	for index, message := range promptReminders.TailMessages {
		candidates = append(candidates, newPromptContextMessage(fmt.Sprintf("tail_reminder/%d", index), message, true))
	}
	for index := range candidates {
		candidates[index].Persist = true
	}
	return filterCurrentTurnPromptContexts(conversation, candidates), nil
}

func (compiler *DefaultPromptCompiler) stableReplayMessageCount(conversation *ConversationFile, replayMessages []modeladapter.Message) (int, error) {
	if compiler == nil || compiler.projector == nil || conversation == nil || len(replayMessages) == 0 {
		return 0, nil
	}
	currentTurnSeq := conversation.CurrentTurnSeq
	if currentTurnSeq <= 0 {
		currentTurnSeq = conversation.NextTurnSeq - 1
	}
	stableCount := 0
	if currentTurnSeq > 0 {
		stableConversation := cloneConversationFile(conversation)
		stableConversation.Entries = stableReplayEntriesBeforeTurn(conversation.Entries, currentTurnSeq)
		stableMessages, err := compiler.projector.ProjectPromptReplay(stableConversation)
		if err != nil {
			return 0, err
		}
		stableCount = len(stableMessages)
	}
	if requestPrefixReplayCount := replayMessageCountFromRequestPrefix(conversation); requestPrefixReplayCount > stableCount {
		stableCount = requestPrefixReplayCount
	}
	if stableCount > len(replayMessages) {
		return len(replayMessages), nil
	}
	return stableCount, nil
}

func replayMessageCountFromRequestPrefix(conversation *ConversationFile) int {
	if conversation == nil || conversation.LatestRequestPrefix == nil {
		return 0
	}
	requestID := strings.TrimSpace(conversation.CurrentRequestID)
	if requestID == "" || strings.TrimSpace(conversation.LatestRequestPrefix.RequestID) != requestID {
		return 0
	}
	return conversation.LatestRequestPrefix.ReplayMessageCount
}

func stableReplayEntriesBeforeTurn(entries []HistoryEntry, currentTurnSeq int64) []HistoryEntry {
	if len(entries) == 0 || currentTurnSeq <= 0 {
		return nil
	}
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.TurnSeq > 0 && entry.TurnSeq >= currentTurnSeq {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

// filterNonEmpty 过滤掉空白字符串，便于安全拼接 system prompt 片段。
func filterNonEmpty(items []string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			filtered = append(filtered, strings.TrimSpace(item))
		}
	}
	return filtered
}

func mergeAdjacentPlainUserMessages(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	merged := make([]modeladapter.Message, 0, len(messages))
	for _, message := range messages {
		text, ok := plainUserMessageText(message)
		if !ok {
			merged = append(merged, message)
			continue
		}
		if len(merged) == 0 {
			message.Role = "user"
			message.Content = text
			merged = append(merged, message)
			continue
		}
		last := &merged[len(merged)-1]
		if lastText, ok := plainUserMessageText(*last); ok {
			last.Role = "user"
			last.Content = lastText + "\n\n" + text
			continue
		}
		message.Role = "user"
		message.Content = text
		merged = append(merged, message)
	}
	return merged
}

func plainUserMessageText(message modeladapter.Message) (string, bool) {
	if strings.TrimSpace(message.Role) != "user" {
		return "", false
	}
	if len(message.ContentParts) > 0 || len(message.ToolCalls) > 0 {
		return "", false
	}
	if strings.TrimSpace(message.ToolCallID) != "" || strings.TrimSpace(message.Name) != "" {
		return "", false
	}
	if strings.TrimSpace(message.ReasoningContent) != "" ||
		strings.TrimSpace(message.ReasoningSignature) != "" ||
		strings.TrimSpace(message.ReasoningSignatureSource) != "" ||
		strings.TrimSpace(message.OpenAIResponsesReasoningID) != "" ||
		strings.TrimSpace(message.OpenAIResponsesReasoningStatus) != "" ||
		len(message.OpenAIResponsesReasoningSummary) > 0 {
		return "", false
	}
	text := strings.TrimSpace(message.Content)
	if text == "" {
		return "", false
	}
	return text, true
}
