package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
	"cursor/internal/modelchannel"
)

const (
	// providerModelListTimeout 限制单次模型列表拉取的最大耗时。
	providerModelListTimeout = 20 * time.Second
	// providerModelListMaxBody 限制读取的响应体大小，避免异常响应占用过多内存。
	providerModelListMaxBody = 1 << 20
	// anthropicModelsAPIVersion 为 Anthropic 模型列表接口要求的版本头。
	anthropicModelsAPIVersion = "2023-06-01"
)

// ProviderModelEntry 表示从远端模型列表接口拉取到的单个模型条目。
type ProviderModelEntry struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	OwnedBy     string `json:"ownedBy,omitempty"`
}

// FetchProviderModels 依据给定的模型配置（type/baseURL/apiKey）调用远端模型列表接口，
// 返回去空去重并按 ID 排序后的可用模型清单。
func (s *ProxyService) FetchProviderModels(adapter serverconfig.ModelAdapterConfig) ([]ProviderModelEntry, error) {
	normalized, err := normalizeSingleModelAdapterConfig(adapter)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), providerModelListTimeout)
	defer cancel()

	entries, err := s.fetchProviderModels(ctx, normalized)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("拉取模型超时，请稍后重试")
		}
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("接口未返回任何模型")
	}
	return entries, nil
}

// fetchProviderModels 按服务商类型分派到具体的模型列表抓取实现。
func (s *ProxyService) fetchProviderModels(ctx context.Context, adapter serverconfig.ModelAdapterConfig) ([]ProviderModelEntry, error) {
	switch strings.TrimSpace(adapter.Type) {
	case "openai":
		return s.fetchOpenAICompatibleModels(ctx, adapter)
	case "anthropic":
		return s.fetchAnthropicModels(ctx, adapter)
	default:
		return nil, fmt.Errorf("unsupported provider %q", strings.TrimSpace(adapter.Type))
	}
}

// resolveProviderModelsEndpoint 规范化 baseURL 并拼出模型列表端点：
// 已以 /v1 结尾则追加 /models，否则追加 /v1/models。
func resolveProviderModelsEndpoint(adapter serverconfig.ModelAdapterConfig, invalidHint string) (string, error) {
	baseURL, err := modelchannel.NormalizeBaseURL(strings.TrimSpace(adapter.BaseURL))
	if err != nil {
		hint := strings.TrimSpace(invalidHint)
		if hint != "" {
			return "", errors.New(hint)
		}
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(baseURL), "/v1") {
		return baseURL + "/models", nil
	}
	return baseURL + "/v1/models", nil
}

// fetchProviderModelList 执行模型列表的 HTTP 抓取：构造请求、应用鉴权头与自定义头、读取并解析响应。
// auth 按 provider 设置鉴权头，parser 按 provider 解析响应体，其余流程对所有 provider 一致。
func (s *ProxyService) fetchProviderModelList(
	ctx context.Context,
	adapter serverconfig.ModelAdapterConfig,
	invalidHint string,
	auth func(*http.Request),
	parser func([]byte) []ProviderModelEntry,
) ([]ProviderModelEntry, error) {
	endpoint, err := resolveProviderModelsEndpoint(adapter, invalidHint)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	auth(req)
	applyProviderModelListCustomHeaders(req, adapter)

	resp, err := s.publicClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readProviderModelListBody(resp)
	if err != nil {
		return nil, err
	}
	return dedupSortProviderModelEntries(parser(body)), nil
}

// fetchOpenAICompatibleModels 调用 OpenAI 兼容的 /models 端点并解析模型清单。
func (s *ProxyService) fetchOpenAICompatibleModels(ctx context.Context, adapter serverconfig.ModelAdapterConfig) ([]ProviderModelEntry, error) {
	return s.fetchProviderModelList(ctx, adapter, "OpenAI 接口地址不合法",
		func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(adapter.APIKey))
		},
		parseOpenAICompatibleModelEntries,
	)
}

// fetchAnthropicModels 调用 Anthropic 的 /v1/models 端点并解析模型清单。
func (s *ProxyService) fetchAnthropicModels(ctx context.Context, adapter serverconfig.ModelAdapterConfig) ([]ProviderModelEntry, error) {
	return s.fetchProviderModelList(ctx, adapter, "Anthropic 接口地址不合法",
		func(req *http.Request) {
			req.Header.Set("x-api-key", strings.TrimSpace(adapter.APIKey))
			req.Header.Set("anthropic-version", anthropicModelsAPIVersion)
		},
		parseAnthropicModelEntries,
	)
}

