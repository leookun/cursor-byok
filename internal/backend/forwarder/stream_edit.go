package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/modelchannel"
	promptassets "cursor/prompt"
)

const (
	streamEditFileContentLimit         = 100_000
	streamEditSelectionLimit           = 40_000
	streamEditCodeBlockTotalLimit      = 40_000
	streamEditCodeBlockSingleLimit     = 12_000
	streamEditConversationLimit        = 12
	streamEditConversationTextLimit    = 4_000
	streamEditExplicitContextLimit     = 20_000
	streamEditRulesLimit               = 12_000
	streamEditLinterLimit              = 8_000
	streamEditMaxOutputTokens          = 8_192
	streamEditGeneratedRequestIDPrefix = "stream-edit-"
)

var errStreamEditToolInvocation = errors.New("stream edit must not invoke tools")

// StreamEdit handles Cursor Ctrl+K inline edit streaming.
func (service *Service) StreamEdit(ctx context.Context, req *connect.Request[aiserverv1.StreamEditRequest], stream *connect.ServerStream[aiserverv1.StreamChatResponse]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := streamEditGeneratedRequestIDPrefix + uuid.NewString()
	recorder, err := newStreamEditLogRecorder(service.streamEditHistoryRoot(), requestID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if req == nil || req.Msg == nil {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("stream edit request is required"))
	}
	if stream == nil {
		return streamEditConnectError(recorder, connect.CodeInternal, fmt.Errorf("stream edit response stream is required"))
	}
	if _, err := recorder.appendEvent("incoming_request", map[string]any{
		"request_id": requestID,
		"session_id": strings.TrimSpace(req.Msg.GetSessionId()),
		"query":      truncateStreamEditText(req.Msg.GetQuery(), 500),
		"fast_mode":  req.Msg.GetFastMode(),
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if service.provider == nil {
		return streamEditConnectError(recorder, connect.CodeInternal, fmt.Errorf("provider gateway is not initialized"))
	}

	messages, promptSummary, err := buildStreamEditPrompt(req.Msg)
	if err != nil {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, err)
	}

	modelCallID := requestID + "-model"
	modelID, modelSource, lastAgentModelHash := service.resolveStreamEditModelID(ctx, req.Msg.GetModelDetails())
	artifactPaths := &modeladapter.LLMArtifactPaths{}
	var streamErr error

	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      streamEditMaxOutputTokens,
		CompileSummary: fmt.Sprintf("stream edit %s model_source=%s last_agent_model_hash=%s", promptSummary, modelSource, lastAgentModelHash),
		Observer:       recorder,
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if event.Text == "" {
				return nil
			}
			if sendErr := stream.Send(&aiserverv1.StreamChatResponse{Text: event.Text}); sendErr != nil {
				streamErr = sendErr
				return sendErr
			}
			return nil
		case modeladapter.ModelEventKindThinkingDelta, modeladapter.ModelEventKindThinkingCompleted, modeladapter.ModelEventKindTurnFinished:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted, modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
			return errStreamEditToolInvocation
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return providerTerminalError{cause: event.Err}
			}
			return providerTerminalError{cause: fmt.Errorf("provider error")}
		default:
			return nil
		}
	})
	if err != nil {
		if streamErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, streamErr)
		}
		if errors.Is(err, errStreamEditToolInvocation) {
			return streamEditConnectError(recorder, connect.CodeInternal, errStreamEditToolInvocation)
		}
		return streamEditConnectError(recorder, connect.CodeUnknown, err)
	}
	if _, err := recorder.appendEvent("final_response", map[string]any{
		"request_id":       requestID,
		"model_call_id":    modelCallID,
		"model_source":     modelSource,
		"model_id":         modelID,
		"artifact_request": artifactPaths.RequestPath,
		"artifact_sse":     artifactPaths.ResponsePath,
		"artifact_summary": artifactPaths.SummaryPath,
	}); err != nil {
		log.Printf("forwarder failed to write stream edit final log request_id=%s error=%v", requestID, err)
	}
	return nil
}

