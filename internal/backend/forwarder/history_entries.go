// history_entries.go extracts stream helper functions from service.go (TD-002).
package forwarder

import (
	"context"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)
func (service *Service) resolveRequestedModelName(message *agentv1.AgentClientMessage, modelID string) string {
	if message != nil {
		if runRequest := message.GetRunRequest(); runRequest != nil {
			if name := firstNonEmpty(
				runRequest.GetModelDetails().GetDisplayName(),
				runRequest.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
		if prewarm := message.GetPrewarmRequest(); prewarm != nil {
			if name := firstNonEmpty(
				prewarm.GetModelDetails().GetDisplayName(),
				prewarm.GetModelDetails().GetDisplayModelId(),
			); name != "" {
				return name
			}
		}
	}
	if service != nil && service.resolver != nil {
		channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
		if err == nil && channel != nil {
			if name := firstNonEmpty(channel.Name, channel.Model); name != "" {
				return name
			}
		}
	}
	return strings.TrimSpace(modelID)
}

func (service *Service) resolveContextWindowTokens(modelID string) uint32 {
	if service == nil || service.resolver == nil {
		return projectedConversationMaxTokens
	}
	channel, err := service.resolver.SelectChannelForModel(context.Background(), strings.TrimSpace(modelID))
	if err != nil || channel == nil || channel.ContextWindowTokens <= 0 {
		return projectedConversationMaxTokens
	}
	return clampInt64ToUint32(int64(channel.ContextWindowTokens))
}

func (service *Service) syncConversationContextWindowTokens(stream *ActiveStream, conversationID string, conversation *ConversationFile) (*ConversationFile, error) {
	if stream == nil || conversation == nil {
		return conversation, nil
	}
	stream.mu.Lock()
	modelID := stream.ModelID
	stream.mu.Unlock()
	target := service.resolveContextWindowTokens(modelID)
	if target == 0 || conversation.TokenDetailsMaxTokens == target {
		return conversation, nil
	}
	return service.updateConversationMetaAndCheckpoint(stream, conversationID, func(item *ConversationFile) error {
		if item == nil {
			return nil
		}
		item.TokenDetailsMaxTokens = target
		return nil
	})
}

// userMessageText 返回用户消息中的纯文本。
func userMessageText(message *agentv1.UserMessage) string {
	if message == nil {
		return ""
	}
	return strings.TrimSpace(message.GetText())
}

func currentProviderPass(stream *ActiveStream) int {
	if stream == nil {
		return 0
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.ProviderPassCount
}

func currentStreamMode(stream *ActiveStream) agentv1.AgentMode {
	if stream == nil {
		return agentv1.AgentMode_AGENT_MODE_AGENT
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if normalized, err := validateSupportedActiveMode(stream.Mode); err == nil {
		return normalized
	}
	return stream.Mode
}

// selectPendingExec 按 exec_id 或 message_id 在当前流里查找挂起执行桥。
func selectPendingExec(execID string, messageID uint32, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if item, ok := stream.PendingExecs[strings.TrimSpace(execID)]; ok {
		return item, true
	}
	if messageID != 0 {
		for _, item := range stream.PendingExecs {
			if item.MessageID == messageID {
				return item, true
			}
		}
	}
	return runtimecore.PendingExec{}, false
}

func selectPendingInteraction(message *agentv1.InteractionResponse, stream *ActiveStream) (runtimecore.PendingInteraction, bool) {
	if stream == nil || message == nil {
		return runtimecore.PendingInteraction{}, false
	}
	interactionID := fmt.Sprintf("%d", message.GetId())
	stream.mu.Lock()
	defer stream.mu.Unlock()
	item, ok := stream.PendingInteractions[interactionID]
	return item, ok
}

// selectPendingExecByControl 根据控制消息的桥消息 ID 查找挂起执行桥。
func selectPendingExecByControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream) (runtimecore.PendingExec, bool) {
	messageID, ok := execControlMessageID(message)
	if !ok {
		return runtimecore.PendingExec{}, false
	}
	return selectPendingExec("", messageID, stream)
}

func execControlMessageID(message *agentv1.ExecClientControlMessage) (uint32, bool) {
	if message == nil {
		return 0, false
	}
	switch item := message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_StreamClose:
		return item.StreamClose.GetId(), true
	case *agentv1.ExecClientControlMessage_Throw:
		return item.Throw.GetId(), true
	case *agentv1.ExecClientControlMessage_Heartbeat:
		return item.Heartbeat.GetId(), true
	default:
		return 0, false
	}
}

func shouldIgnoreMissingExecResult(message *agentv1.ExecClientMessage, stream *ActiveStream) bool {
	if message == nil {
		return false
	}
	return recentlyCompletedExecExists(stream, message.GetId())
}

func shouldIgnoreMissingExecControl(message *agentv1.ExecClientControlMessage, stream *ActiveStream) bool {
	if shouldIgnoreStaleExecControl(message) {
		return true
	}
	messageID, ok := execControlMessageID(message)
	if !ok {
		return false
	}
	return recentlyCompletedExecExists(stream, messageID)
}

func shouldIgnoreStaleExecControl(message *agentv1.ExecClientControlMessage) bool {
	if message == nil {
		return false
	}
	switch message.GetMessage().(type) {
	case *agentv1.ExecClientControlMessage_Heartbeat, *agentv1.ExecClientControlMessage_StreamClose:
		// Reconnecting Cursor clients may keep sending transport-level exec
		// heartbeats / close acks after the original in-memory pending state is gone.
		// Treat these as idempotent noise instead of surfacing protocol 400s.
		return true
	default:
		return false
	}
}