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
	promptassets "cursor/prompt"
)

const (
	cmdKServiceStreamCmdKProcedure        = "/aiserver.v1.CmdKService/StreamCmdK"
	cmdKServiceRerankCmdKContextProcedure = "/aiserver.v1.CmdKService/RerankCmdKContext"
	cmdKFileContentLimit                  = 100_000
	cmdKSelectionLimit                    = 40_000
	cmdKQueryHistoryLimit                 = 8
	cmdKQueryHistoryTextLimit             = 2_000
	cmdKRulesLimit                        = 12_000
	cmdKExplicitContextLimit              = 20_000
	cmdKMaxOutputTokens                   = 8_192
	cmdKGeneratedRequestIDPrefix          = "stream-cmdk-"
	cmdKDefaultEditID                     = int32(1)
)

var errCmdKToolInvocation = errors.New("cmdk generation must not invoke tools")

// StreamCmdK handles Cursor Ctrl+K via aiserver.v1.CmdKService/StreamCmdK.
// Client schema expects StreamCmdKResponseContextWrapped, not bare StreamCmdKResponse.
// First-pass requests often send only context_item_hash; server must reply missing_context_items
// so the client retries with full context items.
func (service *Service) StreamCmdK(ctx context.Context, req *connect.Request[aiserverv1.StreamCmdKRequest], stream *connect.ServerStream[aiserverv1.StreamCmdKResponseContextWrapped]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := cmdKGeneratedRequestIDPrefix + uuid.NewString()
	recorder, err := newStreamEditLogRecorder(service.streamEditHistoryRoot(), requestID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if req == nil || req.Msg == nil {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("stream cmdk request is required"))
	}
	if stream == nil {
		return streamEditConnectError(recorder, connect.CodeInternal, fmt.Errorf("stream cmdk response stream is required"))
	}
	if service.provider == nil {
		return streamEditConnectError(recorder, connect.CodeInternal, fmt.Errorf("provider gateway is not initialized"))
	}

	fullCount, hashCount, missingHashes := inspectCmdKContextItems(req.Msg.GetContextItems())
	if _, err := recorder.appendEvent("incoming_request", map[string]any{
		"request_id":       requestID,
		"session_id":       strings.TrimSpace(req.Msg.GetSessionId()),
		"full_item_count":  fullCount,
		"hash_item_count":  hashCount,
		"missing_hash_len": len(missingHashes),
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	// Hash-only / partial-hash first pass: ask client to resend full items, then end this stream.
	if len(missingHashes) > 0 {
		if _, err := recorder.appendEvent("missing_context_items", map[string]any{
			"hashes": missingHashes,
		}); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if sendErr := stream.Send(&aiserverv1.StreamCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamCmdKResponseContextWrapped_MissingContextItems{
				MissingContextItems: &aiserverv1.MissingContextItems{
					MissingContextItemHashes: missingHashes,
				},
			},
		}); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
		return nil
	}

	promptInput := extractCmdKPromptInput(req.Msg)
	if strings.TrimSpace(promptInput.Query) == "" {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("cmdk query is required"))
	}
	if _, err := recorder.appendEvent("prompt_input", map[string]any{
		"request_id": requestID,
		"query":      truncateStreamEditText(promptInput.Query, 500),
		"chat_mode":  promptInput.ChatMode,
		"file_path":  promptInput.FilePath,
		"start_line": promptInput.StartLineNumber,
		"end_excl":   promptInput.EndLineNumberExclusive,
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	messages, promptSummary, err := buildCmdKPrompt(req.Msg, promptInput)
	if err != nil {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, err)
	}

	modelCallID := requestID + "-model"
	var modelDetails *aiserverv1.ModelDetails
	if req.Msg.GetCmdKOptions() != nil {
		modelDetails = req.Msg.GetCmdKOptions().GetModelDetails()
	}
	modelID, modelSource, lastAgentModelHash := service.resolveStreamEditModelID(ctx, modelDetails)
	artifactPaths := &modeladapter.LLMArtifactPaths{}
	var streamErr error
	editStarted := false
	var filePathPtr *string
	if strings.TrimSpace(promptInput.FilePath) != "" {
		path := strings.TrimSpace(promptInput.FilePath)
		filePathPtr = &path
	}

	sendWrapped := func(msg *aiserverv1.StreamCmdKResponseContextWrapped) error {
		return stream.Send(msg)
	}
	sendReal := func(real *aiserverv1.StreamCmdKResponse) error {
		return sendWrapped(&aiserverv1.StreamCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamCmdKResponseContextWrapped_RealResponse{
				RealResponse: real,
			},
		})
	}
	sendStatus := func(messages ...string) error {
		if len(messages) == 0 {
			return nil
		}
		return sendReal(&aiserverv1.StreamCmdKResponse{
			Response: &aiserverv1.StreamCmdKResponse_StatusUpdate_{
				StatusUpdate: &aiserverv1.StreamCmdKResponse_StatusUpdate{
					Messages: messages,
				},
			},
		})
	}
	sendEditStart := func() error {
		if editStarted || promptInput.ChatMode {
			return nil
		}
		editStarted = true
		maxEnd := promptInput.EndLineNumberExclusive
		return sendReal(&aiserverv1.StreamCmdKResponse{
			Response: &aiserverv1.StreamCmdKResponse_EditStart_{
				EditStart: &aiserverv1.StreamCmdKResponse_EditStart{
					StartLineNumber:           promptInput.StartLineNumber,
					EditId:                    cmdKDefaultEditID,
					MaxEndLineNumberExclusive: &maxEnd,
					FilePath:                  filePathPtr,
				},
			},
		})
	}

	// Mark full items as accepted so client can hash them on later retries.
	if statuses := buildCmdKContextStatusUpdate(req.Msg.GetContextItems()); statuses != nil {
		if sendErr := sendWrapped(&aiserverv1.StreamCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamCmdKResponseContextWrapped_ContextStatusUpdate{
				ContextStatusUpdate: statuses,
			},
		}); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
	}

	// Keep client UI alive before first model token (observed idle disconnect ~10s).
	if sendErr := sendStatus("生成中…"); sendErr != nil {
		return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
	}
	if !promptInput.ChatMode {
		if sendErr := sendEditStart(); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
	}

	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      cmdKMaxOutputTokens,
		CompileSummary: fmt.Sprintf("stream cmdk %s model_source=%s last_agent_model_hash=%s", promptSummary, modelSource, lastAgentModelHash),
		Observer:       recorder,
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if event.Text == "" {
				return nil
			}
			if promptInput.ChatMode {
				if sendErr := sendReal(&aiserverv1.StreamCmdKResponse{
					Response: &aiserverv1.StreamCmdKResponse_Chat_{
						Chat: &aiserverv1.StreamCmdKResponse_Chat{Text: event.Text},
					},
				}); sendErr != nil {
					streamErr = sendErr
					return sendErr
				}
				return nil
			}
			if sendErr := sendEditStart(); sendErr != nil {
				streamErr = sendErr
				return sendErr
			}
			if sendErr := sendReal(&aiserverv1.StreamCmdKResponse{
				Response: &aiserverv1.StreamCmdKResponse_EditStream_{
					EditStream: &aiserverv1.StreamCmdKResponse_EditStream{
						Text:     event.Text,
						EditId:   cmdKDefaultEditID,
						FilePath: filePathPtr,
					},
				},
			}); sendErr != nil {
				streamErr = sendErr
				return sendErr
			}
			return nil
		case modeladapter.ModelEventKindThinkingDelta, modeladapter.ModelEventKindThinkingCompleted, modeladapter.ModelEventKindTurnFinished:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted, modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
			return errCmdKToolInvocation
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
		if errors.Is(err, errCmdKToolInvocation) {
			return streamEditConnectError(recorder, connect.CodeInternal, errCmdKToolInvocation)
		}
		return streamEditConnectError(recorder, connect.CodeUnknown, err)
	}

	if !promptInput.ChatMode {
		if sendErr := sendEditStart(); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
		if sendErr := sendReal(&aiserverv1.StreamCmdKResponse{
			Response: &aiserverv1.StreamCmdKResponse_EditEnd_{
				EditEnd: &aiserverv1.StreamCmdKResponse_EditEnd{
					EndLineNumberExclusive: promptInput.EndLineNumberExclusive,
					EditId:                 cmdKDefaultEditID,
					FilePath:               filePathPtr,
				},
			},
		}); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
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
		log.Printf("forwarder failed to write stream cmdk final log request_id=%s error=%v", requestID, err)
	}
	return nil
}

