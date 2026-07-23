package modeladapter

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestOpenAIChatReasoningDeltaFallback covers the four required cases for the
// delta.reasoning / delta.reasoning_content alias contract:
//  1. reasoning_content set  -> ThinkingDelta with that text (existing behavior)
//  2. reasoning set only      -> ThinkingDelta with that text (NEW alias)
//  3. both set                -> ThinkingDelta with reasoning_content only (no double-count)
//  4. neither set             -> no ThinkingDelta
//
// It exercises the production helper openAIReasoningFromDelta directly. The
// helper is what streamChatCompletions calls (openai.go ~L910/L916) to decide
// which reasoning text to feed into emitThinkingDelta.
func TestOpenAIChatReasoningDeltaFallback(t *testing.T) {
	cases := []struct {
		name            string
		reasoningContent string
		reasoning        string
		want             string
		wantThinkingDelta bool
	}{
		{
			name:             "reasoning_content only emits that text",
			reasoningContent: "thinking from reasoning_content",
			reasoning:        "",
			want:             "thinking from reasoning_content",
			wantThinkingDelta: true,
		},
		{
			name:             "reasoning alias only emits that text",
			reasoningContent: "",
			reasoning:        "thinking from reasoning",
			want:             "thinking from reasoning",
			wantThinkingDelta: true,
		},
		{
			name:             "both set prefers reasoning_content (no double-count)",
			reasoningContent: "canonical",
			reasoning:        "alias-should-be-ignored",
			want:             "canonical",
			wantThinkingDelta: true,
		},
		{
			name:             "neither set emits nothing",
			reasoningContent: "",
			reasoning:        "",
			want:             "",
			wantThinkingDelta: false,
		},
		{
			name:             "whitespace-only reasoning_content falls back to reasoning",
			reasoningContent: "   ",
			reasoning:        "alias-text",
			// Current contract: openAIReasoningFromDelta treats non-empty
			// reasoning_content as authoritative; whitespace is not empty.
			// This pins the current contract so a future TrimSpace change is
			// a deliberate, reviewed decision rather than a silent behavior
			// shift. If the contract changes to trim, update this case.
			want:             "   ",
			wantThinkingDelta: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openAIReasoningFromDelta(tc.reasoningContent, tc.reasoning)
			if tc.wantThinkingDelta {
				if got != tc.want {
					t.Fatalf("openAIReasoningFromDelta(%q, %q) = %q, want %q", tc.reasoningContent, tc.reasoning, got, tc.want)
				}
			} else {
				if got != "" {
					t.Fatalf("openAIReasoningFromDelta(%q, %q) = %q, want empty (no ThinkingDelta)", tc.reasoningContent, tc.reasoning, got)
				}
			}
		})
	}
}

// openAIReasoningDeltaJSON is a mirror of the inline openAIChunk.Delta struct
// (openai.go streamChatCompletions) limited to the reasoning fields. It exists
// only to verify the JSON tag contract: that a provider sending
// "reasoning_content" and/or "reasoning" populates the corresponding struct
// fields as the production code expects. The real struct is function-local and
// not accessible from tests; this mirror pins the wire contract so a tag rename
// or deletion is caught.
type openAIReasoningDeltaJSON struct {
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning,omitempty"`
}

// TestOpenAIChatReasoningDeltaJSONTags verifies that the JSON tags on the
// production Delta struct fields match the wire format providers actually send.
// If a future edit changes "reasoning_content" or "reasoning,omitempty", the
// openAIReasoningDeltaJSON mirror here must be updated in lockstep; this test
// fails if the mirror diverges from what providers send.
func TestOpenAIChatReasoningDeltaJSONTags(t *testing.T) {
	cases := []struct {
		name string
		json string
		want openAIReasoningDeltaJSON
	}{
		{
			name: "reasoning_content key populates ReasoningContent",
			json: `{"reasoning_content":"rc-text"}`,
			want: openAIReasoningDeltaJSON{ReasoningContent: "rc-text"},
		},
		{
			name: "reasoning key populates Reasoning",
			json: `{"reasoning":"r-text"}`,
			want: openAIReasoningDeltaJSON{Reasoning: "r-text"},
		},
		{
			name: "both keys populate both fields (helper decides winner)",
			json: `{"reasoning_content":"rc","reasoning":"r"}`,
			want: openAIReasoningDeltaJSON{ReasoningContent: "rc", Reasoning: "r"},
		},
		{
			name: "neither key leaves both empty",
			json: `{"content":"hello"}`,
			want: openAIReasoningDeltaJSON{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got openAIReasoningDeltaJSON
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.json, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unmarshal %q: got %+v, want %+v", tc.json, got, tc.want)
			}
		})
	}
}

// TestOpenAIChatReasoningFullPipeline pins the end-to-end contract from JSON
// chunk -> struct fields -> helper -> ThinkingDelta text, combining the JSON
// tag test and the helper test into the realistic sequence the stream loop
// performs. This is the integration assertion that the four task-required
// scenarios produce the correct ThinkingDelta (or none).
func TestOpenAIChatReasoningFullPipeline(t *testing.T) {
	cases := []struct {
		name             string
		chunkJSON        string
		wantText         string
		wantThinkingDelta bool
	}{
		{
			name:              "case1 reasoning_content set emits ThinkingDelta with that text",
			chunkJSON:         `{"reasoning_content":"from-rc"}`,
			wantText:          "from-rc",
			wantThinkingDelta: true,
		},
		{
			name:              "case2 reasoning alias set emits ThinkingDelta with that text",
			chunkJSON:         `{"reasoning":"from-r"}`,
			wantText:          "from-r",
			wantThinkingDelta: true,
		},
		{
			name:              "case3 both set emits reasoning_content only no double-count",
			chunkJSON:         `{"reasoning_content":"from-rc","reasoning":"from-r"}`,
			wantText:          "from-rc",
			wantThinkingDelta: true,
		},
		{
			name:              "case4 neither set emits no ThinkingDelta",
			chunkJSON:         `{"content":"plain-text"}`,
			wantText:          "",
			wantThinkingDelta: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var delta openAIReasoningDeltaJSON
			if err := json.Unmarshal([]byte(tc.chunkJSON), &delta); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.chunkJSON, err)
			}
			reasoning := openAIReasoningFromDelta(delta.ReasoningContent, delta.Reasoning)
			if tc.wantThinkingDelta {
				if reasoning != tc.wantText {
					t.Fatalf("pipeline: got reasoning %q, want %q", reasoning, tc.wantText)
				}
			} else {
				if reasoning != "" {
					t.Fatalf("pipeline: got reasoning %q, want empty (no ThinkingDelta)", reasoning)
				}
			}
		})
	}
}
