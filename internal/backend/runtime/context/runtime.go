// Package context 实现 Context Runtime：所有 Prompt 的第一站。
// 负责上下文构建、智能压缩、相关性排序、窗口管理和分层记忆注入。
//
// ADR-035: CompressionEngine now shares token estimation and threshold config
// with forwarder/compaction.go via internal/backend/runtime/compress.
package context

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/runtime/compress"
	"cursor/internal/backend/runtime/embedding"
	memruntime "cursor/internal/backend/runtime/memory"
)

// Runtime 是 Context Runtime 的主入口。
// 所有发给模型的内容都先经过它处理，而不是直接发给 PromptCompiler。
type Runtime struct {
	builder     *ContextBuilder
	compressor  *CompressionEngine
	ranker      *ContextRanker
	window      *WindowManager
	memory      *MemoryManager

	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// BuildRequest 上下文构建请求。
type BuildRequest struct {
	// ConversationID 会话 ID。
	ConversationID string
	// Mode Agent 模式。
	Mode agentv1.AgentMode
	// UserText 最新用户消息文本。
	UserText string
	// ModelID 目标模型 ID。
	ModelID string
	// ModelName 目标模型名称。
	ModelName string
	// Conversation 现有会话数据（由 forwarder 提供）。
	Conversation *ConversationData
	// ContextWindowTokens 目标模型的上下文窗口大小。
	ContextWindowTokens int
}

// ConversationData 会话数据（从 forwarder 的 ConversationFile 映射而来）。
type ConversationData struct {
	// Messages 历史消息列表。
	Messages []MessageEntry
	// Tools 可用工具列表。
	Tools []any
	// Rules 用户规则。
	Rules []string
}

// MessageEntry 单条历史消息。
type MessageEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Kind    string `json:"kind"` // user_message / model_message / compaction_summary / tool_result
}

// BuildResult 上下文构建结果。
type BuildResult struct {
	// Messages 最终发送给模型的消息列表。
	Messages []modeladapter.Message
	// Tools 工具定义。
	Tools []any
	// StableMessageCount 稳定消息数量（不会被压缩的部分）。
	StableMessageCount int
	// PromptTokens 预估 prompt token 数。
	PromptTokens int
	// CompressedTokens 被压缩掉的 token 数。
	CompressedTokens int
	// MemoryInjected 是否注入了 Memory。
	MemoryInjected bool
	// WindowTrimmed 是否进行了窗口裁剪。
	WindowTrimmed bool
}

// NewRuntime 创建 Context Runtime。
func NewRuntime(memoryDir string) (*Runtime, error) {
	memRT, err := memruntime.NewRuntime(memoryDir)
	if err != nil {
		return nil, fmt.Errorf("create memory runtime: %w", err)
	}
	return &Runtime{
		builder:    NewContextBuilder(),
		compressor: NewCompressionEngine(),
		ranker:     NewContextRanker(),
		window:     NewWindowManager(),
		memory:     NewMemoryManager(memRT),
	}, nil
}

