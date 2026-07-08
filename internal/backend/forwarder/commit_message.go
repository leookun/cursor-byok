package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptassets "cursor/prompt"
)

const (
	commitMessageDiffTotalLimit           = 120_000
	commitMessageSingleDiffLimit          = 40_000
	commitMessagePreviousCommitLimit      = 12
	commitMessageExplicitContextLimit     = 20_000
	commitMessageMaxOutputTokens          = 512
	commitMessageGeneratedRequestIDPrefix = "commit-message-"
)

var errCommitMessageToolInvocation = errors.New("commit message generation must not invoke tools")

// WriteGitCommitMessage handles Cursor's SCM "Generate Commit Message" action.
func (service *Service) WriteGitCommitMessage(ctx context.Context, req *connect.Request[aiserverv1.WriteGitCommitMessageRequest]) (*connect.Response[aiserverv1.WriteGitCommitMessageResponse], error) {
	if service == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("forwarder service is nil"))
	}
	requestID := commitMessageGeneratedRequestIDPrefix + uuid.NewString()
	recorder, err := newCommitMessageLogRecorder(service.commitMessageHistoryRoot(), requestID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if req == nil || req.Msg == nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("write git commit message request is required"))
	}
	if err := recordCommitMessageIncomingRequest(recorder, requestID, req.Msg); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	diffs := truncateCommitMessageDiffs(req.Msg.GetDiffs(), commitMessageDiffTotalLimit, commitMessageSingleDiffLimit)
	if len(diffs) == 0 {
		return nil, commitMessageConnectError(recorder, connect.CodeInvalidArgument, fmt.Errorf("diffs are required"))
	}
	if service.provider == nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, fmt.Errorf("provider gateway is not initialized"))
	}
	messages, err := buildCommitMessagePrompt(req.Msg, diffs)
	if err != nil {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, err)
	}
	modelCallID := requestID + "-model"
	modelID, modelSource, lastAgentModelHash := service.resolveCommitMessageModelID(ctx)
	accumulated := ""
	artifactPaths := &modeladapter.LLMArtifactPaths{}
	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      requestID,
		RunID:          requestID,
		ModelCallID:    modelCallID,
		ModelID:        modelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		Tools:          nil,
		MaxTokens:      commitMessageMaxOutputTokens,
		CompileSummary: fmt.Sprintf("generate commit message diffs=%d previous_commits=%d model_source=%s last_agent_model_hash=%s", len(diffs), len(req.Msg.GetPreviousCommitMessages()), modelSource, lastAgentModelHash),
		Observer:       recorder,
		ArtifactPaths:  artifactPaths,
	}, func(event modeladapter.ModelEvent) error {
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			accumulated += event.Text
			return nil
		case modeladapter.ModelEventKindThinkingDelta, modeladapter.ModelEventKindThinkingCompleted, modeladapter.ModelEventKindTurnFinished:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted, modeladapter.ModelEventKindPartialToolCall, modeladapter.ModelEventKindToolCallDelta:
			return errCommitMessageToolInvocation
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
		if errors.Is(err, errCommitMessageToolInvocation) {
			return nil, commitMessageConnectError(recorder, connect.CodeInternal, errCommitMessageToolInvocation)
		}
		return nil, commitMessageConnectError(recorder, connect.CodeUnknown, err)
	}
	commitMessage := cleanGeneratedCommitMessage(accumulated)
	if firstCommitMessageLine(commitMessage) == "" {
		return nil, commitMessageConnectError(recorder, connect.CodeInternal, fmt.Errorf("generated commit message is empty"))
	}
	if _, err := recorder.appendEvent("final_response", map[string]any{
		"request_id":       requestID,
		"model_call_id":    modelCallID,
		"model_source":     modelSource,
		"model_id":         modelID,
		"raw_text":         accumulated,
		"commit_message":   commitMessage,
		"artifact_request": artifactPaths.RequestPath,
		"artifact_sse":     artifactPaths.ResponsePath,
		"artifact_summary": artifactPaths.SummaryPath,
	}); err != nil {
		log.Printf("forwarder failed to write commit message final log request_id=%s error=%v", requestID, err)
	}
	return connect.NewResponse(&aiserverv1.WriteGitCommitMessageResponse{
		CommitMessage: commitMessage,
	}), nil
}

