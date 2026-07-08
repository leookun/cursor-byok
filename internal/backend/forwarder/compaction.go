package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
	promptassets "cursor/prompt"
)

const (
	compactionAutoReserveTokens      = 10000
	compactionTriggerRemainingTokens = 8192
	compactionPreferredTailTurns     = 4
	compactionMinimumTailTurns       = 1
	compactionReserveFloorTokens     = 8192
	compactionSummaryMaxChars        = 12000
	compactionSummaryOutputMaxTokens = 4096
	compactionTurnSnippetMaxChars    = 900

	autoCompactionPreservedToolResultLimitBytes = 16 * 1024
	autoCompactionFallbackToolResultLimitBytes  = 4 * 1024
)

const (
	compactionRequestSourcePromptAsset = "prompt_asset"
	compactionRequestSourceCurrentTurn = "current_turn_compaction"
	compactionOverflowTerminalCode     = "context_overflow_after_compaction"
	compactionSummaryUserMessage       = "现在上下文已满，触发了压缩对话。请把我们到目前为止的对话历史整理成一个 Markdown 表格返回给我。你的回复会直接作为后续对话的压缩内容，请只保留继续协作必需的事实、约束、决定、文件路径、命令、报错、结果和待办。不要调用工具，不要输出表格外说明。"
)

type compactionPlan struct {
	Trigger                   string
	ContextTokens             int64
	ContextWindowSize         int64
	ContextUsagePercent       float64
	ReserveTokens             int64
	MessageCount              int32
	MessagesToCompact         int32
	CompactTurnCount          int32
	IsFirstCompaction         bool
	ExistingSummary           string
	CompactedTurns            []compactedTurnSummary
	ManualInstruction         string
	RequestSource             string
	CurrentTurnSeq            int64
	CurrentRequestID          string
	CurrentUserText           string
	PreserveCurrentTurnInputs bool
}

type compactedTurnSummary struct {
	UserText string
	Steps    []string
}

type compactionCandidateTurn struct {
	Summary         compactedTurnSummary
	ReplayCount     int32
	EstimatedTokens int64
}

func (service *Service) maybeCompactBeforeProvider(stream *ActiveStream, conversation *ConversationFile, compiled CompiledConversation) (bool, error) {
	if service == nil || stream == nil || conversation == nil {
		return false, nil
	}
	manualInstruction, manual := parseManualCompactionDirective(stream.LatestUserText)
	plan, err := service.buildCompactionPlan(stream, conversation, compiled, manual, manualInstruction)
	if err != nil {
		return false, err
	}
	if plan == nil {
		if manual {
			return true, service.finishManualCompactionNoop(stream)
		}
		return false, nil
	}
	return true, service.beginPendingCompaction(stream, plan)
}

func (service *Service) buildCompactionPlan(stream *ActiveStream, conversation *ConversationFile, compiled CompiledConversation, manual bool, manualInstruction string) (*compactionPlan, error) {
	if stream == nil || conversation == nil {
		return nil, nil
	}
	if manual {
		return service.buildManualCompactionPlan(stream, conversation, compiled, manualInstruction)
	}
	return service.buildAutoCompactionPlan(stream, conversation, compiled)
}

func (service *Service) buildManualCompactionPlan(stream *ActiveStream, conversation *ConversationFile, compiled CompiledConversation, manualInstruction string) (*compactionPlan, error) {
	if stream == nil || conversation == nil {
		return nil, nil
	}
	contextWindowSize := compactionContextWindowSize(conversation)
	if contextWindowSize <= 0 {
		return nil, nil
	}
	contextTokens, err := service.resolveCompactionBaselineTokens(stream.ConversationID, compiled, conversation)
	if err != nil {
		return nil, err
	}
	if contextTokens <= 0 {
		return nil, nil
	}
	usagePercent := 0.0
	if contextWindowSize > 0 {
		usagePercent = float64(contextTokens) / float64(contextWindowSize)
	}
	base := &compactionPlan{
		Trigger:                   "manual",
		ContextTokens:             contextTokens,
		ContextWindowSize:         contextWindowSize,
		ContextUsagePercent:       usagePercent,
		ReserveTokens:             compactionAutoReserveTokens,
		MessageCount:              clampInt64ToInt32(int64(len(compiled.Messages))),
		IsFirstCompaction:         len(compactionSummaryTexts(conversation)) == 0,
		ExistingSummary:           existingConversationSummaryText(conversation),
		ManualInstruction:         strings.TrimSpace(manualInstruction),
		CurrentTurnSeq:            stream.TurnSeq,
		CurrentRequestID:          strings.TrimSpace(stream.RequestID),
		CurrentUserText:           strings.TrimSpace(stream.LatestUserText),
		PreserveCurrentTurnInputs: false,
	}
	return service.buildLegacyCompactionPlan(base, conversation, true, 0)
}

func (service *Service) buildAutoCompactionPlan(stream *ActiveStream, conversation *ConversationFile, compiled CompiledConversation) (*compactionPlan, error) {
	if stream == nil || conversation == nil {
		return nil, nil
	}
	contextWindowSize := compactionContextWindowSize(conversation)
	if contextWindowSize <= 0 {
		return nil, nil
	}
	estimatedCompiledTokens := estimateCompiledPromptTokens(compiled)
	reserveTokens := service.resolveCompactionReserveTokens(stream.ModelID)
	if reserveTokens <= 0 {
		reserveTokens = conversation.AutoCompactionReserveTokens
	}
	if reserveTokens <= 0 {
		reserveTokens = compactionAutoReserveTokens
	}
	budgetTokens := contextWindowSize - reserveTokens
	preflightExceeded := estimatedCompiledTokens > 0 && estimatedCompiledTokens > budgetTokens
	contextTokens := maxPositiveInt64(
		conversation.AutoCompactionPromptTokens,
		estimatedCompiledTokens,
		int64(conversation.TokenDetailsUsedTokens),
	)
	pendingExceeded := conversation.AutoCompactionPending && contextTokens > 0 && contextTokens > budgetTokens
	if !pendingExceeded && !preflightExceeded {
		return nil, nil
	}
	usagePercent := 0.0
	if contextWindowSize > 0 && contextTokens > 0 {
		usagePercent = float64(contextTokens) / float64(contextWindowSize)
	}
	base := &compactionPlan{
		Trigger:                   "auto",
		ContextTokens:             contextTokens,
		ContextWindowSize:         contextWindowSize,
		ContextUsagePercent:       usagePercent,
		ReserveTokens:             reserveTokens,
		MessageCount:              clampInt64ToInt32(int64(len(compiled.Messages))),
		IsFirstCompaction:         len(compactionSummaryTexts(conversation)) == 0,
		ExistingSummary:           existingConversationSummaryText(conversation),
		CurrentTurnSeq:            stream.TurnSeq,
		CurrentRequestID:          strings.TrimSpace(stream.RequestID),
		CurrentUserText:           strings.TrimSpace(stream.LatestUserText),
		PreserveCurrentTurnInputs: true,
	}
	plan, err := service.buildAutoCompactionPlanFromHistory(base, conversation)
	if err != nil {
		return nil, err
	}
	if plan == nil && preflightExceeded {
		return nil, compactionTerminalError{
			code: compactionOverflowTerminalCode,
			message: fmt.Sprintf(
				"compiled prompt exceeds context budget before provider request (estimated=%d budget=%d)",
				estimatedCompiledTokens,
				budgetTokens,
			),
		}
	}
	return plan, nil
}

func (service *Service) resolveCompactionBaselineTokens(conversationID string, compiled CompiledConversation, conversation *ConversationFile) (int64, error) {
	contextTokens := estimateCompiledPromptTokens(compiled)
	if contextTokens > 0 {
		return contextTokens, nil
	}
	if conversation != nil && conversation.TokenDetailsUsedTokens > 0 {
		return int64(conversation.TokenDetailsUsedTokens), nil
	}
	if conversationHasLocalCarryForwardState(conversation) {
		return 0, nil
	}
	if service != nil && strings.TrimSpace(conversationID) != "" {
		promptTokens, ok, err := service.loadLatestSummaryPromptTokens(conversationID)
		if err != nil {
			return 0, err
		}
		if ok && promptTokens > 0 {
			return promptTokens, nil
		}
	}
	return 0, nil
}

