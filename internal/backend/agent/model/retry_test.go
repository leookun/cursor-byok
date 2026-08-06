package modeladapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// retryTestTransport 按调用次数依次返回预设状态码/响应体；前 networkErrs 次返回网络错误。
// 全部离线，不依赖真实网络。
type retryTestTransport struct {
	mu          sync.Mutex
	calls       int
	networkErrs int
	statuses    []int
	bodies      []string
}

func (t *retryTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := t.calls
	t.calls++
	if idx < t.networkErrs {
		return nil, errors.New("connection reset by peer")
	}
	si := idx - t.networkErrs
	if si >= len(t.statuses) {
		si = len(t.statuses) - 1
	}
	body := ""
	if si < len(t.bodies) {
		body = t.bodies[si]
	}
	return &http.Response{
		StatusCode: t.statuses[si],
		Status:     fmt.Sprintf("%d", t.statuses[si]),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

func (t *retryTestTransport) CallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func doRetry(t *testing.T, tr *retryTestTransport) (*http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: tr}
	return doProviderRequestWithRetry(context.Background(), client, "opencode", "req-1", "mc-1",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:9/v1/chat/completions", nil)
		})
}

func TestClassifyUpstream4xx(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "upstream request failed", body: `{"error":{"message":"Upstream request failed"}}`, want: true},
		{name: "eai_again", body: "getaddrinfo EAI_AGAIN", want: true},
		{name: "connection reset", body: "connection reset by peer", want: true},
		{name: "bad gateway", body: "Bad Gateway", want: true},
		{name: "invalid api key", body: `{"error":{"message":"Invalid API key"}}`, want: false},
		{name: "rate limited", body: `{"error":"rate limit exceeded"}`, want: false},
		{name: "empty body", body: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(tc.body))}
			transient, _ := classifyUpstream4xx(resp)
			if transient != tc.want {
				t.Fatalf("classifyUpstream4xx(%q) = %v, want %v", tc.body, transient, tc.want)
			}
		})
	}
}

func TestDoProviderRequestWithRetryRetries5xx(t *testing.T) {
	tr := &retryTestTransport{statuses: []int{500, 200}, bodies: []string{"boom", "ok"}}
	resp, err := doRetry(t, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if tr.CallCount() != 2 {
		t.Fatalf("calls = %d, want 2 (1 retry)", tr.CallCount())
	}
}

func TestDoProviderRequestWithRetrySkipsNonTransient4xx(t *testing.T) {
	tr := &retryTestTransport{statuses: []int{400}, bodies: []string{`{"error":"invalid api key"}`}}
	resp, err := doRetry(t, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if tr.CallCount() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry)", tr.CallCount())
	}
}

func TestDoProviderRequestWithRetryRetriesTransient4xx(t *testing.T) {
	tr := &retryTestTransport{
		statuses: []int{400, 200},
		bodies:   []string{`{"error":{"message":"Upstream request failed"}}`, "ok"},
	}
	resp, err := doRetry(t, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if tr.CallCount() != 2 {
		t.Fatalf("calls = %d, want 2 (1 retry)", tr.CallCount())
	}
}

func TestDoProviderRequestWithRetryNetworkError(t *testing.T) {
	tr := &retryTestTransport{networkErrs: 1, statuses: []int{200}, bodies: []string{"ok"}}
	resp, err := doRetry(t, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if tr.CallCount() != 2 {
		t.Fatalf("calls = %d, want 2 (1 retry)", tr.CallCount())
	}
}

func TestDoProviderRequestWithRetryBuildErrorNoRetry(t *testing.T) {
	client := &http.Client{Transport: &retryTestTransport{statuses: []int{200}, bodies: []string{"ok"}}}
	_, err := doProviderRequestWithRetry(context.Background(), client, "opencode", "req-1", "mc-1",
		func(ctx context.Context) (*http.Request, error) {
			return nil, errors.New("build failed")
		})
	if err == nil || !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("err = %v, want build failure", err)
	}
}