// Package plugin implements the Phase 8 Plugin Marketplace runtime:
// a catalog-backed plugin store, a constrained goroutine sandbox for dynamic
// execution, and a REST handler wired into the backend server framework.
//
// Design: ADR-021 (Plugin SDK) + ADR-047 (Plugin sandbox execution).
// The persistence style mirrors the cache and workflow runtimes (JSON file
// under appdata.DataRootPath()).
package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cursor/internal/appdata"
)

const fileName = "manifest.json"

// Manifest is the persisted record of an installed plugin.
// Fields required by the task: name, version, enabled, source, installedAt,
// metadata.
type Manifest struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Enabled     bool           `json:"enabled"`
	Source      string         `json:"source"`
	InstalledAt time.Time      `json:"installedAt"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Store persists the plugin manifest catalog as a single JSON file.
// ponytail: whole-file read/rewrite; plugin counts are tiny so we avoid a
// partial-write journal. A global RWMutex guards the single file handle path.
type Store struct {
	dir  string
	path string
	mu   sync.RWMutex
}

// NewStore creates and loads the plugin store. dir defaults to
// <appdata>/data/plugin when empty.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		dir = filepath.Join(appdata.DataRootPath(), "plugin")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create plugin dir: %w", err)
	}
	s := &Store{
		dir:  dir,
		path: filepath.Join(dir, fileName),
	}
	if err := s.ensureFile(); err != nil {
		return nil, err
	}
	return s, nil
}

type catalogFile struct {
	Plugins []Manifest `json:"plugins"`
}

func (s *Store) ensureFile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		return nil
	}
	return s.writeLocked(catalogFile{Plugins: []Manifest{}})
}

func (s *Store) read() (*catalogFile, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &catalogFile{Plugins: []Manifest{}}, nil
		}
		return nil, fmt.Errorf("read plugin manifest: %w", err)
	}
	var cf catalogFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse plugin manifest: %w", err)
	}
	if cf.Plugins == nil {
		cf.Plugins = []Manifest{}
	}
	return &cf, nil
}

func (s *Store) writeLocked(cf catalogFile) error {
	if cf.Plugins == nil {
		cf.Plugins = []Manifest{}
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal plugin manifest: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write plugin manifest: %w", err)
	}
	return nil
}

// List returns all installed manifests (copy).
func (s *Store) List() []Manifest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cf, err := s.read()
	if err != nil || cf == nil {
		return []Manifest{}
	}
	out := make([]Manifest, len(cf.Plugins))
	copy(out, cf.Plugins)
	return out
}

// Get returns the installed manifest for name, if present.
func (s *Store) Get(name string) (Manifest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cf, err := s.read()
	if err != nil {
		return Manifest{}, false
	}
	for _, m := range cf.Plugins {
		if m.Name == name {
			return m, true
		}
	}
	return Manifest{}, false
}

// Upsert installs or replaces a manifest, persisting to disk.
func (s *Store) Upsert(m Manifest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cf, err := s.read()
	if err != nil {
		return err
	}
	for i, existing := range cf.Plugins {
		if existing.Name == m.Name {
			cf.Plugins[i] = m
			return s.writeLocked(*cf)
		}
	}
	cf.Plugins = append(cf.Plugins, m)
	return s.writeLocked(*cf)
}

// Remove deletes a manifest by name. Returns false if not found.
func (s *Store) Remove(name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cf, err := s.read()
	if err != nil {
		return false, err
	}
	filtered := cf.Plugins[:0]
	removed := false
	for _, m := range cf.Plugins {
		if m.Name == name {
			removed = true
			continue
		}
		filtered = append(filtered, m)
	}
	cf.Plugins = filtered
	if !removed {
		return false, nil
	}
	return true, s.writeLocked(*cf)
}