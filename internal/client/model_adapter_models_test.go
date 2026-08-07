package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestBuildModelListEndpointCandidates(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		baseURL  string
		want     []string
	}{
		{
			name:     "openai 带版本段不再补 v1",
			provider: "openai",
			baseURL:  "https://api.openai.com/v1",
			want:     []string{"https://api.openai.com/v1/models"},
		},
		{
			name:     "openai 裸域名优先试 v1",
			provider: "openai",
			baseURL:  "https://api.openai.com",
			want:     []string{"https://api.openai.com/v1/models", "https://api.openai.com/models"},
		},
		{
			name:     "anthropic 裸域名优先试 v1",
			provider: "anthropic",
			baseURL:  "https://api.anthropic.com",
			want:     []string{"https://api.anthropic.com/v1/models", "https://api.anthropic.com/models"},
		},
		{
			name:     "anthropic 带版本段不再补 v1",
			provider: "anthropic",
			baseURL:  "https://api.anthropic.com/v1",
			want:     []string{"https://api.anthropic.com/v1/models"},
		},
		{
			name:     "剥离 chat completions 后缀",
			provider: "openai",
			baseURL:  "https://api.example.com/v1/chat/completions",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "剥离 responses 后缀",
			provider: "openai",
			baseURL:  "https://api.example.com/v1/responses",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "剥离 anthropic messages 后缀",
			provider: "anthropic",
			baseURL:  "https://api.example.com/v1/messages",
			want:     []string{"https://api.example.com/v1/models"},
		},
		{
			name:     "已填到 models 地址本身则原样使用",
			provider: "openai",
			baseURL:  "https://api.example.com/openai/v1/models",
			want:     []string{"https://api.example.com/openai/v1/models"},
		},
		{
			name:     "自定义网关前缀会补 v1",
			provider: "openai",
			baseURL:  "https://gateway.example.com/proxy",
			want: []string{
				"https://gateway.example.com/proxy/v1/models",
				"https://gateway.example.com/proxy/models",
			},
		},
		{
			name:     "尾部斜杠不影响推导",
			provider: "anthropic",
			baseURL:  "  https://api.anthropic.com/v1/  ",
			want:     []string{"https://api.anthropic.com/v1/models"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rule, ok := modelListProviderRules[test.provider]
			if !ok {
				t.Fatalf("provider %q 没有对应规则", test.provider)
			}
			got := buildModelListEndpointCandidates(rule, test.baseURL)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("buildModelListEndpointCandidates(%q) = %v, want %v", test.baseURL, got, test.want)
			}
		})
	}
}

func TestFetchModelAdapterModelsOpenAIUsesBearer(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotAnthropicVersion string
	var gotAPIKeyHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAnthropicVersion = r.Header.Get("anthropic-version")
		gotAPIKeyHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"},{"id":"gpt-4o"}]}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if gotPath != "/v1/models" {
		t.Fatalf("请求路径 = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want Bearer sk-test", gotAuth)
	}
	if gotAPIKeyHeader != "" {
		t.Fatalf("openai 不应发送 x-api-key，实际 = %q", gotAPIKeyHeader)
	}
	if gotAnthropicVersion != "" {
		t.Fatalf("openai 不应发送 anthropic-version，实际 = %q", gotAnthropicVersion)
	}
	want := []string{"gpt-4o", "gpt-5"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsAnthropicUsesAPIKeyHeader(t *testing.T) {
	var gotAuth string
	var gotAPIKeyHeader string
	var gotAnthropicVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKeyHeader = r.Header.Get("x-api-key")
		gotAnthropicVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4"}],"has_more":false}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if gotAPIKeyHeader != "sk-ant-test" {
		t.Fatalf("x-api-key = %q, want sk-ant-test", gotAPIKeyHeader)
	}
	if gotAnthropicVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q, want 2023-06-01", gotAnthropicVersion)
	}
	if gotAuth != "" {
		t.Fatalf("anthropic 不应发送 Authorization，实际 = %q", gotAuth)
	}
	want := []string{"claude-sonnet-4"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsAnthropicFollowsCursor(t *testing.T) {
	var requestedQueries []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedQueries = append(requestedQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`))
		case "claude-a":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-b"}],"has_more":true,"last_id":"claude-b"}`))
		default:
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-c"}],"has_more":false}`))
		}
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	want := []string{"claude-a", "claude-b", "claude-c"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
	if len(requestedQueries) != 3 {
		t.Fatalf("请求次数 = %d, want 3（两次翻页后停止）", len(requestedQueries))
	}
	for _, query := range requestedQueries {
		if !strings.Contains(query, "limit=1000") {
			t.Fatalf("翻页请求缺少 limit 参数：%q", query)
		}
	}
	if !strings.Contains(requestedQueries[1], "after_id=claude-a") {
		t.Fatalf("第二页未带上游标：%q", requestedQueries[1])
	}
}

