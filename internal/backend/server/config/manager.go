package config

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	legacyruntime "cursor/internal/runtime"
)

const configHotReloadMinInterval = 500 * time.Millisecond

type Manager struct {
	store       *Store
	current     atomic.Pointer[Config]
	listenersMu sync.RWMutex
	listeners   []func(Config)
	reloadMu    sync.Mutex
	snapshot    fileSnapshot
	lastReload  time.Time
	reloadError string
}

func NewManager(ctx context.Context, store *Store) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required")
	}
	cfg, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		store:    store,
		snapshot: store.snapshot(),
	}
	manager.setCurrent(cfg)
	return manager, nil
}

func (manager *Manager) Current() Config {
	if manager == nil {
		return DefaultConfig()
	}
	manager.reloadIfChanged(context.Background())
	return manager.currentConfig()
}

func (manager *Manager) currentConfig() Config {
	if manager == nil {
		return DefaultConfig()
	}
	if current := manager.current.Load(); current != nil {
		return *current
	}
	return DefaultConfig()
}

func (manager *Manager) Load(ctx context.Context) (Config, error) {
	if manager == nil {
		return DefaultConfig(), nil
	}
	manager.reloadIfChanged(ctx)
	return manager.currentConfig(), nil
}

func (manager *Manager) Save(ctx context.Context, cfg Config) (Config, error) {
	if manager == nil || manager.store == nil {
		return Config{}, fmt.Errorf("config manager is not initialized")
	}
	normalized, err := manager.store.Save(ctx, cfg)
	if err != nil {
		return Config{}, err
	}
	manager.setCurrent(normalized)
	manager.reloadMu.Lock()
	manager.snapshot = manager.store.snapshot()
	manager.lastReload = time.Now()
	manager.reloadError = ""
	manager.reloadMu.Unlock()
	manager.notify(normalized)
	return normalized, nil
}

func (manager *Manager) LastAgentModelHash() string {
	if manager == nil {
		return ""
	}
	return strings.TrimSpace(manager.Current().LastAgentModelHash)
}

func (manager *Manager) SaveLastAgentModelHash(ctx context.Context, value string) error {
	if manager == nil {
		return fmt.Errorf("config manager is not initialized")
	}
	normalizedValue := strings.TrimSpace(value)
	current := manager.Current()
	if strings.TrimSpace(current.LastAgentModelHash) == normalizedValue {
		return nil
	}
	current.LastAgentModelHash = normalizedValue
	_, err := manager.Save(ctx, current)
	return err
}

func (manager *Manager) ProviderStreamIdleTimeout(ctx context.Context) time.Duration {
	if manager == nil {
		return time.Duration(DefaultProviderStreamIdleTimeoutSeconds) * time.Second
	}
	manager.reloadIfChanged(ctx)
	seconds := normalizeProviderStreamIdleTimeout(manager.currentConfig().ProviderStreamIdleTimeout)
	return time.Duration(seconds) * time.Second
}

func (manager *Manager) IsObservabilityLogEnabled(ctx context.Context) bool {
	if manager == nil {
		return false
	}
	manager.reloadIfChanged(ctx)
	return manager.currentConfig().Log
}

func (manager *Manager) Subscribe(listener func(Config)) func() {
	if manager == nil || listener == nil {
		return func() {}
	}
	manager.listenersMu.Lock()
	manager.listeners = append(manager.listeners, listener)
	index := len(manager.listeners) - 1
	manager.listenersMu.Unlock()
	return func() {
		manager.listenersMu.Lock()
		defer manager.listenersMu.Unlock()
		if index < 0 || index >= len(manager.listeners) {
			return
		}
		manager.listeners[index] = nil
	}
}

func (manager *Manager) LegacyRuntimeSnapshot(_ context.Context) (legacyruntime.RuntimeConfigSnapshot, error) {
	cfg := manager.Current()
	adapters := make([]legacyruntime.ModelAdapterConfig, 0, len(cfg.ModelAdapters))
	for _, item := range cfg.ModelAdapters {
		adapters = append(adapters, legacyruntime.ModelAdapterConfig{
			ID:                       item.ID,
			DisplayName:              item.DisplayName,
			Type:                     item.Type,
			BaseURL:                  item.BaseURL,
			APIKey:                   item.APIKey,
			TooltipData:              item.TooltipData,
			ModelID:                  item.ModelID,
			ReasoningEffort:          item.ReasoningEffort,
			OpenAIEndpoint:           item.OpenAIEndpoint,
			OpenAIExtraParamsEnabled: item.OpenAIExtraParamsEnabled,
			OpenAIExtraParamsJSON:    item.OpenAIExtraParamsJSON,
			ContextWindowTokens:      item.ContextWindowTokens,
			MaxCompletionTokens:      item.MaxCompletionTokens,
			AnthropicMaxTokens:       item.AnthropicMaxTokens,
			AnthropicThinkingEffort:  item.AnthropicThinkingEffort,
			ThinkingBudgetTokens:     item.ThinkingBudgetTokens,
		})
	}
	return legacyruntime.RuntimeConfigSnapshot{
		ObservabilityLogEnabled:   cfg.Log,
		ProviderStreamIdleTimeout: cfg.ProviderStreamIdleTimeout,
		ModelAdapters:             adapters,
	}, nil
}

func (manager *Manager) RouteMode(hasUpstreamURL bool) string {
	if !hasUpstreamURL {
		return DefaultRoutingMode
	}
	if manager == nil {
		return DefaultRoutingMode
	}
	mode := normalizeRoutingMode(manager.Current().Routing.Mode)
	if mode == "" {
		return DefaultRoutingMode
	}
	return mode
}

func (manager *Manager) setCurrent(cfg Config) {
	next := cfg
	manager.current.Store(&next)
}

func (manager *Manager) reloadIfChanged(ctx context.Context) {
	if manager == nil || manager.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	now := time.Now()
	manager.reloadMu.Lock()
	if !manager.lastReload.IsZero() && now.Sub(manager.lastReload) < configHotReloadMinInterval {
		manager.reloadMu.Unlock()
		return
	}
	manager.lastReload = now
	nextSnapshot := manager.store.snapshot()
	if nextSnapshot == manager.snapshot {
		manager.reloadMu.Unlock()
		return
	}
	cfg, err := manager.store.Load(ctx)
	if err != nil {
		errText := err.Error()
		if errText != manager.reloadError {
			log.Printf("config hot reload skipped path=%s error=%v", manager.store.Path(), err)
			manager.reloadError = errText
		}
		manager.reloadMu.Unlock()
		return
	}
	manager.snapshot = nextSnapshot
	manager.reloadError = ""
	manager.setCurrent(cfg)
	manager.reloadMu.Unlock()
	manager.notify(cfg)
}

func (manager *Manager) notify(cfg Config) {
	manager.listenersMu.RLock()
	listeners := append([]func(Config){}, manager.listeners...)
	manager.listenersMu.RUnlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(cfg)
		}
	}
}
