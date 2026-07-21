// inbound.go 实现上行协议的解码、摘要与命令类型识别。
package protocol

import (
	"encoding/hex"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"

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