// PreloadEdit is a no-op warm path for Ctrl+K; keep the client happy without provider cost.
func (service *Service) PreloadEdit(context.Context, *connect.Request[aiserverv1.PreloadEditRequest]) (*connect.Response[aiserverv1.PreloadEditResponse], error) {
	return connect.NewResponse(&aiserverv1.PreloadEditResponse{}), nil
}

func buildStreamEditPrompt(req *aiserverv1.StreamEditRequest) ([]modeladapter.Message, string, error) {
	system, err := promptassets.ReadCmdKPrompt()
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(system) == "" {
		return nil, "", fmt.Errorf("cmdk prompt asset is empty")
	}

	query := strings.TrimSpace(req.GetQuery())
	if query == "" {
		return nil, "", fmt.Errorf("query is required")
	}

	sections := make([]string, 0, 12)
	sections = append(sections, "User instruction:\n"+query)

	currentFile := req.GetCurrentFile()
	filePath := ""
	languageID := ""
	if currentFile != nil {
		filePath = strings.TrimSpace(currentFile.GetRelativeWorkspacePath())
		languageID = strings.TrimSpace(currentFile.GetLanguageId())
		selectionText, selectionMeta := extractStreamEditSelection(currentFile)
		if filePath != "" || languageID != "" {
			meta := make([]string, 0, 3)
			if filePath != "" {
				meta = append(meta, "path="+filePath)
			}
			if languageID != "" {
				meta = append(meta, "language="+languageID)
			}
			if selectionMeta != "" {
				meta = append(meta, selectionMeta)
			}
			sections = append(sections, "Current file metadata:\n"+strings.Join(meta, "\n"))
		}
		if selectionText != "" {
			sections = append(sections, "Selected code:\n"+truncateStreamEditText(selectionText, streamEditSelectionLimit))
		} else if currentFile.GetCursorPosition() != nil {
			pos := currentFile.GetCursorPosition()
			sections = append(sections, fmt.Sprintf("Cursor position: line=%d column=%d\nSelection is empty; insert at the cursor.", pos.GetLine(), pos.GetColumn()))
		}
		if content := strings.TrimSpace(currentFile.GetContents()); content != "" {
			sections = append(sections, "Current file contents:\n"+truncateStreamEditText(content, streamEditFileContentLimit))
		}
	}

	if blocks := formatStreamEditCodeBlocks("Referenced code blocks", req.GetCodeBlocks()); blocks != "" {
		sections = append(sections, blocks)
	}
	if blocks := formatStreamEditCodeBlocks("Prompt code blocks", req.GetPromptCodeBlocks()); blocks != "" {
		sections = append(sections, blocks)
	}
	if conversation := formatStreamEditConversation(req.GetConversation()); conversation != "" {
		sections = append(sections, conversation)
	}
	if rules := formatStreamEditRules(req.GetRules()); rules != "" {
		sections = append(sections, rules)
	}
	if lints := formatStreamEditLinterErrors(req.GetLinterErrors()); lints != "" {
		sections = append(sections, lints)
	}
	if contextJSON, err := marshalStreamEditExplicitContext(req.GetExplicitContext()); err != nil {
		return nil, "", err
	} else if contextJSON != "" {
		sections = append(sections, "Explicit context:\n"+contextJSON)
	}
	if workspace := strings.TrimSpace(req.GetWorkspaceRootPath()); workspace != "" {
		sections = append(sections, "Workspace root:\n"+workspace)
	}

	summary := fmt.Sprintf("query_len=%d file=%q selection=%t conversation=%d code_blocks=%d",
		len([]rune(query)),
		filePath,
		currentFile != nil && currentFile.GetSelection() != nil,
		len(req.GetConversation()),
		len(req.GetCodeBlocks())+len(req.GetPromptCodeBlocks()),
	)
	return []modeladapter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, summary, nil
}

