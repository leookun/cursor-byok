// providers.go — Provider 分层配置 ↔ 扁平 ModelAdapter 互转。
//
// 落盘真相源：config.yaml → providers[]
// 运行时兼容：NormalizeConfig 始终将 providers 展开为 modelAdapters
// 路由键：Ref = "{providerID}:{modelID}"（全局唯一，供 AOS / SelectChannel 绑定）
// API 调用：仍使用 model.ModelID（上游真实模型名）
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var nonSlugRE = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// BuildModelRef 构造全局唯一路由键：providerID:modelID
func BuildModelRef(providerID, modelID string) string {
	p := strings.TrimSpace(providerID)
	m := strings.TrimSpace(modelID)
	if p == "" || m == "" {
		return m
	}
	return p + ":" + m
}

// ProviderHostFromBaseURL 从 baseURL 提取主机名（用于默认 provider 名）。
func ProviderHostFromBaseURL(baseURL string) string {
	text := strings.TrimSpace(baseURL)
	if text == "" {
		return "unknown"
	}
	if u, err := url.Parse(text); err == nil && u.Host != "" {
		return u.Host
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "https://"), "http://")
	if i := strings.IndexByte(text, '/'); i >= 0 {
		text = text[:i]
	}
	if text == "" {
		return "unknown"
	}
	return text
}

// SlugProviderID 将主机名/名称转为稳定 provider ID。
func slugProviderID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = nonSlugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "provider"
	}
	return s
}

// stableProviderID 生成确定性 provider ID：hostname slug + SHA-256 前 8 位 hex。
// 相同 baseURL+type 永远生成相同 ID，保证 AOS adapterID 绑定在保存/加载周期中不失效。
func stableProviderID(baseURL, pType string) string {
	host := ProviderHostFromBaseURL(baseURL)
	hash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(baseURL)) + "\n" + strings.TrimSpace(pType)))
	hashHex := hex.EncodeToString(hash[:])[:8]
	idBase := slugProviderID(host)
	if pType != "" && pType != "openai" {
		idBase = slugProviderID(host + "-" + pType)
	}
	return idBase + "-" + hashHex
}

// NormalizeProviders 归一化 providers 列表（trim、补 ID、去重 ID）。
func NormalizeProviders(input []ProviderConfig) []ProviderConfig {
	if len(input) == 0 {
		return []ProviderConfig{}
	}
	out := make([]ProviderConfig, 0, len(input))
	seen := make(map[string]int, len(input))
	for _, p := range input {
		baseURL := strings.TrimSpace(p.BaseURL)
		id := strings.TrimSpace(p.ID)
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = ProviderHostFromBaseURL(baseURL)
		}
		// Always regenerate deterministic ID from baseURL+type.
		// Old slug-based IDs are replaced on first save — user reconfigures AOS bindings once.
		id = stableProviderID(baseURL, p.Type)
		// 保证 ID 唯一
		if n, ok := seen[id]; ok {
			seen[id] = n + 1
			id = fmt.Sprintf("%s-%d", id, n+1)
		} else {
			seen[id] = 1
		}
		models := make([]ModelInProviderConfig, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, ModelInProviderConfig{
				DisplayName:                 strings.TrimSpace(m.DisplayName),
				TooltipData:                 strings.TrimSpace(m.TooltipData),
				ModelID:                     strings.TrimSpace(m.ModelID),
				ReasoningEffort:             strings.TrimSpace(m.ReasoningEffort),
				OpenAIEndpoint:              strings.TrimSpace(m.OpenAIEndpoint),
				OpenAIExtraParamsEnabled:    m.OpenAIExtraParamsEnabled,
				OpenAIExtraParamsJSON:       strings.TrimSpace(m.OpenAIExtraParamsJSON),
				CustomHeadersEnabled:        m.CustomHeadersEnabled,
				CustomHeadersJSON:           strings.TrimSpace(m.CustomHeadersJSON),
				AnthropicExtraParamsEnabled: m.AnthropicExtraParamsEnabled,
				AnthropicExtraParamsJSON:    strings.TrimSpace(m.AnthropicExtraParamsJSON),
				ContextWindowTokens:         m.ContextWindowTokens,
				MaxCompletionTokens:         m.MaxCompletionTokens,
				AnthropicMaxTokens:          m.AnthropicMaxTokens,
				AnthropicThinkingEffort:     strings.TrimSpace(m.AnthropicThinkingEffort),
				ThinkingBudgetTokens:        m.ThinkingBudgetTokens,
			})
		}
		// Migrate APIKey → APIKeys: if APIKeys is empty but APIKey exists, copy it.
		apiKeys := normalizeAPIKeys(p.APIKeys, p.APIKey)
		// Keep legacy APIKey in sync with primary key.
		primaryKey := ""
		if len(apiKeys) > 0 {
			primaryKey = apiKeys[0]
		}
		out = append(out, ProviderConfig{
			ID:      id,
			Name:    name,
			Type:    strings.TrimSpace(p.Type),
			BaseURL: baseURL,
			APIKey:  primaryKey,
			APIKeys: apiKeys,
			Models:  models,
		})
	}
	return out
}

