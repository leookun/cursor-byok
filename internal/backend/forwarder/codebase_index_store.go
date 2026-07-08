package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	codebaseIndexStatusReady              = "ready"
	codebaseIndexFileStage                = "indexed"
	codebaseIndexEventKindRegister        = "register"
	codebaseIndexSubscriberSignalCapacity = 1
	codebaseIndexMaxPersistedEvents       = 2000
	codebaseIndexSourceRepositoryService  = "repository_service"
	repositoryIndexStatusReady            = "ready"
	repositoryIndexSyncStatusCompleted    = "sync_completed"
	repositoryIndexCopyStatusCompleted    = "copy_completed"
)

type CodebaseSearchBackend interface {
	EnsureIndexed(record CodebaseIndexRecord) (CodebaseIndexRecord, error)
	RegisterFile(indexID string, file CodebaseIndexFileRecord) (CodebaseIndexFileRecord, error)
	ListFiles(indexID string) ([]CodebaseIndexFileRecord, error)
}

type CodebaseIndexStore struct {
	mu          sync.Mutex
	root        string
	path        string
	loaded      bool
	nextEventID int64
	state       codebaseIndexState
	subscribers map[string]map[string]chan struct{}
}

type codebaseIndexState struct {
	SchemaVersion int                            `json:"schema_version"`
	Indexes       map[string]CodebaseIndexRecord `json:"indexes"`
	Events        []CodebaseIndexEventRecord     `json:"events,omitempty"`
}

type CodebaseIndexRecord struct {
	IndexID           string                             `json:"index_id"`
	Repo              string                             `json:"repo,omitempty"`
	TargetDir         string                             `json:"target_dir,omitempty"`
	Files             []string                           `json:"files,omitempty"`
	Status            string                             `json:"status"`
	Source            string                             `json:"source,omitempty"`
	DependenciesReady bool                               `json:"dependencies_ready,omitempty"`
	TopoSortReady     bool                               `json:"topo_sort_ready,omitempty"`
	RepositoryState   *RepositoryIndexStateRecord        `json:"repository_state,omitempty"`
	CreatedAt         time.Time                          `json:"created_at"`
	UpdatedAt         time.Time                          `json:"updated_at"`
	RegisteredFiles   map[string]CodebaseIndexFileRecord `json:"registered_files,omitempty"`
}

type RepositoryIndexStateRecord struct {
	CodebaseID       string    `json:"codebase_id"`
	Repo             string    `json:"repo,omitempty"`
	TargetDir        string    `json:"target_dir,omitempty"`
	Status           string    `json:"status"`
	CopyStatus       string    `json:"copy_status,omitempty"`
	SyncStatus       string    `json:"sync_status,omitempty"`
	CopyTaskHandle   string    `json:"copy_task_handle,omitempty"`
	PathKeyHash      string    `json:"path_key_hash,omitempty"`
	Source           string    `json:"source"`
	LastHandshakeAt  time.Time `json:"last_handshake_at,omitempty"`
	LastSyncAt       time.Time `json:"last_sync_at,omitempty"`
	LastCopyStatusAt time.Time `json:"last_copy_status_at,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type CodebaseIndexFileRecord struct {
	WorkspaceRelativePath string    `json:"workspace_relative_path"`
	ContentHash           string    `json:"content_hash,omitempty"`
	Stage                 string    `json:"stage"`
	Order                 int32     `json:"order,omitempty"`
	NodeCount             int32     `json:"node_count,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type CodebaseIndexEventRecord struct {
	ID      int64                   `json:"id"`
	IndexID string                  `json:"index_id"`
	Kind    string                  `json:"kind"`
	ReqUUID string                  `json:"req_uuid,omitempty"`
	File    CodebaseIndexFileRecord `json:"file,omitempty"`
	At      time.Time               `json:"at"`
}

type CodebaseIndexSubscription struct {
	IndexID      string
	SubscriberID string
	Signal       <-chan struct{}
}

