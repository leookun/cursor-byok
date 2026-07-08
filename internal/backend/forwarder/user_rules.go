package forwarder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/aiserverv1"
	"cursor/internal/logger"
)

const sharedUserRuleExtension = ".md"

type UserRuleRecord struct {
	ID          string
	Title       string
	Filename    string
	FullPath    string
	Knowledge   string
	CreatedAt   string
	IsGenerated bool
	ModifiedAt  time.Time
	ContentHash string
}

type userRuleGroup struct {
	ContentHash string
	Canonical   UserRuleRecord
	Members     []UserRuleRecord
}

type UserRuleStore struct {
	root string
	mu   sync.Mutex
}

func NewUserRuleStore(root string) *UserRuleStore {
	return &UserRuleStore{root: strings.TrimSpace(root)}
}

func (store *UserRuleStore) List() ([]UserRuleRecord, error) {
	if store == nil {
		return nil, nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	groups, err := store.listGroupsLocked()
	if err != nil {
		return nil, err
	}
	records := canonicalRecordsFromGroups(groups)
	sort.SliceStable(records, func(i int, j int) bool {
		if records[i].ModifiedAt.Equal(records[j].ModifiedAt) {
			return records[i].Filename < records[j].Filename
		}
		return records[i].ModifiedAt.After(records[j].ModifiedAt)
	})
	return records, nil
}

func (store *UserRuleStore) Add(knowledge string) (UserRuleRecord, error) {
	if store == nil {
		return UserRuleRecord{}, fmt.Errorf("user rule store is nil")
	}
	if strings.TrimSpace(knowledge) == "" {
		return UserRuleRecord{}, fmt.Errorf("knowledge is required")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	groups, err := store.listGroupsLocked()
	if err != nil {
		return UserRuleRecord{}, err
	}

	contentHash := hashSharedUserRuleContent([]byte(knowledge))
	if groupIndex := indexGroupByHash(groups, contentHash); groupIndex >= 0 {
		if err := store.removeRuleFilesLocked(nonCanonicalMembers(groups[groupIndex].Members)); err != nil {
			return UserRuleRecord{}, err
		}
		return groups[groupIndex].Canonical, nil
	}

	id := uuid.NewString()
	if err := store.writeRuleLocked(id, knowledge); err != nil {
		return UserRuleRecord{}, err
	}
	return store.loadRuleByIDLocked(id)
}

func (store *UserRuleStore) Update(id string, knowledge string) (UserRuleRecord, bool, error) {
	if store == nil {
		return UserRuleRecord{}, false, fmt.Errorf("user rule store is nil")
	}
	if strings.TrimSpace(knowledge) == "" {
		return UserRuleRecord{}, false, fmt.Errorf("knowledge is required")
	}

	normalizedID, err := normalizeUserRuleID(id)
	if err != nil {
		return UserRuleRecord{}, false, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	current, err := store.loadRuleByIDLocked(normalizedID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UserRuleRecord{}, false, nil
		}
		return UserRuleRecord{}, false, err
	}

	groups, err := store.listGroupsLocked()
	if err != nil {
		return UserRuleRecord{}, false, err
	}

	currentGroupIndex := indexGroupByHash(groups, current.ContentHash)
	targetHash := hashSharedUserRuleContent([]byte(knowledge))
	targetGroupIndex := indexGroupByHash(groups, targetHash)

	if targetGroupIndex >= 0 && groups[targetGroupIndex].ContentHash == current.ContentHash {
		if currentGroupIndex >= 0 {
			if err := store.removeRuleFilesLocked(nonCanonicalMembers(groups[currentGroupIndex].Members)); err != nil {
				return UserRuleRecord{}, false, err
			}
			return groups[currentGroupIndex].Canonical, true, nil
		}
		return current, true, nil
	}

	if targetGroupIndex >= 0 {
		if currentGroupIndex >= 0 {
			remainingCurrentMembers := groupMembersExcludingIDs(groups[currentGroupIndex], map[string]struct{}{current.ID: {}})
			if err := store.removeRuleFilesLocked(nonCanonicalMembers(remainingCurrentMembers)); err != nil {
				return UserRuleRecord{}, false, err
			}
		}
		if err := store.removeRuleFilesLocked(nonCanonicalMembers(groups[targetGroupIndex].Members)); err != nil {
			return UserRuleRecord{}, false, err
		}
		if err := store.removeRuleFilesLocked([]UserRuleRecord{current}); err != nil {
			return UserRuleRecord{}, false, err
		}
		return groups[targetGroupIndex].Canonical, true, nil
	}

	if err := store.writeRuleLocked(current.ID, knowledge); err != nil {
		return UserRuleRecord{}, false, err
	}
	if currentGroupIndex >= 0 {
		remainingCurrentMembers := groupMembersExcludingIDs(groups[currentGroupIndex], map[string]struct{}{current.ID: {}})
		if err := store.removeRuleFilesLocked(nonCanonicalMembers(remainingCurrentMembers)); err != nil {
			return UserRuleRecord{}, false, err
		}
	}
	record, err := store.loadRuleByIDLocked(current.ID)
	if err != nil {
		return UserRuleRecord{}, false, err
	}
	return record, true, nil
}

func (store *UserRuleStore) Remove(id string) error {
	if store == nil {
		return nil
	}

	normalizedID, err := normalizeUserRuleID(id)
	if err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	record, err := store.loadRuleByIDLocked(normalizedID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	groups, err := store.listGroupsLocked()
	if err != nil {
		return err
	}

	groupIndex := indexGroupByHash(groups, record.ContentHash)
	if groupIndex < 0 {
		return store.removeRuleFilesLocked([]UserRuleRecord{record})
	}
	return store.removeRuleFilesLocked(groups[groupIndex].Members)
}

func (store *UserRuleStore) BuildSystemPromptSection() (string, int, int, error) {
	if store == nil {
		return "", 0, 0, nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	groups, err := store.listGroupsLocked()
	if err != nil {
		return "", 0, 0, err
	}
	if len(groups) == 0 {
		return "", 0, 0, nil
	}

	totalFiles := totalRuleFileCount(groups)
	records := canonicalRecordsFromGroups(groups)
	sort.SliceStable(records, func(i int, j int) bool {
		return records[i].Filename < records[j].Filename
	})

	lines := []string{
		`<shared_user_rules description="These shared local rules are loaded from the backend configuration directory and apply to every local conversation. Follow them when relevant.">`,
	}
	visibleCount := 0
	for _, record := range records {
		if strings.TrimSpace(record.Knowledge) == "" {
			continue
		}
		lines = append(lines,
			fmt.Sprintf(`<rule file="%s">`, escapeSharedRulePromptText(record.Filename)),
			escapeSharedRulePromptText(record.Knowledge),
			"</rule>",
		)
		visibleCount++
	}
	if visibleCount == 0 {
		return "", totalFiles, 0, nil
	}
	lines = append(lines, "</shared_user_rules>")
	return strings.Join(lines, "\n"), totalFiles, visibleCount, nil
}

func (store *UserRuleStore) listGroupsLocked() ([]userRuleGroup, error) {
	records, err := store.scanRuleFilesLocked()
	if err != nil {
		return nil, err
	}
	return buildUserRuleGroups(records), nil
}

func (store *UserRuleStore) scanRuleFilesLocked() ([]UserRuleRecord, error) {
	if err := store.ensureRootLocked(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(store.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read shared user rules directory: %w", err)
	}

	records := make([]UserRuleRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != sharedUserRuleExtension {
			continue
		}
		record, err := store.readRuleFileLocked(filepath.Join(store.root, entry.Name()))
		if err != nil {
			logger.Infof("跳过不可用的共享 user rule 文件 path=%s err=%v", filepath.Join(store.root, entry.Name()), err)
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func (store *UserRuleStore) loadRuleByIDLocked(id string) (UserRuleRecord, error) {
	return store.readRuleFileLocked(store.rulePathLocked(id))
}

func (store *UserRuleStore) readRuleFileLocked(path string) (UserRuleRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return UserRuleRecord{}, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return UserRuleRecord{}, fmt.Errorf("shared user rule file is empty")
	}

	info, err := os.Stat(path)
	if err != nil {
		return UserRuleRecord{}, err
	}

	filename := filepath.Base(path)
	id, err := normalizeUserRuleID(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if err != nil {
		return UserRuleRecord{}, err
	}

	modifiedAt := info.ModTime().UTC()
	return UserRuleRecord{
		ID:          id,
		Title:       id,
		Filename:    filename,
		FullPath:    path,
		Knowledge:   string(data),
		CreatedAt:   modifiedAt.Format(time.RFC3339Nano),
		IsGenerated: false,
		ModifiedAt:  modifiedAt,
		ContentHash: hashSharedUserRuleContent(data),
	}, nil
}

func (store *UserRuleStore) writeRuleLocked(id string, knowledge string) error {
	normalizedID, err := normalizeUserRuleID(id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(knowledge) == "" {
		return fmt.Errorf("knowledge is required")
	}
	if err := store.ensureRootLocked(); err != nil {
		return err
	}

	path := store.rulePathLocked(normalizedID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shared user rules directory: %w", err)
	}

	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, []byte(knowledge), 0o644); err != nil {
		return fmt.Errorf("write temp shared user rule: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename shared user rule file: %w", err)
	}
	return nil
}

func (store *UserRuleStore) removeRuleFilesLocked(records []UserRuleRecord) error {
	for _, record := range records {
		path := strings.TrimSpace(record.FullPath)
		if path == "" {
			path = store.rulePathLocked(record.ID)
		}
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove shared user rule %q: %w", record.ID, err)
		}
	}
	return nil
}

func (store *UserRuleStore) ensureRootLocked() error {
	if strings.TrimSpace(store.root) == "" {
		return fmt.Errorf("user rules root is empty")
	}
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return fmt.Errorf("create user rules root: %w", err)
	}
	return nil
}

func (store *UserRuleStore) rulePathLocked(id string) string {
	if store == nil || strings.TrimSpace(store.root) == "" {
		return ""
	}
	return filepath.Join(store.root, id+sharedUserRuleExtension)
}

func buildUserRuleGroups(records []UserRuleRecord) []userRuleGroup {
	if len(records) == 0 {
		return nil
	}

	membersByHash := make(map[string][]UserRuleRecord)
	for _, record := range records {
		membersByHash[record.ContentHash] = append(membersByHash[record.ContentHash], record)
	}

	groups := make([]userRuleGroup, 0, len(membersByHash))
	for contentHash, members := range membersByHash {
		sort.SliceStable(members, func(i int, j int) bool {
			return members[i].Filename < members[j].Filename
		})
		groups = append(groups, userRuleGroup{
			ContentHash: contentHash,
			Canonical:   members[0],
			Members:     members,
		})
	}

	sort.SliceStable(groups, func(i int, j int) bool {
		return groups[i].Canonical.Filename < groups[j].Canonical.Filename
	})
	return groups
}

func canonicalRecordsFromGroups(groups []userRuleGroup) []UserRuleRecord {
	if len(groups) == 0 {
		return nil
	}
	records := make([]UserRuleRecord, 0, len(groups))
	for _, group := range groups {
		records = append(records, group.Canonical)
	}
	return records
}

func totalRuleFileCount(groups []userRuleGroup) int {
	total := 0
	for _, group := range groups {
		total += len(group.Members)
	}
	return total
}

func indexGroupByHash(groups []userRuleGroup, contentHash string) int {
	for index := range groups {
		if groups[index].ContentHash == contentHash {
			return index
		}
	}
	return -1
}

func nonCanonicalMembers(records []UserRuleRecord) []UserRuleRecord {
	if len(records) <= 1 {
		return nil
	}
	output := make([]UserRuleRecord, 0, len(records)-1)
	output = append(output, records[1:]...)
	return output
}

func groupMembersExcludingIDs(group userRuleGroup, excludedIDs map[string]struct{}) []UserRuleRecord {
	if len(group.Members) == 0 {
		return nil
	}
	filtered := make([]UserRuleRecord, 0, len(group.Members))
	for _, record := range group.Members {
		if _, excluded := excludedIDs[record.ID]; excluded {
			continue
		}
		filtered = append(filtered, record)
	}
	return filtered
}

func normalizeUserRuleID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	id = strings.TrimSuffix(id, sharedUserRuleExtension)
	switch {
	case id == "":
		return "", fmt.Errorf("user rule id is required")
	case strings.Contains(id, "/"), strings.Contains(id, "\\"), strings.Contains(id, ".."):
		return "", fmt.Errorf("invalid user rule id %q", raw)
	default:
		return id, nil
	}
}

func hashSharedUserRuleContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func escapeSharedRulePromptText(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(value)
}

func (service *Service) KnowledgeBaseAdd(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseAddRequest]) (*connect.Response[aiserverv1.KnowledgeBaseAddResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if strings.TrimSpace(req.Msg.GetKnowledge()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("knowledge is required"))
	}

	record, err := service.rules.Add(req.Msg.GetKnowledge())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if service.docsIndexStore != nil {
		if _, err := service.docsIndexStore.Upsert(DocsIndexRecord{
			ID:         record.ID,
			Identifier: record.ID,
			Title:      firstNonEmptyDocs(req.Msg.GetTitle(), record.Title, record.ID),
			URL:        firstNonEmptyDocs(docsIndexURLCandidate(req.Msg.GetKnowledge()), docsIndexURLCandidate(req.Msg.GetTitle())),
			Content:    req.Msg.GetKnowledge(),
			GitOrigin:  req.Msg.GetGitOrigin(),
			Source:     docsIndexSourceLocal,
		}); err != nil {
			logger.Errorf("docs index sync failed operation=add id=%s err=%v", record.ID, err)
		}
	}
	return connect.NewResponse(&aiserverv1.KnowledgeBaseAddResponse{
		Success: true,
		Id:      record.ID,
	}), nil
}

func (service *Service) KnowledgeBaseList(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseListRequest]) (*connect.Response[aiserverv1.KnowledgeBaseListResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}

	records, err := service.rules.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if limit := req.Msg.GetLimit(); limit > 0 && len(records) > int(limit) {
		records = records[:limit]
	}

	items := make([]*aiserverv1.KnowledgeBaseListResponse_Item, 0, len(records))
	for _, record := range records {
		items = append(items, &aiserverv1.KnowledgeBaseListResponse_Item{
			Id:          record.ID,
			Knowledge:   record.Knowledge,
			Title:       record.Title,
			CreatedAt:   record.CreatedAt,
			IsGenerated: false,
		})
	}
	return connect.NewResponse(&aiserverv1.KnowledgeBaseListResponse{
		Success:    true,
		AllResults: items,
	}), nil
}

func (service *Service) KnowledgeBaseUpdate(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseUpdateRequest]) (*connect.Response[aiserverv1.KnowledgeBaseUpdateResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if _, err := normalizeUserRuleID(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(req.Msg.GetKnowledge()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("knowledge is required"))
	}

	_, exists, err := service.rules.Update(req.Msg.GetId(), req.Msg.GetKnowledge())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user rule %q not found", strings.TrimSpace(req.Msg.GetId())))
	}
	if service.docsIndexStore != nil {
		if _, err := service.docsIndexStore.Upsert(DocsIndexRecord{
			ID:         strings.TrimSpace(req.Msg.GetId()),
			Identifier: strings.TrimSpace(req.Msg.GetId()),
			Title:      firstNonEmptyDocs(req.Msg.GetTitle(), strings.TrimSpace(req.Msg.GetId())),
			URL:        firstNonEmptyDocs(docsIndexURLCandidate(req.Msg.GetKnowledge()), docsIndexURLCandidate(req.Msg.GetTitle())),
			Content:    req.Msg.GetKnowledge(),
			Source:     docsIndexSourceLocal,
		}); err != nil {
			logger.Errorf("docs index sync failed operation=update id=%s err=%v", strings.TrimSpace(req.Msg.GetId()), err)
		}
	}
	return connect.NewResponse(&aiserverv1.KnowledgeBaseUpdateResponse{Success: true}), nil
}

func (service *Service) KnowledgeBaseRemove(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseRemoveRequest]) (*connect.Response[aiserverv1.KnowledgeBaseRemoveResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if _, err := normalizeUserRuleID(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := service.rules.Remove(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if service.docsIndexStore != nil {
		if err := service.docsIndexStore.Remove(req.Msg.GetId()); err != nil {
			logger.Errorf("docs index sync failed operation=remove id=%s err=%v", strings.TrimSpace(req.Msg.GetId()), err)
		}
	}
	return connect.NewResponse(&aiserverv1.KnowledgeBaseRemoveResponse{Success: true}), nil
}
