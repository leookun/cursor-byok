// engine.go 实现 MVP 阶段的 prompt 编译器与工具过滤逻辑。
package promptengine

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	promptassets "cursor/prompt"
)

const todoSectionReminderMessage = "<system_reminder>\nYou are currently under the todo section, be sure to track tasks and do not forget to update.\n</system_reminder>"

// Message 表示内部统一的模型消息结构。
type Message struct {
	// Role 表示消息角色，例如 system、user、assistant。
	Role string `json:"role"`
	// Content 表示消息文本内容。
	Content string `json:"content"`
	// ContentParts 表示消息中的结构化内容块，例如文本或图片。
	ContentParts []ContentPart `json:"content_parts,omitempty"`
	// ReasoningContent 表示推理内容（用于支持 reasoning 的模型）。
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// ReasoningSignature 表示 provider 对推理内容签发的签名。
	ReasoningSignature string `json:"reasoning_signature,omitempty"`
	// ReasoningSignatureSource 表示 reasoning signature 的 provider 语义来源。
	ReasoningSignatureSource string `json:"reasoning_signature_source,omitempty"`
	// OpenAIResponsesReasoningID 保存 Responses reasoning output item 的原始 id。
	OpenAIResponsesReasoningID string `json:"openai_responses_reasoning_id,omitempty"`
	// OpenAIResponsesReasoningStatus 保存 Responses reasoning output item 的原始 status。
	OpenAIResponsesReasoningStatus string `json:"openai_responses_reasoning_status,omitempty"`
	// OpenAIResponsesReasoningSummary 保存 Responses reasoning output item 的原始 summary。
	OpenAIResponsesReasoningSummary json.RawMessage `json:"openai_responses_reasoning_summary,omitempty"`
	// ToolCalls 表示 assistant 消息中的函数调用。
	ToolCalls []ToolCallDescriptor `json:"tool_calls,omitempty"`
	// ToolCallID 表示 tool role 关联的调用 id。
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Name 表示 tool role 工具名。
	Name string `json:"name,omitempty"`
}

type ToolCallDescriptor struct {
	ID                    string                `json:"id"`
	Index                 int                   `json:"index,omitempty"`
	Type                  string                `json:"type"`
	Function              ToolCallFunctionShape `json:"function"`
	OpenAIResponsesID     string                `json:"openai_responses_id,omitempty"`
	OpenAIResponsesCallID string                `json:"openai_responses_call_id,omitempty"`
	OpenAIResponsesStatus string                `json:"openai_responses_status,omitempty"`
}

type ToolCallFunctionShape struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// CompileInput 描述一次 prompt 编译所需的最小输入。
type CompileInput struct {
	// Mode 表示当前运行模式。
	Mode agentv1.AgentMode
	// RequestedModelName 表示当前请求实际使用的模型名称。
	RequestedModelName string
	// ConversationState 表示当前会话结构化状态。
	ConversationState *agentv1.ConversationStateStructure
	// RequestContext 表示本轮用户动作携带的 request_context。
	RequestContext *agentv1.RequestContext
	// PendingAssistantOutputs 表示尚未收口的 assistant 输出记录。
	PendingAssistantOutputs []string
	// HistoryTurns 保存当前回合历史文本摘要。
	HistoryTurns []string
	// LatestExternalResults 保存最近一次外部桥结果。
	LatestExternalResults []runtimecore.ExternalResultSummary
	// CustomSystemPrompt 表示当前请求附带的自定义系统提示词。
	CustomSystemPrompt string
	// CurrentUserMessageText 表示当前动作携带的用户文本。
	CurrentUserMessageText string
}

// CompiledPrompt 表示编译完成的一次模型输入。
type CompiledPrompt struct {
	// Mode 表示编译结果对应的运行模式。
	Mode agentv1.AgentMode
	// Messages 表示按顺序排列的模型消息列表。
	Messages []Message
	// Tools 表示过滤后的原始工具 JSON 列表。
	Tools []json.RawMessage
	// RequestKnobs 表示模型调用的额外参数。
	RequestKnobs map[string]any
	// CompileSummary 保存本轮编译摘要，便于调试与恢复。
	CompileSummary string
}

// PromptEngine 定义运行时依赖的 prompt 编译接口。
type PromptEngine interface {
	// Compile 根据输入编译一轮模型请求。
	Compile(input CompileInput) (CompiledPrompt, error)
}

// Engine 实现当前 MVP 阶段的 prompt 编译器。
type Engine struct {
}

// NewEngine 创建一个 prompt 编译器实例。
func NewEngine() *Engine {
	return &Engine{}
}