// BuildContext 构建最终上下文。
// 流程：Builder → Compressor → Ranker → Window → Memory → 返回
// 各步骤始终调用；未超预算时 Compress/Trim 为逻辑 no-op（见 CompressedTokens / WindowTrimmed）。
func (rt *Runtime) BuildContext(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	if rt == nil {
		return nil, fmt.Errorf("context runtime is nil")
	}

	// Step 1: Context Builder — 组装所有上下文素材
	rawContext, err := rt.builder.Build(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("context builder: %w", err)
	}
	tokensBeforeCompress := 0
	if rawContext != nil {
		tokensBeforeCompress = rawContext.EstimatedTokens
	}

	// Step 2: Compression Engine — 智能压缩
	compressed, err := rt.compressor.Compress(ctx, rawContext, req.ContextWindowTokens)
	if err != nil {
		return nil, fmt.Errorf("compression engine: %w", err)
	}
	compressedTokens := 0
	if compressed != nil && tokensBeforeCompress > compressed.EstimatedTokens {
		compressedTokens = tokensBeforeCompress - compressed.EstimatedTokens
	}

	// Step 3: Context Ranker — 相关性排序
	ranked := rt.ranker.Rank(ctx, compressed, req.UserText)

	// Step 4: Window Manager — 按模型窗口裁剪
	tokensBeforeTrim := 0
	msgCountBeforeTrim := 0
	if ranked != nil {
		tokensBeforeTrim = ranked.EstimatedTokens
		msgCountBeforeTrim = len(ranked.Messages)
	}
	trimmed := rt.window.Trim(ctx, ranked, req.ContextWindowTokens, req.ModelID)
	windowTrimmed := false
	if trimmed != nil && (trimmed.EstimatedTokens < tokensBeforeTrim || len(trimmed.Messages) < msgCountBeforeTrim) {
		windowTrimmed = true
	}

	// Step 5: Memory Manager — 注入分层记忆
	result := rt.memory.Inject(ctx, trimmed, req)
	if result != nil {
		result.CompressedTokens = compressedTokens
		result.WindowTrimmed = windowTrimmed
	}
	return result, nil
}

// PostProcessRequest 后处理请求（从已编译的 CompiledConversation 映射而来）。
// 跳过 Builder 步骤（PromptCompiler 已完成），只执行 Compressor → Ranker → Window → Memory。
type PostProcessRequest struct {
	// Messages 已编译的消息列表（来自 PromptCompiler.Compile()）。
	Messages []modeladapter.Message
	// StableMessageCount 稳定消息数量（来自 CompiledConversation）。
	StableMessageCount int
	// Tools 工具定义。
	Tools []any
	// UserText 最新用户消息文本（用于相关性排序和记忆检索）。
	UserText string
	// ModelID 目标模型 ID。
	ModelID string
	// ContextWindowTokens 目标模型的上下文窗口大小（0 表示使用默认值 130K）。
	ContextWindowTokens int
}

// PostProcessResult 后处理结果。
type PostProcessResult struct {
	// Messages 最终发送给模型的消息列表。
	Messages []modeladapter.Message
	// StableMessageCount 稳定消息数量。
	StableMessageCount int
	// Tools 工具定义。
	Tools []any
	// PromptTokens 预估 prompt token 数。
	PromptTokens int
	// CompressedTokens 被压缩掉的 token 数。
	CompressedTokens int
	// MemoryInjected 是否注入了 Memory。
	MemoryInjected bool
	// WindowTrimmed 是否进行了窗口裁剪。
	WindowTrimmed bool
}

