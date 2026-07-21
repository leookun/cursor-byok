package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cursor/internal/backend/server"
)

func TestTelemetryHandler_DailySummary(t *testing.T) {
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "telemetry"))
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	h := NewHandler(rt)
	mux := server.New(h.Routes()...)

	req := httptest.NewRequest(http.MethodGet, "/api/telemetry/daily-summary", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var summary DailySummary
	if err := json.Unmarshal(rr.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
