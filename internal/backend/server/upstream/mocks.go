package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"html"
	"strings"
	"time"

	legacyruntime "cursor/internal/runtime"
)

const (
	availableModelsDisableUnusedHours = 2400000
	availableModelsUpgradeHours       = 2

	modelRuntimeThinkingEffortParameterID = "thinking_effort"

	localUltraMembershipType       = "ultra"
	localUltraPaymentID            = "local_ultra"
	localUltraSubscriptionStatus   = "active"
	localUltraPlanIncludedCents    = 20000
	localUltraDashboardUserID      = 1
	localUltraBillingCycleDuration = 30 * 24 * time.Hour

	bootstrapStatsigGlassModeAvailableGate           = "glass_mode_available"
	bootstrapStatsigGlassOpenAgentInWindowGate       = "glass.enable_open_agent_in_window"
	bootstrapStatsigOpenAgentsTitlebarGate           = "glass_open_agents_titlebar_button"
	bootstrapStatsigOpenAgentWindowTopGate           = "open_agent_window_top"
	bootstrapStatsigOpenAgentWindowBottomGate        = "open_agent_window_bottom_convo"
	bootstrapStatsigNALAgentRetriesGate              = "nal_agent_retries"
	bootstrapStatsigNALFreshRetryIDsGate             = "nal_fresh_retry_ids"
	bootstrapStatsigUseModelParametersGate           = "use_model_parameters"
	bootstrapStatsigUseReactModelPickerGate          = "use_react_model_picker"
	bootstrapStatsigIDECmdEnterSubmitGate            = "ide_cmd_enter_submit"
	bootstrapStatsigContextVisualizerGate            = "context_visualizer"
	bootstrapStatsigWysiwygMarkdownGate              = "wysiwyg_markdown"
	bootstrapStatsigWysiwygMarkdownDefaultGate       = "wysiwyg_markdown_default"
	bootstrapStatsigSubagentSupportInterrupt         = "subagent_support_interrupt"
	bootstrapStatsigExplicitSubagentModels           = "explicit_subagent_models"
	bootstrapStatsigMcpDirectClientToolFetch         = "mcp_direct_client_tool_fetch"
	bootstrapStatsigGlassCustomThemeSupport          = "glass_custom_theme_support"
	bootstrapStatsigGlassAutomationsUI               = "glass_automations_ui"
	bootstrapStatsigTerminalUI2                      = "terminal_ui_2"
	bootstrapStatsigDisableTerminalOutputUIStreaming = "disable_terminal_output_ui_streaming"
	bootstrapStatsigBrowserCanvas                    = "browser_canvas"
	bootstrapStatsigEnableMultitaskMode              = "enable_multitask_mode"
	bootstrapStatsigDecomposeAlwaysLocalExtHostGate  = "decompose_always_local_ext_host"
	bootstrapStatsigCursorExtensionsIsolationV2Gate  = "cursor_extensions_isolation_v2"
	bootstrapStatsigCursorAgentWorkerExtension       = "enable_cursor_agent_worker_extension"
	bootstrapStatsigExperimentName                   = "free_user_model_picker"
	bootstrapStatsigVariantParam                     = "variant"
	bootstrapStatsigVariantControl                   = "control"
	bootstrapStatsigVariantLockedPicker              = "locked_picker"
	bootstrapStatsigVariantGrayedModels              = "grayed_models"
	bootstrapStatsigProductTipsConfigName            = "product_tips_config"
	bootstrapStatsigIdleExtensionHostKiller          = "idle_extension_host_killer_config"
	bootstrapStatsigIdleMinutesToKill                = "idleMinutesToKillExtensionHost"
	bootstrapStatsigFreeMemoryPercentageToKill       = "freeMemoryPercentageToKillExtensionHost"
	bootstrapStatsigHTTP2PingConfig                  = "http2_ping_config"
	bootstrapStatsigHTTP1KeepaliveConfig             = "http1_keepalive_config"
	bootstrapStatsigHTTP2AgentPoolConfig             = "http2_agent_connection_pool_config"
	bootstrapStatsigCanvasPromptTextConfig           = "canvas_prompt_text_config"
	bootstrapStatsigEditorBugbotConfig               = "editor_bugbot_config"
	bootstrapStatsigExtensionMonitorControl          = "extension_monitor_control"
	bootstrapStatsigExtensionSignatureBypass         = "extension_signature_verification_bypass_list"
	bootstrapStatsigGCTraceControl                   = "gc_trace_control"
	bootstrapStatsigInlineDiffPerformance            = "inline_diff_performance_config"
	bootstrapStatsigLeakedDisposablesTracker         = "leaked_disposables_tracker"
	bootstrapStatsigMcpIPCTimeouts                   = "mcp_ipc_timeouts"
	bootstrapStatsigMcpWakeProbeConfig               = "mcp_wake_probe_config"
	bootstrapStatsigNALStallDetectorTimeout          = "nal_stall_detector_timeout_config"
	bootstrapStatsigSimulatedThinkingErrorTimeout    = "simulated_thinking_error_timeout"
	bootstrapStatsigPlaywrightLogConfigs             = "playwright_log_configs"
	bootstrapStatsigRetryInterceptorParams           = "retry_interceptor_params_config"
	bootstrapStatsigSandboxNetworkAllowlist          = "sandbox_default_network_allowlist"
	bootstrapStatsigUpdatePromptConfig               = "update_prompt_config"
	bootstrapStatsigLocalDefaultRule                 = "local_default"
)

