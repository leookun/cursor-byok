// anthropic.go 实现 Anthropic Messages 兼容流式适配器。
package modeladapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/netproxy"
)

// AnthropicAdapter 实现 Anthropic 兼容流式请求。
type AnthropicAdapter struct {
	// client 负责发送 HTTP 请求。
	client *http.Client
}

type anthropicToolAccumulator struct {
	CallID                 string
	Name                   string
	Args                   strings.Builder
	LastEmittedPath        string
	LastStreamContent      string
	LastCreatePlanSnapshot string
}

type anthropicTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
}

const (
	anthropicThinkOpenTag            = "<think>"
	anthropicThinkCloseTag           = "</think>"
	anthropicBillingHeaderSystemText = "x-anthropic-billing-header: cc_version=2.1.179.61a; cc_entrypoint=cli; cch=37703;"
)

type anthropicContentPartKind string

const (
	anthropicContentPartText              anthropicContentPartKind = "text"
	anthropicContentPartReasoning         anthropicContentPartKind = "reasoning"
	anthropicContentPartThinkingCompleted anthropicContentPartKind = "thinking_completed"
)

type anthropicContentPart struct {
	Kind anthropicContentPartKind
	Text string
}

// anthropicThinkTagParser 负责把 text_delta 里的 <think> 标签拆回 reasoning 流。
type anthropicThinkTagParser struct {
	carry   string
	inThink bool
}

func (parser *anthropicThinkTagParser) Consume(text string) []anthropicContentPart {
	if parser == nil || text == "" {
		return nil
	}
	input := parser.carry + text
	parser.carry = ""
	parts := make([]anthropicContentPart, 0, 4)
	for input != "" {
		if parser.inThink {
			closeIndex := strings.Index(input, anthropicThinkCloseTag)
			if closeIndex >= 0 {
				if closeIndex > 0 {
					parts = append(parts, anthropicContentPart{
						Kind: anthropicContentPartReasoning,
						Text: input[:closeIndex],
					})
				}
				parts = append(parts, anthropicContentPart{Kind: anthropicContentPartThinkingCompleted})
				parser.inThink = false
				input = input[closeIndex+len(anthropicThinkCloseTag):]
				continue
			}
			carryLen := anthropicTrailingTagPrefixLength(input, anthropicThinkCloseTag)
			if emitText := input[:len(input)-carryLen]; emitText != "" {
				parts = append(parts, anthropicContentPart{
					Kind: anthropicContentPartReasoning,
					Text: emitText,
				})
			}
			parser.carry = input[len(input)-carryLen:]
			break
		}

		openIndex := strings.Index(input, anthropicThinkOpenTag)
		if openIndex >= 0 {
			if openIndex > 0 {
				parts = append(parts, anthropicContentPart{
					Kind: anthropicContentPartText,
					Text: input[:openIndex],
				})
			}
			parser.inThink = true
			input = input[openIndex+len(anthropicThinkOpenTag):]
			continue
		}
		carryLen := anthropicTrailingTagPrefixLength(input, anthropicThinkOpenTag)
		if emitText := input[:len(input)-carryLen]; emitText != "" {
			parts = append(parts, anthropicContentPart{
				Kind: anthropicContentPartText,
				Text: emitText,
			})
		}
		parser.carry = input[len(input)-carryLen:]
		break
	}
	return parts
}

func (parser *anthropicThinkTagParser) Flush() []anthropicContentPart {
	if parser == nil || parser.carry == "" {
		return nil
	}
	kind := anthropicContentPartText
	if parser.inThink {
		kind = anthropicContentPartReasoning
	}
	text := parser.carry
	parser.carry = ""
	return []anthropicContentPart{{
		Kind: kind,
		Text: text,
	}}
}

// NewAnthropicAdapter 创建一个 Anthropic 兼容适配器。
func NewAnthropicAdapter() *AnthropicAdapter {
	return &AnthropicAdapter{
		client: netproxy.NewHTTPClient(0),
	}
}

func anthropicEndpointURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if ProviderURLHasEndpoint(base, "/v1/messages", "/messages") {
		return base
	}
	return base + "/v1/messages"
}

// ApplyAnthropicCompatibleAuthHeaders 同时兼容 Anthropic 原生 x-api-key 和 Bearer token 代理。
func ApplyAnthropicCompatibleAuthHeaders(httpReq *http.Request, apiKey string) {
	if httpReq == nil {
		return
	}
	token := anthropicCompatibleAuthToken(apiKey)
	if token == "" {
		return
	}
	httpReq.Header.Set("x-api-key", token)
	httpReq.Header.Set("Authorization", "Bearer "+token)
}

func anthropicCompatibleAuthToken(apiKey string) string {
	token := strings.TrimSpace(apiKey)
	if len(token) >= len("Bearer ") && strings.EqualFold(token[:len("Bearer ")], "Bearer ") {
		token = strings.TrimSpace(token[len("Bearer "):])
	}
	return token
}

func anthropicProviderSystemBlocks(systemParts []string) []map[string]any {
	blocks := []map[string]any{{
		"type": "text",
		"text": anthropicBillingHeaderSystemText,
	}}
	if len(systemParts) > 0 {
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": strings.Join(systemParts, "\n\n"),
		})
	}
	return blocks
}

