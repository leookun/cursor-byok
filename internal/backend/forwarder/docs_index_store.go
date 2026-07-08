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
)

const (
	docsIndexStatusIndexed    = "indexed"
	docsIndexSourceLocal      = "local_docs"
	docsIndexSourceAdditional = "additional_docs"
)

type DocsIndexStore struct {
	mu     sync.Mutex
	root   string
	path   string
	loaded bool
	state  docsIndexState
}

type docsIndexState struct {
	SchemaVersion int                        `json:"schema_version"`
	Docs          map[string]DocsIndexRecord `json:"docs"`
}

type DocsIndexRecord struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	URL         string    `json:"url,omitempty"`
	Content     string    `json:"content,omitempty"`
	GitOrigin   string    `json:"git_origin,omitempty"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastCrawlAt time.Time `json:"last_crawl_at,omitempty"`
	LastIndexAt time.Time `json:"last_index_at,omitempty"`
}

func NewDocsIndexStore(root string) *DocsIndexStore {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		trimmed = "docs-index"
	}
	return &DocsIndexStore{
		root: trimmed,
		path: filepath.Join(trimmed, "index.json"),
		state: docsIndexState{
			SchemaVersion: 1,
			Docs:          make(map[string]DocsIndexRecord),
		},
	}
}

func (store *DocsIndexStore) Upsert(record DocsIndexRecord) (DocsIndexRecord, error) {
	if store == nil {
		return DocsIndexRecord{}, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return DocsIndexRecord{}, err
	}
	now := time.Now().UTC()
	identifier := strings.TrimSpace(record.Identifier)
	if identifier == "" {
		identifier = stableDocsIdentifier(record.Title, record.URL, record.Content, record.GitOrigin)
	}
	existing, ok := store.state.Docs[identifier]
	if ok {
		existing.Title = firstNonEmptyDocs(record.Title, existing.Title, identifier)
		existing.URL = firstNonEmptyDocs(record.URL, existing.URL)
		existing.Content = firstNonEmptyDocs(record.Content, existing.Content)
		existing.GitOrigin = firstNonEmptyDocs(record.GitOrigin, existing.GitOrigin)
		existing.Status = docsIndexStatusIndexed
		existing.Source = firstNonEmptyDocs(record.Source, existing.Source, docsIndexSourceLocal)
		existing.UpdatedAt = now
		existing.LastCrawlAt = now
		existing.LastIndexAt = now
		store.state.Docs[identifier] = existing
		return existing, store.saveLocked()
	}
	record.ID = firstNonEmptyDocs(record.ID, identifier)
	record.Identifier = identifier
	record.Title = firstNonEmptyDocs(record.Title, identifier)
	record.Status = docsIndexStatusIndexed
	record.Source = firstNonEmptyDocs(record.Source, docsIndexSourceLocal)
	record.CreatedAt = now
	record.UpdatedAt = now
	record.LastCrawlAt = now
	record.LastIndexAt = now
	store.state.Docs[identifier] = record
	return record, store.saveLocked()
}

func (store *DocsIndexStore) List(gitOrigin string, limit int32) ([]DocsIndexRecord, error) {
	if store == nil {
		return nil, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	origin := strings.TrimSpace(gitOrigin)
	records := make([]DocsIndexRecord, 0, len(store.state.Docs))
	for _, record := range store.state.Docs {
		if origin != "" && strings.TrimSpace(record.GitOrigin) != origin {
			continue
		}
		records = append(records, record)
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].Identifier < records[j].Identifier
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	if limit > 0 && len(records) > int(limit) {
		records = records[:limit]
	}
	return records, nil
}

func (store *DocsIndexStore) Get(identifier string) (DocsIndexRecord, bool, error) {
	if store == nil {
		return DocsIndexRecord{}, false, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return DocsIndexRecord{}, false, err
	}
	record, ok := store.state.Docs[strings.TrimSpace(identifier)]
	return record, ok, nil
}

func (store *DocsIndexStore) Remove(identifier string) error {
	if store == nil {
		return fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return err
	}
	delete(store.state.Docs, strings.TrimSpace(identifier))
	return store.saveLocked()
}

func (store *DocsIndexStore) loadLocked() error {
	if store.loaded {
		return nil
	}
	state := docsIndexState{SchemaVersion: 1, Docs: make(map[string]DocsIndexRecord)}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if os.IsNotExist(err) {
			store.state = state
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
	if state.Docs == nil {
		state.Docs = make(map[string]DocsIndexRecord)
	}
	store.state = state
	store.loaded = true
	return nil
}

func (store *DocsIndexStore) saveLocked() error {
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
	defer func() { _ = os.Remove(tmpPath) }()
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

func stableDocsIdentifier(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "doc_local"
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "doc_" + hex.EncodeToString(sum[:])[:24]
}

func firstNonEmptyDocs(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