func TestFetchModelAdapterModelsOpenAIDoesNotPaginate(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.RawQuery != "" {
			t.Errorf("openai 不应附加分页参数，实际 = %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}],"has_more":true,"last_id":"gpt-5"}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	if _, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	}); err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}

	if requestCount != 1 {
		t.Fatalf("请求次数 = %d, want 1（openai 忽略 has_more）", requestCount)
	}
}

func TestFetchModelAdapterModelsReadsLargeBody(t *testing.T) {
	models := make([]map[string]string, 0, 400)
	for index := 0; index < 400; index++ {
		models = append(models, map[string]string{
			"id": "vendor/model-with-a-fairly-long-identifier-" + strings.Repeat("x", 40) + "-" + string(rune('a'+index%26)) + strconv.Itoa(index),
		})
	}
	body, err := json.Marshal(map[string]any{"data": models})
	if err != nil {
		t.Fatalf("构造响应失败：%v", err)
	}
	if len(body) <= modelAdapterTestMaxErrorBodyBytes {
		t.Fatalf("测试响应体只有 %d 字节，需要大于 %d 才能覆盖截断场景", len(body), modelAdapterTestMaxErrorBodyBytes)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	if len(result.Models) != len(models) {
		t.Fatalf("Models 数量 = %d, want %d", len(result.Models), len(models))
	}
}

func TestFetchModelAdapterModelsSupportsStringItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":["gpt-4o","gpt-4.1"]}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	result, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "openai",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-test",
	})
	if err != nil {
		t.Fatalf("FetchModelAdapterModels 返回错误：%v", err)
	}
	want := []string{"gpt-4.1", "gpt-4o"}
	if !reflect.DeepEqual(result.Models, want) {
		t.Fatalf("Models = %v, want %v", result.Models, want)
	}
}

func TestFetchModelAdapterModelsRejectsPaginationTruncation(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-model"}],"has_more":true,"last_id":"next"}`))
	}))
	defer server.Close()

	service := &ProxyService{}
	_, err := service.FetchModelAdapterModels(ModelAdapterModelsRequest{
		Type:    "anthropic",
		BaseURL: server.URL + "/v1",
		APIKey:  "sk-ant-test",
	})
	if err == nil || !strings.Contains(err.Error(), "结果可能不完整") {
		t.Fatalf("期望分页截断错误，实际 = %v", err)
	}
	if requestCount != modelAdapterListMaxPages {
		t.Fatalf("请求次数 = %d, want %d", requestCount, modelAdapterListMaxPages)
	}
}

func TestFetchModelAdapterModelsRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		request ModelAdapterModelsRequest
	}{
		{name: "未知类型", request: ModelAdapterModelsRequest{Type: "gemini", BaseURL: "https://x.com", APIKey: "k"}},
		{name: "缺少地址", request: ModelAdapterModelsRequest{Type: "openai", APIKey: "k"}},
		{name: "缺少密钥", request: ModelAdapterModelsRequest{Type: "openai", BaseURL: "https://x.com"}},
	}

	service := &ProxyService{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.FetchModelAdapterModels(test.request); err == nil {
				t.Fatal("期望返回错误，实际为 nil")
			}
		})
	}
}