func (service *Service) streamEditHistoryRoot() string {
	if service == nil || service.store == nil {
		return ""
	}
	return strings.TrimSpace(service.store.HistoryDir())
}

func (service *Service) resolveStreamEditModelID(ctx context.Context, details *aiserverv1.ModelDetails) (string, string, string) {
	if service == nil || service.resolver == nil {
		return "", "default_fallback", ""
	}
	lastHash := ""
	cmdKHash := ""
	if service.modelMemory != nil {
		lastHash = strings.TrimSpace(service.modelMemory.LastAgentModelHash())
		cmdKHash = strings.TrimSpace(service.modelMemory.CmdKModelHash())
	}

	// Cursor CmdK client currently hardcodes modelDetails.modelName="default"
	// (t7t/c5e). Treat meta aliases (default/auto/fast) as "use local default".
	// Priority:
	// 1) explicit non-meta request model
	// 2) config cmdKModelHash
	// 3) last agent model hash
	// 4) first adapter via meta "default"
	requested := ""
	if details != nil {
		requested = strings.TrimSpace(details.GetModelName())
	}
	if requested != "" && !modelchannel.IsMetaModelAlias(requested) {
		channel, err := service.resolver.SelectChannelForModel(ctx, requested)
		if err == nil && channel != nil && strings.TrimSpace(channel.ID) != "" {
			return strings.TrimSpace(channel.ID), "request_model_details", lastHash
		}
		if err != nil {
			log.Printf("forwarder stream edit ignored request model=%s error=%v", requested, err)
		}
	}
	if cmdKHash != "" {
		channel, err := service.resolver.SelectChannelForModel(ctx, cmdKHash)
		if err == nil && channel != nil && strings.TrimSpace(channel.ID) == cmdKHash {
			return cmdKHash, "config_cmdk_model_hash", lastHash
		}
		if err != nil {
			log.Printf("forwarder stream edit ignored invalid cmdk model hash=%s error=%v", cmdKHash, err)
		}
	}
	if lastHash != "" {
		channel, err := service.resolver.SelectChannelForModel(ctx, lastHash)
		if err == nil && channel != nil && strings.TrimSpace(channel.ID) == lastHash {
			return lastHash, "last_agent_model_hash", lastHash
		}
		if err != nil {
			log.Printf("forwarder stream edit ignored invalid last agent model hash=%s error=%v", lastHash, err)
		}
	}

	// Empty / meta request and no usable configured/last-agent hash: resolver maps default → first adapter.
	channel, err := service.resolver.SelectChannelForModel(ctx, "default")
	if err == nil && channel != nil && strings.TrimSpace(channel.ID) != "" {
		return strings.TrimSpace(channel.ID), "default_channel", lastHash
	}
	return "", "default_fallback", lastHash
}

func streamEditConnectError(recorder *streamEditLogRecorder, code connect.Code, err error) error {
	if err == nil {
		err = fmt.Errorf("stream edit failed")
	}
	if _, logErr := recorder.appendEvent("error", map[string]any{
		"code":  code.String(),
		"error": err.Error(),
	}); logErr != nil {
		log.Printf("forwarder failed to write stream edit error log error=%v original_error=%v", logErr, err)
	}
	return connect.NewError(code, err)
}

