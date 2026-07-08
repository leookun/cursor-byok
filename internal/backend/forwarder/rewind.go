package forwarder

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
)

type runRewindDecision struct {
	Evaluated          bool
	Apply              bool
	Reason             string
	SkipReason         string
	IncomingMessageID  string
	HasClientTurnCount bool
	ClientTurnCount    int
	ServerTailTurnSeq  int64
	ServerNextTurnSeq  int64
	TargetTurnSeq      int64
	TargetEntrySeq     int64
	TargetRequestID    string
	MatchCount         int
	DroppedEntryCount  int
	DroppedTurnCount   int
	DroppedSeqStart    int64
	DroppedSeqEnd      int64
	PrefixEntries      []HistoryEntry
}

type runRewindMatch struct {
	Entry HistoryEntry
}

func (service *Service) decideRunRewind(intent InboundIntent, conversation *ConversationFile) runRewindDecision {
	decision := runRewindDecision{ClientTurnCount: -1}
	if !shouldEvaluateRunRewind(intent) {
		return decision
	}
	decision.Evaluated = true
	decision.IncomingMessageID = strings.TrimSpace(intent.UserMessage.GetMessageId())
	decision.HasClientTurnCount = intent.ConversationState != nil
	if decision.HasClientTurnCount {
		decision.ClientTurnCount = len(intent.ConversationState.GetTurns())
	}
	if conversation != nil {
		decision.ServerTailTurnSeq = maxHistoryTurnSeq(conversation.Entries)
		decision.ServerNextTurnSeq = conversation.NextTurnSeq
	}
	if decision.IncomingMessageID == "" {
		decision.SkipReason = "missing_message_id"
		return decision
	}
	if conversation == nil || len(conversation.Entries) == 0 {
		decision.SkipReason = "message_id_not_found"
		return decision
	}

	matches := findUserMessageEntriesByMessageID(conversation.Entries, decision.IncomingMessageID)
	decision.MatchCount = len(matches)
	if len(matches) == 0 {
		decision.SkipReason = "message_id_not_found"
		return decision
	}
	selected, selectReason := selectRunRewindMatch(matches, decision.ClientTurnCount, decision.HasClientTurnCount)
	decision.TargetTurnSeq = selected.Entry.TurnSeq
	decision.TargetEntrySeq = selected.Entry.Seq
	decision.TargetRequestID = strings.TrimSpace(selected.Entry.RequestID)
	if decision.TargetTurnSeq <= 0 {
		decision.SkipReason = "target_turn_seq_missing"
		return decision
	}

	serverTailBeyondTarget := decision.ServerTailTurnSeq > decision.TargetTurnSeq
	clientBehindServerTail := decision.HasClientTurnCount && int64(decision.ClientTurnCount) < decision.ServerTailTurnSeq
	if !serverTailBeyondTarget && !clientBehindServerTail {
		decision.SkipReason = "message_id_at_active_tail"
		return decision
	}

	decision.Apply = true
	decision.Reason = selectReason
	decision.PrefixEntries = prefixEntriesBeforeTurn(conversation.Entries, decision.TargetTurnSeq)
	decision.DroppedEntryCount, decision.DroppedTurnCount, decision.DroppedSeqStart, decision.DroppedSeqEnd = droppedEntryStats(conversation.Entries, decision.TargetTurnSeq)
	return decision
}

func shouldEvaluateRunRewind(intent InboundIntent) bool {
	if strings.TrimSpace(intent.Kind) != "run" || intent.Prewarm || intent.UserMessage == nil {
		return false
	}
	return conversationActionCase(intent.ClientMessage) == "user_message_action"
}

func findUserMessageEntriesByMessageID(entries []HistoryEntry, messageID string) []runRewindMatch {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" || len(entries) == 0 {
		return nil
	}
	matches := make([]runRewindMatch, 0, 1)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "user_message" || len(entry.Payload) == 0 {
			continue
		}
		userMessage := &agentv1.UserMessage{}
		if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
			continue
		}
		if strings.TrimSpace(userMessage.GetMessageId()) != messageID {
			continue
		}
		matches = append(matches, runRewindMatch{Entry: entry})
	}
	return matches
}

func selectRunRewindMatch(matches []runRewindMatch, clientTurnCount int, hasClientTurnCount bool) (runRewindMatch, string) {
	if len(matches) == 0 {
		return runRewindMatch{}, "no_match"
	}
	if hasClientTurnCount && clientTurnCount >= 0 {
		targetTurnSeq := int64(clientTurnCount) + 1
		for _, match := range matches {
			if match.Entry.TurnSeq == targetTurnSeq {
				return match, "client_turn_count_aligned"
			}
		}
		var candidate *runRewindMatch
		for index := range matches {
			match := matches[index]
			if match.Entry.TurnSeq <= int64(clientTurnCount) {
				continue
			}
			if candidate == nil || earlierHistoryEntry(match.Entry, candidate.Entry) {
				candidate = &match
			}
		}
		if candidate != nil {
			return *candidate, "first_match_after_client_turn_count"
		}
	}
	selected := matches[0]
	for _, match := range matches[1:] {
		if earlierHistoryEntry(match.Entry, selected.Entry) {
			selected = match
		}
	}
	return selected, "earliest_message_id_match"
}

