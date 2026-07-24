// openai.go 实现 OpenAI 兼容流式适配器。
package modeladapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/modelchannel"
	"cursor/internal/netproxy"
)

// OpenAIAdapter 实现 OpenAI 兼容流式请求。
type OpenAIAdapter struct {
	// client 负责发送 HTTP 请求。
	client *http.Client
}

type openAIRequestBody struct {
	Model           string            `json:"model"`
	Tools           []json.RawMessage `json:"tools,omitempty"`
	Messages        []map[string]any  `json:"messages"`
	Stream          bool              `json:"stream"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	StreamOptions   map[string]any    `json:"stream_options"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	PromptCacheKey  string            `json:"prompt_cache_key,omitempty"`
}

type openAIResponsesRequestBody struct {
	Model           string                    `json:"model"`
	Instructions    string                    `json:"instructions,omitempty"`
	Input           []map[string]any          `json:"input"`
	Tools           []map[string]any          `json:"tools,omitempty"`
	Stream          bool                      `json:"stream"`
	MaxOutputTokens int                       `json:"max_output_tokens,omitempty"`
	Reasoning       *openAIResponsesReasoning `json:"reasoning,omitempty"`
	Include         []string                  `json:"include,omitempty"`
	PromptCacheKey  string                    `json:"prompt_cache_key,omitempty"`
	Store           bool                      `json:"store"`
}

type openAIResponsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type openAIToolAccumulator struct {
	CallID                 string
	Name                   string
	Args                   strings.Builder
	LastEmittedPath        string
	LastStreamContent      string
	LastCreatePlanSnapshot string
	ProviderItemID         string
	ProviderCallID         string
	ProviderStatus         string
}

type openAIImageGenerationAccumulator struct {
	CallID         string
	ImageData      string
	OutputFormat   string
	ProviderItemID string
	ProviderStatus string
	StartedEmitted bool
}

const (
	openAIThinkOpenTag       = "<think>"
	openAIThinkCloseTag      = "</think>"
	openAIStreamMaxTokenSize = 64 * 1024 * 1024
)

type openAIContentPartKind string

const (
	openAIContentPartText              openAIContentPartKind = "text"
	openAIContentPartReasoning         openAIContentPartKind = "reasoning"
	openAIContentPartThinkingCompleted openAIContentPartKind = "thinking_completed"
)

type openAIContentPart struct {
	Kind openAIContentPartKind
	Text string
}

// openAIThinkTagParser 负责把某些 OpenAI 兼容 provider 放进 content 的 <think> 标签拆回 reasoning 流。
type openAIThinkTagParser struct {
	carry   string
	inThink bool
}

func (parser *openAIThinkTagParser) Consume(text string) []openAIContentPart {
	if parser == nil || text == "" {
		return nil
	}
	input := parser.carry + text
	parser.carry = ""
	parts := make([]openAIContentPart, 0, 4)
	for input != "" {
		if parser.inThink {
			closeIndex := strings.Index(input, openAIThinkCloseTag)
			if closeIndex >= 0 {
				if closeIndex > 0 {
					parts = append(parts, openAIContentPart{
						Kind: openAIContentPartReasoning,
						Text: input[:closeIndex],
					})
				}
				parts = append(parts, openAIContentPart{Kind: openAIContentPartThinkingCompleted})
				parser.inThink = false
				input = input[closeIndex+len(openAIThinkCloseTag):]
				continue
			}
			carryLen := trailingTagPrefixLength(input, openAIThinkCloseTag)
			if emitText := input[:len(input)-carryLen]; emitText != "" {
				parts = append(parts, openAIContentPart{
					Kind: openAIContentPartReasoning,
					Text: emitText,
				})
			}
			parser.carry = input[len(input)-carryLen:]
			break
		}

		openIndex := strings.Index(input, openAIThinkOpenTag)
		if openIndex >= 0 {
			if openIndex > 0 {
				parts = append(parts, openAIContentPart{
					Kind: openAIContentPartText,
					Text: input[:openIndex],
				})
			}
			parser.inThink = true
			input = input[openIndex+len(openAIThinkOpenTag):]
			continue
		}
		carryLen := trailingTagPrefixLength(input, openAIThinkOpenTag)
		if emitText := input[:len(input)-carryLen]; emitText != "" {
			parts = append(parts, openAIContentPart{
				Kind: openAIContentPartText,
				Text: emitText,
			})
		}
		parser.carry = input[len(input)-carryLen:]
		break
	}
	return parts
}

func (parser *openAIThinkTagParser) Flush() []openAIContentPart {
	if parser == nil || parser.carry == "" {
		return nil
	}
	kind := openAIContentPartText
	if parser.inThink {
		kind = openAIContentPartReasoning
	}
	text := parser.carry
	parser.carry = ""
	return []openAIContentPart{{
		Kind: kind,
		Text: text,
	}}
}

// NewOpenAIAdapter 创建一个 OpenAI 兼容适配器。
func NewOpenAIAdapter() *OpenAIAdapter {
	return &OpenAIAdapter{
		client: netproxy.NewHTTPClient(0),
	}
}

func openAIModelSupportsPromptCacheKey(modelID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "gpt")
}

func openAIPromptCacheKey(req StreamRequest, modelID string) string {
	if !openAIModelSupportsPromptCacheKey(modelID) {
		return ""
	}
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID == "" {
		return ""
	}
	return "cursor:" + conversationID
}

func applyOpenAIPromptCacheKeyOverride(body map[string]any, req StreamRequest, modelID string) {
	if len(body) == 0 {
		return
	}
	if !openAIModelSupportsPromptCacheKey(modelID) {
		delete(body, "prompt_cache_key")
		return
	}
	if _, ok := body["prompt_cache_key"]; ok {
		return
	}
	if key := openAIPromptCacheKey(req, modelID); key != "" {
		body["prompt_cache_key"] = key
	}
}

func shouldExposeOpenAIResponsesImageGeneration(req StreamRequest, tools []map[string]any) bool {
	if !openAIResponsesToolNamePresent(tools, "GenerateImage") {
		return false
	}
	return openAITextLooksLikeImageGenerationRequest(openAILatestUserRequestText(req))
}

func ensureOpenAIResponsesImageGenerationTool(tools []map[string]any) []map[string]any {
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["type"])) == "image_generation" {
			return tools
		}
	}
	return append(tools, map[string]any{"type": "image_generation"})
}

func openAIResponsesToolNamePresent(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["name"])) == name {
			return true
		}
		if functionShape, ok := tool["function"].(map[string]any); ok {
			if strings.TrimSpace(fmt.Sprint(functionShape["name"])) == name {
				return true
			}
		}
	}
	return false
}

