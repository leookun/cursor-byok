package promptengine

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"cursor/gen/agentv1"
)

// BuildUserQueryReplayMessage 构造一条可直接回放给模型的用户消息。
func BuildUserQueryReplayMessage(text string) (Message, bool) {
	return buildUserReplayMessage(strings.TrimSpace(text), nil)
}

// BuildUserMessageReplayMessage 把包含 selected_context 的用户消息还原为 replay message。
func BuildUserMessageReplayMessage(userMessage *agentv1.UserMessage) (Message, bool) {
	if userMessage == nil {
		return Message{}, false
	}
	return buildUserReplayMessage(strings.TrimSpace(userMessage.GetText()), userMessage.GetSelectedContext())
}

func buildUserReplayMessage(text string, selectedContext *agentv1.SelectedContext) (Message, bool) {
	images := buildSelectedImageContentParts(selectedContext)
	sections := make([]string, 0, 4)
	if text != "" {
		sections = append(sections, formatMessageText(fmt.Sprintf("<user_query>\n%s\n</user_query>", text)))
	}
	if ideState := buildSelectedIDEStatePromptSection(selectedContext); ideState != "" {
		sections = append(sections, ideState)
	}
	if selectedFiles := buildSelectedFilesPromptSection(selectedContext); selectedFiles != "" {
		sections = append(sections, selectedFiles)
	}
	content := strings.TrimSpace(strings.Join(sections, "\n\n"))
	if content == "" && len(images) == 0 {
		return Message{}, false
	}
	if len(images) == 0 {
		return Message{
			Role:    "user",
			Content: content,
		}, true
	}

	parts := make([]ContentPart, 0, len(images)+1)
	if content != "" {
		parts = append(parts, ContentPart{
			Type: "text",
			Text: content,
		})
	}
	parts = append(parts, images...)
	return Message{
		Role:         "user",
		Content:      content,
		ContentParts: parts,
	}, true
}