// Compile 编译一轮模式化 prompt。
func (engine *Engine) Compile(input CompileInput) (CompiledPrompt, error) {
	assetMode, err := mapPromptMode(input.Mode)
	if err != nil {
		return CompiledPrompt{}, err
	}

	promptText, err := promptassets.ReadPrompt(assetMode)
	if err != nil {
		return CompiledPrompt{}, err
	}
	rawTools, err := promptassets.ReadTools(assetMode)
	if err != nil {
		return CompiledPrompt{}, err
	}

	tools, toolNames, hiddenToolNames, err := decodeToolsFromBaseline(rawTools)
	if err != nil {
		return CompiledPrompt{}, err
	}

	messages := make([]Message, 0, 8)
	systemPrompt := sanitizePromptAsset(promptText, input.RequestedModelName)
	if strings.TrimSpace(systemPrompt) != "" {
		if strings.TrimSpace(input.CustomSystemPrompt) != "" {
			systemPrompt = systemPrompt + "\n\n" + strings.TrimSpace(input.CustomSystemPrompt)
		}
		messages = append(messages, Message{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	messages = append(messages, buildMessagesFromCommittedTurns(input.ConversationState)...)
	messages = append(messages, buildMessagesFromStructuredState(input.ConversationState)...)
	messages = append(messages, buildMessagesFromRequestContext(input.RequestContext)...)

	if text := strings.TrimSpace(input.CurrentUserMessageText); text != "" {
		messages = append(messages, Message{
			Role:    "user",
			Content: formatMessageText(fmt.Sprintf("<user_query>\n%s\n</user_query>", text)),
		})
	}
	messages = append(messages, buildMessagesFromPendingAssistantOutputs(input.PendingAssistantOutputs)...)

	if len(input.PendingAssistantOutputs) == 0 {
		for _, result := range input.LatestExternalResults {
			payload := strings.TrimSpace(result.Payload)
			if payload == "" {
				continue
			}
			messages = append(messages, Message{
				Role:    "assistant",
				Content: formatMessageText(fmt.Sprintf("<tool_result source=%q tool=%q>\n%s\n</tool_result>", strings.TrimSpace(result.Source), strings.TrimSpace(result.ToolName), payload)),
			})
		}
	}

	summary := fmt.Sprintf(
		"mode=%s messages=%d tools=%d hidden_tools=%d tool_names=%s committed_turns=%d pending=%d external_results=%d",
		input.Mode.String(),
		len(messages),
		len(tools),
		len(hiddenToolNames),
		strings.Join(toolNames, ","),
		countCommittedTurns(input.ConversationState),
		len(input.PendingAssistantOutputs),
		len(input.LatestExternalResults),
	)

	return CompiledPrompt{
		Mode:     input.Mode,
		Messages: messages,
		Tools:    tools,
		RequestKnobs: map[string]any{
			"stream":     true,
			"max_tokens": 65536,
		},
		CompileSummary: summary,
	}, nil
}

// countCommittedTurns 统计当前会话状态中的已提交 turn 数。
func countCommittedTurns(state *agentv1.ConversationStateStructure) int {
	if state == nil {
		return 0
	}
	return len(state.GetTurns())
}

// buildMessagesFromStructuredState 把 summary / plan / todos 编译成下一轮模型可消费的消息。
func buildMessagesFromStructuredState(state *agentv1.ConversationStateStructure) []Message {
	if state == nil {
		return nil
	}

	messages := make([]Message, 0, 4)
	if summary, ok := decodeSummaryForPrompt(state); ok {
		messages = append(messages, Message{
			Role:    "user",
			Content: fmt.Sprintf("<conversation_summary>\n%s\n</conversation_summary>", summary),
		})
	}
	if plan, ok := decodePlanForPrompt(state); ok {
		messages = append(messages, Message{
			Role:    "user",
			Content: fmt.Sprintf("<current_plan>\n%s\n</current_plan>", plan),
		})
	}
	if todos, ok := decodeTodosForPrompt(state); ok {
		messages = append(messages, Message{
			Role:    "user",
			Content: fmt.Sprintf("<todo_list>\n%s\n</todo_list>", todos),
		})
		messages = append(messages, Message{
			Role:    "user",
			Content: todoSectionReminderMessage,
		})
	}
	return messages
}

// buildMessagesFromRequestContext 把 request_context 中的用户环境信息编译成上游可消费消息。
func buildMessagesFromRequestContext(requestContext *agentv1.RequestContext) []Message {
	if requestContext == nil {
		return nil
	}

	sections := append(buildRequestContextStaticSections(requestContext), buildRequestContextRealtimeSections(requestContext)...)
	if len(sections) == 0 {
		return nil
	}
	return []Message{{
		Role:    "user",
		Content: strings.Join(sections, "\n\n"),
	}}
}

// BuildRequestContextReplayMessages 导出 request_context 的精简回放形状，供其他链路复用。
func BuildRequestContextReplayMessages(requestContext *agentv1.RequestContext) []Message {
	return buildMessagesFromRequestContext(requestContext)
}

func buildRequestContextStaticSections(requestContext *agentv1.RequestContext) []string {
	if requestContext == nil {
		return nil
	}
	sections := make([]string, 0, 5)
	if userInfo := buildRequestContextUserInfoSection(requestContext); userInfo != "" {
		sections = append(sections, userInfo)
	}
	if transcripts := buildRequestContextAgentTranscriptsSection(requestContext); transcripts != "" {
		sections = append(sections, transcripts)
	}
	if rules := buildRequestContextRulesSection(requestContext); rules != "" {
		sections = append(sections, rules)
	}
	if skills := buildRequestContextAgentSkillsSection(requestContext); skills != "" {
		sections = append(sections, skills)
	}
	if mcp := buildRequestContextMCPFileSystemSection(requestContext); mcp != "" {
		sections = append(sections, mcp)
	}
	return sections
}

func buildRequestContextRealtimeSections(requestContext *agentv1.RequestContext) []string {
	if requestContext == nil {
		return nil
	}
	sections := make([]string, 0, 5)
	if summary := buildRequestContextUserIntentSummarySection(requestContext); summary != "" {
		sections = append(sections, summary)
	}
	if hooks := buildRequestContextHooksAdditionalContextSection(requestContext); hooks != "" {
		sections = append(sections, hooks)
	}
	if fileContents := buildRequestContextCurrentFileContentsSection(requestContext); fileContents != "" {
		sections = append(sections, fileContents)
	}
	if commit := buildRequestContextCommitAttributionSection(requestContext); commit != "" {
		sections = append(sections, commit)
	}
	if pr := buildRequestContextPRAttributionSection(requestContext); pr != "" {
		sections = append(sections, pr)
	}
	return sections
}

// buildRequestContextUserInfoSection 构造 <user_info> 片段。
func buildRequestContextUserInfoSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil || requestContext.GetEnv() == nil {
		return ""
	}

	env := requestContext.GetEnv()
	lines := make([]string, 0, 8)
	if osVersion := strings.TrimSpace(env.GetOsVersion()); osVersion != "" {
		lines = append(lines, "OS Version: "+osVersion)
	}
	if shell := strings.TrimSpace(env.GetShell()); shell != "" {
		lines = append(lines, "Shell: "+shell)
	}
	if workspacePaths := env.GetWorkspacePaths(); len(workspacePaths) > 0 {
		lines = append(lines, buildWorkspacePathsSection(workspacePaths))
	}
	isGitRepo := "Unknown"
	repositoryInfo := requestContext.GetRepositoryInfo()
	if len(repositoryInfo) > 0 {
		if repositoryInfo[0].GetIsLocal() {
			isGitRepo = "Yes"
		} else {
			isGitRepo = "No"
		}
	}
	lines = append(lines, "Is directory a git repo: "+isGitRepo)
	lines = append(lines, "Today's date: "+formatRequestContextDate())
	if terminalsFolder := strings.TrimSpace(env.GetTerminalsFolder()); terminalsFolder != "" {
		lines = append(lines, "Terminals folder: "+terminalsFolder)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<user_info>\n" + strings.Join(lines, "\n\n") + "\n</user_info>"
}

// buildWorkspacePathsSection 构造工作区路径片段。
//
// 渲染规则：
//   - 单路径：`Workspace Path: <path>`，与历史格式保持一致；
//   - 多路径：`Workspace Paths:` 起行，后续每行以 `- <path>` 列出全部路径，顺序保留输入顺序。
func buildWorkspacePathsSection(workspacePaths []string) string {
	trimmedPaths := make([]string, 0, len(workspacePaths))
	for _, path := range workspacePaths {
		if path = strings.TrimSpace(path); path != "" {
			trimmedPaths = append(trimmedPaths, path)
		}
	}
	if len(trimmedPaths) == 0 {
		return ""
	}
	if len(trimmedPaths) == 1 {
		return "Workspace Path: " + trimmedPaths[0]
	}
	items := make([]string, 0, len(trimmedPaths)+1)
	items = append(items, "Workspace Paths:")
	for _, path := range trimmedPaths {
		items = append(items, "- "+path)
	}
	return strings.Join(items, "\n")
}

// buildRequestContextAgentTranscriptsSection 构造 <agent_transcripts> 片段。
func buildRequestContextAgentTranscriptsSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil || requestContext.GetEnv() == nil {
		return ""
	}
	transcriptsFolder := strings.TrimSpace(requestContext.GetEnv().GetAgentTranscriptsFolder())
	if transcriptsFolder == "" {
		return ""
	}
	return strings.Join([]string{
		"<agent_transcripts>",
		fmt.Sprintf("Agent transcripts (past chats) live in %s. They have names like <uuid>.jsonl, cite them to the user as [<title for chat <=6 words>](<uuid excluding .jsonl>). NEVER cite subagent transcripts/IDs; you can only cite parent uuids. Don't discuss the folder structure.", transcriptsFolder),
		"</agent_transcripts>",
	}, "\n")
}

// buildRequestContextAgentSkillsSection 构造 <agent_skills> 片段。
func buildRequestContextAgentSkillsSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil {
		return ""
	}
	lines := []string{
		"<agent_skills>",
		"When users ask you to perform tasks, check if any of the available skills below can help complete the task more effectively. Skills provide specialized capabilities and domain knowledge. To use a skill, read the skill file at the provided absolute path using the Read tool, then follow the instructions within. When a skill is relevant, read and follow it IMMEDIATELY as your first action. NEVER just announce or mention a skill without actually reading and following it. Only use skills listed below.",
		"",
		`<available_skills description="Skills the agent can use. Use the Read tool with the provided absolute path to fetch full contents.">`,
	}
	seen := make(map[string]struct{}, len(requestContext.GetAgentSkills())+len(requestContext.GetSkillOptions().GetSkillDescriptors()))
	appendSkill := func(fullPath string, description string) {
		fullPath = strings.TrimSpace(fullPath)
		description = strings.TrimSpace(description)
		if fullPath == "" || description == "" {
			return
		}
		if _, ok := seen[fullPath]; ok {
			return
		}
		seen[fullPath] = struct{}{}
		lines = append(lines, fmt.Sprintf(`<agent_skill fullPath="%s">%s</agent_skill>`, fullPath, description))
	}
	for _, skill := range requestContext.GetAgentSkills() {
		if skill == nil {
			continue
		}
		appendSkill(skill.GetFullPath(), skill.GetDescription())
	}
	for _, descriptor := range requestContext.GetSkillOptions().GetSkillDescriptors() {
		if descriptor == nil {
			continue
		}
		appendSkill(descriptor.GetReadmeFilePath(), descriptor.GetDescription())
	}
	if len(lines) == 4 {
		return ""
	}
	lines = append(lines, "</available_skills>", "</agent_skills>")
	return strings.Join(lines, "\n\n")
}

// buildRequestContextRulesSection 构造 <rules> 片段。
func buildRequestContextRulesSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil || len(requestContext.GetRules()) == 0 {
		return ""
	}
	ruleLines := []string{
		"<rules>",
		"The rules section has a number of possible rules/memories/context that you should consider. In each subsection, we provide instructions about what information the subsection contains and how you should consider/follow the contents of the subsection.",
		"",
		`<user_rules description="These are rules set by the user that you should follow if appropriate.">`,
	}
	for _, rule := range requestContext.GetRules() {
		if rule == nil {
			continue
		}
		content := strings.TrimSpace(rule.GetContent())
		if content == "" {
			continue
		}
		ruleLines = append(ruleLines, "<user_rule>"+content+"</user_rule>")
	}
	ruleLines = append(ruleLines, "</user_rules>", "</rules>")
	return strings.Join(ruleLines, "\n")
}

