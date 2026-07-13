// store.go Long Memory 存储实现：基于文件的 JSON 存储 + 简单的关键词索引。
// 完整 embedding + SQLite 实现在 Phase 5 的 embedding 基础设施中。
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// longMemoryStore Long Memory 存储。
// Phase 4 使用 JSON 文件存储。Phase 5 升级为 SQLite + embedding。
type longMemoryStore struct {
	dir string
	mu  sync.RWMutex
}

func newLongMemoryStore(dir string) (*longMemoryStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return &longMemoryStore{dir: dir}, nil
}

// Save 保存 Long Memory 条目。
func (store *longMemoryStore) Save(ctx context.Context, entry *Entry) error {
	if store == nil || entry == nil {
		return nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	path := filepath.Join(store.dir, entry.ID+".json")
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Search 搜索 Long Memory。
// Phase 4: 关键词匹配。Phase 5: embedding 相似度搜索。
func (store *longMemoryStore) Search(ctx context.Context, query string, limit int) ([]Entry, error) {
	if store == nil {
		return nil, nil
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return nil, nil
	}

	query = strings.ToLower(strings.TrimSpace(query))
	var results []Entry
	now := time.Now()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(store.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			continue
		}

		// 检查 TTL
		if entry.TTL > 0 && now.After(entry.Timestamp.Add(entry.TTL)) {
			_ = os.Remove(path)
			continue
		}

		// 关键词匹配
		if query == "" {
			results = append(results, entry)
			continue
		}

		keywords := strings.Fields(query)
		score := 0
		content := strings.ToLower(entry.Content)
		summary := strings.ToLower(entry.Summary)
		for _, kw := range keywords {
			score += strings.Count(content, kw) * 10
			score += strings.Count(summary, kw) * 20
		}
		// 标签匹配加分
		for _, tag := range entry.Tags {
			if strings.Contains(query, strings.ToLower(tag)) {
				score += 50
			}
		}
		if score > 0 {
			entry.Relevance = float64(score)
			results = append(results, entry)
		}
	}

	// 按相关性排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Relevance > results[j].Relevance
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Delete 删除条目。
func (store *longMemoryStore) Delete(ctx context.Context, id string) error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path := filepath.Join(store.dir, id+".json")
	return os.Remove(path)
}

// CleanExpired 清理过期条目。
func (store *longMemoryStore) CleanExpired(ctx context.Context) (int, error) {
	if store == nil {
		return 0, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return 0, err
	}

	cleaned := 0
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(store.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			_ = os.Remove(path)
			cleaned++
			continue
		}
		var entry Entry
		if err := json.Unmarshal(data, &entry); err != nil {
			_ = os.Remove(path)
			cleaned++
			continue
		}
		if entry.TTL > 0 && now.After(entry.Timestamp.Add(entry.TTL)) {
			_ = os.Remove(path)
			cleaned++
		}
	}
	return cleaned, nil
}

// Stats 返回存储统计。
func (store *longMemoryStore) Stats() (map[string]int, error) {
	if store == nil {
		return nil, fmt.Errorf("store is nil")
	}
	store.mu.RLock()
	defer store.mu.RUnlock()

	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return nil, err
	}

	stats := map[string]int{"total": 0, "with_embedding": 0}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		stats["total"]++
		data, err := os.ReadFile(filepath.Join(store.dir, e.Name()))
		if err != nil {
			continue
		}
		var entry Entry
		if json.Unmarshal(data, &entry) == nil && len(entry.Embedding) > 0 {
			stats["with_embedding"]++
		}
	}
	return stats, nil
}