// Stream 发送 Messages 流式请求，并解析统一模型事件。
func (adapter *AnthropicAdapter) Stream(ctx context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("anthropic base url is empty")
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return fmt.Errorf("anthropic api key is empty")
	}
	modelID := strings.TrimSpace(req.ProviderModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(req.ModelID)
	}
	if modelID == "" {
		return fmt.Errorf("anthropic model id is empty")
	}

	startedAt := time.Now().UTC()
	finishedAt := time.Time{}
	requestURL := anthropicEndpointURL(baseURL)
	body := cloneRequestBodyOverride(req.RequestBodyOverride)
	if len(body) == 0 {
		thinkingConfig := buildAnthropicThinkingConfig(req)
		stableMessageCount := anthropicStableProviderMessageCount(req.Messages, req.StableMessageCount, thinkingConfig != nil)
		systemParts, messages, err := normalizeAnthropicProviderMessages(req.Messages, thinkingConfig != nil)
		if err != nil {
			return err
		}

		tools := make([]anthropicTool, 0, len(req.Tools))
		for _, raw := range req.Tools {
			var descriptor struct {
				Function struct {
					Name        string         `json:"name"`
					Description string         `json:"description"`
					Parameters  map[string]any `json:"parameters"`
				} `json:"function"`
			}
			if err := json.Unmarshal(raw, &descriptor); err != nil {
				finishedAt = time.Now().UTC()
				draftBody := map[string]any{
					"model":          modelID,
					"messages":       messages,
					"stream":         true,
					"max_tokens":     req.MaxTokens,
					"tool_raw_count": len(req.Tools),
				}
				draftBody["system"] = anthropicProviderSystemBlocks(systemParts)
				recordLLMRequestArtifact(req, "anthropic", modelID, "POST", requestURL, draftBody)
				recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
				return err
			}
			tools = append(tools, anthropicTool{
				Name:        strings.TrimSpace(descriptor.Function.Name),
				Description: strings.TrimSpace(descriptor.Function.Description),
				InputSchema: descriptor.Function.Parameters,
			})
		}

		body = map[string]any{
			"model":      modelID,
			"messages":   messages,
			"stream":     true,
			"max_tokens": maxAnthropicTokens(req),
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}
		body["system"] = anthropicProviderSystemBlocks(systemParts)
		if thinkingConfig != nil {
			body["thinking"] = thinkingConfig
			if normalizeRuntimeThinkingEffort(req.ThinkingEffort) != "disabled" {
				body["output_config"] = buildAnthropicOutputConfig(req)
			}
		}
		frontier := buildAnthropicCacheFrontier(body, stableMessageCount)
		req.RequestKnobs = annotateAnthropicRequestKnobs(req.RequestKnobs, body, frontier)
		body = cloneRequestBodyOverride(body)
		applyAnthropicCacheBreakpoints(body, frontier.BreakpointPositions)
		frontier.BreakpointCount = len(frontier.BreakpointPositions)
	}
	if err := ApplyAnthropicExtraParams(body, req.AnthropicExtraParamsEnabled, req.AnthropicExtraParamsJSON); err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	recordLLMRequestArtifact(req, "anthropic", modelID, "POST", requestURL, body)

	payload, err := json.Marshal(body)
	if err != nil {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	streamCtx, streamIdle := newProviderStreamIdleWatchdog(ctx, req.ProviderStreamIdleTimeout)
	defer streamIdle.Stop()

	buildHTTPRequest := func(requestContext context.Context) (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(requestContext, http.MethodPost, requestURL, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		ApplyAnthropicCompatibleAuthHeaders(httpReq, apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("content-type", "application/json")
		httpReq.Header.Set("User-Agent", AnthropicClaudeCodeUserAgent)
		if err := ApplyCustomHeaders(httpReq, req.CustomHeadersEnabled, req.CustomHeadersJSON); err != nil {
			return nil, err
		}
		return httpReq, nil
	}

	resp, err := doProviderRequestWithRetry(streamCtx, adapter.client, "anthropic", req.RequestID, req.ModelCallID, buildHTTPRequest)
	if err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			err = idleErr
		}
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}
	streamIdle.AttachBody(resp.Body)
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = buildHTTPStatusError("anthropic adapter", resp)
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", modelID, startedAt, time.Time{}, finishedAt, "", 0, 0, 0, 0, err))
		return err
	}

	type anthropicUsage struct {
		InputTokens              *int64 `json:"input_tokens,omitempty"`
		OutputTokens             *int64 `json:"output_tokens,omitempty"`
		CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens,omitempty"`
		CacheReadInputTokens     *int64 `json:"cache_read_input_tokens,omitempty"`
	}

	type contentBlock struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Text  string `json:"text"`
		Input any    `json:"input"`
	}
	type anthropicEvent struct {
		Type         string       `json:"type"`
		RequestID    string       `json:"request_id"`
		Index        int          `json:"index"`
		ContentBlock contentBlock `json:"content_block"`
		Message      struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage anthropicUsage `json:"usage"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			PartialJSON string `json:"partial_json"`
			Signature   string `json:"signature"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Error *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}

	toolBlocks := make(map[int]*anthropicToolAccumulator)
	thinkingStarted := time.Time{}
	currentThinkingSignature := ""
	thinkParser := &anthropicThinkTagParser{}
	currentModel := modelID
	inputTokens := int64(0)
	outputTokens := int64(0)
	cacheReadTokens := int64(0)
	cacheWriteTokens := int64(0)
	usagePresent := false
	cacheReadPresent := false
	cacheWritePresent := false
	finishReason := "message_stop"
	firstEventAt := time.Time{}
	fail := func(streamErr error) error {
		finishedAt = time.Now().UTC()
		recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", currentModel, startedAt, firstEventAt, finishedAt, finishReason, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, streamErr))
		return streamErr
	}
	flushThinkingCompleted := func() error {
		if thinkingStarted.IsZero() {
			return nil
		}
		duration := int32(time.Since(thinkingStarted).Milliseconds())
		if duration < 0 {
			duration = 0
		}
		thinkingSignature := strings.TrimSpace(currentThinkingSignature)
		thinkingSignatureSource := ""
		if thinkingSignature != "" {
			thinkingSignatureSource = ReasoningSignatureSourceAnthropic
		}
		if err := sink(ModelEvent{
			Kind:                    ModelEventKindThinkingCompleted,
			OccurredAt:              time.Now().UTC(),
			Provider:                "anthropic",
			Model:                   currentModel,
			ThinkingDurationMS:      duration,
			ThinkingSignature:       thinkingSignature,
			ThinkingSignatureSource: thinkingSignatureSource,
		}); err != nil {
			return err
		}
		thinkingStarted = time.Time{}
		currentThinkingSignature = ""
		return nil
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
			Provider:   "anthropic",
			Model:      currentModel,
			Text:       text,
		})
	}
	emitThinkingDelta := func(reasoning string) error {
		if reasoning == "" {
			return nil
		}
		streamIdle.MarkEffectiveContent()
		if thinkingStarted.IsZero() {
			thinkingStarted = time.Now()
		}
		return sink(ModelEvent{
			Kind:          ModelEventKindThinkingDelta,
			OccurredAt:    time.Now().UTC(),
			Provider:      "anthropic",
			Model:         currentModel,
			Text:          reasoning,
			ThinkingStyle: agentv1.ThinkingStyle_THINKING_STYLE_DEFAULT,
		})
	}
	emitTaggedTextParts := func(parts []anthropicContentPart) error {
		for _, part := range parts {
			switch part.Kind {
			case anthropicContentPartText:
				if err := emitTextDelta(part.Text); err != nil {
					return err
				}
			case anthropicContentPartReasoning:
				if err := emitThinkingDelta(part.Text); err != nil {
					return err
				}
			case anthropicContentPartThinkingCompleted:
				if err := flushThinkingCompleted(); err != nil {
					return err
				}
			}
		}
		return nil
	}
	flushTaggedTextTail := func() error {
		return emitTaggedTextParts(thinkParser.Flush())
	}
	applyUsage := func(usage anthropicUsage) {
		if usage.InputTokens != nil {
			usagePresent = true
			inputTokens = maxInt64(*usage.InputTokens, 0)
		}
		if usage.OutputTokens != nil {
			usagePresent = true
			outputTokens = maxInt64(*usage.OutputTokens, 0)
		}
		if usage.CacheReadInputTokens != nil {
			usagePresent = true
			cacheReadPresent = true
			cacheReadTokens = maxInt64(*usage.CacheReadInputTokens, 0)
		}
		if usage.CacheCreationInputTokens != nil {
			usagePresent = true
			cacheWritePresent = true
			cacheWriteTokens = maxInt64(*usage.CacheCreationInputTokens, 0)
		}
	}
	errorFromEvent := func(event anthropicEvent) error {
		finishReason = "error"
		if event.Error != nil {
			parts := make([]string, 0, 4)
			if value := strings.TrimSpace(event.Error.Type); value != "" {
				parts = append(parts, "type="+value)
			}
			if value := strings.TrimSpace(event.Error.Code); value != "" {
				parts = append(parts, "code="+value)
			}
			if value := strings.TrimSpace(event.RequestID); value != "" {
				parts = append(parts, "request_id="+value)
			}
			if message := strings.TrimSpace(event.Error.Message); message != "" {
				if len(parts) > 0 {
					return fmt.Errorf("anthropic provider error %s: %s", strings.Join(parts, " "), message)
				}
				return fmt.Errorf("anthropic provider error: %s", message)
			}
			if len(parts) > 0 {
				return fmt.Errorf("anthropic provider error %s", strings.Join(parts, " "))
			}
		}
		return fmt.Errorf("anthropic provider error")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	currentEvent := ""
	dataLines := make([]string, 0, 2)
	flush := func() error {
		if currentEvent == "" || len(dataLines) == 0 {
			dataLines = dataLines[:0]
			return nil
		}
		payloadLine := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(payloadLine) == "[DONE]" {
			return nil
		}

		var event anthropicEvent
		if err := json.Unmarshal([]byte(payloadLine), &event); err != nil {
			return err
		}
		if currentEvent == "error" || strings.TrimSpace(event.Type) == "error" {
			return errorFromEvent(event)
		}

		switch currentEvent {
		case "message_start":
			if strings.TrimSpace(event.Message.Model) != "" {
				currentModel = strings.TrimSpace(event.Message.Model)
			}
			applyUsage(event.Message.Usage)
		case "content_block_start":
			if strings.TrimSpace(event.ContentBlock.Type) == "tool_use" {
				if err := flushTaggedTextTail(); err != nil {
					return err
				}
				if err := flushThinkingCompleted(); err != nil {
					return err
				}
				accumulator := &anthropicToolAccumulator{
					CallID: namespaceToolCallID(req.ModelCallID, event.ContentBlock.ID),
					Name:   strings.TrimSpace(event.ContentBlock.Name),
				}
				if !isEmptyAnthropicToolInput(event.ContentBlock.Input) {
					if encoded, err := json.Marshal(event.ContentBlock.Input); err == nil && string(encoded) != "null" {
						_, _ = accumulator.Args.Write(encoded)
					}
				}
				toolBlocks[event.Index] = accumulator
				streamIdle.MarkEffectiveContent()
				if err := emitAnthropicToolProgress(sink, currentModel, accumulator, ""); err != nil {
					return err
				}
			}
		case "content_block_delta":
			if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				if err := emitTaggedTextParts(thinkParser.Consume(event.Delta.Text)); err != nil {
					return err
				}
			}
			if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
				if err := emitThinkingDelta(event.Delta.Thinking); err != nil {
					return err
				}
			}
			if event.Delta.Signature != "" {
				currentThinkingSignature = strings.TrimSpace(event.Delta.Signature)
			}
			if event.Delta.Type == "input_json_delta" {
				accumulator := toolBlocks[event.Index]
				if accumulator != nil && event.Delta.PartialJSON != "" {
					_, _ = accumulator.Args.WriteString(event.Delta.PartialJSON)
					streamIdle.MarkEffectiveContent()
					if err := emitAnthropicToolProgress(sink, currentModel, accumulator, event.Delta.PartialJSON); err != nil {
						return err
					}
				}
			}
		case "content_block_stop":
			accumulator := toolBlocks[event.Index]
			if accumulator != nil {
				argsJSON, err := completedAnthropicToolArgsJSON(accumulator)
				if err != nil {
					delete(toolBlocks, event.Index)
					return err
				}
				if err := emitAnthropicToolProgress(sink, currentModel, accumulator, ""); err != nil {
					return err
				}
				streamIdle.MarkEffectiveContent()
				if err := sink(ModelEvent{
					Kind:       ModelEventKindToolLikeCompleted,
					OccurredAt: time.Now().UTC(),
					Provider:   "anthropic",
					Model:      currentModel,
					ToolInvocation: &runtimecore.ToolInvocation{
						CallID:   accumulator.CallID,
						ToolName: accumulator.Name,
						ArgsJSON: argsJSON,
					},
				}); err != nil {
					return err
				}
				delete(toolBlocks, event.Index)
				return nil
			}
			if err := flushTaggedTextTail(); err != nil {
				return err
			}
			if err := flushThinkingCompleted(); err != nil {
				return err
			}
		case "message_delta":
			applyUsage(event.Usage)
			if strings.TrimSpace(event.Delta.StopReason) != "" {
				finishReason = strings.TrimSpace(event.Delta.StopReason)
			}
			// 当前 MVP 阶段只在 message_stop 时统一收口，不在这里重复发 turn finished。
			return nil
		case "message_stop":
			if err := flushTaggedTextTail(); err != nil {
				return err
			}
			if err := flushThinkingCompleted(); err != nil {
				return err
			}
			if err := sink(ModelEvent{
				Kind:              ModelEventKindTurnFinished,
				OccurredAt:        time.Now().UTC(),
				Provider:          "anthropic",
				Model:             currentModel,
				InputTokens:       inputTokens,
				OutputTokens:      outputTokens,
				CacheReadTokens:   cacheReadTokens,
				CacheWriteTokens:  cacheWriteTokens,
				UsagePresent:      usagePresent,
				CacheReadPresent:  cacheReadPresent,
				CacheWritePresent: cacheWritePresent,
				FinishReason:      finishReason,
			}); err != nil {
				return err
			}
		}
		return nil
	}

	for scanner.Scan() {
		rawLine := scanner.Text()
		_, _ = appendLLMResponseArtifact(req, rawLine+"\n")
		line := strings.TrimSpace(rawLine)
		if line == "" {
			if err := flush(); err != nil {
				return fail(err)
			}
			currentEvent = ""
			continue
		}
		if firstEventAt.IsZero() {
			firstEventAt = time.Now().UTC()
		}
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if err := scanner.Err(); err != nil {
		if idleErr := streamIdle.Err(); idleErr != nil {
			return fail(idleErr)
		}
		return fail(err)
	}
	if err := flushTaggedTextTail(); err != nil {
		return fail(err)
	}
	if err := flushThinkingCompleted(); err != nil {
		return fail(err)
	}
	finishedAt = time.Now().UTC()
	recordLLMSummaryArtifact(req, buildLLMSummaryPayload(req, "anthropic", currentModel, startedAt, firstEventAt, finishedAt, finishReason, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens, nil))
	return nil
}