func NewCodebaseIndexStore(root string) *CodebaseIndexStore {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		trimmed = "codebase-index"
	}
	return &CodebaseIndexStore{
		root:        trimmed,
		path:        filepath.Join(trimmed, "index.json"),
		subscribers: make(map[string]map[string]chan struct{}),
		state: codebaseIndexState{
			SchemaVersion: 1,
			Indexes:       make(map[string]CodebaseIndexRecord),
		},
	}
}

func (store *CodebaseIndexStore) EnsureIndexed(record CodebaseIndexRecord) (CodebaseIndexRecord, error) {
	if store == nil {
		return CodebaseIndexRecord{}, fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return CodebaseIndexRecord{}, err
	}
	now := time.Now().UTC()
	indexID := strings.TrimSpace(record.IndexID)
	if indexID == "" {
		indexID = stableCodebaseIndexID(record.Repo, record.TargetDir, record.Files)
	}
	existing, ok := store.state.Indexes[indexID]
	if ok {
		existing.Repo = firstNonEmptyCodebaseIndex(record.Repo, existing.Repo)
		existing.TargetDir = firstNonEmptyCodebaseIndex(record.TargetDir, existing.TargetDir)
		if len(record.Files) > 0 {
			existing.Files = append([]string(nil), record.Files...)
		}
		if strings.TrimSpace(record.Source) != "" {
			existing.Source = strings.TrimSpace(record.Source)
		}
		if record.RepositoryState != nil {
			existing.RepositoryState = mergeRepositoryIndexState(existing.RepositoryState, record.RepositoryState, now)
		}
		existing.Status = codebaseIndexStatusReady
		existing.UpdatedAt = now
		if existing.RegisteredFiles == nil {
			existing.RegisteredFiles = make(map[string]CodebaseIndexFileRecord)
		}
		store.state.Indexes[indexID] = existing
		return existing, store.saveLocked()
	}
	record.IndexID = indexID
	record.Status = codebaseIndexStatusReady
	record.Source = strings.TrimSpace(record.Source)
	record.CreatedAt = now
	record.UpdatedAt = now
	if record.RepositoryState != nil {
		record.RepositoryState = mergeRepositoryIndexState(nil, record.RepositoryState, now)
	}
	record.Files = append([]string(nil), record.Files...)
	record.RegisteredFiles = make(map[string]CodebaseIndexFileRecord)
	store.state.Indexes[indexID] = record
	return record, store.saveLocked()
}

func (store *CodebaseIndexStore) EnsureRepositoryIndexed(state RepositoryIndexStateRecord) (CodebaseIndexRecord, error) {
	if store == nil {
		return CodebaseIndexRecord{}, fmt.Errorf("codebase index store is nil")
	}
	codebaseID := strings.TrimSpace(state.CodebaseID)
	if codebaseID == "" {
		return CodebaseIndexRecord{}, fmt.Errorf("codebase_id is required")
	}
	state.CodebaseID = codebaseID
	state.Source = codebaseIndexSourceRepositoryService
	if strings.TrimSpace(state.Status) == "" {
		state.Status = repositoryIndexStatusReady
	}
	return store.EnsureIndexed(CodebaseIndexRecord{
		IndexID:         codebaseID,
		Repo:            strings.TrimSpace(state.Repo),
		TargetDir:       strings.TrimSpace(state.TargetDir),
		Source:          codebaseIndexSourceRepositoryService,
		RepositoryState: &state,
	})
}

func (store *CodebaseIndexStore) MarkRepositorySyncComplete(codebaseID string, pathKeyHash string) error {
	_, err := store.EnsureRepositoryIndexed(RepositoryIndexStateRecord{
		CodebaseID:  strings.TrimSpace(codebaseID),
		Status:      repositoryIndexStatusReady,
		SyncStatus:  repositoryIndexSyncStatusCompleted,
		PathKeyHash: strings.TrimSpace(pathKeyHash),
		LastSyncAt:  time.Now().UTC(),
	})
	return err
}

func (store *CodebaseIndexStore) MarkRepositoryCopyComplete(codebaseID string, copyTaskHandle string) error {
	_, err := store.EnsureRepositoryIndexed(RepositoryIndexStateRecord{
		CodebaseID:       strings.TrimSpace(codebaseID),
		Status:           repositoryIndexStatusReady,
		CopyStatus:       repositoryIndexCopyStatusCompleted,
		CopyTaskHandle:   strings.TrimSpace(copyTaskHandle),
		LastCopyStatusAt: time.Now().UTC(),
	})
	return err
}

