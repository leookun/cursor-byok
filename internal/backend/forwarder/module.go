// module.go 负责把 forwarder service 装配成 legacy HTTP/Connect handler。
package forwarder

import (
	"net/http"

	"connectrpc.com/connect"

	modeladapter "cursor/internal/backend/agent/model"
	cacheruntime "cursor/internal/backend/runtime/cache"
	optimize "cursor/internal/backend/runtime/optimize"
	toolruntime "cursor/internal/backend/runtime/tool"
	vm "cursor/internal/backend/virtualmodel"
)

type Module struct {
	Service                  *Service
	LocalBidiHandler         http.Handler
	LocalRunSSE              http.Handler
	AiHandler                http.Handler
	RepositoryServiceHandler http.Handler
	UploadServiceHandler     http.Handler
}

// NewModule 创建 forwarder 模块，并导出本地 Bidi / RunSSE 处理器。
func NewModule(historyRoot string, channelService modeladapter.ChannelResolver) *Module {
	return NewModuleWithRuntimes(historyRoot, channelService, nil, nil, nil, nil)
}

// NewModuleWithVM 创建带虚拟模型支持的 forwarder 模块。
func NewModuleWithVM(historyRoot string, channelService modeladapter.ChannelResolver, vmManager *vm.Manager) *Module {
	return NewModuleWithRuntimes(historyRoot, channelService, vmManager, nil, nil, nil)
}

// NewModuleWithRuntimes 创建带完整 Runtime 支持的 forwarder 模块。
func NewModuleWithRuntimes(historyRoot string, channelService modeladapter.ChannelResolver, vmManager *vm.Manager, optRuntime *optimize.Runtime, cacheRuntime *cacheruntime.Runtime, toolRT *toolruntime.Runtime) *Module {
	service := NewServiceWithRuntimes(historyRoot, channelService, vmManager, optRuntime, cacheRuntime, toolRT)
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	return &Module{
		Service:                  service,
		LocalBidiHandler:         connect.NewUnaryHandler(legacyBidiAppendProcedure, service.BidiAppend),
		LocalRunSSE:              NewLegacyRunSSEHandler(legacyRunSSEProcedure, service.RunSSE),
		AiHandler:                newAIHandler(service),
		RepositoryServiceHandler: newRepositoryServiceHandler(service),
		UploadServiceHandler:     newUploadServiceHandler(service),
	}
}
