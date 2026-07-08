// inbound.go 实现上行协议的解码、摘要与命令类型识别。
package protocol

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/backend/agent/core"

	"google.golang.org/protobuf/proto"
)

// ReadAppendRequestID 从 BidiAppendRequest 中读取 request_id 文本。
func ReadAppendRequestID(input *aiserverv1.BidiAppendRequest) string {
	if input == nil {
		return ""
	}
	return ReadBidiRequestID(input.GetRequestId())
}

// ReadBidiRequestID 从 BidiRequestId 结构中提取并去除首尾空白。
func ReadBidiRequestID(input *aiserverv1.BidiRequestId) string {
	if input == nil {
		return ""
	}
	return strings.TrimSpace(input.GetRequestId())
}

// NormalizeRequestID 规范化请求标识并去除首尾空白。
func NormalizeRequestID(requestID string) string {
	return strings.TrimSpace(requestID)
}

// DecodeAgentClientMessage 解析 hex 文本为 AgentClientMessage，并返回消息类型标签。
func DecodeAgentClientMessage(hexData string) (*agentv1.AgentClientMessage, string, error) {
	trimmed := strings.TrimSpace(hexData)
	if trimmed == "" {
		return nil, "", nil
	}
	payload, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, "", fmt.Errorf("bidi append data is not valid hex: %w", err)
	}
	clientMessage := &agentv1.AgentClientMessage{}
	if err := proto.Unmarshal(payload, clientMessage); err != nil {
		return nil, "", fmt.Errorf("decode agent client message failed: %w", err)
	}
	return clientMessage, detectClientMessageKind(clientMessage), nil
}

// MapClientMessageToCommandKind 将上行协议消息映射为运行时命令类型。
func MapClientMessageToCommandKind(message *agentv1.AgentClientMessage, clientKind string) (runtimecore.CommandKind, error) {
	switch strings.TrimSpace(clientKind) {
	case "run_request":
		return runtimecore.CommandKindRunRequested, nil
	case "prewarm_request":
		return runtimecore.CommandKindPrewarmRequested, nil
	case "conversation_action":
		if message == nil || message.GetConversationAction() == nil {
			return "", fmt.Errorf("conversation_action payload is required")
		}
		switch message.GetConversationAction().GetAction().(type) {
		case *agentv1.ConversationAction_CancelAction:
			return runtimecore.CommandKindCancelRequested, nil
		case *agentv1.ConversationAction_UserMessageAction,
			*agentv1.ConversationAction_ResumeAction,
			*agentv1.ConversationAction_SummarizeAction,
			*agentv1.ConversationAction_ShellCommandAction,
			*agentv1.ConversationAction_StartPlanAction,
			*agentv1.ConversationAction_ExecutePlanAction,
			*agentv1.ConversationAction_AsyncAskQuestionCompletionAction,
			*agentv1.ConversationAction_CancelSubagentAction,
			*agentv1.ConversationAction_BackgroundShellAction,
			*agentv1.ConversationAction_BackgroundTaskCompletionAction:
			return runtimecore.CommandKindConversationActionRecordOnly, nil
		default:
			return "", fmt.Errorf("unsupported conversation_action payload")
		}
	case "exec_client_message":
		return runtimecore.CommandKindExecClientMessage, nil
	case "interaction_response":
		return runtimecore.CommandKindInteractionResponse, nil
	case "exec_client_control_message":
		return runtimecore.CommandKindExecClientControlMessage, nil
	case "client_heartbeat":
		return runtimecore.CommandKindClientHeartbeat, nil
	case "kv_client_message":
		return runtimecore.CommandKindKVClientMessage, nil
	default:
		return "", fmt.Errorf("unsupported client message kind: %s", clientKind)
	}
}

// IsResumeRunRequest 判断当前消息是否为带 `resume_action` 的 `run_request`。
func IsResumeRunRequest(message *agentv1.AgentClientMessage) bool {
	if message == nil || message.GetRunRequest() == nil {
		return false
	}
	action := message.GetRunRequest().GetAction()
	if action == nil {
		return false
	}
	_, ok := action.GetAction().(*agentv1.ConversationAction_ResumeAction)
	return ok
}

