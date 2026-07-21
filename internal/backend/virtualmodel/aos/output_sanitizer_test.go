package aos

import (
	"strings"
	"testing"
)

// S1 Happy: clean user answer unchanged
func TestSanitizeUserFacingText_HappyCleanPasses(t *testing.T) {
	in := "你好，今天天气不错"
	if got := SanitizeUserFacingText(in); got != in {
		t.Fatalf("clean text changed: got %q want %q", got, in)
	}
	in2 := "Here is the fix for your bug."
	if got := SanitizeUserFacingText(in2); got != in2 {
		t.Fatalf("clean English changed: got %q want %q", got, in2)
	}
}

// S2 Prompt leak markers → empty
func TestSanitizeUserFacingText_PromptLeakStripped(t *testing.T) {
	leaks := []string{
		"You are an AI Team Leader (AOS Leader), simultaneously a Chief Architect",
		"## IntentGate — Classify Before Planning\nBefore planning, FIRST classify",
		"[System Constraint] You are an AOS team member working on a specific task.",
		"AOS re-entry is strictly forbidden. Do not call aos.",
		"You are reviewing your team members' task outputs. Evaluate each output for:",
		"You are merging your team members' outputs into a single, cohesive final result.",
	}
	for _, in := range leaks {
		if got := SanitizeUserFacingText(in); got != "" {
			t.Fatalf("leak not stripped: input %q got %q", in, got)
		}
	}
}

// S3 Empty simple-intent JSON with no usable reply → empty
func TestSanitizeUserFacingText_EmptySimpleIntentFallback(t *testing.T) {
	in := `{"intent":"simple","reply":""}`
	if got := SanitizeUserFacingText(in); got != "" {
		t.Fatalf("empty simple reply should be empty, got %q", got)
	}
}

// S4 PhaseText-like status → empty (also via SanitizePhaseText if exported, or SanitizeUserFacingText)
func TestSanitizeUserFacingText_PhaseTextBecomesEmpty(t *testing.T) {
	in := "[AOS] completed in 3 sprints (fallback)"
	if got := SanitizeUserFacingText(in); got != "" {
		t.Fatalf("PhaseText must become empty, got %q", got)
	}
	// SanitizePhaseText if you add it must also return ""
	if got := SanitizePhaseText(in); got != "" {
		t.Fatalf("SanitizePhaseText must return empty, got %q", got)
	}
	if got := SanitizePhaseText("anything"); got != "" {
		t.Fatalf("SanitizePhaseText always empty, got %q", got)
	}
}

// S5 Valid simple intent JSON → extract reply
func TestSanitizeUserFacingText_ValidReplyPreserved(t *testing.T) {
	in := `{"intent": "simple", "reply": "Hello world"}`
	if got := SanitizeUserFacingText(in); got != "Hello world" {
		t.Fatalf("got %q want Hello world", got)
	}
}

// S6 Internal markers filtered
func TestSanitizeUserFacingText_InternalMarkersFiltered(t *testing.T) {
	// Pure internal spawn message → empty
	in := "[AOS Member backend spawned via Cursor Task, execID=abc. No result registry — result not collected.]"
	if got := SanitizeUserFacingText(in); got != "" {
		t.Fatalf("internal spawn msg should empty, got %q", got)
	}
}

// S7 Complex JSON plan shell → empty (not user-facing)
func TestSanitizeUserFacingText_JSONPlanShellRemoved(t *testing.T) {
	in := `{"intent":"complex","tasks":[{"id":"t1","role":"dev","description":"x","assignee":"leader","dependencies":[],"priority":"high"}],"architecture":"mvc"}`
	if got := SanitizeUserFacingText(in); got != "" {
		t.Fatalf("plan shell must empty, got %q", got)
	}
}

// S8 Redaction safety: legitimate user content mentioning AOS must NOT be fully stripped
func TestSanitizeUserFacingText_MemberIDRedactionSafety(t *testing.T) {
	in := "Use [AOS Member] notation in your code comments."
	if got := SanitizeUserFacingText(in); got != in {
		t.Fatalf("legitimate content stripped: got %q want %q", got, in)
	}
}