func anthropicTrailingTagPrefixLength(text string, tag string) int {
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

func anthropicEphemeralCacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

type anthropicCacheFrontier struct {
	CanonicalBodyHash       string   `json:"canonical_body_hash,omitempty"`
	FrontierHash            string   `json:"frontier_hash,omitempty"`
	FrontierPath            string   `json:"frontier_path,omitempty"`
	BreakpointPositions     []string `json:"breakpoint_positions,omitempty"`
	BreakpointCount         int      `json:"breakpoint_count,omitempty"`
	ExpectedCacheRead       bool     `json:"expected_cache_read,omitempty"`
	PreviousFrontierMatched bool     `json:"previous_frontier_matched,omitempty"`
}

func buildAnthropicCacheFrontier(canonicalBody map[string]any, stableMessageCount int) anthropicCacheFrontier {
	frontier := anthropicCacheFrontier{
		CanonicalBodyHash:   anthropicCanonicalHash(canonicalBody),
		BreakpointPositions: anthropicCacheBreakpointPositions(canonicalBody, stableMessageCount),
	}
	if len(frontier.BreakpointPositions) > 0 {
		frontier.FrontierPath = frontier.BreakpointPositions[len(frontier.BreakpointPositions)-1]
		frontier.FrontierHash = anthropicCanonicalPrefixHash(canonicalBody, frontier.FrontierPath)
	}
	frontier.BreakpointCount = len(frontier.BreakpointPositions)
	return frontier
}

func anthropicCacheBreakpointPositions(body map[string]any, stableMessageCount int) []string {
	positions := make([]string, 0, 4)
	if tools, ok := body["tools"].([]anthropicTool); ok && len(tools) > 0 {
		positions = append(positions, fmt.Sprintf("tools[%d]", len(tools)-1))
	} else if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
		positions = append(positions, fmt.Sprintf("tools[%d]", len(tools)-1))
	}
	if system, ok := body["system"].([]map[string]any); ok && len(system) > 0 {
		positions = append(positions, fmt.Sprintf("system[%d]", len(system)-1))
	} else if system, ok := body["system"].([]any); ok && len(system) > 0 {
		positions = append(positions, fmt.Sprintf("system[%d]", len(system)-1))
	}
	if stableMessageCount > 0 {
		if path := anthropicMessageCacheBreakpointPath(body, stableMessageCount); path != "" {
			positions = append(positions, path)
		}
	}
	if path := anthropicMessageCacheBreakpointPath(body, anthropicBodyMessageCount(body)); path != "" {
		positions = append(positions, path)
	}
	return dedupeAnthropicCacheBreakpointPositions(positions)
}

