package forwarder

import (
	"fmt"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

func normalizeRequestContextForStorage(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}
	return normalizeRequestContextForStorageMode(requestContext, true)
}

func normalizeRequestContextForStorageMode(requestContext *agentv1.RequestContext, includeStatic bool) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}
	if !includeStatic {
		return normalizeRealtimeRequestContextForStorage(requestContext)
	}

	cloned, ok := proto.Clone(requestContext).(*agentv1.RequestContext)
	if !ok || cloned == nil {
		return requestContext
	}

	descriptors := collectSkillDescriptors(cloned)
	cloned.Rules = filterNonSkillRules(cloned.GetRules())
	cloned.AgentSkills = nil
	descriptors = guardSkillDescriptors(descriptors)
	if len(descriptors) > 0 {
		cloned.SkillOptions = &agentv1.SkillOptions{SkillDescriptors: descriptors}
	} else {
		cloned.SkillOptions = nil
	}
	guardRequestContextForStorage(cloned)
	return cloned
}

func normalizeRealtimeRequestContextForStorage(requestContext *agentv1.RequestContext) *agentv1.RequestContext {
	if requestContext == nil {
		return nil
	}

	normalized := &agentv1.RequestContext{}
	normalized.Rules = guardCursorRules(filterNonSkillRules(requestContext.GetRules()))
	if fileContents := normalizeRealtimeFileContents(requestContext.GetFileContents()); len(fileContents) > 0 {
		normalized.FileContents = fileContents
	}
	if summary := strings.TrimSpace(requestContext.GetUserIntentSummary()); summary != "" {
		normalized.UserIntentSummary = stringPtr(truncatePromptGuardText("request_context.user_intent_summary", summary, promptGuardRealtimeTextChars))
	}
	if hooks := strings.TrimSpace(requestContext.GetHooksAdditionalContext()); hooks != "" {
		normalized.HooksAdditionalContext = stringPtr(truncatePromptGuardText("request_context.hooks_additional_context", hooks, promptGuardRealtimeTextChars))
	}
	if commit := strings.TrimSpace(requestContext.GetCommitAttributionMessage()); commit != "" {
		normalized.CommitAttributionMessage = stringPtr(truncatePromptGuardText("request_context.commit_attribution_message", commit, promptGuardRealtimeTextChars))
	}
	if pr := strings.TrimSpace(requestContext.GetPrAttributionMessage()); pr != "" {
		normalized.PrAttributionMessage = stringPtr(truncatePromptGuardText("request_context.pr_attribution_message", pr, promptGuardRealtimeTextChars))
	}
	if !hasRealtimeRequestContextContent(normalized) {
		return nil
	}
	return normalized
}

func normalizeRealtimeFileContents(fileContents map[string]string) map[string]string {
	if len(fileContents) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(fileContents))
	for path, content := range fileContents {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" || strings.TrimSpace(content) == "" {
			continue
		}
		normalized[trimmedPath] = content
	}
	if len(normalized) == 0 {
		return nil
	}
	return guardStringMap(normalized, "request_context.file_contents", promptGuardRequestFileChars, promptGuardRequestFilesTotalChars, promptGuardRequestFilesMaxCount)
}

func hasRealtimeRequestContextContent(requestContext *agentv1.RequestContext) bool {
	if requestContext == nil {
		return false
	}
	return len(requestContext.GetRules()) > 0 ||
		len(requestContext.GetFileContents()) > 0 ||
		strings.TrimSpace(requestContext.GetUserIntentSummary()) != "" ||
		strings.TrimSpace(requestContext.GetHooksAdditionalContext()) != "" ||
		strings.TrimSpace(requestContext.GetCommitAttributionMessage()) != "" ||
		strings.TrimSpace(requestContext.GetPrAttributionMessage()) != ""
}

func collectSkillDescriptors(requestContext *agentv1.RequestContext) []*agentv1.SkillDescriptor {
	if requestContext == nil {
		return nil
	}

	orderedKeys := make([]string, 0, 16)
	descriptorsByKey := make(map[string]*agentv1.SkillDescriptor)
	addDescriptor := func(descriptor *agentv1.SkillDescriptor) {
		normalized := normalizeSkillDescriptor(descriptor)
		if normalized == nil {
			return
		}
		key := skillDescriptorKey(normalized)
		if key == "" {
			return
		}
		if existing, ok := descriptorsByKey[key]; ok {
			mergeSkillDescriptor(existing, normalized)
			return
		}
		orderedKeys = append(orderedKeys, key)
		descriptorsByKey[key] = normalized
	}

	for _, descriptor := range requestContext.GetSkillOptions().GetSkillDescriptors() {
		addDescriptor(descriptor)
	}
	for _, skill := range requestContext.GetAgentSkills() {
		addDescriptor(skillDescriptorFromAgentSkill(skill))
	}
	for _, rule := range requestContext.GetRules() {
		if isSkillFilePath(rule.GetFullPath()) {
			addDescriptor(skillDescriptorFromCursorRule(rule))
		}
	}

	descriptors := make([]*agentv1.SkillDescriptor, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if descriptor := descriptorsByKey[key]; descriptor != nil {
			descriptors = append(descriptors, descriptor)
		}
	}
	return descriptors
}

