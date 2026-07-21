// Package client — Cache Runtime 管理与前端 DTO。
package client

// CacheStatsDTO 缓存统计 DTO（供前端 Dashboard 展示）。
// 字段对齐 cache.CacheStats，仅对外暴露可展示字段。
type CacheStatsDTO struct {
	ExactHits      int64   `json:"exactHits"`
	ExactMisses    int64   `json:"exactMisses"`
	SemanticHits   int64   `json:"semanticHits"`
	SemanticMisses int64   `json:"semanticMisses"`
	TotalHits      int64   `json:"totalHits"`
	TotalMisses    int64   `json:"totalMisses"`
	HitRate        float64 `json:"hitRate"`
	TokensSaved    int64   `json:"tokensSaved"`
	Entries        int     `json:"entries"`
}

// GetCacheStats 返回 Cache Runtime 统计。
func (s *ProxyService) GetCacheStats() (CacheStatsDTO, error) {
	if s == nil || s.backendHost == nil {
		return CacheStatsDTO{}, nil
	}
	rt := s.backendHost.CacheRuntime()
	if rt == nil {
		return CacheStatsDTO{}, nil
	}
	stats := rt.Stats()
	if stats == nil {
		return CacheStatsDTO{}, nil
	}
	return CacheStatsDTO{
		ExactHits:      stats.ExactHits,
		ExactMisses:    stats.ExactMisses,
		SemanticHits:   stats.SemanticHits,
		SemanticMisses: stats.SemanticMisses,
		TotalHits:      stats.TotalHits,
		TotalMisses:    stats.TotalMisses,
		HitRate:        stats.HitRate,
		TokensSaved:    stats.TokensSaved,
		Entries:        rt.Entries(),
	}, nil
}

// ClearCache 清空精确缓存与语义缓存。
func (s *ProxyService) ClearCache() error {
	if s == nil || s.backendHost == nil {
		return nil
	}
	rt := s.backendHost.CacheRuntime()
	if rt == nil {
		return nil
	}
	return rt.Clear()
}