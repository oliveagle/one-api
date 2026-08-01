package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// getPromptTokens depends on openai.InitTokenEncoders (it calls CountToken*),
// so it has to run after the encoder package is initialised. The
// controller_test.go's init() already takes care of that.
func TestGetPromptTokens(t *testing.T) {
	cases := []struct {
		name    string
		mode    int
		body    string
		wantPos bool // true if expected > 0
	}{
		{"chat completions", relaymode.ChatCompletions, `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}`, true},
		{"completions", relaymode.Completions, `{"model":"gpt-3.5-turbo-instruct","prompt":"hello"}`, true},
		{"moderations", relaymode.Moderations, `{"input":"hello"}`, true},
		{"unknown mode", relaymode.Unknown, `{"model":"gpt-4o-mini"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			r, err := getAndValidateTextRequest(c, tc.mode)
			if err != nil {
				t.Fatalf("getAndValidateTextRequest: %v", err)
			}
			tokens := getPromptTokens(r, tc.mode)
			if tc.wantPos && tokens <= 0 {
				t.Fatalf("expected tokens > 0, got %d", tokens)
			}
			if !tc.wantPos && tokens != 0 {
				t.Fatalf("expected tokens = 0, got %d", tokens)
			}
		})
	}
}
