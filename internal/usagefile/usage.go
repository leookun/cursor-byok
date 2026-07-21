// Package usagefile defines the on-disk schema for usage.json files written
// by the forwarder's UsageFileStore and read by the historymetrics package.
//
// Prior to this package, the writer (forwarder) declared the schema using
// named package-private types while the reader (historymetrics) re-declared
// an inline anonymous struct with only the totals subset. That drift hazard
// is resolved by centralizing the schema here. Both the writer and reader
// import these types so a future field addition is visible to both sides at
// compile time.
package usagefile

import "time"

// Document is the top-level usage.json structure.
type Document struct {
	SchemaVersion int                       `json:"schema_version"`
	UpdatedAt     time.Time                 `json:"updated_at"`
	Totals        Totals                    `json:"totals"`
	Daily         []Daily                   `json:"daily"`
	RecentEvents  []Event                   `json:"recent_events"`
	EventIndex    map[string]Event          `json:"event_index,omitempty"`
}

// Totals aggregates usage across all events recorded in the document.
type Totals struct {
	ProviderCalls     int64 `json:"provider_calls"`
	TurnsTotal        int64 `json:"turns_total"`
	ValidTurnsTotal   int64 `json:"valid_turns_total"`
	InvalidTurnsTotal int64 `json:"invalid_turns_total"`
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CacheReadTokens   int64 `json:"cache_read_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	TotalTokens       int64 `json:"total_tokens"`
}

// Daily is a per-day bucket of usage totals.
type Daily struct {
	Date              string `json:"date"`
	ProviderCalls     int64  `json:"provider_calls"`
	TurnsTotal        int64  `json:"turns_total"`
	ValidTurnsTotal   int64  `json:"valid_turns_total"`
	InvalidTurnsTotal int64  `json:"invalid_turns_total"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	CacheWriteTokens  int64  `json:"cache_write_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
}

// Event describes a single recorded usage event (provider call or finalized
// turn).
type Event struct {
	EventID          string    `json:"event_id"`
	Kind             string    `json:"kind,omitempty"`
	Status           string    `json:"status,omitempty"`
	At               time.Time `json:"at"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	UsagePresent     bool      `json:"usage_present"`
}