func openAILatestUserRequestText(req StreamRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		if strings.TrimSpace(strings.ToLower(message.Role)) != "user" {
			continue
		}
		text := message.Content
		if strings.TrimSpace(text) == "" && len(message.ContentParts) > 0 {
			text = collapseTextContentParts(message.ContentParts)
		}
		if tagged := textBetweenOpenAITag(text, "current_user_request"); tagged != "" {
			return tagged
		}
		if tagged := textBetweenOpenAITag(text, "user_query"); tagged != "" {
			return tagged
		}
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func textBetweenOpenAITag(text string, tag string) string {
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.LastIndex(text, openTag)
	if start < 0 {
		return ""
	}
	start += len(openTag)
	end := strings.Index(text[start:], closeTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func openAITextLooksLikeImageGenerationRequest(text string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return false
	}
	imageTerms := []string{
		"图片", "图像", "照片", "相片", "人像", "头像", "插画", "海报", "壁纸", "封面", "摄影", "真实摄影",
		"image", "picture", "photo", "portrait", "illustration", "poster", "wallpaper", "cover", "photorealistic",
	}
	for _, term := range imageTerms {
		if strings.Contains(trimmed, term) {
			return true
		}
	}
	return false
}

func OpenAIEndpointURL(baseURL string, endpoint string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	normalizedEndpoint := strings.TrimSpace(endpoint)
	if normalizedEndpoint == "" {
		normalizedEndpoint = modelchannel.OpenAIEndpointResponses
	}
	if !strings.HasPrefix(normalizedEndpoint, "/") {
		normalizedEndpoint = "/" + normalizedEndpoint
	}
	// 规则0：自定义路径模式
	// - baseURL 已含 endpoint 后缀（/chat/completions 或 /responses）→ 直接用 base
	// - 否则追加 /chat/completions（默认协议形态，覆盖 Z.AI /v4 等场景）
	if normalizedEndpoint == modelchannel.OpenAIEndpointCustom {
		if OpenAIEndpointFromBaseURL(base) != "" {
			return base
		}
		return base + "/chat/completions"
	}
	// 规则1：baseURL 已含 endpoint 后缀 → 直接用 base
	if OpenAIEndpointFromBaseURL(base) != "" {
		return base
	}
	// 规则2：baseURL 以 /vN 结尾时，剥离 endpoint 的版本前缀（/v1/、/v2/ 等）
	// 这样 base=.../v4 + endpoint=/v1/chat/completions → .../v4/chat/completions
	if _, ok := trailingVersionSegment(base); ok {
		if rest, stripped := stripEndpointVersionPrefix(normalizedEndpoint); stripped {
			return base + rest
		}
	}
	// 规则3：兜底原样拼接
	return base + normalizedEndpoint
}

// trailingVersionSegment 检测 URL 末尾是否以 /vN 形式结尾（N 为数字），
// 返回版本段（如 "v4"）和是否匹配。用于通用版本段去重。
func trailingVersionSegment(base string) (string, bool) {
	idx := strings.LastIndex(base, "/")
	if idx < 0 {
		return "", false
	}
	seg := base[idx+1:]
	if len(seg) < 2 || seg[0] != 'v' {
		return "", false
	}
	for i := 1; i < len(seg); i++ {
		if seg[i] < '0' || seg[i] > '9' {
			return "", false
		}
	}
	return seg, true
}

// stripEndpointVersionPrefix 剥离 endpoint 路径开头的版本段前缀（/vN/），
// 返回剩余路径和是否成功剥离。
// /v1/chat/completions → ("/chat/completions", true)
// /chat/completions    → ("", false)
func stripEndpointVersionPrefix(endpoint string) (string, bool) {
	if len(endpoint) < 4 || endpoint[0] != '/' || endpoint[1] != 'v' {
		return "", false
	}
	i := 2
	for i < len(endpoint) && endpoint[i] >= '0' && endpoint[i] <= '9' {
		i++
	}
	if i == 2 || i >= len(endpoint) || endpoint[i] != '/' {
		return "", false
	}
	return endpoint[i:], true
}

func ResolveOpenAIEndpoint(baseURL string, endpoint string) string {
	if endpointFromURL := OpenAIEndpointFromBaseURL(baseURL); endpointFromURL != "" {
		return endpointFromURL
	}
	return modelchannel.NormalizeOpenAIEndpoint("openai", endpoint)
}

func OpenAIEndpointFromBaseURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(strings.ToLower(baseURL)), "/")
	switch {
	case strings.HasSuffix(base, "/responses"):
		return modelchannel.OpenAIEndpointResponses
	case strings.HasSuffix(base, "/chat/completions"):
		return modelchannel.OpenAIEndpointChatCompletions
	default:
		return ""
	}
}

func ProviderURLHasEndpoint(baseURL string, endpoints ...string) bool {
	base := strings.TrimRight(strings.TrimSpace(strings.ToLower(baseURL)), "/")
	if base == "" {
		return false
	}
	for _, endpoint := range endpoints {
		normalizedEndpoint := strings.TrimRight(strings.TrimSpace(strings.ToLower(endpoint)), "/")
		if normalizedEndpoint == "" {
			continue
		}
		if !strings.HasPrefix(normalizedEndpoint, "/") {
			normalizedEndpoint = "/" + normalizedEndpoint
		}
		if strings.HasSuffix(base, normalizedEndpoint) {
			return true
		}
	}
	return false
}

// Stream 发送 OpenAI 兼容流式请求，并解析统一模型事件。
func (adapter *OpenAIAdapter) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("openai base url is empty")
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return fmt.Errorf("openai api key is empty")
	}
	modelID := strings.TrimSpace(req.ProviderModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(req.ModelID)
	}
	if modelID == "" {
		return fmt.Errorf("openai model id is empty")
	}

	endpoint := ResolveOpenAIEndpoint(baseURL, req.OpenAIEndpoint)
	if endpoint == "" {
		return fmt.Errorf("openai endpoint is unsupported: %s", strings.TrimSpace(req.OpenAIEndpoint))
	}
	req.OpenAIEndpoint = endpoint
	if req.RequestKnobs != nil {
		req.RequestKnobs["openai_endpoint"] = endpoint
		if modelchannel.OpenAIEndpointShape(endpoint) == "responses" {
			req.RequestKnobs["max_output_tokens"] = req.MaxTokens
		}
	}
	if modelchannel.OpenAIEndpointShape(endpoint) == "responses" {
		return adapter.streamResponses(ctx, req, baseURL, apiKey, modelID, sink)
	}
	return adapter.streamChatCompletions(ctx, req, baseURL, apiKey, modelID, sink)
}

