// Package telemetry HTTP layer: exposes Telemetry Runtime daily summary as REST.
package telemetry

import (
	"encoding/json"
	"fmt"
	"net/http"

	"cursor/internal/backend/server"
)

// Handler holds the telemetry runtime and exposes REST routes.
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
		server.GET("/api/telemetry/daily-summary",
			server.Name("telemetry_daily_summary"),
			server.HTTP(),
			server.Local(h.dailySummary),
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

func (h *Handler) dailySummary(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusOK, &DailySummary{})
	}
	summary := h.rt.GetDailySummary()
	if summary == nil {
		summary = &DailySummary{}
	}
	return writeJSON(ctx, http.StatusOK, summary)
}
