// Package embedding 提供统一的 embedding 基础设施。
// 供 Memory Runtime 和 Cache Runtime 共享使用。
//
// 设计原则：
//   - 复用用户已配置的 adapter 做 embedding（避免额外成本）
//   - 内置简单 fallback（TF-IDF 风格的关键词向量）
//   - 统一 EmbeddingStore 接口
//   - 余弦相似度计算
package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
)

// Store 统一的 embedding 存储接口。
type Store interface {
	// Add 添加文本及其 embedding。
	Add(ctx context.Context, id string, text string, embedding []float32) error
	// Search 按余弦相似度搜索 TopK。
	Search(ctx context.Context, query []float32, topK int) ([]SearchResult, error)
	// Delete 删除条目。
	Delete(ctx context.Context, id string) error
	// Size 返回存储条目数。
	Size() int
	// Clear 清空所有条目。
	Clear()
}

// Embedder 是统一的文本嵌入接口（ADR-025）。
// SimpleEmbedder 和 APIEmbedder 都实现此接口。
type Embedder interface {
	// Embed 将文本转为向量。
	Embed(text string) []float32
	// EmbedMulti 批量嵌入。
	EmbedMulti(texts []string) [][]float32
}

// FallbackEmbedder 在主 embedder 失败时自动回退到备用 embedder（ADR-025）。
type FallbackEmbedder struct {
	primary  Embedder // APIEmbedder（生产）
	fallback Embedder // SimpleEmbedder（本地 fallback）
}

// NewFallbackEmbedder 创建回退嵌入器。
func NewFallbackEmbedder(primary, fallback Embedder) *FallbackEmbedder {
	return &FallbackEmbedder{primary: primary, fallback: fallback}
}

func (f *FallbackEmbedder) Embed(text string) []float32 {
	if f == nil {
		return nil
	}
	if f.primary != nil {
		if vec := f.primary.Embed(text); len(vec) > 0 {
			return vec
		}
	}
	if f.fallback != nil {
		return f.fallback.Embed(text)
	}
	return nil
}

func (f *FallbackEmbedder) EmbedMulti(texts []string) [][]float32 {
	if f == nil {
		return nil
	}
	if f.primary != nil {
		if vecs := f.primary.EmbedMulti(texts); len(vecs) > 0 {
			return vecs
		}
	}
	if f.fallback != nil {
		return f.fallback.EmbedMulti(texts)
	}
	return nil
}

// Compile-time interface conformance checks.
var _ Embedder = (*SimpleEmbedder)(nil)
var _ Embedder = (*FallbackEmbedder)(nil)

// SearchResult 搜索结果。
type SearchResult struct {
	ID         string  `json:"id"`
	Similarity float64 `json:"similarity"`
	Text       string  `json:"text,omitempty"`
}

// InMemoryStore 内存中的 embedding 存储（Phase 4 默认实现）。
// Phase 5 升级为 SQLite + FAISS/HNSW。
// 内置 FIFO 淘汰上限（默认 1000 条），防止无限增长。
type InMemoryStore struct {
	mu         sync.RWMutex
	entries    map[string]*storeEntry
	order      []string // insertion order for FIFO eviction
	maxEntries int      // 0 = unlimited
}

type storeEntry struct {
	ID        string
	Text      string
	Embedding []float32
}

// DefaultMaxEntries 是 InMemoryStore 的默认最大条目上限。
const DefaultMaxEntries = 1000

// NewInMemoryStore 创建内存存储，默认上限 1000 条。
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries:    make(map[string]*storeEntry),
		maxEntries: DefaultMaxEntries,
	}
}

// NewInMemoryStoreUnlimited 创建无上限的内存存储（测试用）。
func NewInMemoryStoreUnlimited() *InMemoryStore {
	return &InMemoryStore{
		entries:    make(map[string]*storeEntry),
		maxEntries: 0,
	}
}

