package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cursor/internal/modelchannel"
)

var (
	// ErrInvalidSystemSetting 表示当前模块中的 ErrInvalidSystemSetting 状态值。
	ErrInvalidSystemSetting = errors.New("invalid system setting")
	// ErrChannelNotAvailable 表示当前没有可用模型渠道。
	ErrChannelNotAvailable = errors.New("model channel not available")
	// ErrChannelRateLimited 表示当前模型渠道被限流。
	ErrChannelRateLimited = errors.New("model channel rate limited")
)

const (
	// configurableChannelTimeoutMS 表示当前声明中的 configurableChannelTimeoutMS。
	configurableChannelTimeoutMS = int((2 * time.Hour) / time.Millisecond)
	// configurableChannelContextWindowTokens 表示当前声明中的默认上下文窗口大小。
	configurableChannelContextWindowTokens = 200_000
	// configurableChannelMaxTokens 表示当前声明中的 configurableChannelMaxTokens。
	configurableChannelMaxTokens = 65_536
	// configurableChannelThinkingBudgetTokens 表示当前声明中的 configurableChannelThinkingBudgetTokens。
	configurableChannelThinkingBudgetTokens = 4_096
	// configurableChannelAnthropicThinkingEffort 表示 Anthropic adaptive thinking 默认强度。
	configurableChannelAnthropicThinkingEffort = "xhigh"
)

// ModelAdapterConfig 定义了当前模块中的 ModelAdapterConfig 类型。
type ModelAdapterConfig struct {
	ID string `json:"id,omitempty"`
	// DisplayName 表示当前声明中的 DisplayName。
	DisplayName string `json:"displayName"`
	// Type 表示当前声明中的 Type。
	Type string `json:"type"`
	// BaseURL 表示当前声明中的 BaseURL。
	BaseURL string `json:"baseURL"`
	// APIKey 表示当前声明中的 APIKey。
	APIKey string `json:"apiKey"`
	// TooltipData 表示当前声明中的 TooltipData。
	TooltipData string `json:"tooltipData"`
	// ModelID 表示当前声明中的 ModelID。
	ModelID string `json:"modelID"`
	// ReasoningEffort 表示当前声明中的 ReasoningEffort。
	ReasoningEffort string `json:"reasoningEffort"`
	// OpenAIEndpoint 表示 OpenAI 兼容适配器使用的 API 端点。
	OpenAIEndpoint string `json:"openAIEndpoint"`
	// OpenAIExtraParamsEnabled 表示是否启用 OpenAI 额外请求参数。
	OpenAIExtraParamsEnabled bool `json:"openAIExtraParamsEnabled"`
	// OpenAIExtraParamsJSON 表示 OpenAI 额外请求参数 JSON 对象。
	OpenAIExtraParamsJSON string `json:"openAIExtraParamsJSON"`
	// CustomHeadersEnabled 表示是否启用自定义请求头。
	CustomHeadersEnabled bool `json:"customHeadersEnabled"`
	// CustomHeadersJSON 表示自定义请求头 JSON 对象。
	CustomHeadersJSON string `json:"customHeadersJSON"`
	// AnthropicExtraParamsEnabled 表示是否启用 Anthropic 额外请求参数。
	AnthropicExtraParamsEnabled bool `json:"anthropicExtraParamsEnabled"`
	// AnthropicExtraParamsJSON 表示 Anthropic 额外请求参数 JSON 对象。
	AnthropicExtraParamsJSON string `json:"anthropicExtraParamsJSON"`
	// ContextWindowTokens 表示当前声明中的 ContextWindowTokens。
	ContextWindowTokens int `json:"contextWindowTokens"`
	// MaxCompletionTokens 表示当前声明中的 MaxCompletionTokens。
	MaxCompletionTokens int `json:"maxCompletionTokens"`
	// AnthropicMaxTokens 表示当前声明中的 AnthropicMaxTokens。
	AnthropicMaxTokens int `json:"anthropicMaxTokens"`
	// AnthropicThinkingEffort 表示 Anthropic adaptive thinking 的 output_config.effort。
	AnthropicThinkingEffort string `json:"anthropicThinkingEffort,omitempty"`
	// ThinkingBudgetTokens 表示当前声明中的 ThinkingBudgetTokens。
	ThinkingBudgetTokens int `json:"thinkingBudgetTokens"`
}

