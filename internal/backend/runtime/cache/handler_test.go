package cache

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cursor/internal/backend/server"
)

func TestCacheHandler_StatsAndClear(t *testing.T) {
	dir := t.TempDir()
	rt, err := NewRuntime(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	h := NewHandler(rt)
	mux := server.New(h.Routes()...)

	// GET /api/cache/stats
	req := httptest.NewRequest(http.MethodGet, "/api/cache/stats", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", rr.Code, rr.Body.String())
	}
	var stats statsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}

	// POST /api/cache/clear
	req = httptest.NewRequest(http.MethodPost, "/api/cache/clear", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rr.Code, rr.Body.String())
	}
	var cleared map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("decode clear: %v", err)
	}
	if cleared["cleared"] != true {
		t.Fatalf("expected cleared=true, got %#v", cleared)
	}
}

func TestCacheHandler_NilRuntime(t *testing.T) {
	h := NewHandler(nil)
	mux := server.New(h.Routes()...)
	req := httptest.NewRequest(http.MethodGet, "/api/cache/stats", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("nil rt stats status=%d", rr.Code)
	}
}
