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
	cmdKServiceStreamTerminalCmdKProcedure        = "/aiserver.v1.CmdKService/StreamTerminalCmdK"
	cmdKServiceRerankTerminalCmdKContextProcedure = "/aiserver.v1.CmdKService/RerankTerminalCmdKContext"
	terminalCmdKHistoryLimit                      = 40_000
	terminalCmdKQueryHistoryLimit                 = 8
	terminalCmdKQueryHistoryTextLimit             = 2_000
	terminalCmdKChatHistoryLimit                  = 8
	terminalCmdKChatHistoryTextLimit              = 2_000
	terminalCmdKMaxOutputTokens                   = 2_048
	terminalCmdKGeneratedRequestIDPrefix          = "stream-terminal-cmdk-"
)

var errTerminalCmdKToolInvocation = errors.New("terminal cmdk generation must not invoke tools")

// StreamTerminalCmdK handles Cursor Terminal Generate-in-Terminal.
// Client expects StreamTerminalCmdKResponseContextWrapped and accumulates:
// - terminal_command.partial_command → suggestedCommand
// - chat.text → chatResponse
// - status_update.messages[0] → statusUpdate
func (service *Service) StreamTerminalCmdK(ctx context.Context, req *connect.Request[aiserverv1.StreamTerminalCmdKRequest], stream *connect.ServerStream[aiserverv1.StreamTerminalCmdKResponseContextWrapped]) error {
	if service == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := terminalCmdKGeneratedRequestIDPrefix + uuid.NewString()
	recorder, err := newStreamEditLogRecorder(service.streamEditHistoryRoot(), requestID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if req == nil || req.Msg == nil {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("stream terminal cmdk request is required"))
	}
	if stream == nil {
		return streamEditConnectError(recorder, connect.CodeInternal, fmt.Errorf("stream terminal cmdk response stream is required"))
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

	// Hash-only first pass: ask client to resend full context items.
	if len(missingHashes) > 0 {
		if _, err := recorder.appendEvent("missing_context_items", map[string]any{
			"hashes": missingHashes,
		}); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		if sendErr := stream.Send(&aiserverv1.StreamTerminalCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamTerminalCmdKResponseContextWrapped_MissingContextItems{
				MissingContextItems: &aiserverv1.MissingContextItems{
					MissingContextItemHashes: missingHashes,
				},
			},
		}); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
		return nil
	}

	promptInput := extractTerminalCmdKPromptInput(req.Msg)
	if strings.TrimSpace(promptInput.Query) == "" {
		return streamEditConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("terminal cmdk query is required"))
	}
	if _, err := recorder.appendEvent("prompt_input", map[string]any{
		"request_id": requestID,
		"query":      truncateStreamEditText(promptInput.Query, 500),
		"chat_mode":  promptInput.ChatMode,
		"cwd":        promptInput.Cwd,
	}); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}

	messages, promptSummary, err := buildTerminalCmdKPrompt(req.Msg, promptInput)
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

	sendWrapped := func(msg *aiserverv1.StreamTerminalCmdKResponseContextWrapped) error {
		return stream.Send(msg)
	}
	sendReal := func(real *aiserverv1.StreamTerminalCmdKResponse) error {
		return sendWrapped(&aiserverv1.StreamTerminalCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamTerminalCmdKResponseContextWrapped_RealResponse{
				RealResponse: real,
			},
		})
	}
	sendStatus := func(messages ...string) error {
		if len(messages) == 0 {
			return nil
		}
		return sendReal(&aiserverv1.StreamTerminalCmdKResponse{
			Response: &aiserverv1.StreamTerminalCmdKResponse_StatusUpdate_{
				StatusUpdate: &aiserverv1.StreamTerminalCmdKResponse_StatusUpdate{
					Messages: messages,
				},
			},
		})
	}

	if statuses := buildCmdKContextStatusUpdate(req.Msg.GetContextItems()); statuses != nil {
		if sendErr := sendWrapped(&aiserverv1.StreamTerminalCmdKResponseContextWrapped{
			Response: &aiserverv1.StreamTerminalCmdKResponseContextWrapped_ContextStatusUpdate{
				ContextStatusUpdate: statuses,
			},
		}); sendErr != nil {
			return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
		}
	}

	// Keep client UI alive before first model token.
	if sendErr := sendStatus("生成中…"); sendErr != nil {
		return streamEditConnectError(recorder, connect.CodeUnknown, sendErr)
	}

	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      terminalCmdKMaxOutputTokens,
		CompileSummary: fmt.Sprintf("stream terminal cmdk %s model_source=%s last_agent_model_hash=%s", promptSummary, modelSource, lastAgentModelHash),
		Observer:       recorder,
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if event.Text == "" {
				return nil
			}
			if promptInput.ChatMode {
				if sendErr := sendReal(&aiserverv1.StreamTerminalCmdKResponse{
					Response: &aiserverv1.StreamTerminalCmdKResponse_Chat_{
						Chat: &aiserverv1.StreamTerminalCmdKResponse_Chat{Text: event.Text},
					},
				}); sendErr != nil {
					streamErr = sendErr
					return sendErr
				}
				return nil
			}
			if sendErr := sendReal(&aiserverv1.StreamTerminalCmdKResponse{
				Response: &aiserverv1.StreamTerminalCmdKResponse_TerminalCommand_{
					TerminalCommand: &aiserverv1.StreamTerminalCmdKResponse_TerminalCommand{
						PartialCommand: event.Text,
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
			return errTerminalCmdKToolInvocation
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
		if errors.Is(err, errTerminalCmdKToolInvocation) {
			return streamEditConnectError(recorder, connect.CodeInternal, errTerminalCmdKToolInvocation)
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
		log.Printf("forwarder failed to write stream terminal cmdk final log request_id=%s error=%v", requestID, err)
	}
	return nil
}

// RerankTerminalCmdKContext mirrors the StreamTerminalCmdK hash handshake.
func (service *Service) RerankTerminalCmdKContext(_ context.Context, req *connect.Request[aiserverv1.RerankTerminalCmdKContextRequest]) (*connect.Response[aiserverv1.RerankTerminalCmdKContextResponse], error) {
	if req == nil || req.Msg == nil {
		return connect.NewResponse(&aiserverv1.RerankTerminalCmdKContextResponse{}), nil
	}

	_, _, missingHashes := inspectCmdKContextItems(req.Msg.GetContextItems())
	if len(missingHashes) > 0 {
		return connect.NewResponse(&aiserverv1.RerankTerminalCmdKContextResponse{
			Response: &aiserverv1.RerankTerminalCmdKContextResponse_MissingContextItems{
				MissingContextItems: &aiserverv1.MissingContextItems{
					MissingContextItemHashes: missingHashes,
				},
			},
		}), nil
	}

	statuses := buildCmdKContextStatusUpdate(req.Msg.GetContextItems())
	if statuses == nil {
		return connect.NewResponse(&aiserverv1.RerankTerminalCmdKContextResponse{}), nil
	}
	return connect.NewResponse(&aiserverv1.RerankTerminalCmdKContextResponse{
		Response: &aiserverv1.RerankTerminalCmdKContextResponse_ContextStatusUpdate{
			ContextStatusUpdate: statuses,
		},
	}), nil
}

type terminalCmdKPromptInput struct {
	Query        string
	ChatMode     bool
	Cwd          string
	CwdRelative  string
	History      string
	QueryHistory []string
	ChatHistory  []terminalCmdKChatTurn
}

type terminalCmdKChatTurn struct {
	User      string
	Assistant string
}

func extractTerminalCmdKPromptInput(req *aiserverv1.StreamTerminalCmdKRequest) terminalCmdKPromptInput {
	input := terminalCmdKPromptInput{}
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
		if query := item.GetTerminalCmdKQuery(); query != nil {
			if text := strings.TrimSpace(query.GetQuery()); text != "" {
				input.Query = text
			}
		}
		if history := item.GetTerminalHistory(); history != nil {
			if text := strings.TrimSpace(history.GetHistory()); text != "" {
				// Prefer the history marked active for this CmdK turn when present.
				if history.GetActiveForCmdK() || strings.TrimSpace(input.History) == "" {
					input.History = text
				}
			}
			if cwd := strings.TrimSpace(history.GetCwdFull()); cwd != "" {
				if history.GetActiveForCmdK() || strings.TrimSpace(input.Cwd) == "" {
					input.Cwd = cwd
				}
			}
			if rel := strings.TrimSpace(history.GetCwdRelativeWorkspacePath()); rel != "" {
				if history.GetActiveForCmdK() || strings.TrimSpace(input.CwdRelative) == "" {
					input.CwdRelative = rel
				}
			}
		}
		if history := item.GetTerminalCmdKQueryHistory(); history != nil {
			input.QueryHistory = append(input.QueryHistory, flattenTerminalCmdKQueryHistory(history)...)
		}
		if chat := item.GetChatHistory(); chat != nil {
			input.ChatHistory = append(input.ChatHistory, flattenTerminalCmdKChatHistory(chat)...)
		}
	}
	return input
}

func flattenTerminalCmdKQueryHistory(history *aiserverv1.ContextItem_TerminalCmdKQueryHistory) []string {
	if history == nil {
		return nil
	}
	// Nested history is older → walk parent first, then current query.
	out := flattenTerminalCmdKQueryHistory(history.GetQueryHistory())
	if query := history.GetQuery(); query != nil {
		if text := strings.TrimSpace(query.GetQuery()); text != "" {
			out = append(out, text)
		}
	}
	if suggested := strings.TrimSpace(history.GetSuggestedCommand()); suggested != "" {
		out = append(out, "suggested: "+suggested)
	}
	return out
}

func flattenTerminalCmdKChatHistory(history *aiserverv1.ContextItem_ChatHistory) []terminalCmdKChatTurn {
	if history == nil {
		return nil
	}
	out := flattenTerminalCmdKChatHistory(history.GetChatHistory())
	user := strings.TrimSpace(history.GetUserMessage())
	assistant := strings.TrimSpace(history.GetAssistantResponse())
	if user != "" || assistant != "" {
		out = append(out, terminalCmdKChatTurn{User: user, Assistant: assistant})
	}
	return out
}

func buildTerminalCmdKPrompt(req *aiserverv1.StreamTerminalCmdKRequest, input terminalCmdKPromptInput) ([]modeladapter.Message, string, error) {
	system, err := promptassets.ReadTerminalCmdKPrompt()
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(system) == "" {
		return nil, "", fmt.Errorf("terminal cmdk prompt asset is empty")
	}

	sections := make([]string, 0, 10)
	if input.ChatMode {
		sections = append(sections, "Mode: chat. Answer the user about the terminal/command context. Do not invent a command unless asked.")
	} else {
		sections = append(sections, "Mode: command. Return only the shell command to insert into the terminal.")
	}
	sections = append(sections, "User instruction:\n"+strings.TrimSpace(input.Query))

	if input.Cwd != "" {
		sections = append(sections, "Current working directory:\n"+input.Cwd)
	}
	if input.CwdRelative != "" {
		sections = append(sections, "Workspace-relative cwd:\n"+input.CwdRelative)
	}
	if strings.TrimSpace(input.History) != "" {
		sections = append(sections, "Recent terminal output:\n"+truncateStreamEditText(input.History, terminalCmdKHistoryLimit))
	}
	if len(input.QueryHistory) > 0 {
		history := input.QueryHistory
		if len(history) > terminalCmdKQueryHistoryLimit {
			history = history[len(history)-terminalCmdKQueryHistoryLimit:]
		}
		parts := make([]string, 0, len(history))
		for _, item := range history {
			parts = append(parts, "- "+truncateStreamEditText(item, terminalCmdKQueryHistoryTextLimit))
		}
		sections = append(sections, "Recent terminal CmdK queries:\n"+strings.Join(parts, "\n"))
	}
	if len(input.ChatHistory) > 0 {
		history := input.ChatHistory
		if len(history) > terminalCmdKChatHistoryLimit {
			history = history[len(history)-terminalCmdKChatHistoryLimit:]
		}
		parts := make([]string, 0, len(history)*2)
		for _, turn := range history {
			if turn.User != "" {
				parts = append(parts, "user: "+truncateStreamEditText(turn.User, terminalCmdKChatHistoryTextLimit))
			}
			if turn.Assistant != "" {
				parts = append(parts, "assistant: "+truncateStreamEditText(turn.Assistant, terminalCmdKChatHistoryTextLimit))
			}
		}
		if len(parts) > 0 {
			sections = append(sections, "Recent terminal CmdK chat:\n"+strings.Join(parts, "\n"))
		}
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
		"query_len=%d chat_mode=%t history_len=%d cwd=%q",
		len([]rune(input.Query)),
		input.ChatMode,
		len([]rune(input.History)),
		input.Cwd,
	)
	return []modeladapter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, summary, nil
}