package client

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	serverconfig "cursor/internal/backend/server/config"
)

func TestDiscoverModelAdapterModelsOpenAI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected authorization header")
		}
		if req.Header.Get("X-Tenant") != "tenant-a" {
			t.Fatalf("missing custom header")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen3-max"},{"id":"gpt-5","name":"GPT 5"},{"id":"gpt-5"}]}`))
	}))
	defer server.Close()

	service := &ProxyService{publicClient: server.Client()}
	result, err := service.discoverModelAdapterModels(testDiscoveryAdapter("openai", server.URL+"/v1", "test-key", true))
	if err != nil {
		t.Fatalf("discover models: %v", err)
	}
	if len(result.Models) != 2 {
		t.Fatalf("expected 2 models, got %#v", result.Models)
	}
	if result.Models[0].ID != "gpt-5" || result.Models[0].DisplayName != "GPT 5" {
		t.Fatalf("unexpected first model: %#v", result.Models[0])
	}
	if result.Models[1].ID != "qwen3-max" {
		t.Fatalf("unexpected second model: %#v", result.Models[1])
	}
}

func TestDiscoverModelAdapterModelsAnthropicPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if req.Header.Get("x-api-key") != "anthropic-key" || req.Header.Get("Authorization") != "Bearer anthropic-key" {
			t.Fatalf("unexpected anthropic auth headers")
		}
		if req.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("missing anthropic-version header")
		}
		if req.URL.Query().Get("after_id") == "" {
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet","display_name":"Claude Sonnet"}],"has_more":true,"last_id":"claude-sonnet"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus","display_name":"Claude Opus"}],"has_more":false}`))
	}))
	defer server.Close()

	service := &ProxyService{publicClient: server.Client()}
	result, err := service.discoverModelAdapterModels(testDiscoveryAdapter("anthropic", server.URL, "Bearer anthropic-key", false))
	if err != nil {
		t.Fatalf("discover models: %v", err)
	}
	if len(result.Models) != 2 || result.Models[0].ID != "claude-opus" || result.Models[1].ID != "claude-sonnet" {
		t.Fatalf("unexpected models: %#v", result.Models)
	}
}

func TestDiscoverModelGroupModelsUsesPersistedEmptyGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/models" || req.Header.Get("Authorization") != "Bearer group-key" {
			t.Fatalf("unexpected discovery request: path=%s auth=%s", req.URL.Path, req.Header.Get("Authorization"))
		}
		if req.Header.Get("X-Tenant") != "tenant-a" {
			t.Fatalf("missing persisted group custom header")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()

	store := serverconfig.NewStore(filepath.Join(t.TempDir(), "config.yaml"), t.TempDir())
	config := serverconfig.DefaultConfig()
	config.ModelGroups = []serverconfig.ModelGroupConfig{{
		Name:                 "Empty Group",
		Type:                 "openai",
		BaseURL:              server.URL + "/v1",
		APIKey:               "group-key",
		CustomHeadersEnabled: true,
		CustomHeadersJSON:    `{"X-Tenant":"tenant-a"}`,
	}}
	normalized, err := store.Save(context.Background(), config)
	if err != nil {
		t.Fatalf("save config: %v", err)
	}
	service := &ProxyService{store: store, publicClient: server.Client()}
	result, err := service.DiscoverModelGroupModels(normalized.ModelGroups[0].ID)
	if err != nil {
		t.Fatalf("discover persisted group: %v", err)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "model-a" {
		t.Fatalf("unexpected models: %#v", result.Models)
	}
}

func TestModelDiscoveryEndpointURLFromCustomOpenAIEndpoint(t *testing.T) {
	adapter := testDiscoveryAdapter("openai", "https://provider.example/v4/chat/completions", "key", false)
	adapter.OpenAIEndpoint = "/custom"
	endpoint, err := modelDiscoveryEndpointURL(adapter)
	if err != nil {
		t.Fatalf("build endpoint: %v", err)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	if parsed.String() != "https://provider.example/v4/models" {
		t.Fatalf("unexpected endpoint: %s", parsed.String())
	}
}

func TestModelDiscoveryEndpointURLFollowsConfiguredOpenAIEndpoint(t *testing.T) {
	for _, endpoint := range []string{"/v1/responses", "/v1/chat/completions"} {
		adapter := testDiscoveryAdapter("openai", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "key", false)
		adapter.OpenAIEndpoint = endpoint
		actual, err := modelDiscoveryEndpointURL(adapter)
		if err != nil {
			t.Fatalf("build endpoint for %s: %v", endpoint, err)
		}
		expected := "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1/models"
		if actual != expected {
			t.Fatalf("endpoint %s: expected %s, got %s", endpoint, expected, actual)
		}
	}
}

func TestModelDiscoveryEndpointURLFromArbitraryCustomRequestURL(t *testing.T) {
	adapter := testDiscoveryAdapter("openai", "https://provider.example/openai/v2/generate", "key", false)
	adapter.OpenAIEndpoint = "/custom"
	endpoint, err := modelDiscoveryEndpointURL(adapter)
	if err != nil {
		t.Fatalf("build endpoint: %v", err)
	}
	if endpoint != "https://provider.example/openai/v2/models" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
}

func TestDialModelDiscoveryIPsFallsBackFromIPv6ToIPv4(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	startedAt := time.Now()
	conn, err := dialModelDiscoveryIPs(ctx, "tcp", "443", []net.IP{
		net.ParseIP("2001:db8::1"),
		net.ParseIP("203.0.113.10"),
	}, func(ctx context.Context, _ string, address string) (net.Conn, error) {
		host, _, splitErr := net.SplitHostPort(address)
		if splitErr != nil {
			return nil, splitErr
		}
		if net.ParseIP(host).To4() == nil {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		client, server := net.Pipe()
		go func() {
			<-ctx.Done()
			_ = server.Close()
		}()
		return client, nil
	})
	if err != nil {
		t.Fatalf("dial with IPv4 fallback: %v", err)
	}
	defer conn.Close()
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("IPv4 fallback took too long: %v", elapsed)
	}
}

func TestModelDiscoveryEndpointURLFromVersionedAnthropicBaseURL(t *testing.T) {
	adapter := testDiscoveryAdapter("anthropic", "https://api.anthropic.com/v1", "key", false)
	endpoint, err := modelDiscoveryEndpointURL(adapter)
	if err != nil {
		t.Fatalf("build endpoint: %v", err)
	}
	if endpoint != "https://api.anthropic.com/v1/models?limit=100" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
}

func TestDiscoverModelAdapterModelsDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Store(true)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/models")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	service := &ProxyService{publicClient: server.Client()}
	_, err := service.discoverModelAdapterModels(testDiscoveryAdapter("openai", server.URL+"/v1", "secret-key", false))
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("expected redirect error, got %v", err)
	}
	if redirected.Load() {
		t.Fatal("model discovery followed redirect and could leak credentials")
	}
}

func TestDiscoverModelAdapterModelsRejectsRepeatedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}],"has_more":true,"last_id":"same-cursor"}`))
	}))
	defer server.Close()

	service := &ProxyService{publicClient: server.Client()}
	_, err := service.discoverModelAdapterModels(testDiscoveryAdapter("anthropic", server.URL, "key", false))
	if err == nil || !strings.Contains(err.Error(), "分页游标重复") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
}

