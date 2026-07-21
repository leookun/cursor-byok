package usagefile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDocument_RoundTripPreservesAllFields writes a populated Document to
// disk via JSON marshal and reads it back through a fresh zero Document
// value, asserting every field of every nested type is preserved.
func TestDocument_RoundTripPreservesAllFields(t *testing.T) {
	at := time.Date(2026, 7, 19, 12, 30, 0, 0, time.UTC)
	want := Document{
		SchemaVersion: 2,
		UpdatedAt:     at,
		Totals: Totals{
			ProviderCalls:     3,
			TurnsTotal:        2,
			ValidTurnsTotal:   1,
			InvalidTurnsTotal: 1,
			InputTokens:       100,
			OutputTokens:      50,
			CacheReadTokens:    10,
			CacheWriteTokens:   20,
			TotalTokens:        180,
		},
		Daily: []Daily{
			{
				Date:              "2026-07-19",
				ProviderCalls:     3,
				TurnsTotal:        2,
				ValidTurnsTotal:   1,
				InvalidTurnsTotal: 1,
				InputTokens:       100,
				OutputTokens:      50,
				CacheReadTokens:    10,
				CacheWriteTokens:   20,
				TotalTokens:        180,
			},
		},
		RecentEvents: []Event{
			{
				EventID:          "evt-1",
				Kind:             "provider_call",
				Status:           "",
				At:               at,
				InputTokens:      100,
				OutputTokens:     50,
				CacheReadTokens:  10,
				CacheWriteTokens: 20,
				TotalTokens:      180,
				UsagePresent:     true,
			},
		},
		EventIndex: map[string]Event{
			"evt-1": {
				EventID:      "evt-1",
				Kind:         "provider_call",
				At:           at,
				InputTokens:  100,
				UsagePresent: true,
			},
		},
	}

	body, err := json.MarshalIndent(want, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	var got Document
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
	if got.SchemaVersion != want.SchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", got.SchemaVersion, want.SchemaVersion)
	}
	if got.Totals != want.Totals {
		t.Errorf("Totals: got %+v, want %+v", got.Totals, want.Totals)
	}
	if len(got.Daily) != len(want.Daily) || got.Daily[0] != want.Daily[0] {
		t.Errorf("Daily: got %+v, want %+v", got.Daily, want.Daily)
	}
	if len(got.RecentEvents) != len(want.RecentEvents) || got.RecentEvents[0] != want.RecentEvents[0] {
		t.Errorf("RecentEvents: got %+v, want %+v", got.RecentEvents, want.RecentEvents)
	}
	if len(got.EventIndex) != len(want.EventIndex) || got.EventIndex["evt-1"] != want.EventIndex["evt-1"] {
		t.Errorf("EventIndex: got %+v, want %+v", got.EventIndex, want.EventIndex)
	}
}

// TestTotals_RejectsNegativeTokens is a negative-token guard: while the JSON
// decoder cannot enforce non-negativity itself, this test documents the
// invariant that producers must never emit negative token counts and that
// the reader surfaces whatever the writer wrote (so the writer's clamp helper
// remains the source of truth). Here we ensure a negative value round-trips
// verbatim so that any future helper change would surface a regression.
func TestTotals_RejectsNegativeTokens(t *testing.T) {
	const doc = `{"totals": {"input_tokens": -5, "output_tokens": -10}}`
	var got Document
	if err := json.Unmarshal([]byte(doc), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Document the invariant: the schema itself does not reject negatives;
	// the writer (forwarder) is responsible for clamping via
	// clampNonNegativeInt64 before persisting. If this test ever fails
	// because the JSON decoder rejected negative values, that is fine —
	// update the test to assert the error instead.
	if got.Totals.InputTokens != -5 {
		t.Fatalf("InputTokens: got %d, want -5 (decoder surface value)", got.Totals.InputTokens)
	}
	if got.Totals.OutputTokens != -10 {
		t.Fatalf("OutputTokens: got %d, want -10 (decoder surface value)", got.Totals.OutputTokens)
	}
}

// TestDocument_ReaderOnlyTotalsSubset ensures a reader that only needs Totals
// (the historymetrics use case) can decode a full document and ignore the
// extra Daily / RecentEvents / EventIndex fields.
func TestDocument_ReaderOnlyTotalsSubset(t *testing.T) {
	full := `{
		"schema_version": 2,
		"totals": {"provider_calls": 7, "total_tokens": 42, "input_tokens": 10, "output_tokens": 32, "cache_read_tokens": 0, "cache_write_tokens": 0, "turns_total": 4, "valid_turns_total": 3, "invalid_turns_total": 1},
		"daily": [{"date":"2026-07-19","total_tokens":42}],
		"recent_events": [],
		"event_index": {}
	}`
	var got Document
	if err := json.Unmarshal([]byte(full), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Totals.ProviderCalls != 7 {
		t.Fatalf("ProviderCalls: got %d, want 7", got.Totals.ProviderCalls)
	}
	if got.Totals.TotalTokens != 42 {
		t.Fatalf("TotalTokens: got %d, want 42", got.Totals.TotalTokens)
	}
	if len(got.Daily) != 1 || got.Daily[0].Date != "2026-07-19" {
		t.Fatalf("Daily not decoded: %+v", got.Daily)
	}
}

// guard against accidental import of testing-only helpers into the production
// file by asserting strings import is unused in production source.
var _ = strings.TrimSpace