func dedupeAnthropicCacheBreakpointPositions(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func anthropicBodyMessageCount(body map[string]any) int {
	messages, ok := body["messages"].([]anthropicMessage)
	if ok {
		return len(messages)
	}
	genericMessages, ok := body["messages"].([]any)
	if ok {
		return len(genericMessages)
	}
	return 0
}

func anthropicMessageCacheBreakpointPath(body map[string]any, messageCount int) string {
	messages, ok := anthropicMessagesFromBody(body)
	if !ok || len(messages) == 0 || messageCount <= 0 {
		return ""
	}
	if messageCount > len(messages) {
		messageCount = len(messages)
	}
	for messageIndex := messageCount - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		for blockIndex := len(message.Content) - 1; blockIndex >= 0; blockIndex-- {
			if isAnthropicCacheableBlock(message.Content[blockIndex]) {
				return fmt.Sprintf("messages[%d].content[%d]", messageIndex, blockIndex)
			}
		}
	}
	return ""
}

func anthropicMessagesFromBody(body map[string]any) ([]anthropicMessage, bool) {
	messages, ok := body["messages"].([]anthropicMessage)
	if ok {
		return messages, true
	}
	genericMessages, ok := body["messages"].([]any)
	if !ok {
		return nil, false
	}
	messages = make([]anthropicMessage, 0, len(genericMessages))
	for _, item := range genericMessages {
		messageMap, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		contentItems, ok := messageMap["content"].([]any)
		if !ok {
			return nil, false
		}
		content := make([]map[string]any, 0, len(contentItems))
		for _, contentItem := range contentItems {
			block, ok := contentItem.(map[string]any)
			if !ok {
				return nil, false
			}
			content = append(content, block)
		}
		messages = append(messages, anthropicMessage{
			Role:    strings.TrimSpace(anthropicStringValue(messageMap["role"])),
			Content: content,
		})
	}
	return messages, true
}