func buildSelectedIDEStatePromptSection(selectedContext *agentv1.SelectedContext) string {
	if selectedContext == nil || selectedContext.GetInvocationContext() == nil {
		return ""
	}
	ideState := selectedContext.GetInvocationContext().GetIdeState()
	if ideState == nil {
		return ""
	}
	sections := make([]string, 0, 2)
	if visible := buildIDEStateFilesPromptSection("visible_files", ideState.GetVisibleFiles()); visible != "" {
		sections = append(sections, visible)
	}
	if recent := buildIDEStateFilesPromptSection("recently_viewed_files", ideState.GetRecentlyViewedFiles()); recent != "" {
		sections = append(sections, recent)
	}
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func buildIDEStateFilesPromptSection(tag string, files []*agentv1.InvocationContext_IdeState_File) string {
	if len(files) == 0 {
		return ""
	}
	entries := make([]string, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		attrs := make([]string, 0, 4)
		if path := strings.TrimSpace(file.GetPath()); path != "" {
			attrs = append(attrs, fmt.Sprintf(`path="%s"`, escapePromptXML(path)))
		}
		if relativePath := strings.TrimSpace(file.GetRelativePath()); relativePath != "" {
			attrs = append(attrs, fmt.Sprintf(`relative_path="%s"`, escapePromptXML(relativePath)))
		}
		if cursor := file.GetCursorPosition(); cursor != nil && cursor.GetLine() > 0 {
			attrs = append(attrs, fmt.Sprintf(`cursor_line="%d"`, cursor.GetLine()))
		}
		if totalLines := file.GetTotalLines(); totalLines > 0 {
			attrs = append(attrs, fmt.Sprintf(`total_lines="%d"`, totalLines))
		}
		if len(attrs) == 0 {
			continue
		}
		entries = append(entries, "<file "+strings.Join(attrs, " ")+" />")
	}
	if len(entries) == 0 {
		return ""
	}
	return fmt.Sprintf("<%s>\n%s\n</%s>", tag, strings.Join(entries, "\n"), tag)
}

func buildSelectedFilesPromptSection(selectedContext *agentv1.SelectedContext) string {
	if selectedContext == nil || len(selectedContext.GetFiles()) == 0 {
		return ""
	}
	entries := make([]string, 0, len(selectedContext.GetFiles()))
	for _, file := range selectedContext.GetFiles() {
		if file == nil || strings.TrimSpace(file.GetContent()) == "" {
			continue
		}
		attrs := make([]string, 0, 2)
		if path := strings.TrimSpace(file.GetPath()); path != "" {
			attrs = append(attrs, fmt.Sprintf(`path="%s"`, escapePromptXML(path)))
		}
		if relativePath := strings.TrimSpace(file.GetRelativePath()); relativePath != "" {
			attrs = append(attrs, fmt.Sprintf(`relative_path="%s"`, escapePromptXML(relativePath)))
		}
		if len(attrs) == 0 {
			continue
		}
		entries = append(entries, "<file "+strings.Join(attrs, " ")+">\n"+file.GetContent()+"\n</file>")
	}
	if len(entries) == 0 {
		return ""
	}
	return "<selected_files>\n" + strings.Join(entries, "\n\n") + "\n</selected_files>"
}

func buildSelectedImageContentParts(selectedContext *agentv1.SelectedContext) []ContentPart {
	if selectedContext == nil {
		return nil
	}
	parts := make([]ContentPart, 0, len(selectedContext.GetSelectedImages()))
	for _, image := range selectedContext.GetSelectedImages() {
		if image == nil {
			continue
		}
		data := image.GetData()
		if len(data) == 0 {
			data = image.GetBlobIdWithData().GetData()
		}
		if len(data) == 0 && strings.TrimSpace(image.GetPath()) == "" {
			continue
		}
		parts = append(parts, ContentPart{
			Type: "image",
			Image: &ImageContent{
				MIMEType: strings.TrimSpace(image.GetMimeType()),
				Path:     strings.TrimSpace(image.GetPath()),
				Data:     data,
			},
		})
	}
	return parts
}

// EncodeReplayMessages 把 canonical replay message 编码为 root_prompt_messages_json。
func EncodeReplayMessages(messages []Message) ([][]byte, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	encoded := make([][]byte, 0, len(messages))
	for _, message := range messages {
		payload, err := marshalReplayMessage(message)
		if err != nil {
			return nil, err
		}
		encoded = append(encoded, payload)
	}
	return encoded, nil
}

func marshalReplayMessage(message Message) ([]byte, error) {
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
	if strings.TrimSpace(message.OpenAIResponsesReasoningID) != "" {
		payload["openai_responses_reasoning_id"] = strings.TrimSpace(message.OpenAIResponsesReasoningID)
	}
	if strings.TrimSpace(message.OpenAIResponsesReasoningStatus) != "" {
		payload["openai_responses_reasoning_status"] = strings.TrimSpace(message.OpenAIResponsesReasoningStatus)
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		payload["openai_responses_reasoning_summary"] = json.RawMessage(append([]byte(nil), message.OpenAIResponsesReasoningSummary...))
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

// DecodeReplayMessages 从 root_prompt_messages_json 解码 canonical replay message。
func DecodeReplayMessages(rawItems [][]byte) ([]Message, error) {
	if len(rawItems) == 0 {
		return nil, nil
	}
	messages := make([]Message, 0, len(rawItems))
	for _, raw := range rawItems {
		if len(raw) == 0 {
			continue
		}
		var message Message
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		if strings.TrimSpace(message.Role) == "" {
			continue
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// BuildReplayMessagesFromPendingAssistantOutputs 把 pending assistant raw 还原为 canonical replay message。
func BuildReplayMessagesFromPendingAssistantOutputs(rawValues []string) []Message {
	if len(rawValues) == 0 {
		return nil
	}
	messages := make([]Message, 0, len(rawValues)*3)
	for _, raw := range rawValues {
		messages = append(messages, buildMessagesFromPendingAssistantRaw(raw)...)
	}
	return messages
}

// BuildLegacyMessagesFromConversationStep 使用 legacy XML 文本形状回放单个 step。
func BuildLegacyMessagesFromConversationStep(step *agentv1.ConversationStep) []Message {
	if step == nil {
		return nil
	}

	switch item := step.GetMessage().(type) {
	case *agentv1.ConversationStep_AssistantMessage:
		text := strings.TrimSpace(item.AssistantMessage.GetText())
		if text == "" {
			return nil
		}
		return []Message{{Role: "assistant", Content: formatMessageText(text)}}
	case *agentv1.ConversationStep_ThinkingMessage:
		text := strings.TrimSpace(item.ThinkingMessage.GetText())
		if text == "" {
			return nil
		}
		return []Message{{
			Role:    "assistant",
			Content: formatMessageText(fmt.Sprintf("<thinking>\n%s\n</thinking>", text)),
		}}
	case *agentv1.ConversationStep_ToolCall:
		text := strings.TrimSpace(compactProtoJSON(item.ToolCall))
		if text == "" {
			return nil
		}
		return []Message{{
			Role:    "assistant",
			Content: formatMessageText(fmt.Sprintf("<tool_call>\n%s\n</tool_call>", text)),
		}}
	default:
		return nil
	}
}

// BuildToolCallReplayMessages 把已完成的 ToolCall step 还原为 native assistant/tool replay message。
func BuildToolCallReplayMessages(toolCallID string, toolCall *agentv1.ToolCall) ([]Message, bool) {
	descriptor, toolResult, ok := extractToolCallReplay(toolCallID, toolCall)
	if !ok {
		return nil, false
	}
	return []Message{
		{
			Role:      "assistant",
			Content:   "",
			ToolCalls: []ToolCallDescriptor{descriptor},
		},
		toolResult,
	}, true
}

// BuildToolResultReplayMessage 从已完成的 ToolCall 中提取 tool replay message，
// 用于 history 已经单独记录过 assistant tool_call 时仅回放真实工具结果。
func BuildToolResultReplayMessage(toolCallID string, toolCall *agentv1.ToolCall) (Message, bool) {
	if toolCall == nil || strings.TrimSpace(toolCallID) == "" {
		return Message{}, false
	}
	shape, ok := extractToolCallReplayShape(toolCall)
	if !ok || !shape.HasResult {
		return Message{}, false
	}
	return Message{
		Role:       "tool",
		Content:    shape.ResultJSON,
		ToolCallID: strings.TrimSpace(toolCallID),
		Name:       shape.ToolName,
	}, true
}

// BuildAssistantToolCallReplayMessage 把未完成或已完成的 ToolCall 还原为 assistant tool-call replay message。
func BuildAssistantToolCallReplayMessage(toolCallID string, toolCall *agentv1.ToolCall) (Message, bool) {
	descriptor, ok := BuildToolCallReplayDescriptor(toolCallID, toolCall)
	if !ok {
		return Message{}, false
	}
	return Message{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []ToolCallDescriptor{descriptor},
	}, true
}

// BuildToolCallReplayDescriptor 从 ToolCall proto 提取 assistant replay 所需的工具调用描述。
func BuildToolCallReplayDescriptor(toolCallID string, toolCall *agentv1.ToolCall) (ToolCallDescriptor, bool) {
	if toolCall == nil || strings.TrimSpace(toolCallID) == "" {
		return ToolCallDescriptor{}, false
	}
	shape, ok := extractToolCallReplayShape(toolCall)
	if !ok {
		return ToolCallDescriptor{}, false
	}
	return ToolCallDescriptor{
		ID:   strings.TrimSpace(toolCallID),
		Type: "function",
		Function: ToolCallFunctionShape{
			Name:      shape.ToolName,
			Arguments: firstNonEmpty(shape.ArgsJSON, "{}"),
		},
	}, true
}

func extractToolCallReplay(toolCallID string, toolCall *agentv1.ToolCall) (ToolCallDescriptor, Message, bool) {
	descriptor, ok := BuildToolCallReplayDescriptor(toolCallID, toolCall)
	if !ok {
		return ToolCallDescriptor{}, Message{}, false
	}
	toolResult, ok := BuildToolResultReplayMessage(toolCallID, toolCall)
	if !ok {
		return ToolCallDescriptor{}, Message{}, false
	}
	return descriptor, toolResult, true
}

type toolCallReplayShape struct {
	ToolName   string
	ArgsJSON   string
	ResultJSON string
	HasResult  bool
}

func extractToolCallReplayShape(toolCall *agentv1.ToolCall) (toolCallReplayShape, bool) {
	if toolCall == nil {
		return toolCallReplayShape{}, false
	}
	value := toolCall.ProtoReflect()
	oneof := value.Descriptor().Oneofs().ByName("tool")
	if oneof == nil {
		return toolCallReplayShape{}, false
	}
	selected := value.WhichOneof(oneof)
	if selected == nil {
		return toolCallReplayShape{}, false
	}
	selectedValue := value.Get(selected)
	if !selectedValue.IsValid() {
		return toolCallReplayShape{}, false
	}
	selectedMessage := selectedValue.Message()
	if !selectedMessage.IsValid() {
		return toolCallReplayShape{}, false
	}
	argsJSON, _ := extractReplayFieldJSON(selectedMessage, "args")
	resultJSON, hasResult := extractReplayFieldJSON(selectedMessage, "result")
	toolName := canonicalReplayToolName(string(selected.Name()), string(selectedMessage.Descriptor().Name()), argsJSON, resultJSON)
	if toolName == "" {
		return toolCallReplayShape{}, false
	}
	return toolCallReplayShape{
		ToolName:   toolName,
		ArgsJSON:   firstNonEmpty(argsJSON, "{}"),
		ResultJSON: resultJSON,
		HasResult:  hasResult,
	}, true
}

func extractReplayFieldJSON(message protoreflect.Message, fieldName string) (string, bool) {
	if !message.IsValid() {
		return "", false
	}
	field := message.Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	if field == nil || !message.Has(field) {
		return "", false
	}
	value := message.Get(field)
	if !value.IsValid() {
		return "", false
	}
	child := value.Message()
	if !child.IsValid() {
		return "", false
	}
	item, ok := child.Interface().(proto.Message)
	if !ok {
		return "", false
	}
	return compactProtoJSON(item), true
}

func canonicalReplayToolName(fieldName string, messageName string, argsJSON string, resultJSON string) string {
	switch strings.TrimSpace(fieldName) {
	case "mcp_tool_call":
		return "CallMcpTool"
	case "read_mcp_resource_tool_call":
		return "FetchMcpResource"
	case "update_todos_tool_call":
		return "TodoWrite"
	case "read_todos_tool_call":
		return "ReadTodos"
	case "sem_search_tool_call":
		return "SemanticSearch"
	case "edit_tool_call":
		return replayEditToolName(argsJSON, resultJSON)
	}
	trimmed := strings.TrimSuffix(strings.TrimSpace(messageName), "ToolCall")
	return strings.TrimSpace(trimmed)
}

func replayEditToolName(argsJSON string, resultJSON string) string {
	if replayEditResultLooksLikeStructuredEdit(resultJSON) {
		return "Edit"
	}
	if editArgsIndicateWrite(argsJSON) {
		return "Write"
	}
	return "Edit"
}

func editArgsIndicateWrite(argsJSON string) bool {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return false
	}
	for _, key := range []string{"stream_content", "streamContent"} {
		if _, ok := args[key]; !ok {
			continue
		}
		switch args[key].(type) {
		case string:
			return true
		case nil:
			return true
		default:
			return true
		}
	}
	return false
}

func replayEditResultLooksLikeStructuredEdit(resultJSON string) bool {
	trimmed := strings.TrimSpace(resultJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return false
	}
	success, ok := payload["success"].(map[string]any)
	if !ok || len(success) == 0 {
		return false
	}
	if _, ok := success["beforeFullFileContent"]; ok {
		return true
	}
	if _, ok := success["before_full_file_content"]; ok {
		return true
	}
	if _, ok := success["diffString"]; ok {
		return true
	}
	if _, ok := success["diff_string"]; ok {
		return true
	}
	return false
}