// buildRequestContextMCPFileSystemSection 构造 <mcp_file_system> 片段。
func buildRequestContextMCPFileSystemSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil || requestContext.GetMcpFileSystemOptions() == nil {
		return ""
	}
	options := requestContext.GetMcpFileSystemOptions()
	if !options.GetEnabled() && len(options.GetMcpDescriptors()) == 0 {
		return ""
	}

	rootPath := strings.TrimSpace(options.GetWorkspaceProjectDir())
	if rootPath == "" {
		for _, descriptor := range options.GetMcpDescriptors() {
			if descriptor == nil {
				continue
			}
			if folderPath := strings.TrimSpace(descriptor.GetFolderPath()); folderPath != "" {
				rootPath = strings.TrimRight(strings.TrimSuffix(folderPath, "/"+descriptor.GetServerIdentifier()), "/")
				break
			}
		}
	}
	if rootPath == "" {
		return ""
	}
	mcpRoot := strings.TrimRight(rootPath, "/") + "/mcps"

	serverEntries := make([]string, 0, len(options.GetMcpDescriptors()))
	descriptorEntries := make([]string, 0, len(options.GetMcpDescriptors()))
	for _, descriptor := range options.GetMcpDescriptors() {
		if descriptor == nil {
			continue
		}
		serverID := strings.TrimSpace(descriptor.GetServerIdentifier())
		if serverID == "" {
			serverID = strings.TrimSpace(descriptor.GetServerName())
		}
		if serverID == "" {
			continue
		}
		folderPath := strings.TrimSpace(descriptor.GetFolderPath())
		if folderPath == "" {
			folderPath = mcpRoot + "/" + serverID
		}
		serverEntries = append(serverEntries, fmt.Sprintf(
			`<mcp_file_system_server name="%s" folderPath="%s">%s</mcp_file_system_server>`,
			escapePromptXML(serverID),
			escapePromptXML(folderPath),
			escapePromptXML(serverID),
		))
		descriptorEntries = append(descriptorEntries, buildEmbeddedMCPDescriptorSection(descriptor, serverID, folderPath))
	}
	if len(serverEntries) == 0 {
		return ""
	}

	embeddedDescriptors := ""
	if joined := strings.TrimSpace(strings.Join(filterNonEmptyStrings(descriptorEntries), "\n\n")); joined != "" {
		embeddedDescriptors = "\n\nEmbedded MCP descriptors:\n\n<mcp_embedded_descriptors>" + joined + "</mcp_embedded_descriptors>"
	}

	return fmt.Sprintf(
		"<mcp_file_system>\n\n## MCP Tool Access\n\nYou have access to MCP tools through the MCP FileSystem. Embedded MCP tool descriptors below already satisfy the schema-discovery requirement when present. Only browse descriptor files under %s/<server>/tools/ when a server below does not include the tool you need.\n\n## MCP Resource Access\n\nYou also have access to MCP resources through the MCP FileSystem. Resource descriptors live under %s/<server>/resources/.\n\nAvailable MCP servers:\n\n<mcp_file_system_servers>%s</mcp_file_system_servers>%s\n</mcp_file_system>",
		mcpRoot,
		mcpRoot,
		strings.Join(serverEntries, "\n\n"),
		embeddedDescriptors,
	)
}