// GroupAdaptersToProviders 将旧版扁平 modelAdapters 按 baseURL+type 归并为 providers。
// 同一 baseURL+type 下不同 apiKey 的 adapter 合并为同一 provider 的多个 keys。
// 用于首次加载旧配置时的自动迁移。
func GroupAdaptersToProviders(adapters []ModelAdapterConfig) []ProviderConfig {
	if len(adapters) == 0 {
		return []ProviderConfig{}
	}
	type bucket struct {
		order int
		p     ProviderConfig
	}
	order := 0
	byKey := make(map[string]*bucket)
	var keys []string

	for _, a := range adapters {
		baseURL := strings.TrimSpace(a.BaseURL)
		pType := strings.TrimSpace(a.Type)
		// Group by baseURL+type only (not apiKey) to support multi-key providers.
		key := baseURL + "\n" + pType
		b, ok := byKey[key]
		if !ok {
			host := ProviderHostFromBaseURL(baseURL)
			b = &bucket{
				order: order,
				p: ProviderConfig{
					ID:      stableProviderID(baseURL, pType),
					Name:    host,
					Type:    pType,
					BaseURL: baseURL,
					Models:  nil,
				},
			}
			order++
			byKey[key] = b
			keys = append(keys, key)
		}
		// Collect unique API keys.
		apiKey := strings.TrimSpace(a.APIKey)
		if apiKey != "" {
			found := false
			for _, k := range b.p.APIKeys {
				if k == apiKey {
					found = true
					break
				}
			}
			if !found {
				b.p.APIKeys = append(b.p.APIKeys, apiKey)
			}
		}
		displayName := strings.TrimSpace(a.DisplayName)
		modelID := strings.TrimSpace(a.ModelID)
		if displayName == "" {
			displayName = modelID
		}
		tooltip := strings.TrimSpace(a.TooltipData)
		if tooltip == "" {
			tooltip = b.p.Name
		}
		// 若 displayName 被改成带供应商后缀但 modelID 也一起改了，
		// 迁移时保留 modelID 原样（API 名）；用户可在 UI 再改。
		b.p.Models = append(b.p.Models, ModelInProviderConfig{
			DisplayName:                 displayName,
			TooltipData:                 tooltip,
			ModelID:                     modelID,
			ReasoningEffort:             a.ReasoningEffort,
			OpenAIEndpoint:              a.OpenAIEndpoint,
			OpenAIExtraParamsEnabled:    a.OpenAIExtraParamsEnabled,
			OpenAIExtraParamsJSON:       a.OpenAIExtraParamsJSON,
			CustomHeadersEnabled:        a.CustomHeadersEnabled,
			CustomHeadersJSON:           a.CustomHeadersJSON,
			AnthropicExtraParamsEnabled: a.AnthropicExtraParamsEnabled,
			AnthropicExtraParamsJSON:    a.AnthropicExtraParamsJSON,
			ContextWindowTokens:         a.ContextWindowTokens,
			MaxCompletionTokens:         a.MaxCompletionTokens,
			AnthropicMaxTokens:          a.AnthropicMaxTokens,
			AnthropicThinkingEffort:     a.AnthropicThinkingEffort,
			ThinkingBudgetTokens:        a.ThinkingBudgetTokens,
		})
	}

	// 保证 provider ID 唯一 + set legacy APIKey from first key.
	seen := make(map[string]int, len(keys))
	out := make([]ProviderConfig, 0, len(keys))
	for _, k := range keys {
		p := byKey[k].p
		id := p.ID
		if n, ok := seen[id]; ok {
			seen[id] = n + 1
			p.ID = fmt.Sprintf("%s-%d", id, n+1)
		} else {
			seen[id] = 1
		}
		if len(p.APIKeys) > 0 {
			p.APIKey = p.APIKeys[0]
		}
		out = append(out, p)
	}
	return out
}

