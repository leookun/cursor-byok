// moa/provider.go MOA（Multi-model Orchestration Architecture）虚拟模型实现。
//
// MOA 作为第一个内置虚拟模型，实现了 Planner → 多专家 → Critic → Judge → Aggregator 的
// 工作流编排。每个专家节点通过绑定到已有 ModelAdapter 来调用实际 LLM。
package moa

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	vmconfig "cursor/internal/backend/virtualmodel/config"
	virtualmodel "cursor/internal/backend/virtualmodel"
	optimize "cursor/internal/backend/runtime/optimize"
)

// MOAModel 实现 VirtualModel 接口的 MOA 模型。
type MOAModel struct {
	config      *vmconfig.VirtualModelConfig
	workflow    *vmconfig.WorkflowConfig
	channelSvc  ChannelService
	optimize    *optimize.Runtime
}

// ChannelService 抽象模型渠道解析（由 forwarder/runtime 提供）。
type ChannelService interface {
	// ResolveChannel 按 adapterID 解析渠道信息。
	ResolveChannel(ctx context.Context, adapterID string) (*ChannelInfo, error)
	// CallAdapter 调用物理模型并收集结果。
	CallAdapter(ctx context.Context, info *ChannelInfo, messages []Message, systemPrompt string) (*AdapterResult, error)
}

// ChannelInfo 渠道信息。
type ChannelInfo struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	ModelID     string `json:"modelID"`
	Provider    string `json:"provider"`
	BaseURL     string `json:"baseURL"`
}

// Message 传递给适配器的消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// AdapterResult 适配器调用结果。
type AdapterResult struct {
	Text             string `json:"text"`
	ReasoningContent string `json:"reasoningContent,omitempty"`
	FinishReason     string `json:"finishReason,omitempty"`
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	DurationMS       int64  `json:"durationMS"`
}

// NewMOAModel 创建 MOA 模型实例。
func NewMOAModel(cfg *vmconfig.VirtualModelConfig, workflow *vmconfig.WorkflowConfig, channelSvc ChannelService) *MOAModel {
	return NewMOAModelWithOptimize(cfg, workflow, channelSvc, nil)
}

// NewMOAModelWithOptimize 创建带 Optimization Runtime 的 MOA 模型实例。
func NewMOAModelWithOptimize(cfg *vmconfig.VirtualModelConfig, workflow *vmconfig.WorkflowConfig, channelSvc ChannelService, optRuntime *optimize.Runtime) *MOAModel {
	if cfg == nil {
		cfg = vmconfig.DefaultMOAConfig()
	}
	if workflow == nil {
		workflow = vmconfig.DefaultMOAWorkflow()
	}
	return &MOAModel{
		config:     cfg,
		workflow:   workflow,
		channelSvc: channelSvc,
		optimize:   optRuntime,
	}
}

func (m *MOAModel) ID() string         { return vmconfig.MOAModelID }
func (m *MOAModel) DisplayName() string { return vmconfig.MOADisplayName }
func (m *MOAModel) Enabled() bool       { return m.config != nil && m.config.Enabled }

// AdapterMetadata 从 Planner adapter 继承元数据（tooltip + 上下文窗口等），
// 使 Cursor 模型选择器展示正确的 MOA 描述。
func (m *MOAModel) AdapterMetadata(ctx context.Context) virtualmodel.AdapterMetadata {
	meta := virtualmodel.AdapterMetadata{TooltipData: vmconfig.MOATooltipData}
	if m == nil || m.channelSvc == nil || m.config == nil {
		return meta
	}
	adapterID := m.resolveAdapterForRole(vmconfig.RolePlanner)
	if adapterID == "" {
		// fallback：找第一个非空节点 adapter
		for _, binding := range m.config.Nodes {
			if binding != nil && binding.AdapterID != "" {
				adapterID = binding.AdapterID
				break
			}
		}
	}
	if adapterID == "" {
		return meta
	}
	// ChannelInfo 目前不暴露上下文窗口等字段；tooltip 已足够。
	// 当 ChannelService 扩展元数据接口后，在此处填充其余字段。
	return meta
}

