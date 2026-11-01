package controller

// Command Code Go-plan channel: the upstream has no OpenAI-compatible API at
// all. Its only endpoint for such a key is the CLI's /alpha/generate, which
// speaks the CLI's own envelope and answers with a line-delimited event
// stream. These tests drive the real adaptor through the real relay pipeline
// (TokenAuth → Distribute → RelayTextHelper → adaptor → quota) with the
// outbound call intercepted, and assert BOTH directions of the translation:
// what the adaptor sends (the CLI envelope, never a chat-completions body) and
// what the client receives (chat completions / chunks).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
)

// commandCodeTextSSE is the endpoint's reply shape for a plain answer: one
// text-delta plus the finish event that carries usage.
const commandCodeTextSSE = `{"type":"start"}
{"type":"start-step","request":{},"warnings":[]}
{"type":"text-start","id":"txt-0"}
{"type":"text-delta","id":"txt-0","text":"PONG"}
{"type":"text-end","id":"txt-0"}
{"type":"finish-step","finishReason":"stop","rawFinishReason":"stop","usage":{"inputTokens":7587,"outputTokens":4,"totalTokens":7591}}
{"type":"finish","finishReason":"stop","rawFinishReason":"stop","totalUsage":{"inputTokens":7587,"outputTokens":4,"totalTokens":7591}}
`

// captureCommandCodeUpstream is captureUpstream with the endpoint's own
// content type: the reply is always an event stream, and the relay treats a
// JSON content type on a streaming request as an error (isErrorHappened).
func captureCommandCodeUpstream(t *testing.T, cap **upstreamCapture, status int, respBody []byte) http.Handler {
	t.Helper()
	inner := captureUpstream(t, cap, status, respBody)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(&contentTypeWriter{ResponseWriter: w, header: http.Header{"Content-Type": []string{"text/event-stream"}}}, r)
	})
}

// commandCodeSSEHandler answers with the given body as an event stream.
func commandCodeSSEHandler(status int, body []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

// contentTypeWriter answers with the endpoint's content type instead of
// captureUpstream's application/json (the reply is always an event stream).
type contentTypeWriter struct {
	http.ResponseWriter
	header http.Header
}

func (w *contentTypeWriter) Header() http.Header {
	h := w.ResponseWriter.Header()
	h.Set("Content-Type", w.header.Get("Content-Type"))
	return h
}

func (w *contentTypeWriter) Write(b []byte) (int, error) { return w.ResponseWriter.Write(b) }

func (w *contentTypeWriter) WriteHeader(status int) { w.ResponseWriter.WriteHeader(status) }

func TestProvider_CommandCode_ConvertsChatToCLIEnvelope(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.CommandCode,
		baseURL:     "",
		models:      "deepseek/deepseek-v4.1-flash",
		key:         "user_commandcode_key",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.commandcode.ai/alpha/generate",
		captureCommandCodeUpstream(t, &captured, http.StatusOK, []byte(commandCodeTextSSE)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "", basicChatBody(
		map[string]any{
			"model":    "deepseek/deepseek-v4.1-flash",
			"messages": []map[string]any{{"role": "user", "content": "say PONG"}},
		},
	))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("the adaptor never called /alpha/generate")
	}
	if captured.Method != http.MethodPost {
		t.Errorf("upstream method = %s, want POST", captured.Method)
	}

	// --- what the adaptor sent -------------------------------------------
	body := captured.decodedBody(t)
	params, ok := body["params"].(map[string]any)
	if !ok {
		t.Fatalf("upstream body has no params object: %v", body)
	}
	// The endpoint refuses a non-streaming call ("Proxy use detected. This
	// endpoint only serves CLI."), so the adaptor must force stream:true even
	// though the client asked for a single reply.
	if params["stream"] != true {
		t.Errorf("params.stream = %v, want true (the endpoint rejects non-streaming calls)", params["stream"])
	}
	if params["model"] != "deepseek/deepseek-v4.1-flash" {
		t.Errorf("params.model = %v", params["model"])
	}
	for _, key := range []string{"messages", "tools", "system", "max_tokens"} {
		if _, ok := params[key]; !ok {
			t.Errorf("params.%s missing from the CLI envelope", key)
		}
	}
	for _, key := range []string{"config", "memory", "taste", "skills", "permissionMode"} {
		if _, ok := body[key]; !ok {
			t.Errorf("%s missing from the CLI envelope", key)
		}
	}
	cfg, _ := body["config"].(map[string]any)
	for _, key := range []string{"workingDir", "date", "environment", "structure", "isGitRepo", "currentBranch", "mainBranch", "gitStatus", "recentCommits"} {
		if _, ok := cfg[key]; !ok {
			t.Errorf("config.%s missing (the upstream validates it and 400s)", key)
		}
	}
	// A chat-completions body must never leak through.
	if _, leaked := body["stream_options"]; leaked {
		t.Error("upstream body carries stream_options, which the CLI envelope does not have")
	}

	// --- what the adaptor sent as headers --------------------------------
	// The endpoint only serves what looks like the CLI, so the CLI's own
	// headers have to be there.
	if got := captured.Header.Get("x-command-code-version"); got == "" {
		t.Error("x-command-code-version is missing")
	}
	if got := captured.Header.Get("User-Agent"); got != "cli" {
		t.Errorf("User-Agent = %q, want cli", got)
	}
	if got := captured.Header.Get("x-api-key"); got != "user_commandcode_key" {
		t.Errorf("x-api-key = %q, want the channel key", got)
	}
	if got := captured.Header.Get("Authorization"); got != "Bearer user_commandcode_key" {
		t.Errorf("Authorization = %q, want the channel key", got)
	}
	if got := captured.Header.Get("x-session-id"); len(got) != 36 {
		t.Errorf("x-session-id = %q, want a uuid", got)
	}

	// --- what the client received ---------------------------------------
	var reply struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	if reply.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", reply.Object)
	}
	if got := reply.Choices[0].Message.Content; got != "PONG" {
		t.Errorf("content = %q, want PONG", got)
	}
	if reply.Usage.PromptTokens != 7587 {
		t.Errorf("usage.prompt_tokens = %d, want the upstream's 7587", reply.Usage.PromptTokens)
	}
}

