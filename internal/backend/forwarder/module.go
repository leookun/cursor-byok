// module.go 负责把 forwarder service 装配成 legacy HTTP/Connect handler。
package forwarder

import (
	"net/http"

	"connectrpc.com/connect"

	modeladapter "cursor/internal/backend/agent/model"
)

type Module struct {
	Service                      *Service
	LocalBidiHandler             http.Handler
	LocalRunSSE                  http.Handler
	LocalUploadConversationBlobs http.Handler
	LocalNotifyConversationClone http.Handler
	AiHandler                    http.Handler
	RepositoryServiceHandler     http.Handler
	UploadServiceHandler         http.Handler
}

// NewModule 创建 forwarder 模块，并导出本地 Bidi / RunSSE 处理器。
func NewModule(historyRoot string, channelService modeladapter.ChannelResolver) *Module {
	service := NewService(historyRoot, channelService)
	legacyBidiAppendProcedure := "/aiserver.v1.BidiService/BidiAppend"
	legacyRunSSEProcedure := "/agent.v1.AgentService/RunSSE"
	uploadConversationBlobsProcedure := "/agent.v1.AgentService/UploadConversationBlobs"
	notifyConversationCloneProcedure := "/agent.v1.AgentService/NotifyConversationClone"
	return &Module{
		Service: service,
		LocalBidiHandler: connect.NewUnaryHandler(
			legacyBidiAppendProcedure,
			service.BidiAppend,
			connect.WithReadMaxBytes(conversationBidiMaxRequestBytes),
		),
		LocalRunSSE: NewLegacyRunSSEHandler(legacyRunSSEProcedure, service.RunSSE, func(completion legacyRunSSETerminalCompletion, delivered bool) {
			service.broker.CompleteTerminalDelivery(
				completion.requestID,
				completion.instanceID,
				completion.subscriberID,
				completion.terminalEpoch,
				delivered,
			)
		}),
		LocalUploadConversationBlobs: connect.NewUnaryHandler(
			uploadConversationBlobsProcedure,
			service.UploadConversationBlobs,
			connect.WithReadMaxBytes(conversationBlobMaxRequestBytes+(1<<20)),
		),
		LocalNotifyConversationClone: connect.NewUnaryHandler(notifyConversationCloneProcedure, service.NotifyConversationClone),
		AiHandler:                    newAIHandler(service),
		RepositoryServiceHandler:     newRepositoryServiceHandler(service),
		UploadServiceHandler:         newUploadServiceHandler(service),
	}
}
