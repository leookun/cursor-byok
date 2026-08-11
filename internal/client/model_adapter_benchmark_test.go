package client

import (
	"testing"

	serverconfig "cursor/internal/backend/server/config"
)

func TestNormalizeModelAdapterTestProviderReasoningPreservesBlank(t *testing.T) {
	adapter := serverconfig.ModelAdapterConfig{Type: "openai", ReasoningEffort: ""}

	if got := normalizeModelAdapterTestProviderReasoning(adapter); got != "" {
		t.Fatalf("reasoning effort = %q, want blank", got)
	}
}
