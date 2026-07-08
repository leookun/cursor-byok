package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

type debugLogConfig interface {
	IsObservabilityLogEnabled(context.Context) bool
}

type debugRecorder struct {
	historyRoot string
	broker      *StreamBroker
	config      debugLogConfig
	mu          sync.Mutex
}

func newDebugRecorder(historyRoot string, broker *StreamBroker, config debugLogConfig) *debugRecorder {
	return &debugRecorder{
		historyRoot: strings.TrimSpace(historyRoot),
		broker:      broker,
		config:      config,
	}
}

func (recorder *debugRecorder) enabled(ctx context.Context) bool {
	if recorder == nil || recorder.config == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return recorder.config.IsObservabilityLogEnabled(ctx)
}

func (recorder *debugRecorder) LogBidiRaw(ctx context.Context, requestID string, conversationID string, appendSeqno int64, dataHex string, status string, extra map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("bidi_raw", requestID, conversationID)
	event["direction"] = "client_to_backend"
	event["procedure"] = "/aiserver.v1.BidiService/BidiAppend"
	event["append_seqno"] = appendSeqno
	event["status"] = strings.TrimSpace(status)
	event["data_hex"] = dataHex
	event["data_len"] = len(dataHex)
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "bidi.raw.jsonl", event)
}

func (recorder *debugRecorder) LogBidiDecoded(ctx context.Context, requestID string, conversationID string, appendSeqno int64, clientKind string, message *agentv1.AgentClientMessage, intent InboundIntent, extra map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("bidi_decoded", requestID, conversationID)
	event["schema_version"] = 2
	event["append_seqno"] = appendSeqno
	event["client_kind"] = strings.TrimSpace(clientKind)
	event["message_case"] = agentClientMessageCase(message)
	event["message"] = protoJSONDebugPayload(message)
	event["intent"] = inboundIntentDebugPayload(intent)
	if requestedModel := requestedModelDebugPayload(message); requestedModel != nil {
		event["requested_model"] = requestedModel
	}
	if actionCase := conversationActionCase(message); actionCase != "" {
		event["conversation_action"] = actionCase
	}
	for key, value := range extra {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, firstNonEmpty(conversationID, intent.ConversationID), "bidi.decoded.jsonl", event)
}

func (recorder *debugRecorder) LogRuntime(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runtime", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runtime.jsonl", event)
}

func (recorder *debugRecorder) LogRunSSE(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("runsse", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "runsse.jsonl", event)
}

func (recorder *debugRecorder) LogProvider(ctx context.Context, requestID string, conversationID string, eventName string, fields map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	event := recorder.baseEvent("provider", requestID, conversationID)
	event["event"] = strings.TrimSpace(eventName)
	for key, value := range fields {
		event[key] = value
	}
	recorder.appendJSONL(ctx, requestID, conversationID, "provider.jsonl", event)
}

func (recorder *debugRecorder) LogProviderArtifact(ctx context.Context, requestID string, conversationID string, modelCallID string, eventName string, payload map[string]any) {
	if !recorder.enabled(ctx) {
		return
	}
	recorder.LogProvider(ctx, requestID, conversationID, eventName, map[string]any{
		"model_call_id": strings.TrimSpace(modelCallID),
		"payload":       payload,
	})
}

func (recorder *debugRecorder) baseEvent(layer string, requestID string, conversationID string) map[string]any {
	resolvedConversationID := firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	return map[string]any{
		"schema_version":  1,
		"at":              time.Now().UTC().Format(time.RFC3339Nano),
		"layer":           strings.TrimSpace(layer),
		"request_id":      strings.TrimSpace(requestID),
		"conversation_id": resolvedConversationID,
	}
}

func (recorder *debugRecorder) appendJSONL(ctx context.Context, requestID string, conversationID string, filename string, event map[string]any) {
	if !recorder.enabled(ctx) || len(event) == 0 {
		return
	}
	dir := recorder.debugDir(requestID, conversationID)
	if strings.TrimSpace(dir) == "" {
		return
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, filename), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(payload, '\n'))
}