func (adapter *OpenAIAdapter) streamChatCompletions(ctx context.Context, req StreamRequest, baseURL string, apiKey string, modelID string, sink func(ModelEvent) error) error {
	startedAt := time.Now().UTC()
	finishedAt := time.Time{}
	overrideBody := cloneRequestBodyOverride(req.RequestBodyOverride)
	var body any = overrideBody
	if len(overrideBody) == 0 {
		normalizedMessages, err := normalizeOpenAIProviderMessages(req.Messages, strings.TrimSpace(req.ReasoningEffort) != "")
		if err != nil {
			finishedAt = time.Now().UTC()
			recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
			return err
		}
		requestBody := openAIRequestBody{
			Model:         modelID,
			Messages:      normalizedMessages,
			Stream:        true,
			StreamOptions: map[string]any{"include_usage": true},
		}
		if shouldSendOpenAIMaxOutputTokens(modelID) {
			requestBody.MaxTokens = req.MaxTokens
		}
		if key := openAIPromptCacheKey(req, modelID); key != "" {
			requestBody.PromptCacheKey = key
		}
		if len(req.Tools) > 0 {
			requestBody.Tools = req.Tools
		}
		if strings.TrimSpace(req.ReasoningEffort) != "" {
			requestBody.ReasoningEffort = req.ReasoningEffort
		}
		body = requestBody
	} else {
		applyOpenAIPromptCacheKeyOverride(overrideBody, req, modelID)
	}
	bodyMap, err := requestBodyToMap(body)
	if err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	applyOpenAIThinkingDisable(bodyMap, req, baseURL, modelID, req.OpenAIEndpoint)
	if err := ApplyOpenAIExtraParams(bodyMap, req.OpenAIExtraParamsEnabled, req.OpenAIExtraParamsJSON); err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	body = bodyMap
	requestURL := OpenAIEndpointURL(baseURL, req.OpenAIEndpoint)
	recordLLMRequestArtifact(req, "openai", modelID, "POST", requestURL, body)

	payload, err := json.Marshal(body)
	if err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	streamCtx, streamIdle := newProviderStreamIdleWatchdog(ctx, req.ProviderStreamIdleTimeout)
	defer streamIdle.Stop()

	buildHTTPRequest := func(requestContext context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(requestContext, http.MethodPost, requestURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", ClaudeCodeUserAgent)
		if err := ApplyCustomHeaders(httpReq, req.CustomHeadersEnabled, req.CustomHeadersJSON); err != nil {
			return nil, err
		}
		return httpReq, nil
	}

	resp, err := doProviderRequestWithRetry(streamCtx, adapter.client, "openai", req.RequestID, req.ModelCallID, buildHTTPRequest)
	if err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			err = idleErr
		}
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	streamIdle.AttachBody(resp.Body)
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = buildHTTPStatusError("openai adapter", resp)
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	type openAIToolCallDelta struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type openAIChunk struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Error     *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
		Choices []struct {
			Delta struct {
				Content          string                `json:"content"`
				ReasoningContent string                `json:"reasoning_content"`
				ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Model string `json:"model"`
		Usage *struct {
			PromptTokens        int64 `json:"prompt_tokens"`
			CompletionTokens    int64 `json:"completion_tokens"`
			PromptTokensDetails *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details,omitempty"`
		} `json:"usage,omitempty"`
	}

	tools := make(map[int]*openAIToolAccumulator)
	currentModel := modelID
	inputTokens := int64(0)
	outputTokens := int64(0)
	cacheReadTokens := int64(0)
	cacheWriteTokens := int64(0)
	usagePresent := false
	cacheReadPresent := false
	cacheWritePresent := false
	firstEventAt := time.Time{}
	finishReason := ""
	turnFinishedPending := false
	thinkingStarted := time.Time{}
	thinkingActive := false
	thinkParser := &openAIThinkTagParser{}
	flushThinkingCompleted := func() error {
		if !thinkingActive {
			return nil
		}
		duration := int32(time.Since(thinkingStarted).Milliseconds())
		if duration < 0 {
			duration = 0
		}
		if err := sink(ModelEvent{
			Kind:               ModelEventKindThinkingCompleted,
			OccurredAt:         time.Now().UTC(),
			Provider:           "openai",
			Model:              currentModel,
			ThinkingDurationMS: duration,
		}); err != nil {
			return err
		}
		thinkingActive = false
		thinkingStarted = time.Time{}
		return nil
	}
	flushTurnFinished := func() error {
		if !turnFinishedPending {
			return nil
		}
		turnFinishedPending = false
		return sink(ModelEvent{
			Kind:              ModelEventKindTurnFinished,
			OccurredAt:        time.Now().UTC(),
			Provider:          "openai",
			Model:             currentModel,
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			CacheReadTokens:   cacheReadTokens,
			CacheWriteTokens:  cacheWriteTokens,
			UsagePresent:      usagePresent,
			CacheReadPresent:  cacheReadPresent,
			CacheWritePresent: cacheWritePresent,
			FinishReason:      finishReason,
		})
	}
	emitTextDelta := func(text string) error {
		if text == "" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		if err := flushThinkingCompleted(); err != nil {
			return err
		}
		return sink(ModelEvent{
			Kind:       ModelEventKindTextDelta,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			Text:       text,
		})
	}
	emitThinkingDelta := func(reasoning string) error {
		if reasoning == "" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		if !thinkingActive {
			thinkingStarted = time.Now()
			thinkingActive = true
		}
		return sink(ModelEvent{
			Kind:          ModelEventKindThinkingDelta,
			OccurredAt:    time.Now().UTC(),
			Provider:      "openai",
			Model:         currentModel,
			Text:          reasoning,
			ThinkingStyle: agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
		})
	}
	emitTaggedContentParts := func(parts []openAIContentPart) error {
		for _, part := range parts {
			switch part.Kind {
			case openAIContentPartText:
				if err := emitTextDelta(part.Text); err != nil {
					return err
				}
			case openAIContentPartReasoning:
				if err := emitThinkingDelta(part.Text); err != nil {
					return err
				}
			case openAIContentPartThinkingCompleted:
				if err := flushThinkingCompleted(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	flushTaggedContentTail := func() error {
		return emitTaggedContentParts(thinkParser.Flush())
	}
	fail := func(streamErr error) error {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", currentModel, startedAt, firstEventAt, finishedAt, finishReason, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, streamErr))
		return streamErr
	}
	errorFromChunk := func(chunk openAIChunk) error {
		finishReason = "error"
		if chunk.Error != nil {
			parts := make([]string, 0, 4)
			if value := strings.TrimSpace(chunk.Error.Type); value != "" {
				parts = append(parts, "type="+value)
			}
			if value := strings.TrimSpace(chunk.Error.Code); value != "" {
				parts = append(parts, "code="+value)
			}
			if value := strings.TrimSpace(chunk.RequestID); value != "" {
				parts = append(parts, "request_id="+value)
			}
			if message := strings.TrimSpace(chunk.Error.Message); message != "" {
				if len(parts) > 0 {
					return fmt.Errorf("openai chat stream error %s: %s", strings.Join(parts, " "), message)
				}
				return fmt.Errorf("openai chat stream error: %s", message)
			}
			if len(parts) > 0 {
				return fmt.Errorf("openai chat stream error %s", strings.Join(parts, " "))
			}
		}
		return fmt.Errorf("openai chat stream error")
	}
	applyUsage := func(usage *struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
	}) {
		if usage == nil {
			return
		}
		usagePresent = true
		promptTokens := usage.PromptTokens
		cachedTokens := int64(0)
		if usage.PromptTokensDetails != nil {
			cacheReadPresent = true
			cachedTokens = usage.PromptTokensDetails.CachedTokens
		}
		if promptTokens < 0 {
			promptTokens = 0
		}
		if cachedTokens < 0 {
			cachedTokens = 0
		}
		if cachedTokens > promptTokens {
			cachedTokens = promptTokens
		}
		inputTokens = promptTokens - cachedTokens
		outputTokens = maxInt64(usage.CompletionTokens, 0)
		cacheReadTokens = cachedTokens
		cacheWriteTokens = 0
		cacheWritePresent = true
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), openAIStreamMaxTokenSize)
	for scanner.Scan() {
		rawLine := scanner.Text()
		_, _ = appendLLMResponseArtifact(req, redactOpenAIStreamArtifactLine(rawLine)+"\n")
		line := strings.TrimSpace(rawLine)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now().UTC()
		}
		payloadLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payloadLine == "[DONE]" {
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
			if err := flushTurnFinished(); err != nil {
				return fail(err)
			}
			break
		}

		var chunk openAIChunk
		if err := json.Unmarshal([]byte(payloadLine), &chunk); err != nil {
			return fail(err)
		}
		if strings.TrimSpace(chunk.Type) == "error" || chunk.Error != nil {
			return fail(errorFromChunk(chunk))
		}
		if len(chunk.Choices) == 0 {
			if strings.TrimSpace(chunk.Model) != "" {
				currentModel = strings.TrimSpace(chunk.Model)
			}
			applyUsage(chunk.Usage)
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
			if err := flushTurnFinished(); err != nil {
				return fail(err)
			}
			continue
		}
		choice := chunk.Choices[0]
		if strings.TrimSpace(chunk.Model) != "" {
			currentModel = strings.TrimSpace(chunk.Model)
		}
		applyUsage(chunk.Usage)

		if text := choice.Delta.Content; text != "" {
			if err := emitTaggedContentParts(thinkParser.Consume(text)); err != nil {
				return fail(err)
			}
		}
		if reasoning := choice.Delta.ReasoningContent; reasoning != "" {
			if err := emitThinkingDelta(reasoning); err != nil {
				return fail(err)
			}
		}

		if len(choice.Delta.ToolCalls) > 0 && choice.Delta.Content == "" && choice.Delta.ReasoningContent == "" {
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
		}
		for _, item := range choice.Delta.ToolCalls {
			streamIdle.MarkEffectiveContent()
			accumulator, ok := tools[item.Index]
			if !ok {
				accumulator = &openAIToolAccumulator{}
				tools[item.Index] = accumulator
			}
			if strings.TrimSpace(item.ID) != "" {
				accumulator.CallID = namespaceToolCallID(req.ModelCallID, item.ID)
			}
			if strings.TrimSpace(item.Function.Name) != "" {
				accumulator.Name = strings.TrimSpace(item.Function.Name)
			}
			argsTextDelta := ""
			if item.Function.Arguments != "" {
				_, _ = accumulator.Args.WriteString(item.Function.Arguments)
				argsTextDelta = item.Function.Arguments
			}
			if argsTextDelta != "" || (strings.TrimSpace(accumulator.Name) == "CreatePlan" && accumulator.Args.Len() > 0) {
				if err := emitOpenAIToolProgress(sink, currentModel, accumulator, argsTextDelta); err != nil {
					return fail(err)
				}
			}
		}

		if choice.FinishReason != nil {
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
			for _, accumulator := range tools {
				if err := sink(ModelEvent{
					Kind:       ModelEventKindToolLikeCompleted,
					OccurredAt: time.Now().UTC(),
					Provider:   "openai",
					Model:      currentModel,
					ToolInvocation: &runtimecore.ToolInvocation{
						CallID:   strings.TrimSpace(accumulator.CallID),
						ToolName: strings.TrimSpace(accumulator.Name),
						ArgsJSON: []byte(accumulator.Args.String()),
					},
				}); err != nil {
					return fail(err)
				}
				streamIdle.MarkEffectiveContent()
			}
			tools = make(map[int]*openAIToolAccumulator)
			finishReason = strings.TrimSpace(*choice.FinishReason)
			turnFinishedPending = true
		}
	}
	for _, accumulator := range tools {
		if err := sink(ModelEvent{
			Kind:       ModelEventKindToolLikeCompleted,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			ToolInvocation: &runtimecore.ToolInvocation{
				CallID:   strings.TrimSpace(accumulator.CallID),
				ToolName: strings.TrimSpace(accumulator.Name),
				ArgsJSON: []byte(accumulator.Args.String()),
			},
		}); err != nil {
			return fail(err)
		}
		streamIdle.MarkEffectiveContent()
	}
	if err := scanner.Err(); err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			return fail(idleErr)
		}
		return fail(err)
	}
	if err := flushTaggedContentTail(); err != nil {
		return fail(err)
	}
	if err := flushThinkingCompleted(); err != nil {
		return fail(err)
	}
	if err := flushTurnFinished(); err != nil {
		return fail(err)
	}
	finishedAt = time.Now().UTC()
	recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", currentModel, startedAt, firstEventAt, finishedAt, finishReason, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, nil))
	return nil
}

