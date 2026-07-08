package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	modeladapter "cursor/internal/backend/agent/model"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/modelchannel"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	modelAdapterTestUpdatedEvent      = "model-adapter-test:updated"
	modelAdapterTestPrompt            = "Output the numbers 1 through 120 separated by a single space. No commas, no newlines, no explanation."
	modelAdapterTestTimeout           = 45 * time.Second
	modelAdapterTestDefaultMaxTokens  = 65_536
	modelAdapterTestEmptyTextError    = "未收到文本输出，无法计算测速结果"
	modelAdapterTestMaxErrorBodyBytes = 8192
)

type ModelAdapterTestStatus string

const (
	ModelAdapterTestStatusIdle    ModelAdapterTestStatus = "idle"
	ModelAdapterTestStatusRunning ModelAdapterTestStatus = "running"
	ModelAdapterTestStatusSuccess ModelAdapterTestStatus = "success"
	ModelAdapterTestStatusError   ModelAdapterTestStatus = "error"
)

// ModelAdapterTestResult 表示一次模型测速结果。
type ModelAdapterTestResult struct {
	AdapterID        string  `json:"adapterID"`
	RequestHash      string  `json:"requestHash"`
	Status           string  `json:"status"`
	TokensPerSecond  float64 `json:"tokensPerSecond"`
	FirstTextTokenMS int64   `json:"firstTextTokenMS"`
	TotalDurationMS  int64   `json:"totalDurationMS"`
	OutputTokens     int64   `json:"outputTokens"`
	TokensEstimated  bool    `json:"tokensEstimated"`
	SummaryText      string  `json:"summaryText"`
	Error            string  `json:"error"`
	RawResponse      string  `json:"rawResponse"`
	TestedAt         string  `json:"testedAt"`
}

// ModelAdapterTestResultsPayload 用于向前端广播当前测速结果快照。
type ModelAdapterTestResultsPayload struct {
	Results []ModelAdapterTestResult `json:"results"`
}

type modelAdapterTestMetrics struct {
	firstTextTokenAt time.Time
	finishedAt       time.Time
	outputTokens     int64
	outputProvided   bool
	text             strings.Builder
	rawResponse      string
}

type modelAdapterTestArtifactObserver struct {
	mu       sync.Mutex
	response strings.Builder
}

