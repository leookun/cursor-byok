package modelchannel

import "testing"

func TestNormalizeBaseURL_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"https://api.openai.com", "https://api.openai.com"},
		{"https://api.openai.com/", "https://api.openai.com"},
		{"HTTP://API.OPENAI.COM", "http://api.openai.com"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1"},
		{"  https://api.openai.com  ", "https://api.openai.com"},
	}
	for _, tt := range tests {
		got, err := NormalizeBaseURL(tt.input)
		if err != nil {
			t.Errorf("NormalizeBaseURL(%q) unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeBaseURL_Invalid(t *testing.T) {
	tests := []struct {
		input string
		desc  string
	}{
		{"", "empty"},
		{"  ", "whitespace only"},
		{"not-a-url", "no scheme"},
		{"ftp://api.openai.com", "unsupported scheme"},
		{"https://", "no host"},
	}
	for _, tt := range tests {
		_, err := NormalizeBaseURL(tt.input)
		if err == nil {
			t.Errorf("NormalizeBaseURL(%q) [%s] expected error, got nil", tt.input, tt.desc)
		}
	}
}

func TestNormalizeOpenAIEndpoint(t *testing.T) {
	tests := []struct {
		provider string
		endpoint string
		want     string
	}{
		{"openai", "", "/v1/chat/completions"},
		{"openai", "/v1/responses", "/v1/responses"},
		{"openai", "/v1/chat/completions", "/v1/chat/completions"},
		{"openai", "/custom", "/custom"},
		{"OpenAI", "/v1/responses", "/v1/responses"},
		{"OPENAI", "/v1/responses", "/v1/responses"},
		{"anthropic", "", ""},
		{"", "", ""},
		{"openai", "/invalid", ""},
	}
	for _, tt := range tests {
		got := NormalizeOpenAIEndpoint(tt.provider, tt.endpoint)
		if got != tt.want {
			t.Errorf("NormalizeOpenAIEndpoint(%q, %q) = %q, want %q", tt.provider, tt.endpoint, got, tt.want)
		}
	}
}

func TestOpenAIEndpointShape(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
	}{
		{"/v1/responses", "responses"},
		{"/v1/chat/completions", "chat/completions"},
		{"/v4/chat/completions", "chat/completions"},
		{"/chat/completions", "chat/completions"},
		{"/RESPONSES", "responses"},
		{"/Responses", "responses"},
		{"", "chat/completions"},
		{"/v1/other", "chat/completions"},
		{"garbage", "chat/completions"},
	}
	for _, tt := range tests {
		got := OpenAIEndpointShape(tt.endpoint)
		if got != tt.want {
			t.Errorf("OpenAIEndpointShape(%q) = %q, want %q", tt.endpoint, got, tt.want)
		}
	}
}

func TestBuildChannelID_Deterministic(t *testing.T) {
	id1 := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "/v1/chat/completions")
	id2 := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "/v1/chat/completions")
	if id1 != id2 {
		t.Error("BuildChannelID must be deterministic for the same inputs")
	}
	if len(id1) != ChannelIDHexLength {
		t.Errorf("BuildChannelID length = %d, want %d", len(id1), ChannelIDHexLength)
	}
}

func TestBuildChannelID_DifferentInputs(t *testing.T) {
	id1 := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "/v1/chat/completions")
	id2 := BuildChannelID("https://api.anthropic.com", "claude-3", "sk-yyy", "Claude", "/v1/chat/completions")
	if id1 == id2 {
		t.Error("different inputs must produce different IDs")
	}
}

func TestBuildChannelID_EmptyEndpointFallsBackToLegacy(t *testing.T) {
	with := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "/v1/chat/completions")
	without := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "")
	legacy := BuildLegacyChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4")

	if without != legacy {
		t.Error("BuildChannelID with empty endpoint must equal BuildLegacyChannelID")
	}
	if with == legacy {
		t.Error("BuildChannelID with non-empty endpoint must differ from BuildLegacyChannelID")
	}
}

func TestBuildChannelID_TrimsWhitespace(t *testing.T) {
	trimmed := BuildChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4", "/v1/chat/completions")
	spaced := BuildChannelID("  https://api.openai.com  ", "  gpt-4  ", "  sk-xxx  ", "  GPT-4  ", "  /v1/chat/completions  ")
	if trimmed != spaced {
		t.Error("BuildChannelID should produce the same ID after trimming whitespace")
	}
}

func TestBuildLegacyChannelID_Deterministic(t *testing.T) {
	id1 := BuildLegacyChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4")
	id2 := BuildLegacyChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4")
	if id1 != id2 {
		t.Error("BuildLegacyChannelID must be deterministic for the same inputs")
	}
	if len(id1) != ChannelIDHexLength {
		t.Errorf("BuildLegacyChannelID length = %d, want %d", len(id1), ChannelIDHexLength)
	}
}

func TestBuildLegacyChannelID_DifferentInputs(t *testing.T) {
	id1 := BuildLegacyChannelID("https://api.openai.com", "gpt-4", "sk-xxx", "GPT-4")
	id2 := BuildLegacyChannelID("https://api.anthropic.com", "claude-3", "sk-yyy", "Claude")
	if id1 == id2 {
		t.Error("different inputs must produce different IDs")
	}
}
