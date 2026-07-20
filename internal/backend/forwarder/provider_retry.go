package forwarder

import (
	"errors"
	"strings"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
)

const providerTransientRetryLimit = 3

var providerTransientRetryDelays = [...]time.Duration{
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

func retryableProviderHTTPStatus(err error) (int, bool) {
	status, ok := modeladapter.ProviderHTTPStatus(err)
	if !ok {
		return 0, false
	}
	switch status {
	case 429, 502, 503, 504:
		return status, true
	default:
		return status, false
	}
}

func providerPassProducedOutput(
	text string,
	reasoning string,
	reasoningSignature string,
	reasoningItemID string,
	finishReason string,
	hadToolInvocation bool,
	terminalToolInvocation bool,
) bool {
	return strings.TrimSpace(text) != "" ||
		strings.TrimSpace(reasoning) != "" ||
		strings.TrimSpace(reasoningSignature) != "" ||
		strings.TrimSpace(reasoningItemID) != "" ||
		strings.TrimSpace(finishReason) != "" ||
		hadToolInvocation ||
		terminalToolInvocation
}

func reserveTransientProviderRetry(stream *ActiveStream, err error, passProducedOutput bool) (int, time.Duration, int, bool) {
	if stream == nil || passProducedOutput {
		return 0, 0, 0, false
	}
	status, retryable := retryableProviderHTTPStatus(err)
	if !retryable {
		return 0, 0, status, false
	}

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if isTerminalStreamStatus(stream.Status) || stream.ProviderTransientRetryCount >= providerTransientRetryLimit {
		return 0, 0, status, false
	}
	stream.ProviderTransientRetryCount++
	attempt := stream.ProviderTransientRetryCount
	stream.PendingProviderAction = providerActionResume
	stream.UpdatedAt = time.Now().UTC()
	return attempt, providerTransientRetryDelays[attempt-1], status, true
}

func resetTransientProviderRetry(stream *ActiveStream) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	stream.ProviderTransientRetryCount = 0
	stream.mu.Unlock()
}

func unwrapProviderTerminalError(err error) error {
	var providerErr providerTerminalError
	if errors.As(err, &providerErr) && providerErr.cause != nil {
		return providerErr.cause
	}
	return err
}