// PostProcess 对已编译的上下文进行后处理。
// 跳过 Builder（PromptCompiler 已完成），只执行：
// 1. Compression Engine — 多级压缩
// 2. Context Ranker — 相关性排序
// 3. Window Manager — 按模型窗口裁剪
// 4. Memory Manager — 注入分层记忆
//
// nil 安全：rt 为 nil 时返回原始消息，不执行任何后处理。
func (rt *Runtime) PostProcess(ctx context.Context, req PostProcessRequest) PostProcessResult {
	empty := PostProcessResult{
		Messages:           req.Messages,
		StableMessageCount: req.StableMessageCount,
		Tools:              req.Tools,
		PromptTokens:       estimateTokens(req.Messages),
	}
	if rt == nil {
		return empty
	}

	// 将已编译消息转为 RawContext（跳过 Builder）
	raw := &RawContext{
		Messages:           req.Messages,
		Tools:              req.Tools,
		StableMessageCount: req.StableMessageCount,
		EstimatedTokens:    estimateTokens(req.Messages),
	}

	// Step 1: Compression Engine
	tokensBefore := raw.EstimatedTokens
	compressed, err := rt.compressor.Compress(ctx, raw, req.ContextWindowTokens)
	if err != nil || compressed == nil {
		compressed = raw
	}
	compressedTokens := 0
	if tokensBefore > compressed.EstimatedTokens {
		compressedTokens = tokensBefore - compressed.EstimatedTokens
	}

	// Step 2: Context Ranker
	ranked := rt.ranker.Rank(ctx, compressed, req.UserText)
	if ranked == nil {
		ranked = compressed
	}

	// Step 3: Window Manager
	tokensBeforeTrim := ranked.EstimatedTokens
	msgCountBeforeTrim := len(ranked.Messages)
	trimmed := rt.window.Trim(ctx, ranked, req.ContextWindowTokens, req.ModelID)
	if trimmed == nil {
		trimmed = ranked
	}
	windowTrimmed := trimmed.EstimatedTokens < tokensBeforeTrim || len(trimmed.Messages) < msgCountBeforeTrim

	// Step 4: Memory Manager
	buildReq := BuildRequest{
		UserText:            req.UserText,
		ModelID:             req.ModelID,
		ContextWindowTokens: req.ContextWindowTokens,
	}
	result := rt.memory.Inject(ctx, trimmed, buildReq)
	if result == nil {
		return PostProcessResult{
			Messages:           trimmed.Messages,
			StableMessageCount: trimmed.StableMessageCount,
			Tools:              trimmed.Tools,
			PromptTokens:       trimmed.EstimatedTokens,
			CompressedTokens:   compressedTokens,
			WindowTrimmed:      windowTrimmed,
		}
	}
	result.CompressedTokens = compressedTokens
	result.WindowTrimmed = windowTrimmed
	return PostProcessResult{
		Messages:           result.Messages,
		StableMessageCount: result.StableMessageCount,
		Tools:              result.Tools,
		PromptTokens:       result.PromptTokens,
		CompressedTokens:   result.CompressedTokens,
		MemoryInjected:     result.MemoryInjected,
		WindowTrimmed:      result.WindowTrimmed,
	}
}


// MemoryManager returns the memory manager for external callers (e.g. Forwarder session memory writes).
func (rt *Runtime) MemoryManager() *MemoryManager {
	if rt == nil {
		return nil
	}
	return rt.memory
}

// Close releases underlying Memory Runtime resources (SQLite handles).
// Required on Windows so TempDir cleanup can delete long_memory.db.
func (rt *Runtime) Close() error {
	if rt == nil || rt.memory == nil {
		return nil
	}
	err := rt.memory.Close()
	// Mark closed even on error so the lifecycle guard observes the attempt
	// (a follow-up Close call should still report true).
	rt.closed = true
	return err
}

// CloseCtx is the ctx-aware variant used by Host.Stop. The underlying memory
// store close is not ctx-aware, so ctx is only used for cancellation reporting.
// R14: lifecycle unification contract.
func (rt *Runtime) CloseCtx(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return rt.Close()
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	return rt.closed
}
// ContextBuilder 组装所有上下文素材：历史 + 工具 + 规则 + 固定 prompt。
type ContextBuilder struct{}

func NewContextBuilder() *ContextBuilder {
	return &ContextBuilder{}
}

// RawContext 原始上下文（压缩前）。
type RawContext struct {
	Messages           []modeladapter.Message
	Tools              []any
	SystemPrompt       string
	StableMessageCount int
	EstimatedTokens    int
}

// Build 组装原始上下文（反映 Conversation 历史 + 最新 UserText；nil Conversation 安全）。
func (cb *ContextBuilder) Build(ctx context.Context, req BuildRequest) (*RawContext, error) {
	_ = ctx
	messages := make([]modeladapter.Message, 0)
	systemPrompt := ""
	var tools []any

	if req.Conversation != nil {
		tools = req.Conversation.Tools
		for _, entry := range req.Conversation.Messages {
			role := strings.TrimSpace(entry.Role)
			if role == "" {
				role = "user"
			}
			messages = append(messages, modeladapter.Message{
				Role:    role,
				Content: entry.Content,
			})
		}
		if len(req.Conversation.Rules) > 0 {
			systemPrompt = strings.Join(req.Conversation.Rules, "\n")
			// 规则作为稳定 system 前缀
			messages = append([]modeladapter.Message{{
				Role:    "system",
				Content: systemPrompt,
			}}, messages...)
		}
	}

	// 确保最新用户输入进入消息列表（反映 BuildRequest.UserText）
	userText := strings.TrimSpace(req.UserText)
	if userText != "" {
		needAppend := true
		if n := len(messages); n > 0 {
			last := messages[n-1]
			if last.Role == "user" && strings.TrimSpace(last.Content) == userText {
				needAppend = false
			}
		}
		if needAppend {
			messages = append(messages, modeladapter.Message{
				Role:    "user",
				Content: userText,
			})
		}
	}

	estimatedTokens := estimateTokens(messages)
	stable := 1
	if systemPrompt != "" {
		stable = 2
	}
	if stable > len(messages) {
		stable = len(messages)
	}

	return &RawContext{
		Messages:           messages,
		Tools:              tools,
		SystemPrompt:       systemPrompt,
		StableMessageCount: stable,
		EstimatedTokens:    estimatedTokens,
	}, nil
}

