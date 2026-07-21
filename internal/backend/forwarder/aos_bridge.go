// aos_bridge.go implements Phase 26a — protocol bridge between AOS Internal and
// Cursor-native Task tool call mechanism.
//
// AOS Member tasks are compiled into Task tool call arguments that the Forwarder
// emits into the existing streaming protocol path. The bridge includes:
//   - MemberSpawnRequest type describing what AOS wants to spawn
//   - Compile functions converting to TaskArgs-compatible JSON
//   - AOS re-entry guards (Phase 26g minimal slice)
//   - Service.EmitAOSMemberTask hook for Phase 26b
package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
	vm "cursor/internal/backend/virtualmodel"
)

// BlockedAOSModelIDs contains model IDs that MUST NOT be used for AOS member
// spawns. Prevents AOS re-entry (infinite nesting). Extended automatically
// from the VirtualModel Manager when available.
var BlockedAOSModelIDs = map[string]bool{
	"aos": true,
	"moa": true,
}

// DefaultAOSMemberSubagentType is the subagent type for main-agent sessions.
// AOS Members run as full main-agent (not restricted subagents), and the
// "generalPurpose" type is the Cursor default for main-agent Task spawns.
const DefaultAOSMemberSubagentType = "generalPurpose"

// AntiReEntryPromptSuffix is injected into every AOS Member's prompt to prevent
// the spawned agent from re-entering the AOS organization (Phase 26g guard).
// Also instructs the member to respond in the user's language.
const AntiReEntryPromptSuffix = "\n\n[System Constraint] You are an AOS team member working on a specific task. " +
	"You MUST NOT use any AOS or MOA virtual model. " +
	"You MUST NOT re-enter the AOS organization or spawn additional AOS members. " +
	"Do not call any model named 'aos' or 'moa'. " +
	"AOS re-entry is strictly forbidden. " +
	"IMPORTANT: Respond in the SAME language the user used in their original request. " +
	"If the user wrote in Chinese, respond in Chinese. If in English, respond in English. " +
	"Never switch languages unless explicitly asked."

// MemberSpawnRequest describes a member task to spawn via the Cursor Task
// mechanism. The member runs as a main-agent session (full tool chain,
// independent context, can spawn Cursor-native subagents) — NOT as a
// restricted subagent.
//
// Phase 26b: AOS.executeMemberTask creates a MemberSpawnRequest and passes
// it to Service.EmitAOSMemberTask (or the AOSTaskEmitter interface) instead
// of calling callAdapter directly.
type MemberSpawnRequest struct {
	// TaskID is the AOS task identifier (for result correlation).
	TaskID string

	// MemberID is the AOS member identifier.
	MemberID string

	// Prompt is the full prompt for the member (system prompt + task description).
	// The bridge automatically appends AntiReEntryPromptSuffix.
	Prompt string

	// ModelID is the model/adapter ID to use for this member.
	// Must NOT be a virtual model (ValidateNoAOSReEntry).
	ModelID string

	// Description is a short human-readable description for the Task tool call.
	Description string

	// SubagentType is optional. When empty, defaults to DefaultAOSMemberSubagentType
	// ("generalPurpose") which spawns a main-agent session with full tool access.
	// Do NOT set this to a restricted subagent type unless the member is
	// intentionally limited (not the default AOS Member semantic).
	SubagentType string
}