type statsigSecondaryExposure struct {
	Gate           string `json:"gate,omitempty"`
	GateValue      string `json:"gateValue,omitempty"`
	GateValueSnake string `json:"gate_value,omitempty"`
	RuleID         string `json:"ruleID,omitempty"`
	RuleIDSnake    string `json:"rule_id,omitempty"`
}

type statsigDynamicConfigTemplate struct {
	Name                               string                     `json:"name"`
	Value                              map[string]any             `json:"value"`
	RuleID                             string                     `json:"rule_id"`
	RuleIDCamel                        string                     `json:"ruleID"`
	GroupName                          string                     `json:"group_name"`
	GroupNameCamel                     string                     `json:"groupName"`
	SecondaryExposures                 []statsigSecondaryExposure `json:"secondary_exposures"`
	SecondaryExposuresCamel            []statsigSecondaryExposure `json:"secondaryExposures"`
	UndelegatedSecondaryExposures      []statsigSecondaryExposure `json:"undelegated_secondary_exposures"`
	UndelegatedSecondaryExposuresCamel []statsigSecondaryExposure `json:"undelegatedSecondaryExposures"`
	IsDeviceBased                      bool                       `json:"is_device_based"`
	IsDeviceBasedCamel                 bool                       `json:"isDeviceBased"`
	IsExperimentActive                 bool                       `json:"is_experiment_active"`
	IsExperimentActiveCamel            bool                       `json:"isExperimentActive"`
	IsUserInExperiment                 bool                       `json:"is_user_in_experiment"`
	IsUserInExperimentCamel            bool                       `json:"isUserInExperiment"`
}

type statsigBootstrapTemplate struct {
	FeatureGates   map[string]map[string]any               `json:"feature_gates"`
	DynamicConfigs map[string]statsigDynamicConfigTemplate `json:"dynamic_configs"`
	LayerConfigs   map[string]map[string]any               `json:"layer_configs"`
	User           map[string]any                          `json:"user"`
	HasUpdates     bool                                    `json:"has_updates"`
	HashUsed       string                                  `json:"hash_used"`
	SDKParams      map[string]any                          `json:"sdkParams"`
	Time           int64                                   `json:"time"`
}

