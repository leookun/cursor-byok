package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"cursor/internal/modelchannel"
)

const (
	DefaultBackendListenAddr                = "127.0.0.1:18090"
	DefaultProxyListenAddr                  = "127.0.0.1:18080"
	DefaultFrontendBaseURL                  = "http://127.0.0.1"
	DefaultRoutingMode                      = "local"
	DefaultProviderStreamIdleTimeoutSeconds = 240
	MinProviderStreamIdleTimeoutSeconds     = 30
)

// ModelAdapterConfig is a type alias for the canonical definition in modelchannel.
// The shared struct lives in internal/modelchannel/adapter.go to avoid duplication
// between config and runtime packages (which cannot import each other due to cycles).
type ModelAdapterConfig = modelchannel.ModelAdapterConfig

type RoutingConfig struct {
	Mode string `json:"mode" yaml:"mode"`
}

type HomeMetricsConfig struct {
	IncludeCacheWriteInHitRate bool `json:"includeCacheWriteInHitRate" yaml:"includeCacheWriteInHitRate"`
}

// OptimizationConfig 控制 Optimization Runtime（Token Budget + Cost Optimizer）。
// 落盘路径：~/.cursor-byok/config.yaml → optimization
type OptimizationConfig struct {
	// Enabled 为 false 时主链路仍注入 Runtime，但 AllocateBudget 不覆盖用户 max tokens（见 host 行为约定）。
	// 当前实现：始终创建 Runtime；Enabled 供前端与后续策略使用。
	Enabled bool `json:"enabled" yaml:"enabled"`
	// QualityTier: fast | balanced | quality | ultra
	QualityTier string `json:"qualityTier" yaml:"qualityTier"`
	// MonthlyBudgetUSD 月度成本预算（美元），用于 SelectOptimalProvider 降级策略。
	MonthlyBudgetUSD float64 `json:"monthlyBudgetUSD" yaml:"monthlyBudgetUSD"`
}

type VirtualModelNodeBindingConfig struct {
	AdapterID string `json:"adapterID" yaml:"adapterID"`
	Enabled   bool   `json:"enabled" yaml:"enabled"`
}

type VirtualModelConfig struct {
	Enabled    bool                                      `json:"enabled" yaml:"enabled"`
	WorkflowID string                                    `json:"workflowID" yaml:"workflowID"`
	Planner    *VirtualModelNodeBindingConfig            `json:"planner,omitempty" yaml:"planner,omitempty"`
	Nodes      map[string]*VirtualModelNodeBindingConfig `json:"nodes,omitempty" yaml:"nodes,omitempty"`
}

type VirtualModelsConfig struct {
	MOA *VirtualModelConfig `json:"moa,omitempty" yaml:"moa,omitempty"`
	AOS *AOSConfig          `json:"aos,omitempty" yaml:"aos,omitempty"`
}

// AOSConfig defines the AOS (AI Organization System) virtual model configuration.
type AOSConfig struct {
	Enabled       bool              `json:"enabled" yaml:"enabled"`
	Leader        AOSLeaderConfig   `json:"leader" yaml:"leader"`
	Members       []AOSMemberConfig `json:"members,omitempty" yaml:"members,omitempty"`
	ExecutionMode string            `json:"executionMode,omitempty" yaml:"executionMode,omitempty"`
}

const (
	AOSExecutionModeInternal   = "internal"
	AOSExecutionModeCursorTask = "cursor_task"
)

// AOSLeaderConfig defines the AOS team leader.
type AOSLeaderConfig struct {
	AdapterID string `json:"adapterID" yaml:"adapterID"`
}

