package openai

import (
	"testing"

	"github.com/songquanpeng/one-api/relay/channeltype"
)

// AIHubMix documents its relay endpoint as "https://aihubmix.com/v1", but
// requestURL already carries the "/v1" prefix. Both forms of base_url must
// produce exactly one "/v1" segment; "/v1/v1/chat/completions" is a 404
// upstream (verified against the live API).
func TestGetFullRequestURLAIHubMix(t *testing.T) {
	const requestURL = "/v1/chat/completions"
	const want = "https://aihubmix.com/v1/chat/completions"
	cases := []struct {
		name    string
		baseURL string
	}{
		{"documented base url with /v1", "https://aihubmix.com/v1"},
		{"base url with /v1 and trailing slash", "https://aihubmix.com/v1/"},
		{"bare origin (channeltype default)", "https://aihubmix.com"},
		{"bare origin with trailing slash", "https://aihubmix.com/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetFullRequestURL(tc.baseURL, requestURL, channeltype.AIHubMix)
			if got != want {
				t.Fatalf("GetFullRequestURL(%q) = %q, want %q", tc.baseURL, got, want)
			}
		})
	}
}

// Other channel types must keep their existing concatenation behaviour.
func TestGetFullRequestURLOtherTypesUnchanged(t *testing.T) {
	got := GetFullRequestURL("https://api.openai.com", "/v1/chat/completions", channeltype.OpenAI)
	if want := "https://api.openai.com/v1/chat/completions"; got != want {
		t.Fatalf("OpenAI: got %q, want %q", got, want)
	}
	// OpenAICompatible strips /v1 from the request path instead.
	got = GetFullRequestURL("https://example.com/v1", "/v1/chat/completions", channeltype.OpenAICompatible)
	if want := "https://example.com/v1/chat/completions"; got != want {
		t.Fatalf("OpenAICompatible: got %q, want %q", got, want)
	}
}
