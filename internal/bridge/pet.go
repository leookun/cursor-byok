package bridge

import (
	"cursor/internal/appdata"
	"cursor/internal/pet"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// --- 导出的 Bridge 数据类型 ---

// PetInfo 是前端的宠物列表项（从 PetManifest 映射而来）。
type PetInfo struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Version       string   `json:"version"`
	Author        string   `json:"author"`
	RootPath      string   `json:"rootPath"`
	FrameWidth    int      `json:"frameWidth"`
	FrameHeight   int      `json:"frameHeight"`
	AnimationCnt  int      `json:"animationCnt"`
	Status        string   `json:"status"`
	StatusText    string   `json:"statusText"`
	Errors        []string `json:"errors,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

// PetEvents 事件常量。
const (
	EventPetStateChanged = "pet:state-changed"
	EventPetListChanged  = "pet:list-changed"
	EventCursorActivity  = "cursor:activity"
)

const watchInterval = 3 * time.Second

// PetService 管理宠物资源的自动发现、缓存和实时监听。
type PetService struct {
	mu      sync.RWMutex
	app     *application.App
	petsDir string

	cached   []PetInfo
	cachedAt time.Time

	stopWatch chan struct{}
}

func NewPetService() *PetService {
	return &PetService{}
}

func (s *PetService) SetApp(app *application.App) {
	s.mu.Lock()
	s.app = app
	s.mu.Unlock()
	s.startWatching()
}

func PetsDir() string {
	root := appdata.RootDir()
	if strings.TrimSpace(root) == "" {
		root = ".cursor-local-assistant-v2"
	}
	dir := filepath.Join(root, "pets")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s *PetService) startWatching() {
	s.mu.Lock()
	if s.stopWatch != nil {
		s.mu.Unlock()
		return
	}
	s.stopWatch = make(chan struct{})
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(watchInterval)
		defer ticker.Stop()
		s.refreshIfChanged()
		for {
			select {
			case <-s.stopWatch:
				return
			case <-ticker.C:
				s.refreshIfChanged()
			}
		}
	}()
}

func (s *PetService) refreshIfChanged() {
	newPets := scanAllPets()
	s.mu.Lock()
	changed := !petListEqual(s.cached, newPets)
	s.cached = newPets
	s.cachedAt = time.Now()
	app := s.app
	s.mu.Unlock()

	if changed && app != nil {
		app.Event.Emit(EventPetListChanged, newPets)
	}
}

func (s *PetService) ScanPets() ([]PetInfo, error) {
	s.mu.RLock()
	cached := s.cached
	s.mu.RUnlock()

	if len(cached) == 0 {
		s.mu.Lock()
		if len(s.cached) == 0 {
			s.cached = scanAllPets()
			s.cachedAt = time.Now()
		}
		cached = s.cached
		s.mu.Unlock()
	}
	return cached, nil
}

func (s *PetService) OpenPetsDirectory() {
	s.mu.Lock()
	if s.petsDir == "" {
		s.petsDir = PetsDir()
	}
	dir := s.petsDir
	s.mu.Unlock()
	openDirectory(dir)
}

func (s *PetService) DeletePet(petID string) error {
	dir := PetsDir()
	petDir := filepath.Join(dir, petID)
	if _, err := os.Stat(petDir); os.IsNotExist(err) {
		return fmt.Errorf("宠物 %s 不存在", petID)
	}
	if err := os.RemoveAll(petDir); err != nil {
		return err
	}
	s.refreshIfChanged()
	return nil
}

func (s *PetService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopWatch != nil {
		close(s.stopWatch)
		s.stopWatch = nil
	}
}

// --- 内部 ---

func scanAllPets() []PetInfo {
	dir := PetsDir()
	log.Printf("[Pet Scanner] Pets Root = %s", dir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[Pet Scanner] ReadDir error: %v", err)
		return nil
	}
	log.Printf("[Pet Scanner] Found %d entries in pets directory", len(entries))
	var result []PetInfo
	for _, entry := range entries {
		log.Printf("[Pet Scanner]   Entry: %s (isDir=%v)", entry.Name(), entry.IsDir())
		if !entry.IsDir() {
			continue
		}
		petDir := filepath.Join(dir, entry.Name())
		log.Printf("[Pet Scanner]   Checking: %s", petDir)
		m := pet.ScanPetDir(petDir)
		if m != nil {
			result = append(result, manifestToInfo(m))
			log.Printf("[Pet Scanner]   Added: %s (status=%s)", m.Name, m.Status)
		} else {
			log.Printf("[Pet Scanner]   Skipped: nil result")
		}
	}
	log.Printf("[Pet Scanner] Total Pets = %d", len(result))
	return result
}

func manifestToInfo(m *pet.PetManifest) PetInfo {
	return PetInfo{
		ID:           m.ID,
		Name:         m.Name,
		Version:      m.Version,
		Author:       m.Author,
		RootPath:     m.RootPath,
		FrameWidth:   m.FrameWidth,
		FrameHeight:  m.FrameHeight,
		AnimationCnt: len(m.AnimationNames),
		Status:       m.StatusText,
		StatusText:   m.StatusText,
		Errors:       m.Errors,
		Warnings:     m.Warnings,
	}
}

func petListEqual(a, b []PetInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Status != b[i].Status {
			return false
		}
	}
	return true
}