func buildCommitMessagePrompt(req *aiserverv1.WriteGitCommitMessageRequest, diffs []string) ([]modeladapter.Message, error) {
	system, err := promptassets.ReadCommitPrompt()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(system) == "" {
		return nil, fmt.Errorf("commit prompt asset is empty")
	}
	sections := []string{"Generate a Git commit message for the following changes."}
	previous := truncateCommitMessagePreviousCommits(req.GetPreviousCommitMessages(), commitMessagePreviousCommitLimit)
	if len(previous) > 0 {
		sections = append(sections, "Recent commit messages:\n"+strings.Join(previous, "\n"))
	}
	if contextJSON, err := marshalCommitMessageExplicitContext(req.GetExplicitContext()); err != nil {
		return nil, err
	} else if contextJSON != "" {
		sections = append(sections, "Explicit context:\n"+contextJSON)
	}
	diffSections := make([]string, 0, len(diffs))
	for index, diff := range diffs {
		diffSections = append(diffSections, fmt.Sprintf("--- Diff %d ---\n%s", index+1, diff))
	}
	sections = append(sections, "Diffs:\n"+strings.Join(diffSections, "\n\n"))
	return []modeladapter.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, nil
}

func (service *Service) commitMessageHistoryRoot() string {
	if service == nil || service.store == nil {
		return ""
	}
	return strings.TrimSpace(service.store.HistoryDir())
}

func (service *Service) resolveCommitMessageModelID(ctx context.Context) (string, string, string) {
	if service == nil || service.modelMemory == nil || service.resolver == nil {
		return "", "default_fallback", ""
	}
	hash := strings.TrimSpace(service.modelMemory.LastAgentModelHash())
	if hash == "" {
		return "", "default_fallback", ""
	}
	channel, err := service.resolver.SelectChannelForModel(ctx, hash)
	if err != nil || channel == nil || strings.TrimSpace(channel.ID) != hash {
		if err != nil {
			log.Printf("forwarder commit message ignored invalid last agent model hash=%s error=%v", hash, err)
		}
		return "", "default_fallback", hash
	}
	return hash, "last_agent_model_hash", hash
}

func recordCommitMessageIncomingRequest(recorder *commitMessageLogRecorder, requestID string, request *aiserverv1.WriteGitCommitMessageRequest) error {
	_, err := recorder.appendEvent("incoming_request", map[string]any{
		"request_id": requestID,
	})
	return err
}

func commitMessageConnectError(recorder *commitMessageLogRecorder, code connect.Code, err error) error {
	if err == nil {
		err = fmt.Errorf("commit message generation failed")
	}
	if _, logErr := recorder.appendEvent("error", map[string]any{
		"code":  code.String(),
		"error": err.Error(),
	}); logErr != nil {
		log.Printf("forwarder failed to write commit message error log error=%v original_error=%v", logErr, err)
	}
	return connect.NewError(code, err)
}

func truncateCommitMessageDiffs(input []string, totalLimit int, singleLimit int) []string {
	result := make([]string, 0, len(input))
	remaining := totalLimit
	for _, raw := range input {
		diff := strings.TrimSpace(raw)
		if diff == "" || remaining <= 0 {
			continue
		}
		limit := singleLimit
		if remaining < limit {
			limit = remaining
		}
		truncated := truncateCommitMessageText(diff, limit)
		if truncated == "" {
			continue
		}
		result = append(result, truncated)
		remaining -= len([]rune(truncated))
	}
	return result
}

func truncateCommitMessagePreviousCommits(input []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	result := make([]string, 0, limit)
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		result = append(result, "- "+value)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func marshalCommitMessageExplicitContext(contextValue *aiserverv1.ExplicitContext) (string, error) {
	if contextValue == nil {
		return "", nil
	}
	payload, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(contextValue)
	if err != nil {
		return "", fmt.Errorf("marshal explicit context failed: %w", err)
	}
	return truncateCommitMessageText(string(payload), commitMessageExplicitContextLimit), nil
}

func truncateCommitMessageText(value string, limit int) string {
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

func cleanGeneratedCommitMessage(value string) string {
	result := strings.TrimSpace(value)
	result = stripCommitMessageCodeFence(result)
	prefixes := []string{
		"commit message:",
		"git commit message:",
		"message:",
	}
	for {
		lower := strings.ToLower(strings.TrimSpace(result))
		matched := ""
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				matched = prefix
				break
			}
		}
		if matched == "" {
			break
		}
		result = strings.TrimSpace(result[len(matched):])
	}
	return strings.TrimSpace(result)
}

func stripCommitMessageCodeFence(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	lines = lines[1:]
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstCommitMessageLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
