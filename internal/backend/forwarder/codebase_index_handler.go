package forwarder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/aiserverv1"
)

func (service *Service) CreateExperimentalIndex(_ context.Context, req *connect.Request[aiserverv1.CreateExperimentalIndexRequest]) (*connect.Response[aiserverv1.CreateExperimentalIndexResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	record, err := store.EnsureIndexed(CodebaseIndexRecord{
		Repo:      strings.TrimSpace(req.Msg.GetRepo()),
		TargetDir: strings.TrimSpace(req.Msg.GetTargetDir()),
		Files:     append([]string(nil), req.Msg.GetFiles()...),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&aiserverv1.CreateExperimentalIndexResponse{IndexId: record.IndexID}), nil
}

func (service *Service) ListExperimentalIndexFiles(_ context.Context, req *connect.Request[aiserverv1.ListExperimentalIndexFilesRequest]) (*connect.Response[aiserverv1.ListExperimentalIndexFilesResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	indexID := strings.TrimSpace(req.Msg.GetIndexId())
	files, err := store.ListFiles(indexID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&aiserverv1.ListExperimentalIndexFilesResponse{
		IndexId: indexID,
		Files:   codebaseIndexFileData(indexID, files),
	}), nil
}

func (service *Service) ListenExperimentalIndex(ctx context.Context, req *connect.Request[aiserverv1.ListenExperimentalIndexRequest], stream *connect.ServerStream[aiserverv1.ListenExperimentalIndexResponse]) error {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	indexID := strings.TrimSpace(req.Msg.GetIndexId())
	if indexID == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("index_id is required"))
	}
	_, err = store.EnsureIndexed(CodebaseIndexRecord{IndexID: indexID})
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	subscription, err := store.Subscribe(indexID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	defer store.Unsubscribe(subscription)
	if err := stream.Send(&aiserverv1.ListenExperimentalIndexResponse{
		IndexId: indexID,
		Item: &aiserverv1.ListenExperimentalIndexResponse_Ready{
			Ready: &aiserverv1.ListenExperimentalIndexResponse_ReadyItem{
				IndexId: indexID,
				Request: &aiserverv1.ListenExperimentalIndexRequest{
					IndexId: indexID,
				},
			},
		},
	}); err != nil {
		return err
	}
	var lastEventID int64
	for {
		events, err := store.ListEvents(indexID, lastEventID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		for _, event := range events {
			if event.ID > lastEventID {
				lastEventID = event.ID
			}
			if event.Kind != codebaseIndexEventKindRegister {
				continue
			}
			if err := stream.Send(codebaseIndexRegisterEventResponse(indexID, event)); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-subscription.Signal:
		}
	}
}

func (service *Service) RegisterFileToIndex(_ context.Context, req *connect.Request[aiserverv1.RegisterFileToIndexRequest]) (*connect.Response[aiserverv1.RequestReceivedResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	reqUUID := uuid.NewString()
	_, _, err = store.RegisterFileWithRequest(req.Msg.GetIndexId(), reqUUID, CodebaseIndexFileRecord{
		WorkspaceRelativePath: strings.TrimSpace(req.Msg.GetWorkspaceRelativePath()),
		ContentHash:           codebaseFileContentHash(strings.Join(req.Msg.GetContent(), "\n")),
		Stage:                 codebaseIndexFileStage,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&aiserverv1.RequestReceivedResponse{ReqUuid: reqUUID}), nil
}

func (service *Service) SetupIndexDependencies(_ context.Context, req *connect.Request[aiserverv1.SetupIndexDependenciesRequest]) (*connect.Response[aiserverv1.SetupIndexDependenciesResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := store.MarkDependenciesReady(req.Msg.GetIndexId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&aiserverv1.SetupIndexDependenciesResponse{}), nil
}

func (service *Service) ComputeIndexTopoSort(_ context.Context, req *connect.Request[aiserverv1.ComputeIndexTopoSortRequest]) (*connect.Response[aiserverv1.ComputeIndexTopoSortResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := store.MarkTopoSortReady(req.Msg.GetIndexId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&aiserverv1.ComputeIndexTopoSortResponse{}), nil
}

func (service *Service) requireCodebaseIndexStore() (*CodebaseIndexStore, error) {
	if service == nil || service.codebaseIndexStore == nil {
		return nil, fmt.Errorf("codebase index store is not initialized")
	}
	return service.codebaseIndexStore, nil
}

func codebaseIndexFileData(indexID string, files []CodebaseIndexFileRecord) []*aiserverv1.IndexFileData {
	result := make([]*aiserverv1.IndexFileData, 0, len(files))
	for index, file := range files {
		result = append(result, codebaseIndexFileDatum(indexID, file, int32(index+1)))
	}
	return result
}

func codebaseIndexFileDatum(indexID string, file CodebaseIndexFileRecord, fallbackOrder int32) *aiserverv1.IndexFileData {
	order := file.Order
	if order == 0 {
		order = fallbackOrder
	}
	return &aiserverv1.IndexFileData{
		IndexId:               indexID,
		WorkspaceRelativePath: file.WorkspaceRelativePath,
		Stage:                 file.Stage,
		Order:                 order,
	}
}

func codebaseIndexRegisterEventResponse(indexID string, event CodebaseIndexEventRecord) *aiserverv1.ListenExperimentalIndexResponse {
	fileData := codebaseIndexFileDatum(indexID, event.File, 1)
	return &aiserverv1.ListenExperimentalIndexResponse{
		IndexId: indexID,
		Item: &aiserverv1.ListenExperimentalIndexResponse_Register{
			Register: &aiserverv1.ListenExperimentalIndexResponse_RegisterItem{
				ReqUuid: strings.TrimSpace(event.ReqUUID),
				Request: &aiserverv1.RegisterFileToIndexRequest{
					IndexId:               indexID,
					WorkspaceRelativePath: event.File.WorkspaceRelativePath,
				},
				Response: &aiserverv1.RegisterFileToIndexResponse{
					FileId:            codebaseIndexFileID(indexID, event.File.WorkspaceRelativePath),
					RootContextNodeId: codebaseIndexFileID(indexID, event.File.WorkspaceRelativePath),
					FileData:          fileData,
				},
			},
		},
	}
}

func codebaseIndexFileID(indexID string, workspaceRelativePath string) string {
	trimmedIndexID := strings.TrimSpace(indexID)
	trimmedPath := strings.TrimSpace(workspaceRelativePath)
	if trimmedIndexID == "" || trimmedPath == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(trimmedIndexID + "\x00" + trimmedPath))
	return "file_" + hex.EncodeToString(sum[:])[:24]
}