// HasChannelService 报告是否已注入生产/测试 ChannelService（生产路径必须为 true）。
func (m *MOAModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// ResolveChannelForTest 暴露 resolveChannel 供 host 集成测试验证注入的 ChannelService。
func (m *MOAModel) ResolveChannelForTest(ctx context.Context, adapterID string) (*ChannelInfo, error) {
	return m.resolveChannel(ctx, adapterID)
}

// Execute 执行 MOA 工作流。
func (m *MOAModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {
		return nil, fmt.Errorf("execute request is nil")
	}
	startTime := time.Now()

	// Step 1: Planner
	planResult, err := m.executePlanner(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("planner failed: %w", err)
	}

	// Step 2: 解析 Planner 输出，确定需要执行哪些专家
	activeRoles := m.parsePlannerOutput(planResult.text)

	// Step 3: 并行执行所有激活的专家节点
	expertResults := m.executeExperts(ctx, req, activeRoles, planResult.text)

	// Step 4: Critic（如有配置）
	var criticResult *nodeResult
	if m.hasNodeRole(vmconfig.RoleCritic) {
		criticResult = m.executeCritic(ctx, req, expertResults, planResult.text)
	}

	// Step 5: Judge（如有配置）
	var judgeResult *nodeResult
	if m.hasNodeRole(vmconfig.RoleJudge) {
		judgeResult = m.executeJudge(ctx, req, expertResults, criticResult, planResult.text)
	}

	// Step 6: Aggregator
	aggregatorResult := m.executeAggregator(ctx, req, expertResults, criticResult, judgeResult, planResult.text)

	// 收集结果并计算用量
	nodeResults, totalPrompt, totalCompletion := m.collectNodeResults(planResult, expertResults, criticResult, judgeResult, aggregatorResult, startTime)

	return &virtualmodel.ExecuteResult{
		Text:             aggregatorResult.text,
		ReasoningContent: "",
		FinishReason:     "stop",
		Usage: &virtualmodel.UsageSummary{
			PromptTokens:     totalPrompt,
			CompletionTokens: totalCompletion,
			TotalTokens:      totalPrompt + totalCompletion,
		},
		NodeResults: nodeResults,
	}, nil
}

// nodeResult 内部节点执行结果。
type nodeResult struct {
	nodeID    string
	role      vmconfig.NodeRole
	adapterID string
	text      string
	duration  time.Duration
	err       error
	usage     *nodeUsage
}

type nodeUsage struct {
	promptTokens     int
	completionTokens int
}

// executePlanner 执行规划器节点。
func (m *MOAModel) executePlanner(ctx context.Context, req *virtualmodel.ExecuteRequest) (*nodeResult, error) {
	adapterID := m.selectAdapterIDForRole(ctx, vmconfig.RolePlanner)
	systemPrompt := buildPlannerPrompt(m.workflow)
	userPrompt := fmt.Sprintf("User request:\n%s\n\nAnalyze this request and determine which expert roles are needed. Output ONLY valid JSON.", req.LatestUserText)

	channelInfo, err := m.resolveChannel(ctx, adapterID)
	if err != nil {
		return &nodeResult{
			nodeID:    "planner",
			role:      vmconfig.RolePlanner,
			adapterID: adapterID,
			err:       fmt.Errorf("resolve channel: %w", err),
		}, fmt.Errorf("resolve planner channel: %w", err)
	}

	start := time.Now()
	result, err := m.channelSvc.CallAdapter(ctx, channelInfo, []Message{
		{Role: "user", Content: userPrompt},
	}, systemPrompt)
	duration := time.Since(start)

	if err != nil {
		return &nodeResult{
			nodeID:    "planner",
			role:      vmconfig.RolePlanner,
			adapterID: adapterID,
			duration:  duration,
			err:       err,
		}, err
	}

	nr := &nodeResult{
		nodeID:    "planner",
		role:      vmconfig.RolePlanner,
		adapterID: adapterID,
		text:      result.Text,
		duration:  duration,
		usage: &nodeUsage{
			promptTokens:     result.PromptTokens,
			completionTokens: result.CompletionTokens,
		},
	}
	return nr, nil
}

// parsePlannerOutput 解析 planner 输出的 JSON，提取需要执行的 role 列表。
func (m *MOAModel) parsePlannerOutput(text string) []vmconfig.NodeRole {
	text = strings.TrimSpace(text)

	// 尝试提取 JSON
	jsonText := extractJSON(text)
	if jsonText == "" {
		// 无法解析，默认执行所有 conditional 节点
		var roles []vmconfig.NodeRole
		for _, node := range m.workflow.Nodes {
			if node.ExecutionMode == vmconfig.ModeConditional && node.Enabled {
				roles = append(roles, node.Role)
			}
		}
		return roles
	}

	var plan struct {
		Tasks []struct {
			Role   string `json:"role"`
			Reason string `json:"reason,omitempty"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(jsonText), &plan); err != nil {
		// 解析失败，默认执行所有
		var roles []vmconfig.NodeRole
		for _, node := range m.workflow.Nodes {
			if node.ExecutionMode == vmconfig.ModeConditional && node.Enabled {
				roles = append(roles, node.Role)
			}
		}
		return roles
	}

	seen := make(map[vmconfig.NodeRole]bool)
	var roles []vmconfig.NodeRole
	for _, task := range plan.Tasks {
		role := vmconfig.NodeRole(strings.TrimSpace(strings.ToLower(task.Role)))
		if role != "" && !seen[role] {
			seen[role] = true
			roles = append(roles, role)
		}
	}
	if len(roles) == 0 {
		for _, node := range m.workflow.Nodes {
			if node.ExecutionMode == vmconfig.ModeConditional && node.Enabled {
				roles = append(roles, node.Role)
			}
		}
	}
	return roles
}

// DefaultMaxParallelExperts is the default upper bound on concurrent expert
// adapter calls when MaxParallelExperts is unset or non-positive. It
// prevents N experts from spawning N simultaneous HTTP requests to the
// upstream provider. Tuned for typical provider rate limits; users who
// need stricter (e.g. low-RPM keys) or looser limits should set
// VirtualModelConfig.MaxParallelExperts explicitly.
const DefaultMaxParallelExperts = 4

// maxParallelExperts returns the configured expert concurrency cap, falling
// back to DefaultMaxParallelExperts when unset or non-positive.
func (m *MOAModel) maxParallelExperts() int {
	if m == nil || m.config == nil || m.config.MaxParallelExperts <= 0 {
		return DefaultMaxParallelExperts
	}
	return m.config.MaxParallelExperts
}

// executeExperts 并行执行激活的专家节点。
func (m *MOAModel) executeExperts(ctx context.Context, req *virtualmodel.ExecuteRequest, activeRoles []vmconfig.NodeRole, planText string) map[vmconfig.NodeRole]*nodeResult {
	results := make(map[vmconfig.NodeRole]*nodeResult)
	var mu sync.Mutex
	var wg sync.WaitGroup

	roleSet := make(map[vmconfig.NodeRole]bool)
	for _, r := range activeRoles {
		roleSet[r] = true
	}

	// Semaphore bounds concurrent adapter calls across all expert nodes.
	// Acquired before spawning the worker goroutine so that blocked
	// acquisitions still honor ctx cancellation.
	maxParallel := m.maxParallelExperts()
	if maxParallel < 1 {
		maxParallel = DefaultMaxParallelExperts
	}
	sem := make(chan struct{}, maxParallel)
	ctxCancelled := false

	for _, node := range m.workflow.Nodes {
		node := node
		if !node.Enabled {
			continue
		}
		shouldExecute := false
		switch node.ExecutionMode {
		case vmconfig.ModeAlways:
			shouldExecute = true
		case vmconfig.ModeConditional:
			shouldExecute = roleSet[node.Role]
		case vmconfig.ModeParallel:
			shouldExecute = true
		default:
			shouldExecute = false
		}
		// 跳过 planner / critic / judge / aggregator（由专门方法处理）
		if node.Role == vmconfig.RolePlanner || node.Role == vmconfig.RoleCritic ||
			node.Role == vmconfig.RoleJudge || node.Role == vmconfig.RoleAggregator {
			continue
		}
		if !shouldExecute {
			continue
		}

		// Acquire the semaphore before spawning the worker; this lets us
		// bail out promptly on ctx cancellation instead of queuing an
		// unbounded number of goroutines that would all block on the sem.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Context cancelled while waiting for a slot: stop spawning
			// additional experts. Already-running experts will observe
			// ctx cancellation inside CallAdapter (or return normally).
			ctxCancelled = true
		}
		if ctxCancelled {
			break
		}
		wg.Add(1)
		go func(n vmconfig.WorkflowNodeConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			result := m.executeSingleExpert(ctx, req, n, planText)
			mu.Lock()
			results[n.Role] = result
			mu.Unlock()
		}(node)
	}
	wg.Wait()
	return results
}

// executeSingleExpert 执行单个专家节点。
func (m *MOAModel) executeSingleExpert(ctx context.Context, req *virtualmodel.ExecuteRequest, node vmconfig.WorkflowNodeConfig, planText string) *nodeResult {
	adapterID := m.selectAdapterIDForRole(ctx, node.Role)
	systemPrompt := buildExpertPrompt(node.Role, planText, node)
	userPrompt := buildExpertUserPrompt(node.Role, req)

	channelInfo, err := m.resolveChannel(ctx, adapterID)
	if err != nil {
		return &nodeResult{
			nodeID:    node.ID,
			role:      node.Role,
			adapterID: adapterID,
			err:       fmt.Errorf("resolve channel: %w", err),
		}
	}

	start := time.Now()
	result, err := m.channelSvc.CallAdapter(ctx, channelInfo, []Message{
		{Role: "user", Content: userPrompt},
	}, systemPrompt)
	duration := time.Since(start)

	if err != nil {
		return &nodeResult{
			nodeID:    node.ID,
			role:      node.Role,
			adapterID: adapterID,
			duration:  duration,
			err:       err,
		}
	}

	// Optimization Runtime: 记录该节点的成本（用 modelID 匹配单价表）
	if m.optimize != nil && channelInfo != nil {
		costKey := channelInfo.ModelID
		if costKey == "" {
			costKey = channelInfo.Provider
		}
		m.optimize.RecordCost(costKey, result.PromptTokens, result.CompletionTokens)
	}

	return &nodeResult{
		nodeID:    node.ID,
		role:      node.Role,
		adapterID: adapterID,
		text:      result.Text,
		duration:  duration,
		usage: &nodeUsage{
			promptTokens:     result.PromptTokens,
			completionTokens: result.CompletionTokens,
		},
	}
}

// selectAdapterIDForRole 在已配置 ModelAdapter 绑定池内选择 adapter。
// 优先使用 Optimization.SelectOptimalCandidate；无池或关闭时回退 resolveAdapterForRole。
// 绝不新建 Model Registry——候选仅来自 VirtualModelConfig 已绑定的 adapterID。
func (m *MOAModel) selectAdapterIDForRole(ctx context.Context, role vmconfig.NodeRole) string {
	preferred := m.resolveAdapterForRole(role)
	if m == nil || m.optimize == nil || !m.optimize.Enabled() {
		return preferred
	}
	candidates := m.collectAdapterCandidates(ctx)
	if len(candidates) == 0 {
		return preferred
	}
	// 若角色有显式绑定，确保 preferred 在池中（即使未出现在其它节点）
	if preferred != "" {
		found := false
		for _, c := range candidates {
			if c.Key == preferred {
				found = true
				break
			}
		}
		if !found {
			if info, err := m.resolveChannel(ctx, preferred); err == nil && info != nil {
				hint := info.ModelID
				if hint == "" {
					hint = info.Provider
				}
				candidates = append(candidates, optimize.ProviderCandidate{
					Key:         preferred,
					CostHint:    hint,
					DisplayName: info.DisplayName,
				})
			} else {
				candidates = append(candidates, optimize.ProviderCandidate{Key: preferred, CostHint: preferred})
			}
		}
	}
	chosen := m.optimize.SelectOptimalCandidate(ctx, string(role), candidates)
	if chosen == "" {
		return preferred
	}
	return chosen
}

// collectAdapterCandidates 从 MOA 配置收集所有已启用的 adapter 绑定（用户 ModelAdapter 池子集）。
func (m *MOAModel) collectAdapterCandidates(ctx context.Context) []optimize.ProviderCandidate {
	if m == nil || m.config == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []optimize.ProviderCandidate
	add := func(adapterID string) {
		id := strings.TrimSpace(adapterID)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		hint := id
		display := ""
		if info, err := m.resolveChannel(ctx, id); err == nil && info != nil {
			if info.ModelID != "" {
				hint = info.ModelID
			} else if info.Provider != "" {
				hint = info.Provider
			}
			display = info.DisplayName
			// 也用解析后的 channel ID 作为 key 稳定性
			if info.ID != "" {
				id = info.ID
			}
		}
		out = append(out, optimize.ProviderCandidate{
			Key:         id,
			CostHint:    hint,
			DisplayName: display,
		})
	}
	if m.config.Planner != nil && m.config.Planner.Enabled {
		add(m.config.Planner.AdapterID)
	}
	for _, binding := range m.config.Nodes {
		if binding == nil || !binding.Enabled {
			continue
		}
		add(binding.AdapterID)
	}
	// 若没有任何 enabled binding，仍收集所有配置了 adapterID 的节点（兼容只填 ID 未写 enabled）
	if len(out) == 0 {
		if m.config.Planner != nil {
			add(m.config.Planner.AdapterID)
		}
		for _, binding := range m.config.Nodes {
			if binding == nil {
				continue
			}
			add(binding.AdapterID)
		}
	}
	return out
}

// executeCritic 执行批评节点。
func (m *MOAModel) executeCritic(ctx context.Context, req *virtualmodel.ExecuteRequest, expertResults map[vmconfig.NodeRole]*nodeResult, planText string) *nodeResult {
	adapterID := m.selectAdapterIDForRole(ctx, vmconfig.RoleCritic)
	systemPrompt := buildCriticPrompt()
	expertText := buildExpertResultsText(expertResults)
	userPrompt := fmt.Sprintf("Original request:\n%s\n\nExpert outputs:\n%s\n\nCritique the outputs above. Identify issues, gaps, errors, and risks.", req.LatestUserText, expertText)

	channelInfo, err := m.resolveChannel(ctx, adapterID)
	if err != nil {
		return &nodeResult{nodeID: "critic", role: vmconfig.RoleCritic, adapterID: adapterID, err: err}
	}

	start := time.Now()
	result, err := m.channelSvc.CallAdapter(ctx, channelInfo, []Message{
		{Role: "user", Content: userPrompt},
	}, systemPrompt)
	duration := time.Since(start)

	if err != nil {
		return &nodeResult{nodeID: "critic", role: vmconfig.RoleCritic, adapterID: adapterID, duration: duration, err: err}
	}
	return &nodeResult{
		nodeID:    "critic",
		role:      vmconfig.RoleCritic,
		adapterID: adapterID,
		text:      result.Text,
		duration:  duration,
		usage:     &nodeUsage{promptTokens: result.PromptTokens, completionTokens: result.CompletionTokens},
	}
}

// executeJudge 执行评判节点。
func (m *MOAModel) executeJudge(ctx context.Context, req *virtualmodel.ExecuteRequest, expertResults map[vmconfig.NodeRole]*nodeResult, criticResult *nodeResult, planText string) *nodeResult {
	adapterID := m.selectAdapterIDForRole(ctx, vmconfig.RoleJudge)
	systemPrompt := buildJudgePrompt()

	var parts []string
	parts = append(parts, "Original request: "+req.LatestUserText)
	parts = append(parts, "Expert outputs:\n"+buildExpertResultsText(expertResults))
	if criticResult != nil && criticResult.err == nil {
		parts = append(parts, "Critic feedback:\n"+criticResult.text)
	}
	userPrompt := strings.Join(parts, "\n\n")

	channelInfo, err := m.resolveChannel(ctx, adapterID)
	if err != nil {
		return &nodeResult{nodeID: "judge", role: vmconfig.RoleJudge, adapterID: adapterID, err: err}
	}

	start := time.Now()
	result, err := m.channelSvc.CallAdapter(ctx, channelInfo, []Message{
		{Role: "user", Content: userPrompt},
	}, systemPrompt)
	duration := time.Since(start)

	if err != nil {
		return &nodeResult{nodeID: "judge", role: vmconfig.RoleJudge, adapterID: adapterID, duration: duration, err: err}
	}
	return &nodeResult{
		nodeID:    "judge",
		role:      vmconfig.RoleJudge,
		adapterID: adapterID,
		text:      result.Text,
		duration:  duration,
		usage:     &nodeUsage{promptTokens: result.PromptTokens, completionTokens: result.CompletionTokens},
	}
}

// executeAggregator 执行聚合节点，生成最终输出。
func (m *MOAModel) executeAggregator(ctx context.Context, req *virtualmodel.ExecuteRequest, expertResults map[vmconfig.NodeRole]*nodeResult, criticResult *nodeResult, judgeResult *nodeResult, planText string) *nodeResult {
	adapterID := m.selectAdapterIDForRole(ctx, vmconfig.RoleAggregator)
	systemPrompt := buildAggregatorPrompt()

	var parts []string
	parts = append(parts, "Original request:\n"+req.LatestUserText)
	parts = append(parts, "Expert outputs:\n"+buildExpertResultsText(expertResults))
	if criticResult != nil && criticResult.err == nil {
		parts = append(parts, "Critic feedback:\n"+criticResult.text)
	}
	if judgeResult != nil && judgeResult.err == nil {
		parts = append(parts, "Judge evaluation:\n"+judgeResult.text)
	}
	userPrompt := strings.Join(parts, "\n\n")

	channelInfo, err := m.resolveChannel(ctx, adapterID)
	if err != nil {
		return &nodeResult{nodeID: "aggregator", role: vmconfig.RoleAggregator, adapterID: adapterID, err: err}
	}

	start := time.Now()
	result, err := m.channelSvc.CallAdapter(ctx, channelInfo, []Message{
		{Role: "user", Content: userPrompt},
	}, systemPrompt)
	duration := time.Since(start)

	if err != nil {
		return &nodeResult{nodeID: "aggregator", role: vmconfig.RoleAggregator, adapterID: adapterID, duration: duration, err: err}
	}
	return &nodeResult{
		nodeID:    "aggregator",
		role:      vmconfig.RoleAggregator,
		adapterID: adapterID,
		text:      result.Text,
		duration:  duration,
		usage:     &nodeUsage{promptTokens: result.PromptTokens, completionTokens: result.CompletionTokens},
	}
}

// resolveAdapterForRole 解析角色对应的 adapterID。
func (m *MOAModel) resolveAdapterForRole(role vmconfig.NodeRole) string {
	switch role {
	case vmconfig.RolePlanner:
		if m.config.Planner != nil && m.config.Planner.AdapterID != "" {
			return m.config.Planner.AdapterID
		}
	default:
		if binding, ok := m.config.Nodes[string(role)]; ok && binding != nil && binding.AdapterID != "" {
			return binding.AdapterID
		}
	}
	// fallback: 空 adapterID 表示使用默认（第一个已配置的 adapter）
	return ""
}

// hasNodeRole 检查 workflow 是否包含指定 role 的节点。
func (m *MOAModel) hasNodeRole(role vmconfig.NodeRole) bool {
	for _, node := range m.workflow.Nodes {
		if node.Role == role && node.Enabled {
			return true
		}
	}
	return false
}

// resolveChannel 解析渠道信息。
func (m *MOAModel) resolveChannel(ctx context.Context, adapterID string) (*ChannelInfo, error) {
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is not initialized — virtual model runtime requires a ChannelService adapter")
	}
	return m.channelSvc.ResolveChannel(ctx, adapterID)
}

// collectNodeResults 收集所有节点执行结果，返回结果列表和 token 用量汇总。
func (m *MOAModel) collectNodeResults(planner *nodeResult, experts map[vmconfig.NodeRole]*nodeResult, critic *nodeResult, judge *nodeResult, aggregator *nodeResult, startTime time.Time) ([]virtualmodel.NodeExecuteResult, int, int) {
	var results []virtualmodel.NodeExecuteResult
	totalPrompt := 0
	totalCompletion := 0

	addResult := func(nr *nodeResult) {
		if nr == nil {
			return
		}
		results = append(results, toNodeExecuteResult(nr, startTime))
		if nr.usage != nil {
			totalPrompt += nr.usage.promptTokens
			totalCompletion += nr.usage.completionTokens
		}
	}

	addResult(planner)
	for _, nr := range experts {
		addResult(nr)
	}
	if critic != nil {
		addResult(critic)
	}
	if judge != nil {
		addResult(judge)
	}
	addResult(aggregator)
	return results, totalPrompt, totalCompletion
}

func toNodeExecuteResult(nr *nodeResult, startTime time.Time) virtualmodel.NodeExecuteResult {
	if nr == nil {
		return virtualmodel.NodeExecuteResult{}
	}
	success := nr.err == nil
	errStr := ""
	if nr.err != nil {
		errStr = nr.err.Error()
	}
	return virtualmodel.NodeExecuteResult{
		NodeID:    nr.nodeID,
		Role:      nr.role,
		AdapterID: nr.adapterID,
		DurationMS: nr.duration.Milliseconds(),
		Success:   success,
		Error:     errStr,
		OutputText: truncateText(nr.text, 500),
	}
}

func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

// buildExpertResultsText 将专家结果拼接为文本。
func buildExpertResultsText(results map[vmconfig.NodeRole]*nodeResult) string {
	var parts []string
	for role, nr := range results {
		if nr.err != nil {
			parts = append(parts, fmt.Sprintf("[%s] ERROR: %s", role, nr.err.Error()))
		} else {
			parts = append(parts, fmt.Sprintf("[%s]\n%s", role, nr.text))
		}
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// extractJSON 从文本中提取 JSON 块。Delegates to the shared
// virtualmodel.ExtractJSONObject, which fixes the previous buggy
// strings.Index/strings.LastIndex implementation that broke whenever a '}'
// character appeared inside a JSON string value or fenced markdown block.
func extractJSON(text string) string {
	candidate, err := virtualmodel.ExtractJSONObject(text)
	if err != nil {
		return ""
	}
	return candidate
}