// FlattenProvidersToAdapters 将 providers 展开为运行时扁平 modelAdapters。
// 每个 adapter 带 Ref="{providerID}:{modelID}"，API 仍用 ModelID。
// 使用 provider 的第一个 API key（APIKeys[0] 或 APIKey fallback）。
func FlattenProvidersToAdapters(providers []ProviderConfig) ([]ModelAdapterConfig, error) {
	if len(providers) == 0 {
		return []ModelAdapterConfig{}, nil
	}
	flat := make([]ModelAdapterConfig, 0)
	for _, p := range NormalizeProviders(providers) {
		pType := normalizeModelAdapterType(p.Type)
		if pType == "" {
			pType = "openai"
		}
		// Use primary key from APIKeys, fallback to legacy APIKey.
		primaryKey := p.APIKey
		if len(p.APIKeys) > 0 {
			primaryKey = p.APIKeys[0]
		}
		for _, m := range p.Models {
			modelID := strings.TrimSpace(m.ModelID)
			if modelID == "" {
				continue
			}
			displayName := strings.TrimSpace(m.DisplayName)
			if displayName == "" {
				displayName = modelID
			}
			tooltip := strings.TrimSpace(m.TooltipData)
			if tooltip == "" {
				tooltip = p.Name
			}
			if tooltip == "" {
				tooltip = p.ID
			}
			flat = append(flat, ModelAdapterConfig{
				Ref:                         BuildModelRef(p.ID, modelID),
				DisplayName:                 displayName,
				Type:                        pType,
				BaseURL:                     p.BaseURL,
				APIKey:                      primaryKey,
				TooltipData:                 tooltip,
				ModelID:                     modelID,
				ReasoningEffort:             m.ReasoningEffort,
				OpenAIEndpoint:              m.OpenAIEndpoint,
				OpenAIExtraParamsEnabled:    m.OpenAIExtraParamsEnabled,
				OpenAIExtraParamsJSON:       m.OpenAIExtraParamsJSON,
				CustomHeadersEnabled:        m.CustomHeadersEnabled,
				CustomHeadersJSON:           m.CustomHeadersJSON,
				AnthropicExtraParamsEnabled: m.AnthropicExtraParamsEnabled,
				AnthropicExtraParamsJSON:    m.AnthropicExtraParamsJSON,
				ContextWindowTokens:         m.ContextWindowTokens,
				MaxCompletionTokens:         m.MaxCompletionTokens,
				AnthropicMaxTokens:          m.AnthropicMaxTokens,
				AnthropicThinkingEffort:     m.AnthropicThinkingEffort,
				ThinkingBudgetTokens:        m.ThinkingBudgetTokens,
			})
		}
	}
	return NormalizeModelAdapterConfigs(flat)
}

// normalizeAPIKeys merges legacy APIKey into APIKeys, trims and deduplicates.
func normalizeAPIKeys(apiKeys []string, legacyKey string) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, k := range apiKeys {
		t := strings.TrimSpace(k)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	// Migrate legacy single key if not already present.
	if lt := strings.TrimSpace(legacyKey); lt != "" {
		if _, ok := seen[lt]; !ok {
			out = append([]string{lt}, out...)
		}
	}
	return out
}