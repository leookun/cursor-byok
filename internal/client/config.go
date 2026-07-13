package client

import (
	"context"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = serverconfig.Config

// OptimizationCostSummary 进程内 Optimization Runtime 成本摘要（供前端 Dashboard）。
type OptimizationCostSummary struct {
	Enabled                 bool    `json:"enabled"`
	QualityTier             string  `json:"qualityTier"`
	MonthlyBudgetUSD        float64 `json:"monthlyBudgetUSD"`
	SpentThisMonthUSD       float64 `json:"spentThisMonthUSD"`
	TurnsThisMonth          int64   `json:"turnsThisMonth"`
	EstimatedRemainingTurns int64   `json:"estimatedRemainingTurns"`
}

// GetOptimizationCostSummary 返回当前 Optimization Runtime 成本摘要。
func (s *ProxyService) GetOptimizationCostSummary() (OptimizationCostSummary, error) {
	empty := OptimizationCostSummary{
		Enabled:          serverconfig.DefaultOptimizationEnabled,
		QualityTier:      serverconfig.DefaultOptimizationQualityTier,
		MonthlyBudgetUSD: serverconfig.DefaultOptimizationMonthlyBudget,
	}
	if s == nil || s.backendHost == nil {
		return empty, nil
	}
	cfg := serverconfig.DefaultConfig()
	if cm := s.backendHost.ConfigManager(); cm != nil {
		cfg = cm.Current()
	}
	opt := serverconfig.NormalizeOptimizationConfig(cfg.Optimization)
	summary := OptimizationCostSummary{
		Enabled:          opt.Enabled,
		QualityTier:      opt.QualityTier,
		MonthlyBudgetUSD: opt.MonthlyBudgetUSD,
	}
	tracker := s.backendHost.GetCostSummary()
	if tracker != nil {
		summary.SpentThisMonthUSD = tracker.SpentThisMonthUSD
		summary.TurnsThisMonth = tracker.TurnsThisMonth
		summary.EstimatedRemainingTurns = tracker.EstimatedRemainingTurns
		if tracker.MonthlyBudgetUSD > 0 {
			summary.MonthlyBudgetUSD = tracker.MonthlyBudgetUSD
		}
	}
	rt := s.backendHost.OptimizationRuntime()
	if rt != nil {
		summary.Enabled = rt.Enabled()
		if t := string(rt.QualityTier()); t != "" {
			summary.QualityTier = t
		}
	}
	return summary, nil
}

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	if s == nil {
		return serverconfig.DefaultConfig(), nil
	}
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	if s.backendHost != nil {
		return s.backendHost.LoadConfig(ctx)
	}
	if s.store == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return s.store.Load(ctx)
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.SaveConfig(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.Save(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

func (s *ProxyService) emitUserConfigChanged(cfg UserConfig) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit("user-config:changed", cfg)
}

// resolveUserConfigPath 用于处理与 resolveUserConfigPath 相关的逻辑。
func resolveUserConfigPath() string {
	return appdata.ConfigFilePath()
}

// resolveLogsRootPath 用于处理与 resolveLogsRootPath 相关的逻辑。
func resolveLogsRootPath() string {
	return appdata.LogsRootPath()
}

// ResolveLogsRootPath 用于处理与 ResolveLogsRootPath 相关的逻辑。
func ResolveLogsRootPath() string {
	return resolveLogsRootPath()
}

// ResolveSettingsRootPath 用于处理与 ResolveSettingsRootPath 相关的逻辑。
func ResolveSettingsRootPath() string {
	return appdata.RootDir()
}