// AOSMemberConfig defines an AOS team member (Prompt + ModelAdapter).
//
// Tags are intentionally NOT user-configurable. They are inferred at runtime
// by AOSModel.RecognizeMembers (the Leader reads each member's name + prompt
// and assigns routing tags before the first Sprint). This keeps the Leader's
// per-turn dispatch prompt compact: it reads the short tags via MembersInfo(),
// not the long SystemPrompt of every member.
type AOSMemberConfig struct {
	ID           string `json:"id" yaml:"id"`
	Name         string `json:"name" yaml:"name"`
	AdapterID    string `json:"adapterID" yaml:"adapterID"`
	SystemPrompt string `json:"systemPrompt,omitempty" yaml:"systemPrompt,omitempty"`
}

const (
	DefaultOptimizationQualityTier   = "balanced"
	DefaultOptimizationMonthlyBudget = 50.0
	DefaultOptimizationEnabled       = true
	DefaultEvolverBackgroundEnabled  = true
	DefaultEvolverBackgroundInterval = 30 // minutes
	DefaultPluginEnabled             = true
)

// EvolverBackgroundConfig controls the Host background self-evolution loop
// (ADR-028). The loop runs Diagnose→Sediment→Persist at the configured
// interval. Test/Benchmark/Writeback/Execute are never run in background;
// they remain CLI-only flags.
//
// ponytail: interval=0 or Enabled=false disables the loop. No event bus,
// no dashboard — just a ticker goroutine.
type EvolverBackgroundConfig struct {
	// Enabled controls whether the background evolution loop starts.
	// Default: true.
	Enabled bool `json:"enabled" yaml:"enabled"`
	// IntervalMinutes is the period between evolution cycles.
	// 0 or negative means disabled (same as Enabled=false).
	// Default: 30 minutes.
	IntervalMinutes int `json:"intervalMinutes" yaml:"intervalMinutes"`
}

// PluginConfig controls the Phase 8 Plugin Marketplace runtime
// (ADR-021 + ADR-047). Enabled wires the plugin.Registry into the host and
// exposes the Marketplace REST API; DataDir overrides the manifest storage
// path (defaults to <appdata>/data/plugin).
type PluginConfig struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	DataDir string `json:"dataDir,omitempty" yaml:"dataDir,omitempty"`
}

// OutboundProxyConfig configures the manual outbound proxy override used by
// the netproxy resolver (internal/netproxy). Both fields are optional. When
// both are empty, the resolver falls back to environment/system proxy
// detection. See NormalizeOutboundProxyConfig for the canonicalization rules.
type OutboundProxyConfig struct {
	HTTPProxy  string `json:"httpProxy" yaml:"httpProxy"`
	HTTPSProxy string `json:"httpsProxy" yaml:"httpsProxy"`
}

// ModelInProviderConfig is a model entry inside a ProviderConfig.
// It excludes baseURL/apiKey/type which are inherited from the provider.
type ModelInProviderConfig struct {
	DisplayName                 string `json:"displayName" yaml:"displayName"`
	TooltipData                 string `json:"tooltipData" yaml:"tooltipData"`
	ModelID                     string `json:"modelID" yaml:"modelID"`
	ReasoningEffort             string `json:"reasoningEffort" yaml:"reasoningEffort"`
	OpenAIEndpoint              string `json:"openAIEndpoint" yaml:"openAIEndpoint"`
	OpenAIExtraParamsEnabled    bool   `json:"openAIExtraParamsEnabled" yaml:"openAIExtraParamsEnabled"`
	OpenAIExtraParamsJSON       string `json:"openAIExtraParamsJSON" yaml:"openAIExtraParamsJSON"`
	CustomHeadersEnabled        bool   `json:"customHeadersEnabled" yaml:"customHeadersEnabled"`
	CustomHeadersJSON           string `json:"customHeadersJSON" yaml:"customHeadersJSON"`
	AnthropicExtraParamsEnabled bool   `json:"anthropicExtraParamsEnabled" yaml:"anthropicExtraParamsEnabled"`
	AnthropicExtraParamsJSON    string `json:"anthropicExtraParamsJSON" yaml:"anthropicExtraParamsJSON"`
	ContextWindowTokens         int    `json:"contextWindowTokens" yaml:"contextWindowTokens"`
	MaxCompletionTokens         int    `json:"maxCompletionTokens" yaml:"maxCompletionTokens"`
	AnthropicMaxTokens          int    `json:"anthropicMaxTokens" yaml:"anthropicMaxTokens"`
	AnthropicThinkingEffort     string `json:"anthropicThinkingEffort,omitempty" yaml:"anthropicThinkingEffort,omitempty"`
	ThinkingBudgetTokens        int    `json:"thinkingBudgetTokens" yaml:"thinkingBudgetTokens"`
}