func conversationHasLocalCarryForwardState(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	return len(compactionSummaryTexts(conversation)) > 0
}

func (service *Service) buildLegacyCompactionPlan(base *compactionPlan, conversation *ConversationFile, _ bool, _ int64) (*compactionPlan, error) {
	if conversation == nil || base == nil {
		return nil, nil
	}
	candidates := buildContextCompactionCandidates(checkpointProjectionEntries(conversation.Entries), base.CurrentTurnSeq, base.CurrentRequestID)
	if len(candidates) == 0 {
		return nil, nil
	}
	compactedTurns := make([]compactedTurnSummary, 0, len(candidates))
	messagesToCompact := int32(0)
	for _, candidate := range candidates {
		compactedTurns = append(compactedTurns, candidate.Summary)
		messagesToCompact += candidate.ReplayCount
	}
	plan := cloneCompactionPlanBase(base)
	plan.RequestSource = compactionRequestSourcePromptAsset
	plan.MessagesToCompact = messagesToCompact
	plan.CompactTurnCount = clampInt64ToInt32(int64(len(candidates)))
	plan.CompactedTurns = compactedTurns
	return &plan, nil
}

func (service *Service) buildAutoCompactionPlanFromHistory(base *compactionPlan, conversation *ConversationFile) (*compactionPlan, error) {
	if conversation == nil || base == nil {
		return nil, nil
	}
	legacyPlan, err := service.buildLegacyCompactionPlan(base, conversation, false, 0)
	if err != nil {
		return nil, err
	}
	currentCandidate, hasCurrentCandidate := buildCurrentTurnCompactionCandidate(checkpointProjectionEntries(conversation.Entries), base.CurrentTurnSeq, base.CurrentRequestID)
	if !hasCurrentCandidate {
		return legacyPlan, nil
	}
	if legacyPlan == nil {
		plan := cloneCompactionPlanBase(base)
		plan.RequestSource = compactionRequestSourceCurrentTurn
		plan.MessagesToCompact = currentCandidate.ReplayCount
		plan.CompactTurnCount = 1
		plan.CompactedTurns = []compactedTurnSummary{currentCandidate.Summary}
		return &plan, nil
	}
	legacyPlan.RequestSource = compactionRequestSourceCurrentTurn
	legacyPlan.MessagesToCompact += currentCandidate.ReplayCount
	legacyPlan.CompactTurnCount++
	legacyPlan.CompactedTurns = append(legacyPlan.CompactedTurns, currentCandidate.Summary)
	return legacyPlan, nil
}

func (service *Service) beginPendingCompaction(stream *ActiveStream, plan *compactionPlan) error {
	if service == nil || stream == nil || plan == nil {
		return nil
	}
	request := buildPreCompactHookRequest(stream, plan)
	serverMessage, pendingExec, err := service.execBridge.OpenExecuteHook(request, "execute_hook_pre_compact")
	if err != nil {
		return err
	}
	stream.mu.Lock()
	pendingExec.ModelCallID = strings.TrimSpace(stream.CurrentModelCallID)
	pendingExec.ProviderPass = stream.ProviderPassCount
	stream.PendingCompaction = newPendingCompaction(plan)
	stream.PendingProviderAction = providerActionNone
	stream.PendingExecs[pendingExec.ExecID] = pendingExec
	stream.Phase = TurnPhaseCompacting
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if err := service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildSummaryStartedMessage(),
	}); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{Message: serverMessage})
}

func (service *Service) handlePreCompactTerminal(stream *ActiveStream, sourcePass int, hookMessage string) error {
	if service == nil || stream == nil {
		return nil
	}
	_ = sourcePass
	stream.mu.Lock()
	if stream.PendingCompaction == nil {
		stream.mu.Unlock()
		if strings.TrimSpace(hookMessage) != "" {
			if err := service.publishSummaryCompleted(stream, hookMessage); err != nil {
				return err
			}
		}
		return service.requestProviderAction(stream, providerActionResume)
	}
	plan := clonePendingCompaction(stream.PendingCompaction)
	plan.HookMessage = strings.TrimSpace(hookMessage)
	stream.PendingCompaction = plan
	stream.mu.Unlock()
	return service.startPendingCompactionSummary(stream, plan)
}

func (service *Service) startPendingCompactionSummary(stream *ActiveStream, plan *PendingCompaction) error {
	if service == nil || stream == nil || plan == nil {
		return nil
	}
	summaryModelCallID := uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	stream.mu.Lock()
	if stream.Status == StreamStatusCanceled || stream.Status == StreamStatusCompleted || stream.Status == StreamStatusFailed {
		stream.PendingCompaction = nil
		stream.mu.Unlock()
		cancel()
		return nil
	}
	stream.CurrentCompactionToken++
	token := stream.CurrentCompactionToken
	stream.CurrentModelCallID = summaryModelCallID
	stream.ProviderActive = true
	stream.ProviderCancel = cancel
	stream.PendingProviderAction = providerActionNone
	stream.PendingCompaction.SummaryModelCallID = summaryModelCallID
	plan.SummaryModelCallID = summaryModelCallID
	stream.Phase = TurnPhaseCompacting
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newCompactionRequestEntry(plan),
	}); err != nil {
		cancel()
		return err
	}
	go service.runPendingCompaction(stream, token, clonePendingCompaction(plan), summaryModelCallID, ctx)
	return nil
}

func (service *Service) runPendingCompaction(stream *ActiveStream, token uint64, plan *PendingCompaction, modelCallID string, ctx context.Context) {
	if service == nil || stream == nil || plan == nil {
		return
	}
	summaryText, err := service.generateCompactionSummary(ctx, stream, plan, modelCallID)
	if err == nil {
		if trimmed := strings.TrimSpace(summaryText); trimmed != "" {
			summaryText = trimmed
		} else {
			summaryText = buildFallbackCompactionSummary(plan)
		}
	}
	if postErr := service.postStreamCommandWait(stream, streamCommand{
		Kind: streamCommandCompactionEvent,
		Compaction: &streamCompactionEvent{
			Token:       token,
			Plan:        plan,
			SummaryText: strings.TrimSpace(summaryText),
			Err:         err,
		},
	}); postErr != nil && !errors.Is(postErr, errProviderLoopInterrupted) {
		log.Printf("forwarder compaction completion post failed request_id=%s token=%d err=%v", strings.TrimSpace(stream.RequestID), token, postErr)
		_ = service.failStreamIfNonTerminal(stream, "unknown", postErr)
	}
}

func (service *Service) handleCompactionEvent(stream *ActiveStream, payload *streamCompactionEvent) error {
	if service == nil || stream == nil || payload == nil {
		return nil
	}
	stream.mu.Lock()
	if stream.CurrentCompactionToken != payload.Token {
		stream.mu.Unlock()
		return nil
	}
	status := stream.Status
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingProviderAction = providerActionNone
	stream.PendingCompaction = nil
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if errors.Is(payload.Err, errProviderLoopInterrupted) || isTerminalStreamStatus(status) {
		return nil
	}
	if payload.Err != nil {
		if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newCompactionFailedEntry(payload.Plan, payload.Err),
		}); err != nil {
			return err
		}
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", payload.Err)
	}
	if payload.Plan == nil {
		return fmt.Errorf("pending compaction plan is missing")
	}
	if err := service.applyCompactionPlan(stream, stream.ConversationID, payload.Plan, payload.SummaryText); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, stream.CurrentModelCallID); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildSummaryMessage(payload.SummaryText),
	}); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	if err := service.publishSummaryCompleted(stream, firstNonEmpty(payload.Plan.HookMessage, "Conversation context compacted.")); err != nil {
		service.setTurnPhase(stream, TurnPhaseFailed)
		return service.failStream(stream, "unknown", err)
	}
	if payload.Plan.Trigger == "manual" {
		if err := service.completeManualCompactionTurn(stream); err != nil {
			return service.failStream(stream, "unknown", err)
		}
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildTurnEndedMessage(0, 0, 0, 0),
		}); err != nil {
			return service.failStream(stream, "unknown", err)
		}
		if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
			return service.failStream(stream, "unknown", err)
		}
		service.setTurnPhase(stream, TurnPhaseCompleted)
		return nil
	}
	return service.requestProviderAction(stream, providerActionResume)
}

