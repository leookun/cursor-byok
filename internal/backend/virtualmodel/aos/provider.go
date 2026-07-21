package aos

import (
	"context"
	optimize "cursor/internal/backend/runtime/optimize"
	virtualmodel "cursor/internal/backend/virtualmodel"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	// Reuse MOA's ChannelService infrastructure (ADR-013: do not reinvent)
	vm_moa "cursor/internal/backend/virtualmodel/moa"
	// Phase 26b/26g: re-entry validation and anti-re-entry prompt injection
	// via forwarder bridge. Safe: forwarder does not import vm/aos, no cycle.
	forwarder "cursor/internal/backend/forwarder"
)

// PlanningAdvisor optionally contributes project-health guidance before Leader planning.// Host may inject an Evolver-backed advisor; production ChannelService remains required separately.
type PlanningAdvisor interface {
	AdvisePlanning(ctx context.Context, requirement string) (string, error)
}

// AOSModel implements the VirtualModel interface for AOS (AI Organization System).
type AOSModel struct {
	teamMu          sync.RWMutex
	team            *TeamProfile
	channelSvc      vm_moa.ChannelService
	optimize        *optimize.Runtime
	planningAdvisor PlanningAdvisor
	executionMode   string
	memberTimeout   time.Duration
	vmManager       *virtualmodel.Manager // Phase 26g: real VM manager for re-entry guard
	// channelResolver is optional and used only for AdapterMetadata inheritance.
	// When set (by host.go), AdapterMetadata can resolve the Leader adapter
	// to inherit ContextWindow / MaxTokens / ReasoningEffort from the physical
	// model config so Cursor sees correct parameters for the AOS entry.
	channelResolver vm_moa.ChannelResolver
}

// DefaultAOSMemberTimeout is the default timeout for waiting on a spawned// AOS member Task tool result. Configurable via SetMemberTimeout.// Production: 10 minutes. Tests should set a shorter duration.
const DefaultAOSMemberTimeout = 10 * time.Minute // SetMemberTimeout overrides the default timeout for waiting on spawned// member Task tool results. Use a short value (e.g. 100ms) in timeout tests.
func (m *AOSModel) SetMemberTimeout(timeout time.Duration) {
	if m == nil {

		return
	}
	m.memberTimeout = timeout
}

// SetVMManager injects the VirtualModel Manager for re-entry validation// (Phase 26g). When set, executeMemberTask uses it to dynamically check// whether the member's model ID is a registered virtual model, beyond the// static blocklist (aos, moa).
func (m *AOSModel) SetVMManager(mgr *virtualmodel.Manager) {
	if m == nil {

		return
	}
	m.vmManager = mgr
}

// NewAOSModel creates an AOS virtual model instance.// channelSvc must be non-nil in production (ADR-002).
func NewAOSModel(team *TeamProfile, channelSvc vm_moa.ChannelService, optRuntime *optimize.Runtime) *AOSModel {
	if team == nil {

		team = DefaultTeam("")
	}
	execMode := strings.TrimSpace(team.ExecutionMode)
	if execMode == "hybrid" {
		execMode = ExecutionModeCursorTask // hybrid 也走 cursor_task（并行子代理）
	}
	if execMode == "" {

		execMode = ExecutionModeCursorTask // 默认 cursor_task：AOS 始终用 Cursor 原生 Task 工具 spawn 子代理扮演组员，不依赖 Cursor 客户端选了 multitask
	}
	return &AOSModel{

		team: team,

		channelSvc: channelSvc,

		optimize: optRuntime,

		executionMode: execMode,
	}
}

func (m *AOSModel) ID() string { return ModelID }

func (m *AOSModel) DisplayName() string { return DisplayName }

func (m *AOSModel) Enabled() bool {
	if m == nil {
		return false
	}
	m.teamMu.RLock()
	defer m.teamMu.RUnlock()
	if m.team == nil {
		return false
	}
	if m.team.Leader.AdapterID != "" {
		return true
	}
	for _, member := range m.team.Members {
		if member.AdapterID != "" {
			return true
		}
	}
	return false
}

// HasChannelService reports whether a production/test ChannelService is injected.
func (m *AOSModel) HasChannelService() bool {
	return m != nil && m.channelSvc != nil
}

// LeaderAdapterID returns the leader's adapter ID from the team profile.
// Returns "" if the model or team is nil.
func (m *AOSModel) LeaderAdapterID() string {
	if m == nil {
		return ""
	}
	m.teamMu.RLock()
	defer m.teamMu.RUnlock()
	if m.team == nil {
		return ""
	}
	return strings.TrimSpace(m.team.Leader.AdapterID)
}

// AOSTooltipData is the description shown in Cursor's model selector for the
// AOS virtual model entry. It is returned as AdapterMetadata.TooltipData so
// AOS is distinguishable from MOA in the picker.
const AOSTooltipData = "AI Organization System — Leader/Member/Sprint 编排，按需调用组员"