func filterNonSkillRules(rules []*agentv1.CursorRule) []*agentv1.CursorRule {
	if len(rules) == 0 {
		return nil
	}

	filtered := make([]*agentv1.CursorRule, 0, len(rules))
	for _, rule := range rules {
		if rule == nil {
			continue
		}
		if isSkillFilePath(rule.GetFullPath()) {
			continue
		}
		filtered = append(filtered, rule)
	}
	return filtered
}

func skillDescriptorFromAgentSkill(skill *agentv1.AgentSkill) *agentv1.SkillDescriptor {
	if skill == nil {
		return nil
	}
	fullPath := strings.TrimSpace(skill.GetFullPath())
	if fullPath == "" {
		return nil
	}

	parseError := strings.TrimSpace(skill.GetParseError())
	descriptor := &agentv1.SkillDescriptor{
		Name:           inferSkillName(fullPath),
		Description:    strings.TrimSpace(skill.GetDescription()),
		FolderPath:     filepath.Dir(fullPath),
		Enabled:        parseError == "",
		ReadmeFilePath: fullPath,
		PackageType:    inferSkillPackageType(fullPath),
	}
	if parseError != "" {
		descriptor.ParseError = &parseError
	}
	return descriptor
}

func skillDescriptorFromCursorRule(rule *agentv1.CursorRule) *agentv1.SkillDescriptor {
	if rule == nil {
		return nil
	}
	fullPath := strings.TrimSpace(rule.GetFullPath())
	if fullPath == "" || !isSkillFilePath(fullPath) {
		return nil
	}

	description := ""
	if rule.GetType() != nil && rule.GetType().GetAgentFetched() != nil {
		description = strings.TrimSpace(rule.GetType().GetAgentFetched().GetDescription())
	}
	parseError := strings.TrimSpace(rule.GetParseError())
	descriptor := &agentv1.SkillDescriptor{
		Name:           inferSkillName(fullPath),
		Description:    description,
		FolderPath:     filepath.Dir(fullPath),
		Enabled:        parseError == "",
		ReadmeFilePath: fullPath,
		PackageType:    inferSkillPackageType(fullPath),
	}
	if parseError != "" {
		descriptor.ParseError = &parseError
	}
	return descriptor
}

func normalizeSkillDescriptor(descriptor *agentv1.SkillDescriptor) *agentv1.SkillDescriptor {
	if descriptor == nil {
		return nil
	}

	cloned := proto.Clone(descriptor).(*agentv1.SkillDescriptor)
	cloned.Name = strings.TrimSpace(cloned.GetName())
	cloned.Description = strings.TrimSpace(cloned.GetDescription())
	cloned.FolderPath = strings.TrimSpace(cloned.GetFolderPath())
	cloned.ReadmeFilePath = strings.TrimSpace(cloned.GetReadmeFilePath())
	if cloned.ReadmeFilePath == "" && cloned.FolderPath != "" {
		cloned.ReadmeFilePath = filepath.Join(cloned.FolderPath, "SKILL.md")
	}
	if cloned.FolderPath == "" && cloned.ReadmeFilePath != "" {
		cloned.FolderPath = filepath.Dir(cloned.ReadmeFilePath)
	}
	if cloned.Name == "" {
		cloned.Name = inferSkillName(cloned.GetReadmeFilePath())
	}
	if cloned.PackageType == agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		cloned.PackageType = inferSkillPackageType(firstNonEmpty(cloned.GetReadmeFilePath(), cloned.GetFolderPath()))
	}
	if cloned.GetReadmeFilePath() == "" || cloned.GetDescription() == "" {
		return nil
	}
	return cloned
}

func mergeSkillDescriptor(target *agentv1.SkillDescriptor, candidate *agentv1.SkillDescriptor) {
	if target == nil || candidate == nil {
		return
	}
	if strings.TrimSpace(target.GetDescription()) == "" && strings.TrimSpace(candidate.GetDescription()) != "" {
		target.Description = strings.TrimSpace(candidate.GetDescription())
	}
	if strings.TrimSpace(target.GetReadmeFilePath()) == "" && strings.TrimSpace(candidate.GetReadmeFilePath()) != "" {
		target.ReadmeFilePath = strings.TrimSpace(candidate.GetReadmeFilePath())
	}
	if strings.TrimSpace(target.GetFolderPath()) == "" && strings.TrimSpace(candidate.GetFolderPath()) != "" {
		target.FolderPath = strings.TrimSpace(candidate.GetFolderPath())
	}
	if !target.GetEnabled() && candidate.GetEnabled() {
		target.Enabled = true
	}
	if target.GetPackageType() == agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED && candidate.GetPackageType() != agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED {
		target.PackageType = candidate.GetPackageType()
	}
	if strings.TrimSpace(target.GetParseError()) == "" && strings.TrimSpace(candidate.GetParseError()) != "" {
		parseError := strings.TrimSpace(candidate.GetParseError())
		target.ParseError = &parseError
	}
}