func (adapter *OpenAIAdapter) streamResponses(ctx context.Context, req StreamRequest, baseURL string, apiKey string, modelID string, sink func(ModelEvent) error) error {
	startedAt := time.Now().UTC()
	finishedAt := time.Time{}
	overrideBody := cloneRequestBodyOverride(req.RequestBodyOverride)
	var body any = overrideBody
	if len(overrideBody) == 0 {
		instructions, input, err := normalizeOpenAIResponsesInput(req.Messages)
		if err != nil {
			finishedAt = time.Now().UTC()
			recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
			return err
		}
		requestBody := openAIResponsesRequestBody{
			Model:        modelID,
			Instructions: instructions,
			Input:        input,
			Stream:       true,
			Store:        false,
		}
		if shouldSendOpenAIMaxOutputTokens(modelID) {
			requestBody.MaxOutputTokens = req.MaxTokens
		}
		if key := openAIPromptCacheKey(req, modelID); key != "" {
			requestBody.PromptCacheKey = key
		}
		if len(req.Tools) > 0 {
			tools, err := normalizeOpenAIResponsesTools(req.Tools)
			if err != nil {
				finishedAt = time.Now().UTC()
				recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
				return err
			}
			if shouldExposeOpenAIResponsesImageGeneration(req, tools) {
				tools = ensureOpenAIResponsesImageGenerationTool(tools)
				if req.RequestKnobs != nil {
					req.RequestKnobs["openai_responses_image_generation_tool"] = "auto"
				}
			}
			requestBody.Tools = tools
		}
		if effort := strings.TrimSpace(req.ReasoningEffort); effort != "" {
			requestBody.Reasoning = &openAIResponsesReasoning{Effort: effort}
			requestBody.Include = []string{"reasoning.encrypted_content"}
		}
		body = requestBody
	} else {
		applyOpenAIPromptCacheKeyOverride(overrideBody, req, modelID)
	}
	bodyMap, err := requestBodyToMap(body)
	if err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	applyOpenAIThinkingDisable(bodyMap, req, baseURL, modelID, req.OpenAIEndpoint)
	if err := ApplyOpenAIExtraParams(bodyMap, req.OpenAIExtraParamsEnabled, req.OpenAIExtraParamsJSON); err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	body = bodyMap

	requestURL := OpenAIEndpointURL(baseURL, req.OpenAIEndpoint)
	recordLLMRequestArtifact(req, "openai", modelID, "POST", requestURL, body)

	payload, err := json.Marshal(body)
	if err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	streamCtx, streamIdle := newProviderStreamIdleWatchdog(ctx, req.ProviderStreamIdleTimeout)
	defer streamIdle.Stop()

	buildHTTPRequest := func(requestContext context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(requestContext, http.MethodPost, requestURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("User-Agent", ClaudeCodeUserAgent)
		if err := ApplyCustomHeaders(httpReq, req.CustomHeadersEnabled, req.CustomHeadersJSON); err != nil {
			return nil, err
		}
		return httpReq, nil
	}

	resp, err := doProviderRequestWithRetry(streamCtx, adapter.client, "openai", req.RequestID, req.ModelCallID, buildHTTPRequest)
	if err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			err = idleErr
		}
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	streamIdle.AttachBody(resp.Body)
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = buildHTTPStatusError("openai adapter", resp)
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	type openAIResponsesUsage struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails *struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details,omitempty"`
	}
	type openAIResponsesOutputContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type openAIResponsesOutputItem struct {
		ID               string                         `json:"id"`
		Type             string                         `json:"type"`
		Status           string                         `json:"status"`
		CallID           string                         `json:"call_id"`
		Name             string                         `json:"name"`
		Arguments        string                         `json:"arguments"`
		EncryptedContent string                         `json:"encrypted_content"`
		Summary          json.RawMessage                `json:"summary,omitempty"`
		Content          []openAIResponsesOutputContent `json:"content,omitempty"`
	}
	type openAIResponsesResponse struct {
		ID                string                      `json:"id"`
		Model             string                      `json:"model"`
		Status            string                      `json:"status"`
		Output            []openAIResponsesOutputItem `json:"output,omitempty"`
		OutputText        string                      `json:"output_text,omitempty"`
		Usage             *openAIResponsesUsage       `json:"usage,omitempty"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details,omitempty"`
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	}
	type openAIResponsesStreamEvent struct {
		Type            string                     `json:"type"`
		RequestID       string                     `json:"request_id"`
		Delta           string                     `json:"delta"`
		Arguments       string                     `json:"arguments"`
		PartialImageB64 string                     `json:"partial_image_b64"`
		OutputFormat    string                     `json:"output_format"`
		OutputIndex     int                        `json:"output_index"`
		ItemID          string                     `json:"item_id"`
		Item            *openAIResponsesOutputItem `json:"item,omitempty"`
		Response        *openAIResponsesResponse   `json:"response,omitempty"`
		Error           *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error,omitempty"`
	}

	tools := make(map[string]*openAIToolAccumulator)
	completedTools := make(map[string]struct{})
	imageGenerations := make(map[string]*openAIImageGenerationAccumulator)
	completedImageGenerations := make(map[string]struct{})
	currentModel := modelID
	inputTokens := int64(0)
	outputTokens := int64(0)
	cacheReadTokens := int64(0)
	cacheWriteTokens := int64(0)
	usagePresent := false
	cacheReadPresent := false
	cacheWritePresent := false
	firstEventAt := time.Time{}
	finishReason := ""
	turnFinishedPending := false
	emittedToolInvocation := false
	emittedText := false
	thinkingStarted := time.Time{}
	thinkingActive := false
	emittedReasoningSignature := ""
	thinkParser := &openAIThinkTagParser{}
	toolKey := func(itemID string, outputIndex int) string {
		if strings.TrimSpace(itemID) != "" {
			return strings.TrimSpace(itemID)
		}
		return fmt.Sprintf("output:%d", outputIndex)
	}
	effectiveFinishReason := func() string {
		reason := strings.TrimSpace(finishReason)
		if emittedToolInvocation && (reason == "" || reason == "completed") {
			return "tool_calls"
		}
		return reason
	}
	flushThinkingCompleted := func() error {
		if !thinkingActive {
			return nil
		}
		duration := int32(time.Since(thinkingStarted).Milliseconds())
		if duration < 0 {
			duration = 0
		}
		if err := sink(ModelEvent{
			Kind:               ModelEventKindThinkingCompleted,
			OccurredAt:         time.Now().UTC(),
			Provider:           "openai",
			Model:              currentModel,
			ThinkingDurationMS: duration,
		}); err != nil {
			return err
		}
		thinkingActive = false
		thinkingStarted = time.Time{}
		return nil
	}
	flushTurnFinished := func() error {
		if !turnFinishedPending {
			return nil
		}
		turnFinishedPending = false
		return sink(ModelEvent{
			Kind:              ModelEventKindTurnFinished,
			OccurredAt:        time.Now().UTC(),
			Provider:          "openai",
			Model:             currentModel,
			InputTokens:       inputTokens,
			OutputTokens:      outputTokens,
			CacheReadTokens:   cacheReadTokens,
			CacheWriteTokens:  cacheWriteTokens,
			UsagePresent:      usagePresent,
			CacheReadPresent:  cacheReadPresent,
			CacheWritePresent: cacheWritePresent,
			FinishReason:      effectiveFinishReason(),
		})
	}
	emitTextDelta := func(text string) error {
		if text == "" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		if err := flushThinkingCompleted(); err != nil {
			return err
		}
		emittedText = true
		return sink(ModelEvent{
			Kind:       ModelEventKindTextDelta,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			Text:       text,
		})
	}
	emitThinkingDelta := func(reasoning string) error {
		if reasoning == "" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		if !thinkingActive {
			thinkingStarted = time.Now()
			thinkingActive = true
		}
		return sink(ModelEvent{
			Kind:          ModelEventKindThinkingDelta,
			OccurredAt:    time.Now().UTC(),
			Provider:      "openai",
			Model:         currentModel,
			Text:          reasoning,
			ThinkingStyle: agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
		})
	}
	emitTaggedContentParts := func(parts []openAIContentPart) error {
		for _, part := range parts {
			switch part.Kind {
			case openAIContentPartText:
				if err := emitTextDelta(part.Text); err != nil {
					return err
				}
			case openAIContentPartReasoning:
				if err := emitThinkingDelta(part.Text); err != nil {
					return err
				}
			case openAIContentPartThinkingCompleted:
				if err := flushThinkingCompleted(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	flushTaggedContentTail := func() error {
		return emitTaggedContentParts(thinkParser.Flush())
	}
	fail := func(streamErr error) error {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", currentModel, startedAt, firstEventAt, finishedAt, finishReason, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, streamErr))
		return streamErr
	}
	applyUsage := func(usage *openAIResponsesUsage) {
		if usage == nil {
			return
		}
		usagePresent = true
		promptTokens := maxInt64(usage.InputTokens, 0)
		cachedTokens := int64(0)
		if usage.InputTokensDetails != nil {
			cacheReadPresent = true
			cachedTokens = maxInt64(usage.InputTokensDetails.CachedTokens, 0)
		}
		if cachedTokens > promptTokens {
			cachedTokens = promptTokens
		}
		inputTokens = promptTokens - cachedTokens
		outputTokens = maxInt64(usage.OutputTokens, 0)
		cacheReadTokens = cachedTokens
		cacheWriteTokens = 0
		cacheWritePresent = true
	}
	completeTool := func(key string, accumulator *openAIToolAccumulator) error {
		if accumulator == nil {
			return nil
		}
		completionKey := firstNonEmptyString(key, accumulator.CallID)
		if strings.TrimSpace(completionKey) == "" {
			completionKey = accumulator.Name + ":" + accumulator.Args.String()
		}
		if _, ok := completedTools[completionKey]; ok {
			return nil
		}
		if strings.TrimSpace(accumulator.CallID) != "" {
			if _, ok := completedTools[strings.TrimSpace(accumulator.CallID)]; ok {
				return nil
			}
		}
		completedTools[completionKey] = struct{}{}
		if strings.TrimSpace(accumulator.CallID) != "" {
			completedTools[strings.TrimSpace(accumulator.CallID)] = struct{}{}
		}
		emittedToolInvocation = true
		if err := sink(ModelEvent{
			Kind:       ModelEventKindToolLikeCompleted,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			ToolInvocation: &runtimecore.ToolInvocation{
				CallID:         strings.TrimSpace(accumulator.CallID),
				ToolName:       strings.TrimSpace(accumulator.Name),
				ArgsJSON:       []byte(accumulator.Args.String()),
				ProviderItemID: strings.TrimSpace(accumulator.ProviderItemID),
				ProviderCallID: strings.TrimSpace(accumulator.ProviderCallID),
				ProviderStatus: strings.TrimSpace(accumulator.ProviderStatus),
			},
		}); err != nil {
			return err
		}
		streamIdle.MarkEffectiveContent()
		return nil
	}
	rememberImageGenerationItem := func(item openAIResponsesOutputItem, outputIndex int) *openAIImageGenerationAccumulator {
		key := toolKey(item.ID, outputIndex)
		accumulator, ok := imageGenerations[key]
		if !ok {
			accumulator = &openAIImageGenerationAccumulator{}
			imageGenerations[key] = accumulator
		}
		if itemID := strings.TrimSpace(item.ID); itemID != "" {
			accumulator.ProviderItemID = itemID
			accumulator.CallID = namespaceToolCallID(req.ModelCallID, itemID)
		}
		if status := strings.TrimSpace(item.Status); status != "" {
			accumulator.ProviderStatus = status
		}
		if strings.TrimSpace(accumulator.CallID) == "" {
			accumulator.CallID = namespaceToolCallID(req.ModelCallID, key)
		}
		return accumulator
	}
	emitImageGenerationStarted := func(accumulator *openAIImageGenerationAccumulator) error {
		if accumulator == nil || accumulator.StartedEmitted {
			return nil
		}
		callID := strings.TrimSpace(accumulator.CallID)
		if callID == "" {
			return nil
		}
		accumulator.StartedEmitted = true
		return sink(ModelEvent{
			Kind:       ModelEventKindPartialToolCall,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			ToolCallID: callID,
			ToolCall: &agentv1.ToolCall{
				Tool: &agentv1.ToolCall_GenerateImageToolCall{
					GenerateImageToolCall: &agentv1.GenerateImageToolCall{
						Args: &agentv1.GenerateImageArgs{},
					},
				},
			},
		})
	}
	completeImageGeneration := func(key string, accumulator *openAIImageGenerationAccumulator) error {
		if accumulator == nil || strings.TrimSpace(accumulator.ImageData) == "" {
			return nil
		}
		completionKey := firstNonEmptyString(key, accumulator.CallID, accumulator.ProviderItemID)
		if strings.TrimSpace(completionKey) == "" {
			completionKey = accumulator.ImageData
		}
		if _, ok := completedImageGenerations[completionKey]; ok {
			return nil
		}
		if strings.TrimSpace(accumulator.CallID) != "" {
			if _, ok := completedImageGenerations[strings.TrimSpace(accumulator.CallID)]; ok {
				return nil
			}
		}
		completedImageGenerations[completionKey] = struct{}{}
		if strings.TrimSpace(accumulator.CallID) != "" {
			completedImageGenerations[strings.TrimSpace(accumulator.CallID)] = struct{}{}
		}
		argsPayload := map[string]string{"image_data": strings.TrimSpace(accumulator.ImageData)}
		argsJSON, err := json.Marshal(argsPayload)
		if err != nil {
			return err
		}
		emittedToolInvocation = true
		if err := sink(ModelEvent{
			Kind:       ModelEventKindToolLikeCompleted,
			OccurredAt: time.Now().UTC(),
			Provider:   "openai",
			Model:      currentModel,
			ToolInvocation: &runtimecore.ToolInvocation{
				CallID:         strings.TrimSpace(accumulator.CallID),
				ToolName:       "GenerateImage",
				ArgsJSON:       argsJSON,
				ProviderItemID: strings.TrimSpace(accumulator.ProviderItemID),
				ProviderStatus: strings.TrimSpace(accumulator.ProviderStatus),
			},
		}); err != nil {
			return err
		}
		streamIdle.MarkEffectiveContent()
		return nil
	}
	emitReasoningSignature := func(signature string, providerItemID string, providerStatus string, providerSummary json.RawMessage) error {
		trimmedSignature := strings.TrimSpace(signature)
		if trimmedSignature == "" || trimmedSignature == emittedReasoningSignature {
			return nil
		}
		duration := int32(0)
		if thinkingActive {
			duration = int32(time.Since(thinkingStarted).Milliseconds())
			if duration < 0 {
				duration = 0
			}
			thinkingActive = false
			thinkingStarted = time.Time{}
		}
		emittedReasoningSignature = trimmedSignature
		return sink(ModelEvent{
			Kind:                    ModelEventKindThinkingCompleted,
			OccurredAt:              time.Now().UTC(),
			Provider:                "openai",
			Model:                   currentModel,
			ThinkingDurationMS:      duration,
			ThinkingSignature:       trimmedSignature,
			ThinkingSignatureSource: ReasoningSignatureSourceOpenAIResponses,
			ProviderItemID:          strings.TrimSpace(providerItemID),
			ProviderStatus:          strings.TrimSpace(providerStatus),
			ProviderSummary:         cloneRawJSON(providerSummary),
		})
	}
	applyFunctionCallItem := func(item openAIResponsesOutputItem, outputIndex int, complete bool) error {
		if strings.TrimSpace(item.Type) != "function_call" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		key := toolKey(firstNonEmptyString(item.ID, item.CallID), outputIndex)
		accumulator, ok := tools[key]
		if !ok {
			accumulator = &openAIToolAccumulator{}
			tools[key] = accumulator
		}
		if strings.TrimSpace(item.ID) != "" {
			accumulator.ProviderItemID = strings.TrimSpace(item.ID)
		}
		if strings.TrimSpace(item.Status) != "" {
			accumulator.ProviderStatus = strings.TrimSpace(item.Status)
		}
		if strings.TrimSpace(item.CallID) != "" {
			accumulator.ProviderCallID = strings.TrimSpace(item.CallID)
			accumulator.CallID = namespaceToolCallID(req.ModelCallID, item.CallID)
		} else if strings.TrimSpace(item.ID) != "" {
			accumulator.CallID = namespaceToolCallID(req.ModelCallID, item.ID)
		}
		if strings.TrimSpace(item.Name) != "" {
			accumulator.Name = strings.TrimSpace(item.Name)
		}
		argsTextDelta := ""
		if item.Arguments != "" && accumulator.Args.Len() == 0 {
			_, _ = accumulator.Args.WriteString(item.Arguments)
			argsTextDelta = item.Arguments
		}
		if argsTextDelta != "" || (strings.TrimSpace(accumulator.Name) == "CreatePlan" && accumulator.Args.Len() > 0) {
			if err := emitOpenAIToolProgress(sink, currentModel, accumulator, argsTextDelta); err != nil {
				return err
			}
		}
		if complete {
			delete(tools, key)
			return completeTool(key, accumulator)
		}
		return nil
	}
	applyOutputItem := func(item openAIResponsesOutputItem, outputIndex int, complete bool) error {
		switch strings.TrimSpace(item.Type) {
		case "reasoning":
			return emitReasoningSignature(item.EncryptedContent, item.ID, item.Status, item.Summary)
		case "function_call":
			return applyFunctionCallItem(item, outputIndex, complete)
		case "image_generation_call":
			accumulator := rememberImageGenerationItem(item, outputIndex)
			if !complete {
				return emitImageGenerationStarted(accumulator)
			}
			key := toolKey(item.ID, outputIndex)
			delete(imageGenerations, key)
			return completeImageGeneration(key, accumulator)
		default:
			return nil
		}
	}
	errorFromEvent := func(event openAIResponsesStreamEvent) error {
		if event.Error != nil && strings.TrimSpace(event.Error.Message) != "" {
			return fmt.Errorf("openai responses stream error %s: %s", openAIStreamErrorDetails(event.Error.Type, event.Error.Code, event.RequestID), strings.TrimSpace(event.Error.Message))
		}
		if event.Response != nil && event.Response.Error != nil && strings.TrimSpace(event.Response.Error.Message) != "" {
			return fmt.Errorf("openai responses stream error %s: %s", openAIStreamErrorDetails(event.Response.Error.Type, event.Response.Error.Code, event.RequestID), strings.TrimSpace(event.Response.Error.Message))
		}
		return fmt.Errorf("openai responses stream failed")
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), openAIStreamMaxTokenSize)
	for scanner.Scan() {
		rawLine := scanner.Text()
		_, _ = appendLLMResponseArtifact(req, redactOpenAIStreamArtifactLine(rawLine)+"\n")
		line := strings.TrimSpace(rawLine)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now().UTC()
		}
		payloadLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payloadLine == "[DONE]" {
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
			for key, accumulator := range tools {
				if err := completeTool(key, accumulator); err != nil {
					return fail(err)
				}
			}
			for key, accumulator := range imageGenerations {
				if err := completeImageGeneration(key, accumulator); err != nil {
					return fail(err)
				}
			}
			if err := flushTurnFinished(); err != nil {
				return fail(err)
			}
			break
		}

		var event openAIResponsesStreamEvent
		if err := json.Unmarshal([]byte(payloadLine), &event); err != nil {
			return fail(err)
		}
		if event.Response != nil {
			if strings.TrimSpace(event.Response.Model) != "" {
				currentModel = strings.TrimSpace(event.Response.Model)
			}
			applyUsage(event.Response.Usage)
		}

		switch strings.TrimSpace(event.Type) {
		case "response.output_text.delta":
			if err := emitTaggedContentParts(thinkParser.Consume(event.Delta)); err != nil {
				return fail(err)
			}
		case "response.output_item.added":
			if event.Item != nil {
				if err := applyOutputItem(*event.Item, event.OutputIndex, false); err != nil {
					return fail(err)
				}
			}
		case "response.function_call_arguments.delta":
			key := toolKey(event.ItemID, event.OutputIndex)
			accumulator, ok := tools[key]
			if !ok {
				accumulator = &openAIToolAccumulator{}
				tools[key] = accumulator
			}
			if event.Delta != "" {
				_, _ = accumulator.Args.WriteString(event.Delta)
				streamIdle.MarkEffectiveContent()
				if err := emitOpenAIToolProgress(sink, currentModel, accumulator, event.Delta); err != nil {
					return fail(err)
				}
			}
		case "response.function_call_arguments.done":
			key := toolKey(event.ItemID, event.OutputIndex)
			accumulator, ok := tools[key]
			if !ok {
				accumulator = &openAIToolAccumulator{}
				tools[key] = accumulator
			}
			if event.Arguments != "" && accumulator.Args.Len() == 0 {
				_, _ = accumulator.Args.WriteString(event.Arguments)
				streamIdle.MarkEffectiveContent()
				if err := emitOpenAIToolProgress(sink, currentModel, accumulator, event.Arguments); err != nil {
					return fail(err)
				}
			}
		case "response.image_generation_call.partial_image":
			key := toolKey(event.ItemID, event.OutputIndex)
			accumulator, ok := imageGenerations[key]
			if !ok {
				accumulator = &openAIImageGenerationAccumulator{}
				imageGenerations[key] = accumulator
			}
			if itemID := strings.TrimSpace(event.ItemID); itemID != "" {
				accumulator.ProviderItemID = itemID
				accumulator.CallID = namespaceToolCallID(req.ModelCallID, itemID)
			}
			if strings.TrimSpace(accumulator.CallID) == "" {
				accumulator.CallID = namespaceToolCallID(req.ModelCallID, key)
			}
			if err := emitImageGenerationStarted(accumulator); err != nil {
				return fail(err)
			}
			if imageData := strings.TrimSpace(event.PartialImageB64); imageData != "" {
				accumulator.ImageData = imageData
				streamIdle.MarkEffectiveContent()
			}
			if outputFormat := strings.TrimSpace(event.OutputFormat); outputFormat != "" {
				accumulator.OutputFormat = outputFormat
			}
		case "response.output_item.done":
			if event.Item != nil {
				if err := applyOutputItem(*event.Item, event.OutputIndex, true); err != nil {
					return fail(err)
				}
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if err := emitThinkingDelta(event.Delta); err != nil {
				return fail(err)
			}
		case "response.completed", "response.incomplete":
			if event.Response != nil && !emittedText {
				if strings.TrimSpace(event.Response.OutputText) != "" {
					if err := emitTaggedContentParts(thinkParser.Consume(event.Response.OutputText)); err != nil {
						return fail(err)
					}
				} else {
					for _, item := range event.Response.Output {
						for _, content := range item.Content {
							if strings.TrimSpace(content.Type) != "output_text" && strings.TrimSpace(content.Type) != "text" {
								continue
							}
							if err := emitTaggedContentParts(thinkParser.Consume(content.Text)); err != nil {
								return fail(err)
							}
						}
					}
				}
			}
			if err := flushTaggedContentTail(); err != nil {
				return fail(err)
			}
			if err := flushThinkingCompleted(); err != nil {
				return fail(err)
			}
			if event.Response != nil {
				for index, item := range event.Response.Output {
					if err := applyOutputItem(item, index, true); err != nil {
						return fail(err)
					}
				}
				finishReason = strings.TrimSpace(event.Response.Status)
				if event.Response.IncompleteDetails != nil && strings.TrimSpace(event.Response.IncompleteDetails.Reason) != "" {
					finishReason = strings.TrimSpace(event.Response.IncompleteDetails.Reason)
				}
			}
			turnFinishedPending = true
		case "response.failed", "error":
			return fail(errorFromEvent(event))
		}
	}
	for key, accumulator := range tools {
		if err := completeTool(key, accumulator); err != nil {
			return fail(err)
		}
	}
	for key, accumulator := range imageGenerations {
		if err := completeImageGeneration(key, accumulator); err != nil {
			return fail(err)
		}
	}
	if err := scanner.Err(); err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			return fail(idleErr)
		}
		return fail(err)
	}
	if err := flushTaggedContentTail(); err != nil {
		return fail(err)
	}
	if err := flushThinkingCompleted(); err != nil {
		return fail(err)
	}
	if err := flushTurnFinished(); err != nil {
		return fail(err)
	}
	finishedAt = time.Now().UTC()
	recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "openai", currentModel, startedAt, firstEventAt, finishedAt, effectiveFinishReason(), inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, nil))
	return nil
}