func (recorder *debugRecorder) debugDir(requestID string, conversationID string) string {
	if recorder == nil || strings.TrimSpace(recorder.historyRoot) == "" {
		return ""
	}
	conversationID = firstNonEmpty(strings.TrimSpace(conversationID), recorder.conversationIDForRequest(requestID))
	if conversationID != "" && conversationID != "unknown" {
		return filepath.Join(recorder.historyRoot, sanitizeArtifactName(conversationID), "debug")
	}
	requestID = firstNonEmpty(strings.TrimSpace(requestID), "unknown")
	return filepath.Join(recorder.historyRoot, "_debug", "orphan", sanitizeArtifactName(requestID))
}

func (recorder *debugRecorder) conversationIDForRequest(requestID string) string {
	if recorder == nil || recorder.broker == nil {
		return ""
	}
	stream, ok := recorder.broker.Get(requestID)
	if !ok || stream == nil {
		return ""
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return strings.TrimSpace(stream.ConversationID)
}

func agentClientMessageCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
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
	case *agentv1.AgentClientMessage_ExecClientControlMessage:
		return "exec_client_control_message"
	case *agentv1.AgentClientMessage_InteractionResponse:
		return "interaction_response"
	case *agentv1.AgentClientMessage_ClientHeartbeat:
		return "client_heartbeat"
	case *agentv1.AgentClientMessage_KvClientMessage:
		return "kv_client_message"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func agentServerMessageCase(message *agentv1.AgentServerMessage) string {
	if message == nil {
		return ""
	}
	switch message.GetMessage().(type) {
	case *agentv1.AgentServerMessage_InteractionUpdate:
		return "interaction_update"
	case *agentv1.AgentServerMessage_ExecServerMessage:
		return "exec_server_message"
	case *agentv1.AgentServerMessage_ExecServerControlMessage:
		return "exec_server_control_message"
	case *agentv1.AgentServerMessage_ConversationCheckpointUpdate:
		return "conversation_checkpoint_update"
	case *agentv1.AgentServerMessage_KvServerMessage:
		return "kv_server_message"
	case *agentv1.AgentServerMessage_InteractionQuery:
		return "interaction_query"
	default:
		return fmt.Sprintf("%T", message.GetMessage())
	}
}

func conversationActionCase(message *agentv1.AgentClientMessage) string {
	if message == nil {
		return ""
	}
	action := message.GetConversationAction()
	if action == nil && message.GetRunRequest() != nil {
		action = message.GetRunRequest().GetAction()
	}
	if action == nil {
		return ""
	}
	return conversationActionKind(action)
}

func requestedModelDebugPayload(message *agentv1.AgentClientMessage) map[string]any {
	if message == nil {
		return nil
	}
	if runRequest := message.GetRunRequest(); runRequest != nil {
		return requestedModelPayload(runRequest.GetRequestedModel())
	}
	if prewarm := message.GetPrewarmRequest(); prewarm != nil {
		return requestedModelPayload(prewarm.GetRequestedModel())
	}
	return nil
}

func requestedModelPayload(model *agentv1.RequestedModel) map[string]any {
	if model == nil {
		return nil
	}
	parameters := make([]map[string]string, 0, len(model.GetParameters()))
	for _, parameter := range model.GetParameters() {
		if parameter == nil {
			continue
		}
		parameters = append(parameters, map[string]string{
			"id":    parameter.GetId(),
			"value": parameter.GetValue(),
		})
	}
	return map[string]any{
		"model_id":                         strings.TrimSpace(model.GetModelId()),
		"max_mode":                         model.GetMaxMode(),
		"built_in_model":                   model.GetBuiltInModel(),
		"is_variant_string_representation": model.GetIsVariantStringRepresentation(),
		"parameters":                       parameters,
	}
}

func protoJSONDebugPayload(message proto.Message) any {
	if message == nil {
		return nil
	}
	payload, err := protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
	}.Marshal(message)
	if err != nil {
		return map[string]any{"marshal_error": err.Error()}
	}
	var decoded any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return string(payload)
	}
	return decoded
}