// ProviderConfig groups models sharing the same baseURL/type.
// Models inherit baseURL/type from the provider; API calls use the first key from APIKeys.
type ProviderConfig struct {
	ID      string                  `json:"id" yaml:"id"`
	Name    string                  `json:"name" yaml:"name"`
	Type    string                  `json:"type" yaml:"type"`
	BaseURL string                  `json:"baseURL" yaml:"baseURL"`
	APIKey  string                  `json:"apiKey" yaml:"apiKey"`           // Deprecated: kept for backward compat; use APIKeys
	APIKeys []string                `json:"apiKeys,omitempty" yaml:"apiKeys,omitempty"` // Multiple API keys; first is primary
	Models  []ModelInProviderConfig `json:"models" yaml:"models"`
}

type Config struct {
	Log                       bool                    `json:"log" yaml:"log"`
	ProviderStreamIdleTimeout int                     `json:"providerStreamIdleTimeout" yaml:"providerStreamIdleTimeout"`
	BackendListenAddr         string                  `json:"backendListenAddr" yaml:"backendListenAddr"`
	ProxyListenAddr           string                  `json:"proxyListenAddr" yaml:"proxyListenAddr"`
	ModelAdapters             []ModelAdapterConfig    `json:"modelAdapters" yaml:"modelAdapters"`
	Providers                 []ProviderConfig        `json:"providers,omitempty" yaml:"providers,omitempty"`
	Routing                   RoutingConfig           `json:"routing" yaml:"routing"`
	HomeMetrics               HomeMetricsConfig       `json:"homeMetrics" yaml:"homeMetrics"`
	Optimization              OptimizationConfig      `json:"optimization" yaml:"optimization"`
	LastAgentModelHash        string                  `json:"lastAgentModelHash" yaml:"lastAgentModelHash"`
	VirtualModels             VirtualModelsConfig     `json:"virtualModels" yaml:"virtualModels"`
	EvolverBackground         EvolverBackgroundConfig `json:"evolverBackground" yaml:"evolverBackground"`
	OutboundProxy            OutboundProxyConfig     `json:"outboundProxy" yaml:"outboundProxy"`
	Plugin                    PluginConfig            `json:"plugin" yaml:"plugin"`
}

func DefaultConfig() Config {
	return Config{
		Log:                       false,
		ProviderStreamIdleTimeout: DefaultProviderStreamIdleTimeoutSeconds,
		BackendListenAddr:         DefaultBackendListenAddr,
		ProxyListenAddr:           DefaultProxyListenAddr,
		ModelAdapters:             []ModelAdapterConfig{},
		Routing: RoutingConfig{
			Mode: DefaultRoutingMode,
		},
		Optimization: OptimizationConfig{
			Enabled:          DefaultOptimizationEnabled,
			QualityTier:      DefaultOptimizationQualityTier,
			MonthlyBudgetUSD: DefaultOptimizationMonthlyBudget,
		},
		EvolverBackground: DefaultEvolverBackgroundConfig(),
		OutboundProxy:     DefaultOutboundProxyConfig(),
		Plugin:            DefaultPluginConfig(),
	}
}

// DefaultOutboundProxyConfig returns the default outbound proxy config
// (empty — no manual override; falls back to env/system proxy detection).
func DefaultOutboundProxyConfig() OutboundProxyConfig {
	return OutboundProxyConfig{}
}

