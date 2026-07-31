package forwarder

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
)

const AgentServiceUpdateConversationMetadataProcedure = "/agent.v1.AgentService/UpdateConversationMetadata"

func newAgentHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(
		AgentServiceUpdateConversationMetadataProcedure,
		connect.NewUnaryHandler(AgentServiceUpdateConversationMetadataProcedure, service.UpdateConversationMetadata),
	)
	mux.Handle("/", http.NotFoundHandler())
	return mux
}

func (service *Service) UpdateConversationMetadata(_ context.Context, req *connect.Request[agentv1.UpdateConversationMetadataRequest]) (*connect.Response[agentv1.UpdateConversationMetadataResponse], error) {
	if service == nil || service.store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("conversation store is not initialized"))
	}
	if req == nil || req.Msg == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("update conversation metadata request is required"))
	}
	conversationID := strings.TrimSpace(req.Msg.GetConversationId())
	if conversationID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("conversation_id is required"))
	}

	workspacePaths := compactWorkspacePaths(req.Msg.GetWorkspacePaths(), "")
	_, _, err := service.store.UpdateConversationMetaIfChanged(conversationID, func(item *ConversationFile) (bool, error) {
		changed := false
		if req.Msg.Name != nil {
			name := strings.TrimSpace(req.Msg.GetName())
			if !req.Msg.GetOnlySetNameIfEmpty() || strings.TrimSpace(item.Name) == "" {
				if item.Name != name {
					item.Name = name
					changed = true
				}
			}
		}
		if len(workspacePaths) > 0 && !slices.Equal(item.WorkspacePaths, workspacePaths) {
			item.WorkspacePaths = cloneStringSlice(workspacePaths)
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentv1.UpdateConversationMetadataResponse{}), nil
}
