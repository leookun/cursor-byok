package forwarder

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
	"cursor/internal/logger"
)

const (
	UploadServiceUploadDocumentationProcedure = "/aiserver.v1.UploadService/UploadDocumentation"
	UploadServiceGetDocProcedure              = "/aiserver.v1.UploadService/GetDoc"
	UploadServiceGetPagesProcedure            = "/aiserver.v1.UploadService/GetPages"
	UploadServiceUploadedStatusProcedure      = "/aiserver.v1.UploadService/UploadedStatus"
)

func newUploadServiceHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(
		UploadServiceUploadDocumentationProcedure,
		connect.NewUnaryHandler(UploadServiceUploadDocumentationProcedure, service.UploadDocumentation),
	)
	mux.Handle(
		UploadServiceGetDocProcedure,
		connect.NewUnaryHandler(UploadServiceGetDocProcedure, service.GetDoc),
	)
	mux.Handle(
		UploadServiceGetPagesProcedure,
		connect.NewUnaryHandler(UploadServiceGetPagesProcedure, service.GetPages),
	)
	mux.Handle(
		UploadServiceUploadedStatusProcedure,
		connect.NewUnaryHandler(UploadServiceUploadedStatusProcedure, service.UploadedStatus),
	)
	mux.Handle("/", http.NotFoundHandler())
	return mux
}

func (service *Service) UploadDocumentation(_ context.Context, req *connect.Request[aiserverv1.UploadDocumentationRequest]) (*connect.Response[aiserverv1.UploadResponse], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	identifier := strings.TrimSpace(req.Msg.GetDocIdentifier())
	if identifier == "" {
		identifier = stableDocsIdentifier("upload-documentation", time.Now().UTC().Format(time.RFC3339Nano))
	}
	record, err := store.Upsert(uploadedDocsIndexRecord(identifier))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	logger.Infof("UploadService UploadDocumentation completed doc_identifier=%s", record.Identifier)
	return connect.NewResponse(&aiserverv1.UploadResponse{
		Status:        aiserverv1.UploadResponse_STATUS_SUCCESS,
		Progress:      1,
		UploadedPages: uploadedDocPages(record),
		DocUuid:       uploadDocUUID(record),
	}), nil
}

func (service *Service) GetDoc(_ context.Context, req *connect.Request[aiserverv1.GetDocRequest]) (*connect.Response[aiserverv1.ProtoDoc], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	identifier := strings.TrimSpace(req.Msg.GetDocIdentifier())
	if identifier == "" {
		return connect.NewResponse(uploadedDocNotFound(identifier)), nil
	}
	record, ok, err := store.Get(identifier)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		logger.Infof("UploadService GetDoc not_found doc_identifier=%s", identifier)
		return connect.NewResponse(uploadedDocNotFound(identifier)), nil
	}
	logger.Infof("UploadService GetDoc completed doc_identifier=%s", record.Identifier)
	return connect.NewResponse(uploadedDocsProtoDoc(record)), nil
}

func (service *Service) GetPages(_ context.Context, req *connect.Request[aiserverv1.GetPagesRequest]) (*connect.Response[aiserverv1.Pages], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	identifier := strings.TrimSpace(req.Msg.GetDocIdentifier())
	if identifier == "" {
		return connect.NewResponse(&aiserverv1.Pages{}), nil
	}
	record, ok, err := store.Get(identifier)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		logger.Infof("UploadService GetPages empty doc_identifier=%s", identifier)
		return connect.NewResponse(&aiserverv1.Pages{}), nil
	}
	pages, pageURLs := uploadedDocsPagesResponse(record)
	logger.Infof("UploadService GetPages completed doc_identifier=%s pages=%d", record.Identifier, len(pages))
	return connect.NewResponse(&aiserverv1.Pages{Pages: pages, PageUrls: pageURLs}), nil
}