func applyAnthropicCacheBreakpoints(body map[string]any, positions []string) {
	for _, position := range positions {
		applyAnthropicCacheBreakpoint(body, position)
	}
}

func applyAnthropicCacheBreakpoint(body map[string]any, position string) {
	position = strings.TrimSpace(position)
	if strings.HasPrefix(position, "tools[") {
		index, ok := parseAnthropicBracketIndex(position, "tools")
		if !ok {
			return
		}
		tools, ok := body["tools"].([]any)
		if !ok || index < 0 || index >= len(tools) {
			return
		}
		tool, ok := tools[index].(map[string]any)
		if !ok {
			return
		}
		tool["cache_control"] = anthropicEphemeralCacheControl()
		return
	}
	if strings.HasPrefix(position, "system[") {
		index, ok := parseAnthropicBracketIndex(position, "system")
		if !ok {
			return
		}
		system, ok := body["system"].([]any)
		if !ok || index < 0 || index >= len(system) {
			return
		}
		block, ok := system[index].(map[string]any)
		if !ok {
			return
		}
		block["cache_control"] = anthropicEphemeralCacheControl()
		return
	}
	messageIndex, blockIndex, ok := parseAnthropicMessageBlockPath(position)
	if !ok {
		return
	}
	messages, ok := body["messages"].([]any)
	if !ok || messageIndex < 0 || messageIndex >= len(messages) {
		return
	}
	message, ok := messages[messageIndex].(map[string]any)
	if !ok {
		return
	}
	content, ok := message["content"].([]any)
	if !ok || blockIndex < 0 || blockIndex >= len(content) {
		return
	}
	block, ok := content[blockIndex].(map[string]any)
	if !ok {
		return
	}
	block["cache_control"] = anthropicEphemeralCacheControl()
}

func parseAnthropicBracketIndex(position string, prefix string) (int, bool) {
	start := prefix + "["
	if !strings.HasPrefix(position, start) || !strings.HasSuffix(position, "]") {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(position, start), "]"))
	return value, err == nil
}

func parseAnthropicMessageBlockPath(position string) (int, int, bool) {
	const messagePrefix = "messages["
	const contentPrefix = "].content["
	if !strings.HasPrefix(position, messagePrefix) || !strings.HasSuffix(position, "]") {
		return 0, 0, false
	}
	rest := strings.TrimPrefix(position, messagePrefix)
	parts := strings.Split(rest, contentPrefix)
	if len(parts) != 2 {
		return 0, 0, false
	}
	messageIndex, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	blockIndex, err := strconv.Atoi(strings.TrimSuffix(parts[1], "]"))
	if err != nil {
		return 0, 0, false
	}
	return messageIndex, blockIndex, true
}