func (store *CodebaseIndexStore) RegisterFile(indexID string, file CodebaseIndexFileRecord) (CodebaseIndexFileRecord, error) {
	registered, _, err := store.RegisterFileWithRequest(indexID, "", file)
	return registered, err
}

func (store *CodebaseIndexStore) RegisterFileWithRequest(indexID string, reqUUID string, file CodebaseIndexFileRecord) (CodebaseIndexFileRecord, CodebaseIndexEventRecord, error) {
	if store == nil {
		return CodebaseIndexFileRecord{}, CodebaseIndexEventRecord{}, fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return CodebaseIndexFileRecord{}, CodebaseIndexEventRecord{}, err
	}
	trimmedIndexID := strings.TrimSpace(indexID)
	if trimmedIndexID == "" {
		return CodebaseIndexFileRecord{}, CodebaseIndexEventRecord{}, fmt.Errorf("index_id is required")
	}
	record, ok := store.state.Indexes[trimmedIndexID]
	if !ok {
		now := time.Now().UTC()
		record = CodebaseIndexRecord{
			IndexID:         trimmedIndexID,
			Status:          codebaseIndexStatusReady,
			CreatedAt:       now,
			UpdatedAt:       now,
			RegisteredFiles: make(map[string]CodebaseIndexFileRecord),
		}
	}
	if record.RegisteredFiles == nil {
		record.RegisteredFiles = make(map[string]CodebaseIndexFileRecord)
	}
	path := strings.TrimSpace(file.WorkspaceRelativePath)
	if path == "" {
		return CodebaseIndexFileRecord{}, CodebaseIndexEventRecord{}, fmt.Errorf("workspace_relative_path is required")
	}
	now := time.Now().UTC()
	current, exists := record.RegisteredFiles[path]
	if exists {
		file.CreatedAt = current.CreatedAt
	} else {
		file.CreatedAt = now
	}
	file.WorkspaceRelativePath = path
	if strings.TrimSpace(file.Stage) == "" {
		file.Stage = codebaseIndexFileStage
	}
	if file.Order == 0 {
		if exists && current.Order != 0 {
			file.Order = current.Order
		} else {
			file.Order = int32(len(record.RegisteredFiles) + 1)
		}
	}
	file.UpdatedAt = now
	record.RegisteredFiles[path] = file
	record.Status = codebaseIndexStatusReady
	record.UpdatedAt = now
	store.state.Indexes[trimmedIndexID] = record
	event := CodebaseIndexEventRecord{
		ID:      store.nextCodebaseIndexEventIDLocked(),
		IndexID: trimmedIndexID,
		Kind:    codebaseIndexEventKindRegister,
		ReqUUID: strings.TrimSpace(reqUUID),
		File:    file,
		At:      now,
	}
	store.state.Events = append(store.state.Events, event)
	store.compactEventsLocked()
	if err := store.saveLocked(); err != nil {
		return CodebaseIndexFileRecord{}, CodebaseIndexEventRecord{}, err
	}
	store.notifyIndexSubscribersLocked(trimmedIndexID)
	return file, event, nil
}

func (store *CodebaseIndexStore) ListFiles(indexID string) ([]CodebaseIndexFileRecord, error) {
	if store == nil {
		return nil, fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	record, ok := store.state.Indexes[strings.TrimSpace(indexID)]
	if !ok || len(record.RegisteredFiles) == 0 {
		return nil, nil
	}
	files := make([]CodebaseIndexFileRecord, 0, len(record.RegisteredFiles))
	for _, file := range record.RegisteredFiles {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Order != files[j].Order {
			return files[i].Order < files[j].Order
		}
		return files[i].WorkspaceRelativePath < files[j].WorkspaceRelativePath
	})
	return files, nil
}

