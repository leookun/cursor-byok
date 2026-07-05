package bridge

import (
	"context"

	"cursor/internal/client"
)

// NewAPIService 是暴露给 Wails 前端的 newapi 集成入口。
// 薄包装：所有真实逻辑都在 client.ProxyService 的同名方法中。
type NewAPIService struct {
	core *client.ProxyService
}

// NewNewAPIService 用现有 ProxyService 作为底层 core 构造桥接 service。
// core 必须非空；桥接层不持有任何状态，所有调用直接转发给 core。
func NewNewAPIService(core *client.ProxyService) *NewAPIService {
	return &NewAPIService{core: core}
}

// NewAPITokenLogin 使用个人令牌登录并持久化配置。
func (s *NewAPIService) NewAPITokenLogin(req client.NewAPILoginRequest) (client.NewAPIStatus, error) {
	return s.core.NewAPITokenLogin(context.Background(), req)
}

// NewAPILogin 处理用户名/密码登录并持久化配置。
func (s *NewAPIService) NewAPILogin(req client.NewAPILoginRequest) (client.NewAPIStatus, error) {
	return s.core.NewAPILogin(context.Background(), req)
}

// NewAPIGetStatus 返回当前账号绑定状态与实时余额。
func (s *NewAPIService) NewAPIGetStatus() (client.NewAPIStatus, error) {
	return s.core.NewAPIGetStatus(context.Background())
}

// NewAPIGetLogs 分页拉取使用记录。
func (s *NewAPIService) NewAPIGetLogs(req client.NewAPILogsRequest) (client.NewAPILogsResult, error) {
	return s.core.NewAPIGetLogs(context.Background(), req)
}

// NewAPIGetModels 拉取 newapi 账号可用模型列表。
func (s *NewAPIService) NewAPIGetModels() ([]client.NewAPITokenGroup, error) {
	return s.core.NewAPIGetModels(context.Background())
}

// NewAPIImportModels 将用户勾选的模型写入 ModelAdapters。
func (s *NewAPIService) NewAPIImportModels(req client.NewAPIImportRequest) (client.NewAPIImportResult, error) {
	return s.core.NewAPIImportModels(context.Background(), req)
}

// NewAPILogout 清除配置中的 NewAPI.Token 字段。
func (s *NewAPIService) NewAPILogout() error {
	return s.core.NewAPILogout()
}

// NewAPIOpenTopup 在系统浏览器打开 newapi 充值页面。
func (s *NewAPIService) NewAPIOpenTopup() error {
	return s.core.NewAPIOpenTopup()
}