func (service *Service) finishCompactionWithError(stream *ActiveStream, cancel context.CancelFunc, err error) {
	if cancel != nil {
		cancel()
	}
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.ProviderActive = false
	stream.ProviderCancel = nil
	stream.PendingCompaction = nil
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	terminalCode := "unknown"
	var coded interface{ TerminalCode() string }
	if errors.As(err, &coded) && strings.TrimSpace(coded.TerminalCode()) != "" {
		terminalCode = strings.TrimSpace(coded.TerminalCode())
	}
	_ = service.failStream(stream, terminalCode, err)
}

func (service *Service) finishManualCompactionNoop(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildSummaryStartedMessage(),
	}); err != nil {
		return err
	}
	if err := service.publishCheckpoint(stream.RequestID, stream.ConversationID); err != nil {
		return err
	}
	if err := service.publishSummaryCompleted(stream, "Nothing to compact."); err != nil {
		return err
	}
	if err := service.completeManualCompactionTurn(stream); err != nil {
		return err
	}
	if err := service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildTurnEndedMessage(0, 0, 0, 0),
	}); err != nil {
		return err
	}
	return service.broker.Complete(stream.RequestID, "", "")
}

func (service *Service) completeManualCompactionTurn(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	requestID := strings.TrimSpace(stream.RequestID)
	conversationID := strings.TrimSpace(stream.ConversationID)
	turnSeq := stream.TurnSeq
	modelCallID := "turn:" + requestID
	if conversationID == "" {
		return nil
	}
	if _, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newMetadataEntry(turnSeq, requestID, "turn_completed", map[string]any{
			"model_call_id": modelCallID,
			"provider_call": false,
		}),
	}); err != nil {
		return err
	}
	if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
		return err
	}
	service.setTurnPhase(stream, TurnPhaseCompleted)
	return nil
}

func (service *Service) publishSummaryCompleted(stream *ActiveStream, hookMessage string) error {
	if service == nil || stream == nil {
		return nil
	}
	thought := strings.TrimSpace(hookMessage)
	if thought == "" {
		thought = defaultSummaryCompletedThought
	}
	if strings.TrimSpace(stream.ConversationID) != "" {
		if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
			newMetadataEntry(stream.TurnSeq, stream.RequestID, "thought_annotation", map[string]any{
				"kind":    "summary_completed",
				"thought": thought,
			}),
		}); err != nil {
			return err
		}
		if err := service.syncSummaryCarryForward(stream.ConversationID, stream.RequestID, stream.CurrentModelCallID); err != nil {
			return err
		}
	}
	return service.broker.Publish(stream.RequestID, StreamEvent{
		Message: buildSummaryCompletedMessage(stream.RequestID),
	})
}

func (service *Service) applyCompactionPlan(stream *ActiveStream, conversationID string, plan *PendingCompaction, summaryText string) error {
	if service == nil || stream == nil || service.compiler == nil || plan == nil {
		return nil
	}
	candidateConversation, err := service.snapshotCompactionCandidate(stream, conversationID)
	if err != nil {
		return err
	}
	if err := applyCompactionToConversation(candidateConversation, plan, summaryText); err != nil {
		return err
	}
	latestUserText := ""
	if plan.PreserveCurrentTurnInputs {
		latestUserText = plan.CurrentUserText
	}
	recompiled, err := service.compiler.Compile(candidateConversation, stream.Mode, latestUserText, stream.ModelName)
	if err != nil {
		return err
	}
	if validationErr := validateCompactionCandidateBudget(recompiled, plan); validationErr != nil {
		return validationErr
	}
	replacementEntries := append([]HistoryEntry(nil), candidateConversation.Entries...)
	if service.store != nil {
		persisted, err := service.store.ReplaceEntries(conversationID, replacementEntries, func(item *ConversationFile) error {
			if item == nil {
				return nil
			}
			item.TokenDetailsUsedTokens = 0
			clearConversationAutoCompactionState(item)
			return nil
		})
		if err != nil {
			return err
		}
		stream.mu.Lock()
		stream.CheckpointConversation = persisted
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		return nil
	}
	_, err = service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.Entries = nil
		item.NextEntrySeq = 1
		item.NextTurnSeq = 1
		appendEntriesInPlace(item, resetEntrySequences(replacementEntries))
		item.TokenDetailsUsedTokens = 0
		clearConversationAutoCompactionState(item)
		return nil
	})
	return err
}

func (service *Service) snapshotCompactionCandidate(stream *ActiveStream, conversationID string) (*ConversationFile, error) {
	if service == nil {
		return nil, nil
	}
	candidate, _, _, err := service.snapshotCheckpointConversation(stream)
	if err == nil && candidate != nil {
		return candidate, nil
	}
	if service.recorder != nil {
		latestState, ok, loadErr := service.loadLatestSummaryState(conversationID)
		if loadErr != nil {
			return nil, loadErr
		}
		if ok && latestState != nil && latestState.RuntimeSnapshot != nil {
			return cloneConversationFile(latestState.RuntimeSnapshot), nil
		}
	}
	if err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("conversation %q not found for compaction", strings.TrimSpace(conversationID))
}

func applyCompactionToConversation(conversation *ConversationFile, plan *PendingCompaction, summaryText string) error {
	if conversation == nil || plan == nil {
		return nil
	}
	replacementEntries, err := buildCompactedContextEntries(conversation, plan, summaryText)
	if err != nil {
		return err
	}
	conversation.Entries = nil
	conversation.NextEntrySeq = 1
	conversation.NextTurnSeq = 1
	appendEntriesInPlace(conversation, resetEntrySequences(replacementEntries))
	conversation.TokenDetailsUsedTokens = 0
	clearConversationAutoCompactionState(conversation)
	if conversation.TokenDetailsMaxTokens == 0 {
		conversation.TokenDetailsMaxTokens = projectedConversationMaxTokens
	}
	return nil
}

func buildCompactedContextEntries(conversation *ConversationFile, plan *PendingCompaction, summaryText string) ([]HistoryEntry, error) {
	if plan == nil {
		return nil, nil
	}
	entries := []HistoryEntry{newCompactionSummaryEntry(plan, summaryText)}
	runtimeEntry, ok, err := newCompactedRuntimeStateEntry(conversation, plan)
	if err != nil {
		return nil, err
	}
	if ok {
		entries = append(entries, runtimeEntry)
	}
	if conversation == nil || !plan.PreserveCurrentTurnInputs {
		return entries, nil
	}
	entries = append(entries, buildAutoCompactionPreservedCurrentTurnEntries(conversation.Entries, plan)...)
	return entries, nil
}

func buildAutoCompactionPreservedCurrentTurnEntries(entries []HistoryEntry, plan *PendingCompaction) []HistoryEntry {
	if len(entries) == 0 || plan == nil || !plan.PreserveCurrentTurnInputs {
		return nil
	}
	latestToolCallID := latestCompletedToolCallIDForTurn(entries, plan.CurrentTurnSeq, plan.CurrentRequestID)
	preservedIndexes := autoCompactionPreservedEntryIndexes(entries, plan.CurrentTurnSeq, plan.CurrentRequestID, latestToolCallID)
	if len(preservedIndexes) == 0 {
		return nil
	}
	preserved := make([]HistoryEntry, 0, len(preservedIndexes))
	for index, entry := range entries {
		if _, ok := preservedIndexes[index]; !ok {
			continue
		}
		switch strings.TrimSpace(entry.Kind) {
		case "compaction_summary", "compacted_summary", "compaction_request":
			continue
		case "tool_result":
			if rewritten, ok := rewriteAutoCompactionToolResultEntry(entry, autoCompactionPreservedToolResultLimitBytes, false); ok {
				entry = rewritten
			}
		}
		preserved = append(preserved, entry)
	}
	return preserved
}

