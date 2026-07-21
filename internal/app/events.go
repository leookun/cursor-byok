// Package app — events.go declares every Wails event name the frontend
// subscribes to. Hardcoded strings scattered across runner/bridge/client
// are now centralized here so renames can be code-reviewed against a single
// golden table (see TestEventConstants_StableValues).
//
// R16: lifecycle unification. DO NOT change these values without a
// coordinated frontend bump — IPC subscribers match on string equality.
//
// NOTE: the bridge and updater packages also declare their own constants
// with the same string values (e.g. bridge.EventPetListChanged). We keep
// both copies because client/bridge/updater cannot import app (the app
// package imports them). The golden test in events_test.go enforces that
// the two stay in sync via direct string comparison.
package app

// Event name constants. Values are the canonical IPC channel names and must
// match the bridge/updater package constants exactly.
const (
	// EventProxyState emitted by bridge.ProxyService when the proxy runtime
	// state changes (start/stop). Frontend listens on "proxy:state".
	EventProxyState = "proxy:state"
	// EventUserConfigChanged emitted by client.ConfigService when the user
	// config is saved. Frontend listens on "user-config:changed".
	EventUserConfigChanged = "user-config:changed"
	// EventModelAdapterTestUpdated emitted by client benchmark runner.
	EventModelAdapterTestUpdated = "model-adapter-test:updated"
	// Updater events (state/progress/ready/error).
	EventUpdateState    = "update:state"
	EventUpdateProgress = "update:progress"
	EventUpdateReady    = "update:ready"
	EventUpdateError    = "update:error"
	// Bridge events (cursor activity, pet list/state).
	EventCursorActivity  = "cursor:activity"
	EventPetListChanged  = "pet:list-changed"
	EventPetStateChanged = "pet:state-changed"
	// EventApplicationStarted is emitted by the Wails runtime itself on
	// startup. The frontend listens on this name to drive boot-time hooks
	// (e.g. auto-start proxy). Not registered via
	// application.RegisterEvent — used by app.Event.OnApplicationEvent.
	EventApplicationStarted = "application:started"
)
