// tool_test.go Tool Runtime 单元测试。

package tool

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNewRuntime(t *testing.T) {
	rt := NewRuntime()
	if rt == nil {
		t.Fatal("runtime is nil")
	}
	if len(rt.List()) != 0 {
		t.Errorf("expected empty tool list, got %d", len(rt.List()))
	}
}

func TestRegisterAndGet(t *testing.T) {
	rt := NewRuntime()

	entry := &ToolEntry{
		Name:        "test_tool",
		Description: "A test tool",
		Category:    CategoryFilesystem,
		Enabled:     true,
		Schema:      json.RawMessage(`{"name":"test_tool"}`),
	}
	if err := rt.Register(entry); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	got, ok := rt.Get("test_tool")
	if !ok {
		t.Fatal("Get failed")
	}
	if got.Name != "test_tool" {
		t.Errorf("expected name=test_tool, got %q", got.Name)
	}
	if got.Category != CategoryFilesystem {
		t.Errorf("expected CategoryFilesystem, got %q", got.Category)
	}
}

func TestRegisterNil(t *testing.T) {
	rt := NewRuntime()
	if err := rt.Register(nil); err == nil {
		t.Error("expected error for nil entry")
	}
	if err := rt.Register(&ToolEntry{Name: ""}); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestGetByInternalName(t *testing.T) {
	rt := NewRuntime()

	rt.Register(&ToolEntry{
		Name:         "read_file",
		InternalName: "Read",
		Category:     CategoryFilesystem,
		Enabled:      true,
		Schema:       json.RawMessage(`{}`),
	})
	rt.Register(&ToolEntry{
		Name:         "web_search",
		InternalName: "WebSearch",
		Category:     CategorySearch,
		Enabled:      true,
		Schema:       json.RawMessage(`{}`),
	})

	entry, ok := rt.GetByInternalName("Read")
	if !ok {
		t.Fatal("GetByInternalName(Read) failed")
	}
	if entry.Category != CategoryFilesystem {
		t.Errorf("expected CategoryFilesystem, got %q", entry.Category)
	}

	entry, ok = rt.GetByInternalName("WebSearch")
	if !ok {
		t.Fatal("GetByInternalName(WebSearch) failed")
	}
	if entry.Category != CategorySearch {
		t.Errorf("expected CategorySearch, got %q", entry.Category)
	}

	_, ok = rt.GetByInternalName("NonExistent")
	if ok {
		t.Error("expected false for non-existent tool")
	}
}

func TestIsExecTool(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	execTools := []string{"Read", "Write", "Shell", "Glob", "Grep", "Ls"}
	for _, name := range execTools {
		if !rt.IsExecTool(name) {
			t.Errorf("expected IsExecTool(%q) = true", name)
		}
	}

	interactionTools := []string{"WebSearch", "WebFetch"}
	for _, name := range interactionTools {
		if rt.IsExecTool(name) {
			t.Errorf("expected IsExecTool(%q) = false (it's interaction)", name)
		}
	}
}

func TestIsInteractionTool(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	if !rt.IsInteractionTool("WebSearch") {
		t.Error("expected IsInteractionTool(WebSearch) = true")
	}
	if !rt.IsInteractionTool("WebFetch") {
		t.Error("expected IsInteractionTool(WebFetch) = true")
	}
	if rt.IsInteractionTool("Read") {
		t.Error("expected IsInteractionTool(Read) = false")
	}
}

func TestIsCacheable(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	tests := []struct {
		name     string
		expected bool
	}{
		{"Read", true},
		{"Write", false},
		{"Shell", false},
		{"WebSearch", true},
		{"WebFetch", true},
		{"Ls", true},
	}

	for _, tt := range tests {
		cacheable, ttl := rt.IsCacheable(tt.name)
		if cacheable != tt.expected {
			t.Errorf("IsCacheable(%q) = %v, want %v", tt.name, cacheable, tt.expected)
		}
		if tt.expected && ttl == 0 {
			t.Errorf("IsCacheable(%q) should have non-zero TTL", tt.name)
		}
	}
}

func TestListByCategory(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	filesystemTools := rt.ListByCategory(CategoryFilesystem)
	if len(filesystemTools) == 0 {
		t.Error("expected non-empty filesystem tools")
	}

	searchTools := rt.ListByCategory(CategorySearch)
	if len(searchTools) == 0 {
		t.Error("expected non-empty search tools")
	}

	for _, tool := range filesystemTools {
		if tool.Category != CategoryFilesystem {
			t.Errorf("expected CategoryFilesystem, got %q for %s", tool.Category, tool.Name)
		}
	}
}

func TestEnableDisable(t *testing.T) {
	rt := NewRuntime()
	rt.Register(&ToolEntry{
		Name:     "toggle_tool",
		Category: CategoryFilesystem,
		Enabled:  true,
		Schema:   json.RawMessage(`{}`),
	})

	if err := rt.Enable("toggle_tool", false); err != nil {
		t.Fatalf("Enable(false) failed: %v", err)
	}
	entry, _ := rt.Get("toggle_tool")
	if entry.Enabled {
		t.Error("expected disabled")
	}

	if err := rt.Enable("toggle_tool", true); err != nil {
		t.Fatalf("Enable(true) failed: %v", err)
	}
	entry, _ = rt.Get("toggle_tool")
	if !entry.Enabled {
		t.Error("expected enabled")
	}

	// 不存在的工具
	if err := rt.Enable("nonexistent", false); err == nil {
		t.Error("expected error for non-existent tool")
	}
}

func TestListEnabled(t *testing.T) {
	rt := NewRuntime()
	rt.Register(&ToolEntry{Name: "enabled_tool", Category: CategoryFilesystem, Enabled: true, Schema: json.RawMessage(`{}`)})
	rt.Register(&ToolEntry{Name: "disabled_tool", Category: CategoryFilesystem, Enabled: false, Schema: json.RawMessage(`{}`)})

	enabled := rt.ListEnabled()
	if len(enabled) != 1 {
		t.Errorf("expected 1 enabled tool, got %d", len(enabled))
	}
	if enabled[0].Name != "enabled_tool" {
		t.Errorf("expected enabled_tool, got %s", enabled[0].Name)
	}
}

func TestSyncFromCatalog(t *testing.T) {
	rt := NewRuntime()

	schemas := []json.RawMessage{
		json.RawMessage(`{"name":"Read","description":"Read file contents"}`),
		json.RawMessage(`{"name":"WebSearch","description":"Search the web"}`),
		json.RawMessage(`{"name":"Shell","description":"Execute shell command"}`),
	}
	names := []string{"Read", "WebSearch", "Shell"}

	rt.SyncFromCatalog(schemas, names)

	if len(rt.List()) != 3 {
		t.Errorf("expected 3 tools after sync, got %d", len(rt.List()))
	}

	// 验证分类
	entry, ok := rt.GetByInternalName("Read")
	if !ok || entry.Category != CategoryFilesystem {
		t.Errorf("Read should be CategoryFilesystem, got %q", entry.Category)
	}

	entry, ok = rt.GetByInternalName("WebSearch")
	if !ok || entry.Category != CategorySearch {
		t.Errorf("WebSearch should be CategorySearch, got %q", entry.Category)
	}

	entry, ok = rt.GetByInternalName("Shell")
	if !ok || entry.Category != CategoryShell {
		t.Errorf("Shell should be CategoryShell, got %q", entry.Category)
	}
}

func TestRegisterBuiltinTools(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	tools := rt.List()
	if len(tools) < 5 {
		t.Errorf("expected at least 5 builtin tools, got %d", len(tools))
	}

	// 验证关键工具存在
	requiredTools := []string{"read_file", "write_file", "execute_shell", "web_search", "web_fetch"}
	for _, name := range requiredTools {
		if _, ok := rt.Get(name); !ok {
			t.Errorf("expected builtin tool %q", name)
		}
	}
}

func TestGetCategory(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()

	tests := []struct {
		internalName string
		expected     ToolCategory
	}{
		{"Read", CategoryFilesystem},
		{"Write", CategoryFilesystem},
		{"Shell", CategoryShell},
		{"WebSearch", CategorySearch},
		{"WebFetch", CategoryBrowser},
	}

	for _, tt := range tests {
		got := rt.GetCategory(tt.internalName)
		if got != tt.expected {
			t.Errorf("GetCategory(%q) = %q, want %q", tt.internalName, got, tt.expected)
		}
	}

	// 不存在的工具
	if got := rt.GetCategory("NonExistent"); got != "" {
		t.Errorf("expected empty category for non-existent, got %q", got)
	}
}

type mockExecBridge struct {
	lastTool string
}

func (m *mockExecBridge) OpenExec(toolName string, argsJSON []byte) (string, []byte, error) {
	m.lastTool = toolName
	return "exec-1", []byte(`{"ok":true}`), nil
}

type mockInteractionBridge struct {
	lastTool string
}

func (m *mockInteractionBridge) OpenQuery(toolName string, argsJSON []byte) (string, []byte, error) {
	m.lastTool = toolName
	return "query-1", []byte(`{"ok":true}`), nil
}

func TestExecute_DispatchesByCategory(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()
	exec := &mockExecBridge{}
	interaction := &mockInteractionBridge{}
	rt.SetBridges(exec, interaction)

	res, err := rt.Execute(context.Background(), "Read", []byte(`{"path":"a.go"}`))
	if err != nil || res == nil || !res.Success {
		t.Fatalf("Read execute err=%v res=%+v", err, res)
	}
	if exec.lastTool != "Read" {
		t.Fatalf("exec tool=%q", exec.lastTool)
	}

	res, err = rt.Execute(context.Background(), "WebSearch", []byte(`{"q":"x"}`))
	if err != nil || res == nil || !res.Success {
		t.Fatalf("WebSearch execute err=%v res=%+v", err, res)
	}
	if interaction.lastTool != "WebSearch" {
		t.Fatalf("interaction tool=%q", interaction.lastTool)
	}

	_ = rt.Enable("read_file", false)
	_, err = rt.Execute(context.Background(), "Read", nil)
	if err == nil {
		t.Fatal("expected disabled error")
	}
}

func TestToJSONSchemas(t *testing.T) {
	rt := NewRuntime()
	rt.Register(&ToolEntry{
		Name:     "enabled_tool",
		Category: CategoryFilesystem,
		Enabled:  true,
		Schema:   json.RawMessage(`{"name":"enabled_tool"}`),
	})
	rt.Register(&ToolEntry{
		Name:     "disabled_tool",
		Category: CategoryFilesystem,
		Enabled:  false,
		Schema:   json.RawMessage(`{"name":"disabled_tool"}`),
	})

	schemas, err := rt.ToJSONSchemas(nil, "")
	if err != nil {
		t.Fatalf("ToJSONSchemas failed: %v", err)
	}
	if len(schemas) != 1 {
		t.Errorf("expected 1 schema (only enabled), got %d", len(schemas))
	}
}

func TestSetBridges(t *testing.T) {
	rt := NewRuntime()
	if rt.execBridge != nil {
		t.Error("expected nil execBridge before SetBridges")
	}
	if rt.interactionBridge != nil {
		t.Error("expected nil interactionBridge before SetBridges")
	}

	rt.SetBridges(nil, nil)
	// SetBridges with nil is allowed (for gradual integration)
}

func TestClassifyCursorTool(t *testing.T) {
	tests := []struct {
		name     string
		expected ToolCategory
	}{
		{"Read", CategoryFilesystem},
		{"Write", CategoryFilesystem},
		{"Delete", CategoryFilesystem},
		{"Glob", CategoryFilesystem},
		{"Grep", CategoryFilesystem},
		{"Ls", CategoryFilesystem},
		{"ReadLints", CategoryFilesystem},
		{"Shell", CategoryShell},
		{"WriteShellStdin", CategoryShell},
		{"ForceBackgroundShell", CategoryShell},
		{"CallMcpTool", CategoryMCP},
		{"FetchMcpResource", CategoryMCP},
		{"WebSearch", CategorySearch},
		{"WebFetch", CategoryBrowser},
	}

	for _, tt := range tests {
		got := classifyCursorTool(tt.name)
		if got != tt.expected {
			t.Errorf("classifyCursorTool(%q) = %q, want %q", tt.name, got, tt.expected)
		}
	}
}

func TestToolCacheTTL(t *testing.T) {
	if ttl := toolCacheTTL("Read"); ttl != 5*time.Minute {
		t.Errorf("Read TTL = %v, want 5m", ttl)
	}
	if ttl := toolCacheTTL("WebSearch"); ttl != 10*time.Minute {
		t.Errorf("WebSearch TTL = %v, want 10m", ttl)
	}
	if ttl := toolCacheTTL("Shell"); ttl != 0 {
		t.Errorf("Shell TTL = %v, want 0", ttl)
	}
}