func newCompactionSummaryEntry(plan *PendingCompaction, summaryText string) HistoryEntry {
	payload, _ := json.Marshal(compactionSummaryEntryPayload{
		Summary:                   strings.TrimSpace(summaryText),
		Trigger:                   strings.TrimSpace(plan.Trigger),
		CurrentTurnSeq:            plan.CurrentTurnSeq,
		CurrentRequestID:          strings.TrimSpace(plan.CurrentRequestID),
		CompactTurnCount:          plan.CompactTurnCount,
		MessagesToCompact:         plan.MessagesToCompact,
		PreserveCurrentTurnInputs: plan.PreserveCurrentTurnInputs,
	})
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "compacted_summary",
		Payload:   payload,
	}
}

func newCompactedRuntimeStateEntry(conversation *ConversationFile, plan *PendingCompaction) (HistoryEntry, bool, error) {
	state, err := projectConversationStructuredState(conversation)
	if err != nil {
		return HistoryEntry{}, false, err
	}
	payload := runtimeStateEntryPayload{
		PlanText: state.PlanText,
		Plans:    clonePlanRegistryEntries(state.Plans),
		Todos:    cloneTodoItems(state.Todos),
	}
	if strings.TrimSpace(payload.PlanText) == "" && len(payload.Plans) == 0 && len(payload.Todos) == 0 {
		return HistoryEntry{}, false, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return HistoryEntry{}, false, fmt.Errorf("encode compacted runtime state: %w", err)
	}
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "runtime_state",
		Payload:   encoded,
	}, true, nil
}

func newCompactionRequestEntry(plan *PendingCompaction) HistoryEntry {
	payload, _ := json.Marshal(map[string]any{
		"trigger":               strings.TrimSpace(plan.Trigger),
		"context_tokens":        plan.ContextTokens,
		"context_window_size":   plan.ContextWindowSize,
		"reserve_tokens":        plan.ReserveTokens,
		"messages_to_compact":   plan.MessagesToCompact,
		"compact_turn_count":    plan.CompactTurnCount,
		"request_source":        strings.TrimSpace(plan.RequestSource),
		"summary_model_call_id": strings.TrimSpace(plan.SummaryModelCallID),
	})
	return HistoryEntry{
		TurnSeq:   plan.CurrentTurnSeq,
		RequestID: strings.TrimSpace(plan.CurrentRequestID),
		Role:      "system",
		Kind:      "compaction_request",
		Payload:   payload,
	}
}

func newCompactionFailedEntry(plan *PendingCompaction, cause error) HistoryEntry {
	payload := map[string]any{
		"error": "compaction failed",
	}
	entry := HistoryEntry{
		Role: "system",
		Kind: "compaction_failed",
	}
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		payload["error"] = strings.TrimSpace(cause.Error())
	}
	if plan != nil {
		payload["trigger"] = strings.TrimSpace(plan.Trigger)
		payload["request_source"] = strings.TrimSpace(plan.RequestSource)
		payload["summary_model_call_id"] = strings.TrimSpace(plan.SummaryModelCallID)
		entry.TurnSeq = plan.CurrentTurnSeq
		entry.RequestID = strings.TrimSpace(plan.CurrentRequestID)
	}
	entry.Payload, _ = json.Marshal(payload)
	return entry
}

func buildFallbackCompactionSummary(plan *PendingCompaction) string {
	sections := []string{
		"Conversation summary",
		"Earlier context was compacted into this summary. Preserve the facts, decisions, tool results, and user intent below when continuing the conversation.",
	}
	if plan == nil {
		return strings.Join(sections, "\n\n")
	}
	if strings.TrimSpace(plan.ExistingSummary) != "" {
		sections = append(sections, "Previous summary:\n"+truncateCompactionText(plan.ExistingSummary, compactionSummaryMaxChars/4))
	}
	lines := make([]string, 0, len(plan.CompactedTurns))
	for index, item := range plan.CompactedTurns {
		parts := make([]string, 0, len(item.Steps)+1)
		if strings.TrimSpace(item.UserText) != "" {
			parts = append(parts, "user="+truncateCompactionText(item.UserText, 400))
		}
		for _, step := range item.Steps {
			if strings.TrimSpace(step) == "" {
				continue
			}
			parts = append(parts, truncateCompactionText(step, 400))
		}
		if len(parts) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, strings.Join(parts, " | ")))
	}
	if len(lines) > 0 {
		sections = append(sections, "Compacted turns:\n"+truncateCompactionText(strings.Join(lines, "\n"), compactionSummaryMaxChars/2))
	}
	if strings.TrimSpace(plan.HookMessage) != "" {
		sections = append(sections, "Compaction note:\n"+truncateCompactionText(plan.HookMessage, 800))
	}
	if strings.TrimSpace(plan.ManualInstruction) != "" {
		sections = append(sections, "Manual compact instruction:\n"+truncateCompactionText(plan.ManualInstruction, 800))
	}
	return strings.TrimSpace(truncateCompactionText(strings.Join(sections, "\n\n"), compactionSummaryMaxChars))
}

func validateCompactionCandidateBudget(compiled CompiledConversation, plan *PendingCompaction) error {
	if plan == nil {
		return nil
	}
	budgetTokens := plan.ContextWindowSize - plan.ReserveTokens
	estimatedTokens := estimateCompiledPromptTokens(compiled)
	if budgetTokens > 0 && estimatedTokens <= budgetTokens {
		return nil
	}
	message := fmt.Sprintf(
		"compaction result still exceeds context budget after rebuilding the summary (estimated=%d budget=%d)",
		estimatedTokens,
		budgetTokens,
	)
	if plan.PreserveCurrentTurnInputs {
		message = fmt.Sprintf(
			"compaction result still exceeds context budget after preserving the current user input (estimated=%d budget=%d)",
			estimatedTokens,
			budgetTokens,
		)
	}
	return compactionTerminalError{
		code:    compactionOverflowTerminalCode,
		message: message,
	}
}

func compactionContextWindowSize(conversation *ConversationFile) int64 {
	if conversation == nil {
		return projectedConversationMaxTokens
	}
	if conversation.TokenDetailsMaxTokens > 0 {
		return int64(conversation.TokenDetailsMaxTokens)
	}
	return projectedConversationMaxTokens
}

func (service *Service) resolveCompactionReserveTokens(modelID string) int64 {
	_ = service
	_ = modelID
	return compactionAutoReserveTokens
}

func parseManualCompactionDirective(latestUserText string) (string, bool) {
	trimmed := strings.TrimSpace(latestUserText)
	switch {
	case trimmed == "/compact":
		return "", true
	case strings.HasPrefix(trimmed, "/compact "):
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact")), true
	default:
		return "", false
	}
}

func buildPreCompactHookRequest(stream *ActiveStream, plan *compactionPlan) *agentv1.ExecuteHookRequest {
	if stream == nil || plan == nil {
		return nil
	}
	query := &agentv1.PreCompactRequestQuery{
		Trigger:             strings.TrimSpace(plan.Trigger),
		ContextUsagePercent: plan.ContextUsagePercent,
		ContextTokens:       plan.ContextTokens,
		ContextWindowSize:   plan.ContextWindowSize,
		MessageCount:        plan.MessageCount,
		MessagesToCompact:   plan.MessagesToCompact,
		IsFirstCompaction:   plan.IsFirstCompaction,
		ConversationId:      &stream.ConversationID,
		Model:               &stream.ModelID,
	}
	if generationID := strings.TrimSpace(stream.CurrentModelCallID); generationID != "" {
		query.GenerationId = &generationID
	}
	return &agentv1.ExecuteHookRequest{
		Request: &agentv1.ExecuteHookRequest_PreCompact{
			PreCompact: query,
		},
	}
}