// CompressionEngine 智能压缩引擎。
// ADR-035: Uses shared compress.Config and compress.EstimateTokens so
// memory-level compression and forwarder compaction share the same pipeline.
type CompressionEngine struct {
	config compress.Config
}

func NewCompressionEngine() *CompressionEngine {
	return &CompressionEngine{config: compress.DefaultConfig()}
}

// NewCompressionEngineWithConfig creates CompressionEngine with custom thresholds.
func NewCompressionEngineWithConfig(cfg compress.Config) *CompressionEngine {
	return &CompressionEngine{config: cfg}
}

// Compress 对原始上下文进行多级压缩。
func (ce *CompressionEngine) Compress(ctx context.Context, raw *RawContext, maxTokens int) (*RawContext, error) {
	if raw == nil {
		return nil, fmt.Errorf("raw context is nil")
	}
	if maxTokens <= 0 {
		maxTokens = compress.DefaultContextWindowTokens
	}

	cfg := ce.config
	cfg.MaxTokens = maxTokens
	result := raw

	// Level 1: Lossless — 去除空白/冗余格式
	result = ce.losslessCompress(result)

	// Level 2: 如果仍超限，进行 Lossy 压缩（摘要化旧消息）
	if result.EstimatedTokens > cfg.EffectiveBudget() {
		result = ce.lossyCompress(result, maxTokens)
	}

	// Level 3: 如果仍超限，进行 Semantic 压缩（保留关键语义）
	if result.EstimatedTokens > cfg.EffectiveMaxTokens() {
		result = ce.semanticCompress(result, maxTokens)
	}

	return result, nil
}

func (ce *CompressionEngine) losslessCompress(raw *RawContext) *RawContext {
	result := *raw
	compacted := make([]modeladapter.Message, 0, len(raw.Messages))
	for _, msg := range raw.Messages {
		// 去除连续空行
		content := strings.TrimSpace(msg.Content)
		content = strings.ReplaceAll(content, "\n\n\n", "\n\n")
		if content != "" || msg.Role == "system" {
			compacted = append(compacted, modeladapter.Message{
				Role:    msg.Role,
				Content: content,
			})
		}
	}
	result.Messages = compacted
	result.EstimatedTokens = estimateTokens(compacted)
	return &result
}

func (ce *CompressionEngine) lossyCompress(raw *RawContext, maxTokens int) *RawContext {
	if len(raw.Messages) <= raw.StableMessageCount+2 {
		return raw // 没什么可压缩的
	}

	result := *raw
	// 保留最近的消息，对旧消息做摘要替换
	keepRecent := max(1, len(raw.Messages)/4)
	oldMessages := raw.Messages[:len(raw.Messages)-keepRecent]
	recentMessages := raw.Messages[len(raw.Messages)-keepRecent:]

	// 将旧消息压缩为单条摘要
	summary := buildSummaryMessage(oldMessages)
	newMessages := append([]modeladapter.Message{summary}, recentMessages...)
	result.Messages = newMessages
	result.EstimatedTokens = estimateTokens(newMessages)
	return &result
}

func (ce *CompressionEngine) semanticCompress(raw *RawContext, maxTokens int) *RawContext {
	// Phase 2A 中，semantic compress 与 lossy 相同
	// 后续 Phase 会用 embedding 做更智能的选择性保留
	return ce.lossyCompress(raw, maxTokens)
}