func (store *CodebaseIndexStore) ListEvents(indexID string, afterEventID int64) ([]CodebaseIndexEventRecord, error) {
	if store == nil {
		return nil, fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	trimmedIndexID := strings.TrimSpace(indexID)
	if trimmedIndexID == "" {
		return nil, fmt.Errorf("index_id is required")
	}
	events := make([]CodebaseIndexEventRecord, 0)
	for _, event := range store.state.Events {
		if event.ID <= afterEventID || event.IndexID != trimmedIndexID {
			continue
		}
		events = append(events, event)
	}
	return events, nil
}

func (store *CodebaseIndexStore) Subscribe(indexID string) (CodebaseIndexSubscription, error) {
	if store == nil {
		return CodebaseIndexSubscription{}, fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return CodebaseIndexSubscription{}, err
	}
	trimmedIndexID := strings.TrimSpace(indexID)
	if trimmedIndexID == "" {
		return CodebaseIndexSubscription{}, fmt.Errorf("index_id is required")
	}
	if store.subscribers == nil {
		store.subscribers = make(map[string]map[string]chan struct{})
	}
	subscriberID := uuid.NewString()
	signal := make(chan struct{}, codebaseIndexSubscriberSignalCapacity)
	if store.subscribers[trimmedIndexID] == nil {
		store.subscribers[trimmedIndexID] = make(map[string]chan struct{})
	}
	store.subscribers[trimmedIndexID][subscriberID] = signal
	return CodebaseIndexSubscription{
		IndexID:      trimmedIndexID,
		SubscriberID: subscriberID,
		Signal:       signal,
	}, nil
}

func (store *CodebaseIndexStore) Unsubscribe(subscription CodebaseIndexSubscription) {
	if store == nil {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	indexID := strings.TrimSpace(subscription.IndexID)
	subscriberID := strings.TrimSpace(subscription.SubscriberID)
	if indexID == "" || subscriberID == "" {
		return
	}
	byIndex := store.subscribers[indexID]
	if byIndex == nil {
		return
	}
	delete(byIndex, subscriberID)
	if len(byIndex) == 0 {
		delete(store.subscribers, indexID)
	}
}

func (store *CodebaseIndexStore) MarkDependenciesReady(indexID string) error {
	return store.updateIndex(indexID, func(record *CodebaseIndexRecord) {
		record.DependenciesReady = true
	})
}

func (store *CodebaseIndexStore) MarkTopoSortReady(indexID string) error {
	return store.updateIndex(indexID, func(record *CodebaseIndexRecord) {
		record.TopoSortReady = true
	})
}

func (store *CodebaseIndexStore) updateIndex(indexID string, update func(*CodebaseIndexRecord)) error {
	if store == nil {
		return fmt.Errorf("codebase index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return err
	}
	trimmedIndexID := strings.TrimSpace(indexID)
	if trimmedIndexID == "" {
		return fmt.Errorf("index_id is required")
	}
	now := time.Now().UTC()
	record, ok := store.state.Indexes[trimmedIndexID]
	if !ok {
		record = CodebaseIndexRecord{
			IndexID:         trimmedIndexID,
			Status:          codebaseIndexStatusReady,
			CreatedAt:       now,
			RegisteredFiles: make(map[string]CodebaseIndexFileRecord),
		}
	}
	if record.RegisteredFiles == nil {
		record.RegisteredFiles = make(map[string]CodebaseIndexFileRecord)
	}
	update(&record)
	record.Status = codebaseIndexStatusReady
	record.UpdatedAt = now
	store.state.Indexes[trimmedIndexID] = record
	return store.saveLocked()
}

func (store *CodebaseIndexStore) loadLocked() error {
	if store.loaded {
		return nil
	}
	state := codebaseIndexState{SchemaVersion: 1, Indexes: make(map[string]CodebaseIndexRecord)}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			store.state = state
			store.nextEventID = 1
			store.loaded = true
			return nil
		}
		return err
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return err
		}
	}
	if state.SchemaVersion <= 0 {
		state.SchemaVersion = 1
	}
	if state.Indexes == nil {
		state.Indexes = make(map[string]CodebaseIndexRecord)
	}
	var maxEventID int64
	for id, record := range state.Indexes {
		if record.RegisteredFiles == nil {
			record.RegisteredFiles = make(map[string]CodebaseIndexFileRecord)
			state.Indexes[id] = record
		}
	}
	for _, event := range state.Events {
		if event.ID > maxEventID {
			maxEventID = event.ID
		}
	}
	store.state = state
	store.nextEventID = maxEventID + 1
	store.loaded = true
	return nil
}

