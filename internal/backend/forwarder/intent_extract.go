// intent_extract.go extracts pending exec helpers from service.go (TD-002).
package forwarder

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	runtimecore "cursor/internal/backend/agent/core"
)

type pendingAssistantMessage struct {
	ID      string                     `json:"id,omitempty"` 
	Role    string                     `json:"role,omitempty"` 
	Content []pendingAssistantContent  `json:"content,omitempty"` 
}

type pendingAssistantContent struct {
	Type       string           `json:"type,omitempty"` 
	Text       string           `json:"text,omitempty"` 
	Signature  string           `json:"signature,omitempty"` 
	ToolCallID string           `json:"toolCallId,omitempty"` 
	ToolName   string           `json:"toolName,omitempty"` 
	Args       json.RawMessage  `json:"args,omitempty"` 
}

type pendingToolCallReplay struct {
	OpenedAt time.Time
	SortKey  string
	Raw      string
}
func buildPendingToolCalls(pendingExecs []runtimecore.PendingExec, pendingInteractions []runtimecore.PendingInteraction) []string {
	if len(pendingExecs) == 0 && len(pendingInteractions) == 0 {
		return nil
	}

	items := make([]pendingToolCallReplay, 0, len(pendingExecs)+len(pendingInteractions))
	for _, pending := range pendingExecs {
		raw, ok := encodePendingExecAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  fmt.Sprintf("exec-%020d", pending.MessageID),
			Raw:      raw,
		})
	}
	for _, pending := range pendingInteractions {
		raw, ok := encodePendingInteractionAsAssistantOutput(pending)
		if !ok {
			continue
		}
		items = append(items, pendingToolCallReplay{
			OpenedAt: pending.OpenedAt,
			SortKey:  "interaction-" + strings.TrimSpace(pending.InteractionID),
			Raw:      raw,
		})
	}
	if len(items) == 0 {
		return nil
	}

	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		switch {
		case left.OpenedAt.Equal(right.OpenedAt):
			return left.SortKey < right.SortKey
		case left.OpenedAt.IsZero():
			return false
		case right.OpenedAt.IsZero():
			return true
		default:
			return left.OpenedAt.Before(right.OpenedAt)
		}
	})

	encoded := make([]string, 0, len(items))
	for _, item := range items {
		encoded = append(encoded, item.Raw)
	}
	return encoded
}

func encodePendingExecAsAssistantOutput(pending runtimecore.PendingExec) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName, argsJSON, ok := pendingAssistantToolShape(pending)
	if toolCallID == "" || !ok || strings.TrimSpace(toolName) == "" {
		return "", false
	}

	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, argsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func encodePendingInteractionAsAssistantOutput(pending runtimecore.PendingInteraction) (string, bool) {
	toolCallID := strings.TrimSpace(pending.ToolCallID)
	toolName := strings.TrimSpace(deriveToolNameFromPendingInteraction(pending))
	if toolCallID == "" || toolName == "" {
		return "", false
	}
	payload, err := json.Marshal(pendingAssistantMessage{
		ID:      "1",
		Role:    "assistant",
		Content: buildPendingAssistantContents(pending.ReasoningContent, pending.ReasoningSignature, toolCallID, toolName, pending.ArgsJSON),
	})
	if err != nil {
		return "", false
	}
	return string(payload), true
}

func buildPendingAssistantContents(reasoningContent string, reasoningSignature string, toolCallID string, toolName string, argsJSON []byte) []pendingAssistantContent {
	items := make([]pendingAssistantContent, 0, 2)
	if strings.TrimSpace(reasoningContent) != "" {
		items = append(items, pendingAssistantContent{
			Type:      "reasoning",
			Text:      reasoningContent,
			Signature: strings.TrimSpace(reasoningSignature),
		})
	}
	items = append(items, pendingAssistantContent{
		Type:       "tool-call",
		ToolCallID: toolCallID,
		ToolName:   strings.TrimSpace(toolName),
		Args:       append(json.RawMessage(nil), argsJSON...),
	})
	return items
}

func pendingAssistantToolShape(pending runtimecore.PendingExec) (string, []byte, bool) {
	switch strings.TrimSpace(pending.ExecKind) {
	case patchEditReadExecKindName, patchEditWriteExecKindName, patchEditPostReadExecKindName:
		payload, err := decodePendingPatchEditPayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := patchEditPayloadArgsJSON(payload)
		if err != nil {
			return "", nil, false
		}
		return firstNonEmpty(strings.TrimSpace(payload.ToolName), patchEditToolName), argsJSON, true
	case writeReadExecKind, writeWriteExecKind, writePostReadExecKind:
		payload, err := decodePendingWritePayload(pending.ArgsJSON)
		if err != nil {
			return "", nil, false
		}
		argsJSON, err := payload.VisibleArgs.MarshalJSON()
		if err != nil {
			return "", nil, false
		}
		return "Write", argsJSON, true
	default:
		toolName := strings.TrimSpace(deriveToolNameFromPendingExec(pending))
		if toolName == "" {
			return "", nil, false
		}
		return toolName, append([]byte(nil), pending.ArgsJSON...), true
	}
}

// markExecCompleted 保留一个短时 tombstone，避免迟到的 transport-level control 被误判为协议错误。
func markExecCompleted(stream *ActiveStream, pending runtimecore.PendingExec) {
	if stream == nil {
		return
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	delete(stream.PendingExecs, pending.ExecID)
	if pending.MessageID != 0 {
		if stream.RecentCompletedExecs == nil {
			stream.RecentCompletedExecs = make(map[uint32]time.Time)
		}
		for messageID, completedAt := range stream.RecentCompletedExecs {
			if completedAt.Before(cutoff) {
				delete(stream.RecentCompletedExecs, messageID)
			}
		}
		stream.RecentCompletedExecs[pending.MessageID] = now
	}
	stream.UpdatedAt = now
	stream.mu.Unlock()
}

func recentlyCompletedExecExists(stream *ActiveStream, messageID uint32) bool {
	if stream == nil || messageID == 0 {
		return false
	}
	now := time.Now().UTC()
	cutoff := now.Add(-completedExecRetention)

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if len(stream.RecentCompletedExecs) == 0 {
		return false
	}
	completedAt, ok := stream.RecentCompletedExecs[messageID]
	for id, ts := range stream.RecentCompletedExecs {
		if ts.Before(cutoff) {
			delete(stream.RecentCompletedExecs, id)
		}
	}
	if !ok {
		return false
	}
	if completedAt.Before(cutoff) {
		delete(stream.RecentCompletedExecs, messageID)
		return false
	}
	return true
}