// AdapterMetadata inherits context window / max tokens / reasoning effort
// from the Leader's physical ModelAdapter so the AOS entry shown to Cursor
// matches the underlying model.
//
// MUST stay cheap and non-recursive: called on every AvailableModels request.
// Prefer channelResolver.SelectChannelForModel (reads config snapshot only).
// Never call anything that re-enters ResolveModelAdapters / MergeVirtualModelAdapters.
func (m *AOSModel) AdapterMetadata(ctx context.Context) virtualmodel.AdapterMetadata {
	meta := virtualmodel.AdapterMetadata{
		TooltipData: AOSTooltipData,
	}
	if m == nil {
		return meta
	}
	m.teamMu.RLock()
	if m.team == nil {
		m.teamMu.RUnlock()
		return meta
	}
	leaderAdapter := strings.TrimSpace(m.team.Leader.AdapterID)
	if leaderAdapter == "" {
		for _, member := range m.team.Members {
			if strings.TrimSpace(member.AdapterID) != "" {
				leaderAdapter = member.AdapterID
				break
			}
		}
	}
	m.teamMu.RUnlock()
	if leaderAdapter == "" {
		return meta
	}
	// Prefer ChannelResolver (config-only, no network). Safe on AvailableModels path.
	if m.channelResolver != nil {
		ch, err := m.channelResolver.SelectChannelForModel(ctx, leaderAdapter)
		if err == nil && ch != nil {
			if ch.ContextWindowTokens > 0 {
				meta.ContextWindowTokens = ch.ContextWindowTokens
			}
			if ch.MaxTokens > 0 {
				meta.MaxCompletionTokens = ch.MaxTokens
			}
			if strings.TrimSpace(ch.ReasoningEffort) != "" {
				meta.ReasoningEffort = strings.TrimSpace(ch.ReasoningEffort)
			}
			if strings.TrimSpace(ch.AnthropicThinkingEffort) != "" {
				meta.AnthropicThinkingEffort = strings.TrimSpace(ch.AnthropicThinkingEffort)
			}
			if ch.ThinkingBudgetTokens > 0 {
				meta.ThinkingBudgetTokens = ch.ThinkingBudgetTokens
			}
			return meta
		}
	}
	return meta
}

// SetPlanningAdvisor injects an optional pre-planning health advisor (ADR-035).
func (m *AOSModel) SetPlanningAdvisor(advisor PlanningAdvisor) {
	if m == nil {

		return
	}
	m.planningAdvisor = advisor
}

// SetChannelResolver injects the ChannelResolver used to resolve Leader
// adapter metadata for AdapterMetadata(). This enables AOS to inherit
// ContextWindow / MaxTokens / ReasoningEffort from the Leader's physical
// model config so Cursor sees the correct parameters.
func (m *AOSModel) SetChannelResolver(r vm_moa.ChannelResolver) {
	if m == nil {
		return
	}
	m.channelResolver = r
}

// RecognizeMembersResult is the DTO returned by RecognizeMembers.
// Each entry corresponds to one member of the team.
type RecognizeMembersResult struct {
	Members []RecognizedMember `json:"members"`
}

// RecognizedMember carries the tags the Leader inferred for a single member.
type RecognizedMember struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	Summary string   `json:"summary,omitempty"`
}

// RecognizeMembers asks the Leader to read each member's name + systemPrompt
// and infer a small, fixed set of routing tags. The inferred tags are written
// back into MemberConfig.Tags so that subsequent Leader planning reads only
// the short tags (not the long system prompts) when dispatching tasks.
//
// Workflow:
//  1. Build a "roster" prompt listing each member's ID/Name/SystemPrompt.
//  2. Send it to the Leader adapter.
//  3. Parse JSON { "members": [{ "id": "", "tags": [], "summary": "" }] }.
//  4. Write tags back to m.team.Members[*].Tags (in place).
//
// The method is safe to call before the first Execute(); it does not require
// a Workspace or trace. It requires a non-nil ChannelService (production).
func (m *AOSModel) RecognizeMembers(ctx context.Context) (*RecognizeMembersResult, error) {
	if m == nil {
		return nil, fmt.Errorf("aos model is nil")
	}
	if m.channelSvc == nil {
		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}
	m.teamMu.RLock()
	if m.team == nil {
		m.teamMu.RUnlock()
		return nil, fmt.Errorf("team profile is nil")
	}
	if len(m.team.Members) == 0 {
		m.teamMu.RUnlock()
		return &RecognizeMembersResult{Members: []RecognizedMember{}}, nil
	}

	prompt := buildMemberRecognitionPrompt(m.team)
	leaderAdapterID := m.team.Leader.AdapterID
	m.teamMu.RUnlock()
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	// Use the Leader framework prompt (truncated) as system prompt so the
	// Leader stays in character during roster review.
	systemPrompt := LeaderFrameworkPrompt
	if len(systemPrompt) > 400 {
		systemPrompt = systemPrompt[:400]
	}
	result, err := m.callAdapter(ctx, leaderAdapterID, messages, systemPrompt)
	if err != nil {
		return nil, fmt.Errorf("recognize members: %w", err)
	}
	parsed, parseErr := parseRecognizedMembers(result.Text)
	if parseErr != nil {
		// Fail-open: return an empty result with the error so the caller can
		// surface a message without blocking configuration.
		return &RecognizeMembersResult{
			Members: []RecognizedMember{},
		}, fmt.Errorf("parse leader recognition output: %w", parseErr)
	}

	// Write tags back into the team profile (in-place mutation).
	byID := make(map[string]*RecognizedMember, len(parsed.Members))
	for i := range parsed.Members {
		byID[parsed.Members[i].ID] = &parsed.Members[i]
	}
	m.teamMu.Lock()
	defer m.teamMu.Unlock()
	if m.team == nil {
		return nil, fmt.Errorf("team profile is nil")
	}
	for i := range m.team.Members {
		member := &m.team.Members[i]
		if rm, ok := byID[member.ID]; ok {
			member.Tags = dedupeAndTrimTags(rm.Tags)
			if rm.Name != "" {
				member.Name = rm.Name
			}
		} else if len(member.Tags) == 0 {
			// Leader didn't return tags for this member; leave a sentinel
			// so ResolveMemberByTags still has something to match against.
			member.Tags = []string{"general"}
		}
	}
	return parsed, nil
}