// compileAOSMemberTaskArgs compiles a MemberSpawnRequest into a map compatible
// with buildTaskArgsFromMap. The result can be serialized to JSON for use as
// Task tool call arguments.
//
// Phase 26d — Model Mapping:
//
// When ModelID/AdapterID is non-empty, the compiled args include both:
//   - "model": the model ID (read by openTask → SubagentArgs.ModelId for the
//     spawned session's primary model). This is the protocol-supported field
//     that actually controls model resolution.
//   - "subagent_model_overrides": an array of SubagentModelOverride-compatible
//     entries for traceability and future propagation. This mirrors what the
//     Cursor client sends in AgentRunRequest.subagent_model_overrides.
//
// Mapping rule (documented in ADR-046 §Phase 26d):
//
//	Member.AdapterID → subagent_model_overrides[subagent_type].model.model_id
//
// This reuses the user-configured ModelAdapter via existing ChannelResolver;
// no new Model Registry is created (ADR-002).
//
// The forwarder's rewriteTaskInvocationModelForDisplay and openTask both
// understand "model" (read from args["model"]) and subagent_model_overrides
// (read from stream.SubagentModelOverrides). Since we set "model" directly,
// the spawned session resolves the correct model even when the parent stream
// has no matching override entry.
func compileAOSMemberTaskArgs(req MemberSpawnRequest) map[string]any {
	subagentType := strings.TrimSpace(req.SubagentType)
	if subagentType == "" {
		subagentType = DefaultAOSMemberSubagentType
	}

	// Append anti-re-entry constraint to the prompt
	prompt := strings.TrimSpace(req.Prompt)
	if !strings.Contains(prompt, "AOS re-entry") {
		prompt += AntiReEntryPromptSuffix
	}

	modelID := strings.TrimSpace(req.ModelID)

	args := map[string]any{
		"prompt":        prompt,
		"description":   strings.TrimSpace(req.Description),
		"subagent_type": subagentType,
		"model":         modelID, // primary model field (protocol-supported)
		"readonly":      false,   // member runs as full agent, not readonly
	}

	// Phase 26d: inject subagent_model_overrides for model mapping traceability.
	// When modelID is non-empty, generate a SubagentModelOverride entry that
	// maps the member's subagent_type → model_id. This entry:
	//   - Documents the mapping in the Task tool call JSON for debugging
	//   - Mirrors the Cursor client-side subagent_model_overrides structure
	//   - Does NOT replace "model" — "model" is the primary resolution field
	//
	// The override is NOT injected into stream.SubagentModelOverrides to avoid
	// race conditions during parallel member spawns (WorkflowScheduler runs
	// multiple executeMemberTask in parallel goroutines, and mutating the
	// shared stream map would race).
	if modelID != "" {
		overrides := []map[string]any{
			{
				"subagent_type": subagentType,
				"model": map[string]any{
					"model_id": modelID,
				},
			},
		}
		args["subagent_model_overrides"] = overrides
	}

	// Attach AOS metadata for traceability
	meta := map[string]string{
		"aos_task_id":   req.TaskID,
		"aos_member_id": req.MemberID,
	}
	args["aos_metadata"] = meta

	return args
}

// CompileAOSMemberTaskJSON compiles a MemberSpawnRequest into a JSON raw
// message compatible with Task tool call ArgsJSON. Use this when you need
// the raw JSON for a ToolInvocation.
func CompileAOSMemberTaskJSON(req MemberSpawnRequest) (json.RawMessage, error) {
	args := compileAOSMemberTaskArgs(req)
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal AOS member task args: %w", err)
	}
	return json.RawMessage(raw), nil
}

// BuildAOSMemberTaskArgs compiles a MemberSpawnRequest into a *agentv1.TaskArgs
// proto message, suitable for direct use with TaskToolCall construction.
func BuildAOSMemberTaskArgs(req MemberSpawnRequest) *agentv1.TaskArgs {
	args := compileAOSMemberTaskArgs(req)
	return buildTaskArgsFromMap(args)
}

// AOSTaskEmitter is the interface that AOS runtime uses to emit Task tool calls
// to the Cursor Agent Runtime via the Forwarder.
//
// Phase 26b: Inject this into AOSModel so executeMemberTask can emit Task tool
// calls instead of calling callAdapter. The Service implements this interface.
type AOSTaskEmitter interface {
	// EmitMemberSpawn emits a member Task tool call into the active stream's
	// tool invocation pipeline. The resulting deterministic ToolCallId is the
	// AOS result correlation key; the method itself returns only an error.
	// The caller must validate no AOS re-entry before calling this method.
	EmitMemberSpawn(stream *ActiveStream, req MemberSpawnRequest) error
}

