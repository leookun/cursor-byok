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
}

// SearchResult 搜索结果。
type SearchResult struct {
	ID         string  `json:"id"`
	Similarity float64 `json:"similarity"`
	Text       string  `json:"text,omitempty"`
}

// InMemoryStore 内存中的 embedding 存储（Phase 4 默认实现）。
// Phase 5 升级为 SQLite + FAISS/HNSW。
type InMemoryStore struct {
	mu      sync.RWMutex
	entries map[string]*storeEntry
}

type storeEntry struct {
	ID        string
	Text      string
	Embedding []float32
}

// NewInMemoryStore 创建内存存储。
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		entries: make(map[string]*storeEntry),
	}
}

func (s *InMemoryStore) Add(ctx context.Context, id string, text string, embedding []float32) error {
	if s == nil {
		return fmt.Errorf("store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
// 不依赖外部 LLM，使用 TF-IDF 风格的关键词频率向量。
// Phase 5 升级为使用 adapter 的 embedding API。
type SimpleEmbedder struct {
	vocabulary map[string]int
	mu         sync.RWMutex
}

// NewSimpleEmbedder 创建简单嵌入器。
func NewSimpleEmbedder() *SimpleEmbedder {
	return &SimpleEmbedder{
		vocabulary: make(map[string]int),
	}
}

// Embed 将文本转为关键词向量。
func (e *SimpleEmbedder) Embed(text string) []float32 {
	if e == nil {
		return nil
	}

	words := tokenize(text)

	e.mu.RLock()
	vec := make([]float32, len(e.vocabulary)+len(words))
	e.mu.RUnlock()

	// 为新词分配位置
	e.mu.Lock()
	for _, w := range words {
		if _, ok := e.vocabulary[w]; !ok {
			e.vocabulary[w] = len(e.vocabulary)
		}
	}
	vocabSize := len(e.vocabulary)
	vec = make([]float32, vocabSize)
	for _, w := range words {
		if idx, ok := e.vocabulary[w]; ok {
			vec[idx]++
		}
	}
	e.mu.Unlock()

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

// VocabularySize 返回词汇表大小。
func (e *SimpleEmbedder) VocabularySize() int {
	if e == nil {
		return 0
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.vocabulary)
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