// dedupeAndTrimTags normalizes the tag list from the Leader output:
// trims whitespace, lowercases, drops empties, dedupes preserving order.
func dedupeAndTrimTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// Execute runs the AOS organization workflow.// Phase 26g: sets aosDepth=1 in context to prevent AOS re-entry in// downstream spawns.
func (m *AOSModel) Execute(ctx context.Context, req *virtualmodel.ExecuteRequest) (*virtualmodel.ExecuteResult, error) {
	if req == nil {

		return nil, fmt.Errorf("execute request is nil")
	}
	if m.channelSvc == nil {

		return nil, fmt.Errorf("channel service is nil (production must inject non-nil)")
	}
	// Keep the team profile stable for the complete workflow turn. RecognizeMembers
	// takes the write lock only while applying parsed tags/names, never while
	// waiting on the Leader adapter.
	m.teamMu.RLock()
	defer m.teamMu.RUnlock()
	if m.team == nil {
		return nil, fmt.Errorf("team profile is nil")
	}
	// [修复] ThinkingEffort / MaxTokens 透传：直接注入 ctx，
	// 使 callAdapter → ChannelService.CallAdapter 能读取并真正应用用户在
	// Cursor 模型选择器中选择的思考强度与最大 tokens。
	ctx = virtualmodel.WithThinkingEffort(ctx, req.ThinkingEffort)
	ctx = virtualmodel.WithMaxTokens(ctx, req.MaxTokens)

	// Phase 26g: mark context with AOS depth to prevent re-entry.
	// Any downstream attempt to route to a virtual model (aos, moa)
	// while depth >= 1 will be rejected by the forwarder guard.
	ctx = virtualmodel.ContextWithAOSDepth(ctx, 1)
	startTime := time.Now()
	sessionID := fmt.Sprintf("aos-%d", startTime.UnixNano())
	// Initialize workspace
	ws := NewWorkspace(sessionID)
	// 优先用 forwarder 已提取的 LatestUserText（startVirtualStream 从
	// Cursor 请求里正确解析了最新用户消息）；为空时才 fallback 到
	// extractUserText 遍历 Messages。Cursor 发来的消息序列里 user
	// 消息的 role/位置可能不规整，LatestUserText 是最可靠来源。
	ws.Requirement = strings.TrimSpace(req.LatestUserText)
	if ws.Requirement == "" {
		ws.Requirement = extractUserText(req.Messages)
	}
	// Initialize execution trace
	trace := NewExecutionTrace(sessionID, req.RequestID, ModelID)
	ctx = withExecutionTrace(ctx, trace)
	// Phase 9: persist the full trace to disk at the end of execution (fail-open).
	// ws.Requirement is the original user input, stored for later Replay.
	userPrompt := ws.Requirement
	defer SaveTrace(trace, userPrompt)
	// Step 1: Leader requirement analysis + task planning
	plan, _, _, err := m.executeLeaderPlanningTraced(ctx, req, ws, trace)
	if err != nil {
		// Leader planning 没产出 JSON，但已有可直接返回的文本（常见于
		// 简单问候/闲聊）。跳过 sprint 循环，直接返回 Leader 输出。
		var fallback *leaderPlainTextFallback
		if errors.As(err, &fallback) {
			trace.Finalize()
			return resultWithTrace(trace, fallback.Text, m.team), nil
		}
		return nil, fmt.Errorf("leader planning failed: %w", err)
	}
	ws.Architecture = plan.Architecture
	ws.Tasks = plan.Tasks
	// Step 2: Leader architecture design (personal work)
	if plan.Architecture != "" {

		ws.Decisions = append(ws.Decisions, "Architecture designed by Leader")
	}
	trace.TasksTotal = len(plan.Tasks)
	// Step 3: Execute tasks (members + leader)
	maxIter := m.team.Sprints.MaxIterations
	if maxIter <= 0 {

		maxIter = 3
	}
	for sprint := 1; sprint <= maxIter; sprint++ {

		err := m.executeSprintTraced(ctx, req, ws, plan, trace)

		if err != nil {

			return nil, fmt.Errorf("sprint %d failed: %w", sprint, err)

		}

		// Step 4: Leader review

		reviewResult, _, _, err := m.executeLeaderReviewTraced(ctx, req, ws, trace)

		if err != nil {

			return nil, fmt.Errorf("leader review failed: %w", err)

		}

		if reviewResult.Status == "accepted" || sprint == maxIter {

			// Step 5: Leader merge + final polish

			finalText, _, _, err := m.executeLeaderMergeTraced(ctx, req, ws, trace)

			if err != nil {

				return nil, fmt.Errorf("leader merge failed: %w", err)

			}

			trace.Sprints = sprint

			trace.Finalize()

			executeResult := resultWithTrace(trace, finalText, m.team)

			safeText := executeResult.Text
			executeResult.NodeResults = []virtualmodel.NodeExecuteResult{{

				NodeID: "leader",

				AdapterID: m.team.Leader.AdapterID,

				Success: true,

				OutputText: safeText,
			},
			}

			return executeResult, nil

		}

		// Needs revision: update tasks based on feedback

		for i := range ws.Tasks {

			if ws.Tasks[i].Status == "rejected" {

				ws.Tasks[i].Status = "pending"

			}

		}
	}
	// Fallback: return best effort
	finalText, _, _, _ := m.executeLeaderMergeTraced(ctx, req, ws, trace)
	trace.Sprints = maxIter
	trace.Finalize()
	executeResult := resultWithTrace(trace, finalText, m.team)
	executeResult.PhaseText = "" // AOS must not stream internal phase status to Cursor
	safeText := executeResult.Text
	executeResult.NodeResults = []virtualmodel.NodeExecuteResult{{

		Role: "leader",

		OutputText: safeText,

		Success: true,
	},
	}
	return executeResult, nil
}

