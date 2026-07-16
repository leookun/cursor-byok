package modelchannel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const ChannelIDHexLength = 16

const GroupIDHexLength = 32

const GroupIDPrefix = "grp_"

const (
	OpenAIEndpointResponses       = "/v1/responses"
	OpenAIEndpointChatCompletions = "/v1/chat/completions"
	OpenAIEndpointCustom          = "/custom"
)

func NormalizeBaseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("模型适配器 baseURL 不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("模型适配器 baseURL 不是合法 URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("模型适配器 baseURL 仅支持 http 或 https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("模型适配器 baseURL 缺少主机名")
	}
	parsed.Scheme = strings.ToLower(strings.TrimSpace(parsed.Scheme))
	parsed.Host = strings.ToLower(strings.TrimSpace(parsed.Host))
	normalized := strings.TrimRight(parsed.String(), "/")
	if normalized == "" {
		normalized = parsed.String()
	}
	return normalized, nil
}

// NormalizeOpenAIEndpoint 归一化 OpenAI endpoint 路径。
// 支持三个预设值：/v1/responses、/v1/chat/completions、/custom（自定义路径）。
// 选 /custom 时，用户需在接口地址栏填写完整请求 URL。
func NormalizeOpenAIEndpoint(providerType string, endpoint string) string {
	if strings.TrimSpace(strings.ToLower(providerType)) != "openai" {
		return ""
	}
	normalized := strings.TrimSpace(endpoint)
	switch normalized {
	case "":
		return OpenAIEndpointResponses
	case OpenAIEndpointResponses, OpenAIEndpointChatCompletions, OpenAIEndpointCustom:
		return normalized
	default:
		return ""
	}
}

// OpenAIEndpointShape 根据 endpoint 路径末段推断协议形态。
// 返回 "responses"（Responses API）或 "chat/completions"（Chat Completions API）。
// 这样 /v1/chat/completions、/v4/chat/completions、/chat/completions 都走同一协议分支。
func OpenAIEndpointShape(endpoint string) string {
	lower := strings.ToLower(strings.TrimSpace(endpoint))
	switch {
	case strings.HasSuffix(lower, "/responses"):
		return "responses"
	default:
		return "chat/completions"
	}
}

func BuildLegacyChannelID(baseURL string, modelID string, apiKey string, name string) string {
	return buildChannelID([]string{
		strings.TrimSpace(baseURL),
		strings.TrimSpace(modelID),
		strings.TrimSpace(apiKey),
		strings.TrimSpace(name),
	})
}

func BuildChannelID(baseURL string, modelID string, apiKey string, name string, openAIEndpoint string) string {
	endpoint := strings.TrimSpace(openAIEndpoint)
	if endpoint == "" {
		return BuildLegacyChannelID(baseURL, modelID, apiKey, name)
	}
	return buildChannelID([]string{
		strings.TrimSpace(baseURL),
		strings.TrimSpace(modelID),
		strings.TrimSpace(apiKey),
		strings.TrimSpace(name),
		endpoint,
	})
}

func BuildGroupID(providerType string, baseURL string, apiKey string, customHeadersEnabled bool, customHeadersJSON string) string {
	return GroupIDPrefix + buildIdentityID([]string{
		strings.ToLower(strings.TrimSpace(providerType)),
		strings.TrimSpace(baseURL),
		strings.TrimSpace(apiKey),
		normalizeGroupHeaders(customHeadersEnabled, customHeadersJSON),
	}, GroupIDHexLength)
}

func normalizeGroupHeaders(enabled bool, headersJSON string) string {
	if !enabled {
		return ""
	}
	text := strings.TrimSpace(headersJSON)
	var headers map[string]string
	if err := json.Unmarshal([]byte(text), &headers); err != nil || headers == nil {
		return text
	}
	type headerPair struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	pairs := make([]headerPair, 0, len(headers))
	for name, value := range headers {
		pairs = append(pairs, headerPair{Name: strings.ToLower(strings.TrimSpace(name)), Value: value})
	}
	sort.Slice(pairs, func(left int, right int) bool {
		if pairs[left].Name == pairs[right].Name {
			return pairs[left].Value < pairs[right].Value
		}
		return pairs[left].Name < pairs[right].Name
	})
	payload, err := json.Marshal(pairs)
	if err != nil {
		return text
	}
	return string(payload)
}

func buildChannelID(parts []string) string {
	return buildIdentityID(parts, ChannelIDHexLength)
}

func buildIdentityID(parts []string, length int) string {
	payload := strings.Join(parts, "\n")
	hashBytes := sha256.Sum256([]byte(payload))
	encoded := hex.EncodeToString(hashBytes[:])
	if length <= 0 || length > len(encoded) {
		return encoded
	}
	return encoded[:length]
}
