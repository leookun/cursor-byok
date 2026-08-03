package forwarder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

var (
	transcriptContextTagPatterns = compileTranscriptContextTagPatterns([]string{
		"user_info",
		"project_layout",
		"rules",
		"always_applied_workspace_rules",
		"agent_requestable_workspace_rules",
		"user_rules",
		"agent_skills",
		"available_skills",
		"cloud_instructions",
		"cloud_task_instructions",
		"open_and_recently_viewed_files",
		"system_reminder",
		"system-reminder",
		"mcp_instructions",
		"mcp_file_system",
		"mcp_file_system_servers",
		"git_status",
		"agent_transcripts",
		"cursor_rules_context",
		"attached_files",
		"system_notification",
		"task_notification",
		"agent_notification",
	})
	transcriptThinkingPattern   = regexp.MustCompile(`(?is)<(?:think|thinking)>.*?</(?:think|thinking)>`)
	transcriptBlankLinesPattern = regexp.MustCompile(`\n{3,}`)
)

type cursorTranscriptLine struct {
	Role    string                   `json:"role,omitempty"`
	Message *cursorTranscriptMessage `json:"message,omitempty"`
	Type    string                   `json:"type,omitempty"`
	Status  string                   `json:"status,omitempty"`
	Error   string                   `json:"error,omitempty"`
}

type cursorTranscriptMessage struct {
	Content []cursorTranscriptContent `json:"content"`
}

type cursorTranscriptContent struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// projectCursorTranscriptJSONL projects the local semantic history into Cursor's
// current agent transcript JSONL contract. context.json remains the source of truth.
func projectCursorTranscriptJSONL(conversation *ConversationFile) ([]byte, error) {
	return projectCursorTranscriptJSONLWithLatestStatus(conversation, false)
}

func projectCursorTranscriptJSONLWithLatestStatus(conversation *ConversationFile, includeLatestStatus bool) ([]byte, error) {
	if conversation == nil {
		return nil, nil
	}
	lines := make([]cursorTranscriptLine, 0, len(conversation.Entries))
	maxTurnSeq := int64(0)
	for _, entry := range conversation.Entries {
		if entry.TurnSeq > maxTurnSeq {
			maxTurnSeq = entry.TurnSeq
		}
	}
	currentTurnSeq := int64(0)
	pendingTurnStatus := cursorTranscriptLine{}
	flushTurnStatus := func() {
		if currentTurnSeq > 0 && (includeLatestStatus || currentTurnSeq < maxTurnSeq) && pendingTurnStatus.Type != "" {
			lines = append(lines, pendingTurnStatus)
		}
		pendingTurnStatus = cursorTranscriptLine{}
	}
	for _, entry := range conversation.Entries {
		if entry.TurnSeq > 0 && entry.TurnSeq != currentTurnSeq {
			flushTurnStatus()
			currentTurnSeq = entry.TurnSeq
		}
		projected, ok, err := projectCursorTranscriptEntry(entry)
		if err != nil {
			return nil, err
		}
		if ok {
			lines = append(lines, projected)
		}
		if status, ok := cursorTranscriptTurnStatus(entry); ok {
			pendingTurnStatus = status
		}
	}
	flushTurnStatus()

	if len(lines) == 0 {
		return nil, nil
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, line := range lines {
		if err := encoder.Encode(line); err != nil {
			return nil, fmt.Errorf("encode cursor transcript line: %w", err)
		}
	}
	return output.Bytes(), nil
}

func projectCursorTranscriptEntry(entry HistoryEntry) (cursorTranscriptLine, bool, error) {
	switch strings.TrimSpace(entry.Kind) {
	case "user_message":
		message := &agentv1.UserMessage{}
		if err := protojson.Unmarshal(entry.Payload, message); err != nil {
			return cursorTranscriptLine{}, false, fmt.Errorf("decode transcript user_message: %w", err)
		}
		text := cleanCursorTranscriptUserText(message.GetText())
		if text == "" {
			return cursorTranscriptLine{}, false, nil
		}
		return cursorTranscriptTextLine("user", text), true, nil
	case "assistant_text":
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return cursorTranscriptLine{}, false, fmt.Errorf("decode transcript assistant_text: %w", err)
		}
		text := cleanCursorTranscriptAssistantText(payload.Text)
		thinking := strings.TrimSpace(payload.ReasoningContent)
		content := joinTranscriptText(text, thinking)
		if content == "" {
			return cursorTranscriptLine{}, false, nil
		}
		return cursorTranscriptTextLine("assistant", content), true, nil
	case "tool_call":
		var payload toolCallEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return cursorTranscriptLine{}, false, fmt.Errorf("decode transcript tool_call: %w", err)
		}
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
			return cursorTranscriptLine{}, false, fmt.Errorf("decode transcript tool_call payload: %w", err)
		}
		descriptor, ok := promptengine.BuildToolCallReplayDescriptor(firstNonEmpty(payload.ToolCallID, entry.ToolCallID), toolCall)
		if !ok {
			return cursorTranscriptLine{}, false, nil
		}
		return cursorTranscriptToolCallLine(descriptor.Function.Name, descriptor.Function.Arguments, payload.ReasoningContent), true, nil
	case "model_message":
		var payload modelMessageEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return cursorTranscriptLine{}, false, fmt.Errorf("decode transcript model_message: %w", err)
		}
		return projectCursorTranscriptModelMessage(payload.Message)
	default:
		return cursorTranscriptLine{}, false, nil
	}
}