func earlierHistoryEntry(left HistoryEntry, right HistoryEntry) bool {
	if left.TurnSeq != right.TurnSeq {
		return left.TurnSeq < right.TurnSeq
	}
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func maxHistoryTurnSeq(entries []HistoryEntry) int64 {
	var maxTurnSeq int64
	for _, entry := range entries {
		if entry.TurnSeq > maxTurnSeq {
			maxTurnSeq = entry.TurnSeq
		}
	}
	return maxTurnSeq
}

func prefixEntriesBeforeTurn(entries []HistoryEntry, targetTurnSeq int64) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	prefix := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.TurnSeq < targetTurnSeq {
			prefix = append(prefix, entry)
		}
	}
	return prefix
}

func droppedEntryStats(entries []HistoryEntry, targetTurnSeq int64) (int, int, int64, int64) {
	if len(entries) == 0 {
		return 0, 0, 0, 0
	}
	droppedTurns := make(map[int64]struct{})
	var droppedEntries int
	var seqStart int64
	var seqEnd int64
	for _, entry := range entries {
		if entry.TurnSeq < targetTurnSeq {
			continue
		}
		droppedEntries++
		if entry.TurnSeq > 0 {
			droppedTurns[entry.TurnSeq] = struct{}{}
		}
		if entry.Seq > 0 && (seqStart == 0 || entry.Seq < seqStart) {
			seqStart = entry.Seq
		}
		if entry.Seq > seqEnd {
			seqEnd = entry.Seq
		}
	}
	return droppedEntries, len(droppedTurns), seqStart, seqEnd
}

func appendReplacementRunEntries(prefix []HistoryEntry, entries []HistoryEntry) []HistoryEntry {
	replacement := make([]HistoryEntry, 0, len(prefix)+len(entries))
	replacement = append(replacement, prefix...)
	replacement = append(replacement, entries...)
	return replacement
}

func (service *Service) applyRunRewindToConversation(conversation *ConversationFile, decision runRewindDecision, entries []HistoryEntry, intent InboundIntent, turnSeq int64) {
	if conversation == nil || !decision.Apply {
		return
	}
	conversation.Entries = nil
	conversation.NextEntrySeq = 1
	conversation.NextTurnSeq = 1
	appendEntriesInPlace(conversation, appendReplacementRunEntries(decision.PrefixEntries, entries))
	applyRunRewindConversationState(conversation, intent, turnSeq)
	deriveConversationLoopState(conversation)
}

func applyRunRewindConversationState(conversation *ConversationFile, intent InboundIntent, turnSeq int64) {
	if conversation == nil {
		return
	}
	conversation.TokenDetailsUsedTokens = 0
	if conversation.TokenDetailsMaxTokens == 0 {
		conversation.TokenDetailsMaxTokens = projectedConversationMaxTokens
	}
	clearConversationAutoCompactionState(conversation)
	conversation.LatestRequestPrefix = nil
	conversation.LastProviderCall = nil
	conversation.CurrentLoopID = fmt.Sprintf("%d:%s", turnSeq, strings.TrimSpace(intent.RequestID))
	conversation.CurrentLoopStatus = "running"
	conversation.CurrentRequestID = strings.TrimSpace(intent.RequestID)
	conversation.CurrentTurnSeq = turnSeq
}

func applyRunRewindMetadata(conversation *ConversationFile, source *ConversationFile, intent InboundIntent, turnSeq int64) {
	if conversation == nil {
		return
	}
	if source != nil {
		if strings.TrimSpace(source.ConversationID) != "" {
			conversation.ConversationID = strings.TrimSpace(source.ConversationID)
		}
		if strings.TrimSpace(source.RootConversationID) != "" {
			conversation.RootConversationID = strings.TrimSpace(source.RootConversationID)
		}
		conversation.ParentConversationID = strings.TrimSpace(source.ParentConversationID)
		conversation.ParentToolCallID = strings.TrimSpace(source.ParentToolCallID)
		conversation.SubagentTypeName = strings.TrimSpace(source.SubagentTypeName)
		if strings.TrimSpace(source.Mode) != "" {
			conversation.Mode = strings.TrimSpace(source.Mode)
		}
		if source.TokenDetailsMaxTokens > 0 {
			conversation.TokenDetailsMaxTokens = source.TokenDetailsMaxTokens
		}
	}
	applyRunRewindConversationState(conversation, intent, turnSeq)
}

func (service *Service) logRunRewindDecision(requestID string, conversationID string, eventName string, decision runRewindDecision) {
	if service == nil || !decision.Evaluated {
		return
	}
	fields := map[string]any{
		"message_id":           decision.IncomingMessageID,
		"apply":                decision.Apply,
		"reason":               decision.Reason,
		"skip_reason":          decision.SkipReason,
		"target_turn_seq":      decision.TargetTurnSeq,
		"target_entry_seq":     decision.TargetEntrySeq,
		"target_request_id":    decision.TargetRequestID,
		"server_tail_turn_seq": decision.ServerTailTurnSeq,
		"server_next_turn_seq": decision.ServerNextTurnSeq,
		"match_count":          decision.MatchCount,
		"dropped_entry_count":  decision.DroppedEntryCount,
		"dropped_turn_count":   decision.DroppedTurnCount,
		"dropped_seq_start":    decision.DroppedSeqStart,
		"dropped_seq_end":      decision.DroppedSeqEnd,
	}
	if decision.HasClientTurnCount {
		fields["client_turn_count"] = decision.ClientTurnCount
	} else {
		fields["client_turn_count"] = nil
	}
	service.debug.LogRuntime(context.Background(), requestID, conversationID, eventName, fields)
}
