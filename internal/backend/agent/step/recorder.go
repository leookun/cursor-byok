// recorder.go 实现 pending assistant 输出记录的解析与构造。
package step

import (
	"encoding/json"
	"strings"

	"cursor/internal/backend/agent/core"
)

const (
	// defaultTextPreviewLimit 表示文本摘要允许保留的最大 rune 数。
	defaultTextPreviewLimit = 120
)

// assistantMessage 表示 `pending_tool_calls` 中常见的 assistant message 结构。
type assistantMessage struct {
	// ID 是 message 级别的标识，当前抓包中常见值为 1。
	ID string `json:"id,omitempty"`
	// Role 是当前 message 的角色，当前常见值为 assistant。
	Role string `json:"role,omitempty"`
	// Content 保存该 message 的内容块列表。
	Content []assistantContent `json:"content,omitempty"`
}

// assistantContent 表示 assistant message 内的单个内容块。
type assistantContent struct {
	// Type 表示内容块类型，例如 text、reasoning 或 tool-call。
	Type string `json:"type,omitempty"`
	// Text 保存文本内容块文本。
	Text string `json:"text,omitempty"`
	// ToolCallID 保存工具调用标识。
	ToolCallID string `json:"toolCallId,omitempty"`
	// ToolName 保存工具名称。
	ToolName string `json:"toolName,omitempty"`
	// Args 保存工具调用参数原文。
	Args json.RawMessage `json:"args,omitempty"`
	// Result 保存工具调用结果原文。
	Result json.RawMessage `json:"result,omitempty"`
}

// Recorder 负责解析与构造 pending assistant 输出记录。
type Recorder struct {
}

// StepRecorder 定义运行时依赖的 step 记录接口。
type StepRecorder interface {
	// ParsePendingAssistantOutputs 解析一组原始 pending assistant 输出记录。
	ParsePendingAssistantOutputs(rawValues []string) []runtimecore.PendingAssistantOutput
	// ParsePendingAssistantOutput 解析单条原始 assistant 输出记录。
	ParsePendingAssistantOutput(raw string) runtimecore.PendingAssistantOutput
	// BuildTextAssistantOutput 构造一条只包含文本的 assistant 输出记录。
	BuildTextAssistantOutput(text string) (string, runtimecore.PendingAssistantOutput, error)
	// StartAssistantOutput 创建一个新的 assistant 输出构造器。
	StartAssistantOutput() *AssistantOutputBuilder
}

// NewRecorder 创建 assistant 输出记录整理器。
func NewRecorder() *Recorder {
	return &Recorder{}
}

// AssistantOutputBuilder 表示一条 assistant 输出记录的构造器。
type AssistantOutputBuilder struct {
	// message 保存当前正在构造的原始 assistant message。
	message assistantMessage
}

// ParsePendingAssistantOutputs 解析一组原始 `pending_tool_calls` 字符串。
func (recorder *Recorder) ParsePendingAssistantOutputs(rawValues []string) []runtimecore.PendingAssistantOutput {
	if len(rawValues) == 0 {
		return nil
	}

	outputs := make([]runtimecore.PendingAssistantOutput, 0, len(rawValues))
	for _, raw := range rawValues {
		outputs = append(outputs, recorder.ParsePendingAssistantOutput(raw))
	}
	return outputs
}

// ParsePendingAssistantOutput 解析单条原始 assistant 输出记录。
func (recorder *Recorder) ParsePendingAssistantOutput(raw string) runtimecore.PendingAssistantOutput {
	output := runtimecore.PendingAssistantOutput{
		RawMessage: strings.TrimSpace(raw),
	}
	if output.RawMessage == "" {
		return output
	}

	var message assistantMessage
	if err := json.Unmarshal([]byte(output.RawMessage), &message); err != nil {
		output.TextPreview = truncateText(output.RawMessage, defaultTextPreviewLimit)
		return output
	}

	output.Role = strings.TrimSpace(message.Role)
	output.ContentKinds = make([]string, 0, len(message.Content))
	output.ToolCallIDs = make([]string, 0, len(message.Content))
	output.ToolNames = make([]string, 0, len(message.Content))

	textParts := make([]string, 0, len(message.Content))
	for _, part := range message.Content {
		kind := strings.TrimSpace(part.Type)
		if kind == "" {
			kind = "unknown"
		}
		output.ContentKinds = append(output.ContentKinds, kind)

		if trimmedToolCallID := strings.TrimSpace(part.ToolCallID); trimmedToolCallID != "" {
			output.ToolCallIDs = append(output.ToolCallIDs, trimmedToolCallID)
		}
		if trimmedToolName := strings.TrimSpace(part.ToolName); trimmedToolName != "" {
			output.ToolNames = append(output.ToolNames, trimmedToolName)
		}
		if trimmedText := strings.TrimSpace(part.Text); trimmedText != "" {
			textParts = append(textParts, trimmedText)
		}
	}

	output.TextPreview = truncateText(strings.Join(textParts, "\n"), defaultTextPreviewLimit)
	return output
}