func skillDescriptorKey(descriptor *agentv1.SkillDescriptor) string {
	if descriptor == nil {
		return ""
	}
	if readme := strings.TrimSpace(descriptor.GetReadmeFilePath()); readme != "" {
		return readme
	}
	if folder := strings.TrimSpace(descriptor.GetFolderPath()); folder != "" {
		return folder
	}
	return strings.TrimSpace(descriptor.GetName())
}

func buildSkillDiscoveryMessage(requestContext *agentv1.RequestContext) string {
	normalized := normalizeRequestContextForStorageMode(requestContext, true)
	if normalized == nil || normalized.GetSkillOptions() == nil || len(normalized.GetSkillOptions().GetSkillDescriptors()) == 0 {
		return ""
	}

	lines := []string{
		"<agent_skills>",
		"When users ask you to perform tasks, check if any of the available skills below can help complete the task more effectively. Skills provide specialized capabilities and domain knowledge. To use a skill, read the skill file at the provided absolute path using the Read tool, then follow the instructions within. When a skill is relevant, read and follow it IMMEDIATELY as your first action. NEVER just announce or mention a skill without actually reading and following it. Only use skills listed below.",
		"",
		`<available_skills description="Skills the agent can use. Use the Read tool with the provided absolute path to fetch full contents.">`,
	}
	for _, descriptor := range normalized.GetSkillOptions().GetSkillDescriptors() {
		if descriptor == nil {
			continue
		}
		fullPath := strings.TrimSpace(descriptor.GetReadmeFilePath())
		description := strings.TrimSpace(descriptor.GetDescription())
		if fullPath == "" || description == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf(`<agent_skill fullPath="%s">%s</agent_skill>`, escapeSkillPromptXML(fullPath), escapeSkillPromptXML(description)))
	}
	lines = append(lines, "</available_skills>", "</agent_skills>")
	if len(lines) == 6 {
		return ""
	}
	return strings.Join(lines, "\n\n")
}

func collectMCPToolServers(requestContext *agentv1.RequestContext) map[string]string {
	if requestContext == nil {
		return nil
	}

	servers := make(map[string]string)
	addDescriptors := func(descriptors []*agentv1.McpDescriptor) {
		for _, descriptor := range descriptors {
			if descriptor == nil {
				continue
			}
			serverIdentifier := firstNonEmpty(descriptor.GetServerIdentifier(), descriptor.GetServerName())
			if strings.TrimSpace(serverIdentifier) == "" {
				continue
			}
			for _, tool := range descriptor.GetTools() {
				if tool == nil {
					continue
				}
				toolName := strings.TrimSpace(tool.GetToolName())
				if toolName == "" {
					continue
				}
				if _, exists := servers[toolName]; exists {
					continue
				}
				servers[toolName] = strings.TrimSpace(serverIdentifier)
			}
		}
	}

	addDescriptors(requestContext.GetMcpFileSystemOptions().GetMcpDescriptors())
	addDescriptors(requestContext.GetMcpMetaToolOptions().GetMcpDescriptors())
	if len(servers) == 0 {
		return nil
	}
	return servers
}

func escapeSkillPromptXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func inferSkillName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(filepath.Base(trimmed), "SKILL.md") {
		return strings.TrimSpace(filepath.Base(filepath.Dir(trimmed)))
	}
	return strings.TrimSpace(strings.TrimSuffix(filepath.Base(trimmed), filepath.Ext(trimmed)))
}

func inferSkillPackageType(path string) agentv1.PackageType {
	normalized := strings.ToLower(strings.TrimSpace(path))
	switch {
	case strings.Contains(normalized, "/.agents/skills/"):
		return agentv1.PackageType_PACKAGE_TYPE_CURSOR_PROJECT
	case strings.Contains(normalized, "/.cursor/skills/"),
		strings.Contains(normalized, "/.cursor/skills-cursor/"),
		strings.Contains(normalized, "/.codex/skills/"):
		return agentv1.PackageType_PACKAGE_TYPE_CURSOR_PERSONAL
	default:
		return agentv1.PackageType_PACKAGE_TYPE_UNSPECIFIED
	}
}

func isSkillFilePath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	return strings.EqualFold(filepath.Base(trimmed), "SKILL.md")
}
