// Package tool 实现 Tool Runtime：统一工具注册、发现和执行。
// 统一管理 Filesystem / MCP / Browser / Shell / Git / Search 等工具。
//
// 与现有 ExecBridge / InteractionBridge 的关系：
//
//	ToolRuntime 是上层统一入口，按 Category 分派到 ExecBridge 或 InteractionBridge。
//	ExecBridge 处理 CategoryFilesystem / CategoryShell / CategoryGit
//	InteractionBridge 处理 CategoryBrowser / CategorySearch
//
// 架构：
//
//	ToolRuntime.Execute(toolName, args)
//	    ├── CategoryFilesystem → ExecBridge.OpenExec("Read" / "Write" / ...)
//	    ├── CategoryShell      → ExecBridge.OpenExec("Shell" / ...)
//	    ├── CategoryBrowser    → InteractionBridge.OpenQuery("WebFetch" / ...)
//	    ├── CategorySearch     → InteractionBridge.OpenQuery("WebSearch" / ...)
//	    └── CategoryMCP        → ExecBridge.OpenExec("CallMcpTool" / ...)
package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Runtime 是 Tool Runtime 的主入口。
type Runtime struct {
	mu      sync.RWMutex
	entries map[string]*ToolEntry

	// 外部桥接（由 Forwarder 注入）
	execBridge        ExecBridge
	interactionBridge InteractionBridge
}

// ToolEntry 工具条目。
type ToolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Category    ToolCategory    `json:"category"`
	Enabled     bool            `json:"enabled"`
	Cacheable   bool            `json:"cacheable"`
	CacheTTL    time.Duration   `json:"cacheTTL"`
	// InternalName 对应的 Cursor 内部工具名（如 "Read"、"WebSearch"）
	InternalName string `json:"internalName,omitempty"`
}

// ToolCategory 工具类别。
type ToolCategory string

const (
	CategoryFilesystem ToolCategory = "filesystem"
	CategoryMCP        ToolCategory = "mcp"
	CategoryBrowser    ToolCategory = "browser"
	CategoryShell      ToolCategory = "shell"
	CategoryGit        ToolCategory = "git"
	CategorySearch     ToolCategory = "search"
)

// ToolResult 工具执行结果。
type ToolResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
	Error   string `json:"error,omitempty"`
	Tokens  int    `json:"tokens"`
}

// ExecBridge 执行型工具桥接接口（由 forwarder 的 execbridge 实现）。
type ExecBridge interface {
	OpenExec(toolName string, argsJSON []byte) (execID string, serverPayload []byte, err error)
}

// InteractionBridge 交互型工具桥接接口（由 forwarder 的 interactionbridge 实现）。
type InteractionBridge interface {
	OpenQuery(toolName string, argsJSON []byte) (queryID string, serverPayload []byte, err error)
}

// NewRuntime 创建 Tool Runtime。
func NewRuntime() *Runtime {
	return &Runtime{
		entries: make(map[string]*ToolEntry),
	}
}

// SetBridges 设置外部桥接。
func (rt *Runtime) SetBridges(exec ExecBridge, interaction InteractionBridge) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.execBridge = exec
	rt.interactionBridge = interaction
}