// executeLeaderPlanning runs the Leader's requirement analysis, intent classification,
// and task planning in a single LLM call (IntentGate pattern).
//
// The Leader outputs JSON with an "intent" field:
//   - "simple"  → Leader's "reply" is returned directly via leaderPlainTextFallback
//   - "complex" → tasks array is parsed into a TaskPlan for sprint execution
//
// If JSON parsing fails entirely, the Leader's raw text is returned as fallback
// (same behavior as before — simple conversations that don't produce JSON).
func (m *AOSModel) executeLeaderPlanning(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace) (*TaskPlan, error) {
	leaderPrompt := strings.ReplaceAll(LeaderFrameworkPrompt, "{members_info}", m.team.MembersInfo())
	requirement := ws.Requirement
	if m.planningAdvisor != nil {

		if advice, err := m.planningAdvisor.AdvisePlanning(ctx, requirement); err == nil {

			advice = strings.TrimSpace(advice)

			if advice != "" {

				requirement = requirement + "\n\n[Project Health Advisory]\n" + advice

			}

		}
	}
	systemPrompt := fmt.Sprintf("%s\n\nUser requirement: %s\n\nClassify the intent and output JSON as specified in the format section.", leaderPrompt, requirement)
	messages := []vm_moa.Message{

		{Role: "user", Content: systemPrompt},
	}
	result, err := m.callAdapter(ctx, m.team.Leader.AdapterID, messages, leaderPrompt)
	if err != nil {

		return nil, err
	}
	// Update trace with token info
	if result != nil {

		_ = result.PromptTokens + result.CompletionTokens // tokens recorded in callAdapter via RecordCost
	}

	// IntentGate: parse Leader's JSON output (intent + tasks/reply)
	out, ok := parseLeaderPlanOutput(result.Text)
	if !ok {
		// Leader didn't output valid JSON — return raw text as fallback
		return nil, &leaderPlainTextFallback{Text: SanitizeUserFacingText(result.Text)}
	}

	// Simple intent: Leader answered directly, skip sprint entirely
	if isSimpleIntent(out) {
		reply := SanitizeUserFacingText(strings.TrimSpace(out.Reply))
		if reply == "" {
			reply = SanitizeUserFacingText(result.Text)
		}
		return nil, &leaderPlainTextFallback{Text: reply}
	}

	// Complex intent: convert to TaskPlan with assignee validation
	plan := out.toTaskPlan(m.team)
	if plan == nil || len(plan.Tasks) == 0 {
		// Leader said "complex" but didn't produce tasks — treat as fallback
		return nil, &leaderPlainTextFallback{Text: SanitizeUserFacingText(result.Text)}
	}
	return plan, nil
}

// leaderPlainTextFallback 表示 Leader planning 没产出 JSON 任务计划，
// 但已有可直接返回给用户的文本。Execute 检测到此 sentinel 后跳过
// sprint 循环，直接把 Text 作为最终输出。
type leaderPlainTextFallback struct {
	Text string
}

func (e *leaderPlainTextFallback) Error() string {
	return "leader planning did not produce a JSON task plan; returning leader text directly"
}

// executeSprint executes all pending tasks in a single sprint.// In cursor_task mode, uses ExecuteBatch (two-phase spawn then resolve)
// to align with Cursor Multitask semantics: emit all Task tool calls// before blocking on any result (Phase 26e).
func (m *AOSModel) executeSprint(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, plan *TaskPlan) error {
	pendingTasks := make([]Task, 0)
	for _, t := range ws.Tasks {

		if t.Status == "pending" {

			pendingTasks = append(pendingTasks, t)

		}
	}
	if len(pendingTasks) == 0 {

		return nil
	}
	maxParallel := m.team.Workflow.MaxParallel
	if maxParallel <= 0 {

		maxParallel = 4
	}
	if m.executionMode == ExecutionModeCursorTask {

		// Phase 26e: batch spawn  ?parallel resolve (Multitask-aligned)

		scheduler := NewWorkflowScheduler(maxParallel)

		spawn := func(ctx context.Context, task Task) (string, error) {

			if task.AssigneeID == "leader" || task.AssigneeID == "" {

				// Leader tasks complete synchronously; return task ID as execID

				result, err := m.executeLeaderTask(ctx, req, ws, task)

				if err != nil {

					return "", err

				}

				// Persist Leader work before the resolve phase and before review/merge.
				ws.SetTaskResult(task.ID, task.AssigneeID, result)

				return task.ID, nil

			}

			return m.spawnMemberTask(ctx, req, ws, task)

		}

		resolve := func(ctx context.Context, task Task, execID string) (string, error) {

			if task.AssigneeID == "leader" || task.AssigneeID == "" {

				// Leader task was already completed in spawn phase;

				// look up result from workspace

				ws.mu.Lock()
				defer ws.mu.Unlock()
				for _, t := range ws.Tasks {
					if t.ID == task.ID {
						return t.Result, nil
					}
				}
				return "", fmt.Errorf("leader task %s not found in workspace", task.ID)

			}

			return m.resolveMemberTask(ctx, task, execID)

		}

		// Pass the full workspace set so completed tasks from prior levels/sprints
		// can satisfy dependencies of the current pending tasks.
		return scheduler.ExecuteBatch(ctx, ws.Tasks, spawn, resolve, ws)
	}
	// Internal mode: use WorkflowScheduler (dependency-aware parallel)
	scheduler := NewWorkflowScheduler(maxParallel)
	executor := func(ctx context.Context, task Task) (string, error) {

		if task.AssigneeID == "leader" || task.AssigneeID == "" {

			return m.executeLeaderTask(ctx, req, ws, task)

		}

		return m.executeMemberTask(ctx, req, ws, task)
	}
	// Pass the full workspace set so completed tasks from prior levels/sprints
	// can satisfy dependencies of the current pending tasks.
	return scheduler.Execute(ctx, ws.Tasks, executor, ws)
}