func newPendingCompaction(plan *compactionPlan) *PendingCompaction {
	if plan == nil {
		return nil
	}
	clonedTurns := append([]compactedTurnSummary(nil), plan.CompactedTurns...)
	return &PendingCompaction{
		Trigger:                   plan.Trigger,
		ContextTokens:             plan.ContextTokens,
		ContextWindowSize:         plan.ContextWindowSize,
		ContextUsagePercent:       plan.ContextUsagePercent,
		ReserveTokens:             plan.ReserveTokens,
		MessageCount:              plan.MessageCount,
		MessagesToCompact:         plan.MessagesToCompact,
		CompactTurnCount:          plan.CompactTurnCount,
		IsFirstCompaction:         plan.IsFirstCompaction,
		ExistingSummary:           plan.ExistingSummary,
		CompactedTurns:            clonedTurns,
		ManualInstruction:         plan.ManualInstruction,
		RequestSource:             plan.RequestSource,
		CurrentTurnSeq:            plan.CurrentTurnSeq,
		CurrentRequestID:          plan.CurrentRequestID,
		CurrentUserText:           plan.CurrentUserText,
		PreserveCurrentTurnInputs: plan.PreserveCurrentTurnInputs,
	}
}

func clonePendingCompaction(plan *PendingCompaction) *PendingCompaction {
	if plan == nil {
		return nil
	}
	clonedTurns := append([]compactedTurnSummary(nil), plan.CompactedTurns...)
	return &PendingCompaction{
		Trigger:                   plan.Trigger,
		ContextTokens:             plan.ContextTokens,
		ContextWindowSize:         plan.ContextWindowSize,
		ContextUsagePercent:       plan.ContextUsagePercent,
		ReserveTokens:             plan.ReserveTokens,
		MessageCount:              plan.MessageCount,
		MessagesToCompact:         plan.MessagesToCompact,
		CompactTurnCount:          plan.CompactTurnCount,
		IsFirstCompaction:         plan.IsFirstCompaction,
		ExistingSummary:           plan.ExistingSummary,
		CompactedTurns:            clonedTurns,
		ManualInstruction:         plan.ManualInstruction,
		RequestSource:             plan.RequestSource,
		CurrentTurnSeq:            plan.CurrentTurnSeq,
		CurrentRequestID:          plan.CurrentRequestID,
		CurrentUserText:           plan.CurrentUserText,
		PreserveCurrentTurnInputs: plan.PreserveCurrentTurnInputs,
		HookMessage:               plan.HookMessage,
		SummaryModelCallID:        plan.SummaryModelCallID,
		StartedAt:                 plan.StartedAt,
	}
}

func cloneCompactionPlanBase(base *compactionPlan) compactionPlan {
	if base == nil {
		return compactionPlan{}
	}
	return compactionPlan{
		Trigger:                   base.Trigger,
		ContextTokens:             base.ContextTokens,
		ContextWindowSize:         base.ContextWindowSize,
		ContextUsagePercent:       base.ContextUsagePercent,
		ReserveTokens:             base.ReserveTokens,
		MessageCount:              base.MessageCount,
		MessagesToCompact:         base.MessagesToCompact,
		CompactTurnCount:          base.CompactTurnCount,
		IsFirstCompaction:         base.IsFirstCompaction,
		ExistingSummary:           base.ExistingSummary,
		CompactedTurns:            append([]compactedTurnSummary(nil), base.CompactedTurns...),
		ManualInstruction:         base.ManualInstruction,
		RequestSource:             base.RequestSource,
		CurrentTurnSeq:            base.CurrentTurnSeq,
		CurrentRequestID:          base.CurrentRequestID,
		CurrentUserText:           base.CurrentUserText,
		PreserveCurrentTurnInputs: base.PreserveCurrentTurnInputs,
	}
}

func selectTurnsForCompaction(candidates []compactionCandidateTurn, manual bool, reclaimTokens int64) (int, int32, []compactedTurnSummary) {
	if len(candidates) <= compactionMinimumTailTurns {
		return 0, 0, nil
	}
	preferredCompactCount := len(candidates) - compactionPreferredTailTurns
	if preferredCompactCount < 0 {
		preferredCompactCount = 0
	}
	maxCompactCount := preferredCompactCount
	if maxCompactCount == 0 {
		maxCompactCount = len(candidates) - compactionMinimumTailTurns
	}
	if maxCompactCount <= 0 {
		return 0, 0, nil
	}

	selectedCount := 0
	messageCount := int32(0)
	summaries := make([]compactedTurnSummary, 0, maxCompactCount)
	if manual {
		for index := 0; index < maxCompactCount; index++ {
			selectedCount++
			messageCount += candidates[index].ReplayCount
			summaries = append(summaries, candidates[index].Summary)
		}
		return selectedCount, messageCount, summaries
	}

	requiredTokens := reclaimTokens
	if requiredTokens <= 0 {
		requiredTokens = 1
	}
	reclaimedTokens := int64(0)
	for index := 0; index < maxCompactCount; index++ {
		selectedCount++
		messageCount += candidates[index].ReplayCount
		reclaimedTokens += candidates[index].EstimatedTokens
		summaries = append(summaries, candidates[index].Summary)
		if reclaimedTokens >= requiredTokens {
			return selectedCount, messageCount, summaries
		}
	}
	for index := maxCompactCount; index < len(candidates)-compactionMinimumTailTurns; index++ {
		selectedCount++
		messageCount += candidates[index].ReplayCount
		reclaimedTokens += candidates[index].EstimatedTokens
		summaries = append(summaries, candidates[index].Summary)
		if reclaimedTokens >= requiredTokens {
			return selectedCount, messageCount, summaries
		}
	}
	if selectedCount <= 0 {
		return 0, 0, nil
	}
	return selectedCount, messageCount, summaries
}

func buildCompactionCandidates(rawTurns [][]byte) ([]compactionCandidateTurn, error) {
	candidates := make([]compactionCandidateTurn, 0, len(rawTurns))
	for _, rawTurn := range rawTurns {
		if len(rawTurn) == 0 {
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			return nil, fmt.Errorf("decode compacted turn candidate: %w", err)
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			continue
		}
		replayMessages := buildReplayMessagesFromAgentTurn(agentTurn)
		candidates = append(candidates, compactionCandidateTurn{
			Summary:         buildCompactedTurnSummary(agentTurn),
			ReplayCount:     clampInt64ToInt32(int64(len(replayMessages))),
			EstimatedTokens: estimatePromptReplayMessagesTokens(replayMessages),
		})
	}
	return candidates, nil
}

func buildCurrentTurnCompactionCandidate(entries []HistoryEntry, turnSeq int64, requestID string) (compactionCandidateTurn, bool) {
	if len(entries) == 0 || turnSeq <= 0 || strings.TrimSpace(requestID) == "" {
		return compactionCandidateTurn{}, false
	}
	normalizedRequestID := strings.TrimSpace(requestID)
	latestToolCallID := latestCompletedToolCallIDForTurn(entries, turnSeq, normalizedRequestID)
	if latestToolCallID == "" {
		return compactionCandidateTurn{}, false
	}
	preservedEntryIndexes := autoCompactionPreservedEntryIndexes(entries, turnSeq, normalizedRequestID, latestToolCallID)
	summary := compactedTurnSummary{}
	replayCount := int32(0)
	estimatedTokens := int64(0)
	removedToolHistory := false
	for index, entry := range entries {
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != normalizedRequestID {
			continue
		}
		if strings.TrimSpace(entry.Kind) == "user_message" && strings.TrimSpace(summary.UserText) == "" {
			summary.UserText = currentTurnUserText(entry)
		}
		if _, ok := preservedEntryIndexes[index]; ok {
			continue
		}
		switch strings.TrimSpace(entry.Kind) {
		case "assistant_text":
			if step := summarizeCurrentTurnAssistantEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		case "tool_call":
			removedToolHistory = true
			if step := summarizeCurrentTurnToolCallEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		case "tool_result":
			removedToolHistory = true
			if step := summarizeCurrentTurnToolResultEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		}
	}
	if !removedToolHistory || replayCount <= 0 {
		return compactionCandidateTurn{}, false
	}
	if len(summary.Steps) == 0 {
		summary.Steps = append(summary.Steps, "tool_history=earlier current-turn tool history compacted")
	}
	return compactionCandidateTurn{
		Summary:         summary,
		ReplayCount:     replayCount,
		EstimatedTokens: estimatedTokens,
	}, true
}