func projectCursorTranscriptModelMessage(message modeladapter.Message) (cursorTranscriptLine, bool, error) {
	role := strings.TrimSpace(message.Role)
	if role == "" || role == "system" || role == "tool" {
		return cursorTranscriptLine{}, false, nil
	}
	content := make([]cursorTranscriptContent, 0, len(message.ToolCalls)+1)
	texts := make([]string, 0, len(message.ContentParts)+2)
	if text := strings.TrimSpace(message.Content); text != "" {
		texts = append(texts, text)
	}
	for _, part := range message.ContentParts {
		switch strings.TrimSpace(strings.ToLower(part.Type)) {
		case "text", "":
			if text := strings.TrimSpace(part.Text); text != "" {
				texts = append(texts, text)
			}
		case "image":
			texts = append(texts, "[Image]")
		}
	}
	if thinking := strings.TrimSpace(message.ReasoningContent); thinking != "" {
		texts = append(texts, thinking)
	}
	if len(texts) > 0 {
		text := strings.Join(texts, "\n\n")
		if role == "user" {
			text = cleanCursorTranscriptUserText(text)
		} else if role == "assistant" {
			text = cleanCursorTranscriptAssistantText(text)
		}
		if text != "" {
			content = append(content, cursorTranscriptContent{Type: "text", Text: text})
		}
	}
	for _, call := range message.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		content = append(content, cursorTranscriptContent{
			Type:  "tool_use",
			Name:  name,
			Input: decodeTranscriptToolInput(call.Function.Arguments),
		})
	}
	if len(content) == 0 {
		return cursorTranscriptLine{}, false, nil
	}
	return cursorTranscriptLine{Role: role, Message: &cursorTranscriptMessage{Content: content}}, true, nil
}

func cursorTranscriptTextLine(role string, text string) cursorTranscriptLine {
	if strings.TrimSpace(text) == "" {
		return cursorTranscriptLine{}
	}
	return cursorTranscriptLine{
		Role: strings.TrimSpace(role),
		Message: &cursorTranscriptMessage{Content: []cursorTranscriptContent{{
			Type: "text",
			Text: text,
		}}},
	}
}

func cursorTranscriptToolCallLine(name string, arguments string, reasoning string) cursorTranscriptLine {
	content := make([]cursorTranscriptContent, 0, 2)
	if thinking := strings.TrimSpace(reasoning); thinking != "" {
		content = append(content, cursorTranscriptContent{Type: "text", Text: thinking})
	}
	content = append(content, cursorTranscriptContent{
		Type:  "tool_use",
		Name:  strings.TrimSpace(name),
		Input: decodeTranscriptToolInput(arguments),
	})
	return cursorTranscriptLine{Role: "assistant", Message: &cursorTranscriptMessage{Content: content}}
}

func decodeTranscriptToolInput(arguments string) any {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return trimmed
}

func cursorTranscriptTurnStatus(entry HistoryEntry) (cursorTranscriptLine, bool) {
	if strings.TrimSpace(entry.Kind) != "metadata" || entry.TurnSeq <= 0 {
		return cursorTranscriptLine{}, false
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return cursorTranscriptLine{}, false
	}
	switch strings.TrimSpace(payload.Type) {
	case "turn_completed":
		return cursorTranscriptLine{Type: "turn_ended", Status: "success"}, true
	case "provider_error", "failed":
		return cursorTranscriptLine{
			Type:   "turn_ended",
			Status: "error",
			Error:  firstNonEmpty(readStringValue(payload.Value["error"]), readStringValue(payload.Value["message"]), "Request failed"),
		}, true
	case "control":
		if strings.TrimSpace(readStringValue(payload.Value["status"])) != "canceled" {
			return cursorTranscriptLine{}, false
		}
		return cursorTranscriptLine{
			Type:   "turn_ended",
			Status: "aborted",
			Error:  firstNonEmpty(readStringValue(payload.Value["reason"]), readStringValue(payload.Value["message"]), "User aborted request"),
		}, true
	default:
		return cursorTranscriptLine{}, false
	}
}