func buildRequestContextUserIntentSummarySection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil {
		return ""
	}
	summary := strings.TrimSpace(requestContext.GetUserIntentSummary())
	if summary == "" {
		return ""
	}
	return "<user_intent_summary>\n" + summary + "\n</user_intent_summary>"
}

func buildRequestContextHooksAdditionalContextSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil {
		return ""
	}
	hooks := strings.TrimSpace(requestContext.GetHooksAdditionalContext())
	if hooks == "" {
		return ""
	}
	return "<hooks_additional_context>\n" + hooks + "\n</hooks_additional_context>"
}

func buildRequestContextCurrentFileContentsSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil || len(requestContext.GetFileContents()) == 0 {
		return ""
	}
	paths := make([]string, 0, len(requestContext.GetFileContents()))
	contentsByPath := make(map[string]string, len(requestContext.GetFileContents()))
	for path, content := range requestContext.GetFileContents() {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath == "" || strings.TrimSpace(content) == "" {
			continue
		}
		if _, exists := contentsByPath[trimmedPath]; exists {
			continue
		}
		contentsByPath[trimmedPath] = content
		paths = append(paths, trimmedPath)
	}
	if len(paths) == 0 {
		return ""
	}
	sort.Strings(paths)
	entries := make([]string, 0, len(paths))
	for _, path := range paths {
		entries = append(entries, fmt.Sprintf("<file path=%q>\n%s\n</file>", escapePromptXML(path), contentsByPath[path]))
	}
	return "<current_file_contents>\n" + strings.Join(entries, "\n\n") + "\n</current_file_contents>"
}

func buildRequestContextCommitAttributionSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil {
		return ""
	}
	message := strings.TrimSpace(requestContext.GetCommitAttributionMessage())
	if message == "" {
		return ""
	}
	return "<commit_attribution_message>\n" + message + "\n</commit_attribution_message>"
}

func buildRequestContextPRAttributionSection(requestContext *agentv1.RequestContext) string {
	if requestContext == nil {
		return ""
	}
	message := strings.TrimSpace(requestContext.GetPrAttributionMessage())
	if message == "" {
		return ""
	}
	return "<pr_attribution_message>\n" + message + "\n</pr_attribution_message>"
}

func buildEmbeddedMCPDescriptorSection(descriptor *agentv1.McpDescriptor, serverID string, folderPath string) string {
	if descriptor == nil {
		return ""
	}
	toolEntries := make([]string, 0, len(descriptor.GetTools()))
	for _, tool := range descriptor.GetTools() {
		if tool == nil {
			continue
		}
		toolName := strings.TrimSpace(tool.GetToolName())
		if toolName == "" {
			continue
		}
		entry := fmt.Sprintf(`<mcp_tool name="%s">`, escapePromptXML(toolName))
		if definitionPath := strings.TrimSpace(tool.GetDefinitionPath()); definitionPath != "" {
			entry += "\n<definition_path>" + escapePromptXML(definitionPath) + "</definition_path>"
		}
		if description := strings.TrimSpace(tool.GetDescription()); description != "" {
			entry += "\n<description>" + escapePromptXML(description) + "</description>"
		}
		if inputSchema := strings.TrimSpace(compactProtoJSON(tool.GetInputSchema())); inputSchema != "" {
			entry += "\n<input_schema>" + escapePromptXML(inputSchema) + "</input_schema>"
		}
		entry += "\n</mcp_tool>"
		toolEntries = append(toolEntries, entry)
	}
	if len(toolEntries) == 0 && strings.TrimSpace(descriptor.GetServerUseInstructions()) == "" {
		return ""
	}

	lines := []string{
		fmt.Sprintf(
			`<mcp_server_descriptor name="%s" identifier="%s" folderPath="%s">`,
			escapePromptXML(firstNonEmpty(strings.TrimSpace(descriptor.GetServerName()), serverID)),
			escapePromptXML(serverID),
			escapePromptXML(folderPath),
		),
	}
	if instructions := strings.TrimSpace(descriptor.GetServerUseInstructions()); instructions != "" {
		lines = append(lines, "<server_use_instructions>"+escapePromptXML(instructions)+"</server_use_instructions>")
	}
	if len(toolEntries) > 0 {
		lines = append(lines, "<tools>")
		lines = append(lines, strings.Join(toolEntries, "\n"))
		lines = append(lines, "</tools>")
	}
	lines = append(lines, "</mcp_server_descriptor>")
	return strings.Join(lines, "\n")
}