func TestModelDiscoveryEndpointURLRejectsQuery(t *testing.T) {
	adapter := testDiscoveryAdapter("openai", "https://provider.example/v1?token=value", "key", false)
	_, err := modelDiscoveryEndpointURL(adapter)
	if err == nil || !strings.Contains(err.Error(), "query") {
		t.Fatalf("expected query rejection, got %v", err)
	}
}

func TestValidateModelDiscoveryTargetBlocksMetadataAndUnsafeAddresses(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.200/latest/meta-data",
		"http://0.0.0.0/models",
		"http://metadata.google.internal/computeMetadata/v1",
	} {
		if err := validateModelDiscoveryTarget(context.Background(), target); err == nil {
			t.Fatalf("expected protected target rejection: %s", target)
		}
	}
	if err := validateModelDiscoveryTarget(context.Background(), "http://127.0.0.1:8080/v1/models"); err != nil {
		t.Fatalf("loopback model service should remain supported: %v", err)
	}
}

func testDiscoveryAdapter(providerType string, baseURL string, apiKey string, customHeaders bool) serverconfig.ModelAdapterConfig {
	return serverconfig.ModelAdapterConfig{
		DisplayName:          "Existing Model",
		Type:                 providerType,
		BaseURL:              baseURL,
		APIKey:               apiKey,
		TooltipData:          "备注",
		ModelID:              "existing-model",
		ReasoningEffort:      "medium",
		OpenAIEndpoint:       "/v1/responses",
		CustomHeadersEnabled: customHeaders,
		CustomHeadersJSON:    `{"X-Tenant":"tenant-a"}`,
	}
}