func (s *InMemoryStore) Add(ctx context.Context, id string, text string, embedding []float32) error {
	if s == nil {
		return fmt.Errorf("store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// FIFO eviction when at capacity
	if s.maxEntries > 0 && len(s.entries) >= s.maxEntries {
		if _, exists := s.entries[id]; !exists {
			oldKey := s.order[0]
			s.order = s.order[1:]
			delete(s.entries, oldKey)
		}
	}

	if _, exists := s.entries[id]; !exists {
		s.order = append(s.order, id)
	}
	s.entries[id] = &storeEntry{
		ID:        id,
		Text:      text,
		Embedding: append([]float32(nil), embedding...),
	}
	return nil
}

func (s *InMemoryStore) Search(ctx context.Context, query []float32, topK int) ([]SearchResult, error) {
	if s == nil {
		return nil, fmt.Errorf("store is nil")
	}
	if topK <= 0 {
		topK = 10
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	type scored struct {
		result SearchResult
		score  float64
	}
	var results []scored

	for id, entry := range s.entries {
		sim := CosineSimilarity(query, entry.Embedding)
		results = append(results, scored{
			result: SearchResult{ID: id, Similarity: sim, Text: entry.Text},
			score:  sim,
		})
	}

	// 按相似度降序
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].score > results[i].score {
				results[i], results[j] = results[j], results[i]
			}
		}
	}

	if len(results) > topK {
		results = results[:topK]
	}

	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = r.result
	}
	return out, nil
}

func (s *InMemoryStore) Delete(ctx context.Context, id string) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	for i, k := range s.order {
		if k == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	return nil
}

func (s *InMemoryStore) Size() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}

func (s *InMemoryStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]*storeEntry)
	s.order = nil
}

// CosineSimilarity 计算两个向量的余弦相似度。
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// SimpleEmbedder 简单的关键词向量生成器（Phase 4 fallback）。
// 不依赖外部 LLM，使用固定维度的哈希分桶词频向量。
// Phase 5 升级为使用 adapter 的 embedding API。
//
// ponytail: 固定维度（simpleEmbedderDim）保证所有向量长度一致，
// 否则 CosineSimilarity 在向量长度不匹配时返回 0，破坏语义搜索。
// 词汇表不再增长到向量维度——每个词通过 sha256 哈希映射到一个桶。
type SimpleEmbedder struct {
	mu sync.RWMutex
}

// simpleEmbedderDim 是 SimpleEmbedder 输出向量的固定维度。
const simpleEmbedderDim = 256

// NewSimpleEmbedder 创建简单嵌入器。
func NewSimpleEmbedder() *SimpleEmbedder {
	return &SimpleEmbedder{}
}

// Embed 将文本转为固定维度的哈希词频向量。
func (e *SimpleEmbedder) Embed(text string) []float32 {
	if e == nil {
		return nil
	}
	words := tokenize(text)
	vec := make([]float32, simpleEmbedderDim)
	for _, w := range words {
		idx := hashWordToBucket(w, simpleEmbedderDim)
		vec[idx]++
	}
	// 归一化
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		norm = float32(math.Sqrt(float64(norm)))
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// EmbedMulti 批量嵌入。
func (e *SimpleEmbedder) EmbedMulti(texts []string) [][]float32 {
	result := make([][]float32, len(texts))
	for i, t := range texts {
		result[i] = e.Embed(t)
	}
	return result
}

func tokenize(text string) []string {
	text = strings.ToLower(text)
	// 简单分词：按空格和标点分割
	words := strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\t' ||
			r == '.' || r == ',' || r == '!' || r == '?' ||
			r == ';' || r == ':' || r == '"' || r == '\'' ||
			r == '(' || r == ')' || r == '[' || r == ']' ||
			r == '{' || r == '}'
	})
	// 过滤短词
	var result []string
	for _, w := range words {
		if len(w) > 2 {
			result = append(result, w)
		}
	}
	return result
}

// hashWordToBucket 将词通过 sha256 哈希映射到 [0, dim) 的桶。
// 固定维度避免向量长度不匹配导致的相似度计算失败。
func hashWordToBucket(word string, dim int) int {
	if dim <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(word))
	// 取前 8 字节作为 uint64，再 mod dim
	v := binary.BigEndian.Uint64(sum[:8])
	return int(v % uint64(dim))
}