// escapePromptXML 对 prompt 片段做最小 XML 转义。
func escapePromptXML(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func compactProtoJSON(message proto.Message) string {
	if message == nil {
		return ""
	}
	payload, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(message)
	if err != nil {
		return ""
	}
	return compactPromptJSON(payload)
}

func filterNonEmptyStrings(items []string) []string {
	filtered := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// formatRequestContextDate 返回与主项目对齐的日期格式。
func formatRequestContextDate() string {
	return time.Now().Format("Monday Jan 2, 2006")
}

// buildMessagesFromPendingAssistantOutputs 把 `pending_tool_calls` 中的原始记录转换为安全的 assistant 文本消息。
func buildMessagesFromPendingAssistantOutputs(rawValues []string) []Message {
	return BuildReplayMessagesFromPendingAssistantOutputs(rawValues)
}

// buildMessagesFromCommittedTurns 把已提交的 `ConversationStateStructure.turns` 映射为有序 prompt 消息。
func buildMessagesFromCommittedTurns(state *agentv1.ConversationStateStructure) []Message {
	if state == nil {
		return nil
	}
	if messages, ok := buildMessagesFromCanonicalReplay(state); ok {
		return messages
	}
	if len(state.GetTurns()) == 0 {
		return nil
	}

	messages := make([]Message, 0, len(state.GetTurns())*2)
	for _, rawTurn := range state.GetTurns() {
		if len(rawTurn) == 0 {
			continue
		}

		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			continue
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			continue
		}

		if rawUser := agentTurn.GetUserMessage(); len(rawUser) > 0 {
			userMessage := &agentv1.UserMessage{}
			if err := proto.Unmarshal(rawUser, userMessage); err == nil {
				if message, ok := BuildUserMessageReplayMessage(userMessage); ok {
					messages = append(messages, message)
				}
			}
		}

		for _, rawStep := range agentTurn.GetSteps() {
			if len(rawStep) == 0 {
				continue
			}
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(rawStep, step); err != nil {
				continue
			}

			messages = append(messages, buildMessagesFromConversationStep(step)...)
		}
	}
	return messages
}

// buildMessagesFromConversationStep 把单个 ConversationStep 映射为 prompt 消息。
func buildMessagesFromConversationStep(step *agentv1.ConversationStep) []Message {
	return BuildLegacyMessagesFromConversationStep(step)
}

func buildMessagesFromCanonicalReplay(state *agentv1.ConversationStateStructure) ([]Message, bool) {
	if state == nil || len(state.GetRootPromptMessagesJson()) == 0 {
		return nil, false
	}
	messages, err := DecodeReplayMessages(state.GetRootPromptMessagesJson())
	if err != nil {
		return nil, false
	}
	return messages, true
}

// mapPromptMode 将协议 mode 映射为静态 prompt 资产 mode。
func mapPromptMode(mode agentv1.AgentMode) (promptassets.Mode, error) {
	switch mode {
	case agentv1.AgentMode_AGENT_MODE_AGENT:
		return promptassets.ModeAgent, nil
	case agentv1.AgentMode_AGENT_MODE_ASK:
		return promptassets.ModeAsk, nil
	case agentv1.AgentMode_AGENT_MODE_PLAN:
		return promptassets.ModePlan, nil
	case agentv1.AgentMode_AGENT_MODE_DEBUG:
		return promptassets.ModeDebug, nil
	case agentv1.AgentMode_AGENT_MODE_MULTITASK:
		return promptassets.ModeMultitask, nil
	default:
		return "", fmt.Errorf("unsupported prompt compile mode: %s", mode.String())
	}
}

// sanitizePromptAsset 去除资产文件中的文档性标题，只保留真实 prompt 文本。
func sanitizePromptAsset(text string, modelName string) string {
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch trimmed {
		case "# 通用系统提示词", "# 模式静态补充", "---":
			continue
		default:
			filtered = append(filtered, line)
		}
	}
	return promptassets.RenderPromptTemplate(strings.TrimSpace(strings.Join(filtered, "\n")), modelName)
}

