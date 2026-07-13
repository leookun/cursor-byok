// Package cache 实现 Cache Runtime：精确缓存 + 语义缓存。
// 减少重复 LLM 调用，降低延迟和成本。
//
// 两级缓存架构：
//   Layer 1: 精确缓存（SHA-256 hash）→ 完全相同的 prompt 直接命中
//   Layer 2: 语义缓存（embedding + cosine similarity）→ 相似度 > 阈值命中
package cache

import (
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

// Runtime 是 Cache Runtime 的主入口。
type Runtime struct {
	dir       string
	mu        sync.RWMutex
	stats     *CacheStats
	embedder  *embedding.SimpleEmbedder
	semantic  embedding.Store
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
}

// CacheEntry 缓存条目。
type CacheEntry struct {
	Key          string    `json:"key"`
	ModelID      string    `json:"modelID"`
	Mode         string    `json:"mode"`
	PromptHash   string    `json:"promptHash"`
	PromptText   string    `json:"promptText,omitempty"`   // 用户问题的文本（用于语义匹配）
	Result       string    `json:"result"`
	PromptTokens int       `json:"promptTokens"`
	OutputTokens int       `json:"outputTokens"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	HitCount     int       `json:"hitCount"`
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
		dir:      dir,
		stats:    &CacheStats{},
		embedder: embedding.NewSimpleEmbedder(),
		semantic: embedding.NewInMemoryStore(),
	}
	rt.loadStats()
	rt.loadSemanticStore()
	return rt, nil
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

// Store 存储缓存条目（同时写入精确缓存和语义缓存）。
func (rt *Runtime) Store(messages []Message, systemPrompt string, modelID string, mode string, result string, promptTokens int, outputTokens int, ttl time.Duration) error {
	if rt == nil {
		return nil
	}

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

	return nil
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

	entry.HitCount++
	rt.writeEntry(&entry)
	return &entry, true
}

func (rt *Runtime) storeExact(key string, entry *CacheEntry) error {
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