func buildContextCompactionCandidates(entries []HistoryEntry, currentTurnSeq int64, currentRequestID string) []compactionCandidateTurn {
	if len(entries) == 0 {
		return nil
	}
	turnOrder := make([]int64, 0)
	grouped := make(map[int64][]HistoryEntry)
	for _, entry := range entries {
		if entry.TurnSeq <= 0 || isCompactionSummaryKind(entry.Kind) {
			continue
		}
		if entry.TurnSeq == currentTurnSeq && strings.TrimSpace(entry.RequestID) == strings.TrimSpace(currentRequestID) {
			continue
		}
		if _, ok := grouped[entry.TurnSeq]; !ok {
			turnOrder = append(turnOrder, entry.TurnSeq)
		}
		grouped[entry.TurnSeq] = append(grouped[entry.TurnSeq], entry)
	}
	candidates := make([]compactionCandidateTurn, 0, len(turnOrder))
	for _, turnSeq := range turnOrder {
		if candidate, ok := buildContextTurnCompactionCandidate(grouped[turnSeq]); ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func buildContextTurnCompactionCandidate(entries []HistoryEntry) (compactionCandidateTurn, bool) {
	if len(entries) == 0 {
		return compactionCandidateTurn{}, false
	}
	summary := compactedTurnSummary{}
	replayCount := int32(0)
	estimatedTokens := int64(0)
	for _, entry := range entries {
		switch strings.TrimSpace(entry.Kind) {
		case "user_message":
			if strings.TrimSpace(summary.UserText) == "" {
				summary.UserText = currentTurnUserText(entry)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		case "assistant_text":
			if step := summarizeCurrentTurnAssistantEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		case "tool_call":
			if step := summarizeCurrentTurnToolCallEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		case "tool_result":
			if step := summarizeCurrentTurnToolResultEntry(entry); step != "" {
				summary.Steps = append(summary.Steps, step)
			}
			replayCount++
			estimatedTokens += estimateTextTokens(string(entry.Payload))
		}
	}
	if replayCount <= 0 || (strings.TrimSpace(summary.UserText) == "" && len(summary.Steps) == 0) {
		return compactionCandidateTurn{}, false
	}
	return compactionCandidateTurn{
		Summary:         summary,
		ReplayCount:     replayCount,
		EstimatedTokens: estimatedTokens,
	}, true
}

func countCompactableContextTurns(entries []HistoryEntry, currentTurnSeq int64, currentRequestID string) int32 {
	return clampInt64ToInt32(int64(len(buildContextCompactionCandidates(entries, currentTurnSeq, currentRequestID))))
}

func currentTurnUserText(entry HistoryEntry) string {
	userMessage := &agentv1.UserMessage{}
	if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
		return ""
	}
	return truncateCompactionText(userMessage.GetText(), compactionTurnSnippetMaxChars/3)
}

func summarizeCurrentTurnAssistantEntry(entry HistoryEntry) string {
	var payload assistantTextPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	if text := truncateCompactionText(payload.Text, compactionTurnSnippetMaxChars/3); text != "" {
		return "assistant=" + text
	}
	if text := truncateCompactionText(payload.ReasoningContent, compactionTurnSnippetMaxChars/4); text != "" {
		return "thinking=" + text
	}
	return ""
}

func summarizeCurrentTurnToolCallEntry(entry HistoryEntry) string {
	var payload toolCallEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	toolName := strings.TrimSpace(payload.ToolName)
	if toolName == "" {
		toolName = "tool_call"
	}
	return toolName + "=called"
}

func summarizeCurrentTurnToolResultEntry(entry HistoryEntry) string {
	var payload toolResultEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return ""
	}
	if len(payload.ToolCall) > 0 {
		toolCall := &agentv1.ToolCall{}
		if err := protojson.Unmarshal(payload.ToolCall, toolCall); err == nil {
			if toolName, detail := summarizeCompactedToolCall(toolCall); toolName != "" {
				return toolName + "=" + detail
			}
		}
	}
	toolName := strings.TrimSpace(payload.ToolName)
	if toolName == "" {
		toolName = "tool_result"
	}
	if result := truncateCompactionText(payload.ResultText, compactionTurnSnippetMaxChars/3); result != "" {
		return toolName + "=" + result
	}
	return toolName + "=completed"
}

func buildReplayMessagesFromAgentTurn(agentTurn *agentv1.AgentConversationTurnStructure) []promptengine.Message {
	if agentTurn == nil {
		return nil
	}
	messages := make([]promptengine.Message, 0, 4)
	if rawUser := agentTurn.GetUserMessage(); len(rawUser) > 0 {
		userMessage := &agentv1.UserMessage{}
		if err := proto.Unmarshal(rawUser, userMessage); err == nil {
			if replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage); ok {
				messages = append(messages, replay)
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
		messages = append(messages, promptengine.BuildLegacyMessagesFromConversationStep(step)...)
	}
	return messages
}

func estimatePromptReplayMessagesTokens(messages []promptengine.Message) int64 {
	total := int64(0)
	for _, message := range messages {
		total += estimateTextTokens(message.Role)
		total += estimateTextTokens(message.Content)
		total += estimatePromptContentPartsTokens(message.Content, message.ContentParts)
		total += estimateTextTokens(message.ReasoningContent)
		total += estimateTextTokens(message.ReasoningSignature)
		total += estimateTextTokens(message.ToolCallID)
		total += estimateTextTokens(message.Name)
	}
	return total
}

func buildCompactedTurnSummary(agentTurn *agentv1.AgentConversationTurnStructure) compactedTurnSummary {
	if agentTurn == nil {
		return compactedTurnSummary{}
	}
	item := compactedTurnSummary{}
	if rawUser := agentTurn.GetUserMessage(); len(rawUser) > 0 {
		userMessage := &agentv1.UserMessage{}
		if err := proto.Unmarshal(rawUser, userMessage); err == nil {
			item.UserText = truncateCompactionText(userMessage.GetText(), compactionTurnSnippetMaxChars/3)
			if imageCount := len(userMessage.GetSelectedContext().GetSelectedImages()); imageCount > 0 {
				suffix := fmt.Sprintf(" [attached_images=%d]", imageCount)
				if strings.TrimSpace(item.UserText) == "" {
					item.UserText = strings.TrimSpace(suffix)
				} else {
					item.UserText += suffix
				}
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
		switch message := step.GetMessage().(type) {
		case *agentv1.ConversationStep_AssistantMessage:
			if text := truncateCompactionText(message.AssistantMessage.GetText(), compactionTurnSnippetMaxChars/3); text != "" {
				item.Steps = append(item.Steps, "assistant="+text)
			}
		case *agentv1.ConversationStep_ThinkingMessage:
			if text := truncateCompactionText(message.ThinkingMessage.GetText(), compactionTurnSnippetMaxChars/4); text != "" {
				item.Steps = append(item.Steps, "thinking="+text)
			}
		case *agentv1.ConversationStep_ToolCall:
			if toolName, toolDetail := summarizeCompactedToolCall(message.ToolCall); toolName != "" {
				item.Steps = append(item.Steps, toolName+"="+toolDetail)
			}
		}
	}
	return item
}

func rewriteAutoCompactionToolResultEntry(entry HistoryEntry, limitBytes int, minimal bool) (HistoryEntry, bool) {
	if strings.TrimSpace(entry.Kind) != "tool_result" {
		return entry, false
	}
	var payload toolResultEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return entry, false
	}
	toolName := firstNonEmpty(strings.TrimSpace(payload.ToolName), "tool")
	resultText := strings.TrimSpace(payload.ResultText)
	if compacted, ok := compactProjectedEditToolResultReplay(toolName, resultText); ok {
		resultText = compacted
	}
	if minimal {
		resultText = autoCompactionOmittedToolResultText(toolName)
		payload.ToolCall = nil
	} else {
		if resultText == "" {
			resultText = autoCompactionOmittedToolResultText(toolName)
		} else {
			resultText = truncateProjectedReplayText(toolName, resultText, limitBytes)
		}
		if len(payload.ToolCall) > limitBytes || len(payload.ResultText) > limitBytes {
			payload.ToolCall = nil
		}
	}
	payload.ResultText = resultText
	encoded, err := json.Marshal(payload)
	if err != nil {
		return entry, false
	}
	if bytes.Equal(bytes.TrimSpace(entry.Payload), bytes.TrimSpace(encoded)) {
		return entry, false
	}
	entry.Payload = encoded
	return entry, true
}

func autoCompactionOmittedToolResultText(toolName string) string {
	return fmt.Sprintf(
		"[%s result omitted by auto compaction because the preserved current turn still exceeded the context budget; rerun the relevant tool if exact output is needed]",
		firstNonEmpty(strings.TrimSpace(toolName), "tool"),
	)
}

func autoCompactionPreservedEntryIndexes(entries []HistoryEntry, turnSeq int64, requestID string, latestToolCallID string) map[int]struct{} {
	preserved := make(map[int]struct{})
	if len(entries) == 0 || turnSeq <= 0 {
		return preserved
	}
	normalizedRequestID := strings.TrimSpace(requestID)
	normalizedToolCallID := strings.TrimSpace(latestToolCallID)
	latestToolCallIndex := -1
	for index, entry := range entries {
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != normalizedRequestID {
			continue
		}
		if shouldPreserveAutoCompactionEntry(entry, normalizedToolCallID) {
			preserved[index] = struct{}{}
		}
		if strings.TrimSpace(entry.Kind) == "tool_call" && historyEntryToolCallID(entry) == normalizedToolCallID {
			latestToolCallIndex = index
		}
	}
	if latestToolCallIndex < 0 || !toolCallEntryNeedsReasoningCarrier(entries[latestToolCallIndex]) {
		return preserved
	}
	if carrierIndex := latestAssistantReasoningCarrierIndex(entries, latestToolCallIndex, turnSeq, normalizedRequestID); carrierIndex >= 0 {
		preserved[carrierIndex] = struct{}{}
	}
	return preserved
}

func toolCallEntryNeedsReasoningCarrier(entry HistoryEntry) bool {
	if strings.TrimSpace(entry.Kind) != "tool_call" {
		return false
	}
	var payload toolCallEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return false
	}
	return strings.TrimSpace(payload.ReasoningContent) == "" || strings.TrimSpace(payload.ReasoningSignature) == ""
}

func latestAssistantReasoningCarrierIndex(entries []HistoryEntry, beforeIndex int, turnSeq int64, requestID string) int {
	if len(entries) == 0 || beforeIndex <= 0 {
		return -1
	}
	for index := beforeIndex - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) {
			continue
		}
		switch strings.TrimSpace(entry.Kind) {
		case "assistant_text":
			var payload assistantTextPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				continue
			}
			if strings.TrimSpace(payload.ReasoningContent) != "" && strings.TrimSpace(payload.ReasoningSignature) != "" {
				return index
			}
		case "user_message", "request_context":
			return -1
		}
	}
	return -1
}

