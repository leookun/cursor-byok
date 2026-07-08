package forwarder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
	"cursor/internal/logger"
)

const (
	RepositoryServiceFastRepoInitHandshakeV2Procedure        = "/aiserver.v1.RepositoryService/FastRepoInitHandshakeV2"
	RepositoryServiceFastRepoInitHandshakeProcedure          = "/aiserver.v1.RepositoryService/FastRepoInitHandshake"
	RepositoryServiceFastRepoSyncCompleteProcedure           = "/aiserver.v1.RepositoryService/FastRepoSyncComplete"
	RepositoryServiceSyncMerkleSubtreeV2Procedure            = "/aiserver.v1.RepositoryService/SyncMerkleSubtreeV2"
	RepositoryServiceSyncMerkleSubtreeProcedure              = "/aiserver.v1.RepositoryService/SyncMerkleSubtree"
	RepositoryServiceFastUpdateFileV2Procedure               = "/aiserver.v1.RepositoryService/FastUpdateFileV2"
	RepositoryServiceFastUpdateFileProcedure                 = "/aiserver.v1.RepositoryService/FastUpdateFile"
	RepositoryServiceEnsureIndexCreatedProcedure             = "/aiserver.v1.RepositoryService/EnsureIndexCreated"
	RepositoryServiceGetCopyStatusProcedure                  = "/aiserver.v1.RepositoryService/GetCopyStatus"
	RepositoryServiceGetUploadLimitsProcedure                = "/aiserver.v1.RepositoryService/GetUploadLimits"
	RepositoryServiceGetNumFilesToSendProcedure              = "/aiserver.v1.RepositoryService/GetNumFilesToSend"
	RepositoryServiceGetAvailableChunkingStrategiesProcedure = "/aiserver.v1.RepositoryService/GetAvailableChunkingStrategies"
	RepositoryServiceGetHighLevelFolderDescriptionProcedure  = "/aiserver.v1.RepositoryService/GetHighLevelFolderDescription"
	RepositoryServiceRepositoryStatusProcedure               = "/aiserver.v1.RepositoryService/RepositoryStatus"
	RepositoryServiceBatchRepositoryStatusProcedure          = "/aiserver.v1.RepositoryService/BatchRepositoryStatus"
)

func newRepositoryServiceHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(
		RepositoryServiceFastRepoInitHandshakeV2Procedure,
		connect.NewUnaryHandler(RepositoryServiceFastRepoInitHandshakeV2Procedure, service.FastRepoInitHandshakeV2),
	)
	mux.Handle(
		RepositoryServiceFastRepoInitHandshakeProcedure,
		connect.NewUnaryHandler(RepositoryServiceFastRepoInitHandshakeProcedure, service.FastRepoInitHandshake),
	)
	mux.Handle(
		RepositoryServiceFastRepoSyncCompleteProcedure,
		connect.NewUnaryHandler(RepositoryServiceFastRepoSyncCompleteProcedure, service.FastRepoSyncComplete),
	)
	mux.Handle(
		RepositoryServiceSyncMerkleSubtreeV2Procedure,
		connect.NewUnaryHandler(RepositoryServiceSyncMerkleSubtreeV2Procedure, service.SyncMerkleSubtreeV2),
	)
	mux.Handle(
		RepositoryServiceSyncMerkleSubtreeProcedure,
		connect.NewUnaryHandler(RepositoryServiceSyncMerkleSubtreeProcedure, service.SyncMerkleSubtree),
	)
	mux.Handle(
		RepositoryServiceFastUpdateFileV2Procedure,
		connect.NewUnaryHandler(RepositoryServiceFastUpdateFileV2Procedure, service.FastUpdateFileV2),
	)
	mux.Handle(
		RepositoryServiceFastUpdateFileProcedure,
		connect.NewUnaryHandler(RepositoryServiceFastUpdateFileProcedure, service.FastUpdateFile),
	)
	mux.Handle(
		RepositoryServiceEnsureIndexCreatedProcedure,
		connect.NewUnaryHandler(RepositoryServiceEnsureIndexCreatedProcedure, service.EnsureIndexCreated),
	)
	mux.Handle(
		RepositoryServiceGetCopyStatusProcedure,
		connect.NewUnaryHandler(RepositoryServiceGetCopyStatusProcedure, service.GetCopyStatus),
	)
	mux.Handle(
		RepositoryServiceGetUploadLimitsProcedure,
		connect.NewUnaryHandler(RepositoryServiceGetUploadLimitsProcedure, service.GetUploadLimits),
	)
	mux.Handle(
		RepositoryServiceGetNumFilesToSendProcedure,
		connect.NewUnaryHandler(RepositoryServiceGetNumFilesToSendProcedure, service.GetNumFilesToSend),
	)
	mux.Handle(
		RepositoryServiceGetAvailableChunkingStrategiesProcedure,
		connect.NewUnaryHandler(RepositoryServiceGetAvailableChunkingStrategiesProcedure, service.GetAvailableChunkingStrategies),
	)
	mux.Handle(
		RepositoryServiceGetHighLevelFolderDescriptionProcedure,
		connect.NewUnaryHandler(RepositoryServiceGetHighLevelFolderDescriptionProcedure, service.GetHighLevelFolderDescription),
	)
	mux.Handle(
		RepositoryServiceRepositoryStatusProcedure,
		connect.NewUnaryHandler(RepositoryServiceRepositoryStatusProcedure, service.RepositoryStatus),
	)
	mux.Handle(
		RepositoryServiceBatchRepositoryStatusProcedure,
		connect.NewUnaryHandler(RepositoryServiceBatchRepositoryStatusProcedure, service.BatchRepositoryStatus),
	)
	mux.Handle("/", http.NotFoundHandler())
	return mux
}

func (service *Service) FastRepoInitHandshakeV2(_ context.Context, req *connect.Request[aiserverv1.FastRepoInitHandshakeV2Request]) (*connect.Response[aiserverv1.FastRepoInitHandshakeV2Response], error) {
	repo := req.Msg.GetRepository()
	codebaseID := repositoryCodebaseID(repo)
	if err := service.ensureRepositoryCodebase(codebaseID, repo); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	logger.Infof("RepositoryService FastRepoInitHandshakeV2 ready codebase_id=%s repo=%s root_hash_present=%t", codebaseID, repositoryLogName(repo), strings.TrimSpace(req.Msg.GetRootHash()) != "")
	return connect.NewResponse(&aiserverv1.FastRepoInitHandshakeV2Response{
		Status: aiserverv1.FastRepoInitHandshakeV2Response_STATUS_SUCCESS,
		Codebases: []*aiserverv1.RepositoryCodebaseInfo{
			{
				CodebaseId: codebaseID,
				Status:     aiserverv1.RepositoryCodebaseInfo_STATUS_UP_TO_DATE,
			},
		},
	}), nil
}

func (service *Service) FastRepoInitHandshake(_ context.Context, req *connect.Request[aiserverv1.FastRepoInitHandshakeRequest]) (*connect.Response[aiserverv1.FastRepoInitHandshakeResponse], error) {
	repo := req.Msg.GetRepository()
	codebaseID := repositoryCodebaseID(repo)
	if err := service.ensureRepositoryCodebase(codebaseID, repo); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	repoName := repositoryRepoName(repo)
	logger.Infof("RepositoryService FastRepoInitHandshake ready codebase_id=%s repo=%s root_hash_present=%t", codebaseID, repositoryLogName(repo), strings.TrimSpace(req.Msg.GetRootHash()) != "")
	return connect.NewResponse(&aiserverv1.FastRepoInitHandshakeResponse{
		Status:   aiserverv1.FastRepoInitHandshakeResponse_STATUS_UP_TO_DATE,
		RepoName: repoName,
	}), nil
}