func TestProvider_CommandCode_StreamsChatChunks(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.CommandCode,
		models:      "deepseek/deepseek-v4.1-flash",
		key:         "user_commandcode_key",
	})
	// The real endpoint always answers with an SSE content type — even for a
	// non-streaming request — because it only ever streams.
	mt.Match(http.MethodPost, "https://api.commandcode.ai/alpha/generate",
		commandCodeSSEHandler(http.StatusOK, []byte(commandCodeTextSSE)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "", basicChatBody(map[string]any{
		"model":    "deepseek/deepseek-v4.1-flash",
		"stream":   true,
		"messages": []map[string]any{{"role": "user", "content": "say PONG"}},
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) {
		t.Errorf("no chat.completion.chunk was rendered: %s", body)
	}
	if got := reassembleChatStreamContent(body); got != "PONG" {
		t.Errorf("reassembled stream content = %q, want PONG", got)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("stream has no [DONE]: %s", body)
	}
}

func TestProvider_CommandCode_UpstreamErrorSurfaces(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.CommandCode,
		models:      "claude-opus-5",
		key:         "user_commandcode_key",
	})
	// A model the Go plan doesn't include: the endpoint answers 403 with its
	// own error envelope, which the relay must surface rather than mask.
	mt.Match(http.MethodPost, "https://api.commandcode.ai/alpha/generate",
		capturingHandler(t, new(*http.Request), http.StatusForbidden,
			[]byte(`{"success":false,"error":{"code":"FORBIDDEN","status":403,"message":"MODEL_NOT_IN_PLAN: Claude Opus 5 available in Provider and above plans"}}`)))

	rec := doRelayRequestTo(t, r, "/v1/chat/completions", "Bearer sk-test", "", basicChatBody(
		map[string]any{"model": "claude-opus-5"},
	))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "MODEL_NOT_IN_PLAN") {
		t.Errorf("client does not see the upstream's reason: %s", rec.Body.String())
	}
}

// The default base URL must be the endpoint's host, so a channel created
// without an explicit Base URL works.
func TestProvider_CommandCode_DefaultBaseURL(t *testing.T) {
	m := &meta.Meta{ChannelType: channeltype.CommandCode, BaseURL: channeltype.ChannelBaseURLs[channeltype.CommandCode]}
	if m.BaseURL != "https://api.commandcode.ai" {
		t.Fatalf("default base URL = %q, want https://api.commandcode.ai", m.BaseURL)
	}
}