func buildSummaryMessage(messages []modeladapter.Message) modeladapter.Message {
	var parts []string
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content != "" {
			parts = append(parts, "User: "+truncateForSummary(msg.Content, 200))
		} else if msg.Role == "assistant" && msg.Content != "" {
			parts = append(parts, "Assistant: "+truncateForSummary(msg.Content, 200))
		}
	}
	return modeladapter.Message{
		Role:    "system",
		Content: "<conversation_summary>\n" + strings.Join(parts, "\n") + "\n</conversation_summary>",
	}
}

func truncateForSummary(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "..."
}

// ContextRanker 上下文相关性排序器。
type ContextRanker struct{}

func NewContextRanker() *ContextRanker {
	return &ContextRanker{}
}

// Rank 根据与当前用户消息的相关性对上下文重新排序。
// Phase 2A: 简单启发式 — 最近修改的文件相关消息排前面。
func (cr *ContextRanker) Rank(ctx context.Context, raw *RawContext, userText string) *RawContext {
	if raw == nil || len(raw.Messages) <= raw.StableMessageCount+2 {
		return raw
	}

	// 简单启发式：保持时间顺序但提升包含关键术语的消息
	keywords := extractKeywords(userText)
	if len(keywords) == 0 {
		return raw
	}

	// 对非稳定消息按相关性打分
	scored := make([]scoredMessage, 0, len(raw.Messages))
	for i, msg := range raw.Messages {
		score := 0.0
		if i < raw.StableMessageCount {
			score = 1000.0 // 稳定消息永远排前
		} else {
			score = relevanceScore(msg.Content, keywords)
			score += float64(i) * 0.01 // 时间衰减（越新分越高）
		}
		scored = append(scored, scoredMessage{msg: msg, score: score})
	}

	// 按分数排序（稳定消息保持原位）
	sortScoredMessages(scored, raw.StableMessageCount)

	result := *raw
	result.Messages = make([]modeladapter.Message, len(scored))
	for i, sm := range scored {
		result.Messages[i] = sm.msg
	}
	return &result
}

type scoredMessage struct {
	msg   modeladapter.Message
	score float64
}

func extractKeywords(text string) []string {
	// 简单实现：提取长度 > 3 的单词
	words := strings.Fields(strings.ToLower(text))
	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, ".,!?;:\"'()[]{}")
		if len(w) > 3 {
			keywords = append(keywords, w)
		}
	}
	return keywords
}

func relevanceScore(content string, keywords []string) float64 {
	lower := strings.ToLower(content)
	score := 0.0
	for _, kw := range keywords {
		count := strings.Count(lower, kw)
		score += float64(count) * 10.0
	}
	return score
}

func sortScoredMessages(scored []scoredMessage, stableCount int) {
	// 稳定消息部分不动，其余按分数降序
	if stableCount >= len(scored) {
		return
	}
	for i := stableCount; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].score > scored[i].score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}
}

// WindowManager 窗口管理器：按模型动态裁剪上下文。
type WindowManager struct{}

func NewWindowManager() *WindowManager {
	return &WindowManager{}
}

// Trim 按模型上下文窗口裁剪消息列表。
func (wm *WindowManager) Trim(ctx context.Context, raw *RawContext, maxTokens int, modelID string) *RawContext {
	if raw == nil || maxTokens <= 0 {
		return raw
	}

	// 安全边距
	safetyMargin := compress.DefaultSafetyMargin
	effectiveMax := maxTokens - safetyMargin
	if effectiveMax <= 0 {
		effectiveMax = maxTokens
	}

	if raw.EstimatedTokens <= effectiveMax {
		return raw
	}

	// 从旧到新裁剪，但保留稳定消息
	result := *raw
	trimmed := make([]modeladapter.Message, 0)
	tokenCount := 0

	// 先保留稳定消息（最新用户消息 + system prompt）
	for i := 0; i < result.StableMessageCount && i < len(raw.Messages); i++ {
		msg := raw.Messages[i]
		trimmed = append(trimmed, msg)
		tokenCount += compress.EstimateTokens(msg.Content) + 8
	}

	// 从新到旧添加剩余消息
	for i := len(raw.Messages) - 1; i >= result.StableMessageCount; i-- {
		msg := raw.Messages[i]
		msgTokens := compress.EstimateTokens(msg.Content) + 8
		if tokenCount+msgTokens > effectiveMax {
			continue
		}
		// 插入到稳定消息之后
		trimmed = append(trimmed[:result.StableMessageCount],
			append([]modeladapter.Message{msg}, trimmed[result.StableMessageCount:]...)...)
		tokenCount += msgTokens
	}

	result.Messages = trimmed
	result.EstimatedTokens = tokenCount
	return &result
}