func (service *Service) FastRepoSyncComplete(_ context.Context, req *connect.Request[aiserverv1.FastRepoSyncCompleteRequest]) (*connect.Response[aiserverv1.FastRepoSyncCompleteResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, codebase := range req.Msg.GetCodebases() {
		codebaseID := strings.TrimSpace(codebase.GetCodebaseId())
		if codebaseID == "" {
			continue
		}
		if err := store.MarkRepositorySyncComplete(codebaseID, codebase.GetPathKeyHash()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	logger.Infof("RepositoryService FastRepoSyncComplete acknowledged codebase_count=%d", len(req.Msg.GetCodebases()))
	return connect.NewResponse(&aiserverv1.FastRepoSyncCompleteResponse{}), nil
}

func (service *Service) SyncMerkleSubtreeV2(_ context.Context, req *connect.Request[aiserverv1.SyncMerkleSubtreeV2Request]) (*connect.Response[aiserverv1.SyncMerkleSubtreeV2Response], error) {
	logger.Infof("RepositoryService SyncMerkleSubtreeV2 matched codebase_id=%s partial_paths=%d", strings.TrimSpace(req.Msg.GetCodebaseId()), len(req.Msg.GetLocalPartialPaths()))
	return connect.NewResponse(&aiserverv1.SyncMerkleSubtreeV2Response{
		Result:  &aiserverv1.SyncMerkleSubtreeV2Response_Match{Match: true},
		Results: repositoryPartialPathMatchResults(len(req.Msg.GetLocalPartialPaths())),
	}), nil
}

func (service *Service) SyncMerkleSubtree(_ context.Context, req *connect.Request[aiserverv1.SyncMerkleSubtreeRequest]) (*connect.Response[aiserverv1.SyncMerkleSubtreeResponse], error) {
	logger.Infof("RepositoryService SyncMerkleSubtree matched repo=%s", repositoryLogName(req.Msg.GetRepository()))
	return connect.NewResponse(&aiserverv1.SyncMerkleSubtreeResponse{
		Result: &aiserverv1.SyncMerkleSubtreeResponse_Match{Match: true},
	}), nil
}

func (service *Service) FastUpdateFileV2(_ context.Context, req *connect.Request[aiserverv1.FastUpdateFileV2Request]) (*connect.Response[aiserverv1.FastUpdateFileV2Response], error) {
	logger.Infof("RepositoryService FastUpdateFileV2 accepted codebase_id=%s update_type=%s file_updates=%d", strings.TrimSpace(req.Msg.GetCodebaseId()), req.Msg.GetUpdateType().String(), len(req.Msg.GetFileUpdates()))
	return connect.NewResponse(&aiserverv1.FastUpdateFileV2Response{
		Status: aiserverv1.FastUpdateFileV2Response_STATUS_SUCCESS,
	}), nil
}

func (service *Service) FastUpdateFile(_ context.Context, req *connect.Request[aiserverv1.FastUpdateFileRequest]) (*connect.Response[aiserverv1.FastUpdateFileResponse], error) {
	logger.Infof("RepositoryService FastUpdateFile accepted repo=%s update_type=%s", repositoryLogName(req.Msg.GetRepository()), req.Msg.GetUpdateType().String())
	return connect.NewResponse(&aiserverv1.FastUpdateFileResponse{
		Status: aiserverv1.FastUpdateFileResponse_STATUS_SUCCESS,
	}), nil
}

func (service *Service) EnsureIndexCreated(_ context.Context, req *connect.Request[aiserverv1.EnsureIndexCreatedRequest]) (*connect.Response[aiserverv1.EnsureIndexCreatedResponse], error) {
	repo := req.Msg.GetRepository()
	codebaseID := repositoryCodebaseID(repo)
	if err := service.ensureRepositoryCodebase(codebaseID, repo); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	logger.Infof("RepositoryService EnsureIndexCreated ready codebase_id=%s repo=%s", codebaseID, repositoryLogName(repo))
	return connect.NewResponse(&aiserverv1.EnsureIndexCreatedResponse{}), nil
}

func (service *Service) GetCopyStatus(_ context.Context, req *connect.Request[aiserverv1.GetCopyStatusRequest]) (*connect.Response[aiserverv1.GetCopyStatusResponse], error) {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if codebaseID := strings.TrimSpace(req.Msg.GetCodebaseId()); codebaseID != "" {
		if err := store.MarkRepositoryCopyComplete(codebaseID, req.Msg.GetCopyTaskHandle()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	completed := aiserverv1.GetCopyStatusResponse_COMPLETED_STATUS_UP_TO_DATE
	logger.Infof("RepositoryService GetCopyStatus completed codebase_id=%s copy_task_handle=%s", strings.TrimSpace(req.Msg.GetCodebaseId()), strings.TrimSpace(req.Msg.GetCopyTaskHandle()))
	return connect.NewResponse(&aiserverv1.GetCopyStatusResponse{
		Phase:           aiserverv1.GetCopyStatusResponse_PHASE_COMPLETED,
		PercentDone:     1,
		CompletedStatus: &completed,
	}), nil
}

func (service *Service) GetUploadLimits(_ context.Context, req *connect.Request[aiserverv1.GetUploadLimitsRequest]) (*connect.Response[aiserverv1.GetUploadLimitsResponse], error) {
	logger.Infof("RepositoryService GetUploadLimits allowed repo=%s", repositoryLogName(req.Msg.GetRepository()))
	return connect.NewResponse(&aiserverv1.GetUploadLimitsResponse{
		SoftLimit: 1_000_000,
		HardLimit: 1_000_000,
	}), nil
}

func (service *Service) GetNumFilesToSend(_ context.Context, req *connect.Request[aiserverv1.GetNumFilesToSendRequest]) (*connect.Response[aiserverv1.GetNumFilesToSendResponse], error) {
	logger.Infof("RepositoryService GetNumFilesToSend none repo=%s", repositoryLogName(req.Msg.GetRepository()))
	return connect.NewResponse(&aiserverv1.GetNumFilesToSendResponse{NumFiles: 0}), nil
}

func (service *Service) GetAvailableChunkingStrategies(_ context.Context, req *connect.Request[aiserverv1.GetAvailableChunkingStrategiesRequest]) (*connect.Response[aiserverv1.GetAvailableChunkingStrategiesResponse], error) {
	logger.Infof("RepositoryService GetAvailableChunkingStrategies default repo=%s", repositoryLogName(req.Msg.GetRepository()))
	return connect.NewResponse(&aiserverv1.GetAvailableChunkingStrategiesResponse{
		ChunkingStrategies: []aiserverv1.ChunkingStrategy{aiserverv1.ChunkingStrategy_CHUNKING_STRATEGY_DEFAULT},
	}), nil
}

func (service *Service) GetHighLevelFolderDescription(_ context.Context, req *connect.Request[aiserverv1.GetHighLevelFolderDescriptionRequest]) (*connect.Response[aiserverv1.GetHighLevelFolderDescriptionResponse], error) {
	logger.Infof("RepositoryService GetHighLevelFolderDescription empty workspace_root=%s top_level_count=%d", strings.TrimSpace(req.Msg.GetWorkspaceRootPath()), len(req.Msg.GetTopLevelRelativeWorkspacePaths()))
	return connect.NewResponse(&aiserverv1.GetHighLevelFolderDescriptionResponse{}), nil
}

func (service *Service) RepositoryStatus(_ context.Context, req *connect.Request[aiserverv1.RepositoryStatusRequest]) (*connect.Response[aiserverv1.RepositoryStatusResponse], error) {
	logger.Infof("RepositoryService RepositoryStatus synced repo=%s", repositoryLogName(req.Msg.GetRepository()))
	return connect.NewResponse(repositoryStatusSyncedResponse()), nil
}

func (service *Service) BatchRepositoryStatus(_ context.Context, req *connect.Request[aiserverv1.BatchRepositoryStatusRequest]) (*connect.Response[aiserverv1.BatchRepositoryStatusResponse], error) {
	requests := req.Msg.GetRequests()
	responses := make([]*aiserverv1.RepositoryStatusResponse, 0, len(requests))
	for _, item := range requests {
		logger.Infof("RepositoryService BatchRepositoryStatus synced repo=%s", repositoryLogName(item.GetRepository()))
		responses = append(responses, repositoryStatusSyncedResponse())
	}
	return connect.NewResponse(&aiserverv1.BatchRepositoryStatusResponse{Responses: responses}), nil
}

func repositoryStatusSyncedResponse() *aiserverv1.RepositoryStatusResponse {
	owner := true
	return &aiserverv1.RepositoryStatusResponse{
		IsOwner: &owner,
		Status: &aiserverv1.RepositoryStatusResponse_Synced_{
			Synced: &aiserverv1.RepositoryStatusResponse_Synced{
				Branch: "local",
				Commit: "local",
			},
		},
	}
}

func (service *Service) ensureRepositoryCodebase(codebaseID string, repo *aiserverv1.RepositoryInfo) error {
	store, err := service.requireCodebaseIndexStore()
	if err != nil {
		return err
	}
	_, err = store.EnsureRepositoryIndexed(RepositoryIndexStateRecord{
		CodebaseID:      strings.TrimSpace(codebaseID),
		Repo:            repositoryRepoName(repo),
		TargetDir:       strings.TrimSpace(repo.GetRelativeWorkspacePath()),
		Status:          repositoryIndexStatusReady,
		LastHandshakeAt: time.Now().UTC(),
	})
	return err
}

func repositoryPartialPathMatchResults(count int) []*aiserverv1.SyncMerkleSubtreeV2Response_PartialPathResult {
	if count <= 0 {
		return nil
	}
	results := make([]*aiserverv1.SyncMerkleSubtreeV2Response_PartialPathResult, 0, count)
	for index := 0; index < count; index++ {
		results = append(results, &aiserverv1.SyncMerkleSubtreeV2Response_PartialPathResult{
			Result: &aiserverv1.SyncMerkleSubtreeV2Response_PartialPathResult_Match{Match: true},
		})
	}
	return results
}

func repositoryCodebaseID(repo *aiserverv1.RepositoryInfo, extras ...string) string {
	parts := repositoryIdentityParts(repo)
	for _, extra := range extras {
		trimmed := strings.TrimSpace(extra)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "cb_local"
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "cb_" + hex.EncodeToString(sum[:])[:24]
}

func repositoryIdentityParts(repo *aiserverv1.RepositoryInfo) []string {
	if repo == nil {
		return nil
	}
	parts := make([]string, 0, 6+len(repo.GetRemoteUrls()))
	for _, value := range []string{
		repo.GetWorkspaceUri(),
		repo.GetRelativeWorkspacePath(),
		repo.GetRepoOwner(),
		repo.GetRepoName(),
	} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	for _, remoteURL := range repo.GetRemoteUrls() {
		trimmed := strings.TrimSpace(remoteURL)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if repo.GetOrthogonalTransformSeed() != 0 {
		parts = append(parts, fmt.Sprintf("seed:%f", repo.GetOrthogonalTransformSeed()))
	}
	return parts
}

func repositoryRepoName(repo *aiserverv1.RepositoryInfo) string {
	if repo == nil {
		return "local"
	}
	if owner := strings.TrimSpace(repo.GetRepoOwner()); owner != "" {
		if name := strings.TrimSpace(repo.GetRepoName()); name != "" {
			return owner + "/" + name
		}
	}
	for _, value := range []string{repo.GetRepoName(), repo.GetRelativeWorkspacePath(), repo.GetWorkspaceUri()} {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return filepath.Base(trimmed)
		}
	}
	return "local"
}

func repositoryLogName(repo *aiserverv1.RepositoryInfo) string {
	if repo == nil {
		return ""
	}
	return strings.TrimSpace(strings.Join(repositoryIdentityParts(repo), "|"))
}
