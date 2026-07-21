// spawner.go defines shared types for AOS member task spawning via context-based
// injection. The forwarder layer stores a spawner function in context before
// calling VirtualModel.Execute; AOSModel extracts it when executionMode is
// "cursor_task" to emit Cursor-native Task tool calls instead of callAdapter.
//
// Phase 26b: Context injection avoids import cycles between forwarder and
// vm/aos packages — both import the parent vm package.
package virtualmodel

import "context"

// AOSMemberSpawnerFunc is a function that spawns an AOS member task via the
// Cursor-native Task mechanism. Implemented by the forwarder in
// runProviderStream, wrapping Service.EmitMemberSpawn + ActiveStream.
//
// Parameters:
//   - taskID: AOS task identifier (for result correlation)
//   - memberID: AOS member identifier
//   - prompt: Full member prompt (already injected with anti-re-entry suffix)
//   - modelID: Physical model/adapter ID (already validated no AOS re-entry)
//   - description: Short human-readable task description
//
// Returns:
//   - correlationKey: deterministic AOS result correlation key. Production
//     currently uses the generated Cursor Task ToolCallId (the same key used
//     by AOSResultRegistry), not ExecServerMessage.exec_id.
//   - error: Spawn error
type AOSMemberSpawnerFunc func(taskID, memberID, prompt, modelID, description string) (string, error)

type aosMemberSpawnerKey struct{}

// WithAOSMemberSpawner stores an AOSMemberSpawnerFunc in context.
// Called by the forwarder in runProviderStream before starting the provider
// stream for an AOS virtual model.
func WithAOSMemberSpawner(ctx context.Context, spawner AOSMemberSpawnerFunc) context.Context {
	return context.WithValue(ctx, aosMemberSpawnerKey{}, spawner)
}

// GetAOSMemberSpawner extracts an AOSMemberSpawnerFunc from context.
// Called by AOSModel.executeMemberTask when executionMode is "cursor_task".
// Returns nil if no spawner is present in context.
func GetAOSMemberSpawner(ctx context.Context) AOSMemberSpawnerFunc {
	if s, ok := ctx.Value(aosMemberSpawnerKey{}).(AOSMemberSpawnerFunc); ok {
		return s
	}
	return nil
}

// ---------------------------------------------------------------------------
// AOS Depth context — Phase 26g re-entry guard
// ---------------------------------------------------------------------------

type aosDepthKey struct{}

// ContextWithAOSDepth stores the AOS nesting depth in context.
// Depth 0 = not in AOS; depth >= 1 = inside AOS execution.
// Called by AOSModel.Execute to mark the context as AOS-scoped.
func ContextWithAOSDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, aosDepthKey{}, depth)
}

// GetAOSDepth extracts the AOS nesting depth from context.
// Returns 0 if no depth is set (default: not inside AOS).
func GetAOSDepth(ctx context.Context) int {
	if d, ok := ctx.Value(aosDepthKey{}).(int); ok {
		return d
	}
	return 0
}