// RerankCmdKContext warms/reranks CmdK context. Mirror the StreamCmdK hash handshake so
// the client expands hashes before generation.
func (service *Service) RerankCmdKContext(_ context.Context, req *connect.Request[aiserverv1.RerankCmdKContextRequest]) (*connect.Response[aiserverv1.RerankCmdKContextResponse], error) {
	didCall := false
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&aiserverv1.RerankCmdKContextResponse{DidCall: &didCall}), nil
	}

	_, _, missingHashes := inspectCmdKContextItems(req.Msg.GetContextItems())
	if len(missingHashes) > 0 {
		return connect.NewResponse(&aiserverv1.RerankCmdKContextResponse{
			DidCall: &didCall,
			Response: &aiserverv1.RerankCmdKContextResponse_MissingContextItems{
				MissingContextItems: &aiserverv1.MissingContextItems{
					MissingContextItemHashes: missingHashes,
				},
			},
		}), nil
	}

	statuses := buildCmdKContextStatusUpdate(req.Msg.GetContextItems())
	if statuses == nil {
		return connect.NewResponse(&aiserverv1.RerankCmdKContextResponse{DidCall: &didCall}), nil
	}
	return connect.NewResponse(&aiserverv1.RerankCmdKContextResponse{
		DidCall: &didCall,
		Response: &aiserverv1.RerankCmdKContextResponse_ContextStatusUpdate{
			ContextStatusUpdate: statuses,
		},
	}), nil
}

