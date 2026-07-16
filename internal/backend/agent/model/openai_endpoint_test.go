package modeladapter

import "testing"

func TestOpenAIEndpointURLCustomUsesCompleteRequestURL(t *testing.T) {
	baseURL := "https://provider.example/openai/v2/generate"
	if actual := OpenAIEndpointURL(baseURL, "/custom"); actual != baseURL {
		t.Fatalf("custom endpoint must use complete request URL: %s", actual)
	}
}

func TestOpenAIEndpointURLFollowsConfiguredStandardEndpoint(t *testing.T) {
	baseURL := "https://provider.example/v1"
	if actual := OpenAIEndpointURL(baseURL, "/v1/responses"); actual != baseURL+"/responses" {
		t.Fatalf("unexpected responses URL: %s", actual)
	}
	if actual := OpenAIEndpointURL(baseURL, "/v1/chat/completions"); actual != baseURL+"/chat/completions" {
		t.Fatalf("unexpected chat completions URL: %s", actual)
	}
}

func TestOpenAIEndpointURLConfiguredEndpointOverridesAddressSuffix(t *testing.T) {
	baseURL := "https://provider.example/v1/responses"
	expected := "https://provider.example/v1/chat/completions"
	if actual := OpenAIEndpointURL(baseURL, "/v1/chat/completions"); actual != expected {
		t.Fatalf("configured endpoint must override address suffix: %s", actual)
	}
	if actual := ResolveOpenAIEndpoint(baseURL, "/v1/chat/completions"); actual != "/v1/chat/completions" {
		t.Fatalf("configured endpoint shape must be authoritative: %s", actual)
	}
}