// RuntimeConfigSnapshot 定义了当前模块中的 RuntimeConfigSnapshot 类型。
type RuntimeConfigSnapshot struct {
	// ObservabilityLogEnabled 表示当前声明中的 ObservabilityLogEnabled。
	ObservabilityLogEnabled bool
	// ProviderStreamIdleTimeout 表示 provider 流式响应无有效内容时的空闲超时，单位秒。
	ProviderStreamIdleTimeout int
	// ModelAdapters 表示当前声明中的 ModelAdapters。
	ModelAdapters []ModelAdapterConfig
}

// RuntimeConfigProvider 定义了当前模块中的 RuntimeConfigProvider 类型。
type RuntimeConfigProvider func(context.Context) (RuntimeConfigSnapshot, error)

// NormalizeModelAdapterConfigs 用于处理与 NormalizeModelAdapterConfigs 相关的逻辑。
func NormalizeModelAdapterConfigs(input []ModelAdapterConfig) ([]ModelAdapterConfig, error) {
	if len(input) == 0 {
		return []ModelAdapterConfig{}, nil
	}

	normalized := make([]ModelAdapterConfig, 0, len(input))
	seenChannelIDs := make(map[string]struct{}, len(input))
	for _, item := range input {
		baseURL, err := modelchannel.NormalizeBaseURL(item.BaseURL)
		if err != nil {
			return nil, err
		}
		next := ModelAdapterConfig{
			DisplayName:          strings.TrimSpace(item.DisplayName),
			Type:                 normalizeModelAdapterType(item.Type),
			BaseURL:              baseURL,
			APIKey:               strings.TrimSpace(item.APIKey),
			TooltipData:          strings.TrimSpace(item.TooltipData),
			ModelID:              strings.TrimSpace(item.ModelID),
			ReasoningEffort:      normalizeReasoningEffort(item.ReasoningEffort),
			OpenAIEndpoint:       modelchannel.NormalizeOpenAIEndpoint(item.Type, item.OpenAIEndpoint),
			ContextWindowTokens:  normalizeMaxCompletionTokens(item.ContextWindowTokens),
			MaxCompletionTokens:  normalizeMaxCompletionTokens(item.MaxCompletionTokens),
			AnthropicMaxTokens:   normalizeMaxCompletionTokens(item.AnthropicMaxTokens),
			ThinkingBudgetTokens: normalizeMaxCompletionTokens(item.ThinkingBudgetTokens),
		}
		if next.Type == "openai" {
			next.OpenAIExtraParamsEnabled = item.OpenAIExtraParamsEnabled
			next.OpenAIExtraParamsJSON = strings.TrimSpace(item.OpenAIExtraParamsJSON)
		} else if next.Type == "anthropic" {
			next.AnthropicThinkingEffort = normalizeAnthropicThinkingEffort(item.AnthropicThinkingEffort)
			next.AnthropicExtraParamsEnabled = item.AnthropicExtraParamsEnabled
			next.AnthropicExtraParamsJSON = strings.TrimSpace(item.AnthropicExtraParamsJSON)
		}
		next.CustomHeadersEnabled = item.CustomHeadersEnabled
		next.CustomHeadersJSON = strings.TrimSpace(item.CustomHeadersJSON)
		switch {
		case next.DisplayName == "":
			return nil, errors.New("模型适配器 displayName 不能为空")
		case next.Type == "":
			return nil, errors.New("模型适配器 type 仅支持 openai 或 anthropic")
		case next.APIKey == "":
			return nil, errors.New("模型适配器 apiKey 不能为空")
		case next.TooltipData == "":
			return nil, errors.New("模型适配器 tooltipData 不能为空")
		case next.ModelID == "":
			return nil, errors.New("模型适配器 modelID 不能为空")
		case next.Type == "openai" && next.ReasoningEffort == "":
			return nil, errors.New("模型适配器 reasoningEffort 仅支持 low、medium、high、xhigh")
		case next.Type == "openai" && next.OpenAIEndpoint == "":
			return nil, errors.New("模型适配器 openAIEndpoint 仅支持 /v1/responses 或 /v1/chat/completions")
		case next.Type == "openai" && next.OpenAIExtraParamsEnabled:
			if err := validateJSONMap(next.OpenAIExtraParamsJSON, "openAIExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.CustomHeadersEnabled:
			if err := validateHeadersJSON(next.CustomHeadersJSON); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicExtraParamsEnabled:
			if err := validateJSONMap(next.AnthropicExtraParamsJSON, "anthropicExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.Type == "anthropic" && next.AnthropicThinkingEffort == "":
			return nil, errors.New("模型适配器 anthropicThinkingEffort 仅支持 low、medium、high、xhigh、max")
		}
		next.ID = modelchannel.BuildChannelID(next.BaseURL, next.ModelID, next.APIKey, next.DisplayName, next.OpenAIEndpoint)
		if _, exists := seenChannelIDs[next.ID]; exists {
			return nil, errors.New("模型适配器渠道不能重复，请检查 url、modelID、apiKey、displayName、endpoint 组合")
		}
		seenChannelIDs[next.ID] = struct{}{}
		normalized = append(normalized, next)
	}
	return normalized, nil
}

func validateJSONMap(value string, fieldName string) error {
	text := strings.TrimSpace(value)
	if text == "" {
		return fmt.Errorf("模型适配器 %s 不能为空", fieldName)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return fmt.Errorf("模型适配器 %s 必须是合法 JSON 对象", fieldName)
	}
	if parsed == nil {
		return fmt.Errorf("模型适配器 %s 必须是 JSON 对象", fieldName)
	}
	return nil
}

func validateHeadersJSON(value string) error {
	text := strings.TrimSpace(value)
	if err := validateJSONMap(text, "customHeadersJSON"); err != nil {
		return err
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return errors.New("模型适配器 customHeadersJSON 的值必须是字符串")
	}
	for key := range parsed {
		if strings.TrimSpace(key) == "" {
			return errors.New("模型适配器 customHeadersJSON 的请求头名称不能为空")
		}
	}
	return nil
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "medium":
		return "medium"
	case "low", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeAnthropicThinkingEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xhigh":
		return "xhigh"
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizeMaxCompletionTokens(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

// normalizeModelAdapterType 用于处理与 normalizeModelAdapterType 相关的逻辑。
func normalizeModelAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return ""
	}
}

// ResolvedChannel 表示当前选中的模型渠道。
type ResolvedChannel struct {
	// ID 表示当前声明中的 ID。
	ID string
	// Name 表示当前声明中的 Name。
	Name string
	// GroupName 表示当前声明中的 GroupName。
	GroupName string
	// Code 表示当前声明中的 Code。
	Code string
	// Provider 表示当前声明中的 Provider。
	Provider string
	// BaseURL 表示当前声明中的 BaseURL。
	BaseURL string
	// APIKey 表示当前声明中的 APIKey。
	APIKey string
	// Model 表示当前声明中的 Model。
	Model string
	// TimeoutMS 表示当前声明中的 TimeoutMS。
	TimeoutMS int
	// ContextWindowTokens 表示当前声明中的 ContextWindowTokens。
	ContextWindowTokens int
	// MaxTokens 表示当前声明中的 MaxTokens。
	MaxTokens int
	// ReasoningEffort 表示当前声明中的 ReasoningEffort。
	ReasoningEffort string
	// OpenAIEndpoint 表示 OpenAI 兼容适配器使用的 API 端点。
	OpenAIEndpoint string
	// OpenAIExtraParamsEnabled 表示是否启用 OpenAI 额外请求参数。
	OpenAIExtraParamsEnabled bool
	// OpenAIExtraParamsJSON 表示 OpenAI 额外请求参数 JSON 对象。
	OpenAIExtraParamsJSON string
	// CustomHeadersEnabled 表示是否启用自定义请求头。
	CustomHeadersEnabled bool
	// CustomHeadersJSON 表示自定义请求头 JSON 对象。
	CustomHeadersJSON string
	// AnthropicExtraParamsEnabled 表示是否启用 Anthropic 额外请求参数。
	AnthropicExtraParamsEnabled bool
	// AnthropicExtraParamsJSON 表示 Anthropic 额外请求参数 JSON 对象。
	AnthropicExtraParamsJSON string
	// AnthropicMaxTokens 表示当前声明中的 AnthropicMaxTokens。
	AnthropicMaxTokens int
	// AnthropicThinkingEffort 表示 Anthropic adaptive thinking 的 output_config.effort。
	AnthropicThinkingEffort string
	// ThinkingEnabled 表示当前声明中的 ThinkingEnabled。
	ThinkingEnabled bool
	// ThinkingBudgetTokens 表示当前声明中的 ThinkingBudgetTokens。
	ThinkingBudgetTokens int
}

// ChannelUsageRecordCreatePayload 定义了一次渠道使用记录的最小载荷。
type ChannelUsageRecordCreatePayload struct {
	// RequestID 表示当前声明中的 RequestID。
	RequestID string
	// ConversationID 表示当前声明中的 ConversationID。
	ConversationID string
	// RuntimeModelID 表示当前声明中的 RuntimeModelID。
	RuntimeModelID string
}

// ChannelCallRecordCreatePayload 定义了一次渠道调用记录的最小载荷。
type ChannelCallRecordCreatePayload struct {
	// RequestID 表示当前声明中的 RequestID。
	RequestID string
	// ConversationID 表示当前声明中的 ConversationID。
	ConversationID string
	// ChannelID 表示当前声明中的 ChannelID。
	ChannelID string
	// ChannelName 表示当前声明中的 ChannelName。
	ChannelName string
	// GroupName 表示当前声明中的 GroupName。
	GroupName string
	// Provider 表示当前声明中的 Provider。
	Provider string
	// RuntimeModelID 表示当前声明中的 RuntimeModelID。
	RuntimeModelID string
	// ProviderModelID 表示当前声明中的 ProviderModelID。
	ProviderModelID string
	// StatusCode 表示当前声明中的 StatusCode。
	StatusCode int
	// Success 表示当前声明中的 Success。
	Success bool
	// DurationMS 表示当前声明中的 DurationMS。
	DurationMS int64
	// ErrorCode 表示当前声明中的 ErrorCode。
	ErrorCode string
	// ErrorMessage 表示当前声明中的 ErrorMessage。
	ErrorMessage string
}

// FixedChannelService 定义了当前模块中的 FixedChannelService 类型。
type FixedChannelService struct {
	// channel 表示当前声明中的 channel。
	channel ResolvedChannel
	// configProvider 表示当前声明中的 configProvider。
	configProvider RuntimeConfigProvider
}

// NewFixedChannelService 用于处理与 NewFixedChannelService 相关的逻辑。
func NewFixedChannelService(channel ResolvedChannel, logsRoot string) *FixedChannelService {
	_ = logsRoot
	return &FixedChannelService{
		channel: channel,
	}
}

// NewConfigurableChannelService 用于处理与 NewConfigurableChannelService 相关的逻辑。
func NewConfigurableChannelService(provider RuntimeConfigProvider, logsRoot string) *FixedChannelService {
	_ = logsRoot
	return &FixedChannelService{
		configProvider: provider,
	}
}

// SelectChannelForRequestBody 用于处理与 SelectChannelForRequestBody 相关的逻辑。
func (s *FixedChannelService) SelectChannelForRequestBody(_ context.Context, _ []byte) (*ResolvedChannel, error) {
	return s.SelectChannelForModel(context.Background(), "")
}

// SelectChannelForModel 用于处理与 SelectChannelForModel 相关的逻辑。
func (s *FixedChannelService) SelectChannelForModel(ctx context.Context, modelID string) (*ResolvedChannel, error) {
	if s == nil {
		return nil, ErrChannelNotAvailable
	}
	if s.configProvider != nil {
		cfg, err := s.configProvider(ctx)
		if err != nil {
			return nil, err
		}
		adapters, err := NormalizeModelAdapterConfigs(cfg.ModelAdapters)
		if err != nil {
			return nil, err
		}
		matchIndex, ok := modelchannel.ResolveAdapterIndex(
			adapters,
			modelID,
			func(adapter ModelAdapterConfig) string { return adapter.ID },
			func(adapter ModelAdapterConfig) string { return adapter.ModelID },
			func(adapter ModelAdapterConfig) string {
				return modelchannel.BuildLegacyChannelID(adapter.BaseURL, adapter.ModelID, adapter.APIKey, adapter.DisplayName)
			},
		)
		if !ok {
			return nil, ErrChannelNotAvailable
		}
		adapter := adapters[matchIndex]
		resolved := ResolvedChannel{
			ID:                          strings.TrimSpace(adapter.ID),
			Name:                        strings.TrimSpace(adapter.DisplayName),
			GroupName:                   "local",
			Code:                        strings.TrimSpace(adapter.ID),
			Provider:                    strings.TrimSpace(adapter.Type),
			BaseURL:                     strings.TrimSpace(adapter.BaseURL),
			APIKey:                      strings.TrimSpace(adapter.APIKey),
			Model:                       strings.TrimSpace(adapter.ModelID),
			TimeoutMS:                   configurableChannelTimeoutMS,
			ContextWindowTokens:         configurableChannelContextWindowTokens,
			MaxTokens:                   configurableChannelMaxTokens,
			ReasoningEffort:             strings.TrimSpace(adapter.ReasoningEffort),
			OpenAIEndpoint:              strings.TrimSpace(adapter.OpenAIEndpoint),
			OpenAIExtraParamsEnabled:    adapter.OpenAIExtraParamsEnabled,
			OpenAIExtraParamsJSON:       strings.TrimSpace(adapter.OpenAIExtraParamsJSON),
			CustomHeadersEnabled:        adapter.CustomHeadersEnabled,
			CustomHeadersJSON:           strings.TrimSpace(adapter.CustomHeadersJSON),
			AnthropicExtraParamsEnabled: adapter.AnthropicExtraParamsEnabled,
			AnthropicExtraParamsJSON:    strings.TrimSpace(adapter.AnthropicExtraParamsJSON),
			AnthropicMaxTokens:          configurableChannelMaxTokens,
			AnthropicThinkingEffort:     configurableChannelAnthropicThinkingEffort,
			ThinkingEnabled:             true,
			ThinkingBudgetTokens:        configurableChannelThinkingBudgetTokens,
		}
		if adapter.ContextWindowTokens > 0 {
			resolved.ContextWindowTokens = adapter.ContextWindowTokens
		}
		if adapter.MaxCompletionTokens > 0 {
			resolved.MaxTokens = adapter.MaxCompletionTokens
		}
		if adapter.ThinkingBudgetTokens > 0 {
			resolved.ThinkingBudgetTokens = adapter.ThinkingBudgetTokens
		}
		if adapter.AnthropicMaxTokens > 0 {
			resolved.AnthropicMaxTokens = adapter.AnthropicMaxTokens
		}
		if strings.TrimSpace(adapter.AnthropicThinkingEffort) != "" {
			resolved.AnthropicThinkingEffort = strings.TrimSpace(adapter.AnthropicThinkingEffort)
		}
		return &resolved, nil
	}
	if strings.TrimSpace(s.channel.BaseURL) == "" || strings.TrimSpace(s.channel.APIKey) == "" {
		return nil, ErrChannelNotAvailable
	}
	resolved := s.channel
	return &resolved, nil
}

// RecordRunRequestUsage 用于处理与 RecordRunRequestUsage 相关的逻辑。
func (s *FixedChannelService) RecordRunRequestUsage(_ context.Context, payload ChannelUsageRecordCreatePayload) error {
	_ = s
	_ = payload
	return nil
}

// RecordChannelCall 用于处理与 RecordChannelCall 相关的逻辑。
func (s *FixedChannelService) RecordChannelCall(_ context.Context, payload ChannelCallRecordCreatePayload) error {
	_ = s
	_ = payload
	return nil
}

// LocalSystemSettingService 定义了当前模块中的 LocalSystemSettingService 类型。
type LocalSystemSettingService struct {
	// provider 表示当前声明中的 provider。
	provider RuntimeConfigProvider
}

// NewLocalSystemSettingService 用于处理与 NewLocalSystemSettingService 相关的逻辑。
func NewLocalSystemSettingService(provider RuntimeConfigProvider) *LocalSystemSettingService {
	return &LocalSystemSettingService{provider: provider}
}

// ResolveFrontendBaseURL 用于处理与 ResolveFrontendBaseURL 相关的逻辑。
func (s *LocalSystemSettingService) ResolveFrontendBaseURL(context.Context) (string, error) {
	return "http://127.0.0.1", nil
}

// IsObservabilityLogEnabled 用于处理与 IsObservabilityLogEnabled 相关的逻辑。
func (s *LocalSystemSettingService) IsObservabilityLogEnabled(ctx context.Context) bool {
	cfg, err := s.load(ctx)
	if err != nil {
		return true
	}
	return cfg.ObservabilityLogEnabled
}

// IsAgentRuntimeModelEnabled 用于处理与 IsAgentRuntimeModelEnabled 相关的逻辑。
func (s *LocalSystemSettingService) IsAgentRuntimeModelEnabled(context.Context) bool {
	return true
}

// ResolveCursorServerBridge 用于处理与 ResolveCursorServerBridge 相关的逻辑。
func (s *LocalSystemSettingService) ResolveCursorServerBridge(context.Context) (string, bool) {
	return "", false
}

// LoadRuntimeConfigSnapshot 用于处理与 LoadRuntimeConfigSnapshot 相关的逻辑。
func (s *LocalSystemSettingService) LoadRuntimeConfigSnapshot(ctx context.Context) (RuntimeConfigSnapshot, error) {
	return s.load(ctx)
}

// ResolveModelAdapters 用于处理与 ResolveModelAdapters 相关的逻辑。
func (s *LocalSystemSettingService) ResolveModelAdapters(ctx context.Context) ([]ModelAdapterConfig, error) {
	cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	return NormalizeModelAdapterConfigs(cfg.ModelAdapters)
}

// load 用于处理与 load 相关的逻辑。
func (s *LocalSystemSettingService) load(ctx context.Context) (RuntimeConfigSnapshot, error) {
	if s == nil || s.provider == nil {
		return RuntimeConfigSnapshot{}, nil
	}
	return s.provider(ctx)
}