func trailingTagPrefixLength(text string, tag string) int {
	maxLen := len(text)
	if len(tag)-1 < maxLen {
		maxLen = len(tag) - 1
	}
	for size := maxLen; size > 0; size-- {
		if strings.HasSuffix(text, tag[:size]) {
			return size
		}
	}
	return 0
}

func maxInt64(value int64, floor int64) int64 {
	if value < floor {
		return floor
	}
	return value
}

func cloneRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func redactOpenAIStreamArtifactLine(rawLine string) string {
	line := strings.TrimSpace(rawLine)
	if !strings.HasPrefix(line, "data:") {
		return rawLine
	}
	payloadLine := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
	if !strings.Contains(payloadLine, "partial_image_b64") && !strings.Contains(payloadLine, "image_data") && !strings.Contains(payloadLine, "imageData") {
		return rawLine
	}
	var payload any
	if err := json.Unmarshal([]byte(payloadLine), &payload); err != nil {
		return rawLine
	}
	if !redactOpenAIImagePayloadFields(payload) {
		return rawLine
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return rawLine
	}
	return "data: " + string(encoded)
}

func redactOpenAIImagePayloadFields(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "partial_image_b64", "image_data", "imageData":
					item[key] = fmt.Sprintf("[base64 image data omitted from debug log; bytes=%d]", len(strings.TrimSpace(text)))
					changed = true
					continue
				}
			}
			if redactOpenAIImagePayloadFields(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if redactOpenAIImagePayloadFields(child) {
				changed = true
			}
		}
	}
	return changed
}

