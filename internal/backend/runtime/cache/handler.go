// Package cache HTTP layer: exposes Cache Runtime stats and clear as REST routes.
package cache

import (
	"encoding/json"
	"fmt"
	"net/http"

	"cursor/internal/backend/server"
)

// Handler holds the cache runtime and exposes REST routes.
type Handler struct {
	rt *Runtime
}

// NewHandler creates the HTTP handler. rt may be nil (nil-safe handlers).
func NewHandler(rt *Runtime) *Handler {
	return &Handler{rt: rt}
}

// Routes returns REST routes for host registration into server.New.
func (h *Handler) Routes() []server.Option {
	return []server.Option{
		server.GET("/api/cache/stats",
			server.Name("cache_stats"),
			server.HTTP(),
			server.Local(h.stats),
		),
		server.POST("/api/cache/clear",
			server.Name("cache_clear"),
			server.HTTP(),
			server.Local(h.clear),
		),
	}
}

func writeJSON(ctx *server.Context, status int, payload any) error {
	if ctx == nil || ctx.Writer == nil {
		return fmt.Errorf("nil context writer")
	}
	ctx.Writer.Header().Set("Content-Type", "application/json")
	ctx.Writer.WriteHeader(status)
	return json.NewEncoder(ctx.Writer).Encode(payload)
}

// statsResponse matches frontend CacheDashboard + client CacheStatsDTO fields.
type statsResponse struct {
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

func (h *Handler) stats(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusOK, statsResponse{})
	}
	s := h.rt.Stats()
	if s == nil {
		s = &CacheStats{}
	}
	return writeJSON(ctx, http.StatusOK, statsResponse{
		ExactHits:      s.ExactHits,
		ExactMisses:    s.ExactMisses,
		SemanticHits:   s.SemanticHits,
		SemanticMisses: s.SemanticMisses,
		TotalHits:      s.TotalHits,
		TotalMisses:    s.TotalMisses,
		HitRate:        s.HitRate,
		TokensSaved:    s.TokensSaved,
		Entries:        h.rt.Entries() + h.rt.CountExact(),
	})
}

func (h *Handler) clear(ctx *server.Context) error {
	if h != nil && h.rt != nil {
		if err := h.rt.Clear(); err != nil {
			return writeJSON(ctx, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		}
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"cleared": true})
}
