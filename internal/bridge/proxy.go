package bridge

import (
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/mitm"
	"runtime"
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

// IsWindows 用于处理与 IsWindows 相关的逻辑。
func (s *ProxyService) IsWindows() bool {
	return runtime.GOOS == "windows"
}
