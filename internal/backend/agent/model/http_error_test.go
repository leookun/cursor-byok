package modeladapter

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBuildHTTPStatusErrorPreservesStatus(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"type":"engine_overloaded_error"}}`)),
	}

	err := buildHTTPStatusError("openai adapter", resp)
	status, ok := ProviderHTTPStatus(err)
	if !ok || status != http.StatusTooManyRequests {
		t.Fatalf("ProviderHTTPStatus() = (%d, %t), want (%d, true)", status, ok, http.StatusTooManyRequests)
	}
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T, want *HTTPStatusError", err)
	}
	if got := err.Error(); !strings.Contains(got, "openai adapter status=429 body=") {
		t.Fatalf("Error() = %q, want status and body summary", got)
	}
}
