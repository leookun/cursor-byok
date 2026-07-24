package modeladapter

import (
	"testing"
)

// TestApplyAnthropicThinkingConfig covers the three effort branches on a body
// built by the normal construction path, plus the override path symmetry that
// was the original bug (thinking config was only applied inside the
// `if len(body)==0` block, so RequestBodyOverride skipped it entirely).
func TestApplyAnthropicThinkingConfig(t *testing.T) {
	tests := []struct {
		name           string
		body           map[string]any
		thinkingEffort string
		adaptiveEffort string
		wantThinking   any
		wantOutputCfg  bool // expect output_config key present
	}{
		{
			name:           "disabled_writes_disabled_and_drops_output_config",
			body:           map[string]any{"model": "m", "output_config": map[string]any{"effort": "high"}},
			thinkingEffort: "disabled",
			wantThinking:   map[string]any{"type": "disabled"},
			wantOutputCfg:  false,
		},
		{
			name:           "adaptive_writes_adaptive_and_output_config",
			body:           map[string]any{"model": "m"},
			adaptiveEffort: "high",
			wantThinking:   map[string]any{"type": "adaptive", "display": "summarized"},
			wantOutputCfg:  true,
		},
		{
			name:           "empty_effort_no_op",
			body:           map[string]any{"model": "m"},
			wantThinking:   nil,
			wantOutputCfg:  false,
		},
		{
			name:           "disabled_overrides_existing_adaptive_thinking",
			body:           map[string]any{"thinking": map[string]any{"type": "adaptive", "display": "summarized"}, "output_config": map[string]any{"effort": "high"}},
			thinkingEffort: "disabled",
			wantThinking:   map[string]any{"type": "disabled"},
			wantOutputCfg:  false,
		},
		{
			name:           "disabled_normalizes_aliases",
			body:           map[string]any{"model": "m"},
			thinkingEffort: "off",
			wantThinking:   map[string]any{"type": "disabled"},
			wantOutputCfg:  false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := StreamRequest{
				ThinkingEffort:         tc.thinkingEffort,
				AnthropicThinkingEffort: tc.adaptiveEffort,
				RequestKnobs:           map[string]any{},
			}
			applyAnthropicThinkingConfig(tc.body, req)

			gotThinking, hasThinking := tc.body["thinking"]
			if tc.wantThinking == nil {
				if hasThinking {
					t.Fatalf("expected no thinking key, got %v", gotThinking)
				}
			} else {
				if !hasThinking {
					t.Fatalf("expected thinking=%v, key absent", tc.wantThinking)
				}
				if !mapEqual(gotThinking, tc.wantThinking) {
					t.Fatalf("thinking mismatch\nwant: %v\ngot:  %v", tc.wantThinking, gotThinking)
				}
			}

			_, hasOutputCfg := tc.body["output_config"]
			if hasOutputCfg != tc.wantOutputCfg {
				t.Fatalf("output_config presence=%v, want %v", hasOutputCfg, tc.wantOutputCfg)
			}

			if tc.thinkingEffort == "disabled" || tc.thinkingEffort == "off" {
				gotKnob := req.RequestKnobs["thinking_disabled_provider_param"]
				if gotKnob != "thinking.type" {
					t.Fatalf("knob thinking_disabled_provider_param=%v, want thinking.type", gotKnob)
				}
			}
		})
	}
}

// TestApplyAnthropicThinkingConfigOverridePath simulates the RequestBodyOverride
// branch: the adapter receives a body that did NOT go through the normal
// construction (no tools/messages/system written by the adapter). Before the
// fix, thinking config was skipped entirely on this path. Now
// applyAnthropicThinkingConfig runs unconditionally and disables thinking.
func TestApplyAnthropicThinkingConfigOverridePath(t *testing.T) {
	overrideBody := map[string]any{
		"model":         "claude-x",
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":      map[string]any{"type": "adaptive", "display": "summarized"},
		"output_config": map[string]any{"effort": "high"},
		"stream":        true,
	}
	req := StreamRequest{
		ThinkingEffort: "disabled",
		RequestKnobs:   map[string]any{},
	}

	applyAnthropicThinkingConfig(overrideBody, req)

	gotThinking := overrideBody["thinking"]
	if !mapEqual(gotThinking, map[string]any{"type": "disabled"}) {
		t.Fatalf("override path: thinking not forced to disabled, got %v", gotThinking)
	}
	if _, hasOutputCfg := overrideBody["output_config"]; hasOutputCfg {
		t.Fatalf("override path: output_config should be dropped on disabled, still present")
	}
	if gotKnob := req.RequestKnobs["thinking_disabled_provider_param"]; gotKnob != "thinking.type" {
		t.Fatalf("override path: knob=%v, want thinking.type", gotKnob)
	}
}

func mapEqual(a, b any) bool {
	ma, okA := a.(map[string]any)
	mb, okB := b.(map[string]any)
	if !okA || !okB {
		return a == b
	}
	if len(ma) != len(mb) {
		return false
	}
	for k, va := range ma {
		vb, ok := mb[k]
		if !ok || !mapEqual(va, vb) {
			return false
		}
	}
	return true
}