// decodeToolsFromBaseline 从原始工具 JSON 中按原始顺序解码工具列表与名称。
//
// 当前约束：
// 1. `tools.json` 就是该 mode 的工具暴露基线；
// 2. 必须保持原始顺序与原始工具描述结构，不得在 prompt 编译阶段做二次过滤；
// 3. 未实现能力的显式错误语义必须由 runtime 负责，而不是由 prompt 编译器隐藏工具。
func decodeToolsFromBaseline(rawTools []byte) ([]json.RawMessage, []string, []string, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(rawTools, &items); err != nil {
		return nil, nil, nil, fmt.Errorf("decode tools asset failed: %w", err)
	}

	filtered := make([]json.RawMessage, 0, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		name, err := extractToolName(item)
		if err != nil {
			return nil, nil, nil, err
		}
		filtered = append(filtered, item)
		names = append(names, name)
	}
	return filtered, names, nil, nil
}

// extractToolName 从原始工具 JSON 中提取工具名称。
func extractToolName(raw json.RawMessage) (string, error) {
	var wrapper struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return "", fmt.Errorf("decode tool descriptor failed: %w", err)
	}
	name := strings.TrimSpace(wrapper.Function.Name)
	if name == "" {
		return "", fmt.Errorf("tool descriptor name is required")
	}
	return name, nil
}