type cmdKPromptInput struct {
	Query                  string
	ChatMode               bool
	FilePath               string
	SelectionLines         []string
	StartLineNumber        int32
	EndLineNumberExclusive int32
	ImmediateContext       string
	ImmediateTotalLines    int32
	QueryHistory           []string
}

func inspectCmdKContextItems(items []*aiserverv1.PotentiallyCachedContextItem) (fullCount int, hashCount int, missingHashes []string) {
	if len(items) == 0 {
		return 0, 0, nil
	}
	missingHashes = make([]string, 0, len(items))
	for _, cached := range items {
		if cached == nil {
			continue
		}
		if hash := strings.TrimSpace(cached.GetContextItemHash()); hash != "" {
			hashCount++
			missingHashes = append(missingHashes, hash)
			continue
		}
		if cached.GetContextItem() != nil {
			fullCount++
		}
	}
	return fullCount, hashCount, missingHashes
}

func buildCmdKContextStatusUpdate(items []*aiserverv1.PotentiallyCachedContextItem) *aiserverv1.ContextStatusUpdate {
	if len(items) == 0 {
		return nil
	}
	statuses := make([]*aiserverv1.ContextItemStatus, 0, len(items))
	for _, cached := range items {
		if cached == nil {
			continue
		}
		// Only full items have known content on this request; hashes are handled via missing_context_items.
		if hash := strings.TrimSpace(cached.GetContextItemHash()); hash != "" {
			continue
		}
		item := cached.GetContextItem()
		if item == nil {
			continue
		}
		// Client matches status by hash of the item it already holds. Without a server-side
		// cache we cannot invent hashes; skip status when only full payloads are present.
		// Leaving status unset keeps items expandable on the next request if needed.
		_ = item
	}
	if len(statuses) == 0 {
		return nil
	}
	return &aiserverv1.ContextStatusUpdate{ContextItemStatuses: statuses}
}