func extractStreamEditSelection(file *aiserverv1.CurrentFileInfo) (string, string) {
	if file == nil {
		return "", ""
	}
	selection := file.GetSelection()
	content := file.GetContents()
	if selection == nil {
		return "", ""
	}
	start := selection.GetStartPosition()
	end := selection.GetEndPosition()
	if start == nil || end == nil {
		return "", fmt.Sprintf("selection=present but incomplete start=%v end=%v", start != nil, end != nil)
	}
	meta := fmt.Sprintf("selection=line %d:%d -> %d:%d", start.GetLine(), start.GetColumn(), end.GetLine(), end.GetColumn())
	selected := sliceStreamEditByCursorRange(content, start, end)
	if strings.TrimSpace(selected) == "" {
		// Some clients send 1-based lines; retry once if zero-based slice is empty.
		altStart := &aiserverv1.CursorPosition{Line: maxInt32(start.GetLine()-1, 0), Column: start.GetColumn()}
		altEnd := &aiserverv1.CursorPosition{Line: maxInt32(end.GetLine()-1, 0), Column: end.GetColumn()}
		if alt := sliceStreamEditByCursorRange(content, altStart, altEnd); strings.TrimSpace(alt) != "" {
			return alt, meta + " (normalized_from_1_based)"
		}
		return "", meta
	}
	return selected, meta
}

func sliceStreamEditByCursorRange(content string, start *aiserverv1.CursorPosition, end *aiserverv1.CursorPosition) string {
	if content == "" || start == nil || end == nil {
		return ""
	}
	lines := splitStreamEditLines(content)
	startLine := int(start.GetLine())
	endLine := int(end.GetLine())
	startCol := int(start.GetColumn())
	endCol := int(end.GetColumn())
	if startLine < 0 || endLine < 0 || startLine >= len(lines) || endLine >= len(lines) {
		return ""
	}
	if startLine > endLine || (startLine == endLine && startCol > endCol) {
		startLine, endLine = endLine, startLine
		startCol, endCol = endCol, startCol
	}
	if startLine == endLine {
		line := lines[startLine]
		startCol = clampStreamEditIndex(startCol, len(line))
		endCol = clampStreamEditIndex(endCol, len(line))
		if startCol >= endCol {
			return ""
		}
		return line[startCol:endCol]
	}
	var b strings.Builder
	first := lines[startLine]
	startCol = clampStreamEditIndex(startCol, len(first))
	b.WriteString(first[startCol:])
	b.WriteString("\n")
	for lineIndex := startLine + 1; lineIndex < endLine; lineIndex++ {
		b.WriteString(lines[lineIndex])
		b.WriteString("\n")
	}
	last := lines[endLine]
	endCol = clampStreamEditIndex(endCol, len(last))
	b.WriteString(last[:endCol])
	return b.String()
}

func splitStreamEditLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	return strings.Split(normalized, "\n")
}

func clampStreamEditIndex(value int, length int) int {
	if value < 0 {
		return 0
	}
	if value > length {
		return length
	}
	return value
}

func maxInt32(a, b int32) int32 {
	if a > b {
		return a
	}
	return b
}

func formatStreamEditCodeBlocks(title string, blocks []*aiserverv1.CodeBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	remaining := streamEditCodeBlockTotalLimit
	for index, block := range blocks {
		if block == nil || remaining <= 0 {
			continue
		}
		path := strings.TrimSpace(block.GetRelativeWorkspacePath())
		content := strings.TrimSpace(block.GetContents())
		if content == "" {
			content = strings.TrimSpace(block.GetOverrideContents())
		}
		if content == "" {
			content = strings.TrimSpace(block.GetOriginalContents())
		}
		if content == "" {
			content = strings.TrimSpace(block.GetFileContents())
		}
		if content == "" {
			continue
		}
		limit := streamEditCodeBlockSingleLimit
		if remaining < limit {
			limit = remaining
		}
		truncated := truncateStreamEditText(content, limit)
		if truncated == "" {
			continue
		}
		label := path
		if label == "" {
			label = fmt.Sprintf("block-%d", index+1)
		}
		parts = append(parts, fmt.Sprintf("--- %s ---\n%s", label, truncated))
		remaining -= len([]rune(truncated))
	}
	if len(parts) == 0 {
		return ""
	}
	return title + ":\n" + strings.Join(parts, "\n\n")
}

