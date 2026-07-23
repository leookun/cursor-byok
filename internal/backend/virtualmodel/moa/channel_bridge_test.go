// channel_bridge_test.go verifies that CallAdapter only accumulates
// ModelEventKindTextDelta into its non-streaming output and excludes
// reasoning/thinking text (ThinkingDelta, ThinkingCompleted).
package moa

import (
	"context"
	"errors"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

// testModelRouter is a minimal ModelAdapterRouter implementation used to
// feed predefined ModelEvent sequences into the CallAdapter sink without
// touching the real provider stack.
type testModelRouter struct {
	streamFn func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error
}

func (r *testModelRouter) Stream(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
	if r.streamFn == nil {
		return errors.New("streamFn not set")
	}
	return r.streamFn(ctx, req, sink)
}

// TestCallAdapterExcludesReasoningText verifies that the CallAdapter sink
// only accumulates ModelEventKindTextDelta events into the output text.
// ThinkingDelta / ThinkingCompleted events (the reasoning chain) must NOT
// appear in the aggregated result.
func TestCallAdapterExcludesReasoningText(t *testing.T) {
	t.Run("only text delta appears in output", func(t *testing.T) {
		// Given: a router that emits a ThinkingDelta, a TextDelta, and a
		// TurnFinished event in sequence.
		router := &testModelRouter{
			streamFn: func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindThinkingDelta,
					Text: "internal reasoning",
				}); err != nil {
					return err
				}
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindTextDelta,
					Text: "actual answer",
				}); err != nil {
					return err
				}
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindTurnFinished,
				}); err != nil {
					return err
				}
				return nil
			},
		}

		svc := &AdapterChannelService{router: router}

		// When: CallAdapter aggregates the stream into a single result.
		result, err := svc.CallAdapter(
			context.Background(),
			&ChannelInfo{ID: "test-model"},
			nil,
			"",
		)
		if err != nil {
			t.Fatalf("CallAdapter returned error: %v", err)
		}

		// Then: only the TextDelta text appears, NOT the reasoning.
		want := "actual answer"
		if result.Text != want {
			t.Fatalf("expected output %q, got %q (reasoning leaked into output)", want, result.Text)
		}
	})

	t.Run("only thinking events yields empty output", func(t *testing.T) {
		// Given: a router that emits ONLY thinking events (no TextDelta).
		router := &testModelRouter{
			streamFn: func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindThinkingDelta,
					Text: "internal reasoning step 1",
				}); err != nil {
					return err
				}
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindThinkingDelta,
					Text: "internal reasoning step 2",
				}); err != nil {
					return err
				}
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindThinkingCompleted,
				}); err != nil {
					return err
				}
				if err := sink(modeladapter.ModelEvent{
					Kind: modeladapter.ModelEventKindTurnFinished,
				}); err != nil {
					return err
				}
				return nil
			},
		}

		svc := &AdapterChannelService{router: router}

		// When: CallAdapter aggregates the stream into a single result.
		result, err := svc.CallAdapter(
			context.Background(),
			&ChannelInfo{ID: "test-model"},
			nil,
			"",
		)
		if err != nil {
			t.Fatalf("CallAdapter returned error: %v", err)
		}

		// Then: output is empty — reasoning text must not leak.
		if result.Text != "" {
			t.Fatalf("expected empty output, got %q (reasoning leaked into output)", result.Text)
		}
	})
}