func annotateAnthropicRequestKnobs(requestKnobs map[string]any, canonicalBody map[string]any, frontier anthropicCacheFrontier) map[string]any {
	if len(requestKnobs) == 0 {
		requestKnobs = map[string]any{}
	}
	previousHash := anthropicPreviousFrontierHash(requestKnobs)
	previousPath := anthropicPreviousFrontierPath(requestKnobs)
	currentPreviousFrontierHash := ""
	if previousPath != "" {
		currentPreviousFrontierHash = anthropicCanonicalPrefixHash(canonicalBody, previousPath)
	}
	frontier.PreviousFrontierMatched = previousHash != "" && currentPreviousFrontierHash == previousHash
	frontier.ExpectedCacheRead = frontier.PreviousFrontierMatched && frontier.BreakpointCount > 0
	requestKnobs["cache_frontier"] = map[string]any{
		"canonical_body_hash":          frontier.CanonicalBodyHash,
		"frontier_hash":                frontier.FrontierHash,
		"frontier_path":                frontier.FrontierPath,
		"breakpoint_positions":         append([]string(nil), frontier.BreakpointPositions...),
		"breakpoint_count":             frontier.BreakpointCount,
		"expected_cache_read":          frontier.ExpectedCacheRead,
		"previous_frontier_matched":    frontier.PreviousFrontierMatched,
		"previous_frontier_hash":       previousHash,
		"previous_frontier_path":       previousPath,
		"current_previous_prefix_hash": currentPreviousFrontierHash,
	}
	return requestKnobs
}

func anthropicPreviousFrontierHash(requestKnobs map[string]any) string {
	if len(requestKnobs) == 0 {
		return ""
	}
	value := strings.TrimSpace(anthropicStringValue(requestKnobs["previous_cache_frontier_hash"]))
	if value != "" {
		return value
	}
	previous, ok := requestKnobs["previous_cache_frontier"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(anthropicStringValue(previous["frontier_hash"]))
}

func anthropicPreviousFrontierPath(requestKnobs map[string]any) string {
	if len(requestKnobs) == 0 {
		return ""
	}
	previous, ok := requestKnobs["previous_cache_frontier"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(anthropicStringValue(previous["frontier_path"]))
}

func anthropicStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

func anthropicCanonicalHash(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:16]
}

func anthropicCanonicalPrefixHash(body map[string]any, frontierPath string) string {
	prefix := cloneRequestBodyOverride(body)
	if strings.TrimSpace(frontierPath) == "" {
		return anthropicCanonicalHash(prefix)
	}
	trimAnthropicBodyAfterFrontier(prefix, frontierPath)
	return anthropicCanonicalHash(prefix)
}

func trimAnthropicBodyAfterFrontier(body map[string]any, frontierPath string) {
	if strings.HasPrefix(frontierPath, "tools[") {
		return
	}
	if strings.HasPrefix(frontierPath, "system[") {
		delete(body, "messages")
		return
	}
	messageIndex, blockIndex, ok := parseAnthropicMessageBlockPath(frontierPath)
	if !ok {
		return
	}
	messages, ok := body["messages"].([]any)
	if !ok || messageIndex < 0 || messageIndex >= len(messages) {
		return
	}
	messages = messages[:messageIndex+1]
	message, ok := messages[messageIndex].(map[string]any)
	if ok {
		if content, ok := message["content"].([]any); ok && blockIndex >= 0 && blockIndex < len(content) {
			message["content"] = content[:blockIndex+1]
		}
	}
	body["messages"] = messages
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func anthropicStableProviderMessageCount(input []Message, stableReplayMessageCount int, thinkingEnabled bool) int {
	if len(input) == 0 || stableReplayMessageCount <= 0 {
		return 0
	}
	stableReplayMessages := make([]Message, 0, stableReplayMessageCount)
	for _, message := range input {
		if strings.TrimSpace(message.Role) == "system" {
			continue
		}
		if len(stableReplayMessages) >= stableReplayMessageCount {
			break
		}
		stableReplayMessages = append(stableReplayMessages, message)
	}
	if len(stableReplayMessages) == 0 {
		return 0
	}
	_, messages, err := normalizeAnthropicProviderMessages(stableReplayMessages, thinkingEnabled)
	if err != nil {
		return 0
	}
	return len(messages)
}

func applyAnthropicMessageCacheBreakpoints(messages []anthropicMessage, stableMessageCountOverride ...int) {
	if len(stableMessageCountOverride) == 0 || len(messages) == 0 {
		return
	}
	stableMessageCount := stableMessageCountOverride[0]
	if stableMessageCount > 0 {
		applyAnthropicMessageCacheBreakpointAt(messages, stableMessageCount)
	}
	if len(messages) > stableMessageCount {
		applyAnthropicMessageCacheBreakpointAt(messages, len(messages))
	}
}

func applyAnthropicMessageCacheBreakpointAt(messages []anthropicMessage, messageCount int) {
	if len(messages) == 0 || messageCount <= 0 {
		return
	}
	if messageCount > len(messages) {
		messageCount = len(messages)
	}
	for messageIndex := messageCount - 1; messageIndex >= 0; messageIndex-- {
		message := messages[messageIndex]
		for blockIndex := len(message.Content) - 1; blockIndex >= 0; blockIndex-- {
			block := message.Content[blockIndex]
			if !isAnthropicCacheableBlock(block) {
				continue
			}
			block["cache_control"] = anthropicEphemeralCacheControl()
			return
		}
	}
}

func isAnthropicCacheableBlock(block map[string]any) bool {
	if len(block) == 0 {
		return false
	}
	switch strings.TrimSpace(anthropicStringField(block, "type")) {
	case contentPartTypeText:
		return strings.TrimSpace(anthropicStringField(block, "text")) != ""
	case "tool_result":
		return strings.TrimSpace(anthropicStringField(block, "content")) != ""
	case "tool_use":
		return strings.TrimSpace(anthropicStringField(block, "id")) != "" && strings.TrimSpace(anthropicStringField(block, "name")) != ""
	default:
		return false
	}
}

func normalizeAnthropicProviderMessages(input []Message, thinkingEnabled bool) ([]string, []anthropicMessage, error) {
	systemParts := make([]string, 0, len(input))
	messages := make([]anthropicMessage, 0, len(input))
	pendingToolResults := make([]map[string]any, 0, 2)
	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		messages = append(messages, anthropicMessage{
			Role:    "user",
			Content: append([]map[string]any(nil), pendingToolResults...),
		})
		pendingToolResults = pendingToolResults[:0]
	}

	for _, message := range input {
		role := strings.TrimSpace(message.Role)
		switch role {
		case "system":
			if hasImageContentParts(message.ContentParts) {
				return nil, nil, fmt.Errorf("anthropic system message does not support image content")
			}
			content := message.Content
			if strings.TrimSpace(content) == "" && len(message.ContentParts) > 0 {
				content = collapseTextContentParts(message.ContentParts)
			}
			if strings.TrimSpace(content) != "" {
				systemParts = append(systemParts, content)
			}
		case "tool":
			toolUseID := providerToolCallID(message.ToolCallID)
			if toolUseID == "" {
				return nil, nil, fmt.Errorf("anthropic tool message requires tool_call_id")
			}
			pendingToolResults = append(pendingToolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolUseID,
				"content":     message.Content,
			})
		case "user", "assistant":
			flushToolResults()
			contentBlocks, err := anthropicProviderContentBlocks(message, thinkingEnabled)
			if err != nil {
				return nil, nil, err
			}
			blocks := make([]map[string]any, 0, len(contentBlocks)+len(message.ToolCalls))
			blocks = append(blocks, contentBlocks...)
			if role == "assistant" {
				for _, toolCall := range message.ToolCalls {
					inputJSON, err := decodeAnthropicToolInput(toolCall.Function.Arguments)
					if err != nil {
						return nil, nil, err
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    providerToolCallID(toolCall.ID),
						"name":  strings.TrimSpace(toolCall.Function.Name),
						"input": inputJSON,
					})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			if role == "assistant" && mergeAnthropicAssistantToolUseWithPrevious(&messages, message, blocks) {
				continue
			}
			messages = append(messages, anthropicMessage{
				Role:    role,
				Content: blocks,
			})
		default:
			flushToolResults()
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			messages = append(messages, anthropicMessage{
				Role: "user",
				Content: []map[string]any{{
					"type": "text",
					"text": message.Content,
				}},
			})
		}
	}
	flushToolResults()
	return systemParts, messages, nil
}

func anthropicProviderContentBlocks(message Message, thinkingEnabled bool) ([]map[string]any, error) {
	blocks, err := anthropicContentBlocks(message)
	if err != nil {
		return nil, err
	}
	if !shouldIncludeAnthropicThinkingBlock(message, thinkingEnabled) {
		return blocks, nil
	}

	thinkingBlock := map[string]any{
		"type":     "thinking",
		"thinking": message.ReasoningContent,
	}
	if signature := anthropicThinkingSignature(message); signature != "" {
		thinkingBlock["signature"] = signature
	}
	return append([]map[string]any{thinkingBlock}, blocks...), nil
}

func mergeAnthropicAssistantToolUseWithPrevious(messages *[]anthropicMessage, message Message, blocks []map[string]any) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}
	if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
		return false
	}
	if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 {
		return false
	}
	reasoning := strings.TrimSpace(message.ReasoningContent)
	signature := anthropicThinkingSignature(message)
	if reasoning == "" {
		return false
	}
	last := &(*messages)[len(*messages)-1]
	if strings.TrimSpace(last.Role) != "assistant" || !anthropicMessageHasLeadingThinking(*last, reasoning, signature) {
		return false
	}
	toolUseBlocks := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if strings.TrimSpace(anthropicStringField(block, "type")) == "thinking" {
			continue
		}
		toolUseBlocks = append(toolUseBlocks, block)
	}
	if len(toolUseBlocks) == 0 {
		return false
	}
	last.Content = append(last.Content, toolUseBlocks...)
	return true
}

