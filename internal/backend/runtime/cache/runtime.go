// Package cache 实现 Cache Runtime：精确缓存 + 语义缓存。
// 减少重复 LLM 调用，降低延迟和成本。
//
// 两级缓存架构：
//   Layer 1: 精确缓存（SHA-256 hash）→ 完全相同的 prompt 直接命中
//   Layer 2: 语义缓存（embedding + cosine similarity）→ 相似度 > 阈值命中
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/backend/runtime/embedding"
)

// DefaultMaxExactEntries caps on-disk exact cache size (0 = unlimited).
// ponytail: true LRU eviction by LastHitAt (fallback CreatedAt); full file
// scan under cap is O(n) but n<=cap so it stays cheap and simple.
const DefaultMaxExactEntries = 2000

// Runtime 是 Cache Runtime 的主入口。
type Runtime struct {
	dir             string
	mu              sync.RWMutex
	stats           *CacheStats
	embedder        embedding.Embedder
	semantic        embedding.Store
	maxExactEntries int // 0 = no cap

	// promptCache 是稳定 system prompt 前缀缓存注册表：key 为 systemPrompt
	// 内容的 SHA-256 哈希，value 为最近一次记录时间。命中时 caller 可对请求
	// 打 prompt_cache 标记（或跳过前缀传递），复用 Provider 级 Prompt Cache。
	// 采用单稳定前缀模型：system prompt 变化时旧前缀被替换/失效。
	promptCache        map[string]time.Time
	promptCacheKey     string // 当前稳定前缀的 key（空=无）
	promptCacheEnabled bool

	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// CacheStats 缓存统计。
type CacheStats struct {
	ExactHits      int64   `json:"exactHits"`
	ExactMisses    int64   `json:"exactMisses"`
	SemanticHits   int64   `json:"semanticHits"`
	SemanticMisses int64   `json:"semanticMisses"`
	TotalHits      int64   `json:"totalHits"`
	TotalMisses    int64   `json:"totalMisses"`
	HitRate        float64 `json:"hitRate"`
	TokensSaved    int64   `json:"tokensSaved"`
	Evicted        int64   `json:"evicted"` // exact entries removed by LRU cap
}

// ponytail: CacheEntry lives at the application level. Provider-level Prompt
// Cache (e.g. Anthropic cache_control, injected by the adapter) is NOT stored
// here. The Cache Runtime tracks a stable system-prompt prefix registry
// (promptCache) so callers can mark requests with prompt_cache when the prefix
// matches, reusing the provider's own cache. Prefix hit is exposed via
// SystemPrefixHit(systemPrompt); the single stable prefix is invalidated when
// the system prompt changes. ponytail: simple map, O(1) lookup, fine for one
// stable prefix per runtime.

// CacheEntry 缓存条目。
type CacheEntry struct {
	Key          string    `json:"key"`
	ModelID      string    `json:"modelID"`
	Mode         string    `json:"mode"`
	PromptHash   string    `json:"promptHash"`
	PromptText   string    `json:"promptText,omitempty"` // 用户问题的文本（用于语义匹配）
	Result       string    `json:"result"`
	PromptTokens int       `json:"promptTokens"`
	OutputTokens int       `json:"outputTokens"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	HitCount     int       `json:"hitCount"`
	LastHitAt    time.Time `json:"lastHitAt,omitempty"`
}

// NewRuntime 创建 Cache Runtime。
func NewRuntime(dir string) (*Runtime, error) {
	if err := os.MkdirAll(filepath.Join(dir, "exact"), 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "semantic"), 0755); err != nil {
		return nil, fmt.Errorf("create semantic dir: %w", err)
	}
	rt := &Runtime{
		dir:             dir,
		stats:           &CacheStats{},
		embedder:        embedding.NewSimpleEmbedder(),
		semantic:        embedding.NewInMemoryStore(),
		maxExactEntries: DefaultMaxExactEntries,
		promptCache:     map[string]time.Time{},
		promptCacheEnabled: true,
	}
	rt.loadStats()
	rt.loadSemanticStore()
	return rt, nil
}

// systemPromptHash returns the stable key for a system prompt prefix.
// Empty system prompts return "" (no prefix to cache).
func systemPromptHash(systemPrompt string) string {
	trimmed := strings.TrimSpace(systemPrompt)
	if trimmed == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmed))
	return hex.EncodeToString(sum[:])
}

// SystemPrefixHit reports whether the given systemPrompt matches a registered
// stable prefix. Callers (e.g. the Anthropic adapter) use this to mark a
// request with prompt_cache / cache_control so the provider reuses its own
// cached prefix. Empty systemPrompt or disabled cache returns false.
func (rt *Runtime) SystemPrefixHit(systemPrompt string) bool {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	if !rt.promptCacheEnabled {
		return false
	}
	key := systemPromptHash(systemPrompt)
	if key == "" {
		return false
	}
	_, ok := rt.promptCache[key]
	return ok
}

// Lookup 查询缓存。
// 返回 (result, hitType, hit)。
// hitType: "exact" / "semantic" / ""
func (rt *Runtime) Lookup(messages []Message, systemPrompt string, modelID string, mode string) (string, string, bool) {
	if rt == nil {
		return "", "", false
	}

	// Step 1: 精确匹配（SHA-256 hash）
	exactKey := rt.exactKey(messages, systemPrompt)
	if entry, ok := rt.lookupExact(exactKey); ok {
		rt.recordHit("exact", entry.OutputTokens)
		return entry.Result, "exact", true
	}

	// Step 2: 语义匹配（embedding + cosine similarity）
	userText := rt.extractUserText(messages)
	if userText != "" {
		if entry, ok := rt.lookupSemantic(userText); ok {
			rt.recordHit("semantic", entry.OutputTokens)
			return entry.Result, "semantic", true
		}
	}

	rt.recordMiss()
	return "", "", false
}

// registerSystemPrefix 记录稳定 system prompt 前缀。采用单稳定前缀模型：
// 仅保留最新 systemPrompt 的哈希，system prompt 变化时旧前缀自动失效/被替换。
func (rt *Runtime) registerSystemPrefix(systemPrompt string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.promptCacheEnabled {
		return
	}
	key := systemPromptHash(systemPrompt)
	if key == "" {
		return
	}
	// 单稳定前缀模型：system prompt 变化（key 改变）时，旧前缀失效。
	if rt.promptCacheKey != "" && rt.promptCacheKey != key {
		delete(rt.promptCache, rt.promptCacheKey)
	}
	rt.promptCacheKey = key
	rt.promptCache[key] = time.Now()
}

// Store 存储缓存条目（同时写入精确缓存和语义缓存）。
// 同时注册 system prompt 稳定前缀到 Prompt Cache 注册表（命中时 caller 可跳过
// 前缀重复传递或打 prompt_cache 标记）。system prompt 变化时旧前缀被替换。
func (rt *Runtime) Store(messages []Message, systemPrompt string, modelID string, mode string, result string, promptTokens int, outputTokens int, ttl time.Duration) error {
	if rt == nil {
		return nil
	}

	rt.registerSystemPrefix(systemPrompt)

	exactKey := rt.exactKey(messages, systemPrompt)
	now := time.Now()
	userText := rt.extractUserText(messages)

	entry := CacheEntry{
		Key:          exactKey,
		ModelID:      modelID,
		Mode:         mode,
		PromptHash:   exactKey,
		PromptText:   userText,
		Result:       result,
		PromptTokens: promptTokens,
		OutputTokens: outputTokens,
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
		HitCount:     0,
	}

	// 写入精确缓存
	if err := rt.storeExact(exactKey, &entry); err != nil {
		return fmt.Errorf("store exact: %w", err)
	}

	// 写入语义缓存（embedding + vector store）
	if userText != "" {
		emb := rt.embedder.Embed(userText)
		if len(emb) > 0 {
			if err := rt.semantic.Add(nil, exactKey, userText, emb); err != nil {
				// 语义缓存写入失败不影响精确缓存
				_ = err
			}
		}
	}

	// Phase 5 slice: cap exact entries (true LRU by LastHitAt, fallback CreatedAt).
	rt.enforceMaxExactEntries()

	return nil
}

// lruTime returns the eviction timestamp for an entry: LastHitAt when it has
// been hit, otherwise the creation time. Used so eviction keeps most-recently-
// used entries, falling back to oldest-created only for never-hit entries.
func lruTime(e CacheEntry) time.Time {
	if !e.LastHitAt.IsZero() {
		return e.LastHitAt
	}
	return e.CreatedAt
}

// enforceMaxExactEntries removes least-recently-used exact entries when over maxExactEntries.
func (rt *Runtime) enforceMaxExactEntries() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	max := rt.maxExactEntries
	rt.mu.Unlock()
	if max <= 0 {
		return
	}

	type datedPath struct {
		path string
		at   time.Time
	}
	var all []datedPath
	exactDir := filepath.Join(rt.dir, "exact")
	prefixes, err := os.ReadDir(exactDir)
	if err != nil {
		return
	}
	for _, p := range prefixes {
		if !p.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(exactDir, p.Name()))
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".json") {
				continue
			}
			path := filepath.Join(exactDir, p.Name(), se.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var entry CacheEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				continue
			}
			all = append(all, datedPath{path: path, at: lruTime(entry)})
		}
	}
	if len(all) <= max {
		return
	}
	// Sort least-recently-used first (insertion sort — n small under cap).
	// Most-recently-used entries are kept; oldest-created only breaks ties.
	for i := 1; i < len(all); i++ {
		j := i
		for j > 0 && all[j].at.Before(all[j-1].at) {
			all[j], all[j-1] = all[j-1], all[j]
			j--
		}
	}
	overflow := len(all) - max
	for i := 0; i < overflow; i++ {
		_ = os.Remove(all[i].path)
	}
	rt.mu.Lock()
	rt.stats.Evicted += int64(overflow)
	rt.mu.Unlock()
}

// Stats 返回缓存统计。
func (rt *Runtime) Stats() *CacheStats {
	if rt == nil {
		return &CacheStats{}
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	stats := *rt.stats
	if stats.TotalHits+stats.TotalMisses > 0 {
		stats.HitRate = float64(stats.TotalHits) / float64(stats.TotalHits+stats.TotalMisses)
	}
	return &stats
}

// Entries 返回语义缓存当前条目数。
func (rt *Runtime) Entries() int {
	if rt == nil {
		return 0
	}
	if rt.semantic == nil {
		return 0
	}
	return rt.semantic.Size()
}

// CountExact 返回精确缓存当前磁盘条目数。
func (rt *Runtime) CountExact() int {
	if rt == nil {
		return 0
	}
	n := 0
	exactDir := filepath.Join(rt.dir, "exact")
	prefixes, err := os.ReadDir(exactDir)
	if err != nil {
		return 0
	}
	for _, p := range prefixes {
		if !p.IsDir() {
			continue
		}
		sub, err := os.ReadDir(filepath.Join(exactDir, p.Name()))
		if err != nil {
			continue
		}
		for _, se := range sub {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".json") {
				continue
			}
			n++
		}
	}
	return n
}

// Clear 清空精确缓存与语义缓存，并重置统计。
func (rt *Runtime) Clear() error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// 清空精确缓存（磁盘文件）。
	exactDir := filepath.Join(rt.dir, "exact")
	if entries, err := os.ReadDir(exactDir); err == nil {
		for _, prefix := range entries {
			if !prefix.IsDir() {
				continue
			}
			if sub, err := os.ReadDir(filepath.Join(exactDir, prefix.Name())); err == nil {
				for _, se := range sub {
					if se.IsDir() || !strings.HasSuffix(se.Name(), ".json") {
						continue
					}
					_ = os.Remove(filepath.Join(exactDir, prefix.Name(), se.Name()))
				}
			}
		}
	}

	// 清空语义缓存（内存向量 store）。
	if rt.semantic != nil {
		rt.semantic.Clear()
	}

	// 重置统计。
	rt.stats = &CacheStats{}
	rt.persistStats()

	// 清空 Prompt Cache 稳定前缀注册表。
	rt.promptCache = map[string]time.Time{}
	rt.promptCacheKey = ""
	return nil
}

// SetEmbedder replaces the embedder (ADR-025).
// Pass an APIEmbedder or FallbackEmbedder for production semantic search.
func (rt *Runtime) SetEmbedder(e embedding.Embedder) {
	if rt == nil || e == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.embedder = e
}

// Message 缓存用的消息类型。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// --- 语义缓存 ---

const semanticSimilarityThreshold = 0.85

// lookupSemantic 通过语义相似度搜索缓存。
func (rt *Runtime) lookupSemantic(userText string) (*CacheEntry, bool) {
	if rt == nil || rt.embedder == nil || rt.semantic == nil {
		return nil, false
	}

	// 生成查询 embedding
	queryEmb := rt.embedder.Embed(userText)
	if len(queryEmb) == 0 {
		return nil, false
	}

	// 搜索 TopK 相似条目
	results, err := rt.semantic.Search(nil, queryEmb, 5)
	if err != nil || len(results) == 0 {
		return nil, false
	}

	// 检查相似度是否超过阈值
	if results[0].Similarity < semanticSimilarityThreshold {
		rt.mu.Lock()
		rt.stats.SemanticMisses++
		rt.mu.Unlock()
		return nil, false
	}

	// 从精确缓存加载匹配的条目
	entry, ok := rt.lookupExact(results[0].ID)
	if !ok {
		return nil, false
	}
	return entry, true
}

// --- 精确缓存 ---

func (rt *Runtime) exactKey(messages []Message, systemPrompt string) string {
	payload := systemPrompt + "\n"
	for _, msg := range messages {
		payload += fmt.Sprintf("[%s]%s\n", msg.Role, msg.Content)
	}
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func (rt *Runtime) lookupExact(key string) (*CacheEntry, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	path := rt.exactEntryPath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	// 检查过期
	if time.Now().After(entry.ExpiresAt) {
		_ = os.Remove(path)
		return nil, false
	}

	// Update LRU timestamp best-effort (write back without holding lock if possible).
	entry.HitCount++
	entry.LastHitAt = time.Now()
	_ = rt.writeEntry(&entry)

	return &entry, true
}

func (rt *Runtime) storeExact(key string, entry *CacheEntry) error {
	entry.HitCount++
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.writeEntry(entry)
}

func (rt *Runtime) writeEntry(entry *CacheEntry) error {
	path := rt.exactEntryPath(entry.Key)
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (rt *Runtime) exactEntryPath(key string) string {
	prefix := key[:2]
	dir := filepath.Join(rt.dir, "exact", prefix)
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, key+".json")
}

// --- 辅助方法 ---

// extractUserText 从消息列表中提取用户问题文本（用于语义匹配）。
//
// NOTE: This is a deliberate local duplicate of
// virtualmodel.LastUserMessage. The two cannot directly share code because
// cache.Runtime uses its own local Message struct (cache.Message), not
// virtualmodel.Message. Keep the body in sync with
// internal/backend/virtualmodel/last_user_message.go.
func (rt *Runtime) extractUserText(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" && strings.TrimSpace(messages[i].Content) != "" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

// --- 统计管理 ---

func (rt *Runtime) recordHit(hitType string, tokensSaved int) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	switch hitType {
	case "exact":
		rt.stats.ExactHits++
	case "semantic":
		rt.stats.SemanticHits++
	}
	rt.stats.TotalHits++
	rt.stats.TokensSaved += int64(tokensSaved)
	rt.persistStats()
}

func (rt *Runtime) recordMiss() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.stats.TotalMisses++
	rt.stats.ExactMisses++
	rt.persistStats()
}

func (rt *Runtime) persistStats() {
	path := filepath.Join(rt.dir, "stats.json")
	data, _ := json.MarshalIndent(rt.stats, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}

func (rt *Runtime) loadStats() {
	path := filepath.Join(rt.dir, "stats.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var stats CacheStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return
	}
	rt.mu.Lock()
	rt.stats = &stats
	rt.mu.Unlock()
}

// loadSemanticStore 从语义缓存目录加载已有条目到内存。
func (rt *Runtime) loadSemanticStore() {
	dir := filepath.Join(rt.dir, "semantic")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry CacheEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}
		if entry.PromptText != "" {
			emb := rt.embedder.Embed(entry.PromptText)
			_ = rt.semantic.Add(nil, entry.Key, entry.PromptText, emb)
		}
	}
}

// CleanExpired 清理过期缓存条目。
func (rt *Runtime) CleanExpired() (int, error) {
	if rt == nil {
		return 0, nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	cleaned := 0
	exactDir := filepath.Join(rt.dir, "exact")
	entries, err := os.ReadDir(exactDir)
	if err != nil {
		return 0, err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			subEntries, _ := os.ReadDir(filepath.Join(exactDir, entry.Name()))
			for _, se := range subEntries {
				if se.IsDir() || !strings.HasSuffix(se.Name(), ".json") {
					continue
				}
				path := filepath.Join(exactDir, entry.Name(), se.Name())
				if rt.isExpired(path, now) {
					_ = os.Remove(path)
					cleaned++
				}
			}
		}
	}
	return cleaned, nil
}

func (rt *Runtime) isExpired(path string, now time.Time) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return true
	}
	return now.After(entry.ExpiresAt)
}

// Summary returns a compact cache efficiency line for evolution/benchmark evidence.
func (s *CacheStats) Summary() string {
	if s == nil {
		return "cache stats: n/a"
	}
	return fmt.Sprintf("cache hitRate=%.4f exact=%d semantic=%d tokensSaved=%d",
		s.HitRate, s.ExactHits, s.SemanticHits, s.TokensSaved)
}

// Close flushes in-memory stats to disk and marks the runtime closed.
// Subsequent Close calls are no-ops. R14: lifecycle unification.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	rt.mu.Unlock()
	rt.persistStats()
	return nil
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.closed
}