// extractPendingAssistantText 从单条 pending assistant 原始记录中提取文本内容。
func extractPendingAssistantText(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}

	var payload struct {
		Content []struct {
			Type string `json:"type,omitempty"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}

	textParts := make([]string, 0, len(payload.Content))
	for _, item := range payload.Content {
		if strings.TrimSpace(item.Type) != "text" {
			continue
		}
		if text := strings.TrimSpace(item.Text); text != "" {
			textParts = append(textParts, item.Text)
		}
	}
	if len(textParts) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(textParts, "")), true
}

func buildMessagesFromPendingAssistantRaw(raw string) []Message {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	var payload struct {
		Content []struct {
			Type       string          `json:"type,omitempty"`
			Text       string          `json:"text,omitempty"`
			Signature  string          `json:"signature,omitempty"`
			ToolCallID string          `json:"toolCallId,omitempty"`
			ToolName   string          `json:"toolName,omitempty"`
			Args       json.RawMessage `json:"args,omitempty"`
			Result     json.RawMessage `json:"result,omitempty"`
		} `json:"content,omitempty"`
	}
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		text, ok := extractPendingAssistantText(raw)
		if !ok {
			return nil
		}
		return []Message{{Role: "assistant", Content: formatMessageText(text)}}
	}

	messages := make([]Message, 0, len(payload.Content)+1)
	textParts := make([]string, 0, len(payload.Content))
	reasoningParts := make([]string, 0, len(payload.Content))
	reasoningSignature := ""
	pendingToolCalls := make([]ToolCallDescriptor, 0, len(payload.Content))
	pendingToolResults := make([]Message, 0, len(payload.Content))
	flushAssistantText := func(forceReasoningOnly bool) {
		if len(textParts) == 0 && (!forceReasoningOnly || len(reasoningParts) == 0) {
			return
		}
		messages = append(messages, Message{
			Role:               "assistant",
			Content:            formatMessageText(strings.Join(textParts, "")),
			ReasoningContent:   strings.Join(reasoningParts, ""),
			ReasoningSignature: reasoningSignature,
		})
		textParts = textParts[:0]
		reasoningParts = reasoningParts[:0]
		reasoningSignature = ""
	}
	flushToolCalls := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		messages = append(messages, Message{
			Role:               "assistant",
			Content:            "",
			ReasoningContent:   strings.Join(reasoningParts, ""),
			ReasoningSignature: reasoningSignature,
			ToolCalls:          append([]ToolCallDescriptor(nil), pendingToolCalls...),
		})
		messages = append(messages, pendingToolResults...)
		reasoningParts = reasoningParts[:0]
		reasoningSignature = ""
		pendingToolCalls = pendingToolCalls[:0]
		pendingToolResults = pendingToolResults[:0]
	}

	for _, item := range payload.Content {
		switch strings.TrimSpace(item.Type) {
		case "reasoning":
			flushToolCalls()
			flushAssistantText(false)
			if item.Text != "" {
				reasoningParts = append(reasoningParts, item.Text)
			}
			if strings.TrimSpace(item.Signature) != "" {
				reasoningSignature = strings.TrimSpace(item.Signature)
			}
		case "text":
			flushToolCalls()
			if item.Text != "" {
				textParts = append(textParts, item.Text)
			}
		case "tool-call":
			flushAssistantText(false)
			pendingToolCalls = append(pendingToolCalls, ToolCallDescriptor{
				ID:    strings.TrimSpace(item.ToolCallID),
				Index: len(pendingToolCalls),
				Type:  "function",
				Function: ToolCallFunctionShape{
					Name:      strings.TrimSpace(item.ToolName),
					Arguments: compactPromptJSON(item.Args),
				},
			})
			if resultText := extractToolResultText(item.Result); resultText != "" {
				pendingToolResults = append(pendingToolResults, Message{
					Role:       "tool",
					Name:       strings.TrimSpace(item.ToolName),
					ToolCallID: strings.TrimSpace(item.ToolCallID),
					Content:    formatMessageText(resultText),
				})
			}
		}
	}
	flushAssistantText(false)
	flushToolCalls()
	flushAssistantText(true)
	return messages
}

func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		if payload, ok := object["payload"].(string); ok && strings.TrimSpace(payload) != "" {
			return strings.TrimSpace(payload)
		}
		if text, ok := object["text"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return compactPromptJSON(raw)
}

func compactPromptJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return strings.TrimSpace(string(raw))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(string(raw))
	}
	return string(encoded)
}

func formatMessageText(text string) string {
	return strings.TrimSpace(text)
}

// decodeSummaryForPrompt 解码会话 summary，供 prompt 编译使用。
func decodeSummaryForPrompt(state *agentv1.ConversationStateStructure) (string, bool) {
	if state == nil || len(state.GetSummary()) == 0 {
		return "", false
	}
	item := &agentv1.ConversationSummary{}
	if err := proto.Unmarshal(state.GetSummary(), item); err != nil {
		return "", false
	}
	text := strings.TrimSpace(item.GetSummary())
	return text, text != ""
}

// decodePlanForPrompt 解码会话 plan，供 prompt 编译使用。
func decodePlanForPrompt(state *agentv1.ConversationStateStructure) (string, bool) {
	if state == nil || len(state.GetPlan()) == 0 {
		return "", false
	}
	item := &agentv1.ConversationPlan{}
	if err := proto.Unmarshal(state.GetPlan(), item); err != nil {
		return "", false
	}
	text := strings.TrimSpace(item.GetPlan())
	return text, text != ""
}

// decodeTodosForPrompt 解码会话 todos，供 prompt 编译使用。
func decodeTodosForPrompt(state *agentv1.ConversationStateStructure) (string, bool) {
	if state == nil || len(state.GetTodos()) == 0 {
		return "", false
	}
	lines := make([]string, 0, len(state.GetTodos()))
	for _, raw := range state.GetTodos() {
		if len(raw) == 0 {
			continue
		}
		item := &agentv1.TodoItem{}
		if err := proto.Unmarshal(raw, item); err != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s: %s", todoStatusLabel(item.GetStatus()), item.GetId(), item.GetContent()))
	}
	if len(lines) == 0 {
		return "", false
	}
	return strings.Join(lines, "\n"), true
}

// todoStatusLabel 将 proto todo 状态转为 prompt 文本标签。
func todoStatusLabel(status agentv1.TodoStatus) string {
	switch status {
	case agentv1.TodoStatus_TODO_STATUS_PENDING:
		return "pending"
	case agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS:
		return "in_progress"
	case agentv1.TodoStatus_TODO_STATUS_COMPLETED:
		return "completed"
	case agentv1.TodoStatus_TODO_STATUS_CANCELLED:
		return "cancelled"
	default:
		return "unspecified"
	}
}
