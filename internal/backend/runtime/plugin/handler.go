// Package plugin HTTP layer: exposes the Marketplace as REST routes on the
// existing chi-based server framework, mirroring the workflow runtime handler.
package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"

	"cursor/internal/backend/server"
)

// Handler holds the plugin runtime and exposes REST routes.
type Handler struct {
	rt *Runtime
}

// NewHandler creates the HTTP handler.
func NewHandler(rt *Runtime) *Handler {
	return &Handler{rt: rt}
}

// UnloadAll removes every loaded plugin from the underlying runtime. Called by
// Host.Stop so the shared plugin.Registry is cleared on shutdown.
func (h *Handler) UnloadAll() {
	if h.rt != nil {
		h.rt.UnloadAll()
	}
}

func (h *Handler) Routes() []server.Option {
	return []server.Option{
		server.GET("/api/plugins",
			server.Name("plugin_list"),
			server.HTTP(),
			server.Local(h.list),
		),
		server.GET("/api/plugins/{name}",
			server.Name("plugin_get"),
			server.HTTP(),
			server.Local(h.get),
		),
		server.POST("/api/plugins/{name}/install",
			server.Name("plugin_install"),
			server.HTTP(),
			server.Local(h.install),
		),
		server.POST("/api/plugins/{name}/uninstall",
			server.Name("plugin_uninstall"),
			server.HTTP(),
			server.Local(h.uninstall),
		),
		server.POST("/api/plugins/{name}/toggle",
			server.Name("plugin_toggle"),
			server.HTTP(),
			server.Local(h.toggle),
		),
		server.POST("/api/plugins/{name}/call",
			server.Name("plugin_call"),
			server.HTTP(),
			server.Local(h.call),
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
	return writeJSON(ctx, http.StatusOK, map[string]any{"plugins": h.rt.List()})
}

func (h *Handler) get(ctx *server.Context) error {
	name := ctx.Request.PathValue("name")
	for _, e := range h.rt.List() {
		if e.Name == name {
			return writeJSON(ctx, http.StatusOK, e)
		}
	}
	return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": "plugin not found"})
}

type installBody struct {
	Version  string         `json:"version"`
	Metadata map[string]any `json:"metadata"`
}

func (h *Handler) install(ctx *server.Context) error {
	name := ctx.Request.PathValue("name")
	var body installBody
	_ = json.NewDecoder(ctx.Request.Body).Decode(&body)
	entry, err := h.rt.LoadPlugin(name, Manifest{Version: body.Version, Metadata: body.Metadata})
	if err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, entry)
}

func (h *Handler) uninstall(ctx *server.Context) error {
	name := ctx.Request.PathValue("name")
	if err := h.rt.UnloadPlugin(name); err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"uninstalled": name})
}

func (h *Handler) toggle(ctx *server.Context) error {
	name := ctx.Request.PathValue("name")
	entry, err := h.rt.Toggle(name)
	if err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, entry)
}

type callBody struct {
	Input map[string]any `json:"input"`
}

func (h *Handler) call(ctx *server.Context) error {
	name := ctx.Request.PathValue("name")
	var body callBody
	_ = json.NewDecoder(ctx.Request.Body).Decode(&body)
	out, err := h.rt.CallPlugin(name, body.Input)
	if err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"result": out})
}