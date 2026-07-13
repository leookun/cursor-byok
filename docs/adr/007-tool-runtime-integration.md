# ADR-007: Tool Runtime 与现有 ExecBridge 集成

**状态**：Accepted

**日期**：2026-07-13

**决策者**：AI Chief Architect

---

## 背景

现有工具体系分散在两个桥接器中（ExecBridge 处理执行型工具、InteractionBridge 处理交互型工具），缺少统一的管理层。需要建立 Tool Runtime 作为统一入口。

## 决策

### 架构层次

```
ToolRuntime (统一入口)
    │
    ├── CategoryFilesystem → ExecBridge.OpenExec("Read"/"Write"/...)
    ├── CategoryShell      → ExecBridge.OpenExec("Shell"/...)
    ├── CategoryMCP        → ExecBridge.OpenExec("CallMcpTool"/...)
    ├── CategoryBrowser    → InteractionBridge.OpenQuery("WebFetch"/...)
    └── CategorySearch     → InteractionBridge.OpenQuery("WebSearch"/...)
```

### 关键设计

1. **不替代 ExecBridge/InteractionBridge**：Tool Runtime 是上层管理层，现有桥接器保持不变
2. **Category 分类**：每个工具绑定一个 `ToolCategory`，运行时按 Category 分派
3. **`InternalName` 映射**：`ToolEntry.InternalName` 指向 Cursor 内部工具名（如 "Read"），外部用友好名（如 "read_file"）
4. **`SyncFromCatalog`**：从 `tool_catalog.go` 自动同步工具列表到 Tool Runtime
5. **`IsCacheable` + `CacheTTL`**：每个工具声明是否可缓存及 TTL，供 Cache Runtime 使用

### Forwarder 集成

在 `handleToolInvocation` 中，工具分派前先通过 Tool Runtime 查询元数据：
- 检查工具是否被禁用（`entry.Enabled`）
- 获取 Category 用于分类
- 获取 Cacheable 状态用于缓存策略

## 工具分类表

| Cursor 工具 | Category | Cacheable | TTL |
|---|---|---|---|
| Read | filesystem | yes | 5 min |
| Write | filesystem | no | — |
| Delete | filesystem | no | — |
| Glob | filesystem | yes | 5 min |
| Grep | filesystem | yes | 1 min |
| Ls | filesystem | yes | 5 min |
| ReadLints | filesystem | yes | 1 min |
| Shell | shell | no | — |
| WriteShellStdin | shell | no | — |
| CallMcpTool | mcp | no | — |
| FetchMcpResource | mcp | yes | 2 min |
| WebSearch | search | yes | 10 min |
| WebFetch | browser | yes | 5 min |

## 测试覆盖（14 个，全部通过）

| 测试 | 覆盖场景 |
|---|---|
| TestNewRuntime | 创建和初始状态 |
| TestRegisterAndGet | 注册和查找 |
| TestRegisterNil | 空值校验 |
| TestGetByInternalName | InternalName 映射 |
| TestIsExecTool | 执行型判断 |
| TestIsInteractionTool | 交互型判断 |
| TestIsCacheable | 缓存策略 |
| TestListByCategory | 按分类列出 |
| TestEnableDisable | 启用/禁用 |
| TestListEnabled | 启用过滤 |
| TestSyncFromCatalog | 从 catalog 同步 |
| TestRegisterBuiltinTools | 内置工具注册 |
| TestGetCategory | 分类查询 |
| TestToJSONSchemas | Schema 导出 |
| TestSetBridges | 桥接设置 |
| TestClassifyCursorTool | 工具分类 |
| TestToolCacheTTL | TTL 配置 |

## 影响

- `internal/backend/runtime/tool/runtime.go` — 增加 Category 分类、InternalName 映射、SyncFromCatalog、IsCacheable
- `internal/backend/runtime/tool/adapter.go` — ExecBridge/InteractionBridge 适配器
- `internal/backend/forwarder/service.go` — handleToolInvocation 集成 Tool Runtime 查询
- `internal/backend/forwarder/module.go` — 接受 Tool Runtime 参数
- `internal/backend/host.go` — 创建 Tool Runtime 实例

## 参考

- `internal/backend/agent/bridge/exec/bridge.go` — ExecBridge 实现
- `internal/backend/agent/bridge/interaction/bridge.go` — InteractionBridge 实现
- `internal/backend/forwarder/tool_catalog.go` — 工具白名单
