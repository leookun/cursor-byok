// Package workflow 的 HTTP 接入层：将 CRUD + 执行暴露为 REST 路由。
// 注册到现有 server 框架（chi），与 cache/evolver runtime 风格一致。
package workflow

import (
	"encoding/json"
	"fmt"
	"net/http"

	"cursor/internal/backend/server"
)

// Handler 持有工作流存储与执行引擎，提供 REST 路由。
type Handler struct {
	store  *Store
	engine *Engine
}

// NewHandler 创建 HTTP 处理器。
func NewHandler(store *Store) *Handler {
	return &Handler{store: store, engine: NewEngine(store)}
}

// Routes 返回该 Handler 提供的所有路由选项（供 host 注册到 server.New）。
func (h *Handler) Routes() []server.Option {
	return []server.Option{
		server.GET("/api/workflows",
			server.Name("workflow_list"),
			server.HTTP(),
			server.Local(h.list),
		),
		server.GET("/api/workflows/{id}",
			server.Name("workflow_get"),
			server.HTTP(),
			server.Local(h.get),
		),
		server.POST("/api/workflows",
			server.Name("workflow_create"),
			server.HTTP(),
			server.Local(h.create),
		),
		server.PUT("/api/workflows/{id}",
			server.Name("workflow_update"),
			server.HTTP(),
			server.Local(h.update),
		),
		server.DELETE("/api/workflows/{id}",
			server.Name("workflow_delete"),
			server.HTTP(),
			server.Local(h.delete),
		),
		server.POST("/api/workflows/{id}/execute",
			server.Name("workflow_execute"),
			server.HTTP(),
			server.Local(h.execute),
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
	return writeJSON(ctx, http.StatusOK, map[string]any{
		"workflows": h.store.List(),
	})
}

func (h *Handler) get(ctx *server.Context) error {
	id := ctx.Request.PathValue("id")
	wf, ok := h.store.Get(id)
	if !ok {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": "not found"})
	}
	return writeJSON(ctx, http.StatusOK, wf)
}

func (h *Handler) create(ctx *server.Context) error {
	var wf Workflow
	if err := json.NewDecoder(ctx.Request.Body).Decode(&wf); err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
	}
	if wf.ID == "" {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "id is required"})
	}
	if err := h.store.Create(wf); err != nil {
		return writeJSON(ctx, http.StatusConflict, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusCreated, wf)
}

func (h *Handler) update(ctx *server.Context) error {
	id := ctx.Request.PathValue("id")
	var wf Workflow
	if err := json.NewDecoder(ctx.Request.Body).Decode(&wf); err != nil {
		return writeJSON(ctx, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
	}
	wf.ID = id // 路径 ID 为准
	if err := h.store.Update(wf); err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, wf)
}

func (h *Handler) delete(ctx *server.Context) error {
	id := ctx.Request.PathValue("id")
	if err := h.store.Delete(id); err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, map[string]any{"deleted": id})
}

type executeRequest struct {
	Input map[string]any `json:"input"`
}

func (h *Handler) execute(ctx *server.Context) error {
	id := ctx.Request.PathValue("id")
	var req executeRequest
	// 允许空 body。
	_ = json.NewDecoder(ctx.Request.Body).Decode(&req)
	result, err := h.engine.Execute(id, req.Input)
	if err != nil {
		return writeJSON(ctx, http.StatusNotFound, map[string]any{"error": err.Error()})
	}
	return writeJSON(ctx, http.StatusOK, result)
}