// DefaultPluginConfig returns the default Plugin Marketplace config.
func DefaultPluginConfig() PluginConfig {
	return PluginConfig{
		Enabled: DefaultPluginEnabled,
	}
}

// NormalizePluginConfig normalizes the plugin config. A zero-valued struct
// returns the default (enabled). DataDir is left empty to use the appdata
// default when not explicitly set.
func NormalizePluginConfig(input PluginConfig) PluginConfig {
	if input == (PluginConfig{}) {
		return DefaultPluginConfig()
	}
	return PluginConfig{
		Enabled: input.Enabled,
		DataDir: strings.TrimSpace(input.DataDir),
	}
}

// DefaultEvolverBackgroundConfig returns the default background evolution config.
func DefaultEvolverBackgroundConfig() EvolverBackgroundConfig {
	return EvolverBackgroundConfig{
		Enabled:         DefaultEvolverBackgroundEnabled,
		IntervalMinutes: DefaultEvolverBackgroundInterval,
	}
}

func NormalizeConfig(input Config) (Config, error) {
	output := DefaultConfig()
	output.Log = input.Log
	output.ProviderStreamIdleTimeout = normalizeProviderStreamIdleTimeout(input.ProviderStreamIdleTimeout)
	backendListenAddr, err := normalizeListenAddr(input.BackendListenAddr, DefaultBackendListenAddr, "backendListenAddr")
	if err != nil {
		return Config{}, err
	}
	proxyListenAddr, err := normalizeListenAddr(input.ProxyListenAddr, DefaultProxyListenAddr, "proxyListenAddr")
	if err != nil {
		return Config{}, err
	}
	output.BackendListenAddr = backendListenAddr
	output.ProxyListenAddr = proxyListenAddr
	output.HomeMetrics.IncludeCacheWriteInHitRate = input.HomeMetrics.IncludeCacheWriteInHitRate
	output.Optimization = NormalizeOptimizationConfig(input.Optimization)
	output.LastAgentModelHash = strings.TrimSpace(input.LastAgentModelHash)
	output.VirtualModels = normalizeVirtualModelsConfig(input.VirtualModels)
	output.Routing.Mode = normalizeRoutingMode(input.Routing.Mode)
	if output.Routing.Mode == "" {
		output.Routing.Mode = DefaultRoutingMode
	}

	// When modelAdapters are present, regenerate providers from adapters.
	// This makes modelAdapters the canonical source of truth — user edits
	// from the frontend take precedence over stale providers from a prior save.
	if len(input.ModelAdapters) > 0 {
		output.Providers = GroupAdaptersToProviders(input.ModelAdapters)
	} else if len(input.Providers) > 0 {
		output.Providers = NormalizeProviders(input.Providers)
	}

	// Always derive modelAdapters from providers (for resolver compatibility)
	output.ModelAdapters, err = FlattenProvidersToAdapters(output.Providers)
	if err != nil {
		return Config{}, err
	}

	output.EvolverBackground = NormalizeEvolverBackgroundConfig(input.EvolverBackground)
	output.OutboundProxy = NormalizeOutboundProxyConfig(input.OutboundProxy)
	output.Plugin = NormalizePluginConfig(input.Plugin)
	return output, nil
}

// NormalizeEvolverBackgroundConfig normalizes the evolver background config.
// Zero-value fields are replaced with defaults. Interval <= 0 disables the loop.
func NormalizeEvolverBackgroundConfig(input EvolverBackgroundConfig) EvolverBackgroundConfig {
	// If the whole struct is zero-valued, return defaults.
	if input == (EvolverBackgroundConfig{}) {
		return DefaultEvolverBackgroundConfig()
	}
	// If interval is explicitly 0 or negative, treat as disabled regardless of Enabled.
	if input.IntervalMinutes <= 0 {
		return EvolverBackgroundConfig{Enabled: false, IntervalMinutes: 0}
	}
	return input
}

