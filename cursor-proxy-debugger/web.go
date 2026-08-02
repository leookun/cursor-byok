package proxydebugger

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"cursor/internal/certs"
)

//go:embed web/*
var webAssets embed.FS

func (server *Server) newUIHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", server.handleStatus)
	mux.HandleFunc("GET /api/exchanges", server.handleExchangeList)
	mux.HandleFunc("GET /api/exchanges/{id}", server.handleExchangeDetail)
	mux.HandleFunc("DELETE /api/exchanges", server.handleClearExchanges)
	mux.HandleFunc("GET /api/events", server.handleEvents)
	mux.HandleFunc("GET /api/ca.crt", server.handleCACertificate)
	assets, _ := fs.Sub(webAssets, "web")
	fileServer := http.FileServer(http.FS(assets))
	mux.Handle("/", fileServer)
	return securityHeaders(mux)
}

func (server *Server) handleStatus(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"proxyAddr":  server.config.ProxyAddr,
		"uiAddr":     server.config.UIAddr,
		"targetHost": server.config.TargetHost,
		"running":    true,
	})
}

func (server *Server) handleExchangeList(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, server.store.summaries())
}

func (server *Server) handleExchangeDetail(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimSpace(request.PathValue("id"))
	exchange, ok := server.store.get(id)
	if !ok {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "请求记录不存在"})
		return
	}
	writeJSON(writer, http.StatusOK, exchange)
}

func (server *Server) handleClearExchanges(writer http.ResponseWriter, _ *http.Request) {
	server.store.clear()
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "当前响应不支持流式刷新", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	updates, unsubscribe := server.store.subscribe()
	defer unsubscribe()
	fmt.Fprint(writer, "event: ready\ndata: {}\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-updates:
			if !open {
				return
			}
			payload, _ := json.Marshal(event)
			fmt.Fprintf(writer, "event: update\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(writer, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (server *Server) handleCACertificate(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/x-x509-ca-cert")
	writer.Header().Set("Content-Disposition", `attachment; filename="cursor-local-proxy-ca.crt"`)
	_, _ = writer.Write(certs.EmbeddedCACertPEM())
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'")
		next.ServeHTTP(writer, request)
	})
}
