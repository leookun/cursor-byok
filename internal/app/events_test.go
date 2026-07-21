package app

import (
	"testing"
)

// TestEventConstants_StableValues enforces golden values for every event name
// the frontend subscribes to. These must not change without a coordinated
// frontend bump — a silent rename breaks IPC silently. R16: lifecycle
// unification (extract hardcoded event strings to constants).
//
// TODO: enable -race in CI once CGO is available (R17 spec).
func TestEventConstants_StableValues(t *testing.T) {
	golden := map[string]string{
		"EventProxyState":               EventProxyState,
		"EventUserConfigChanged":        EventUserConfigChanged,
		"EventModelAdapterTestUpdated":  EventModelAdapterTestUpdated,
		"EventUpdateState":              EventUpdateState,
		"EventUpdateProgress":           EventUpdateProgress,
		"EventUpdateReady":              EventUpdateReady,
		"EventUpdateError":              EventUpdateError,
		"EventCursorActivity":           EventCursorActivity,
		"EventPetListChanged":           EventPetListChanged,
		"EventPetStateChanged":          EventPetStateChanged,
		"EventApplicationStarted":       EventApplicationStarted,
	}
	expected := map[string]string{
		"EventProxyState":               "proxy:state",
		"EventUserConfigChanged":        "user-config:changed",
		"EventModelAdapterTestUpdated":  "model-adapter-test:updated",
		"EventUpdateState":              "update:state",
		"EventUpdateProgress":           "update:progress",
		"EventUpdateReady":              "update:ready",
		"EventUpdateError":              "update:error",
		"EventCursorActivity":           "cursor:activity",
		"EventPetListChanged":           "pet:list-changed",
		"EventPetStateChanged":          "pet:state-changed",
		"EventApplicationStarted":       "application:started",
	}
	for name, got := range golden {
		want, ok := expected[name]
		if !ok {
			t.Fatalf("unknown event constant %s in test (update expected map)", name)
		}
		if got != want {
			t.Fatalf("event %s = %q, want %q (do NOT change without coordinated frontend bump)", name, got, want)
		}
	}
	// Sanity: every expected constant is actually declared in the package.
	for name, want := range expected {
		got, ok := golden[name]
		if !ok {
			t.Fatalf("expected event constant %s is not declared in app package", name)
		}
		if got != want {
			t.Fatalf("event %s = %q, want %q", name, got, want)
		}
	}
}