// Empty / whitespace
func TestSanitizeUserFacingText_EmptyInput(t *testing.T) {
	if got := SanitizeUserFacingText(""); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := SanitizeUserFacingText("   \n"); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Idempotent
func TestSanitizeUserFacingText_Idempotent(t *testing.T) {
	in := "Normal answer"
	once := SanitizeUserFacingText(in)
	twice := SanitizeUserFacingText(once)
	if once != twice {
		t.Fatalf("not idempotent: %q vs %q", once, twice)
	}
}

// S9 Path #5: redact configured member IDs/names from merge-style output
func TestRedactTeamIdentity_MemberIDsAndNames(t *testing.T) {
	team := &TeamProfile{
		Members: []MemberConfig{
			{ID: "backend-eng", Name: "Backend Engineer"},
			{ID: "frontend", Name: "UI Specialist"},
		},
	}
	in := "Combining backend-eng and Backend Engineer work with UI Specialist notes."
	got := RedactTeamIdentity(in, team)
	if strings.Contains(got, "backend-eng") || strings.Contains(got, "Backend Engineer") || strings.Contains(got, "UI Specialist") {
		t.Fatalf("member identity not redacted: %q", got)
	}
	if !strings.Contains(got, "Combining") || !strings.Contains(got, "work") {
		t.Fatalf("legitimate content lost: %q", got)
	}
}

// S9b: short generic IDs (e.g. "go") must not be redacted (false-positive guard)
func TestRedactTeamIdentity_SkipsShortIDs(t *testing.T) {
	team := &TeamProfile{
		Members: []MemberConfig{{ID: "go", Name: "ab"}},
	}
	in := "Please go implement ab tests carefully."
	got := RedactTeamIdentity(in, team)
	if got != in {
		t.Fatalf("short identity over-redacted: got %q want %q", got, in)
	}
}

// S9c: nil/empty team is no-op
func TestRedactTeamIdentity_NilTeam(t *testing.T) {
	in := "backend-eng did the work"
	if got := RedactTeamIdentity(in, nil); got != in {
		t.Fatalf("nil team changed text: %q", got)
	}
}

// S10: SanitizeUserFacingTextWithTeam combines leak strip + identity redaction
func TestSanitizeUserFacingTextWithTeam_Combines(t *testing.T) {
	team := &TeamProfile{
		Members: []MemberConfig{{ID: "architect", Name: "Chief Architect"}},
	}
	// Clean answer with member name → redact name, keep content
	in := "The design from architect looks solid."
	got := SanitizeUserFacingTextWithTeam(in, team)
	if strings.Contains(got, "architect") {
		t.Fatalf("member id still present: %q", got)
	}
	// Leak still empties
	if got := SanitizeUserFacingTextWithTeam("You are an AI Team Leader (AOS Leader) secret", team); got != "" {
		t.Fatalf("leak not emptied with team: %q", got)
	}
}

// S11 (B1 regression): legitimate user JSON containing "intent" must NOT be stripped.
// Only full Leader plan shells ({intent + tasks/reply}) are stripped.
func TestSanitizeUserFacingText_LegitUserJSONPreserved(t *testing.T) {
	cases := []string{
		`{"intent":"user-action","action":"run"}`,         // no tasks/reply → not plan shell
		`{"myIntent":"simple","reply":"hi"}`,               // wrong field name → not plan shell
		`Here is the config: {"intent":"simple","reply":"x"} and more text`, // not whole-text JSON
		`{"user":"alice","preferences":{"intent":"greet"}}`, // nested intent, no tasks/reply at top
	}
	for _, in := range cases {
		got := SanitizeUserFacingText(in)
		if got == "" {
			t.Fatalf("legit JSON wrongly stripped: input %q got %q", in, got)
		}
	}
}

// S12 (B2 regression): user prose with "[AOS Member " + space must survive
// when not followed by the spawn-message template.
func TestSanitizeUserFacingText_AOSMemberUserProsePreserved(t *testing.T) {
	// Real user configuring AOS would write this; should NOT be blanked.
	in := "I added [AOS Member backend] to my config file."
	got := SanitizeUserFacingText(in)
	if got == "" {
		t.Fatalf("user prose blanked: input %q got %q", in, got)
	}
	// Actual spawn-message format (from provider.go resolveMemberTask) still stripped.
	spawn := "[AOS Member backend spawned via Cursor Task, execID=abc. No result registry — result not collected.]"
	if got := SanitizeUserFacingText(spawn); got != "" {
		t.Fatalf("spawn message not stripped: got %q", got)
	}
}
