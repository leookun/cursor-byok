package modeladapter

import (
	"encoding/json"
	"strings"
	"time"
)

// recordLLMRequestArtifact 记录一次模型调用的原始请求工件。
func recordLLMRequestArtifact(req StreamRequest, provider string, model string, method string, url string, body any) {
	if req.Observer == nil {
		return
	}
	path, err := req.Observer.RecordLLMRequest(req.RequestID, req.RunID, req.ModelCallID, map[string]any{
		"request_id":                     req.RequestID,
		"run_id":                         req.RunID,
		"model_call_id":                  req.ModelCallID,
		"provider":                       strings.TrimSpace(provider),
		"openai_endpoint":                strings.TrimSpace(req.OpenAIEndpoint),
		"model":                          firstNonEmptyString(model, req.ModelID),
		"runtime_model_id":               strings.TrimSpace(req.ModelID),
		"resolved_channel_id":            strings.TrimSpace(req.ResolvedChannelID),
		"resolved_channel_name":          strings.TrimSpace(req.ResolvedChannelName),
		"resolved_context_window_tokens": req.ResolvedContextWindowTokens,
		"url":                            url,
		"method":                         method,
		"body":                           body,
		"request_knobs":                  req.RequestKnobs,
		"compile_summary":                req.CompileSummary,
		"stable_message_count":           req.StableMessageCount,
		"tools_summary":                  summarizeTools(req.Tools),
		"messages_summary":               summarizeMessages(req.Messages),
	})
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.RequestPath = path
	}
}

// buildLLMSummaryPayload 生成 LLM 调用摘要工件内容。
func buildLLMSummaryPayload(
	req StreamRequest,
	provider string,
	model string,
	startedAt time.Time,
	firstEventAt time.Time,
	finishedAt time.Time,
	finishReason string,
	inputTokens int64,
	outputTokens int64,
	cacheReadTokens int64,
	cacheWriteTokens int64,
	err error,
) map[string]any {
	finished := finishedAt
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	promptTokensTotal := inputTokens + cacheReadTokens + cacheWriteTokens
	requestTokensTotal := promptTokensTotal + outputTokens
	return map[string]any{
		"provider":             strings.TrimSpace(provider),
		"model":                strings.TrimSpace(model),
		"started_at":           normalizeModelArtifactTime(startedAt),
		"first_event_at":       normalizeModelArtifactTime(firstEventAt),
		"finished_at":          normalizeModelArtifactTime(finished),
		"finish_reason":        strings.TrimSpace(finishReason),
		"input_tokens":         inputTokens,
		"output_tokens":        outputTokens,
		"cache_read_tokens":    cacheReadTokens,
		"cache_write_tokens":   cacheWriteTokens,
		"prompt_tokens_total":  promptTokensTotal,
		"request_tokens_total": requestTokensTotal,
		"error":                summarizeModelArtifactError(err),
		"ttft_ms":              computeTTFTMS(startedAt, firstEventAt),
		"duration_ms":          computeDurationMS(startedAt, finished),
	}
}

// appendLLMResponseArtifact 追加模型调用的原始响应文本。
func appendLLMResponseArtifact(req StreamRequest, chunk string) (string, error) {
	if req.Observer == nil {
		return "", nil
	}
	path, err := req.Observer.AppendLLMResponseChunk(req.RequestID, req.RunID, req.ModelCallID, chunk)
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.ResponsePath = path
	}
	return path, err
}

// recordLLMSummaryArtifact 记录模型调用摘要。
func recordLLMSummaryArtifact(req StreamRequest, payload map[string]any) {
	if req.Observer == nil {
		return
	}
	path, err := req.Observer.RecordLLMSummary(req.RequestID, req.RunID, req.ModelCallID, payload)
	if err == nil && req.ArtifactPaths != nil {
		req.ArtifactPaths.SummaryPath = path
	}
}

// summarizeTools 生成工具列表摘要。
func summarizeTools(items []json.RawMessage) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		var wrapper struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(item, &wrapper); err != nil {
			continue
		}
		if strings.TrimSpace(wrapper.Function.Name) != "" {
			result = append(result, strings.TrimSpace(wrapper.Function.Name))
		}
	}
	return result
}

// summarizeMessages 生成消息列表摘要。
func summarizeMessages(items []Message) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		content := truncateArtifactText(item.Content, 120)
		imageCount := 0
		for _, part := range item.ContentParts {
			if normalizeContentPartType(part.Type) == contentPartTypeImage {
				imageCount++
			}
		}
		result = append(result, map[string]any{
			"role":            item.Role,
			"content_preview": content,
			"content_length":  len([]rune(content)),
			"image_count":     imageCount,
			"tool_call_count": len(item.ToolCalls),
			"tool_call_id":    item.ToolCallID,
			"name":            item.Name,
		})
	}
	return result
}

func truncateArtifactText(text string, maxRunes int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return trimmed
	}
	return string(runes[:maxRunes]) + "..."
}

// normalizeModelArtifactTime 把时间格式化为 RFC3339Nano。
func normalizeModelArtifactTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// computeTTFTMS 计算首事件耗时。
func computeTTFTMS(startedAt time.Time, firstEventAt time.Time) int64 {
	if startedAt.IsZero() || firstEventAt.IsZero() {
		return 0
	}
	value := firstEventAt.Sub(startedAt).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

// computeDurationMS 计算调用总耗时。
func computeDurationMS(startedAt time.Time, finishedAt time.Time) int64 {
	if startedAt.IsZero() || finishedAt.IsZero() {
		return 0
	}
	value := finishedAt.Sub(startedAt).Milliseconds()
	if value < 0 {
		return 0
	}
	return value
}

// summarizeModelArtifactError 返回可安全落盘的错误文本。
func summarizeModelArtifactError(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

// firstNonEmptyString 返回第一个非空字符串。
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
