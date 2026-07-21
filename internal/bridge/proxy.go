package bridge

import (
	backend "cursor/internal/backend"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/mitm"
	"runtime"
	"sync"
)

// Public DTOs remain in package main for Wails service compatibility.
// ProxyState 定义了当前模块中的 ProxyState 类型。
type ProxyState = client.ProxyState

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = client.UserConfig

// ModelAdapterConfig 定义模型测速使用的模型配置结构。
type ModelAdapterConfig = serverconfig.ModelAdapterConfig

// ModelAdapterTestResult 定义一次模型测速结果。
type ModelAdapterTestResult = client.ModelAdapterTestResult

// ModelAdapterTestResultsPayload 定义测速结果事件载荷。
type ModelAdapterTestResultsPayload = client.ModelAdapterTestResultsPayload

// LicenseActionRequest 定义了当前模块中的 LicenseActionRequest 类型。
type LicenseActionRequest = client.LicenseActionRequest

// LicenseSwitchDeviceRequest 定义了当前模块中的 LicenseSwitchDeviceRequest 类型。
type LicenseSwitchDeviceRequest = client.LicenseSwitchDeviceRequest

// LicenseAPIResult 定义了当前模块中的 LicenseAPIResult 类型。
type LicenseAPIResult = client.LicenseAPIResult

// UsageRecordsRequest 定义了当前模块中的 UsageRecordsRequest 类型。
type UsageRecordsRequest = client.UsageRecordsRequest

// UsageRecord 定义了当前模块中的 UsageRecord 类型。
type UsageRecord = client.UsageRecord

// UsageRecordsData 定义了当前模块中的 UsageRecordsData 类型。
type UsageRecordsData = client.UsageRecordsData

// UsageRecordsResult 定义了当前模块中的 UsageRecordsResult 类型。
type UsageRecordsResult = client.UsageRecordsResult

// ProxyService 定义了当前模块中的 ProxyService 类型。
type ProxyService struct {
	// core 表示当前声明中的 core。
	core *client.ProxyService
	// onCursorActivity 是可选的 Cursor 请求活动回调。
	onCursorActivityMu sync.RWMutex
	onCursorActivity   func(method, path string)
}

// NewProxyService 用于处理与 NewProxyService 相关的逻辑。
func NewProxyService(proxy *mitm.ProxyServer, certManager *certs.Manager, caCertPEM []byte) *ProxyService {
	return &ProxyService{core: client.NewProxyService(proxy, certManager, caCertPEM)}
}

// StartProxy 用于处理与 StartProxy 相关的逻辑。
func (s *ProxyService) StartProxy() (ProxyState, error) {
	return s.core.StartProxy()
}

// StopProxy 用于处理与 StopProxy 相关的逻辑。
func (s *ProxyService) StopProxy() (ProxyState, error) {
	return s.core.StopProxy()
}

// GetState 用于处理与 GetState 相关的逻辑。
func (s *ProxyService) GetState() ProxyState {
	return s.core.GetState()
}

// ClearLastError 用于处理与 ClearLastError 相关的逻辑。
func (s *ProxyService) ClearLastError() ProxyState {
	return s.core.ClearLastError()
}

// SetBaseURL 用于处理与 SetBaseURL 相关的逻辑。
func (s *ProxyService) SetBaseURL(baseURL string) (ProxyState, error) {
	return s.core.SetBaseURL(baseURL)
}

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	return s.core.LoadUserConfig()
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	return s.core.SaveUserConfig(cfg)
}

// OptimizationCostSummary 定义 Optimization Runtime 成本摘要 DTO。
type OptimizationCostSummary = client.OptimizationCostSummary

// AOSLastTraceSummary 最近一次 AOS 执行摘要 DTO。
type AOSLastTraceSummary = client.AOSLastTraceSummary

// AOSExecutionTreeNode 交互式执行树节点 DTO。
type AOSExecutionTreeNode = client.AOSExecutionTreeNode

// AOSExecutionTree 结构化执行树 DTO。
type AOSExecutionTree = client.AOSExecutionTree

// RecognizeAOSMembersResult Leader「认识组员」结果 DTO。
type RecognizeAOSMembersResult = client.RecognizeAOSMembersResult