// Ensure Service implements AOSTaskEmitter.
var _ AOSTaskEmitter = (*Service)(nil)

// EmitMemberSpawn implements AOSTaskEmitter. It compiles the MemberSpawnRequest
// into a Task tool call and dispatches it through the existing forwarder tool
// invocation pipeline (handleToolInvocation), reusing the exec bridge and
// streaming protocol.
//
// The caller MUST validate no AOS re-entry first (ValidateNoAOSReEntry).
// This method does NOT validate re-entry itself, allowing the caller to batch
// validation or handle errors differently.
//
// Phase 26b: AOS.executeMemberTask should call this instead of m.callAdapter.
func (service *Service) EmitMemberSpawn(stream *ActiveStream, req MemberSpawnRequest) error {
	if service == nil {
		return fmt.Errorf("forwarder service is nil")
	}
	if stream == nil {
		return fmt.Errorf("active stream is nil")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return fmt.Errorf("member spawn request has empty model ID")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return fmt.Errorf("member spawn request has empty prompt")
	}

	// Compile the spawn request into Task tool call args
	argsJSON, err := CompileAOSMemberTaskJSON(req)
	if err != nil {
		return fmt.Errorf("compile AOS member task: %w", err)
	}

	// Create a tool invocation that looks like a model-produced Task call
	invocation := runtimecore.ToolInvocation{
		CallID:      fmt.Sprintf("aos-member-%s-%s", req.TaskID, req.MemberID),
		ToolName:    "Task",
		ArgsJSON:    argsJSON,
		ModelCallID: stream.CurrentModelCallID,
	}

	// Dispatch through the existing tool invocation pipeline.
	// This reuses the exec bridge (openTask → SubagentArgs), the pending
	// exec tracking, and the streaming protocol — no parallel path needed.
	return service.handleToolInvocation(stream, invocation)
}

// ValidateNoAOSReEntry checks that the given modelID is not a blocked or
// virtual model. This is the primary re-entry guard for AOS member spawns
// (Phase 26g minimal slice).
//
// Returns an error if the model ID is:
//   - In the BlockedAOSModelIDs static set ("aos", "moa")
//   - Registered as a virtual model (if vmManager is non-nil)
func ValidateNoAOSReEntry(modelID string, vmManager *vm.Manager) error {
	id := strings.TrimSpace(modelID)
	if id == "" {
		return fmt.Errorf("model ID is empty")
	}

	// Static blocklist (fast path)
	if BlockedAOSModelIDs[id] {
		return fmt.Errorf("model %q is on the blocklist: AOS re-entry is forbidden", id)
	}

	// Dynamic virtual model check (catches any registered VM, not just aos/moa)
	if vmManager != nil {
		if vmManager.IsVirtualModel(id) {
			return fmt.Errorf("model %q is a virtual model: AOS re-entry is forbidden", id)
		}
	}

	return nil
}

// InjectAOSAntiReEntryPrompt appends anti-re-entry constraints to a system
// prompt. This is a compile-time guard (Phase 26g) that hardcodes the
// restriction in the prompt text, making it visible to the spawned model.
//
// The constraint warns against:
//   - Using AOS/MOA virtual models
//   - Re-entering the AOS organization
//   - Spawning additional AOS members
//
// Returns the original prompt with the constraint appended (no-op if the
// constraint is already present).
func InjectAOSAntiReEntryPrompt(systemPrompt string) string {
	trimmed := strings.TrimSpace(systemPrompt)
	if strings.Contains(trimmed, "AOS re-entry") {
		return systemPrompt
	}
	if trimmed == "" {
		return strings.TrimSpace(AntiReEntryPromptSuffix)
	}
	return systemPrompt + AntiReEntryPromptSuffix
}
