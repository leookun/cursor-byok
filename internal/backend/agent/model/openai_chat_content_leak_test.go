package modeladapter

import (
	"reflect"
	"testing"
)

// This file documents the KNOWN LEAK in openAIThinkTagParser's reasoning capture.
//
// When a provider sends reasoning as plain text inside the chat-completion
// `content` field — no `<...>` wrapper, no `<thought>` wrapper, and no
// `reasoning_content` field — the parser cannot tell it apart from normal
// assistant text. Such content flows out as `openAIContentPartText`, which
// the stream loop routes to `emitTextDelta` → `ModelEventKindTextDelta`,
// i.e. it becomes visible to the user as ordinary assistant text.
//
// This is "should not appear" leak #1 from the thinking-chain-display plan.
//
// The tests below:
//   - TestOpenAIChatContentLeak_RawReasoningFlowsAsText: documents the leak.
//   - TestOpenAIChatContentLeak_TaggedReasoningCaptured: positive control —
//     reasoning wrapped in the default `<｜end▁of▁thinking｜>` tags IS
//     captured, proving the parser works when tags are present.
//   - TestOpenAIChatContentLeak_ThoughtTaggedReasoningCaptured: positive
//     control for the `<thought>` tag added in task 1.
//
// When a future fix adds heuristic detection of tagless reasoning (e.g. a
// "Let me think..." probe), TestOpenAIChatContentLeak_RawReasoningFlowsAsText
// should be UPDATED to assert the reasoning is captured as
// `openAIContentPartReasoning` instead of `openAIContentPartText`. Until then
// this test pins the current (leaky) behavior so a regression that silently
// changes the leak surface is caught.

// TestOpenAIChatContentLeak_RawReasoningFlowsAsText documents the known leak:
// reasoning text with NO tag wrapper flows through the parser as TextDelta,
// which would be shown to the user as normal assistant text rather than as a
// thinking/reasoning block.
//
// When a future fix lands heuristic detection of tagless reasoning, update
// the assertion below: the parts should then be openAIContentPartReasoning,
// not openAIContentPartText. Fail the positive-path assertion first to force
// the update — this test is the sentinel.
func TestOpenAIChatContentLeak_RawReasoningFlowsAsText(t *testing.T) {
	parser := &openAIThinkTagParser{}
	// Plain reasoning prose. No <｜end▁of▁thinking｜>, no <thought>, no
	// reasoning_content field — just a delta.content that reads like
	// chain-of-thought. Real providers do send this (see learnings.md gap
	// table: Qwen/GLM raw-text variants).
	input := "Let me think about this step by step. First, I need to consider the constraints. Then, I will derive the answer."
	parts := parser.Consume(input)
	got := partsKindText(parts)

	// KNOWN LEAK: every part is Text, none is Reasoning.
	for _, p := range got {
		if p.Kind != openAIContentPartText {
			t.Fatalf("leak regression: expected all parts to be openAIContentPartText (known leak), got kind %v for text %q",
				p.Kind, p.Text)
		}
	}
	// And specifically: NONE are reasoning.
	for _, p := range got {
		if p.Kind == openAIContentPartReasoning {
			t.Fatalf("leak regression: tagless reasoning was unexpectedly captured as Reasoning — if a fix landed, update this test to assert the new (fixed) behavior")
		}
	}
	// The full input is recovered as text (no bytes dropped).
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartText, input},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw reasoning leak: got %+v, want %+v", got, want)
	}
}

// TestOpenAIChatContentLeak_TaggedReasoningCaptured is the positive control:
// reasoning wrapped in the default `<｜end▁of▁thinking｜>` tags IS captured as
// `openAIContentPartReasoning`. If this test fails, the parser regressed on
// its primary (tagged) capture path — fix the parser, not this test.
func TestOpenAIChatContentLeak_TaggedReasoningCaptured(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(wrapThink("reasoning here"))
	got := partsKindText(parts)
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, "reasoning here"},
		{openAIContentPartThinkingCompleted, ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tagged reasoning (default tag): got %+v, want %+v", got, want)
	}
	// Belt-and-suspenders: at least one part must be reasoning, proving the
	// tagged path is genuinely exercised (not vacuously passing because all
	// parts were Text).
	sawReasoning := false
	for _, p := range got {
		if p.Kind == openAIContentPartReasoning {
			sawReasoning = true
			break
		}
	}
	if !sawReasoning {
		t.Fatalf("tagged reasoning: expected at least one openAIContentPartReasoning, got %+v", got)
	}
}

// TestOpenAIChatContentLeak_ThoughtTaggedReasoningCaptured is the positive
// control for the `<thought>` tag added in task 1. If this fails, the
// multi-tag extension regressed on the `<thought>` pair.
func TestOpenAIChatContentLeak_ThoughtTaggedReasoningCaptured(t *testing.T) {
	parser := &openAIThinkTagParser{}
	parts := parser.Consume(wrapThought("reasoning here"))
	got := partsKindText(parts)
	want := []struct {
		Kind openAIContentPartKind
		Text string
	}{
		{openAIContentPartReasoning, "reasoning here"},
		{openAIContentPartThinkingCompleted, ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tagged reasoning (<thought>): got %+v, want %+v", got, want)
	}
	sawReasoning := false
	for _, p := range got {
		if p.Kind == openAIContentPartReasoning {
			sawReasoning = true
			break
		}
	}
	if !sawReasoning {
		t.Fatalf("tagged reasoning (<thought>): expected at least one openAIContentPartReasoning, got %+v", got)
	}
}