func emitOpenAIToolProgress(
	sink func(ModelEvent) error,
	model string,
	accumulator *openAIToolAccumulator,
	argsTextDelta string,
) error {
	if accumulator == nil {
		return nil
	}
	toolName := strings.TrimSpace(accumulator.Name)
	if toolName == "CreatePlan" {
		return emitCreatePlanToolProgress(
			sink,
			"openai",
			model,
			accumulator.CallID,
			accumulator.Args.String(),
			argsTextDelta,
			&accumulator.LastCreatePlanSnapshot,
		)
	}
	if toolName != "Write" && toolName != "PatchEdit" {
		return nil
	}

	rawArgs := accumulator.Args.String()
	path, pathFound, pathComplete := extractJSONStringFieldPrefix(rawArgs, "path")
	if !pathFound {
		path, pathFound, pathComplete = extractJSONStringFieldPrefix(rawArgs, "file_path")
	}
	if pathFound && pathComplete {
		trimmedPath := strings.TrimSpace(path)
		if trimmedPath != "" {
			pathChanged := trimmedPath != accumulator.LastEmittedPath
			accumulator.LastEmittedPath = trimmedPath
			if toolName == "PatchEdit" && pathChanged {
				if err := sink(ModelEvent{
					Kind:       ModelEventKindPartialToolCall,
					OccurredAt: time.Now().UTC(),
					Provider:   "openai",
					Model:      model,
					ToolCallID: strings.TrimSpace(accumulator.CallID),
					ToolCall: &agentv1.ToolCall{
						Tool: &agentv1.ToolCall_EditToolCall{
							EditToolCall: &agentv1.EditToolCall{
								Args: &agentv1.EditArgs{Path: trimmedPath},
							},
						},
					},
				}); err != nil {
					return err
				}
			}
			if toolName == "Write" && pathChanged {
				if err := sink(ModelEvent{
					Kind:          ModelEventKindPartialToolCall,
					OccurredAt:    time.Now().UTC(),
					Provider:      "openai",
					Model:         model,
					ToolCallID:    strings.TrimSpace(accumulator.CallID),
					ArgsTextDelta: argsTextDelta,
					ToolCall: &agentv1.ToolCall{
						Tool: &agentv1.ToolCall_EditToolCall{
							EditToolCall: &agentv1.EditToolCall{
								Args: &agentv1.EditArgs{Path: trimmedPath},
							},
						},
					},
				}); err != nil {
					return err
				}
			}
		}
	}
	streamContent, streamFound := extractToolStreamContentPrefix(rawArgs, toolName)
	if !streamFound {
		return nil
	}
	delta := suffixAfterCommonPrefix(accumulator.LastStreamContent, streamContent)
	if delta == "" {
		return nil
	}
	accumulator.LastStreamContent = streamContent
	return sink(ModelEvent{
		Kind:       ModelEventKindToolCallDelta,
		OccurredAt: time.Now().UTC(),
		Provider:   "openai",
		Model:      model,
		ToolCallID: strings.TrimSpace(accumulator.CallID),
		ToolCallDelta: &agentv1.ToolCallDelta{
			Delta: &agentv1.ToolCallDelta_EditToolCallDelta{
				EditToolCallDelta: &agentv1.EditToolCallDelta{
					StreamContentDelta: delta,
				},
			},
		},
	})
}

