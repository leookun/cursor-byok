// Package telemetry 实现 Telemetry Runtime：全链路可观测。
// 收集 Token、成本、延迟、缓存命中率、专家贡献度等指标。
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Runtime 是 Telemetry Runtime 的主入口。
type Runtime struct {
	dir     string
	mu      sync.Mutex
	daily   *DailySummary
	// closed 标记 Close 是否已调用（R14 lifecycle unification）。
	closed bool
}

// TurnRecord 单次 turn 的完整遥测数据。
type TurnRecord struct {
	TurnID         string    `json:"turnID"`
	RequestID      string    `json:"requestID"`
	ConversationID string    `json:"conversationID"`
	ModelID        string    `json:"modelID"`
	VirtualModel   string    `json:"virtualModel,omitempty"` // "moa" or ""
	Timestamp      time.Time `json:"timestamp"`

	// 耗时（毫秒）
	PlannerDurationMS    int64            `json:"plannerDurationMS,omitempty"`
	ExpertDurationsMS    map[string]int64 `json:"expertDurationsMS,omitempty"`
	JudgeDurationMS      int64            `json:"judgeDurationMS,omitempty"`
	AggregatorDurationMS int64            `json:"aggregatorDurationMS,omitempty"`
	TotalDurationMS      int64            `json:"totalDurationMS"`

	// Token
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
	CacheHitTokens   int `json:"cacheHitTokens"`

	// 成本
	EstimatedCostUSD  float64            `json:"estimatedCostUSD"`
	ProviderBreakdown map[string]float64 `json:"providerBreakdown,omitempty"`

	// 缓存
	CacheHit     bool   `json:"cacheHit"`
	CacheHitType string `json:"cacheHitType,omitempty"` // "exact" / "semantic" / ""

	// 压缩
	CompactionTriggered bool    `json:"compactionTriggered"`
	TokensCompacted     int     `json:"tokensCompacted"`
	CompactionRatio     float64 `json:"compactionRatio"`
}

// DailySummary 每日汇总。
type DailySummary struct {
	Date              string  `json:"date"`
	TurnsTotal        int64   `json:"turnsTotal"`
	TotalTokens       int64   `json:"totalTokens"`
	TotalCostUSD      float64 `json:"totalCostUSD"`
	CacheHitRate      float64 `json:"cacheHitRate"`
	AvgDurationMS     int64   `json:"avgDurationMS"`
	VirtualModelTurns int64   `json:"virtualModelTurns"`
	PhysicalTurns     int64   `json:"physicalTurns"`
}

// NewRuntime 创建 Telemetry Runtime。
func NewRuntime(dir string) (*Runtime, error) {
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0755); err != nil {
		return nil, fmt.Errorf("create telemetry dir: %w", err)
	}
	rt := &Runtime{
		dir:   dir,
		daily: &DailySummary{Date: time.Now().Format("2006-01-02")},
	}
	rt.loadDailySummary()
	return rt, nil
}

// RecordTurn 记录一次 turn。
func (rt *Runtime) RecordTurn(record *TurnRecord) error {
	if rt == nil || record == nil {
		return nil
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	// 写入文件
	dateDir := record.Timestamp.Format("2006-01-02")
	dir := filepath.Join(rt.dir, "turns", dateDir)
	_ = os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, record.TurnID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}

	// 更新每日汇总
	rt.daily.TurnsTotal++
	rt.daily.TotalTokens += int64(record.TotalTokens)
	rt.daily.TotalCostUSD += record.EstimatedCostUSD
	rt.daily.AvgDurationMS = (rt.daily.AvgDurationMS*(rt.daily.TurnsTotal-1) + record.TotalDurationMS) / rt.daily.TurnsTotal
	if record.CacheHit {
		newHitRate := float64(rt.daily.TurnsTotal-1)*rt.daily.CacheHitRate + 1.0
		rt.daily.CacheHitRate = newHitRate / float64(rt.daily.TurnsTotal)
	} else {
		rt.daily.CacheHitRate = float64(rt.daily.TurnsTotal-1) * rt.daily.CacheHitRate / float64(rt.daily.TurnsTotal)
	}
	if record.VirtualModel != "" {
		rt.daily.VirtualModelTurns++
	} else {
		rt.daily.PhysicalTurns++
	}

	rt.persistDailySummary()
	return nil
}

// GetDailySummary 获取每日汇总。
func (rt *Runtime) GetDailySummary() *DailySummary {
	if rt == nil {
		return &DailySummary{}
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	summary := *rt.daily
	return &summary
}

func (rt *Runtime) persistDailySummary() {
	path := filepath.Join(rt.dir, "daily_summary.json")
	data, _ := json.MarshalIndent(rt.daily, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}

func (rt *Runtime) loadDailySummary() {
	path := filepath.Join(rt.dir, "daily_summary.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var summary DailySummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return
	}
	// 检查是否同一天
	if summary.Date == time.Now().Format("2006-01-02") {
		rt.daily = &summary
	}
}

// Close flushes the in-memory daily summary to disk and marks the runtime
// closed. Subsequent Close calls are no-ops. R14: lifecycle unification.
func (rt *Runtime) Close(ctx context.Context) error {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return nil
	}
	rt.closed = true
	rt.persistDailySummaryLocked()
	rt.mu.Unlock()
	return nil
}

// IsClosed reports whether Close has been invoked on this runtime.
func (rt *Runtime) IsClosed() bool {
	if rt == nil {
		return false
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.closed
}

// persistDailySummaryLocked persists the daily summary assuming the caller
// already holds rt.mu. Used by Close to avoid re-entrant locking.
func (rt *Runtime) persistDailySummaryLocked() {
	if rt == nil || rt.dir == "" {
		return
	}
	path := filepath.Join(rt.dir, "daily_summary.json")
	data, _ := json.MarshalIndent(rt.daily, "", "  ")
	_ = os.WriteFile(path, data, 0644)
}