// executeLeaderTask handles a task that the Leader does personally.
func (m *AOSModel) executeLeaderTask(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, task Task) (string, error) {
	prompt := fmt.Sprintf("Task: %s\n\nArchitecture context: %s\n\nComplete this task.", task.Description, ws.Architecture)
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, m.team.Leader.AdapterID, messages, LeaderFrameworkPrompt)
	if err != nil {

		return "", err
	}
	return result.Text, nil
}

// executeMemberTask dispatches a task to the assigned member.// When executionMode is "cursor_task", it spawns a Cursor-native Task tool call// via the context-injected AOSMemberSpawnerFunc (Phase 26b). Otherwise, it// calls callAdapter directly (existing behavior, default).//// Re-entry guard: In cursor_task mode, ValidateNoAOSReEntry is called on the// member's model/adapter ID before spawning (Phase 26g minimal slice).// The spawner injects AntiReEntryPromptSuffix into the member prompt.
func (m *AOSModel) executeMemberTask(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, task Task) (string, error) {
	member, ok := m.team.FindMember(task.AssigneeID)
	if !ok {
		// Fallback: try tag-based matching from task description
		taskTags := extractTaskTags(task.Description, m.team)
		matched, score, found := m.team.ResolveMemberByTags(taskTags)
		if found && score > 0 {
			member = matched
		} else {
			return "", fmt.Errorf("member %s not found (tags: %v, no fallback match)", task.AssigneeID, taskTags)
		}
	}
	// [修复] member.SystemPrompt 传递：spawnMemberTask / executeMemberTask
	// cursor_task 分支必须将成员的 SystemPrompt 嵌入到 prompt 中，使子代理
	// 真正按照成员配置的角色执行（用户需求：组员可使用 cursor 内设定的 subagent）。
	systemPart := ""
	if member.SystemPrompt != "" {
		systemPart = member.SystemPrompt + "\n\n"
	}
	prompt := fmt.Sprintf("%sTask: %s\n\nArchitecture context: %s\n\nComplete this task according to your role.", systemPart, task.Description, ws.Architecture)
	if m.executionMode == ExecutionModeCursorTask {

		memberStart := time.Now()

		// Phase 26b: spawn via Cursor-native Task tool call

		spawner := virtualmodel.GetAOSMemberSpawner(ctx)

		if spawner == nil {

			return "", fmt.Errorf("cursor_task mode requires AOS member spawner in context (forwarder must inject via WithAOSMemberSpawner)")

		}

		// Phase 26g minimal slice: validate no AOS re-entry on the member's model

		// Uses real vmManager when available (injected via SetVMManager).

		if err := forwarder.ValidateNoAOSReEntry(member.AdapterID, m.vmManager); err != nil {

			return "", fmt.Errorf("re-entry blocked for member %s (adapter %s): %w",

				task.AssigneeID, member.AdapterID, err)

		}

		// Inject anti-re-entry constraint into the prompt

		memberPrompt := forwarder.InjectAOSAntiReEntryPrompt(prompt)

		// Phase 26c: compute execID deterministically BEFORE spawn so we can

		// register the result expectation with the registry before the result

		// potentially arrives (avoids race).

		execID := fmt.Sprintf("aos-member-%s-%s", task.ID, task.AssigneeID)

		// Phase 26f: helper to record member trace node

		recordMemberTrace := func(trace *ExecutionTrace, status, errMsg string) {

			if trace == nil {

				return

			}

			trace.AddNode(TraceNode{

				Role: task.AssigneeID,

				Action: "execution",

				ExecutionMode: m.executionMode,

				Spawned: true,

				AdapterID: member.AdapterID,

				TaskID: task.ID,

				ExecID: execID,

				Status: status,

				Duration: time.Since(memberStart),

				StartTime: memberStart,

				Error: errMsg,
			})

		}

		// Register expectation with the AOSResultRegistry (if available).

		// The forwarder's handleExecResult will Resolve this when the Task

		// tool result arrives. Must be done before spawn.

		reg := virtualmodel.GetAOSResultRegistry(ctx)

		var resultCh <-chan virtualmodel.AOSMemberResult

		if reg != nil {

			resultCh = reg.Expect(execID)

		}

		// Spawn the member task

		_, err := spawner(task.ID, task.AssigneeID, memberPrompt, member.AdapterID, task.Description)

		if err != nil {

			if reg != nil {

				reg.Remove(execID)

			}

			recordMemberTrace(executionTraceFromContext(ctx), "error", err.Error())

			return "", fmt.Errorf("spawn member %s: %w", task.AssigneeID, err)

		}

		// Wait for the spawned Task tool result or timeout.

		if reg != nil && resultCh != nil {

			timeout := m.memberTimeout

			if timeout <= 0 {

				timeout = DefaultAOSMemberTimeout

			}

			result, err := reg.WaitCtx(ctx, execID, timeout)
			if err != nil {
				recordMemberTrace(executionTraceFromContext(ctx), "error", err.Error())
				return "", fmt.Errorf("resolve member %s execID=%s: %w", task.AssigneeID, execID, err)
			}
			if result.Error != nil {
				recordMemberTrace(executionTraceFromContext(ctx), "error", result.Error.Error())
				return "", fmt.Errorf("member %s result error: %w", task.AssigneeID, result.Error)
			}
			recordMemberTrace(executionTraceFromContext(ctx), "ok", "")
			return result.Text, nil

		}

		// No registry in context  ?return confirmation but note missing registry.

		// This path is unusual (should only happen in tests or misconfigured

		// forwarder); the registry is injected by runProviderStream alongside

		// the spawner.

		recordMemberTrace(executionTraceFromContext(ctx), "ok", "no result registry")

		return fmt.Sprintf("[AOS Member %s spawned via Cursor Task, execID=%s. No result registry  ?result not collected.]",

			task.AssigneeID, execID), nil
	}
	// Default: direct callAdapter (internal) mode
	internalStart := time.Now()
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, member.AdapterID, messages, member.SystemPrompt)
	if err != nil {

		return "", err
	}
	// Phase 26f: record internal member trace node
	if trace := executionTraceFromContext(ctx); trace != nil {

		trace.AddNode(TraceNode{

			Role: task.AssigneeID,

			Action: "execution",

			ExecutionMode: ExecutionModeInternal,

			Spawned: false,

			AdapterID: member.AdapterID,

			TaskID: task.ID,

			Status: "ok",

			Duration: time.Since(internalStart),

			StartTime: internalStart,
		})
	}
	return result.Text, nil
}

