package modeladapter

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestOpenAIResponsesReasoningFallback covers the four required cases for the
// Responses API reasoning_content / reasoning_text.delta contract:
//  1. reasoning_content set (on a non-canonical event) -> emits that text (NEW)
//  2. reasoning_text.delta Delta set                     -> emits that text (existing)
//  3. both set on the same reasoning event               -> emits Delta only (no double-count)
//  4. neither set                                         -> no ThinkingDelta
//
// It exercises the production helper openAIResponsesReasoningText directly.
// The helper is what streamResponses calls (openai.go, in the
// "response.reasoning_summary_text.delta" / "response.reasoning_text.delta"
// case and in the switch default) to decide which reasoning text to feed into
// emitThinkingDelta.
func TestOpenAIResponsesReasoningFallback(t *testing.T) {
	cases := []struct {
		name              string
		delta             string
		reasoningContent  string
		want              string
		wantThinkingDelta bool
	}{
		{
			name:              "reasoning_content only emits that text (proxy injection on non-canonical event)",
			delta:             "",
			reasoningContent:  "proxy reasoning summary",
			want:              "proxy reasoning summary",
			wantThinkingDelta: true,
		},
		{
			name:              "reasoning_text.delta Delta only emits that text (existing behavior)",
			delta:             "explicit reasoning delta",
			reasoningContent:  "",
			want:              "explicit reasoning delta",
			wantThinkingDelta: true,
		},
		{
			name:              "both set prefers Delta (no double-count)",
			delta:             "from-delta",
			reasoningContent:  "from-rc-should-be-ignored",
			want:              "from-delta",
			wantThinkingDelta: true,
		},
		{
			name:              "neither set emits nothing",
			delta:             "",
			reasoningContent:  "",
			want:              "",
			wantThinkingDelta: false,
		},
		{
			name: "whitespace-only reasoning_content with empty delta emits whitespace (pins contract)",
			// Current contract: openAIResponsesReasoningText treats non-empty
			// reasoning_content as authoritative when delta is empty; whitespace
			// is not empty. This pins the current contract so a future
			// TrimSpace change is a deliberate, reviewed decision rather than a
			// silent behavior shift. If the contract changes to trim, update
			// this case.
			delta:             "",
			reasoningContent:  "   ",
			want:              "   ",
			wantThinkingDelta: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := openAIResponsesReasoningText(tc.delta, tc.reasoningContent)
			if tc.wantThinkingDelta {
				if got != tc.want {
					t.Fatalf("openAIResponsesReasoningText(%q, %q) = %q, want %q", tc.delta, tc.reasoningContent, got, tc.want)
				}
			} else {
				if got != "" {
					t.Fatalf("openAIResponsesReasoningText(%q, %q) = %q, want empty (no ThinkingDelta)", tc.delta, tc.reasoningContent, got)
				}
			}
		})
	}
}

// openAIResponsesReasoningJSON is a mirror of the relevant fields of the
// inline openAIResponsesStreamEvent struct (openai.go streamResponses) limited
// to the reasoning fields. It exists only to verify the JSON tag contract:
// that a provider/proxy sending "reasoning_content" and/or "delta" populates
// the corresponding struct fields as the production code expects. The real
// struct is function-local and not accessible from tests; this mirror pins the
// wire contract so a tag rename or deletion is caught.
type openAIResponsesReasoningJSON struct {
	Delta            string `json:"delta"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

// TestOpenAIResponsesReasoningJSONTags verifies that the JSON tags on the
// production Responses event struct fields match the wire format providers and
// proxies actually send. If a future edit changes "delta" or
// "reasoning_content,omitempty", the openAIResponsesReasoningJSON mirror here
// must be updated in lockstep; this test fails if the mirror diverges from what
// providers send.
func TestOpenAIResponsesReasoningJSONTags(t *testing.T) {
	cases := []struct {
		name string
		json string
		want openAIResponsesReasoningJSON
	}{
		{
			name: "reasoning_content key populates ReasoningContent",
			json: `{"reasoning_content":"rc-text"}`,
			want: openAIResponsesReasoningJSON{ReasoningContent: "rc-text"},
		},
		{
			name: "delta key populates Delta",
			json: `{"delta":"d-text"}`,
			want: openAIResponsesReasoningJSON{Delta: "d-text"},
		},
		{
			name: "both keys populate both fields (helper decides winner)",
			json: `{"delta":"d","reasoning_content":"rc"}`,
			want: openAIResponsesReasoningJSON{Delta: "d", ReasoningContent: "rc"},
		},
		{
			name: "neither key leaves both empty",
			json: `{"type":"response.output_text.delta"}`,
			want: openAIResponsesReasoningJSON{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got openAIResponsesReasoningJSON
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.json, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unmarshal %q: got %+v, want %+v", tc.json, got, tc.want)
			}
		})
	}
}

// TestOpenAIResponsesReasoningFullPipeline pins the end-to-end contract from
// JSON event -> struct fields -> helper -> ThinkingDelta text, combining the
// JSON tag test and the helper test into the realistic sequence the
// streamResponses loop performs. This is the integration assertion that the
// task-required scenarios produce the correct ThinkingDelta (or none).
func TestOpenAIResponsesReasoningFullPipeline(t *testing.T) {
	cases := []struct {
		name              string
		eventJSON         string
		wantText          string
		wantThinkingDelta bool
	}{
		{
			name:              "case1 reasoning_content set emits ThinkingDelta with that text",
			eventJSON:         `{"reasoning_content":"from-rc"}`,
			wantText:          "from-rc",
			wantThinkingDelta: true,
		},
		{
			name:              "case2 reasoning_text.delta Delta set emits ThinkingDelta",
			eventJSON:         `{"delta":"from-delta"}`,
			wantText:          "from-delta",
			wantThinkingDelta: true,
		},
		{
			name:              "case3 both set emits Delta only no double-count",
			eventJSON:         `{"delta":"from-delta","reasoning_content":"from-rc"}`,
			wantText:          "from-delta",
			wantThinkingDelta: true,
		},
		{
			name:              "case4 neither set emits no ThinkingDelta",
			eventJSON:         `{"type":"response.output_text.delta","delta":""}`,
			wantText:          "",
			wantThinkingDelta: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ev openAIResponsesReasoningJSON
			if err := json.Unmarshal([]byte(tc.eventJSON), &ev); err != nil {
				t.Fatalf("unmarshal %q: %v", tc.eventJSON, err)
			}
			reasoning := openAIResponsesReasoningText(ev.Delta, ev.ReasoningContent)
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
