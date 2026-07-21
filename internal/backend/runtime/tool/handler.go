// Package tool HTTP layer: exposes Tool Runtime list/toggle/MCP/cache as REST routes.
package tool

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"cursor/internal/backend/server"
)

// Handler holds the tool runtime and exposes REST routes.
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
		server.GET("/api/tools",
			server.Name("tool_list"),
			server.HTTP(),
			server.Local(h.list),
		),
		server.POST("/api/tools/{name}/toggle",
			server.Name("tool_toggle"),
			server.HTTP(),
			server.Local(h.toggle),
		),
		server.GET("/api/tools/mcp-servers",
			server.Name("tool_mcp_servers"),
			server.HTTP(),
			server.Local(h.listMCPServers),
		),
		server.POST("/api/tools/mcp-servers/{server}/toggle",
			server.Name("tool_mcp_toggle"),
			server.HTTP(),
			server.Local(h.toggleMCPServer),
		),
		server.GET("/api/tools/cache/stats",
			server.Name("tool_cache_stats"),
			server.HTTP(),
			server.Local(h.cacheStats),
		),
		server.POST("/api/tools/cache/clear",
			server.Name("tool_cache_clear"),
			server.HTTP(),
			server.Local(h.clearCache),
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

func (h *Handler) list(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusOK, map[string]any{"tools": []any{}})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"tools": h.rt.List()})
}

type toggleRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) toggle(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusServiceUnavailable, map[string]any{"error": "tool runtime unavailable"})
	}
	name := strings.TrimSpace(ctx.Request.PathValue("name"))
	if name == "" {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "name is required"})
	}
	var req toggleRequest
	if err := json.NewDecoder(ctx.Request.Body).Decode(&req); err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
	}
	if err := h.rt.Enable(name, req.Enabled); err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"name": name, "enabled": req.Enabled})
}

func (h *Handler) listMCPServers(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusOK, map[string]any{"servers": []any{}})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"servers": h.rt.ListMCPServers()})
}

func (h *Handler) toggleMCPServer(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusServiceUnavailable, map[string]any{"error": "tool runtime unavailable"})
	}
	serverName := strings.TrimSpace(ctx.Request.PathValue("server"))
	if serverName == "" {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "server is required"})
	}
	var req toggleRequest
	if err := json.NewDecoder(ctx.Request.Body).Decode(&req); err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
	}
	if err := h.rt.ToggleMCPServer(serverName, req.Enabled); err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"name": serverName, "enabled": req.Enabled})
}

func (h *Handler) cacheStats(ctx *server.Context) error {
	if h == nil || h.rt == nil {
		return writeJSON(ctx, http.StatusOK, ToolCacheStats{})
	}
	return writeJSON(ctx, http.StatusOK, h.rt.CacheStats())
}

func (h *Handler) clearCache(ctx *server.Context) error {
	if h != nil && h.rt != nil {
		h.rt.ClearCache()
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"cleared": true})
}