// Register 注册工具。
func (rt *Runtime) Register(entry *ToolEntry) error {
	if rt == nil {
		return fmt.Errorf("tool runtime is nil")
	}
	if entry == nil || strings.TrimSpace(entry.Name) == "" {
		return fmt.Errorf("tool entry name is required")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.entries[strings.TrimSpace(entry.Name)] = entry
	return nil
}

// Get 获取工具。
func (rt *Runtime) Get(name string) (*ToolEntry, bool) {
	if rt == nil {
		return nil, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	entry, ok := rt.entries[strings.TrimSpace(name)]
	return entry, ok
}

// GetByInternalName 按 Cursor 内部工具名查找。
func (rt *Runtime) GetByInternalName(internalName string) (*ToolEntry, bool) {
	if rt == nil {
		return nil, false
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, entry := range rt.entries {
		if strings.EqualFold(strings.TrimSpace(entry.InternalName), strings.TrimSpace(internalName)) {
			return entry, true
		}
	}
	return nil, false
}

// List 列出所有已注册工具。
func (rt *Runtime) List() []*ToolEntry {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	result := make([]*ToolEntry, 0, len(rt.entries))
	for _, entry := range rt.entries {
		result = append(result, entry)
	}
	return result
}

// ListByCategory 按类别列出工具。
func (rt *Runtime) ListByCategory(category ToolCategory) []*ToolEntry {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var result []*ToolEntry
	for _, entry := range rt.entries {
		if entry.Category == category {
			result = append(result, entry)
		}
	}
	return result
}

// ListEnabled 列出所有启用的工具。
func (rt *Runtime) ListEnabled() []*ToolEntry {
	if rt == nil {
		return nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	var result []*ToolEntry
	for _, entry := range rt.entries {
		if entry.Enabled {
			result = append(result, entry)
		}
	}
	return result
}

// Enable 启用/禁用工具。
func (rt *Runtime) Enable(name string, enabled bool) error {
	if rt == nil {
		return fmt.Errorf("tool runtime is nil")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	entry, ok := rt.entries[strings.TrimSpace(name)]
	if !ok {
		return fmt.Errorf("tool %q not found", name)
	}
	entry.Enabled = enabled
	return nil
}

// ToJSONSchemas 将所有启用的工具导出为 JSON Schema 列表。
func (rt *Runtime) ToJSONSchemas(ctx context.Context, mode string) ([]json.RawMessage, error) {
	if rt == nil {
		return nil, nil
	}
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	var schemas []json.RawMessage
	for _, entry := range rt.entries {
		if !entry.Enabled {
			continue
		}
		schemas = append(schemas, entry.Schema)
	}
	return schemas, nil
}

// IsExecTool 判断工具是否为执行型（走 ExecBridge）。
func (rt *Runtime) IsExecTool(name string) bool {
	if rt == nil {
		return false
	}
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		return false
	}
	switch entry.Category {
	case CategoryFilesystem, CategoryShell, CategoryGit, CategoryMCP:
		return true
	default:
		return false
	}
}

// IsInteractionTool 判断工具是否为交互型（走 InteractionBridge）。
func (rt *Runtime) IsInteractionTool(name string) bool {
	if rt == nil {
		return false
	}
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		return false
	}
	switch entry.Category {
	case CategoryBrowser, CategorySearch:
		return true
	default:
		return false
	}
}

// GetCategory 获取工具的分类。
func (rt *Runtime) GetCategory(internalName string) ToolCategory {
	if rt == nil {
		return ""
	}
	entry, ok := rt.GetByInternalName(internalName)
	if !ok {
		return ""
	}
	return entry.Category
}

// IsCacheable 判断工具结果是否可缓存。
func (rt *Runtime) IsCacheable(internalName string) (bool, time.Duration) {
	if rt == nil {
		return false, 0
	}
	entry, ok := rt.GetByInternalName(internalName)
	if !ok {
		return false, 0
	}
	return entry.Cacheable, entry.CacheTTL
}

// SyncFromCatalog 从现有 tool catalog 同步工具注册信息。
// 参数 tools 是已加载的 JSON Schema 列表。
func (rt *Runtime) SyncFromCatalog(toolSchemas []json.RawMessage, toolNames []string) {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()

	for i, name := range toolNames {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		// 如果已注册，跳过
		if _, exists := rt.entries[strings.ToLower(trimmed)]; exists {
			continue
		}
		entry := &ToolEntry{
			Name:         strings.ToLower(trimmed),
			Description:  trimmed + " tool",
			Category:     classifyCursorTool(trimmed),
			Enabled:      true,
			Cacheable:    isToolCacheable(trimmed),
			CacheTTL:     toolCacheTTL(trimmed),
			InternalName: trimmed,
		}
		if i < len(toolSchemas) {
			entry.Schema = toolSchemas[i]
		}
		rt.entries[strings.ToLower(trimmed)] = entry
	}
}

// classifyCursorTool 将 Cursor 内部工具名映射到 Category。
func classifyCursorTool(name string) ToolCategory {
	switch strings.TrimSpace(name) {
	case "Read", "Write", "Delete", "Glob", "Grep", "Ls", "ReadLints", "PatchEdit":
		return CategoryFilesystem
	case "Shell", "WriteShellStdin", "ForceBackgroundShell":
		return CategoryShell
	case "CallMcpTool", "FetchMcpResource", "ListMcpResources":
		return CategoryMCP
	case "WebSearch":
		return CategorySearch
	case "WebFetch":
		return CategoryBrowser
	case "Task", "AskQuestion", "CreatePlan", "SwitchMode", "TodoWrite":
		return CategoryFilesystem // 这些是 Forwarder 内部处理，不走桥接
	default:
		return CategoryFilesystem
	}
}

func isToolCacheable(name string) bool {
	switch strings.TrimSpace(name) {
	case "Read", "Glob", "Ls", "ReadLints", "WebSearch", "WebFetch", "FetchMcpResource":
		return true
	default:
		return false
	}
}

func toolCacheTTL(name string) time.Duration {
	switch strings.TrimSpace(name) {
	case "Read", "Glob", "Ls":
		return 5 * time.Minute
	case "ReadLints":
		return 1 * time.Minute
	case "WebSearch":
		return 10 * time.Minute
	case "WebFetch":
		return 5 * time.Minute
	case "FetchMcpResource":
		return 2 * time.Minute
	default:
		return 0
	}
}

// Execute 按 Category 分派到 ExecBridge / InteractionBridge（Phase 6 统一入口）。
// 注意：当前 Forwarder 主路径仍直接调用桥接器以保持协议状态机完整；
// Execute 供未来统一分发与单测使用，不替代 handleToolInvocation 的 pending 生命周期。
func (rt *Runtime) Execute(ctx context.Context, toolName string, argsJSON []byte) (*ToolResult, error) {
	_ = ctx
	if rt == nil {
		return nil, fmt.Errorf("tool runtime is nil")
	}
	name := strings.TrimSpace(toolName)
	entry, ok := rt.GetByInternalName(name)
	if !ok {
		entry, ok = rt.Get(name)
	}
	if !ok {
		return &ToolResult{Success: false, Error: fmt.Sprintf("tool %q not registered", name)}, fmt.Errorf("tool %q not registered", name)
	}
	if !entry.Enabled {
		return &ToolResult{Success: false, Error: fmt.Sprintf("tool %q is disabled", entry.Name)}, fmt.Errorf("tool %q is disabled", entry.Name)
	}
	internal := strings.TrimSpace(entry.InternalName)
	if internal == "" {
		internal = name
	}

	rt.mu.RLock()
	exec := rt.execBridge
	interaction := rt.interactionBridge
	rt.mu.RUnlock()

	switch entry.Category {
	case CategoryFilesystem, CategoryShell, CategoryGit, CategoryMCP:
		if exec == nil {
			return &ToolResult{Success: false, Error: "exec bridge not configured"}, fmt.Errorf("exec bridge not configured")
		}
		execID, payload, err := exec.OpenExec(internal, argsJSON)
		if err != nil {
			return &ToolResult{Success: false, Error: err.Error()}, err
		}
		return &ToolResult{Success: true, Output: string(payload) + "\nexecID=" + execID}, nil
	case CategoryBrowser, CategorySearch:
		if interaction == nil {
			return &ToolResult{Success: false, Error: "interaction bridge not configured"}, fmt.Errorf("interaction bridge not configured")
		}
		queryID, payload, err := interaction.OpenQuery(internal, argsJSON)
		if err != nil {
			return &ToolResult{Success: false, Error: err.Error()}, err
		}
		return &ToolResult{Success: true, Output: string(payload) + "\nqueryID=" + queryID}, nil
	default:
		return &ToolResult{Success: false, Error: fmt.Sprintf("unsupported category %q", entry.Category)}, fmt.Errorf("unsupported category %q", entry.Category)
	}
}

// RegisterBuiltinTools 注册内置工具集。
func (rt *Runtime) RegisterBuiltinTools() {
	builtins := []*ToolEntry{
		{Name: "read_file", InternalName: "Read", Description: "Read contents of a file", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"read_file","description":"Read contents of a file"}`)},
		{Name: "write_file", InternalName: "Write", Description: "Write contents to a file", Category: CategoryFilesystem, Enabled: true, Cacheable: false, Schema: json.RawMessage(`{"name":"write_file","description":"Write contents to a file"}`)},
		{Name: "search_files", InternalName: "Glob", Description: "Search for files by pattern", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"search_files","description":"Search for files by pattern"}`)},
		{Name: "search_content", InternalName: "Grep", Description: "Search file contents", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 1 * time.Minute, Schema: json.RawMessage(`{"name":"search_content","description":"Search file contents"}`)},
		{Name: "list_directory", InternalName: "Ls", Description: "List directory contents", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"list_directory","description":"List directory contents"}`)},
		{Name: "read_lints", InternalName: "ReadLints", Description: "Read linter errors", Category: CategoryFilesystem, Enabled: true, Cacheable: true, CacheTTL: 1 * time.Minute, Schema: json.RawMessage(`{"name":"read_lints","description":"Read linter errors"}`)},
		{Name: "execute_shell", InternalName: "Shell", Description: "Execute a shell command", Category: CategoryShell, Enabled: true, Cacheable: false, Schema: json.RawMessage(`{"name":"execute_shell","description":"Execute a shell command"}`)},
		{Name: "web_search", InternalName: "WebSearch", Description: "Search the web", Category: CategorySearch, Enabled: true, Cacheable: true, CacheTTL: 10 * time.Minute, Schema: json.RawMessage(`{"name":"web_search","description":"Search the web"}`)},
		{Name: "web_fetch", InternalName: "WebFetch", Description: "Fetch content from a URL", Category: CategoryBrowser, Enabled: true, Cacheable: true, CacheTTL: 5 * time.Minute, Schema: json.RawMessage(`{"name":"web_fetch","description":"Fetch content from a URL"}`)},
	}
	for _, entry := range builtins {
		_ = rt.Register(entry)
	}
}
