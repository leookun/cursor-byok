// model_fetch.go — 从上游 Provider 获取可用模型列表。
// 支持 OpenAI 兼容格式（GET /v1/models，Bearer 鉴权）和
// Anthropic 格式（GET /v1/models，x-api-key 鉴权）。
// URL 候选逻辑参考 cc-switch model_fetch.rs：
//   baseURL/v1/models → baseURL/models（fallback）
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const fetchModelsTimeout = 20 * time.Second

// FetchModelsRequest 是 FetchModelsFromProvider 的请求 DTO。
type FetchModelsRequest struct {
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Type    string `json:"type"` // "openai" | "anthropic"
}

// FetchModelsResult 是 FetchModelsFromProvider 的响应 DTO。
type FetchModelsResult struct {
	Models []string `json:"models"`
	// Error 非空时表示请求失败，Models 可能为空。
	Error string `json:"error,omitempty"`
}

// FetchModelsFromProvider 调用上游 /v1/models 接口，返回可用模型 ID 列表。
// 不会向上 panic；所有错误都通过 FetchModelsResult.Error 返回。
func (s *ProxyService) FetchModelsFromProvider(req FetchModelsRequest) (FetchModelsResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	apiKey := strings.TrimSpace(req.APIKey)
	providerType := strings.ToLower(strings.TrimSpace(req.Type))

	if baseURL == "" {
		return FetchModelsResult{Error: "baseURL 不能为空"}, nil
	}
	if apiKey == "" {
		return FetchModelsResult{Error: "apiKey 不能为空"}, nil
	}

	candidates := buildFetchModelURLCandidates(baseURL)

	ctx, cancel := context.WithTimeout(context.Background(), fetchModelsTimeout)
	defer cancel()

	var lastErr error
	for _, url := range candidates {
		models, err := doFetchModels(ctx, url, apiKey, providerType)
		if err == nil {
			return FetchModelsResult{Models: models}, nil
		}
		lastErr = err
	}

	return FetchModelsResult{
		Error: fmt.Sprintf("获取模型列表失败：%v", lastErr),
	}, nil
}

// buildFetchModelURLCandidates 根据 baseURL 构建候选请求 URL。
//
//   - 若 baseURL 以 /v1 结尾 → 直接追加 /models
//   - 否则依次尝试 /v1/models → /models
func buildFetchModelURLCandidates(baseURL string) []string {
	if strings.HasSuffix(baseURL, "/v1") {
		return []string{baseURL + "/models"}
	}
	return []string{
		baseURL + "/v1/models",
		baseURL + "/models",
	}
}

// openAIModelsResponse 对应 OpenAI /v1/models 的标准响应格式，
// Anthropic 的 /v1/models 也使用相同的 {data:[{id}]} 结构。
type openAIModelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func doFetchModels(ctx context.Context, url, apiKey, providerType string) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}

	// Anthropic 用 x-api-key；其余（OpenAI 兼容）用 Bearer。
	if providerType == "anthropic" {
		httpReq.Header.Set("x-api-key", apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	c := &http.Client{Timeout: fetchModelsTimeout}
	resp, err := c.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}

	var result openAIModelsResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败：%w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if id := strings.TrimSpace(m.ID); id != "" {
			models = append(models, id)
		}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Provider 未返回任何模型")
	}

	sort.Strings(models)
	return models, nil
}