func inboundIntentDebugPayload(intent InboundIntent) map[string]any {
	payload := map[string]any{
		"kind":               strings.TrimSpace(intent.Kind),
		"request_id":         strings.TrimSpace(intent.RequestID),
		"conversation_id":    strings.TrimSpace(intent.ConversationID),
		"model_id":           strings.TrimSpace(intent.ModelID),
		"model_name":         strings.TrimSpace(intent.ModelName),
		"thinking_effort":    strings.TrimSpace(intent.ThinkingEffort),
		"mode":               intent.Mode.String(),
		"has_explicit_mode":  intent.HasExplicitMode,
		"mode_source":        string(intent.ModeSource),
		"starts_run":         intent.StartsRun,
		"subagent_type_name": strings.TrimSpace(intent.SubagentTypeName),
		"cancel_reason":      strings.TrimSpace(intent.CancelReason),
		"prewarm":            intent.Prewarm,
	}
	if len(intent.SubagentModelOverrides) > 0 {
		payload["subagent_model_overrides"] = subagentModelOverrideSummaries(intent.SubagentModelOverrides)
		payload["subagent_model_override_count"] = len(intent.SubagentModelOverrides)
	}
	if intent.ClientMessage != nil {
		payload["client_message"] = protoJSONDebugPayload(intent.ClientMessage)
	}
	if intent.ConversationState != nil {
		payload["conversation_state"] = protoJSONDebugPayload(intent.ConversationState)
	}
	if intent.UserMessage != nil {
		payload["user_message"] = protoJSONDebugPayload(intent.UserMessage)
	}
	if intent.RequestContext != nil {
		payload["request_context"] = protoJSONDebugPayload(intent.RequestContext)
	}
	if strings.TrimSpace(intent.IgnoredReason) != "" {
		payload["ignored_reason"] = strings.TrimSpace(intent.IgnoredReason)
		payload["ignored_empty_resume"] = strings.TrimSpace(intent.IgnoredReason) == "empty_resume_without_pending_continuation"
	}
	if intent.ExecClientMessage != nil {
		payload["exec_client_message"] = protoJSONDebugPayload(intent.ExecClientMessage)
	}
	if intent.ExecClientControlMessage != nil {
		payload["exec_client_control_message"] = protoJSONDebugPayload(intent.ExecClientControlMessage)
	}
	if intent.InteractionResponse != nil {
		payload["interaction_response"] = protoJSONDebugPayload(intent.InteractionResponse)
	}
	if intent.KVClientMessage != nil {
		payload["kv_client_message"] = protoJSONDebugPayload(intent.KVClientMessage)
	}
	return payload
}

func conversationActionKind(action *agentv1.ConversationAction) string {
	if action == nil {
		return ""
	}
	switch action.GetAction().(type) {
	case *agentv1.ConversationAction_UserMessageAction:
		return "user_message_action"
	case *agentv1.ConversationAction_ResumeAction:
		return "resume_action"
	case *agentv1.ConversationAction_CancelAction:
		return "cancel_action"
	case *agentv1.ConversationAction_SummarizeAction:
		return "summarize_action"
	case *agentv1.ConversationAction_ShellCommandAction:
		return "shell_command_action"
	case *agentv1.ConversationAction_StartPlanAction:
		return "start_plan_action"
	case *agentv1.ConversationAction_ExecutePlanAction:
		return "execute_plan_action"
	case *agentv1.ConversationAction_AsyncAskQuestionCompletionAction:
		return "async_ask_question_completion_action"
	case *agentv1.ConversationAction_CancelSubagentAction:
		return "cancel_subagent_action"
	case *agentv1.ConversationAction_BackgroundTaskCompletionAction:
		return "background_task_completion_action"
	case *agentv1.ConversationAction_BackgroundShellAction:
		return "background_shell_action"
	case *agentv1.ConversationAction_BackgroundSubagentAction:
		return "background_subagent_action"
	default:
		return fmt.Sprintf("%T", action.GetAction())
	}
}