func (store *CodebaseIndexStore) saveLocked() error {
	if err := os.MkdirAll(store.root, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(store.state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(store.root, ".index-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, store.path)
}

func (store *CodebaseIndexStore) nextCodebaseIndexEventIDLocked() int64 {
	if store.nextEventID <= 0 {
		store.nextEventID = 1
	}
	eventID := store.nextEventID
	store.nextEventID++
	return eventID
}

func (store *CodebaseIndexStore) notifyIndexSubscribersLocked(indexID string) {
	if store.subscribers == nil {
		return
	}
	for _, signal := range store.subscribers[strings.TrimSpace(indexID)] {
		select {
		case signal <- struct{}{}:
		default:
		}
	}
}

func (store *CodebaseIndexStore) compactEventsLocked() {
	if len(store.state.Events) <= codebaseIndexMaxPersistedEvents {
		return
	}
	store.state.Events = append([]CodebaseIndexEventRecord(nil), store.state.Events[len(store.state.Events)-codebaseIndexMaxPersistedEvents:]...)
}

func stableCodebaseIndexID(repo string, targetDir string, files []string) string {
	parts := append([]string{strings.TrimSpace(repo), strings.TrimSpace(targetDir)}, files...)
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	if len(normalized) == 0 {
		return "idx_local"
	}
	sort.Strings(normalized)
	sum := sha256.Sum256([]byte(strings.Join(normalized, "\x00")))
	return "idx_" + hex.EncodeToString(sum[:])[:24]
}

func codebaseFileContentHash(content string) string {
	if content == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func firstNonEmptyCodebaseIndex(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func mergeRepositoryIndexState(existing *RepositoryIndexStateRecord, next *RepositoryIndexStateRecord, now time.Time) *RepositoryIndexStateRecord {
	if next == nil {
		return existing
	}
	merged := RepositoryIndexStateRecord{}
	if existing != nil {
		merged = *existing
	}
	merged.CodebaseID = firstNonEmptyCodebaseIndex(next.CodebaseID, merged.CodebaseID)
	merged.Repo = firstNonEmptyCodebaseIndex(next.Repo, merged.Repo)
	merged.TargetDir = firstNonEmptyCodebaseIndex(next.TargetDir, merged.TargetDir)
	merged.Status = firstNonEmptyCodebaseIndex(next.Status, merged.Status, repositoryIndexStatusReady)
	merged.CopyStatus = firstNonEmptyCodebaseIndex(next.CopyStatus, merged.CopyStatus)
	merged.SyncStatus = firstNonEmptyCodebaseIndex(next.SyncStatus, merged.SyncStatus)
	merged.CopyTaskHandle = firstNonEmptyCodebaseIndex(next.CopyTaskHandle, merged.CopyTaskHandle)
	merged.PathKeyHash = firstNonEmptyCodebaseIndex(next.PathKeyHash, merged.PathKeyHash)
	merged.Source = firstNonEmptyCodebaseIndex(next.Source, merged.Source, codebaseIndexSourceRepositoryService)
	merged.LastHandshakeAt = firstNonZeroCodebaseIndexTime(next.LastHandshakeAt, merged.LastHandshakeAt)
	merged.LastSyncAt = firstNonZeroCodebaseIndexTime(next.LastSyncAt, merged.LastSyncAt)
	merged.LastCopyStatusAt = firstNonZeroCodebaseIndexTime(next.LastCopyStatusAt, merged.LastCopyStatusAt)
	merged.CreatedAt = firstNonZeroCodebaseIndexTime(merged.CreatedAt, next.CreatedAt, now)
	merged.UpdatedAt = now
	return &merged
}

func firstNonZeroCodebaseIndexTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}
