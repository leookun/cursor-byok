package tool

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cursor/internal/backend/server"
)

func TestToolHandler_ListAndToggle(t *testing.T) {
	rt := NewRuntime()
	rt.RegisterBuiltinTools()
	h := NewHandler(rt)
	mux := server.New(h.Routes()...)

	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list struct {
		Tools []*ToolEntry `json:"tools"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Tools) == 0 {
		t.Fatal("expected builtin tools")
	}
	name := list.Tools[0].Name

	body, _ := json.Marshal(map[string]any{"enabled": false})
	req = httptest.NewRequest(http.MethodPost, "/api/tools/"+name+"/toggle", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("toggle status=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tools/mcp-servers", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("mcp servers status=%d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tools/cache/stats", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cache stats status=%d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/tools/cache/clear", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cache clear status=%d", rr.Code)
	}
}