func (observer *modelAdapterTestArtifactObserver) RecordLLMRequest(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) AppendLLMResponseChunk(_ string, _ string, _ string, chunk string) (string, error) {
	if observer == nil {
		return "", nil
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	_, _ = observer.response.WriteString(chunk)
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) RecordLLMSummary(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (observer *modelAdapterTestArtifactObserver) RawResponse() string {
	if observer == nil {
		return ""
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return strings.TrimSpace(observer.response.String())
}

func (s *ProxyService) GetModelAdapterTestResults() []ModelAdapterTestResult {
	return s.snapshotModelAdapterTestResults()
}

func (s *ProxyService) TestModelAdapter(adapter serverconfig.ModelAdapterConfig) (ModelAdapterTestResult, error) {
	requestHash := buildModelAdapterTestRequestHash(adapter)
	adapterID := buildModelAdapterTestCacheKey(adapter, requestHash)

	if cached, ok := s.getRunningModelAdapterTestResult(adapterID, requestHash); ok {
		return cached, nil
	}

	normalized, err := normalizeSingleModelAdapterConfig(adapter)
	if err != nil {
		result := ModelAdapterTestResult{
			AdapterID:   adapterID,
			RequestHash: requestHash,
			Status:      string(ModelAdapterTestStatusError),
			SummaryText: buildModelAdapterTestErrorSummary(err),
			Error:       buildModelAdapterTestErrorSummary(err),
			RawResponse: strings.TrimSpace(modelAdapterTestErrorMessage(err)),
			TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		}
		s.storeAndEmitModelAdapterTestResult(result)
		return result, err
	}

	running := ModelAdapterTestResult{
		AdapterID:   normalized.ID,
		RequestHash: requestHash,
		Status:      string(ModelAdapterTestStatusRunning),
		SummaryText: "测试中...",
		TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	s.storeAndEmitModelAdapterTestResult(running)

	result, testErr := s.runModelAdapterTest(normalized, requestHash)
	s.storeAndEmitModelAdapterTestResult(result)
	if testErr != nil {
		return result, testErr
	}
	return result, nil
}

func normalizeSingleModelAdapterConfig(adapter serverconfig.ModelAdapterConfig) (serverconfig.ModelAdapterConfig, error) {
	normalized, err := serverconfig.NormalizeModelAdapterConfigs([]serverconfig.ModelAdapterConfig{adapter})
	if err != nil {
		return serverconfig.ModelAdapterConfig{}, err
	}
	if len(normalized) == 0 {
		return serverconfig.ModelAdapterConfig{}, errors.New("模型配置不能为空")
	}
	return normalized[0], nil
}

func (s *ProxyService) runModelAdapterTest(adapter serverconfig.ModelAdapterConfig, requestHash string) (ModelAdapterTestResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), modelAdapterTestTimeout)
	defer cancel()

	startedAt := time.Now().UTC()
	metrics, requestErr := s.executeModelAdapterNonStreamingTest(ctx, adapter)
	if requestErr != nil {
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, requestErr)
		return result, requestErr
	}

	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	if metrics.firstTextTokenAt.IsZero() {
		emptyTextErr := errors.New(modelAdapterTestEmptyTextError)
		result := buildErroredModelAdapterTestResult(adapter.ID, requestHash, emptyTextErr)
		return result, emptyTextErr
	}

	outputTokens := metrics.outputTokens
	tokensEstimated := false
	if !metrics.outputProvided || outputTokens <= 0 {
		outputTokens = estimateBenchmarkTextTokens(metrics.text.String())
		tokensEstimated = true
	}

	firstTextTokenMS := metrics.firstTextTokenAt.Sub(startedAt).Milliseconds()
	if firstTextTokenMS < 0 {
		firstTextTokenMS = 0
	}
	totalDurationMS := metrics.finishedAt.Sub(startedAt).Milliseconds()
	if totalDurationMS < 0 {
		totalDurationMS = 0
	}

	tokensPerSecond := 0.0
	totalDuration := metrics.finishedAt.Sub(startedAt)
	if outputTokens > 0 && totalDuration > 0 {
		tokensPerSecond = float64(outputTokens) / totalDuration.Seconds()
	}

	result := ModelAdapterTestResult{
		AdapterID:        adapter.ID,
		RequestHash:      requestHash,
		Status:           string(ModelAdapterTestStatusSuccess),
		TokensPerSecond:  tokensPerSecond,
		FirstTextTokenMS: firstTextTokenMS,
		TotalDurationMS:  totalDurationMS,
		OutputTokens:     outputTokens,
		TokensEstimated:  tokensEstimated,
		TestedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		RawResponse:      strings.TrimSpace(metrics.rawResponse),
	}
	result.SummaryText = buildModelAdapterTestSummaryText(result)
	return result, nil
}

func (s *ProxyService) executeModelAdapterNonStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig) (*modelAdapterTestMetrics, error) {
	switch strings.TrimSpace(adapter.Type) {
	case "openai":
		return s.executeOpenAIStreamingTest(ctx, adapter)
	case "anthropic":
		return s.executeAnthropicStreamingTest(ctx, adapter)
	default:
		return nil, fmt.Errorf("unsupported provider %q", strings.TrimSpace(adapter.Type))
	}
}