// FetchModelsRequest 从上游 Provider 获取模型列表的请求 DTO。
type FetchModelsRequest = client.FetchModelsRequest

// FetchModelsResult 从上游 Provider 获取模型列表的响应 DTO。
type FetchModelsResult = client.FetchModelsResult

// RecognizedMemberDTO 单成员识别结果 DTO。
type RecognizedMemberDTO = client.RecognizedMemberDTO

// ToolEntryDTO 工具条目 DTO（供前端展示）。
type ToolEntryDTO = client.ToolEntryDTO

// ToolCacheStatsDTO 工具缓存统计 DTO。
type ToolCacheStatsDTO = client.ToolCacheStatsDTO

// GetOptimizationCostSummary 返回进程内 Optimization 成本与当前 Tier。
func (s *ProxyService) GetOptimizationCostSummary() (OptimizationCostSummary, error) {
	return s.core.GetOptimizationCostSummary()
}

// GetAOSLastTraceSummary 返回最近一次 AOS 执行轨迹摘要。
func (s *ProxyService) GetAOSLastTraceSummary() (AOSLastTraceSummary, error) {
	return s.core.GetAOSLastTraceSummary()
}

// GetAOSExecutionTree 按 session ID 返回结构化 AOS 执行树（Phase 9 切片）。
func (s *ProxyService) GetAOSExecutionTree(sessionID string) (AOSExecutionTree, error) {
	return s.core.GetAOSExecutionTree(sessionID)
}

// ReplayAOSTrace 以 trace 中保存的原始用户输入重新触发 AOS 执行（Phase 9 切片）。
func (s *ProxyService) ReplayAOSTrace(sessionID string) (string, error) {
	return s.core.ReplayAOSTrace(sessionID)
}

// ReplayAOSNode replays a single trace node by session ID and node index (Phase 9 slice).
func (s *ProxyService) ReplayAOSNode(sessionID string, nodeIndex int) (string, error) {
	return s.core.ReplayAOSNode(sessionID, nodeIndex)
}

// RecognizeAOSMembers 让 Leader 读取每位成员的 name+systemPrompt，推断路由 tags。
// 供 AosConfig.vue「认识组员」按钮调用（Wails 绑定入口）。
func (s *ProxyService) RecognizeAOSMembers() (RecognizeAOSMembersResult, error) {
	return s.core.RecognizeAOSMembers()
}

// FetchModelsFromProvider 调用上游 /v1/models 接口，返回可用模型 ID 列表。
// 供 ModelAdapterModal.vue「获取模型」按钮调用（Wails 绑定入口）。
func (s *ProxyService) FetchModelsFromProvider(req FetchModelsRequest) (FetchModelsResult, error) {
	return s.core.FetchModelsFromProvider(req)
}

// TestModelAdapter 用于处理与 TestModelAdapter 相关的逻辑。
func (s *ProxyService) TestModelAdapter(adapter ModelAdapterConfig) (ModelAdapterTestResult, error) {
	return s.core.TestModelAdapter(adapter)
}

// GetModelAdapterTestResults 用于处理与 GetModelAdapterTestResults 相关的逻辑。
func (s *ProxyService) GetModelAdapterTestResults() []ModelAdapterTestResult {
	return s.core.GetModelAdapterTestResults()
}

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func (s *ProxyService) GetDeviceID() (string, error) {
	return s.core.GetDeviceID()
}

// ActivateLicense 用于处理与 ActivateLicense 相关的逻辑。
func (s *ProxyService) ActivateLicense(req LicenseActionRequest) (LicenseAPIResult, error) {
	return s.core.ActivateLicense(req)
}

// BindLicenseDevice 用于处理与 BindLicenseDevice 相关的逻辑。
func (s *ProxyService) BindLicenseDevice(req LicenseActionRequest) (LicenseAPIResult, error) {
	return s.core.BindLicenseDevice(req)
}

// SwitchLicenseDevice 用于处理与 SwitchLicenseDevice 相关的逻辑。
func (s *ProxyService) SwitchLicenseDevice(req LicenseSwitchDeviceRequest) (LicenseAPIResult, error) {
	return s.core.SwitchLicenseDevice(req)
}

