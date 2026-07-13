// Package memory 实现 Memory Runtime：五层分层记忆系统。
// 基于 MemGPT (arxiv.org/abs/2310.08560) 和 AIOS (arxiv.org/abs/2403.16971) 的研究。
//
// 五层记忆模型：
//
//	Working Memory  → 当前 turn，上下文窗口内（内存）
//	Session Memory  → 当前会话，compaction 摘要（文件）
//	Long Memory     → 跨会话，embedding 检索（SQLite + vector）
//	Project Memory  → 项目级，.cursor/rules/ 风格（文件）
//	User Memory     → 用户级，config.yaml 扩展（配置）
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

// Layer 记忆层级。
type Layer int

const (
	LayerWorking Layer = iota
	LayerSession
	LayerLong
	LayerProject
	LayerUser
)

func (l Layer) String() string {
	switch l {
	case LayerWorking:
		return "working"
	case LayerSession:
		return "session"
	case LayerLong:
		return "long"
	case LayerProject:
		return "project"
	case LayerUser:
		return "user"
	default:
		return "unknown"
	}
}

// Entry 单条记忆条目。
type Entry struct {
	ID        string    `json:"id"`
	Layer     Layer     `json:"layer"`
	Content   string    `json:"content"`
	Summary   string    `json:"summary,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	TTL       time.Duration `json:"-"` // 过期时间，0 表示永不过期
	Relevance float64  `json:"relevance,omitempty"` // 动态计算
	Source    string   `json:"source,omitempty"`    // 来源（conversation_id, project 等）
}

// Runtime Memory Runtime 主入口。
type Runtime struct {
	dir      string
	mu       sync.RWMutex

	// Working Memory: 内存中
	working []Entry

	// Session Memory: 文件存储
	sessionFile string

	// Long Memory: SQLite + embedding
	longStore *longMemoryStore
}

// NewRuntime 创建 Memory Runtime。
func NewRuntime(dir string) (*Runtime, error) {
	memDir := filepath.Join(dir, "memory")
	for _, sub := range []string{"session", "long", "project"} {
		if err := os.MkdirAll(filepath.Join(memDir, sub), 0755); err != nil {
			return nil, fmt.Errorf("create memory dir %s: %w", sub, err)
		}
	}

	longStore, err := newLongMemoryStore(filepath.Join(memDir, "long"))
	if err != nil {
		return nil, fmt.Errorf("init long memory: %w", err)
	}

	return &Runtime{
		dir:         memDir,
		working:     make([]Entry, 0, 32),
		sessionFile: filepath.Join(memDir, "session", "session.json"),
		longStore:   longStore,
	}, nil
}

// Remember 写入一条记忆。
func (rt *Runtime) Remember(ctx context.Context, entry *Entry) error {
	if rt == nil || entry == nil {
		return nil
	}
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	}
	entry.Timestamp = time.Now()

	switch entry.Layer {
	case LayerWorking:
		rt.mu.Lock()
		rt.working = append(rt.working, *entry)
		// 保持 Working Memory 在合理大小（最近 50 条）
		if len(rt.working) > 50 {
			rt.working = rt.working[len(rt.working)-50:]
		}
		rt.mu.Unlock()

	case LayerSession:
		return rt.saveSessionEntry(entry)

	case LayerLong:
		return rt.longStore.Save(ctx, entry)

	case LayerProject:
		return rt.saveProjectEntry(entry)

	case LayerUser:
		return rt.saveUserEntry(entry)
	}
	return nil
}

// Recall 检索记忆。
func (rt *Runtime) Recall(ctx context.Context, layer Layer, query string, limit int) ([]Entry, error) {
	if rt == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	switch layer {
	case LayerWorking:
		rt.mu.RLock()
		defer rt.mu.RUnlock()
		return rt.searchWorking(query, limit), nil

	case LayerSession:
		return rt.loadSessionEntries(query, limit)

	case LayerLong:
		return rt.longStore.Search(ctx, query, limit)

	case LayerProject:
		return rt.loadProjectEntries(query, limit)

	case LayerUser:
		return rt.loadUserEntries(query, limit)
	}
	return nil, nil
}

// RecallAll 从所有层级检索记忆。
func (rt *Runtime) RecallAll(ctx context.Context, query string, perLayer int) (map[Layer][]Entry, error) {
	result := make(map[Layer][]Entry)
	for _, layer := range []Layer{LayerWorking, LayerSession, LayerLong, LayerProject, LayerUser} {
		entries, err := rt.Recall(ctx, layer, query, perLayer)
		if err != nil {
			continue
		}
		result[layer] = entries
	}
	return result, nil
}

// BuildMemoryContext 构建记忆上下文文本（注入到 prompt 中）。
func (rt *Runtime) BuildMemoryContext(ctx context.Context, query string) string {
	if rt == nil {
		return ""
	}

	allMemories, err := rt.RecallAll(ctx, query, 5)
	if err != nil {
		return ""
	}

	var parts []string

	// User Memory 优先（用户偏好）
	if entries, ok := allMemories[LayerUser]; ok && len(entries) > 0 {
		parts = append(parts, "<user_memory>")
		for _, e := range entries {
			parts = append(parts, "- "+e.Content)
		}
		parts = append(parts, "</user_memory>")
	}

	// Project Memory（项目上下文）
	if entries, ok := allMemories[LayerProject]; ok && len(entries) > 0 {
		parts = append(parts, "<project_memory>")
		for _, e := range entries {
			parts = append(parts, "- "+e.Summary)
		}
		parts = append(parts, "</project_memory>")
	}

	// Long Memory（历史关键信息）
	if entries, ok := allMemories[LayerLong]; ok && len(entries) > 0 {
		parts = append(parts, "<long_term_memory>")
		for _, e := range entries {
			parts = append(parts, "- "+e.Summary)
		}
		parts = append(parts, "</long_term_memory>")
	}

	// Session Memory（当前会话摘要）
	if entries, ok := allMemories[LayerSession]; ok && len(entries) > 0 {
		parts = append(parts, "<session_memory>")
		for _, e := range entries {
			parts = append(parts, "- "+e.Content)
		}
		parts = append(parts, "</session_memory>")
	}

	return strings.Join(parts, "\n")
}

// Forget 删除记忆。
func (rt *Runtime) Forget(ctx context.Context, id string) error {
	if rt == nil {
		return nil
	}
	// 简化实现：从 long store 删除
	return rt.longStore.Delete(ctx, id)
}

// CleanExpired 清理过期记忆。
func (rt *Runtime) CleanExpired(ctx context.Context) (int, error) {
	if rt == nil {
		return 0, nil
	}
	return rt.longStore.CleanExpired(ctx)
}

// --- 内部实现 ---

func (rt *Runtime) searchWorking(query string, limit int) []Entry {
	query = strings.ToLower(query)
	var results []Entry
	for _, e := range rt.working {
		if strings.Contains(strings.ToLower(e.Content), query) {
			results = append(results, e)
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

func (rt *Runtime) saveSessionEntry(entry *Entry) error {
	entries, _ := rt.loadSessionEntriesRaw()
	entries = append(entries, *entry)
	return rt.writeSessionEntries(entries)
}

func (rt *Runtime) loadSessionEntriesRaw() ([]Entry, error) {
	data, err := os.ReadFile(rt.sessionFile)
	if err != nil {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil
	}
	return entries, nil
}

func (rt *Runtime) loadSessionEntries(query string, limit int) ([]Entry, error) {
	entries, _ := rt.loadSessionEntriesRaw()
	return filterByQuery(entries, query, limit), nil
}

func (rt *Runtime) writeSessionEntries(entries []Entry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(rt.sessionFile, data, 0644)
}

func (rt *Runtime) saveProjectEntry(entry *Entry) error {
	path := filepath.Join(rt.dir, "project", "project.json")
	entries, _ := rt.loadProjectEntriesRaw(path)
	entries = append(entries, *entry)
	data, _ := json.MarshalIndent(entries, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func (rt *Runtime) loadProjectEntries(query string, limit int) ([]Entry, error) {
	path := filepath.Join(rt.dir, "project", "project.json")
	entries, _ := rt.loadProjectEntriesRaw(path)
	return filterByQuery(entries, query, limit), nil
}

func (rt *Runtime) loadProjectEntriesRaw(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil
	}
	return entries, nil
}

func (rt *Runtime) saveUserEntry(entry *Entry) error {
	path := filepath.Join(rt.dir, "user.json")
	entries, _ := rt.loadUserEntriesRaw(path)
	entries = append(entries, *entry)
	data, _ := json.MarshalIndent(entries, "", "  ")
	return os.WriteFile(path, data, 0644)
}

func (rt *Runtime) loadUserEntries(query string, limit int) ([]Entry, error) {
	path := filepath.Join(rt.dir, "user.json")
	entries, _ := rt.loadUserEntriesRaw(path)
	return filterByQuery(entries, query, limit), nil
}

func (rt *Runtime) loadUserEntriesRaw(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil
	}
	return entries, nil
}

func filterByQuery(entries []Entry, query string, limit int) []Entry {
	if query == "" {
		if len(entries) > limit {
			return entries[len(entries)-limit:]
		}
		return entries
	}
	query = strings.ToLower(query)
	var results []Entry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Content), query) ||
			strings.Contains(strings.ToLower(e.Summary), query) {
			results = append(results, e)
		}
	}
	// 按时间降序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.After(results[j].Timestamp)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
