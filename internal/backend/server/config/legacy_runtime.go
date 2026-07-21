package config

import (
	"context"

	legacyruntime "cursor/internal/runtime"
)

func (store *Store) LegacyRuntimeSnapshot(ctx context.Context) (legacyruntime.RuntimeConfigSnapshot, error) {
	cfg, err := store.Load(ctx)
	if err != nil {
		return legacyruntime.RuntimeConfigSnapshot{}, err
	}

	// ModelAdapterConfig now shares the same canonical type via modelchannel,
	// so we can directly pass the slice instead of field-by-field copying.
	adapters := make([]legacyruntime.ModelAdapterConfig, len(cfg.ModelAdapters))
	copy(adapters, cfg.ModelAdapters)

	return legacyruntime.RuntimeConfigSnapshot{
		ObservabilityLogEnabled:   cfg.Log,
		ProviderStreamIdleTimeout: cfg.ProviderStreamIdleTimeout,
		ModelAdapters:             adapters,
	}, nil
}
