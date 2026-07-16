package modelchannel

import "testing"

func TestBuildGroupIDCanonicalizesChannelIdentity(t *testing.T) {
	leftURL, err := NormalizeBaseURL("HTTPS://Provider.Example/v1/")
	if err != nil {
		t.Fatalf("normalize left URL: %v", err)
	}
	rightURL, err := NormalizeBaseURL("https://provider.example/v1")
	if err != nil {
		t.Fatalf("normalize right URL: %v", err)
	}
	left := BuildGroupID("OpenAI", leftURL, " key ", true, `{"Authorization":"Bearer token","X-Tenant":"a"}`)
	right := BuildGroupID("openai", rightURL, "key", true, `{"x-tenant":"a","authorization":"Bearer token"}`)
	if left != right {
		t.Fatalf("equivalent channels must share group ID: %q != %q", left, right)
	}
	if len(left) != len(GroupIDPrefix)+GroupIDHexLength {
		t.Fatalf("unexpected group ID length: %q", left)
	}

	variants := []string{
		BuildGroupID("anthropic", rightURL, "key", true, `{"authorization":"Bearer token","x-tenant":"a"}`),
		BuildGroupID("openai", rightURL, "other-key", true, `{"authorization":"Bearer token","x-tenant":"a"}`),
		BuildGroupID("openai", rightURL, "key", true, `{"authorization":"Bearer token","x-tenant":"b"}`),
		BuildGroupID("openai", rightURL, "key", false, `{"authorization":"Bearer token","x-tenant":"a"}`),
	}
	for _, variant := range variants {
		if variant == left {
			t.Fatalf("different channel identity reused group ID %q", left)
		}
	}
}

func TestBuildGroupIDIgnoresDisabledHeaderJSON(t *testing.T) {
	left := BuildGroupID("openai", "https://provider.example/v1", "key", false, `{"X-A":"1"}`)
	right := BuildGroupID("openai", "https://provider.example/v1", "key", false, `{"X-B":"2"}`)
	if left != right {
		t.Fatalf("disabled headers must not affect group identity: %q != %q", left, right)
	}
}
