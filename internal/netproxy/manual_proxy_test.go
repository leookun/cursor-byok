package netproxy

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

// TestSetManualProxy verifies that a manual proxy override takes the highest
// priority and that clearing it reverts to env / system detection.
func TestSetManualProxy(t *testing.T) {
	// Snapshot and restore env so this test is isolated from the host shell.
	savedEnv := envSnapshot()
	defer envRestore(savedEnv)
	// Ensure no env proxy is active so the "cleared" case falls back to direct.
	clearProxyEnv()

	// Always start from a clean state.
	SetManualProxy("", "")
	// Force a rebuild by clearing the cached snapshot.
	defaultResolver.mu.Lock()
	defaultResolver.snapshot = proxySnapshot{}
	defaultResolver.mu.Unlock()

	// 1. Set a manual proxy and confirm CurrentStatus reports it.
	SetManualProxy("http://localhost:8080", "http://localhost:8080")
	status := CurrentStatus()
	if status.Source != "manual" {
		t.Fatalf("expected source=manual, got %q", status.Source)
	}
	if !status.Active {
		t.Fatalf("expected active=true, got false")
	}
	if !strings.Contains(status.HTTPProxy, "localhost:8080") {
		t.Fatalf("expected HTTPProxy to contain localhost:8080, got %q", status.HTTPProxy)
	}
	if !strings.Contains(status.HTTPSProxy, "localhost:8080") {
		t.Fatalf("expected HTTPSProxy to contain localhost:8080, got %q", status.HTTPSProxy)
	}

	// 2. ProxyForRequest must return the manual proxy for a real request URL.
	req := &http.Request{
		URL:    &url.URL{Scheme: "https", Host: "example.com"},
		Method: http.MethodGet,
	}
	proxyURL, err := ProxyForRequest(req)
	if err != nil {
		t.Fatalf("ProxyForRequest returned error: %v", err)
	}
	if proxyURL == nil {
		t.Fatalf("expected non-nil proxy URL from ProxyForRequest")
	}
	if !strings.Contains(proxyURL.String(), "localhost:8080") {
		t.Fatalf("expected proxy URL to contain localhost:8080, got %q", proxyURL.String())
	}

	// 3. Localhost requests must bypass the manual proxy (alwaysNoProxyList).
	localReq := &http.Request{
		URL:    &url.URL{Scheme: "http", Host: "127.0.0.1:9000"},
		Method: http.MethodGet,
	}
	bypassURL, err := ProxyForRequest(localReq)
	if err != nil {
		t.Fatalf("ProxyForRequest localhost returned error: %v", err)
	}
	if bypassURL != nil {
		t.Fatalf("expected nil proxy for localhost, got %q", bypassURL.String())
	}

	// 4. Clearing the manual override falls back to env/system (none here → direct).
	SetManualProxy("", "")
	defaultResolver.mu.Lock()
	defaultResolver.snapshot = proxySnapshot{}
	defaultResolver.mu.Unlock()
	status = CurrentStatus()
	if status.Active {
		t.Fatalf("expected active=false after clearing manual proxy, got true (source=%q http=%q https=%q)",
			status.Source, status.HTTPProxy, status.HTTPSProxy)
	}
	if status.Source != "direct" {
		t.Fatalf("expected source=direct after clearing manual proxy, got %q", status.Source)
	}
}

// TestSetManualProxyOverridesEnv confirms manual proxy wins over HTTP_PROXY.
func TestSetManualProxyOverridesEnv(t *testing.T) {
	savedEnv := envSnapshot()
	defer envRestore(savedEnv)

	// Set an env proxy first.
	os.Setenv("HTTP_PROXY", "http://env-proxy.example:3128")
	os.Setenv("HTTPS_PROXY", "http://env-proxy.example:3128")

	SetManualProxy("", "")
	defaultResolver.mu.Lock()
	defaultResolver.snapshot = proxySnapshot{}
	defaultResolver.mu.Unlock()

	// Without manual override, env should win.
	status := CurrentStatus()
	if status.Source != "env" {
		t.Fatalf("expected source=env without manual override, got %q", status.Source)
	}

	// Now set manual — it must take priority over env.
	SetManualProxy("http://localhost:8080", "http://localhost:8080")
	status = CurrentStatus()
	if status.Source != "manual" {
		t.Fatalf("expected source=manual to override env, got %q", status.Source)
	}
	if !strings.Contains(status.HTTPProxy, "localhost:8080") {
		t.Fatalf("expected HTTPProxy localhost:8080, got %q", status.HTTPProxy)
	}

	// Clear manual — env should take over again.
	SetManualProxy("", "")
	defaultResolver.mu.Lock()
	defaultResolver.snapshot = proxySnapshot{}
	defaultResolver.mu.Unlock()
	status = CurrentStatus()
	if status.Source != "env" {
		t.Fatalf("expected source=env after clearing manual, got %q", status.Source)
	}
}

// envSnapshot captures proxy-related env vars for later restoration.
func envSnapshot() map[string]string {
	keys := []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"}
	saved := make(map[string]string, len(keys))
	for _, k := range keys {
		saved[k] = os.Getenv(k)
	}
	return saved
}

func envRestore(saved map[string]string) {
	for k, v := range saved {
		os.Unsetenv(k)
		if v != "" {
			os.Setenv(k, v)
		}
	}
}

func clearProxyEnv() {
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		os.Unsetenv(k)
	}
}