// spawnMemberTask performs Phase 1 of batch execution (Phase 26e).// Pre-registers the result channel via AOSResultRegistry.Expect, validates// AOS re-entry using the real vmManager (Phase 26g), injects anti-re-entry// prompt, and emits the spawn via the context-injected spawner.//// Returns execID for result correlation in resolveMemberTask.
func (m *AOSModel) spawnMemberTask(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, task Task) (string, error) {
	member, ok := m.team.FindMember(task.AssigneeID)
	if !ok {

		return "", fmt.Errorf("member %s not found", task.AssigneeID)
	}
	// [修复] member.SystemPrompt 传递（spawnMemberTask 路径）
	systemPart := ""
	if member.SystemPrompt != "" {
		systemPart = member.SystemPrompt + "\n\n"
	}
	prompt := fmt.Sprintf("%sTask: %s\n\nArchitecture context: %s\n\nComplete this task according to your role.",

		systemPart, task.Description, ws.Architecture)
	spawner := virtualmodel.GetAOSMemberSpawner(ctx)
	if spawner == nil {

		return "", fmt.Errorf("cursor_task mode requires AOS member spawner in context")
	}
	// Phase 26g: validate re-entry using real vmManager
	if err := forwarder.ValidateNoAOSReEntry(member.AdapterID, m.vmManager); err != nil {

		return "", fmt.Errorf("re-entry blocked for member %s (adapter %s): %w",

			task.AssigneeID, member.AdapterID, err)
	}
	memberPrompt := forwarder.InjectAOSAntiReEntryPrompt(prompt)
	execID := fmt.Sprintf("aos-member-%s-%s", task.ID, task.AssigneeID)
	// Pre-register result channel with the registry
	reg := virtualmodel.GetAOSResultRegistry(ctx)
	if reg != nil {

		reg.Expect(execID)
	}
	// Emit the spawn
	_, err := spawner(task.ID, task.AssigneeID, memberPrompt, member.AdapterID, task.Description)
	if err != nil {

		if reg != nil {

			reg.Remove(execID)

		}

		return "", fmt.Errorf("spawn member %s: %w", task.AssigneeID, err)
	}
	// Phase 26f: record spawn trace node with member model info
	if trace := executionTraceFromContext(ctx); trace != nil {

		trace.AddNode(TraceNode{

			Role: task.AssigneeID,

			Action: "spawn",

			ExecutionMode: m.executionMode,

			Spawned: true,

			AdapterID: member.AdapterID,

			TaskID: task.ID,

			ExecID: execID,

			Status: "ok",
		})
	}
	return execID, nil
}

// resolveMemberTask performs Phase 2 of batch execution (Phase 26e).// Waits for the spawned member's result via the pre-registered channel// in AOSResultRegistry. The execID was returned by spawnMemberTask.// Phase 26f: records timing and result in execution trace.
func (m *AOSModel) resolveMemberTask(ctx context.Context, task Task, execID string) (string, error) {
	resolveStart := time.Now()
	reg := virtualmodel.GetAOSResultRegistry(ctx)
	if reg == nil {

		// No registry  ?cannot wait for result; return descriptive message

		return fmt.Sprintf("[AOS Member %s spawned via Cursor Task, execID=%s. No result registry  ?result not collected.]",

			task.AssigneeID, execID), nil
	}
	timeout := m.memberTimeout
	if timeout <= 0 {

		timeout = DefaultAOSMemberTimeout
	}
	result, err := reg.WaitCtx(ctx, execID, timeout)
	duration := time.Since(resolveStart)
	status := "ok"
	errMsg := ""
	if err != nil {

		status = "error"

		errMsg = err.Error()
	} else if result.Error != nil {

		status = "error"

		errMsg = result.Error.Error()
	}
	// Phase 26f: record resolve trace node
	if trace := executionTraceFromContext(ctx); trace != nil {

		trace.AddNode(TraceNode{

			Role: task.AssigneeID,

			Action: "resolve",

			ExecutionMode: m.executionMode,

			Spawned: true,

			TaskID: task.ID,

			ExecID: execID,

			Status: status,

			Duration: duration,

			StartTime: resolveStart,

			Error: errMsg,
		})
	}
	if err != nil {

		return "", fmt.Errorf("resolve member %s execID=%s: %w", task.AssigneeID, execID, err)
	}
	if result.Error != nil {

		return "", fmt.Errorf("member %s result error: %w", task.AssigneeID, result.Error)
	}
	return result.Text, nil
}