func extractCmdKPromptInput(req *aiserverv1.StreamCmdKRequest) cmdKPromptInput {
	input := cmdKPromptInput{
		StartLineNumber:        1,
		EndLineNumberExclusive: 1,
	}
	if req == nil {
		return input
	}
	if options := req.GetCmdKOptions(); options != nil {
		input.ChatMode = options.GetChatMode()
	}

	for _, cached := range req.GetContextItems() {
		if cached == nil {
			continue
		}
		item := cached.GetContextItem()
		if item == nil {
			continue
		}
		if query := item.GetCmdKQuery(); query != nil {
			if text := strings.TrimSpace(query.GetQuery()); text != "" {
				input.Query = text
			}
		}
		if selection := item.GetCmdKSelection(); selection != nil {
			input.SelectionLines = append([]string(nil), selection.GetLines()...)
			if selection.GetStartLineNumber() > 0 {
				input.StartLineNumber = selection.GetStartLineNumber()
			}
		}
		if immediate := item.GetCmdKImmediateContext(); immediate != nil {
			if path := strings.TrimSpace(immediate.GetRelativeWorkspacePath()); path != "" {
				input.FilePath = path
			}
			input.ImmediateTotalLines = immediate.GetTotalNumberOfLinesInFile()
			lines := make([]string, 0, len(immediate.GetLines()))
			for _, line := range immediate.GetLines() {
				if line == nil {
					continue
				}
				lines = append(lines, fmt.Sprintf("%d|%s", line.GetLineNumber(), line.GetLine()))
			}
			if len(lines) > 0 {
				input.ImmediateContext = strings.Join(lines, "\n")
			}
			if input.StartLineNumber <= 0 {
				for _, line := range immediate.GetLines() {
					if line != nil && line.GetLineNumber() > 0 {
						input.StartLineNumber = line.GetLineNumber()
						break
					}
				}
			}
		}
		if history := item.GetCmdKQueryHistory(); history != nil {
			if query := history.GetQuery(); query != nil {
				if text := strings.TrimSpace(query.GetQuery()); text != "" {
					input.QueryHistory = append(input.QueryHistory, text)
				}
			}
		}
	}

	if input.StartLineNumber <= 0 {
		input.StartLineNumber = 1
	}
	if len(input.SelectionLines) == 0 {
		input.EndLineNumberExclusive = input.StartLineNumber
	} else {
		input.EndLineNumberExclusive = input.StartLineNumber + int32(len(input.SelectionLines))
	}
	return input
}

func buildCmdKPrompt(req *aiserverv1.StreamCmdKRequest, input cmdKPromptInput) ([]modeladapter.Message, string, error) {
	system, err := promptassets.ReadCmdKPrompt()
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(system) == "" {
		return nil, "", fmt.Errorf("cmdk prompt asset is empty")
	}

	sections := make([]string, 0, 10)
	if input.ChatMode {
		sections = append(sections, "Mode: chat. Answer the user about the selected code. Do not return a full-file rewrite unless asked.")
	} else {
		sections = append(sections, "Mode: edit. Return only the replacement code for the current selection/cursor.")
	}
	sections = append(sections, "User instruction:\n"+strings.TrimSpace(input.Query))

	if input.FilePath != "" {
		sections = append(sections, "Current file path:\n"+input.FilePath)
	}
	if len(input.SelectionLines) > 0 {
		sections = append(sections, fmt.Sprintf(
			"Selected code (start_line=%d end_line_exclusive=%d):\n%s",
			input.StartLineNumber,
			input.EndLineNumberExclusive,
			truncateStreamEditText(strings.Join(input.SelectionLines, "\n"), cmdKSelectionLimit),
		))
	} else {
		sections = append(sections, fmt.Sprintf(
			"Selection is empty. Insert at line %d.",
			input.StartLineNumber,
		))
	}
	if strings.TrimSpace(input.ImmediateContext) != "" {
		sections = append(sections, "Surrounding file context:\n"+truncateStreamEditText(input.ImmediateContext, cmdKFileContentLimit))
	}
	if len(input.QueryHistory) > 0 {
		history := input.QueryHistory
		if len(history) > cmdKQueryHistoryLimit {
			history = history[len(history)-cmdKQueryHistoryLimit:]
		}
		parts := make([]string, 0, len(history))
		for _, item := range history {
			parts = append(parts, "- "+truncateStreamEditText(item, cmdKQueryHistoryTextLimit))
		}
		sections = append(sections, "Recent CmdK queries:\n"+strings.Join(parts, "\n"))
	}
	if rules := formatStreamEditRules(req.GetRules()); rules != "" {
		sections = append(sections, rules)
	}
	if legacy := req.GetLegacyContext(); legacy != nil {
		if blocks := formatStreamEditCodeBlocks("Prompt code blocks", legacy.GetPromptCodeBlocks()); blocks != "" {
			sections = append(sections, blocks)
		}
		if contextJSON, err := marshalStreamEditExplicitContext(legacy.GetExplicitContext()); err != nil {
			return nil, "", err
		} else if contextJSON != "" {
			sections = append(sections, "Explicit context:\n"+contextJSON)
		}
	}

	summary := fmt.Sprintf(
		"query_len=%d file=%q selection_lines=%d chat_mode=%t",
		len([]rune(input.Query)),
		input.FilePath,
		len(input.SelectionLines),
		input.ChatMode,
	)
	return []modeladapter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, summary, nil
}