package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSyncCursorAuthStateDBDisablesCachedTerminalOutputUIStreamingIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temporary state db: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		db.Close()
		t.Fatalf("create ItemTable: %v", err)
	}
	bootstrap := map[string]any{
		"feature_gates": map[string]any{
			"disable_terminal_output_ui_streaming": map[string]any{
				"value":     true,
				"rule_id":   "local_enabled",
				"groupName": "local_enabled",
			},
			"unrelated_gate": map[string]any{"value": true},
		},
		"hash_used": "none",
	}
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		db.Close()
		t.Fatalf("encode bootstrap: %v", err)
	}
	if _, err := db.Exec("INSERT INTO ItemTable(key, value) VALUES(?, ?)", cursorStateStatsigBootstrapKey, raw); err != nil {
		db.Close()
		t.Fatalf("insert bootstrap: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close setup db: %v", err)
	}

	values := map[string]string{"cursorAuth/cachedEmail": "local@example.com"}
	if err := syncCursorAuthStateDB(path, values); err != nil {
		t.Fatalf("first state sync: %v", err)
	}
	first := readCursorStatsigBootstrapForTest(t, path)
	assertCursorStatsigGateValueForTest(t, first, "disable_terminal_output_ui_streaming", false)
	assertCursorStatsigGateValueForTest(t, first, "unrelated_gate", true)

	if err := syncCursorAuthStateDB(path, values); err != nil {
		t.Fatalf("second state sync: %v", err)
	}
	second := readCursorStatsigBootstrapForTest(t, path)
	if string(second) != string(first) {
		t.Fatalf("repeated sync changed bootstrap:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func readCursorStatsigBootstrapForTest(t *testing.T, path string) []byte {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	var raw []byte
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&raw); err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	return raw
}

func assertCursorStatsigGateValueForTest(t *testing.T, raw []byte, name string, want bool) {
	t.Helper()
	var payload struct {
		FeatureGates map[string]struct {
			Value bool `json:"value"`
		} `json:"feature_gates"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	gate, ok := payload.FeatureGates[name]
	if !ok {
		t.Fatalf("missing gate %q", name)
	}
	if gate.Value != want {
		t.Fatalf("gate %q value=%t, want %t", name, gate.Value, want)
	}
}