func (s *ProxyService) executeOpenAIStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig) (*modelAdapterTestMetrics, error) {
	_ = s
	metrics := &modelAdapterTestMetrics{}
	observer := &modelAdapterTestArtifactObserver{}
	maxTokens := modelAdapterTestConfiguredOpenAIMaxTokens(adapter)
	requestID := "model-adapter-test-" + buildModelAdapterTestRequestHash(adapter)
	req := modeladapter.StreamRequest{
		RequestID:                   requestID,
		RunID:                       requestID,
		ModelCallID:                 requestID,
		ModelID:                     strings.TrimSpace(adapter.ID),
		Provider:                    "openai",
		BaseURL:                     strings.TrimSpace(adapter.BaseURL),
		APIKey:                      strings.TrimSpace(adapter.APIKey),
		ProviderModelID:             strings.TrimSpace(adapter.ModelID),
		ResolvedChannelID:           strings.TrimSpace(adapter.ID),
		ResolvedChannelName:         strings.TrimSpace(adapter.DisplayName),
		ResolvedContextWindowTokens: adapter.ContextWindowTokens,
		ReasoningEffort:             strings.TrimSpace(adapter.ReasoningEffort),
		OpenAIEndpoint:              strings.TrimSpace(adapter.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    adapter.OpenAIExtraParamsEnabled,
		OpenAIExtraParamsJSON:       strings.TrimSpace(adapter.OpenAIExtraParamsJSON),
		CustomHeadersEnabled:        adapter.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(adapter.CustomHeadersJSON),
		Messages:                    []modeladapter.Message{{Role: "user", Content: modelAdapterTestPrompt}},
		MaxTokens:                   maxTokens,
		Stream:                      true,
		RequestKnobs:                map[string]any{"stream": true, "max_tokens": maxTokens},
		Observer:                    observer,
		ProviderStreamIdleTimeout:   modelAdapterTestTimeout,
	}
	err := modeladapter.NewOpenAIAdapter().Stream(ctx, req, func(event modeladapter.ModelEvent) error {
		now := time.Now().UTC()
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if strings.TrimSpace(event.Text) != "" && metrics.firstTextTokenAt.IsZero() {
				metrics.firstTextTokenAt = now
			}
			_, _ = metrics.text.WriteString(event.Text)
		case modeladapter.ModelEventKindTurnFinished:
			metrics.finishedAt = now
			if event.OutputTokens > 0 {
				metrics.outputTokens = event.OutputTokens
				metrics.outputProvided = true
			}
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return event.Err
			}
			return errors.New("provider error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	metrics.rawResponse = observer.RawResponse()
	if strings.TrimSpace(metrics.rawResponse) == "" {
		metrics.rawResponse = strings.TrimSpace(metrics.text.String())
	}
	return metrics, nil
}

func (s *ProxyService) executeAnthropicStreamingTest(ctx context.Context, adapter serverconfig.ModelAdapterConfig) (*modelAdapterTestMetrics, error) {
	_ = s
	metrics := &modelAdapterTestMetrics{}
	observer := &modelAdapterTestArtifactObserver{}
	maxTokens := modelAdapterTestConfiguredAnthropicMaxTokens(adapter)
	thinkingEffort := normalizeModelAdapterTestAnthropicThinkingEffort(adapter.AnthropicThinkingEffort)
	requestID := "model-adapter-test-" + buildModelAdapterTestRequestHash(adapter)
	req := modeladapter.StreamRequest{
		RequestID:                   requestID,
		RunID:                       requestID,
		ModelCallID:                 requestID,
		ModelID:                     strings.TrimSpace(adapter.ID),
		Provider:                    "anthropic",
		BaseURL:                     strings.TrimSpace(adapter.BaseURL),
		APIKey:                      strings.TrimSpace(adapter.APIKey),
		ProviderModelID:             strings.TrimSpace(adapter.ModelID),
		ResolvedChannelID:           strings.TrimSpace(adapter.ID),
		ResolvedChannelName:         strings.TrimSpace(adapter.DisplayName),
		ResolvedContextWindowTokens: adapter.ContextWindowTokens,
		ThinkingEffort:              thinkingEffort,
		AnthropicMaxTokens:          maxTokens,
		AnthropicThinkingEffort:     thinkingEffort,
		CustomHeadersEnabled:        adapter.CustomHeadersEnabled,
		CustomHeadersJSON:           strings.TrimSpace(adapter.CustomHeadersJSON),
		AnthropicExtraParamsEnabled: adapter.AnthropicExtraParamsEnabled,
		AnthropicExtraParamsJSON:    strings.TrimSpace(adapter.AnthropicExtraParamsJSON),
		ThinkingBudgetTokens:        adapter.ThinkingBudgetTokens,
		Messages:                    []modeladapter.Message{{Role: "user", Content: modelAdapterTestPrompt}},
		MaxTokens:                   maxTokens,
		Stream:                      true,
		RequestKnobs:                map[string]any{"stream": true, "anthropic_max_tokens": maxTokens, "max_tokens": maxTokens},
		Observer:                    observer,
		ProviderStreamIdleTimeout:   modelAdapterTestTimeout,
	}
	err := modeladapter.NewAnthropicAdapter().Stream(ctx, req, func(event modeladapter.ModelEvent) error {
		now := time.Now().UTC()
		switch event.Kind {
		case modeladapter.ModelEventKindTextDelta:
			if strings.TrimSpace(event.Text) != "" && metrics.firstTextTokenAt.IsZero() {
				metrics.firstTextTokenAt = now
			}
			_, _ = metrics.text.WriteString(event.Text)
		case modeladapter.ModelEventKindTurnFinished:
			metrics.finishedAt = now
			if event.OutputTokens > 0 {
				metrics.outputTokens = event.OutputTokens
				metrics.outputProvided = true
			}
		case modeladapter.ModelEventKindProviderError:
			if event.Err != nil {
				return event.Err
			}
			return errors.New("provider error")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if metrics.finishedAt.IsZero() {
		metrics.finishedAt = time.Now().UTC()
	}
	metrics.rawResponse = observer.RawResponse()
	if strings.TrimSpace(metrics.rawResponse) == "" {
		metrics.rawResponse = strings.TrimSpace(metrics.text.String())
	}
	return metrics, nil
}

func (s *ProxyService) getRunningModelAdapterTestResult(adapterID string, requestHash string) (ModelAdapterTestResult, bool) {
	s.modelTestMu.RLock()
	defer s.modelTestMu.RUnlock()

	if s.modelTestResults == nil {
		return ModelAdapterTestResult{}, false
	}
	result, ok := s.modelTestResults[adapterID]
	if !ok {
		return ModelAdapterTestResult{}, false
	}
	if strings.TrimSpace(result.Status) != string(ModelAdapterTestStatusRunning) {
		return ModelAdapterTestResult{}, false
	}
	if strings.TrimSpace(result.RequestHash) != strings.TrimSpace(requestHash) {
		return ModelAdapterTestResult{}, false
	}
	return result, true
}

func (s *ProxyService) storeAndEmitModelAdapterTestResult(result ModelAdapterTestResult) {
	if strings.TrimSpace(result.AdapterID) == "" {
		return
	}
	s.modelTestMu.Lock()
	if s.modelTestResults == nil {
		s.modelTestResults = make(map[string]ModelAdapterTestResult)
	}
	s.modelTestResults[result.AdapterID] = result
	snapshot := snapshotModelAdapterTestResultsLocked(s.modelTestResults)
	s.modelTestMu.Unlock()
	s.emitModelAdapterTestResults(snapshot)
}

func (s *ProxyService) snapshotModelAdapterTestResults() []ModelAdapterTestResult {
	s.modelTestMu.RLock()
	defer s.modelTestMu.RUnlock()
	return snapshotModelAdapterTestResultsLocked(s.modelTestResults)
}

func snapshotModelAdapterTestResultsLocked(items map[string]ModelAdapterTestResult) []ModelAdapterTestResult {
	if len(items) == 0 {
		return []ModelAdapterTestResult{}
	}
	results := make([]ModelAdapterTestResult, 0, len(items))
	for _, item := range items {
		results = append(results, item)
	}
	sort.Slice(results, func(i int, j int) bool {
		if results[i].TestedAt == results[j].TestedAt {
			return results[i].AdapterID < results[j].AdapterID
		}
		return results[i].TestedAt > results[j].TestedAt
	})
	return results
}

func (s *ProxyService) emitModelAdapterTestResults(results []ModelAdapterTestResult) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit(modelAdapterTestUpdatedEvent, ModelAdapterTestResultsPayload{
		Results: results,
	})
}

func buildErroredModelAdapterTestResult(adapterID string, requestHash string, err error) ModelAdapterTestResult {
	message := strings.TrimSpace(modelAdapterTestErrorMessage(err))
	summary := buildModelAdapterTestErrorSummary(err)
	return ModelAdapterTestResult{
		AdapterID:   strings.TrimSpace(adapterID),
		RequestHash: strings.TrimSpace(requestHash),
		Status:      string(ModelAdapterTestStatusError),
		SummaryText: summary,
		Error:       summary,
		RawResponse: message,
		TestedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func buildModelAdapterTestSummaryText(result ModelAdapterTestResult) string {
	if strings.TrimSpace(result.Status) != string(ModelAdapterTestStatusSuccess) {
		return firstNonEmptyTrimmed(result.SummaryText, "测试失败")
	}
	return fmt.Sprintf("%d t/s | 首字 %s", int(math.Round(maxFloat64(result.TokensPerSecond, 0))), formatModelAdapterTestDuration(result.FirstTextTokenMS))
}

func buildModelAdapterHTTPStatusError(prefix string, resp *http.Response) error {
	if resp == nil {
		return fmt.Errorf("%s response is nil", strings.TrimSpace(prefix))
	}
	limitedBody, err := io.ReadAll(io.LimitReader(resp.Body, modelAdapterTestMaxErrorBodyBytes))
	if err != nil {
		if retrySummary := modeladapter.ProviderRetryAttemptSummary(resp); retrySummary != "" {
			return fmt.Errorf("%s status=%d %s body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, err)
		}
		return fmt.Errorf("%s status=%d body_read_error=%v", strings.TrimSpace(prefix), resp.StatusCode, err)
	}
	retrySummary := modeladapter.ProviderRetryAttemptSummary(resp)
	bodyText := strings.TrimSpace(string(limitedBody))
	if bodyText == "" {
		if retrySummary != "" {
			return fmt.Errorf("%s status=%d %s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary)
		}
		return fmt.Errorf("%s status=%d", strings.TrimSpace(prefix), resp.StatusCode)
	}
	if retrySummary != "" {
		return fmt.Errorf("%s status=%d %s body=%s", strings.TrimSpace(prefix), resp.StatusCode, retrySummary, bodyText)
	}
	return fmt.Errorf("%s status=%d body=%s", strings.TrimSpace(prefix), resp.StatusCode, bodyText)
}

func buildModelAdapterProviderBodyError(prefix string, body []byte) error {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	errorValue, ok := payload["error"]
	if !ok || errorValue == nil {
		return nil
	}
	message := ""
	details := make([]string, 0, 2)
	switch value := errorValue.(type) {
	case string:
		message = strings.TrimSpace(value)
	case map[string]any:
		message = strings.TrimSpace(fmt.Sprint(value["message"]))
		if errorType := strings.TrimSpace(fmt.Sprint(value["type"])); errorType != "" && errorType != "<nil>" {
			details = append(details, "type="+errorType)
		}
		if code := strings.TrimSpace(fmt.Sprint(value["code"])); code != "" && code != "<nil>" {
			details = append(details, "code="+code)
		}
	default:
		message = strings.TrimSpace(fmt.Sprint(value))
	}
	if message == "" || message == "<nil>" {
		message = "provider returned error response"
	}
	summary := strings.TrimSpace(prefix)
	if summary == "" {
		summary = "model adapter"
	}
	if len(details) > 0 {
		return fmt.Errorf("%s provider error %s: %s", summary, strings.Join(details, " "), message)
	}
	return fmt.Errorf("%s provider error: %s", summary, message)
}

func modelAdapterTestErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "模型测试失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "模型测试超时，请稍后重试"
	}
	return message
}

func buildModelAdapterTestErrorSummary(err error) string {
	if err == nil {
		return "测试失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "测试超时"
	}
	message := strings.TrimSpace(err.Error())
	switch {
	case strings.Contains(message, modelAdapterTestEmptyTextError):
		return "无正文返回"
	case strings.Contains(strings.ToLower(message), "context canceled"):
		return "测试已停止"
	default:
		return "测试失败"
	}
}

func formatModelAdapterTestDuration(durationMS int64) string {
	if durationMS < 1000 {
		if durationMS < 0 {
			durationMS = 0
		}
		return fmt.Sprintf("%d ms", durationMS)
	}
	seconds := float64(durationMS) / 1000
	return fmt.Sprintf("%.1f s", seconds)
}

func estimateBenchmarkTextTokens(text string) int64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runeCount := utf8.RuneCountInString(trimmed)
	if runeCount <= 0 {
		return 0
	}
	estimated := int64((runeCount + 3) / 4)
	estimated += int64(strings.Count(trimmed, "\n"))
	if estimated < 1 {
		return 1
	}
	return estimated
}

func buildModelAdapterTestCacheKey(adapter serverconfig.ModelAdapterConfig, requestHash string) string {
	baseURL, baseURLErr := modelchannel.NormalizeBaseURL(adapter.BaseURL)
	if baseURLErr == nil &&
		strings.TrimSpace(adapter.DisplayName) != "" &&
		strings.TrimSpace(adapter.ModelID) != "" &&
		strings.TrimSpace(adapter.APIKey) != "" {
		return modelchannel.BuildChannelID(baseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName, modelchannel.NormalizeOpenAIEndpoint(adapter.Type, adapter.OpenAIEndpoint))
	}
	return "invalid:" + strings.TrimSpace(requestHash)
}

func buildModelAdapterTestRequestHash(adapter serverconfig.ModelAdapterConfig) string {
	source := normalizeModelAdapterTestHashSource(adapter)
	payload := strings.Join([]string{
		source.Type,
		source.BaseURL,
		source.APIKey,
		source.ModelID,
		source.ReasoningEffort,
		source.OpenAIEndpoint,
		strconv.Itoa(source.OpenAIExtraParamsEnabled),
		source.OpenAIExtraParamsJSON,
		strconv.Itoa(source.CustomHeadersEnabled),
		source.CustomHeadersJSON,
		strconv.Itoa(source.AnthropicExtraParamsEnabled),
		source.AnthropicExtraParamsJSON,
		strconv.Itoa(source.ContextWindowTokens),
		strconv.Itoa(source.MaxCompletionTokens),
		strconv.Itoa(source.AnthropicMaxTokens),
		source.AnthropicThinkingEffort,
	}, "\n")
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(payload))
	sum := hasher.Sum(nil)
	return hex.EncodeToString(sum)
}

type modelAdapterTestHashSource struct {
	Type                        string
	BaseURL                     string
	APIKey                      string
	ModelID                     string
	ReasoningEffort             string
	OpenAIEndpoint              string
	OpenAIExtraParamsEnabled    int
	OpenAIExtraParamsJSON       string
	CustomHeadersEnabled        int
	CustomHeadersJSON           string
	AnthropicExtraParamsEnabled int
	AnthropicExtraParamsJSON    string
	ContextWindowTokens         int
	MaxCompletionTokens         int
	AnthropicMaxTokens          int
	AnthropicThinkingEffort     string
}

func normalizeModelAdapterTestHashSource(adapter serverconfig.ModelAdapterConfig) modelAdapterTestHashSource {
	baseURL := strings.TrimSpace(adapter.BaseURL)
	if normalizedBaseURL, err := modelchannel.NormalizeBaseURL(adapter.BaseURL); err == nil {
		baseURL = normalizedBaseURL
	}
	return modelAdapterTestHashSource{
		Type:                        normalizeModelAdapterTestType(adapter.Type),
		BaseURL:                     baseURL,
		APIKey:                      strings.TrimSpace(adapter.APIKey),
		ModelID:                     strings.TrimSpace(adapter.ModelID),
		ReasoningEffort:             normalizeModelAdapterTestProviderReasoning(adapter),
		OpenAIEndpoint:              modelchannel.NormalizeOpenAIEndpoint(adapter.Type, adapter.OpenAIEndpoint),
		OpenAIExtraParamsEnabled:    normalizeModelAdapterTestBool(adapter.Type == "openai" && adapter.OpenAIExtraParamsEnabled),
		OpenAIExtraParamsJSON:       normalizeModelAdapterTestOpenAIExtraParamsJSON(adapter),
		CustomHeadersEnabled:        normalizeModelAdapterTestBool(adapter.CustomHeadersEnabled),
		CustomHeadersJSON:           normalizeModelAdapterTestCustomHeadersJSON(adapter),
		AnthropicExtraParamsEnabled: normalizeModelAdapterTestBool(adapter.Type == "anthropic" && adapter.AnthropicExtraParamsEnabled),
		AnthropicExtraParamsJSON:    normalizeModelAdapterTestAnthropicExtraParamsJSON(adapter),
		ContextWindowTokens:         normalizeModelAdapterTestInt(adapter.ContextWindowTokens),
		MaxCompletionTokens:         normalizeModelAdapterTestInt(adapter.MaxCompletionTokens),
		AnthropicMaxTokens:          normalizeModelAdapterTestInt(adapter.AnthropicMaxTokens),
		AnthropicThinkingEffort:     normalizeModelAdapterTestProviderAnthropicThinkingEffort(adapter),
	}
}

func normalizeModelAdapterTestType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anthropic":
		return "anthropic"
	case "openai":
		return "openai"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func normalizeModelAdapterTestReasoning(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "medium"
	}
}

func normalizeModelAdapterTestProviderReasoning(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "openai" {
		return ""
	}
	return normalizeModelAdapterTestReasoning(adapter.ReasoningEffort)
}

func normalizeModelAdapterTestAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "xhigh"
	}
}

func normalizeModelAdapterTestProviderAnthropicThinkingEffort(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "anthropic" {
		return ""
	}
	return normalizeModelAdapterTestAnthropicThinkingEffort(adapter.AnthropicThinkingEffort)
}

func modelAdapterTestConfiguredAnthropicMaxTokens(adapter serverconfig.ModelAdapterConfig) int {
	if adapter.AnthropicMaxTokens > 0 {
		return adapter.AnthropicMaxTokens
	}
	if adapter.MaxCompletionTokens > 0 {
		return adapter.MaxCompletionTokens
	}
	return modelAdapterTestDefaultMaxTokens
}

func modelAdapterTestConfiguredOpenAIMaxTokens(adapter serverconfig.ModelAdapterConfig) int {
	if adapter.MaxCompletionTokens > 0 {
		return adapter.MaxCompletionTokens
	}
	if adapter.AnthropicMaxTokens > 0 {
		return adapter.AnthropicMaxTokens
	}
	return modelAdapterTestDefaultMaxTokens
}

func normalizeModelAdapterTestBool(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeModelAdapterTestOpenAIExtraParamsJSON(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "openai" || !adapter.OpenAIExtraParamsEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.OpenAIExtraParamsJSON)
}

func normalizeModelAdapterTestCustomHeadersJSON(adapter serverconfig.ModelAdapterConfig) string {
	if !adapter.CustomHeadersEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.CustomHeadersJSON)
}

func normalizeModelAdapterTestAnthropicExtraParamsJSON(adapter serverconfig.ModelAdapterConfig) string {
	if normalizeModelAdapterTestType(adapter.Type) != "anthropic" || !adapter.AnthropicExtraParamsEnabled {
		return ""
	}
	return strings.TrimSpace(adapter.AnthropicExtraParamsJSON)
}

func normalizeModelAdapterTestInt(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func maxFloat64(value float64, fallback float64) float64 {
	if value < fallback {
		return fallback
	}
	return value
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
