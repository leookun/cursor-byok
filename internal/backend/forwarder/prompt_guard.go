package forwarder

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const (
	promptGuardUserTextChars            = 32000
	promptGuardUserRichTextChars        = 32000
	promptGuardSubagentReminderChars    = 12000
	promptGuardSelectedFileChars        = 16000
	promptGuardSelectedFilesTotalChars  = 64000
	promptGuardSelectedFilesMaxCount    = 12
	promptGuardRequestFileChars         = 16000
	promptGuardRequestFilesTotalChars   = 64000
	promptGuardRequestFilesMaxCount     = 12
	promptGuardRuleChars                = 6000
	promptGuardRulesTotalChars          = 24000
	promptGuardRulesMaxCount            = 40
	promptGuardSkillDescriptionChars    = 800
	promptGuardSkillDescriptionsTotal   = 16000
	promptGuardSkillDescriptorsMaxCount = 32
	promptGuardAgentSkillContentChars   = 6000
	promptGuardAgentSkillsMaxCount      = 16
	promptGuardRealtimeTextChars        = 12000
	promptGuardCompiledMessageChars     = 120000
)

func normalizeUserMessageForStorage(userMessage *agentv1.UserMessage) *agentv1.UserMessage {
	if userMessage == nil {
		return nil
	}
	cloned, ok := proto.Clone(userMessage).(*agentv1.UserMessage)
	if !ok || cloned == nil {
		return userMessage
	}
	cloned.Text = truncatePromptGuardText("user_message.text", cloned.GetText(), promptGuardUserTextChars)
	if richText := strings.TrimSpace(cloned.GetRichText()); richText != "" {
		cloned.RichText = stringPtr(truncatePromptGuardText("user_message.rich_text", richText, promptGuardUserRichTextChars))
	}
	if reminder := strings.TrimSpace(cloned.GetSubagentSystemReminder()); reminder != "" {
		cloned.SubagentSystemReminder = stringPtr(truncatePromptGuardText("user_message.subagent_system_reminder", reminder, promptGuardSubagentReminderChars))
	}
	cloned.SelectedContext = guardSelectedContext(cloned.GetSelectedContext())
	return cloned
}

func guardRequestContextForStorage(requestContext *agentv1.RequestContext) {
	if requestContext == nil {
		return
	}
	requestContext.Rules = guardCursorRules(requestContext.GetRules())
	requestContext.FileContents = guardStringMap(
		requestContext.GetFileContents(),
		"request_context.file_contents",
		promptGuardRequestFileChars,
		promptGuardRequestFilesTotalChars,
		promptGuardRequestFilesMaxCount,
	)
	if summary := strings.TrimSpace(requestContext.GetUserIntentSummary()); summary != "" {
		requestContext.UserIntentSummary = stringPtr(truncatePromptGuardText("request_context.user_intent_summary", summary, promptGuardRealtimeTextChars))
	}
	if hooks := strings.TrimSpace(requestContext.GetHooksAdditionalContext()); hooks != "" {
		requestContext.HooksAdditionalContext = stringPtr(truncatePromptGuardText("request_context.hooks_additional_context", hooks, promptGuardRealtimeTextChars))
	}
	if commit := strings.TrimSpace(requestContext.GetCommitAttributionMessage()); commit != "" {
		requestContext.CommitAttributionMessage = stringPtr(truncatePromptGuardText("request_context.commit_attribution_message", commit, promptGuardRealtimeTextChars))
	}
	if pr := strings.TrimSpace(requestContext.GetPrAttributionMessage()); pr != "" {
		requestContext.PrAttributionMessage = stringPtr(truncatePromptGuardText("request_context.pr_attribution_message", pr, promptGuardRealtimeTextChars))
	}
}

func guardCompiledConversationForProvider(compiled CompiledConversation) CompiledConversation {
	for index := range compiled.Messages {
		message := &compiled.Messages[index]
		if strings.TrimSpace(message.Role) == "system" {
			continue
		}
		if strings.TrimSpace(message.Content) != "" {
			message.Content = truncatePromptGuardText("compiled."+firstNonEmpty(strings.TrimSpace(message.Role), "message"), message.Content, promptGuardCompiledMessageChars)
		}
		for partIndex := range message.ContentParts {
			if strings.TrimSpace(message.ContentParts[partIndex].Text) == "" {
				continue
			}
			message.ContentParts[partIndex].Text = truncatePromptGuardText("compiled.content_part", message.ContentParts[partIndex].Text, promptGuardCompiledMessageChars)
		}
	}
	return compiled
}

func guardSelectedContext(selectedContext *agentv1.SelectedContext) *agentv1.SelectedContext {
	if selectedContext == nil {
		return nil
	}
	cloned, ok := proto.Clone(selectedContext).(*agentv1.SelectedContext)
	if !ok || cloned == nil {
		return selectedContext
	}
	cloned.Files = guardSelectedFiles(cloned.GetFiles())
	cloned.SelectedSkills = guardAgentSkills(cloned.GetSelectedSkills())
	cloned.ExtraContext = guardStringSlice(cloned.GetExtraContext(), "selected_context.extra_context", promptGuardRealtimeTextChars, promptGuardRealtimeTextChars, promptGuardAgentSkillsMaxCount)
	return cloned
}

func guardSelectedFiles(files []*agentv1.SelectedFile) []*agentv1.SelectedFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]*agentv1.SelectedFile, 0, minInt(len(files), promptGuardSelectedFilesMaxCount))
	remaining := promptGuardSelectedFilesTotalChars
	for _, file := range files {
		if file == nil || len(result) >= promptGuardSelectedFilesMaxCount {
			continue
		}
		content := strings.TrimSpace(file.GetContent())
		if content == "" {
			continue
		}
		limit := minInt(promptGuardSelectedFileChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(file).(*agentv1.SelectedFile)
		if !ok || cloned == nil {
			continue
		}
		cloned.Content = truncatePromptGuardText("selected_context.files.content", content, limit)
		remaining -= promptGuardRuneCount(cloned.GetContent())
		result = append(result, cloned)
	}
	return result
}

