// Package workflow 实现可视化工作流编辑器后端运行时。
// 提供工作流模型的 JSON 文件持久化（与 cache runtime 风格一致）、
// CRUD 操作，以及按节点依赖顺序执行的简单执行引擎。
package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cursor/internal/appdata"
)

// NodeType 节点类型。
type NodeType string

const (
	NodeStart     NodeType = "start"
	NodeTask      NodeType = "task"
	NodeCondition NodeType = "condition"
	NodeEnd       NodeType = "end"
)

// Position 节点画布坐标。
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node 工作流节点。
type Node struct {
	ID       string         `json:"id"`
	Type     NodeType       `json:"type"`
	Name     string         `json:"name"`
	Position Position       `json:"position"`
	Config   map[string]any `json:"config"`
}

// Edge 工作流有向边。Condition 可选：空=无条件；"true"/"false"=条件分支。
type Edge struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

// Workflow 工作流定义。
type Workflow struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Nodes       []Node    `json:"nodes"`
	Edges       []Edge    `json:"edges"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Store 工作流持久化存储（JSON 文件）。
type Store struct {
	dir  string
	path string
	mu   sync.RWMutex
}

const fileName = "workflows.json"

// NewStore 创建并加载工作流存储。dir 缺省时使用 appdata 数据根。
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = filepath.Join(appdata.DataRootPath(), "workflow")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create workflow dir: %w", err)
	}
	s := &Store{
		dir:  dir,
		path: filepath.Join(dir, fileName),
	}
	if err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

// workflowFile 磁盘文件结构。
type workflowFile struct {
	Workflows []Workflow `json:"workflows"`
}

func (s *Store) readFile() (*workflowFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &workflowFile{}, nil
		}
		return nil, fmt.Errorf("read workflows: %w", err)
	}
	var wf workflowFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflows: %w", err)
	}
	if wf.Workflows == nil {
		wf.Workflows = []Workflow{}
	}
	return &wf, nil
}

// Load 从磁盘加载（幂等，已加载内容留空则创建空文件）。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.readFile()
	if err != nil {
		return err
	}
	// 确保文件存在。
	return s.writeFile(wf)
}

func (s *Store) writeFile(wf *workflowFile) error {
	if wf == nil {
		wf = &workflowFile{}
	}
	if wf.Workflows == nil {
		wf.Workflows = []Workflow{}
	}
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workflows: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write workflows: %w", err)
	}
	return nil
}

// List 返回所有工作流（浅拷贝，不含大字段影响）。
func (s *Store) List() []Workflow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wf, err := s.readFile()
	if err != nil || wf == nil {
		return []Workflow{}
	}
	out := make([]Workflow, len(wf.Workflows))
	copy(out, wf.Workflows)
	return out
}

// Get 按 ID 获取工作流。
func (s *Store) Get(id string) (Workflow, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	wf, err := s.readFile()
	if err != nil {
		return Workflow{}, false
	}
	for _, w := range wf.Workflows {
		if w.ID == id {
			return w, true
		}
	}
	return Workflow{}, false
}

// Create 新建工作流（ID 由调用方提供，需唯一）。
func (s *Store) Create(w Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.readFile()
	if err != nil {
		return err
	}
	for _, existing := range wf.Workflows {
		if existing.ID == w.ID {
			return fmt.Errorf("workflow %q already exists", w.ID)
		}
	}
	now := time.Now()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	w.UpdatedAt = now
	wf.Workflows = append(wf.Workflows, w)
	return s.writeFile(wf)
}

// Update 更新工作流（按 ID 覆盖）。
func (s *Store) Update(w Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.readFile()
	if err != nil {
		return err
	}
	found := false
	for i, existing := range wf.Workflows {
		if existing.ID == w.ID {
			w.CreatedAt = existing.CreatedAt
			w.UpdatedAt = time.Now()
			wf.Workflows[i] = w
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("workflow %q not found", w.ID)
	}
	return s.writeFile(wf)
}

// Delete 删除工作流。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	wf, err := s.readFile()
	if err != nil {
		return err
	}
	filtered := wf.Workflows[:0]
	removed := false
	for _, w := range wf.Workflows {
		if w.ID == id {
			removed = true
			continue
		}
		filtered = append(filtered, w)
	}
	wf.Workflows = filtered
	if !removed {
		return fmt.Errorf("workflow %q not found", id)
	}
	return s.writeFile(wf)
}

// Save 整体持久化（用于外部批量修改后回写）。
func (s *Store) Save(workflows []Workflow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeFile(&workflowFile{Workflows: workflows})
}