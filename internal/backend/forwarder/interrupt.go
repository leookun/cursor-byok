package forwarder

import (
	"context"
	"errors"
	"strings"
)

var errProviderLoopInterrupted = errors.New("provider loop interrupted")

func isTerminalStreamStatus(status StreamStatus) bool {
	switch status {
	case StreamStatusCanceled, StreamStatusCompleted, StreamStatusFailed:
		return true
	default:
		return false
	}
}

func providerLoopInterruptErr(ctx context.Context, stream *ActiveStream, modelCallID string) error {
	if ctx != nil && ctx.Err() != nil {
		return errProviderLoopInterrupted
	}
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) {
		return errProviderLoopInterrupted
	}
	switch stream.Phase {
	case TurnPhaseCanceled, TurnPhaseCompleted, TurnPhaseFailed:
		return errProviderLoopInterrupted
	}
	expectedModelCallID := strings.TrimSpace(modelCallID)
	currentModelCallID := strings.TrimSpace(stream.CurrentModelCallID)
	if expectedModelCallID != "" && currentModelCallID != "" && currentModelCallID != expectedModelCallID {
		return errProviderLoopInterrupted
	}
	return nil
}