func normalizeOpenAIProviderMessages(messages []Message, thinkingEnabled bool) ([]map[string]any, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	items := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		content, err := openAIContentValue(message)
		if err != nil {
			return nil, err
		}
		item := map[string]any{
			"role":    strings.TrimSpace(message.Role),
			"content": content,
		}
		// 开启 thinking 时，tool_calls 对应的 assistant message 也要显式携带空 reasoning_content。
		if shouldIncludeOpenAIReasoningContent(message, thinkingEnabled) {
			item["reasoning_content"] = message.ReasoningContent
		}
		if len(message.ToolCalls) > 0 {
			item["tool_calls"] = normalizeToolCallDescriptors(message.ToolCalls)
		}
		if strings.TrimSpace(message.ToolCallID) != "" {
			item["tool_call_id"] = providerToolCallID(message.ToolCallID)
		}
		if strings.TrimSpace(message.Name) != "" {
			item["name"] = strings.TrimSpace(message.Name)
		}
		items = append(items, item)
	}
	return items, nil
}

func shouldSendOpenAIMaxOutputTokens(modelID string) bool {
	return !strings.Contains(strings.ToLower(strings.TrimSpace(modelID)), "gpt")
}

func shouldIncludeOpenAIReasoningContent(message Message, thinkingEnabled bool) bool {
	if strings.TrimSpace(message.ReasoningContent) != "" {
		return true
	}
	if !thinkingEnabled {
		return false
	}
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	return len(message.ToolCalls) > 0
}

func applyOpenAIThinkingDisable(body map[string]any, req StreamRequest, baseURL string, modelID string, endpoint string) {
	if len(body) == 0 || normalizeRuntimeThinkingEffort(req.ThinkingEffort) != "disabled" {
		return
	}
	switch openAIThinkingDisableKind(baseURL, modelID, endpoint) {
	case "thinking_type":
		body["thinking"] = map[string]any{"type": "disabled"}
		delete(body, "reasoning_effort")
		setRequestKnob(req, "thinking_disabled_provider_param", "thinking.type")
	case "enable_thinking":
		body["enable_thinking"] = false
		delete(body, "reasoning_effort")
		setRequestKnob(req, "thinking_disabled_provider_param", "enable_thinking")
	case "reasoning_none":
		if modelchannel.OpenAIEndpointShape(endpoint) == "responses" {
			body["reasoning"] = map[string]any{"effort": "none"}
		} else {
			body["reasoning_effort"] = "none"
		}
		setRequestKnob(req, "thinking_disabled_provider_param", "reasoning.effort")
	}
}

func openAIThinkingDisableKind(baseURL string, modelID string, endpoint string) string {
	base := strings.ToLower(strings.TrimSpace(baseURL))
	model := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case strings.Contains(base, "dashscope") ||
		strings.Contains(base, "qwen") ||
		strings.Contains(base, "aliyun") ||
		strings.Contains(model, "qwen"):
		return "enable_thinking"
	case strings.Contains(base, "deepseek") ||
		strings.Contains(base, "bigmodel") ||
		strings.Contains(base, "z.ai") ||
		strings.Contains(base, "zhipu") ||
		strings.Contains(base, "xiaomimimo") ||
		strings.Contains(base, "mimo") ||
		strings.Contains(model, "deepseek") ||
		strings.Contains(model, "glm") ||
		strings.Contains(model, "zai") ||
		strings.Contains(model, "zhipu") ||
		strings.Contains(model, "mimo"):
		return "thinking_type"
	case openAIModelSupportsReasoningNone(model):
		return "reasoning_none"
	default:
		return ""
	}
}

func openAIModelSupportsReasoningNone(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(model, "gpt-6") {
		return true
	}
	if strings.Contains(model, "gpt-5.1") {
		return true
	}
	if !strings.HasPrefix(model, "gpt-5.") {
		return false
	}
	minorText := strings.TrimPrefix(model, "gpt-5.")
	minorEnd := 0
	for minorEnd < len(minorText) && minorText[minorEnd] >= '0' && minorText[minorEnd] <= '9' {
		minorEnd++
	}
	if minorEnd == 0 {
		return false
	}
	minor, err := strconv.Atoi(minorText[:minorEnd])
	return err == nil && minor >= 1
}

func setRequestKnob(req StreamRequest, key string, value any) {
	if req.RequestKnobs == nil {
		return
	}
	req.RequestKnobs[key] = value
}