func cleanCursorTranscriptUserText(text string) string {
	return cleanTranscriptContextTags(text)
}

func cleanCursorTranscriptAssistantText(text string) string {
	cleaned := transcriptThinkingPattern.ReplaceAllString(text, "")
	return collapseTranscriptBlankLines(cleaned)
}

func cleanTranscriptContextTags(text string) string {
	cleaned := text
	for _, pattern := range transcriptContextTagPatterns {
		cleaned = pattern.ReplaceAllString(cleaned, "")
	}
	return collapseTranscriptBlankLines(cleaned)
}

func compileTranscriptContextTagPatterns(tags []string) []*regexp.Regexp {
	patterns := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		patterns = append(patterns, regexp.MustCompile(`(?is)<`+regexp.QuoteMeta(tag)+`(?:\s[^>]*)?>.*?</`+regexp.QuoteMeta(tag)+`>`))
	}
	return patterns
}

func collapseTranscriptBlankLines(text string) string {
	return strings.TrimSpace(transcriptBlankLinesPattern.ReplaceAllString(text, "\n\n"))
}

func joinTranscriptText(text string, thinking string) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(text) != "" {
		parts = append(parts, strings.TrimSpace(text))
	}
	if strings.TrimSpace(thinking) != "" {
		parts = append(parts, strings.TrimSpace(thinking))
	}
	return strings.Join(parts, "\n\n")
}

func normalizeAgentTranscriptsFolder(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if filepath.Base(cleaned) != "agent-transcripts" {
		return ""
	}
	return cleaned
}

func agentTranscriptsFolderFromEntries(entries []HistoryEntry) string {
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "request_context" {
			continue
		}
		requestContext := &agentv1.RequestContext{}
		if err := protojson.Unmarshal(entry.Payload, requestContext); err != nil {
			continue
		}
		if folder := normalizeAgentTranscriptsFolder(requestContext.GetEnv().GetAgentTranscriptsFolder()); folder != "" {
			return folder
		}
	}
	return ""
}

func cursorTranscriptPath(transcriptsFolder string, conversationID string) (string, error) {
	folder := normalizeAgentTranscriptsFolder(transcriptsFolder)
	if folder == "" {
		return "", fmt.Errorf("invalid agent transcripts folder")
	}
	id, err := validateConversationID(conversationID)
	if err != nil {
		return "", err
	}
	return filepath.Join(folder, id, id+".jsonl"), nil
}

func preserveCursorAppendedTurnEnded(path string, projected []byte) []byte {
	existing, err := os.ReadFile(path)
	if err != nil {
		return projected
	}
	lastLine := lastNonEmptyJSONLLine(existing)
	if len(lastLine) == 0 {
		return projected
	}
	var terminal cursorTranscriptLine
	if json.Unmarshal(lastLine, &terminal) != nil || terminal.Type != "turn_ended" {
		return projected
	}
	if countTranscriptTurnEnded(existing) <= countTranscriptTurnEnded(projected) {
		return projected
	}
	result := append([]byte(nil), projected...)
	if len(result) > 0 && result[len(result)-1] != '\n' {
		result = append(result, '\n')
	}
	result = append(result, lastLine...)
	return append(result, '\n')
}

func lastNonEmptyJSONLLine(data []byte) []byte {
	lines := bytes.Split(data, []byte{'\n'})
	for index := len(lines) - 1; index >= 0; index-- {
		if line := bytes.TrimSpace(lines[index]); len(line) > 0 {
			return append([]byte(nil), line...)
		}
	}
	return nil
}

func countTranscriptTurnEnded(data []byte) int {
	count := 0
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var item cursorTranscriptLine
		if json.Unmarshal(trimmed, &item) == nil && item.Type == "turn_ended" {
			count++
		}
	}
	return count
}

func writeCursorTranscriptAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create transcript directory: %w", err)
	}
	file, tempPath, err := openUniqueArtifactTempFile(path)
	if err != nil {
		return fmt.Errorf("open transcript temp file: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write transcript temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transcript temp file: %w", err)
	}
	if err := renameArtifactTempFile(tempPath, path); err != nil {
		return fmt.Errorf("rename transcript temp file: %w", err)
	}
	renamed = true
	return syncDirectory(filepath.Dir(path))
}