func anthropicMessageHasLeadingThinking(message anthropicMessage, reasoning string, signature string) bool {
	if len(message.Content) == 0 {
		return false
	}
	first := message.Content[0]
	if strings.TrimSpace(anthropicStringField(first, "type")) != "thinking" {
		return false
	}
	return strings.TrimSpace(anthropicStringField(first, "thinking")) == reasoning && strings.TrimSpace(anthropicStringField(first, "signature")) == signature
}

func anthropicThinkingSignature(message Message) string {
	signature := strings.TrimSpace(message.ReasoningSignature)
	if signature == "" {
		return ""
	}
	source := strings.TrimSpace(message.ReasoningSignatureSource)
	if source == "" || source == ReasoningSignatureSourceAnthropic {
		return signature
	}
	return ""
}

func anthropicStringField(payload map[string]any, key string) string {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func shouldIncludeAnthropicThinkingBlock(message Message, thinkingEnabled bool) bool {
	if !thinkingEnabled {
		return false
	}
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if strings.TrimSpace(message.ReasoningContent) == "" {
		return false
	}
	return true
}

func decodeAnthropicToolInput(arguments string) (any, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil, fmt.Errorf("decode anthropic tool input failed: %w", err)
	}
	return value, nil
}

func completedAnthropicToolArgsJSON(accumulator *anthropicToolAccumulator) ([]byte, error) {
	if accumulator == nil {
		return []byte("{}"), nil
	}
	trimmed := strings.TrimSpace(accumulator.Args.String())
	if trimmed == "" {
		return []byte("{}"), nil
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		toolName := strings.TrimSpace(accumulator.Name)
		if toolName == "" {
			toolName = "tool"
		}
		return nil, fmt.Errorf("anthropic returned incomplete or malformed tool input for %s: %w", toolName, err)
	}
	if value == nil {
		toolName := strings.TrimSpace(accumulator.Name)
		if toolName == "" {
			toolName = "tool"
		}
		return nil, fmt.Errorf("anthropic returned non-object tool input for %s", toolName)
	}
	return []byte(trimmed), nil
}

