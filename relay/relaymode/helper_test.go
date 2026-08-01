package relaymode

import "testing"

// The Responses API must map to its own mode. If it fell through to Unknown the
// relay would hand it to the ChatCompletions pipeline, which would silently
// discard the Responses-specific fields.
func TestGetByPathResponses(t *testing.T) {
	cases := []struct {
		path string
		want int
	}{
		{"/v1/responses", Responses},
		{"/v1/chat/completions", ChatCompletions},
		{"/v1/completions", Completions},
		{"/v1/embeddings", Embeddings},
		{"/v1/moderations", Moderations},
		{"/v1/images/generations", ImagesGenerations},
		{"/v1/audio/speech", AudioSpeech},
		{"/v1/oneapi/proxy/1/v1/responses", Proxy},
		{"/v1/unknown", Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if got := GetByPath(tc.path); got != tc.want {
				t.Fatalf("GetByPath(%q) = %d, want %d", tc.path, got, tc.want)
			}
		})
	}
}

// /v1/responses must not be shadowed by the /v1/completions prefix check.
func TestResponsesIsNotCompletions(t *testing.T) {
	if got := GetByPath("/v1/responses"); got == Completions {
		t.Fatal("/v1/responses was misrouted to Completions")
	}
}