func formatStreamEditConversation(messages []*aiserverv1.ConversationMessage) string {
	if len(messages) == 0 {
		return ""
	}
	start := 0
	if len(messages) > streamEditConversationLimit {
		start = len(messages) - streamEditConversationLimit
	}
	parts := make([]string, 0, len(messages)-start)
	for _, message := range messages[start:] {
		if message == nil {
			continue
		}
		text := truncateStreamEditText(message.GetText(), streamEditConversationTextLimit)
		if text == "" {
			continue
		}
		role := "user"
		switch message.GetType() {
		case aiserverv1.ConversationMessage_MESSAGE_TYPE_AI:
			role = "assistant"
		case aiserverv1.ConversationMessage_MESSAGE_TYPE_HUMAN:
			role = "user"
		default:
			role = "message"
		}
		parts = append(parts, fmt.Sprintf("%s: %s", role, text))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Recent Ctrl+K conversation:\n" + strings.Join(parts, "\n")
}

func formatStreamEditRules(rules []*aiserverv1.CursorRule) string {
	if len(rules) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rules))
	remaining := streamEditRulesLimit
	for _, rule := range rules {
		if rule == nil || remaining <= 0 {
			continue
		}
		name := strings.TrimSpace(rule.GetName())
		body := strings.TrimSpace(rule.GetBody())
		if body == "" {
			body = strings.TrimSpace(rule.GetDescription())
		}
		if body == "" {
			continue
		}
		chunk := body
		if name != "" {
			chunk = name + ":\n" + body
		}
		truncated := truncateStreamEditText(chunk, remaining)
		if truncated == "" {
			continue
		}
		parts = append(parts, truncated)
		remaining -= len([]rune(truncated))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Rules:\n" + strings.Join(parts, "\n\n")
}

func formatStreamEditLinterErrors(lints *aiserverv1.LinterErrors) string {
	if lints == nil {
		return ""
	}
	errors := lints.GetErrors()
	if len(errors) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errors))
	remaining := streamEditLinterLimit
	for index, item := range errors {
		if item == nil || remaining <= 0 {
			continue
		}
		message := strings.TrimSpace(item.GetMessage())
		if message == "" {
			continue
		}
		line := "?"
		if item.GetRange() != nil && item.GetRange().GetStartPosition() != nil {
			line = fmt.Sprintf("%d", item.GetRange().GetStartPosition().GetLine())
		}
		chunk := fmt.Sprintf("%d. line %s: %s", index+1, line, message)
		truncated := truncateStreamEditText(chunk, remaining)
		if truncated == "" {
			continue
		}
		parts = append(parts, truncated)
		remaining -= len([]rune(truncated))
	}
	if len(parts) == 0 {
		return ""
	}
	path := strings.TrimSpace(lints.GetRelativeWorkspacePath())
	title := "Linter errors"
	if path != "" {
		title += " (" + path + ")"
	}
	return title + ":\n" + strings.Join(parts, "\n")
}

func marshalStreamEditExplicitContext(contextValue *aiserverv1.ExplicitContext) (string, error) {
	if contextValue == nil {
		return "", nil
	}
	// Keep this light: only surface plain text context fields.
	parts := make([]string, 0, 3)
	if text := strings.TrimSpace(contextValue.GetContext()); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(contextValue.GetRepoContext()); text != "" {
		parts = append(parts, "repo_context:\n"+text)
	}
	if text := strings.TrimSpace(contextValue.GetModeSpecificContext()); text != "" {
		parts = append(parts, "mode_specific_context:\n"+text)
	}
	if len(parts) == 0 {
		return "", nil
	}
	return truncateStreamEditText(strings.Join(parts, "\n\n"), streamEditExplicitContextLimit), nil
}

func truncateStreamEditText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || limit <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= limit {
		return trimmed
	}
	return strings.TrimSpace(string(runes[:limit])) + "\n...[truncated]"
}