// QueryUsageRecords 用于处理与 QueryUsageRecords 相关的逻辑。
func (s *ProxyService) QueryUsageRecords(req UsageRecordsRequest) (UsageRecordsResult, error) {
	return s.core.QueryUsageRecords(req)
}

// ApplyCursorSettings 用于处理与 ApplyCursorSettings 相关的逻辑。
func (s *ProxyService) ApplyCursorSettings() error {
	return s.core.ApplyCursorSettings()
}

// ClearCursorSettings 用于处理与 ClearCursorSettings 相关的逻辑。
func (s *ProxyService) ClearCursorSettings() error {
	return s.core.ClearCursorSettings()
}

// ShutdownForQuit 用于处理与 ShutdownForQuit 相关的逻辑。
func (s *ProxyService) ShutdownForQuit() {
	s.core.ShutdownForQuit()
}

// BackendHost 返回底层 client.ProxyService 的 backend.Host（可能为 nil）。
// 供 runner 注入跨包回调（如模型活动状态桥接到 pet），避免暴露 core 私有字段。
func (s *ProxyService) BackendHost() *backend.Host {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.BackendHost()
}

// SetCursorActivityCallback 注册 Cursor 请求活动回调（供 PetService 使用）。
func (s *ProxyService) SetCursorActivityCallback(fn func(method, path string)) {
	s.onCursorActivityMu.Lock()
	defer s.onCursorActivityMu.Unlock()
	s.onCursorActivity = fn
}

// FireCursorActivity 触发活动回调（供 mitm 层或 runner 调用）。
func (s *ProxyService) FireCursorActivity(method, path string) {
	s.onCursorActivityMu.RLock()
	fn := s.onCursorActivity
	s.onCursorActivityMu.RUnlock()
	if fn != nil {
		fn(method, path)
	}
}

// IsWindows 用于处理与 IsWindows 相关的逻辑。
func (s *ProxyService) IsWindows() bool {
	return runtime.GOOS == "windows"
}

// ListTools 列出所有已注册工具。
func (s *ProxyService) ListTools() ([]ToolEntryDTO, error) {
	if s == nil || s.core == nil {
		return nil, nil
	}
	return s.core.ListTools()
}

// ToggleTool 启用/禁用指定工具。
func (s *ProxyService) ToggleTool(name string, enabled bool) error {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.ToggleTool(name, enabled)
}

// GetToolCacheStats 返回工具缓存统计。
func (s *ProxyService) GetToolCacheStats() (ToolCacheStatsDTO, error) {
	if s == nil || s.core == nil {
		return ToolCacheStatsDTO{}, nil
	}
	return s.core.GetToolCacheStats()
}

// CacheStatsDTO 缓存统计 DTO。
type CacheStatsDTO = client.CacheStatsDTO

// GetCacheStats 返回 Cache Runtime 统计（精确命中、语义命中、总命中、命中率、节省 token 等）。
func (s *ProxyService) GetCacheStats() (CacheStatsDTO, error) {
	if s == nil || s.core == nil {
		return CacheStatsDTO{}, nil
	}
	return s.core.GetCacheStats()
}

// ClearCache 清空精确缓存与语义缓存。
func (s *ProxyService) ClearCache() error {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.ClearCache()
}

// MCPServerInfoDTO MCP server 概要 DTO。
type MCPServerInfoDTO = client.MCPServerInfoDTO

// ListMCPServers 列出所有已知 MCP server。
func (s *ProxyService) ListMCPServers() ([]MCPServerInfoDTO, error) {
	if s == nil || s.core == nil {
		return nil, nil
	}
	return s.core.ListMCPServers()
}

// ToggleMCPServer 启用/禁用指定 MCP server 的所有工具。
func (s *ProxyService) ToggleMCPServer(server string, enabled bool) error {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.ToggleMCPServer(server, enabled)
}

// ClearToolCache 清空工具结果缓存。
func (s *ProxyService) ClearToolCache() (client.ClearToolCacheResult, error) {
	if s == nil || s.core == nil {
		return client.ClearToolCacheResult{}, nil
	}
	return s.core.ClearToolCache()
}