// executeLeaderReview reviews all member outputs.
func (m *AOSModel) executeLeaderReview(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace) (*ReviewResult, error) {
	var outputs []string
	for _, t := range ws.Tasks {

		if t.Result != "" {

			outputs = append(outputs, fmt.Sprintf("Task %s (%s):\n%s", t.ID, t.AssigneeID, t.Result))

		}
	}
	if len(outputs) == 0 {

		return &ReviewResult{Status: "accepted"}, nil
	}
	prompt := fmt.Sprintf("Review the following member outputs:\n\n%s\n\nOutput JSON: {\"status\":\"accepted|rejected|needs_revision\",\"feedback\":\"\",\"issues\":[]}", strings.Join(outputs, "\n\n---\n\n"))
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, m.team.Leader.AdapterID, messages, LeaderReviewPrompt)
	if err != nil {

		return &ReviewResult{Status: "accepted"}, nil // fail open
	}
	rr := parseReviewResult(result.Text)
	if rr == nil {

		return &ReviewResult{Status: "accepted"}, nil
	}
	return rr, nil
}

// executeLeaderMerge produces the final output.
// Path #5: sanitize member identity before returning (defense-in-depth; resultWithTrace also sanitizes).
func (m *AOSModel) executeLeaderMerge(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace) (string, error) {
	var outputs []string
	for _, t := range ws.Tasks {

		if t.Result != "" && !strings.HasPrefix(t.Result, "ERROR:") {

			outputs = append(outputs, t.Result)

		}
	}
	if len(outputs) == 0 {

		return SanitizeUserFacingTextWithTeam("No output produced.", m.team), nil
	}
	if len(outputs) == 1 {

		return SanitizeUserFacingTextWithTeam(outputs[0], m.team), nil
	}
	// Merge: ask leader to synthesize
	prompt := fmt.Sprintf("Merge the following outputs into a final result. Do not reference member names.\n\n%s", strings.Join(outputs, "\n\n---\n\n"))
	messages := []vm_moa.Message{{Role: "user", Content: prompt}}
	result, err := m.callAdapter(ctx, m.team.Leader.AdapterID, messages, LeaderMergePrompt)
	if err != nil {

		return SanitizeUserFacingTextWithTeam(strings.Join(outputs, "\n\n"), m.team), nil // fallback: concatenation
	}
	return SanitizeUserFacingTextWithTeam(result.Text, m.team), nil
}

// ReplayNodeCall replays a single trace node by adapterID and prompt.
// ponytail: system prompt is not stored separately in trace; replay uses the
// node's stored prompt as the sole user message. Upgrade path: store
// systemPrompt in TraceNode for faithful replication.
func (m *AOSModel) ReplayNodeCall(ctx context.Context, adapterID, prompt string) (string, error) {
	if strings.TrimSpace(adapterID) == "" || strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("node is not replayable (missing adapter or prompt)")
	}
	result, err := m.callAdapter(ctx, adapterID, []vm_moa.Message{{Role: "user", Content: prompt}}, "")
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("adapter returned nil result for %s", adapterID)
	}
	return result.Text, nil
}

// callAdapter resolves the channel and calls the physical model.
func (m *AOSModel) callAdapter(ctx context.Context, adapterID string, messages []vm_moa.Message, systemPrompt string) (*vm_moa.AdapterResult, error) {
	if m.channelSvc == nil {

		return nil, fmt.Errorf("channel service is nil")
	}
	info, err := m.channelSvc.ResolveChannel(ctx, adapterID)
	if err != nil {

		return nil, fmt.Errorf("resolve channel %s: %w", adapterID, err)
	}
	result, err := m.channelSvc.CallAdapter(ctx, info, messages, systemPrompt)
	if err != nil {

		return nil, err
	}
	// Record cost to Optimization Runtime (ADR-013: reuse existing infra)
	if m.optimize != nil && result != nil {

		m.optimize.RecordCost(adapterID, result.PromptTokens, result.CompletionTokens)
	}
	if trace := executionTraceFromContext(ctx); trace != nil && result != nil {

		trace.AddUsage(result.PromptTokens, result.CompletionTokens)
	}
	return result, nil
}

// resultWithTrace builds ExecuteResult for Cursor. Text is always sanitized
// (prompt-leak strip + team identity redaction). team may be nil.
func resultWithTrace(trace *ExecutionTrace, text string, team *TeamProfile) *virtualmodel.ExecuteResult {
	promptTokens, completionTokens, totalTokens := 0, 0, 0
	if trace != nil {

		promptTokens, completionTokens, totalTokens = trace.Usage()

		// Phase 26f/9 slice: keep last summary for UI / Wails.

		RememberLastTrace(trace)
	}
	return &virtualmodel.ExecuteResult{

		Text: SanitizeUserFacingTextWithTeam(text, team),

		Usage: &virtualmodel.UsageSummary{

			PromptTokens: promptTokens,

			CompletionTokens: completionTokens,

			TotalTokens: totalTokens,
		},

		Metadata: func() map[string]string {

			if trace == nil {

				return nil

			}

			return trace.Metadata()

		}(),
	}
}

// executeLeaderPlanningTraced wraps executeLeaderPlanning with trace recording.
func (m *AOSModel) executeLeaderPlanningTraced(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, trace *ExecutionTrace) (*TaskPlan, int, time.Duration, error) {
	start := time.Now()
	plan, err := m.executeLeaderPlanning(ctx, req, ws)
	dur := time.Since(start)
	status := "ok"
	if err != nil {

		status = "error"
	}
	trace.AddNode(TraceNode{

		Role: "leader",

		Action: "planning",

		AdapterID: m.team.Leader.AdapterID,

		Duration: dur,

		StartTime: start,

		Status: status,
	})
	if err != nil {

		return nil, 0, dur, err
	}
	return plan, 0, dur, nil
}