func buildAnthropicThinkingConfig(req StreamRequest) map[string]any {
	if normalizeRuntimeThinkingEffort(req.ThinkingEffort) == "disabled" {
		return map[string]any{
			"type": "disabled",
		}
	}
	if strings.TrimSpace(req.AnthropicThinkingEffort) == "" {
		return nil
	}
	return map[string]any{
		"type":    "adaptive",
		"display": "summarized",
	}
}

func buildAnthropicOutputConfig(req StreamRequest) map[string]any {
	return map[string]any{
		"effort": anthropicThinkingEffort(req),
	}
}

func anthropicThinkingEffort(req StreamRequest) string {
	switch strings.ToLower(strings.TrimSpace(req.AnthropicThinkingEffort)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(req.AnthropicThinkingEffort))
	default:
		return "xhigh"
	}
}

func emitAnthropicToolProgress(
	sink func(ModelEvent) error,
	model string,
	accumulator *anthropicToolAccumulator,
	argsTextDelta string,
) error {
	if accumulator == nil {
		return nil
	}
	toolName := strings.TrimSpace(accumulator.Name)
	if toolName == "CreatePlan" {
		return emitCreatePlanToolProgress(
			sink,
			"anthropic",
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
					Provider:   "anthropic",
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
					Provider:      "anthropic",
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
		Provider:   "anthropic",
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

func isEmptyAnthropicToolInput(input any) bool {
	if input == nil {
		return true
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return false
	}
	switch string(encoded) {
	case "", "null", "{}", "[]":
		return true
	default:
		return false
	}
}

func extractJSONStringFieldPrefix(input string, field string) (string, bool, bool) {
	keyToken := `"` + strings.TrimSpace(field) + `"`
	start := strings.Index(input, keyToken)
	if start < 0 {
		return "", false, false
	}
	index := start + len(keyToken)
	for index < len(input) && isJSONWhitespace(input[index]) {
		index++
	}
	if index >= len(input) || input[index] != ':' {
		return "", false, false
	}
	index++
	for index < len(input) && isJSONWhitespace(input[index]) {
		index++
	}
	if index >= len(input) || input[index] != '"' {
		return "", false, false
	}
	index++

	var builder strings.Builder
	for index < len(input) {
		character := input[index]
		if character == '"' {
			return builder.String(), true, true
		}
		if character != '\\' {
			builder.WriteByte(character)
			index++
			continue
		}
		if index+1 >= len(input) {
			return builder.String(), true, false
		}
		next := input[index+1]
		switch next {
		case '"', '\\', '/':
			builder.WriteByte(next)
			index += 2
		case 'b':
			builder.WriteByte('\b')
			index += 2
		case 'f':
			builder.WriteByte('\f')
			index += 2
		case 'n':
			builder.WriteByte('\n')
			index += 2
		case 'r':
			builder.WriteByte('\r')
			index += 2
		case 't':
			builder.WriteByte('\t')
			index += 2
		case 'u':
			if index+6 > len(input) {
				return builder.String(), true, false
			}
			runeValue, width, ok := decodeUnicodeEscape(input[index:])
			if !ok {
				return builder.String(), true, false
			}
			builder.WriteRune(runeValue)
			index += width
		default:
			builder.WriteByte(next)
			index += 2
		}
	}
	return builder.String(), true, false
}

func decodeUnicodeEscape(input string) (rune, int, bool) {
	if len(input) < 6 || input[0] != '\\' || input[1] != 'u' {
		return 0, 0, false
	}
	value, err := strconv.ParseUint(input[2:6], 16, 16)
	if err != nil {
		return 0, 0, false
	}
	r := rune(value)
	if utf16.IsSurrogate(r) {
		if len(input) < 12 || input[6] != '\\' || input[7] != 'u' {
			return 0, 0, false
		}
		nextValue, nextErr := strconv.ParseUint(input[8:12], 16, 16)
		if nextErr != nil {
			return 0, 0, false
		}
		decoded := utf16.DecodeRune(r, rune(nextValue))
		if decoded == '\uFFFD' {
			return 0, 0, false
		}
		return decoded, 12, true
	}
	return r, 6, true
}

func suffixAfterCommonPrefix(previous string, current string) string {
	if previous == "" {
		return current
	}
	maxPrefix := len(previous)
	if len(current) < maxPrefix {
		maxPrefix = len(current)
	}
	index := 0
	for index < maxPrefix && previous[index] == current[index] {
		index++
	}
	return current[index:]
}

func extractToolStreamContentPrefix(rawArgs string, toolName string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit":
		if value, found, _ := extractJSONStringFieldPrefix(rawArgs, "new_string"); found {
			return value, true
		}
	case "Write":
		for _, field := range []string{"contents", "content", "stream_content", "streamContent"} {
			if value, found, _ := extractJSONStringFieldPrefix(rawArgs, field); found {
				return value, true
			}
		}
	}
	return "", false
}

func isJSONWhitespace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

// anthropicThinkingBudget 计算当前 Anthropic thinking 预算。
func anthropicThinkingBudget(maxTokens int) int {
	if maxTokens <= 0 {
		return 2048
	}
	budget := maxTokens / 2
	if budget < 1024 {
		budget = 1024
	}
	return budget
}