// readProviderModelListBody 统一处理远端响应：非 2xx 复用测速的错误摘要风格，
// 成功则返回响应体；额外多读 1 字节以判断是否超过上限被截断，避免解析失败时误报"无模型"。
func readProviderModelListBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, buildModelAdapterHTTPStatusError("拉取模型", resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerModelListMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}
	if int64(len(body)) > providerModelListMaxBody {
		return nil, fmt.Errorf("模型列表响应过大（超过 %d 字节），可能被截断", providerModelListMaxBody)
	}
	return body, nil
}

// applyProviderModelListCustomHeaders 在启用自定义请求头时，将其覆盖到请求上（同名以用户配置为准）。
func applyProviderModelListCustomHeaders(req *http.Request, adapter serverconfig.ModelAdapterConfig) {
	if !adapter.CustomHeadersEnabled {
		return
	}
	trimmed := strings.TrimSpace(adapter.CustomHeadersJSON)
	if trimmed == "" {
		return
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(trimmed), &headers); err != nil {
		logger.Errorf("解析模型列表自定义请求头失败，已忽略: %v", err)
		return
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
}

// parseOpenAICompatibleModelEntries 兼容解析 OpenAI 风格的模型列表响应：
// 优先 {data:[...]}，其次 {models:[...]}，最后裸数组 [...]，兼容不同网关的返回形态。
// 展示名优先取 display_name，其次 name，最终在去重阶段回退为 ID。
func parseOpenAICompatibleModelEntries(body []byte) []ProviderModelEntry {
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		OwnedBy     string `json:"owned_by"`
	}
	toEntry := func(current item) ProviderModelEntry {
		return ProviderModelEntry{
			ID:          current.ID,
			DisplayName: firstNonEmptyTrimmed(current.DisplayName, current.Name),
			OwnedBy:     current.OwnedBy,
		}
	}

	var wrapper struct {
		Data   []item `json:"data"`
		Models []item `json:"models"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && (len(wrapper.Data) > 0 || len(wrapper.Models) > 0) {
		entries := make([]ProviderModelEntry, 0, len(wrapper.Data)+len(wrapper.Models))
		for _, current := range wrapper.Data {
			entries = append(entries, toEntry(current))
		}
		for _, current := range wrapper.Models {
			entries = append(entries, toEntry(current))
		}
		return entries
	}

	var bareArray []item
	if err := json.Unmarshal(body, &bareArray); err == nil {
		entries := make([]ProviderModelEntry, 0, len(bareArray))
		for _, current := range bareArray {
			entries = append(entries, toEntry(current))
		}
		return entries
	}
	return nil
}

// parseAnthropicModelEntries 解析 Anthropic 风格的模型列表响应 {data:[{id,display_name,type}]}。
func parseAnthropicModelEntries(body []byte) []ProviderModelEntry {
	var wrapper struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Type        string `json:"type"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil
	}
	entries := make([]ProviderModelEntry, 0, len(wrapper.Data))
	for _, current := range wrapper.Data {
		entries = append(entries, ProviderModelEntry{
			ID:          current.ID,
			DisplayName: current.DisplayName,
			OwnedBy:     current.Type,
		})
	}
	return entries
}

// dedupSortProviderModelEntries 去除空 ID 与重复项，修剪并回退缺失的展示名，再按 ID 升序排序。
func dedupSortProviderModelEntries(entries []ProviderModelEntry) []ProviderModelEntry {
	seen := make(map[string]struct{})
	unique := make([]ProviderModelEntry, 0, len(entries))
	for _, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		entry.ID = id
		entry.DisplayName = strings.TrimSpace(entry.DisplayName)
		entry.OwnedBy = strings.TrimSpace(entry.OwnedBy)
		if entry.DisplayName == "" {
			entry.DisplayName = id
		}
		unique = append(unique, entry)
	}
	sort.Slice(unique, func(i int, j int) bool {
		return unique[i].ID < unique[j].ID
	})
	return unique
}