func historyEntryToolCallID(entry HistoryEntry) string {
	if toolCallID := strings.TrimSpace(entry.ToolCallID); toolCallID != "" {
		return toolCallID
	}
	switch strings.TrimSpace(entry.Kind) {
	case "tool_call":
		var payload toolCallEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err == nil {
			return strings.TrimSpace(payload.ToolCallID)
		}
	case "tool_result":
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err == nil {
			return strings.TrimSpace(payload.ToolCallID)
		}
	}
	return ""
}

func shouldPreserveAutoCompactionEntry(entry HistoryEntry, latestToolCallID string) bool {
	switch strings.TrimSpace(entry.Kind) {
	case "request_context", "user_message":
		return true
	case "tool_call", "tool_result":
		toolCallID := historyEntryToolCallID(entry)
		return toolCallID != "" && toolCallID == strings.TrimSpace(latestToolCallID)
	case "metadata":
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return false
		}
		switch strings.TrimSpace(payload.Type) {
		case "mode", "run_request":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func latestCompletedToolCallIDForTurn(entries []HistoryEntry, turnSeq int64, requestID string) string {
	normalizedRequestID := strings.TrimSpace(requestID)
	latest := ""
	for _, entry := range entries {
		if entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != normalizedRequestID {
			continue
		}
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		toolCallID := strings.TrimSpace(entry.ToolCallID)
		if toolCallID == "" {
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err == nil {
				toolCallID = strings.TrimSpace(payload.ToolCallID)
			}
		}
		if toolCallID != "" {
			latest = toolCallID
		}
	}
	return latest
}

type compactionTerminalError struct {
	code    string
	message string
}

func (err compactionTerminalError) Error() string {
	if strings.TrimSpace(err.message) != "" {
		return strings.TrimSpace(err.message)
	}
	if strings.TrimSpace(err.code) != "" {
		return strings.TrimSpace(err.code)
	}
	return "compaction failed"
}

func (err compactionTerminalError) TerminalCode() string {
	return strings.TrimSpace(err.code)
}

func (service *Service) buildCompactionSummaryMessages(plan *PendingCompaction) ([]modeladapter.Message, error) {
	if plan == nil {
		return nil, nil
	}
	systemText, err := promptassets.ReadCompactionPrompt()
	if err != nil {
		return nil, err
	}
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		return nil, fmt.Errorf("compaction prompt asset is empty")
	}
	sections := make([]string, 0, len(plan.CompactedTurns)+4)
	if strings.TrimSpace(plan.ExistingSummary) != "" {
		sections = append(sections, "Existing summary:\n"+strings.TrimSpace(plan.ExistingSummary))
	}
	lines := make([]string, 0, len(plan.CompactedTurns))
	for index, item := range plan.CompactedTurns {
		parts := make([]string, 0, 1+len(item.Steps))
		if strings.TrimSpace(item.UserText) != "" {
			parts = append(parts, "user="+strings.TrimSpace(item.UserText))
		}
		parts = append(parts, item.Steps...)
		if len(parts) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, strings.Join(parts, " | ")))
	}
	if len(lines) > 0 {
		sections = append(sections, "History to compact:\n"+strings.Join(lines, "\n"))
	}
	if strings.TrimSpace(plan.HookMessage) != "" {
		sections = append(sections, "Pre-compact hook guidance:\n"+strings.TrimSpace(plan.HookMessage))
	}
	if strings.TrimSpace(plan.ManualInstruction) != "" {
		sections = append(sections, "User emphasis for this manual compact:\n"+strings.TrimSpace(plan.ManualInstruction))
	}
	sections = append(sections, "Return only the replacement summary text.")
	return []modeladapter.Message{
		{Role: "system", Content: systemText},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	}, nil
}