func normalizeOpenAIResponsesInput(messages []Message) (string, []map[string]any, error) {
	if len(messages) == 0 {
		return "", nil, nil
	}
	instructionParts := make([]string, 0, 2)
	items := make([]map[string]any, 0, len(messages))
	responsesCallIDs := make(map[string]string)
	activeAssistantReasoningKey := ""
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "system" {
			if text := openAIResponsesMessageText(message); strings.TrimSpace(text) != "" {
				instructionParts = append(instructionParts, strings.TrimSpace(text))
			}
			activeAssistantReasoningKey = ""
			continue
		}
		if role == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			callID := openAIResponsesToolMessageCallID(message, responsesCallIDs)
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  openAIResponsesMessageText(message),
			})
			activeAssistantReasoningKey = ""
			continue
		}
		if role != "assistant" {
			activeAssistantReasoningKey = ""
		}
		if shouldIncludeOpenAIResponsesReasoningItem(message) {
			reasoningKey := openAIResponsesReasoningReplayKey(message)
			if reasoningKey != activeAssistantReasoningKey {
				items = append(items, openAIResponsesReasoningItem(message))
				activeAssistantReasoningKey = reasoningKey
			}
		}
		if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 {
			content, err := openAIResponsesMessageContent(message, role == "assistant")
			if err != nil {
				return "", nil, err
			}
			if len(content) > 0 {
				items = append(items, map[string]any{
					"role":    openAIResponsesMessageRole(role),
					"content": content,
				})
			}
		}
		if role == "assistant" && len(message.ToolCalls) > 0 {
			for _, toolCall := range message.ToolCalls {
				name := strings.TrimSpace(toolCall.Function.Name)
				if name == "" {
					continue
				}
				callID := openAIResponsesToolCallCallID(toolCall)
				if strings.TrimSpace(callID) == "" {
					callID = openAIResponsesProviderCallID(name)
				}
				if internalID := strings.TrimSpace(toolCall.ID); internalID != "" && strings.TrimSpace(callID) != "" {
					responsesCallIDs[internalID] = strings.TrimSpace(callID)
				}
				toolItem := map[string]any{
					"type":      "function_call",
					"call_id":   callID,
					"name":      name,
					"arguments": toolCall.Function.Arguments,
				}
				if itemID := strings.TrimSpace(toolCall.OpenAIResponsesID); itemID != "" {
					toolItem["id"] = itemID
				}
				if status := strings.TrimSpace(toolCall.OpenAIResponsesStatus); status != "" {
					toolItem["status"] = status
				} else {
					toolItem["status"] = "completed"
				}
				items = append(items, toolItem)
			}
		}
	}
	return strings.Join(instructionParts, "\n\n"), items, nil
}

func openAIResponsesReasoningReplayKey(message Message) string {
	return strings.Join([]string{
		strings.TrimSpace(message.ReasoningSignature),
		strings.TrimSpace(message.OpenAIResponsesReasoningID),
		strings.TrimSpace(message.OpenAIResponsesReasoningStatus),
		string(message.OpenAIResponsesReasoningSummary),
	}, "\x00")
}

func openAIResponsesReasoningItem(message Message) map[string]any {
	reasoningItem := map[string]any{
		"type":              "reasoning",
		"encrypted_content": strings.TrimSpace(message.ReasoningSignature),
	}
	if reasoningID := strings.TrimSpace(message.OpenAIResponsesReasoningID); reasoningID != "" {
		reasoningItem["id"] = reasoningID
	}
	if reasoningStatus := strings.TrimSpace(message.OpenAIResponsesReasoningStatus); reasoningStatus != "" {
		reasoningItem["status"] = reasoningStatus
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		reasoningItem["summary"] = json.RawMessage(append([]byte(nil), message.OpenAIResponsesReasoningSummary...))
	} else {
		reasoningItem["summary"] = []any{}
	}
	return reasoningItem
}

func shouldIncludeOpenAIResponsesReasoningItem(message Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" || strings.TrimSpace(message.ReasoningSignature) == "" {
		return false
	}
	return strings.TrimSpace(message.ReasoningSignatureSource) == ReasoningSignatureSourceOpenAIResponses
}

func openAIResponsesToolMessageCallID(message Message, responsesCallIDs map[string]string) string {
	internalID := strings.TrimSpace(message.ToolCallID)
	if internalID == "" {
		return ""
	}
	if callID := strings.TrimSpace(responsesCallIDs[internalID]); callID != "" {
		return callID
	}
	return openAIResponsesProviderCallID(internalID)
}

func openAIResponsesToolCallCallID(toolCall ToolCallDescriptor) string {
	if callID := strings.TrimSpace(toolCall.OpenAIResponsesCallID); callID != "" {
		return callID
	}
	return openAIResponsesProviderCallID(toolCall.ID)
}

func openAIResponsesProviderCallID(toolCallID string) string {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return ""
	}
	if _, raw, ok := splitLegacyToolCallID(trimmed); ok {
		return raw
	}
	if strings.HasPrefix(trimmed, "tc_") {
		parts := strings.SplitN(trimmed, "_", 3)
		if len(parts) == 3 && strings.TrimSpace(parts[2]) != "" {
			return strings.TrimSpace(parts[2])
		}
	}
	return providerToolCallID(trimmed)
}

func openAIResponsesMessageRole(role string) string {
	switch strings.TrimSpace(role) {
	case "assistant":
		return "assistant"
	default:
		return "user"
	}
}

func openAIResponsesMessageText(message Message) string {
	if strings.TrimSpace(message.Content) != "" {
		return message.Content
	}
	if len(message.ContentParts) > 0 {
		return collapseTextContentParts(message.ContentParts)
	}
	return ""
}

func openAIResponsesMessageContent(message Message, assistant bool) ([]map[string]any, error) {
	textType := "input_text"
	if assistant {
		textType = "output_text"
	}
	if !hasImageContentParts(message.ContentParts) {
		text := openAIResponsesMessageText(message)
		if text == "" {
			return nil, nil
		}
		return []map[string]any{{
			"type": textType,
			"text": text,
		}}, nil
	}
	parts := make([]map[string]any, 0, len(message.ContentParts)+1)
	if len(message.ContentParts) == 0 && strings.TrimSpace(message.Content) != "" {
		parts = append(parts, map[string]any{
			"type": textType,
			"text": message.Content,
		})
	}
	for _, part := range message.ContentParts {
		switch normalizeContentPartType(part.Type) {
		case contentPartTypeText:
			if part.Text == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type": textType,
				"text": part.Text,
			})
		case contentPartTypeImage:
			dataURL, err := imageContentDataURL(part.Image)
			if err != nil {
				return nil, err
			}
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": dataURL,
			})
		default:
			return nil, fmt.Errorf("unsupported openai responses content part type: %s", strings.TrimSpace(part.Type))
		}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	return parts, nil
}

func normalizeOpenAIResponsesTools(items []json.RawMessage) ([]map[string]any, error) {
	if len(items) == 0 {
		return nil, nil
	}
	tools := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var raw map[string]any
		if err := json.Unmarshal(item, &raw); err != nil {
			return nil, fmt.Errorf("decode openai responses tool descriptor failed: %w", err)
		}
		source := raw
		if functionShape, ok := raw["function"].(map[string]any); ok {
			source = functionShape
		}
		name := strings.TrimSpace(asStringMapValue(source, "name"))
		if name == "" {
			return nil, fmt.Errorf("openai responses tool descriptor name is required")
		}
		tool := map[string]any{
			"type": "function",
			"name": name,
		}
		if description := strings.TrimSpace(asStringMapValue(source, "description")); description != "" {
			tool["description"] = description
		}
		if parameters, ok := source["parameters"]; ok && parameters != nil {
			tool["parameters"] = parameters
		} else {
			tool["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if strict, ok := source["strict"]; ok {
			tool["strict"] = strict
		} else if strict, ok := raw["strict"]; ok {
			tool["strict"] = strict
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

func asStringMapValue(source map[string]any, key string) string {
	if len(source) == 0 {
		return ""
	}
	switch value := source[key].(type) {
	case string:
		return value
	case fmt.Stringer:
		return value.String()
	default:
		return ""
	}
}

func openAIStreamErrorDetails(errorType string, code string, requestID string) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(errorType); value != "" {
		parts = append(parts, "type="+value)
	}
	if value := strings.TrimSpace(code); value != "" {
		parts = append(parts, "code="+value)
	}
	if value := strings.TrimSpace(requestID); value != "" {
		parts = append(parts, "request_id="+value)
	}
	if len(parts) == 0 {
		return "provider_error"
	}
	return strings.Join(parts, " ")
}