func (service *Service) UploadedStatus(_ context.Context, req *connect.Request[aiserverv1.UploadedStatusRequest]) (*connect.Response[aiserverv1.UploadedStatus], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	identifier := strings.TrimSpace(req.Msg.GetDocIdentifier())
	if identifier == "" {
		return connect.NewResponse(&aiserverv1.UploadedStatus{Status: aiserverv1.UploadedStatus_STATUS_NOT_FOUND}), nil
	}
	record, ok, err := store.Get(identifier)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewResponse(&aiserverv1.UploadedStatus{Status: aiserverv1.UploadedStatus_STATUS_NOT_FOUND}), nil
	}
	return connect.NewResponse(&aiserverv1.UploadedStatus{
		Status:        aiserverv1.UploadedStatus_STATUS_SUCCEEDED,
		UploadedPages: uploadedDocPages(record),
	}), nil
}

func uploadedDocsIndexRecord(identifier string) DocsIndexRecord {
	url := docsIndexURLCandidate(identifier)
	title := identifier
	if url != "" {
		title = docsTitleFromURL(url)
	}
	return DocsIndexRecord{
		ID:         identifier,
		Identifier: identifier,
		Title:      title,
		URL:        url,
		Status:     docsIndexStatusIndexed,
		Source:     docsIndexSourceLocal,
	}
}

func uploadedDocsProtoDoc(record DocsIndexRecord) *aiserverv1.ProtoDoc {
	createdAt := docsIndexTimeString(record.CreatedAt)
	updatedAt := docsIndexTimeString(record.UpdatedAt)
	lastUploadedAt := docsIndexTimeString(record.LastIndexAt)
	if lastUploadedAt == "" {
		lastUploadedAt = updatedAt
	}
	return &aiserverv1.ProtoDoc{
		Uuid:           uploadDocUUID(record),
		DocIdentifier:  record.Identifier,
		DocName:        record.Title,
		DocUrlRoot:     record.URL,
		DocUrlPrefix:   record.URL,
		CreatedAt:      createdAt,
		UpdatedAt:      updatedAt,
		LastUploadedAt: lastUploadedAt,
		UploadStatus: &aiserverv1.UploadedStatus{
			Status:        aiserverv1.UploadedStatus_STATUS_SUCCEEDED,
			UploadedPages: uploadedDocPages(record),
		},
		Pages: uploadedDocProtoPages(record),
	}
}

func uploadedDocNotFound(identifier string) *aiserverv1.ProtoDoc {
	return &aiserverv1.ProtoDoc{
		DocIdentifier: strings.TrimSpace(identifier),
		UploadStatus:  &aiserverv1.UploadedStatus{Status: aiserverv1.UploadedStatus_STATUS_NOT_FOUND},
	}
}

func uploadedDocProtoPages(record DocsIndexRecord) []*aiserverv1.ProtoDocPage {
	pages, pageURLs := uploadedDocsPagesResponse(record)
	items := make([]*aiserverv1.ProtoDocPage, 0, len(pages))
	for i, page := range pages {
		url := ""
		if i < len(pageURLs) {
			url = pageURLs[i]
		}
		items = append(items, &aiserverv1.ProtoDocPage{Title: page, Url: url})
	}
	return items
}

func uploadedDocsPagesResponse(record DocsIndexRecord) ([]string, []string) {
	title := strings.TrimSpace(record.Title)
	url := strings.TrimSpace(record.URL)
	if title == "" || url == "" {
		return nil, nil
	}
	return []string{title}, []string{url}
}

func uploadedDocPages(record DocsIndexRecord) []string {
	pages, _ := uploadedDocsPagesResponse(record)
	return pages
}

func uploadDocUUID(record DocsIndexRecord) string {
	identifier := strings.TrimSpace(record.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(record.ID)
	}
	if identifier == "" {
		identifier = fmt.Sprintf("doc-%d", time.Now().UnixNano())
	}
	return stableDocsIdentifier("upload-doc-uuid", identifier)
}

func docsIndexTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func docsTitleFromURL(value string) string {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "https://")
	trimmed = strings.TrimPrefix(trimmed, "http://")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return value
	}
	return trimmed
}