// BuildClientHistoryEntry 将消息类型与负载摘要拼接成会话历史记录文本。
func BuildClientHistoryEntry(kind string, message *agentv1.AgentClientMessage) string {
	normalizedKind := strings.TrimSpace(kind)
	if normalizedKind == "" {
		normalizedKind = "unknown"
	}

	payload := extractClientMessagePayload(message)
	summary := summarizePayload(payload)
	if summary == "" {
		if normalizedKind == "unknown" {
			return ""
		}
		return normalizedKind
	}
	return fmt.Sprintf("%s:%s", normalizedKind, summary)
}

// detectClientMessageKind 判断 oneof message 当前承载的消息分支类型。
func detectClientMessageKind(message *agentv1.AgentClientMessage) string {
	if message == nil || message.GetMessage() == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentClientMessage_RunRequest:
		return "run_request"
	case *agentv1.AgentClientMessage_PrewarmRequest:
		return "prewarm_request"
	case *agentv1.AgentClientMessage_ConversationAction:
		return "conversation_action"
	case *agentv1.AgentClientMessage_ExecClientMessage:
		return "exec_client_message"
	case *agentv1.AgentClientMessage_InteractionResponse:
		return "interaction_response"
	case *agentv1.AgentClientMessage_ExecClientControlMessage:
		return "exec_client_control_message"
	case *agentv1.AgentClientMessage_ClientHeartbeat:
		return "client_heartbeat"
	case *agentv1.AgentClientMessage_KvClientMessage:
		return "kv_client_message"
	default:
		return ""
	}
}

// extractClientMessagePayload 从 oneof 分支中提取原始 bytes 负载。
func extractClientMessagePayload(message *agentv1.AgentClientMessage) []byte {
	if message == nil || message.GetMessage() == nil {
		return nil
	}

	switch item := message.GetMessage().(type) {
	case *agentv1.AgentClientMessage_RunRequest:
		return marshalProtoMessage(item.RunRequest)
	case *agentv1.AgentClientMessage_PrewarmRequest:
		return marshalProtoMessage(item.PrewarmRequest)
	case *agentv1.AgentClientMessage_ConversationAction:
		return marshalProtoMessage(item.ConversationAction)
	case *agentv1.AgentClientMessage_ExecClientMessage:
		return marshalProtoMessage(item.ExecClientMessage)
	case *agentv1.AgentClientMessage_InteractionResponse:
		return marshalProtoMessage(item.InteractionResponse)
	case *agentv1.AgentClientMessage_ExecClientControlMessage:
		return marshalProtoMessage(item.ExecClientControlMessage)
	case *agentv1.AgentClientMessage_ClientHeartbeat:
		return marshalProtoMessage(item.ClientHeartbeat)
	case *agentv1.AgentClientMessage_KvClientMessage:
		return marshalProtoMessage(item.KvClientMessage)
	default:
		return nil
	}
}

// marshalProtoMessage 将 proto 消息重新编码为 bytes，用于调试摘要展示。
func marshalProtoMessage(message proto.Message) []byte {
	if message == nil {
		return nil
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return nil
	}
	return payload
}

// summarizePayload 生成可读摘要：优先文本，无法直接读时回退为 hex 片段。
func summarizePayload(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}

	if utf8.Valid(payload) {
		text := strings.TrimSpace(string(payload))
		text = strings.ReplaceAll(text, "\n", " ")
		text = strings.ReplaceAll(text, "\r", " ")
		text = strings.TrimSpace(text)
		if text != "" {
			return truncateText(text, 120)
		}
	}
	return "hex:" + truncateText(hex.EncodeToString(payload), 120)
}

// truncateText 按 rune 数截断文本，避免在多字节字符中间截断导致乱码。
func truncateText(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}
