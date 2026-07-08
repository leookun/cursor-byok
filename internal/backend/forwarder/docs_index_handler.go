package forwarder

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
)

func (service *Service) AvailableDocs(_ context.Context, req *connect.Request[aiserverv1.AvailableDocsRequest]) (*connect.Response[aiserverv1.AvailableDocsResponse], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	records, err := store.List("", 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	records = filterDocsIndexRecords(records, req.Msg)
	records, err = appendMissingAdditionalDocs(store, records, req.Msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	docs := make([]*aiserverv1.DocumentationInfo, 0, len(records))
	for _, record := range records {
		docs = append(docs, docsIndexDocumentationInfo(record))
	}
	return connect.NewResponse(&aiserverv1.AvailableDocsResponse{Docs: docs}), nil
}

func (service *Service) DocumentationQuery(_ context.Context, req *connect.Request[aiserverv1.DocumentationQueryRequest]) (*connect.Response[aiserverv1.DocumentationQueryResponse], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	identifier := strings.TrimSpace(req.Msg.GetDocIdentifier())
	if identifier == "" {
		return connect.NewResponse(&aiserverv1.DocumentationQueryResponse{
			Status: aiserverv1.DocumentationQueryResponse_STATUS_NOT_FOUND,
		}), nil
	}
	record, ok, err := store.Get(identifier)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !ok {
		return connect.NewResponse(&aiserverv1.DocumentationQueryResponse{
			DocIdentifier: identifier,
			Status:        aiserverv1.DocumentationQueryResponse_STATUS_NOT_FOUND,
		}), nil
	}
	return connect.NewResponse(&aiserverv1.DocumentationQueryResponse{
		DocIdentifier: record.Identifier,
		DocName:       record.Title,
		DocChunks:     docsIndexChunks(record, req.Msg.GetTopK()),
		Status:        aiserverv1.DocumentationQueryResponse_STATUS_SUCCESS,
	}), nil
}

func (service *Service) FetchRelevantKnowledgeForConversation(_ context.Context, req *connect.Request[aiserverv1.FetchRelevantKnowledgeForConversationRequest]) (*connect.Response[aiserverv1.FetchRelevantKnowledgeForConversationResponse], error) {
	store, err := service.requireDocsIndexStore()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	gitOrigin := strings.TrimSpace(req.Msg.GetGitOrigin())
	if gitOrigin == "" {
		return connect.NewResponse(&aiserverv1.FetchRelevantKnowledgeForConversationResponse{}), nil
	}
	records, err := store.List(gitOrigin, req.Msg.GetLimit())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*aiserverv1.ConversationMessage_KnowledgeItem, 0, len(records))
	for _, record := range records {
		items = append(items, &aiserverv1.ConversationMessage_KnowledgeItem{
			Title:       record.Title,
			Knowledge:   docsIndexKnowledgeText(record),
			KnowledgeId: record.Identifier,
			IsGenerated: false,
		})
	}
	return connect.NewResponse(&aiserverv1.FetchRelevantKnowledgeForConversationResponse{KnowledgeItems: items}), nil
}

func (service *Service) requireDocsIndexStore() (*DocsIndexStore, error) {
	if service == nil || service.docsIndexStore == nil {
		return nil, fmt.Errorf("docs index store is not initialized")
	}
	return service.docsIndexStore, nil
}

func filterDocsIndexRecords(records []DocsIndexRecord, req *aiserverv1.AvailableDocsRequest) []DocsIndexRecord {
	if req == nil || req.GetGetAll() || (req.GetPartialUrl() == "" && req.GetPartialDocName() == "" && len(req.GetAdditionalDocIdentifiers()) == 0) {
		return records
	}
	partialURL := strings.ToLower(strings.TrimSpace(req.GetPartialUrl()))
	partialName := strings.ToLower(strings.TrimSpace(req.GetPartialDocName()))
	allowed := make(map[string]struct{})
	for _, identifier := range req.GetAdditionalDocIdentifiers() {
		trimmed := strings.TrimSpace(identifier)
		if trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}
	filtered := make([]DocsIndexRecord, 0, len(records))
	for _, record := range records {
		_, explicitlyAllowed := allowed[record.Identifier]
		matchesURL := partialURL != "" && strings.Contains(strings.ToLower(record.URL), partialURL)
		matchesName := partialName != "" && strings.Contains(strings.ToLower(record.Title), partialName)
		if explicitlyAllowed || matchesURL || matchesName {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func appendMissingAdditionalDocs(store *DocsIndexStore, records []DocsIndexRecord, req *aiserverv1.AvailableDocsRequest) ([]DocsIndexRecord, error) {
	if req == nil {
		return records, nil
	}
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		seen[record.Identifier] = struct{}{}
	}
	for _, identifier := range req.GetAdditionalDocIdentifiers() {
		trimmed := strings.TrimSpace(identifier)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		record := DocsIndexRecord{
			ID:         trimmed,
			Identifier: trimmed,
			Title:      trimmed,
			Status:     docsIndexStatusIndexed,
			Source:     docsIndexSourceAdditional,
		}
		if store != nil {
			persisted, err := store.Upsert(record)
			if err != nil {
				return nil, err
			}
			record = persisted
		}
		records = append(records, record)
		seen[trimmed] = struct{}{}
	}
	return records, nil
}

func docsIndexDocumentationInfo(record DocsIndexRecord) *aiserverv1.DocumentationInfo {
	return &aiserverv1.DocumentationInfo{
		DocIdentifier: record.Identifier,
		Metadata: &aiserverv1.DocumentationMetadata{
			PrefixUrl:     record.URL,
			DocName:       record.Title,
			TruePrefixUrl: record.URL,
			Public:        false,
		},
	}
}

func docsIndexChunks(record DocsIndexRecord, topK uint32) []*aiserverv1.DocumentationChunk {
	content := strings.TrimSpace(record.Content)
	if content == "" {
		return nil
	}
	if topK == 0 {
		topK = 1
	}
	return []*aiserverv1.DocumentationChunk{
		{
			DocName:            record.Title,
			PageUrl:            record.URL,
			DocumentationChunk: content,
			Score:              1,
			PageTitle:          record.Title,
		},
	}
}

func docsIndexKnowledgeText(record DocsIndexRecord) string {
	parts := make([]string, 0, 3)
	if strings.TrimSpace(record.URL) != "" {
		parts = append(parts, "URL: "+strings.TrimSpace(record.URL))
	}
	if strings.TrimSpace(record.Title) != "" {
		parts = append(parts, "Title: "+strings.TrimSpace(record.Title))
	}
	if strings.TrimSpace(record.Content) != "" {
		parts = append(parts, strings.TrimSpace(record.Content))
	}
	return strings.Join(parts, "\n")
}

func docsIndexURLCandidate(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		fields := strings.Fields(trimmed)
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}