func guardCursorRules(rules []*agentv1.CursorRule) []*agentv1.CursorRule {
	if len(rules) == 0 {
		return nil
	}
	result := make([]*agentv1.CursorRule, 0, minInt(len(rules), promptGuardRulesMaxCount))
	remaining := promptGuardRulesTotalChars
	for _, rule := range rules {
		if rule == nil || len(result) >= promptGuardRulesMaxCount {
			continue
		}
		content := strings.TrimSpace(rule.GetContent())
		if content == "" {
			continue
		}
		limit := minInt(promptGuardRuleChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(rule).(*agentv1.CursorRule)
		if !ok || cloned == nil {
			continue
		}
		cloned.Content = truncatePromptGuardText("request_context.rules.content", content, limit)
		remaining -= promptGuardRuneCount(cloned.GetContent())
		result = append(result, cloned)
	}
	return result
}

func guardSkillDescriptors(descriptors []*agentv1.SkillDescriptor) []*agentv1.SkillDescriptor {
	if len(descriptors) == 0 {
		return nil
	}
	result := make([]*agentv1.SkillDescriptor, 0, minInt(len(descriptors), promptGuardSkillDescriptorsMaxCount))
	remaining := promptGuardSkillDescriptionsTotal
	for _, descriptor := range descriptors {
		if descriptor == nil || len(result) >= promptGuardSkillDescriptorsMaxCount {
			continue
		}
		description := strings.TrimSpace(descriptor.GetDescription())
		if description == "" {
			continue
		}
		limit := minInt(promptGuardSkillDescriptionChars, remaining)
		if limit <= 0 {
			break
		}
		cloned, ok := proto.Clone(descriptor).(*agentv1.SkillDescriptor)
		if !ok || cloned == nil {
			continue
		}
		cloned.Description = truncatePromptGuardText("skill.description", description, limit)
		remaining -= promptGuardRuneCount(cloned.GetDescription())
		result = append(result, cloned)
	}
	return result
}

func guardAgentSkills(skills []*agentv1.AgentSkill) []*agentv1.AgentSkill {
	if len(skills) == 0 {
		return nil
	}
	result := make([]*agentv1.AgentSkill, 0, minInt(len(skills), promptGuardAgentSkillsMaxCount))
	for _, skill := range skills {
		if skill == nil || len(result) >= promptGuardAgentSkillsMaxCount {
			continue
		}
		cloned, ok := proto.Clone(skill).(*agentv1.AgentSkill)
		if !ok || cloned == nil {
			continue
		}
		if content := strings.TrimSpace(cloned.GetContent()); content != "" {
			cloned.Content = truncatePromptGuardText("agent_skill.content", content, promptGuardAgentSkillContentChars)
		}
		if description := strings.TrimSpace(cloned.GetDescription()); description != "" {
			cloned.Description = truncatePromptGuardText("agent_skill.description", description, promptGuardSkillDescriptionChars)
		}
		result = append(result, cloned)
	}
	return result
}

func guardStringMap(input map[string]string, label string, itemLimit int, totalLimit int, maxItems int) map[string]string {
	if len(input) == 0 {
		return nil
	}
	keys := make([]string, 0, len(input))
	values := make(map[string]string, len(input))
	for key, value := range input {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" {
			continue
		}
		if _, exists := values[trimmedKey]; exists {
			continue
		}
		keys = append(keys, trimmedKey)
		values[trimmedKey] = trimmedValue
	}
	sort.Strings(keys)
	result := make(map[string]string, minInt(len(keys), maxItems))
	remaining := totalLimit
	for _, key := range keys {
		if len(result) >= maxItems {
			break
		}
		content := values[key]
		if content == "" {
			continue
		}
		limit := minInt(itemLimit, remaining)
		if limit <= 0 {
			break
		}
		result[key] = truncatePromptGuardText(label, content, limit)
		remaining -= promptGuardRuneCount(result[key])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func guardStringSlice(input []string, label string, itemLimit int, totalLimit int, maxItems int) []string {
	if len(input) == 0 {
		return nil
	}
	result := make([]string, 0, minInt(len(input), maxItems))
	remaining := totalLimit
	for _, item := range input {
		if len(result) >= maxItems {
			break
		}
		content := strings.TrimSpace(item)
		if content == "" {
			continue
		}
		limit := minInt(itemLimit, remaining)
		if limit <= 0 {
			break
		}
		truncated := truncatePromptGuardText(label, content, limit)
		remaining -= promptGuardRuneCount(truncated)
		result = append(result, truncated)
	}
	return result
}

func truncatePromptGuardText(label string, text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if promptGuardRuneCount(text) <= limit {
		return text
	}
	runes := []rune(text)
	notice := fmt.Sprintf("\n\n[truncated: %s exceeded %d chars; kept head and tail from %d chars]\n\n", strings.TrimSpace(label), limit, len(runes))
	noticeRunes := []rune(notice)
	keep := limit - len(noticeRunes)
	if keep <= 0 {
		return string(runes[:limit])
	}
	head := keep * 2 / 3
	tail := keep - head
	if tail <= 0 {
		return string(runes[:head]) + notice
	}
	return string(runes[:head]) + notice + string(runes[len(runes)-tail:])
}

func promptGuardRuneCount(text string) int {
	return utf8.RuneCountInString(text)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