// BuildTextAssistantOutput 构造一条只包含文本的 assistant 输出记录。
func (recorder *Recorder) BuildTextAssistantOutput(text string) (string, runtimecore.PendingAssistantOutput, error) {
	message := assistantMessage{
		ID:   "1",
		Role: "assistant",
		Content: []assistantContent{
			{
				Type: "text",
				Text: text,
			},
		},
	}

	payload, err := json.Marshal(message)
	if err != nil {
		return "", runtimecore.PendingAssistantOutput{}, err
	}

	raw := string(payload)
	return raw, recorder.ParsePendingAssistantOutput(raw), nil
}

// StartAssistantOutput 创建一个新的 assistant 输出构造器。
func (recorder *Recorder) StartAssistantOutput() *AssistantOutputBuilder {
	return &AssistantOutputBuilder{
		message: assistantMessage{
			ID:      "1",
			Role:    "assistant",
			Content: make([]assistantContent, 0, 4),
		},
	}
}

// AppendTextDelta 追加一段文本内容。
func (builder *AssistantOutputBuilder) AppendTextDelta(text string) {
	if builder == nil {
		return
	}
	builder.message.Content = append(builder.message.Content, assistantContent{
		Type: "text",
		Text: text,
	})
}

// AppendReasoningDelta 追加一段推理内容，供 reasoning 模型在续跑时回放。
func (builder *AssistantOutputBuilder) AppendReasoningDelta(text string) {
	if builder == nil {
		return
	}
	builder.message.Content = append(builder.message.Content, assistantContent{
		Type: "reasoning",
		Text: text,
	})
}

// OpenToolCall 追加一个尚未完成的工具调用块。
func (builder *AssistantOutputBuilder) OpenToolCall(toolCall runtimecore.ToolInvocation) {
	if builder == nil {
		return
	}
	builder.message.Content = append(builder.message.Content, assistantContent{
		Type:       "tool-call",
		ToolCallID: strings.TrimSpace(toolCall.CallID),
		ToolName:   strings.TrimSpace(toolCall.ToolName),
		Args:       append(json.RawMessage(nil), toolCall.ArgsJSON...),
	})
}

// CompleteToolCall 为指定工具调用补充结果内容。
func (builder *AssistantOutputBuilder) CompleteToolCall(toolCallID string, resultJSON []byte) {
	if builder == nil {
		return
	}
	for index := range builder.message.Content {
		if builder.message.Content[index].Type != "tool-call" {
			continue
		}
		if strings.TrimSpace(builder.message.Content[index].ToolCallID) != strings.TrimSpace(toolCallID) {
			continue
		}
		builder.message.Content[index].Result = append(json.RawMessage(nil), resultJSON...)
		return
	}
}

// SnapshotRaw 输出当前 builder 的原始 JSON 和解析结果。
func (builder *AssistantOutputBuilder) SnapshotRaw(recorder *Recorder) (string, runtimecore.PendingAssistantOutput, error) {
	if builder == nil {
		return "", runtimecore.PendingAssistantOutput{}, nil
	}
	payload, err := json.Marshal(builder.message)
	if err != nil {
		return "", runtimecore.PendingAssistantOutput{}, err
	}
	raw := string(payload)
	if recorder == nil {
		recorder = NewRecorder()
	}
	return raw, recorder.ParsePendingAssistantOutput(raw), nil
}

// truncateText 按 rune 数截断文本，避免在多字节字符中间截断。
func truncateText(text string, maxRunes int) string {
	trimmed := strings.TrimSpace(text)
	if maxRunes <= 0 || trimmed == "" {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return trimmed
	}
	return string(runes[:maxRunes]) + "..."
}
