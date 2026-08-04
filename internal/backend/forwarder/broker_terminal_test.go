package forwarder

import (
	"testing"

	"cursor/gen/agentv1"
)

func TestStreamBrokerTerminalOperationsAreIdempotent(t *testing.T) {
	testCases := []struct {
		name       string
		first      func(*StreamBroker, string) error
		second     func(*StreamBroker, string) error
		wantStatus StreamStatus
		wantCode   string
	}{
		{
			name:  "complete then fail",
			first: func(broker *StreamBroker, requestID string) error { return broker.Complete(requestID, "", "") },
			second: func(broker *StreamBroker, requestID string) error {
				return broker.Fail(requestID, "late_failure", "late failure")
			},
			wantStatus: StreamStatusCompleted,
		},
		{
			name: "fail then complete",
			first: func(broker *StreamBroker, requestID string) error {
				return broker.Fail(requestID, "provider_error", "provider failed")
			},
			second:     func(broker *StreamBroker, requestID string) error { return broker.Complete(requestID, "", "") },
			wantStatus: StreamStatusFailed,
			wantCode:   "provider_error",
		},
		{
			name:  "cancel then fail",
			first: func(broker *StreamBroker, requestID string) error { return broker.Cancel(requestID, "user aborted") },
			second: func(broker *StreamBroker, requestID string) error {
				return broker.Fail(requestID, "late_failure", "late failure")
			},
			wantStatus: StreamStatusCanceled,
			wantCode:   "canceled",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			broker := NewStreamBroker()
			stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
			if err != nil {
				t.Fatalf("OpenStream() error = %v", err)
			}
			if err := testCase.first(broker, stream.RequestID); err != nil {
				t.Fatalf("first terminal operation error = %v", err)
			}
			if err := testCase.second(broker, stream.RequestID); err != nil {
				t.Fatalf("late terminal operation error = %v", err)
			}

			stream.mu.Lock()
			status := stream.Status
			stream.mu.Unlock()
			if status != testCase.wantStatus {
				t.Fatalf("status = %q, want %q", status, testCase.wantStatus)
			}
			events, err := broker.ReadFromCursor(stream.RequestID, 0)
			if err != nil {
				t.Fatalf("ReadFromCursor() error = %v", err)
			}
			endCount := 0
			endCode := ""
			for _, event := range events {
				if !event.End {
					continue
				}
				endCount++
				endCode = event.TerminalErrorCode
			}
			if endCount != 1 {
				t.Fatalf("terminal event count = %d, want 1; events = %#v", endCount, events)
			}
			if endCode != testCase.wantCode {
				t.Fatalf("terminal code = %q, want %q", endCode, testCase.wantCode)
			}
		})
	}
}

func TestTerminalCompletionAfterCheckpointIsIdempotent(t *testing.T) {
	broker := NewStreamBroker()
	service := &Service{broker: broker}
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	completion := pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 3, OutputTokens: 2}}
	if err := service.finishSuccessfulTurnAfterCheckpoint(stream, completion); err != nil {
		t.Fatalf("first terminal completion error = %v", err)
	}
	if err := service.finishSuccessfulTurnAfterCheckpoint(stream, completion); err != nil {
		t.Fatalf("duplicate terminal completion error = %v", err)
	}

	events, err := broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	turnEndedCount := 0
	endCount := 0
	for _, event := range events {
		if event.End {
			endCount++
		}
		if event.Message != nil && event.Message.GetInteractionUpdate().GetTurnEnded() != nil {
			turnEndedCount++
		}
	}
	if turnEndedCount != 1 || endCount != 1 {
		t.Fatalf("terminal events = turn_ended:%d end:%d, want 1 each", turnEndedCount, endCount)
	}
}

func TestCanceledCompletionAfterCheckpointIsIdempotent(t *testing.T) {
	broker := NewStreamBroker()
	service := &Service{broker: broker}
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := service.finishCanceledTurnAfterCheckpoint(stream, "user aborted"); err != nil {
		t.Fatalf("first cancellation completion error = %v", err)
	}
	if err := service.finishCanceledTurnAfterCheckpoint(stream, "late cancellation"); err != nil {
		t.Fatalf("duplicate cancellation completion error = %v", err)
	}

	events, err := broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	endCount := 0
	for _, event := range events {
		if event.End {
			endCount++
		}
	}
	if endCount != 1 {
		t.Fatalf("cancellation terminal event count = %d, want 1", endCount)
	}
}

func TestStreamBrokerRejectsLateProviderOutputAfterCompletion(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("request-1", "conversation-1", 1, "model", "model", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := broker.Complete(stream.RequestID, "", ""); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := broker.Publish(stream.RequestID, StreamEvent{Message: buildTextDeltaMessage("late provider output")}); err != nil {
		t.Fatalf("late Publish() error = %v", err)
	}
	events, err := broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	for _, event := range events {
		if event.Message != nil && event.Message.GetInteractionUpdate().GetTextDelta() != nil {
			t.Fatal("late non-terminal provider output was published")
		}
	}
}