func (service *Service) generateCompactionSummary(ctx context.Context, stream *ActiveStream, plan *PendingCompaction, modelCallID string) (string, error) {
	if service == nil || stream == nil || plan == nil {
		return "", nil
	}
	messages, err := service.buildCompactionSummaryMessages(plan)
	if err != nil {
		return "", err
	}
	if len(messages) == 0 {
		return "", nil
	}
	accumulated := ""
	usage := turnUsageSnapshot{}
	err = service.provider.StartStream(ctx, ProviderRequest{
		RequestID:      stream.RequestID,
		ConversationID: stream.ConversationID,
		RunID:          stream.RequestID,
		ModelCallID:    modelCallID,
		ModelID:        stream.ModelID,
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		Messages:       messages,
		MaxTokens:      compactionSummaryOutputMaxTokens,
		CompileSummary: fmt.Sprintf("compaction trigger=%s source=%s turns=%d messages=%d", plan.Trigger, plan.RequestSource, plan.CompactTurnCount, plan.MessagesToCompact),
		Observer:       service.recorder,
		ArtifactPaths:  &modeladapter.LLMArtifactPaths{},
	}, func(event modeladapter.ModelEvent) error {
		if err := providerLoopInterruptErr(ctx, stream, modelCallID); err != nil {
			return err
		}
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			accumulated += event.Text
			if strings.TrimSpace(accumulated) == "" {
				return nil
			}
			return service.broker.Publish(stream.RequestID, StreamEvent{
				Message: buildSummaryMessage(accumulated),
			})
		case modeladapter.ModelEventKindThinkingDelta, modeladapter.ModelEventKindThinkingCompleted:
			return nil
		case modeladapter.ModelEventKindToolLikeCompleted:
			return fmt.Errorf("compaction summary generation must not invoke tools")
		case modeladapter.ModelEventKindTurnFinished:
			usage = turnUsageSnapshot{
				Provider:          event.Provider,
				Model:             event.Model,
				InputTokens:       event.InputTokens,
				OutputTokens:      event.OutputTokens,
				CacheReadTokens:   event.CacheReadTokens,
				CacheWriteTokens:  event.CacheWriteTokens,
				UsagePresent:      event.UsagePresent,
				CacheReadPresent:  event.CacheReadPresent,
				CacheWritePresent: event.CacheWritePresent,
			}
			return nil
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
		if !errors.Is(err, errProviderLoopInterrupted) {
			stream.mu.Lock()
			conversationID := stream.ConversationID
			requestID := stream.RequestID
			turnSeq := stream.TurnSeq
			stream.mu.Unlock()
			if usageErr := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "provider_error", usage, err.Error(), false); usageErr != nil {
				return "", fmt.Errorf("record compaction provider error usage: %w", usageErr)
			}
		}
		return "", err
	}
	stream.mu.Lock()
	conversationID := stream.ConversationID
	requestID := stream.RequestID
	turnSeq := stream.TurnSeq
	stream.mu.Unlock()
	if err := service.recordTurnUsageSnapshot(stream, conversationID, turnSeq, requestID, modelCallID, "completed", usage, "", false); err != nil {
		return "", fmt.Errorf("record compaction provider usage: %w", err)
	}
	return strings.TrimSpace(accumulated), nil
}

func existingConversationSummaryText(conversation *ConversationFile) string {
	texts := compactionSummaryTexts(conversation)
	if len(texts) == 0 {
		return ""
	}
	return strings.TrimSpace(texts[len(texts)-1])
}

func encodeConversationSummaryBytes(summary string) []byte {
	text := strings.TrimSpace(summary)
	if text == "" {
		return nil
	}
	payload, err := proto.Marshal(&agentv1.ConversationSummary{Summary: text})
	if err != nil {
		return nil
	}
	return payload
}

func decodeCompactedTurnSummaries(rawTurns [][]byte) ([]compactedTurnSummary, error) {
	summaries := make([]compactedTurnSummary, 0, len(rawTurns))
	for _, rawTurn := range rawTurns {
		if len(rawTurn) == 0 {
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(rawTurn, turn); err != nil {
			return nil, fmt.Errorf("decode compacted turn: %w", err)
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil {
			continue
		}
		summaries = append(summaries, buildCompactedTurnSummary(agentTurn))
	}
	return summaries, nil
}

func summarizeCompactedToolCall(toolCall *agentv1.ToolCall) (string, string) {
	shape, ok := extractCompactToolCallShape(toolCall)
	if !ok {
		return "", ""
	}
	return shape.ToolName, truncateCompactionText(shape.ResultJSON, compactionTurnSnippetMaxChars/3)
}

type compactToolCallShape struct {
	ArgsJSON   string
	ToolName   string
	ResultJSON string
}

func extractCompactToolCallShape(toolCall *agentv1.ToolCall) (compactToolCallShape, bool) {
	if toolCall == nil {
		return compactToolCallShape{}, false
	}
	value := toolCall.ProtoReflect()
	oneof := value.Descriptor().Oneofs().ByName("tool")
	if oneof == nil {
		return compactToolCallShape{}, false
	}
	selected := value.WhichOneof(oneof)
	if selected == nil {
		return compactToolCallShape{}, false
	}
	selectedValue := value.Get(selected)
	if !selectedValue.IsValid() {
		return compactToolCallShape{}, false
	}
	selectedMessage := selectedValue.Message()
	if !selectedMessage.IsValid() {
		return compactToolCallShape{}, false
	}
	argsJSON, _ := extractCompactFieldJSON(selectedMessage, "args")
	resultJSON, _ := extractCompactFieldJSON(selectedMessage, "result")
	toolName := canonicalCompactToolName(string(selected.Name()), string(selectedMessage.Descriptor().Name()), argsJSON, resultJSON)
	if toolName == "" {
		return compactToolCallShape{}, false
	}
	return compactToolCallShape{
		ArgsJSON:   argsJSON,
		ToolName:   toolName,
		ResultJSON: resultJSON,
	}, true
}

func canonicalCompactToolName(fieldName string, messageName string, argsJSON string, resultJSON string) string {
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
		return compactEditToolName(argsJSON, resultJSON)
	}
	trimmed := strings.TrimSuffix(strings.TrimSpace(messageName), "ToolCall")
	return strings.TrimSpace(trimmed)
}

func compactEditToolName(argsJSON string, resultJSON string) string {
	if editResultJSONLooksLikeStructuredEdit(resultJSON) {
		return "Edit"
	}
	if compactEditArgsIndicateWrite(argsJSON) {
		return "Write"
	}
	return "Edit"
}

func compactEditArgsIndicateWrite(argsJSON string) bool {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return false
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return false
	}
	for _, key := range []string{"stream_content", "streamContent"} {
		if _, ok := args[key]; ok {
			return true
		}
	}
	return false
}

func editResultJSONLooksLikeStructuredEdit(resultJSON string) bool {
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

func extractCompactFieldJSON(message protoreflect.Message, fieldName string) (string, bool) {
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
	payload, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(item)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(payload)), true
}

func buildCompactedConversationSummary(existingSummary string, turns []compactedTurnSummary) string {
	parts := make([]string, 0, len(turns)+1)
	if strings.TrimSpace(existingSummary) != "" {
		parts = append(parts, strings.TrimSpace(existingSummary))
	}
	for index, item := range turns {
		segments := make([]string, 0, 1+len(item.Steps))
		if strings.TrimSpace(item.UserText) != "" {
			segments = append(segments, "user="+strings.TrimSpace(item.UserText))
		}
		segments = append(segments, item.Steps...)
		if len(segments) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d. %s", index+1, strings.Join(segments, " | ")))
	}
	return truncateCompactionText(strings.Join(parts, "\n"), compactionSummaryMaxChars)
}

func estimateCompactedTurnSummariesTokens(rawTurns [][]byte) int64 {
	summaries, err := decodeCompactedTurnSummaries(rawTurns)
	if err != nil {
		return 0
	}
	total := int64(0)
	for _, item := range summaries {
		total += estimateTextTokens(item.UserText)
		for _, step := range item.Steps {
			total += estimateTextTokens(step)
		}
	}
	return total
}

func truncateCompactionText(text string, maxChars int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxChars <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= maxChars {
		return trimmed
	}
	return string(runes[:maxChars]) + "..."
}