// MemoryManager 分层记忆管理器。
type MemoryManager struct {
	runtime *memruntime.Runtime
}

func NewMemoryManager(memRT *memruntime.Runtime) *MemoryManager {
	return &MemoryManager{runtime: memRT}
}

// Close releases the underlying memory runtime (SQLite).
func (mm *MemoryManager) Close() error {
	if mm == nil || mm.runtime == nil {
		return nil
	}
	return mm.runtime.Close()
}

// Inject 将分层记忆注入到上下文中。
func (mm *MemoryManager) Inject(ctx context.Context, raw *RawContext, req BuildRequest) *BuildResult {
	result := &BuildResult{
		Messages:           raw.Messages,
		Tools:              raw.Tools,
		StableMessageCount: raw.StableMessageCount,
		PromptTokens:       raw.EstimatedTokens,
		MemoryInjected:     false,
	}

	if mm.runtime == nil {
		return result
	}

	// 构建记忆上下文
	memoryContext := mm.runtime.BuildMemoryContext(ctx, req.UserText)
	if memoryContext == "" {
		return result
	}

	// 将记忆作为 system message 插入到消息列表最前面
	memoryMsg := modeladapter.Message{
		Role:    "system",
		Content: memoryContext,
	}

	newMessages := make([]modeladapter.Message, 0, len(raw.Messages)+1)
	newMessages = append(newMessages, memoryMsg)
	newMessages = append(newMessages, raw.Messages...)

	result.Messages = newMessages
	result.StableMessageCount = raw.StableMessageCount + 1
	result.PromptTokens = raw.EstimatedTokens + compress.EstimateTokens(memoryContext)
	result.MemoryInjected = true

	return result
}

// Remember 写入一条记忆（供外部调用）。
func (mm *MemoryManager) Remember(ctx context.Context, entry *memruntime.Entry) error {
	if mm == nil || mm.runtime == nil {
		return nil
	}
	return mm.runtime.Remember(ctx, entry)
}

// RecallAll 检索所有层级的记忆（供外部调用）。
func (mm *MemoryManager) RecallAll(ctx context.Context, query string, perLayer int) (map[memruntime.Layer][]memruntime.Entry, error) {
	if mm == nil || mm.runtime == nil {
		return nil, nil
	}
	return mm.runtime.RecallAll(ctx, query, perLayer)
}

func estimateTokens(messages []modeladapter.Message) int {
	total := 0
	for _, msg := range messages {
		total += compress.EstimateTokens(msg.Content)
		if msg.Role == "system" {
			total += 100 // system prompt overhead
		}
		// structural overhead per message (role + formatting)
		total += 8
	}
	return total
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SetEmbedder replaces the embedder used by the underlying Memory Runtime (ADR-025).
func (rt *Runtime) SetEmbedder(e embedding.Embedder) {
	if rt == nil || rt.memory == nil {
		return
	}
	rt.memory.SetEmbedder(e)
}

// SetEmbedder delegates to the underlying memruntime.Runtime (ADR-025).
func (mm *MemoryManager) SetEmbedder(e embedding.Embedder) {
	if mm == nil || mm.runtime == nil {
		return
	}
	mm.runtime.SetEmbedder(e)
}

// 确保 time 包被使用
var _ = time.Now