// NormalizeOutboundProxyConfig canonicalizes the outbound proxy fields:
//   - whitespace is trimmed from both fields;
//   - a non-empty value missing a scheme ("://") is prefixed with "http://".
//
// Both-empty input is valid and means "no manual override — use env/system
// proxy detection". Non-empty values without a scheme default to http:// so
// callers that expect a *url.URL parse cleanly.
func NormalizeOutboundProxyConfig(input OutboundProxyConfig) OutboundProxyConfig {
	return OutboundProxyConfig{
		HTTPProxy:  normalizeProxyAddress(input.HTTPProxy),
		HTTPSProxy: normalizeProxyAddress(input.HTTPSProxy),
	}
}

// normalizeProxyAddress trims the address and prefixes "http://" when a
// scheme separator ("://") is absent. Empty input stays empty.
func normalizeProxyAddress(value string) string {
	addr := strings.TrimSpace(value)
	if addr == "" {
		return ""
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return addr
}

// NormalizeOptimizationConfig 归一化 Optimization 配置。
//
// 约定：
//   - 配置块完全缺省（零值）→ enabled=true, balanced, $50
//   - 显式 enabled:false 保留（需同时写 qualityTier 或 monthlyBudgetUSD 之一，
//     或写 enabled:false 且非完全零值——即至少有一个非零字段；
//     仅 {enabled:false} 在 YAML 中 Enabled=false 且其余为零，会被当成“缺省”并默认开启。
//     若需关闭，请写 optimization.enabled: false 并保留 qualityTier 或 budget。）
//
// 更稳妥的关闭方式：optimization: { enabled: false, qualityTier: balanced }
func NormalizeOptimizationConfig(input OptimizationConfig) OptimizationConfig {
	isZero := !input.Enabled && strings.TrimSpace(input.QualityTier) == "" && input.MonthlyBudgetUSD == 0
	if isZero {
		return OptimizationConfig{
			Enabled:          DefaultOptimizationEnabled,
			QualityTier:      DefaultOptimizationQualityTier,
			MonthlyBudgetUSD: DefaultOptimizationMonthlyBudget,
		}
	}
	tier := normalizeQualityTier(input.QualityTier)
	if tier == "" {
		tier = DefaultOptimizationQualityTier
	}
	budget := input.MonthlyBudgetUSD
	if budget <= 0 {
		budget = DefaultOptimizationMonthlyBudget
	}
	return OptimizationConfig{
		Enabled:          input.Enabled,
		QualityTier:      tier,
		MonthlyBudgetUSD: budget,
	}
}

func normalizeQualityTier(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "fast", "balanced", "quality", "ultra":
		return strings.ToLower(strings.TrimSpace(value))
	case "":
		return ""
	default:
		return ""
	}
}

func normalizeVirtualModelsConfig(input VirtualModelsConfig) VirtualModelsConfig {
	result := VirtualModelsConfig{}
	if input.MOA != nil {
		moa := *input.MOA
		moa.WorkflowID = strings.TrimSpace(moa.WorkflowID)
		if moa.Planner != nil {
			p := *moa.Planner
			p.AdapterID = strings.TrimSpace(p.AdapterID)
			moa.Planner = &p
		}
		if moa.Nodes != nil {
			nodes := make(map[string]*VirtualModelNodeBindingConfig, len(moa.Nodes))
			for k, v := range moa.Nodes {
				if v == nil {
					continue
				}
				n := *v
				n.AdapterID = strings.TrimSpace(n.AdapterID)
				nodes[strings.TrimSpace(k)] = &n
			}
			moa.Nodes = nodes
		}
		result.MOA = &moa
	}
	if input.AOS != nil {
		aos := *input.AOS
		aos.Leader.AdapterID = strings.TrimSpace(aos.Leader.AdapterID)
		aos.ExecutionMode = NormalizeAOSExecutionMode(aos.ExecutionMode)
		for i := range aos.Members {
			aos.Members[i].ID = strings.TrimSpace(aos.Members[i].ID)
			aos.Members[i].AdapterID = strings.TrimSpace(aos.Members[i].AdapterID)
		}
		result.AOS = &aos
	}
	return result
}

