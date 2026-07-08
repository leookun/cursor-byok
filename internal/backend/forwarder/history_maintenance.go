package forwarder

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const historyMaintenanceLockStaleAfter = 30 * time.Minute
const historyMaintenanceTempStaleAfter = 5 * time.Minute

func (service *Service) startHistoryMaintenance() {
	if service == nil || service.store == nil {
		return
	}
	go func() {
		if err := service.runHistoryMaintenance(); err != nil {
			log.Printf("forwarder history maintenance failed: %v", err)
		}
	}()
}

func (service *Service) runHistoryMaintenance() error {
	historyRoot := strings.TrimSpace(service.store.HistoryDir())
	if historyRoot == "" {
		return nil
	}
	release, ok, err := acquireHistoryMaintenanceLock(historyRoot)
	if err != nil || !ok {
		return err
	}
	defer release()

	entries, err := os.ReadDir(historyRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			cleanupRootLegacyHistoryArtifact(historyRoot, entry.Name())
			continue
		}
		service.cleanupConversationLegacyArtifacts(filepath.Join(historyRoot, entry.Name()))
	}
	return nil
}

func cleanupRootLegacyHistoryArtifact(historyRoot string, name string) {
	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || trimmedName == ".history-maintenance.lock" {
		return
	}
	if !isRootLegacyHistoryArtifact(trimmedName) {
		return
	}
	path := filepath.Join(historyRoot, trimmedName)
	if strings.Contains(trimmedName, ".tmp-") {
		cleanupStaleHistoryTempArtifact(path)
		return
	}
	if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("forwarder root legacy cleanup failed path=%s err=%v", path, err)
	}
}

func isRootLegacyHistoryArtifact(name string) bool {
	if strings.TrimSpace(name) == usageFileName || strings.TrimSpace(name) == usageFileName+".lock" {
		return false
	}
	if strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl") {
		return true
	}
	if strings.Contains(name, ".tmp-") {
		return true
	}
	if strings.HasSuffix(name, ".lock") {
		return true
	}
	return false
}

func (service *Service) cleanupConversationLegacyArtifacts(conversationDir string) {
	for _, name := range []string{
		"turns",
		"active",
		"latest.json",
		"summary.json",
		"replay.json",
		"runtime.json",
		"request.json",
		"recovery.json",
		"conversation.json",
		"entries.jsonl",
	} {
		path := filepath.Join(conversationDir, name)
		if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("forwarder legacy cleanup failed path=%s err=%v", path, err)
		}
	}
	entries, err := os.ReadDir(conversationDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if strings.Contains(entry.Name(), ".tmp-") {
				cleanupStaleHistoryTempArtifact(filepath.Join(conversationDir, entry.Name()))
			}
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(entry.Name())); err != nil {
			continue
		}
		path := filepath.Join(conversationDir, entry.Name())
		if err := os.RemoveAll(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("forwarder numeric legacy cleanup failed path=%s err=%v", path, err)
		}
	}
}

func cleanupStaleHistoryTempArtifact(path string) {
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) < historyMaintenanceTempStaleAfter {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("forwarder temp cleanup failed path=%s err=%v", path, err)
	}
}

func acquireHistoryMaintenanceLock(historyRoot string) (func(), bool, error) {
	if err := os.MkdirAll(historyRoot, 0o755); err != nil {
		return nil, false, err
	}
	lockPath := filepath.Join(historyRoot, ".history-maintenance.lock")
	for attempt := 0; attempt < 2; attempt++ {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = file.WriteString(time.Now().UTC().Format(time.RFC3339Nano))
			_ = file.Close()
			return func() {
				_ = os.Remove(lockPath)
			}, true, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false, err
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return nil, false, statErr
		}
		if time.Since(info.ModTime()) > historyMaintenanceLockStaleAfter {
			_ = os.Remove(lockPath)
			continue
		}
		return nil, false, nil
	}
	return nil, false, nil
}
