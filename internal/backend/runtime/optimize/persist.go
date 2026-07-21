package optimize

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor/internal/appdata"
)

const costStoreVersion = 1

// CostSnapshot 是落盘的月度花费快照（预算本身仍来自 config）。
type CostSnapshot struct {
	Version           int     `json:"version"`
	YearMonth         string  `json:"yearMonth"` // YYYY-MM
	SpentThisMonthUSD float64 `json:"spentThisMonthUSD"`
	TurnsThisMonth    int64   `json:"turnsThisMonth"`
	UpdatedAt         string  `json:"updatedAt,omitempty"`
}

// DefaultCostStorePath 返回 Optimization cost tracker 默认路径（appdata data 根下）。
func DefaultCostStorePath() string {
	return filepath.Join(appdata.DataRootPath(), "optimize", "cost_tracker.json")
}

// CurrentYearMonth 返回本机时区的 YYYY-MM。
func CurrentYearMonth() string {
	return time.Now().Format("2006-01")
}

// LoadCostSnapshot 从 path 读取快照。文件不存在时返回零值与 nil error。
func LoadCostSnapshot(path string) (CostSnapshot, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return CostSnapshot{}, fmt.Errorf("empty cost store path")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CostSnapshot{}, nil
		}
		return CostSnapshot{}, fmt.Errorf("read cost store: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return CostSnapshot{}, nil
	}
	var snap CostSnapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return CostSnapshot{}, fmt.Errorf("decode cost store: %w", err)
	}
	if snap.Version == 0 {
		snap.Version = costStoreVersion
	}
	return snap, nil
}

// SaveCostSnapshot 原子写快照（先写临时文件再 rename）。
func SaveCostSnapshot(path string, snap CostSnapshot) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("empty cost store path")
	}
	if snap.Version == 0 {
		snap.Version = costStoreVersion
	}
	if strings.TrimSpace(snap.YearMonth) == "" {
		snap.YearMonth = CurrentYearMonth()
	}
	if strings.TrimSpace(snap.UpdatedAt) == "" {
		snap.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir cost store: %w", err)
	}
	body, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cost store: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "cost_tracker-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp cost store: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp cost store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp cost store: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		// Windows: rename 目标存在时可能失败 → 先删再 rename
		_ = os.Remove(path)
		if err2 := os.Rename(tmpName, path); err2 != nil {
			return fmt.Errorf("rename cost store: %w", err2)
		}
	}
	cleanup = false
	return nil
}

// ApplySnapshotToTracker 将快照合并进 tracker；月份不一致则清零 spent/turns。
// yearMonth 为当前应使用的月份键。
func ApplySnapshotToTracker(tracker *CostTracker, snap CostSnapshot, yearMonth string) {
	if tracker == nil {
		return
	}
	ym := strings.TrimSpace(yearMonth)
	if ym == "" {
		ym = CurrentYearMonth()
	}
	snapYM := strings.TrimSpace(snap.YearMonth)
	if snapYM == "" || snapYM != ym {
		tracker.SpentThisMonthUSD = 0
		tracker.TurnsThisMonth = 0
		return
	}
	if snap.SpentThisMonthUSD < 0 {
		snap.SpentThisMonthUSD = 0
	}
	if snap.TurnsThisMonth < 0 {
		snap.TurnsThisMonth = 0
	}
	tracker.SpentThisMonthUSD = snap.SpentThisMonthUSD
	tracker.TurnsThisMonth = snap.TurnsThisMonth
}

// SnapshotFromTracker 从 tracker 生成落盘快照。
func SnapshotFromTracker(tracker *CostTracker, yearMonth string) CostSnapshot {
	ym := strings.TrimSpace(yearMonth)
	if ym == "" {
		ym = CurrentYearMonth()
	}
	snap := CostSnapshot{
		Version:   costStoreVersion,
		YearMonth: ym,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if tracker != nil {
		snap.SpentThisMonthUSD = tracker.SpentThisMonthUSD
		snap.TurnsThisMonth = tracker.TurnsThisMonth
	}
	return snap
}
