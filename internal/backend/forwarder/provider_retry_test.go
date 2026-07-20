package forwarder

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

func TestHandleProviderDoneEventKeepsStreamOpenForTransientRetry(t *testing.T) {
	originalDelays := providerTransientRetryDelays
	providerTransientRetryDelays[0] = time.Hour
	defer func() { providerTransientRetryDelays = originalDelays }()

	service := &Service{
		broker: NewStreamBroker(),
		debug:  newDebugRecorder("", nil, nil),
	}
	stream := &ActiveStream{
		RequestID:            "request-1",
		ConversationID:       "conversation-1",
		Status:               StreamStatusStreaming,
		Phase:                TurnPhaseProviderRunning,
		ProviderActive:       true,
		CurrentProviderToken: 1,
		CurrentModelCallID:   "model-call-1",
		TimerTokens:          map[string]uint64{},
	}
	payload := &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err: providerTerminalError{cause: &modeladapter.HTTPStatusError{
			Prefix:     "openai adapter",
			StatusCode: http.StatusTooManyRequests,
		}},
	}

	if err := service.handleProviderDoneEvent(stream, payload); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status != StreamStatusStreaming {
		t.Fatalf("stream status = %q, want %q", stream.Status, StreamStatusStreaming)
	}
	if stream.Phase != TurnPhaseWaitingExternal {
		t.Fatalf("stream phase = %q, want %q", stream.Phase, TurnPhaseWaitingExternal)
	}
	if stream.PendingProviderAction != providerActionResume {
		t.Fatalf("pending provider action = %q, want %q", stream.PendingProviderAction, providerActionResume)
	}
	if stream.ProviderTransientRetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", stream.ProviderTransientRetryCount)
	}
	if stream.ProviderActive {
		t.Fatal("provider remained active after transient failure")
	}
}

func TestReserveTransientProviderRetry(t *testing.T) {
	stream := &ActiveStream{Status: StreamStatusStreaming}
	err := providerTerminalError{cause: &modeladapter.HTTPStatusError{
		Prefix:     "openai adapter",
		StatusCode: http.StatusTooManyRequests,
	}}

	for wantAttempt := 1; wantAttempt <= providerTransientRetryLimit; wantAttempt++ {
		attempt, delay, status, ok := reserveTransientProviderRetry(stream, unwrapProviderTerminalError(err), false)
		if !ok || attempt != wantAttempt || delay != providerTransientRetryDelays[wantAttempt-1] || status != http.StatusTooManyRequests {
			t.Fatalf("attempt %d = (%d, %s, %d, %t)", wantAttempt, attempt, delay, status, ok)
		}
	}
	if _, _, _, ok := reserveTransientProviderRetry(stream, unwrapProviderTerminalError(err), false); ok {
		t.Fatal("retry limit exceeded but retry was reserved")
	}
}

func TestReserveTransientProviderRetryRejectsUnsafeCases(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		hadOutput  bool
		wantStatus int
	}{
		{name: "partial output", err: &modeladapter.HTTPStatusError{StatusCode: 429}, hadOutput: true, wantStatus: 0},
		{name: "bad request", err: &modeladapter.HTTPStatusError{StatusCode: 400}, wantStatus: 400},
		{name: "untyped error", err: fmt.Errorf("openai adapter status=429"), wantStatus: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := &ActiveStream{Status: StreamStatusStreaming}
			_, _, status, ok := reserveTransientProviderRetry(stream, tt.err, tt.hadOutput)
			if ok || status != tt.wantStatus {
				t.Fatalf("reserveTransientProviderRetry() = status %d, retry %t; want status %d, retry false", status, ok, tt.wantStatus)
			}
		})
	}
}

func TestProviderPassProducedOutput(t *testing.T) {
	if providerPassProducedOutput("", "", "", "", "", false, false) {
		t.Fatal("empty provider pass reported output")
	}
	if !providerPassProducedOutput("partial", "", "", "", "", false, false) {
		t.Fatal("text output was not detected")
	}
	if !providerPassProducedOutput("", "", "", "", "", true, false) {
		t.Fatal("tool invocation was not detected")
	}
}