// executeSprintTraced wraps executeSprint with trace recording for each task.
func (m *AOSModel) executeSprintTraced(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, plan *TaskPlan, trace *ExecutionTrace) error {
	// Record pre-execution state
	for _, t := range ws.Tasks {

		if t.Status != "pending" {

			continue

		}

		// Execute via original method; tracing is coarse-grained per sprint
	}
	start := time.Now()
	err := m.executeSprint(ctx, req, ws, plan)
	dur := time.Since(start)
	status := "ok"
	if err != nil {

		status = "error"
	}
	// Record one node per sprint (aggregated)
	trace.AddNode(TraceNode{

		Role: "sprint",

		Action: "execution",

		Duration: dur,

		StartTime: start,

		Status: status,
	})
	return err
}

// executeLeaderReviewTraced wraps executeLeaderReview with trace recording.
func (m *AOSModel) executeLeaderReviewTraced(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, trace *ExecutionTrace) (*ReviewResult, int, time.Duration, error) {
	start := time.Now()
	rr, err := m.executeLeaderReview(ctx, req, ws)
	dur := time.Since(start)
	status := "ok"
	if err != nil {

		status = "error"
	}
	node := TraceNode{

		Role: "leader",

		Action: "review",

		AdapterID: m.team.Leader.AdapterID,

		Duration: dur,

		StartTime: start,

		Status: status,
	}
	if rr != nil {

		node.Response = rr.Status
	}
	trace.AddNode(node)
	return rr, 0, dur, err
}

// executeLeaderMergeTraced wraps executeLeaderMerge with trace recording.
func (m *AOSModel) executeLeaderMergeTraced(ctx context.Context, req *virtualmodel.ExecuteRequest, ws *Workspace, trace *ExecutionTrace) (string, int, time.Duration, error) {
	start := time.Now()
	text, err := m.executeLeaderMerge(ctx, req, ws)
	dur := time.Since(start)
	status := "ok"
	if err != nil {

		status = "error"
	}
	trace.AddNode(TraceNode{

		Role: "leader",

		Action: "merge",

		AdapterID: m.team.Leader.AdapterID,

		Duration: dur,

		StartTime: start,

		Status: status,
	})
	return text, 0, dur, err
}

// ReviewResult is the Leader's review output.
type ReviewResult struct {
	Status   string   `json:"status"`
	Feedback string   `json:"feedback"`
	Issues   []string `json:"issues"`
}

// parseTaskPlan parses JSON output from the Leader's planning step.
// team is used to validate assignee IDs — unknown assignees default to "leader".
func parseTaskPlan(text string, team *TeamProfile) *TaskPlan {
	text = extractJSON(text)
	if text == "" {

		return nil
	}
	var raw struct {
		Tasks []struct {
			ID string `json:"id"`

			Role string `json:"role"`

			Description string `json:"description"`

			Assignee string `json:"assignee"`

			Dependencies []string `json:"dependencies"`

			Priority string `json:"priority"`
		} `json:"tasks"`

		Architecture string `json:"architecture"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {

		return nil
	}
	plan := &TaskPlan{Architecture: raw.Architecture}
	for _, t := range raw.Tasks {

		assignee := strings.TrimSpace(t.Assignee)

		// 空值或未知 assignee 一律视为 leader 任务（leader 自己 callAdapter，
		// 不走 Task tool spawn）。已知 member ID 才走 spawnMemberTask。
		if assignee == "" {
			assignee = "leader"
		} else if assignee != "leader" && team != nil {
			if _, isMember := team.FindMember(assignee); !isMember {
				// LLM 编造的 assignee（如 "architect"/"pm"/"me"）不匹配任何成员，
				// 视为 leader 自留任务，避免被误派给 spawnMemberTask。
				assignee = "leader"
			}
		}

		plan.Tasks = append(plan.Tasks, Task{

			ID: t.ID, Role: t.Role, Description: t.Description,

			AssigneeID: assignee, Dependencies: t.Dependencies,

			Priority: t.Priority, Status: "pending",
		})
	}
	return plan
}

// parseReviewResult parses JSON output from the Leader's review step.
func parseReviewResult(text string) *ReviewResult {
	text = extractJSON(text)
	if text == "" {

		return nil
	}
	var rr ReviewResult
	if err := json.Unmarshal([]byte(text), &rr); err != nil {

		return nil
	}
	return &rr
}

// extractJSON finds the first valid JSON object in text. Delegates to the
// shared virtualmodel.ExtractJSONObject (which is string-aware and tolerates
// fenced markdown and leading prose) and returns "" when no object is found.
// The shared implementation supersedes the older, brace-ignorant scan that
// broke on '}' characters inside string values (see ADR for details).
func extractJSON(text string) string {
	candidate, err := virtualmodel.ExtractJSONObject(text)
	if err != nil {
		return ""
	}
	return candidate
}

// extractUserText gets the latest user message from the request.
// Delegates to the shared virtualmodel.LastUserMessage to avoid drift across
// the AOS / VM providers.
func extractUserText(messages []virtualmodel.Message) string {
	return virtualmodel.LastUserMessage(messages)
}

// extractTaskTags extracts relevant tags from a task description by matching
// against known member tags in the team profile. Used as a fallback when the
// Leader's assignee ID doesn't match any member.
func extractTaskTags(description string, team *TeamProfile) []string {
	if team == nil {
		return nil
	}
	descLower := strings.ToLower(description)
	var matches []string
	for _, member := range team.Members {
		for _, tag := range member.Tags {
			tagLower := strings.ToLower(tag)
			if strings.Contains(descLower, tagLower) {
				matched := false
				for _, m := range matches {
					if strings.EqualFold(m, tag) {
						matched = true
						break
					}
				}
				if !matched {
					matches = append(matches, tag)
				}
				break
			}
		}
	}
	return matches
}