var bootstrapStatsigTemplate = statsigBootstrapTemplate{
	FeatureGates: map[string]map[string]any{
		bootstrapStatsigGlassModeAvailableGate:           buildEnabledStatsigGate(bootstrapStatsigGlassModeAvailableGate),
		bootstrapStatsigGlassOpenAgentInWindowGate:       buildEnabledStatsigGate(bootstrapStatsigGlassOpenAgentInWindowGate),
		bootstrapStatsigOpenAgentsTitlebarGate:           buildEnabledStatsigGate(bootstrapStatsigOpenAgentsTitlebarGate),
		bootstrapStatsigOpenAgentWindowTopGate:           buildEnabledStatsigGate(bootstrapStatsigOpenAgentWindowTopGate),
		bootstrapStatsigOpenAgentWindowBottomGate:        buildEnabledStatsigGate(bootstrapStatsigOpenAgentWindowBottomGate),
		bootstrapStatsigNALAgentRetriesGate:              buildEnabledStatsigGate(bootstrapStatsigNALAgentRetriesGate),
		bootstrapStatsigNALFreshRetryIDsGate:             buildEnabledStatsigGate(bootstrapStatsigNALFreshRetryIDsGate),
		bootstrapStatsigUseModelParametersGate:           buildEnabledStatsigGate(bootstrapStatsigUseModelParametersGate),
		bootstrapStatsigUseReactModelPickerGate:          buildEnabledStatsigGate(bootstrapStatsigUseReactModelPickerGate),
		bootstrapStatsigIDECmdEnterSubmitGate:            buildEnabledStatsigGate(bootstrapStatsigIDECmdEnterSubmitGate),
		bootstrapStatsigContextVisualizerGate:            buildEnabledStatsigGate(bootstrapStatsigContextVisualizerGate),
		bootstrapStatsigWysiwygMarkdownGate:              buildEnabledStatsigGate(bootstrapStatsigWysiwygMarkdownGate),
		bootstrapStatsigWysiwygMarkdownDefaultGate:       buildEnabledStatsigGate(bootstrapStatsigWysiwygMarkdownDefaultGate),
		bootstrapStatsigSubagentSupportInterrupt:         buildEnabledStatsigGate(bootstrapStatsigSubagentSupportInterrupt),
		bootstrapStatsigExplicitSubagentModels:           buildEnabledStatsigGate(bootstrapStatsigExplicitSubagentModels),
		bootstrapStatsigMcpDirectClientToolFetch:         buildEnabledStatsigGate(bootstrapStatsigMcpDirectClientToolFetch),
		bootstrapStatsigGlassCustomThemeSupport:          buildEnabledStatsigGate(bootstrapStatsigGlassCustomThemeSupport),
		bootstrapStatsigGlassAutomationsUI:               buildEnabledStatsigGate(bootstrapStatsigGlassAutomationsUI),
		bootstrapStatsigTerminalUI2:                      buildEnabledStatsigGate(bootstrapStatsigTerminalUI2),
		bootstrapStatsigDisableTerminalOutputUIStreaming: buildEnabledStatsigGate(bootstrapStatsigDisableTerminalOutputUIStreaming),
		bootstrapStatsigBrowserCanvas:                    buildEnabledStatsigGate(bootstrapStatsigBrowserCanvas),
		bootstrapStatsigEnableMultitaskMode:              buildEnabledStatsigGate(bootstrapStatsigEnableMultitaskMode),
		bootstrapStatsigDecomposeAlwaysLocalExtHostGate:  buildDisabledStatsigGate(bootstrapStatsigDecomposeAlwaysLocalExtHostGate),
		bootstrapStatsigCursorExtensionsIsolationV2Gate:  buildDisabledStatsigGate(bootstrapStatsigCursorExtensionsIsolationV2Gate),
		bootstrapStatsigCursorAgentWorkerExtension:       buildDisabledStatsigGate(bootstrapStatsigCursorAgentWorkerExtension),
	},
	DynamicConfigs: map[string]statsigDynamicConfigTemplate{
		bootstrapStatsigExperimentName: buildStatsigDynamicConfig(
			bootstrapStatsigExperimentName,
			map[string]any{bootstrapStatsigVariantParam: bootstrapStatsigVariantControl},
			bootstrapStatsigVariantControl,
		),
		bootstrapStatsigProductTipsConfigName: buildStatsigDynamicConfig(
			bootstrapStatsigProductTipsConfigName,
			map[string]any{
				"tips": []map[string]any{},
				"config": map[string]any{
					"intervalMs":       8000,
					"minClientVersion": "",
				},
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigIdleExtensionHostKiller: buildStatsigDynamicConfig(
			bootstrapStatsigIdleExtensionHostKiller,
			map[string]any{
				bootstrapStatsigIdleMinutesToKill:          0,
				bootstrapStatsigFreeMemoryPercentageToKill: 0,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigCanvasPromptTextConfig: buildStatsigDynamicConfig(
			bootstrapStatsigCanvasPromptTextConfig,
			buildCanvasPromptTextConfigValue(),
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigEditorBugbotConfig: buildStatsigDynamicConfig(
			bootstrapStatsigEditorBugbotConfig,
			map[string]any{
				"model":              "claude-4-5-sonnet-20250929",
				"iterations":         0,
				"agentic_iterations": 1,
				"agentic_model":      "claude-4.5-haiku",
				"context_lines":      10,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigExtensionMonitorControl: buildStatsigDynamicConfig(
			bootstrapStatsigExtensionMonitorControl,
			map[string]any{
				"local_enabled":              false,
				"backend_reporting_enabled":  false,
				"subsample_polling_rate_sec": 0,
				"sample_polling_rate_min":    0,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigExtensionSignatureBypass: buildStatsigDynamicConfig(
			bootstrapStatsigExtensionSignatureBypass,
			map[string]any{
				"extensionIds": []string{
					"nromanov.dotrush",
					"ms-python.python",
					"typescriptteam.native-preview",
					"typespec.typespec-vscode",
					"ms-toolsai.jupyter",
					"k3ndr1ckfu.tcl-language-support-for-vscode",
					"amiq.dvt",
				},
				"remoteVerificationMinVersion": "2.25.0",
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigGCTraceControl: buildStatsigDynamicConfig(
			bootstrapStatsigGCTraceControl,
			map[string]any{
				"enabled":            false,
				"drain_interval_sec": 120,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigInlineDiffPerformance: buildStatsigDynamicConfig(
			bootstrapStatsigInlineDiffPerformance,
			map[string]any{
				"maxDecorations": 100,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigLeakedDisposablesTracker: buildStatsigDynamicConfig(
			bootstrapStatsigLeakedDisposablesTracker,
			map[string]any{
				"enabled":          false,
				"reportIntervalMs": 60000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigMcpIPCTimeouts: buildStatsigDynamicConfig(
			bootstrapStatsigMcpIPCTimeouts,
			map[string]any{
				"metadata_timeout_ms":           10000,
				"lifecycle_timeout_ms":          10000,
				"dashboard_timeout_ms":          10000,
				"recovery_per_retry_timeout_ms": 10000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigMcpWakeProbeConfig: buildStatsigDynamicConfig(
			bootstrapStatsigMcpWakeProbeConfig,
			map[string]any{
				"probeOnFocus":              true,
				"probeOnBrowserOnline":      true,
				"probeOnElapsedTimeGap":     true,
				"elapsedTimeGapThresholdMs": 300000,
				"focusProbeDebounceMs":      60000,
				"onlineProbeDebounceMs":     5000,
				"resumeProbeDebounceMs":     5000,
				"startupGraceMs":            15000,
				"minProbeIntervalMs":        30000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigNALStallDetectorTimeout: buildStatsigDynamicConfig(
			bootstrapStatsigNALStallDetectorTimeout,
			map[string]any{
				"advisoryTimeoutMs": 20000,
				"failTimeoutMs":     30000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigSimulatedThinkingErrorTimeout: buildStatsigDynamicConfig(
			bootstrapStatsigSimulatedThinkingErrorTimeout,
			map[string]any{
				"timeout_ms": 120000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigPlaywrightLogConfigs: buildStatsigDynamicConfig(
			bootstrapStatsigPlaywrightLogConfigs,
			map[string]any{
				"logSizeThreshold": 25000,
				"logPreviewLines":  25,
				"logPreviewChars":  25000,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigRetryInterceptorParams: buildStatsigDynamicConfig(
			bootstrapStatsigRetryInterceptorParams,
			map[string]any{},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigUpdatePromptConfig: buildStatsigDynamicConfig(
			bootstrapStatsigUpdatePromptConfig,
			map[string]any{
				"min_hours_between_prompts": 48,
				"max_prompts_per_version":   3,
				"max_prompts_per_day":       1,
				"snooze_duration_hours":     72,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP2PingConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP2PingConfig,
			map[string]any{
				"enabled":                 []string{},
				"pingIdleConnection":      nil,
				"pingIntervalMs":          nil,
				"pingTimeoutMs":           nil,
				"idleConnectionTimeoutMs": nil,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP1KeepaliveConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP1KeepaliveConfig,
			map[string]any{
				"keepAliveInitialDelayMs": nil,
			},
			bootstrapStatsigLocalDefaultRule,
		),
		bootstrapStatsigHTTP2AgentPoolConfig: buildStatsigDynamicConfig(
			bootstrapStatsigHTTP2AgentPoolConfig,
			map[string]any{
				"poolSize": 4,
			},
			bootstrapStatsigLocalDefaultRule,
		),
	},
	LayerConfigs: map[string]map[string]any{},
	User: map[string]any{
		"userID": localUltraPaymentID,
		"email":  legacyruntime.InjectAccountEmail,
		"customIDs": map[string]string{
			"localUserID": localUltraPaymentID,
		},
	},
	HasUpdates: true,
	HashUsed:   "none",
	SDKParams: map[string]any{
		"stableID":                  localUltraPaymentID,
		"disableDiagnosticsLogging": true,
	},
}

func buildStatsigDynamicConfig(name string, value map[string]any, ruleID string) statsigDynamicConfigTemplate {
	name = strings.TrimSpace(name)
	ruleID = strings.TrimSpace(ruleID)
	if ruleID == "" {
		ruleID = bootstrapStatsigLocalDefaultRule
	}
	exposures := []statsigSecondaryExposure{}
	return statsigDynamicConfigTemplate{
		Name:                               name,
		Value:                              value,
		RuleID:                             ruleID,
		RuleIDCamel:                        ruleID,
		GroupName:                          ruleID,
		GroupNameCamel:                     ruleID,
		SecondaryExposures:                 exposures,
		SecondaryExposuresCamel:            exposures,
		UndelegatedSecondaryExposures:      exposures,
		UndelegatedSecondaryExposuresCamel: exposures,
		IsDeviceBased:                      false,
		IsDeviceBasedCamel:                 false,
		IsExperimentActive:                 false,
		IsExperimentActiveCamel:            false,
		IsUserInExperiment:                 false,
		IsUserInExperimentCamel:            false,
	}
}

func buildCanvasPromptTextConfigValue() map[string]any {
	return map[string]any{
		"skillDescription": "A Cursor Canvas is a live React app that the user can open beside the chat. You MUST use a canvas when the agent produces a standalone analytical artifact \u2014 quantitative analyses, billing investigations, security audits, architecture reviews, data-heavy content, timelines, charts, tables, interactive explorations, repeatable tools, or any response that benefits from visual layout. Especially prefer a canvas when presenting results from MCP tools (Datadog, Databricks, Linear, Sentry, Slack, etc.) where the data is the deliverable \u2014 render it in a rich canvas rather than dumping it into a markdown table or code block. If you catch yourself about to write a markdown table, stop and use a canvas instead. You MUST also read this skill whenever you create, edit, or debug any .canvas.tsx file.",
		"errorFixPromptTemplate": strings.Join([]string{
			"The canvas at `{canvasPath}` has the following error:",
			"",
			`"""`,
			"{errorMessage}",
			`"""`,
			"",
			"Check if the canvas SDK has changed since this canvas was created.",
			"Update the canvas to use the latest SDK components according to the supplied documentation in the canvas skill.",
		}, "\n"),
		"welcomePageEnabled":     true,
		"marketplaceCategoryKey": "canvas-featured",
		"marketplaceMaxCards":    4,
	}
}

func buildEnabledStatsigGate(name string) map[string]any {
	return buildStatsigGate(name, true, "local_enabled")
}

func buildDisabledStatsigGate(name string) map[string]any {
	return buildStatsigGate(name, false, "local_disabled")
}

func buildStatsigGate(name string, value bool, ruleID string) map[string]any {
	return map[string]any{
		"name":                            name,
		"value":                           value,
		"rule_id":                         ruleID,
		"ruleID":                          ruleID,
		"group_name":                      ruleID,
		"groupName":                       ruleID,
		"secondary_exposures":             []statsigSecondaryExposure{},
		"secondaryExposures":              []statsigSecondaryExposure{},
		"undelegated_secondary_exposures": []statsigSecondaryExposure{},
		"undelegatedSecondaryExposures":   []statsigSecondaryExposure{},
		"is_device_based":                 false,
		"isDeviceBased":                   false,
		"id_type":                         "userID",
		"idType":                          "userID",
	}
}

func buildServerTimePayload(*RequestContext) (map[string]any, error) {
	now := float64(time.Now().UnixMilli())
	return map[string]any{
		"receiveTimestamp":  now,
		"transmitTimestamp": now,
	}, nil
}

func buildServerConfigPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"configVersion": "local_cli_sandbox_defaults_disabled_v2",
		// "http2Config":              "HTTP2_CONFIG_FORCE_ALL_DISABLED",
		"cliSandboxDefaultEnabled": true,
	}, nil
}

func buildAvailableModelsPayload(reqCtx *RequestContext) (map[string]any, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	modelRefs := collectModelAdapterRefs(adapters)
	defaultModel := ""
	if len(modelRefs) > 0 {
		defaultModel = modelRefs[0]
	}
	return map[string]any{
		"backgroundComposerModelConfig": map[string]any{
			"bestOfNDefaultModels": append([]string(nil), modelRefs...),
			"defaultModel":         defaultModel,
			"fallbackModels":       append([]string(nil), modelRefs...),
		},
		"cmdKModelConfig": map[string]any{
			"defaultModel":   defaultModel,
			"fallbackModels": append([]string(nil), modelRefs...),
		},
		"composerModelConfig": map[string]any{
			"bestOfNDefaultModels": append([]string(nil), modelRefs...),
			"defaultModel":         defaultModel,
			"fallbackModels":       append([]string(nil), modelRefs...),
		},
		"deepSearchModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"disableUnusedModelsAfterNHours": availableModelsDisableUnusedHours,
		"models":                         buildAvailableModelEntries(adapters),
		"planExecutionModelConfig": map[string]any{
			"defaultModel":   defaultModel,
			"fallbackModels": append([]string(nil), modelRefs...),
		},
		"quickAgentModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"specModelConfig": map[string]any{
			"defaultModel": defaultModel,
		},
		"useModelParameters":                true,
		"upgradeUnchangedModelsAfterNHours": availableModelsUpgradeHours,
	}, nil
}

func buildDefaultModelNudgeDataPayload(reqCtx *RequestContext) (map[string]any, error) {
	adapters, err := loadConfiguredModelAdapters(reqCtx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"modelsWithNoDefaultSwitch": collectModelAdapterRefs(adapters),
		"nudgeDate":                 "0",
	}, nil
}

func buildBootstrapStatsigPayload(reqCtx *RequestContext) (map[string]any, error) {
	generatedAtMs := uint64(time.Now().UnixMilli())
	authID := resolveBootstrapStatsigAuthID(reqCtx)
	configJSON, err := buildBootstrapStatsigConfigJSON(int64(generatedAtMs), authID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"config":        string(configJSON),
		"generatedAtMs": generatedAtMs,
	}, nil
}

func buildFirstWindowStatsigDecisionPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"variant": bootstrapStatsigVariantControl,
		"reason":  bootstrapStatsigLocalDefaultRule,
	}, nil
}

func buildDashboardCurrentPeriodUsagePayload(*RequestContext) (map[string]any, error) {
	billingCycleStart := time.Now().Add(-localUltraBillingCycleDuration).UnixMilli()
	billingCycleEnd := time.Now().Add(10 * 365 * 24 * time.Hour).UnixMilli()
	return map[string]any{
		"autoModelSelectedDisplayMessage":  "Ultra plan active",
		"billingCycleEnd":                  billingCycleEnd,
		"billingCycleStart":                billingCycleStart,
		"displayMessage":                   "Ultra plan active",
		"displayThreshold":                 99999999,
		"enabled":                          true,
		"namedModelSelectedDisplayMessage": "Ultra plan active",
		"planUsage": map[string]any{
			"apiPercentUsed":   0,
			"apiSpend":         0,
			"autoPercentUsed":  0,
			"autoSpend":        0,
			"bonusTooltip":     "Ultra local account mock is active.",
			"includedSpend":    localUltraPlanIncludedCents,
			"limit":            localUltraPlanIncludedCents,
			"remaining":        localUltraPlanIncludedCents,
			"remainingBonus":   false,
			"totalPercentUsed": 0,
			"totalSpend":       0,
		},
		"spendLimitUsage": map[string]any{
			"limitType": "user",
		},
	}, nil
}

func buildDashboardTeamsPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"teams": []map[string]any{},
	}, nil
}

func buildDashboardManagedSkillsPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"skills": []map[string]any{},
	}, nil
}

func buildDashboardGetMePayload(reqCtx *RequestContext) (map[string]any, error) {
	authID := ""
	if reqCtx != nil {
		authID = authIDFromBearer(reqCtx.Headers.Get("authorization"))
	}
	if authID == "" {
		authID = authIDFromJWT(legacyruntime.InjectAuthToken)
	}
	if authID == "" {
		authID = localUltraPaymentID
	}

	return map[string]any{
		"authId":            authID,
		"userId":            localUltraDashboardUserID,
		"email":             legacyruntime.InjectAccountEmail,
		"firstName":         "Cursor",
		"lastName":          "Local",
		"createdAt":         time.Now().UTC().Format(time.RFC3339),
		"isEnterpriseUser":  false,
		"teamName":          "",
		"emailDomainType":   "personal",
		"country":           "US",
		"profilePictureUrl": "",
	}, nil
}

func buildDashboardUserPrivacyModePayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"privacyMode":                          "PRIVACY_MODE_NO_STORAGE",
		"hoursRemainingInGracePeriod":          0,
		"isEnforcedByTeam":                     false,
		"isNotMigratedToServerSourceOfTruth":   false,
		"partnerDataShare":                     false,
		"hasAcknowledgedGracePeriodDisclaimer": true,
	}, nil
}

func buildDashboardPlanInfoPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"planInfo": map[string]any{
			"planName":            "Ultra Plan",
			"includedAmountCents": localUltraPlanIncludedCents,
			"price":               "$200/mo",
			"billingCycleEnd":     time.Now().Add(10 * 365 * 24 * time.Hour).UnixMilli(),
		},
	}, nil
}

func buildDashboardUsageLimitStatusAndActiveGrantsPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"usageLimitPolicyStatus": map[string]any{
			"isInSlowPool":           false,
			"features":               map[string]string{},
			"canConfigureSpendLimit": true,
			"hasPendingRequest":      false,
			"allowedModelIds":        []string{},
			"allowedModelTags":       []string{},
		},
		"activeGrants": []map[string]any{},
	}, nil
}

func buildDashboardIsOnNewPricingPayload(*RequestContext) (map[string]any, error) {
	return map[string]any{
		"isOnNewPricing":   true,
		"isOptedOut":       false,
		"hasAutoSpillover": true,
		"dashboardUserId":  localUltraDashboardUserID,
	}, nil
}

func loadConfiguredModelAdapters(reqCtx *RequestContext) ([]legacyruntime.ModelAdapterConfig, error) {
	if reqCtx == nil || reqCtx.Deps == nil || reqCtx.Deps.SystemSettingService == nil {
		return []legacyruntime.ModelAdapterConfig{}, nil
	}
	ctx := context.Background()
	if reqCtx.Request != nil {
		ctx = reqCtx.Request.Context()
	}
	return reqCtx.Deps.SystemSettingService.ResolveModelAdapters(ctx)
}

func buildAvailableModelEntries(adapters []legacyruntime.ModelAdapterConfig) []map[string]any {
	if len(adapters) == 0 {
		return []map[string]any{}
	}
	output := make([]map[string]any, 0, len(adapters))
	for _, adapter := range adapters {
		channelID := strings.TrimSpace(adapter.ID)
		displayName := strings.TrimSpace(adapter.DisplayName)
		modelID := strings.TrimSpace(adapter.ModelID)
		tooltipData := strings.TrimSpace(adapter.TooltipData)
		if channelID == "" || modelID == "" {
			continue
		}
		modelDisplayName := displayName
		if modelDisplayName == "" {
			modelDisplayName = modelID
		}
		defaultThinkingEffort := defaultThinkingEffortForAdapter(adapter)
		output = append(output, map[string]any{
			"clientDisplayName":                  displayName,
			"defaultOn":                          true,
			"degradationStatus":                  "DEGRADATION_STATUS_UNSPECIFIED",
			"inputboxShortModelName":             displayName,
			"isRecommendedForBackgroundComposer": false,
			"name":                               channelID,
			"namedModelSectionIndex":             1,
			"parameterDefinitions":               buildThinkingEffortParameterDefinitions(adapter.Type),
			"serverModelName":                    channelID,
			"supportsAgent":                      true,
			"supportsImages":                     true,
			"supportsMaxMode":                    false,
			"supportsNonMaxMode":                 true,
			"supportsPlanMode":                   true,
			"supportsSandboxing":                 true,
			"supportsThinking":                   true,
			"tagline":                            thinkingEffortDisplayName(defaultThinkingEffort),
			"tooltipData": map[string]any{
				"markdownContent": tooltipData,
			},
			"tooltipDataForMaxMode": map[string]any{
				"markdownContent": tooltipData,
			},
			"variants": buildThinkingEffortVariants(adapter.Type, channelID, modelDisplayName, tooltipData, defaultThinkingEffort),
		})
	}
	return output
}

func buildThinkingEffortParameterDefinitions(adapterType string) []map[string]any {
	values := thinkingEffortValuesForAdapter(adapterType)
	options := make([]map[string]any, 0, len(values))
	for _, value := range values {
		options = append(options, map[string]any{
			"displayName":        thinkingEffortDisplayName(value),
			"increasesModelCost": value == "xhigh" || value == "max",
			"value":              value,
		})
	}
	return []map[string]any{{
		"id":                  modelRuntimeThinkingEffortParameterID,
		"isCycleableByHotkey": true,
		"markdownTooltip":     "Controls the model thinking intensity for this run.",
		"name":                "Thinking intensity",
		"parameterType": map[string]any{
			"enumParameter": map[string]any{
				"values": options,
			},
		},
	}}
}

func buildThinkingEffortVariants(adapterType string, channelID string, modelDisplayName string, tooltipData string, defaultThinkingEffort string) []map[string]any {
	values := orderThinkingEffortValues(thinkingEffortValuesForAdapter(adapterType), defaultThinkingEffort)
	channelID = strings.TrimSpace(channelID)
	modelDisplayName = strings.TrimSpace(modelDisplayName)
	variants := make([]map[string]any, 0, len(values))
	for _, value := range values {
		effortDisplayName := thinkingEffortDisplayName(value)
		variantDisplayName := buildThinkingEffortVariantDisplayName(modelDisplayName, value)
		variant := map[string]any{
			"displayName":              variantDisplayName,
			"displayNameOutsidePicker": variantDisplayName,
			"isDefaultNonMaxConfig":    value == defaultThinkingEffort,
			"isMaxMode":                false,
			"parameterValues":          []map[string]any{{"id": modelRuntimeThinkingEffortParameterID, "value": value}},
		}
		if normalizeAvailableModelThinkingEffort(value, true, "") != "disabled" {
			variant["tagline"] = effortDisplayName
		}
		if channelID != "" {
			variant["variantStringRepresentation"] = channelID + ":" + value
		}
		if strings.TrimSpace(tooltipData) != "" {
			variant["tooltipData"] = map[string]any{"markdownContent": tooltipData}
		}
		variants = append(variants, variant)
	}
	return variants
}

func buildThinkingEffortVariantDisplayName(modelDisplayName string, effortValue string) string {
	modelDisplayName = html.EscapeString(strings.TrimSpace(modelDisplayName))
	if normalizeAvailableModelThinkingEffort(effortValue, true, "") == "disabled" {
		return modelDisplayName
	}
	effortDisplayName := thinkingEffortDisplayName(effortValue)
	effortDisplayName = html.EscapeString(strings.TrimSpace(effortDisplayName))
	if modelDisplayName == "" {
		return `<span class="ui-model-picker__item-tagline" style="color: var(--cursor-text-secondary); white-space: nowrap;">:icon-brain: ` + effortDisplayName + `</span>`
	}
	return modelDisplayName + ` <span class="ui-model-picker__item-tagline" style="color: var(--cursor-text-secondary); white-space: nowrap;">:icon-brain: ` + effortDisplayName + `</span>`
}

func thinkingEffortValuesForAdapter(adapterType string) []string {
	values := []string{"disabled", "low", "medium", "high", "xhigh"}
	if strings.EqualFold(strings.TrimSpace(adapterType), "anthropic") {
		values = append(values, "max")
	}
	return values
}

func orderThinkingEffortValues(values []string, defaultValue string) []string {
	defaultValue = strings.ToLower(strings.TrimSpace(defaultValue))
	output := make([]string, 0, len(values))
	for _, value := range values {
		if strings.EqualFold(value, defaultValue) {
			output = append(output, value)
			break
		}
	}
	for _, value := range values {
		if !strings.EqualFold(value, defaultValue) {
			output = append(output, value)
		}
	}
	return output
}

func defaultThinkingEffortForAdapter(adapter legacyruntime.ModelAdapterConfig) string {
	if strings.EqualFold(strings.TrimSpace(adapter.Type), "anthropic") {
		return normalizeAvailableModelThinkingEffort(adapter.AnthropicThinkingEffort, true, "xhigh")
	}
	return normalizeAvailableModelThinkingEffort(adapter.ReasoningEffort, false, "medium")
}

func normalizeAvailableModelThinkingEffort(raw string, allowMax bool, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "disabled", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(raw))
	case "disable", "off", "none", "false", "no", "0":
		return "disabled"
	case "max":
		if allowMax {
			return "max"
		}
		return fallback
	default:
		return fallback
	}
}

func thinkingEffortDisplayName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "disabled":
		return "Disabled"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "XHigh"
	case "max":
		return "Max"
	default:
		return strings.TrimSpace(value)
	}
}

func collectModelAdapterRefs(adapters []legacyruntime.ModelAdapterConfig) []string {
	output := make([]string, 0, len(adapters))
	for _, adapter := range adapters {
		channelID := strings.TrimSpace(adapter.ID)
		if channelID == "" {
			continue
		}
		output = append(output, channelID)
	}
	return output
}

func resolveBootstrapStatsigAuthID(reqCtx *RequestContext) string {
	if reqCtx != nil {
		if authID := authIDFromBearer(reqCtx.Headers.Get("authorization")); authID != "" {
			return authID
		}
	}
	if authID := authIDFromJWT(legacyruntime.InjectAuthToken); authID != "" {
		return authID
	}
	return localUltraPaymentID
}

func authIDFromBearer(authorization string) string {
	authorization = strings.TrimSpace(authorization)
	if len(authorization) >= len("Bearer ") && strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		authorization = strings.TrimSpace(authorization[len("Bearer "):])
	}
	return authIDFromJWT(authorization)
}

func authIDFromJWT(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Sub)
}

func buildBootstrapStatsigConfigJSON(nowMs int64, authID string) ([]byte, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		authID = localUltraPaymentID
	}
	template := bootstrapStatsigTemplate
	template.Time = nowMs
	template.User = map[string]any{
		"userID": authID,
		"email":  legacyruntime.InjectAccountEmail,
		"customIDs": map[string]string{
			"localUserID": authID,
		},
	}

	// This template mirrors the Statsig initialize/bootstrap response shape that
	// the bundled client reads for experiments. hash_used stays "none" so the
	// experiment can be looked up by its plain name without spec hashing.
	//
	// Cursor currently branches on free_user_model_picker.variant. Known values
	// are "control", "locked_picker", and "grayed_models". Keep this template
	// centralized and update it first if the bundled Statsig bootstrap shape changes.
	return json.Marshal(template)
}
