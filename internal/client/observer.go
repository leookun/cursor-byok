package client

import (
	"context"

	localruntime "cursor/internal/runtime"
)

// runtimeConfigSnapshot 用于处理与 runtimeConfigSnapshot 相关的逻辑。
func (s *ProxyService) runtimeConfigSnapshot(ctx context.Context) (localruntime.RuntimeConfigSnapshot, error) {
	if s == nil {
		return localruntime.RuntimeConfigSnapshot{}, nil
	}
	if s.backendHost != nil && s.backendHost.ConfigManager() != nil {
		return s.backendHost.ConfigManager().LegacyRuntimeSnapshot(ctx)
	}
	if s.store == nil {
		return localruntime.RuntimeConfigSnapshot{}, nil
	}
	return s.store.LegacyRuntimeSnapshot(ctx)
}