// NormalizeAOSExecutionMode keeps the legacy direct-adapter mode explicit;
// all other values use Cursor-native task execution.
func NormalizeAOSExecutionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AOSExecutionModeInternal:
		return AOSExecutionModeInternal
	default:
		return AOSExecutionModeCursorTask
	}
}

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
		nextType := normalizeModelAdapterType(item.Type)
		next := ModelAdapterConfig{
			Ref:                  strings.TrimSpace(item.Ref),
			DisplayName:          strings.TrimSpace(item.DisplayName),
			Type:                 nextType,
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
		isVirtual := next.Type == "virtual"
		switch {
		case next.DisplayName == "":
			return nil, errors.New("模型适配器 displayName 不能为空")
		case next.Type == "":
			return nil, errors.New("模型适配器 type 仅支持 openai、anthropic 或 virtual")
		case !isVirtual && next.APIKey == "":
			return nil, errors.New("模型适配器 apiKey 不能为空")
		case next.TooltipData == "":
			return nil, errors.New("模型适配器 tooltipData 不能为空")
		case next.ModelID == "":
			return nil, errors.New("模型适配器 modelID 不能为空")
		case next.Type == "openai" && next.ReasoningEffort == "":
			return nil, errors.New("模型适配器 reasoningEffort 仅支持 low、medium、high、xhigh")
		case next.Type == "openai" && next.OpenAIEndpoint == "":
			return nil, errors.New("模型适配器 openAIEndpoint 仅支持 /v1/responses、/v1/chat/completions 或 /custom（自定义路径）")
		case next.Type == "openai" && next.OpenAIExtraParamsEnabled:
			if err := validateJSONMap(next.OpenAIExtraParamsJSON, "openAIExtraParamsJSON"); err != nil {
				return nil, err
			}
		case next.CustomHeadersEnabled && !isVirtual:
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
	// 未识别值（如 Cursor 新增的 "minimal" / "none" 等）回退到 "medium"，
	// 而不是返回空字符串触发 validate 的 reasoningEffort 校验报错。
	// 与前端 appState.js normalizeModelAdapter 行为保持一致。
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "medium":
		return "medium"
	case "low", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "medium"
	}
}

func normalizeAnthropicThinkingEffort(value string) string {
	// 与 normalizeReasoningEffort 对称：未识别值回退到默认 "xhigh"，
	// 避免上游同步过来的脏值触发 validate 的 anthropicThinkingEffort 校验报错。
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "xhigh":
		return "xhigh"
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "xhigh"
	}
}

func normalizeListenAddr(value string, defaultValue string, fieldName string) (string, error) {
	addr := strings.TrimSpace(value)
	if addr == "" {
		addr = defaultValue
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s 必须是 host:port 格式", fieldName)
	}
	if strings.TrimSpace(host) == "" {
		return "", fmt.Errorf("%s host 不能为空", fieldName)
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", fmt.Errorf("%s port 必须在 1-65535 之间", fieldName)
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func normalizeProviderStreamIdleTimeout(value int) int {
	if value <= 0 {
		return DefaultProviderStreamIdleTimeoutSeconds
	}
	if value < MinProviderStreamIdleTimeoutSeconds {
		return MinProviderStreamIdleTimeoutSeconds
	}
	return value
}

func normalizeMaxCompletionTokens(value int) int {
	if value <= 0 {
		return 0
	}
	return value
}

func normalizeModelAdapterType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "openai":
		return "openai"
	case "anthropic":
		return "anthropic"
	case "virtual":
		return "virtual"
	default:
		return ""
	}
}

func normalizeRoutingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "local":
		return "local"
	case "upstream":
		return "upstream"
	default:
		return ""
	}
}
