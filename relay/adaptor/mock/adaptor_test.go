package mock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/model"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newCtxWithBehavior builds a *gin.Context carrying the given
// X-Mock-Behavior header and a JSON request body. DoRequest reads the
// behavior from c.Request.Header and the request shape from the body.
func newCtxWithBehavior(t *testing.T, behavior, body string) *gin.Context {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if behavior != "" {
		req.Header.Set(BehaviorHeader, behavior)
	}
	c.Request = req
	return c
}

func bodyString(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

// reassembleContentDeltas extracts every "content" value from the SSE
// chat-completion chunks in body and concatenates them, mirroring what
// the OpenAI StreamHandler does in production. Used to verify that
// multi-chunk streaming reconstructs the original text.
func reassembleContentDeltas(body string) string {
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		for _, c := range chunk.Choices {
			out.WriteString(c.Delta.Content)
		}
	}
	return out.String()
}

func TestDoRequest_NonStreamChat(t *testing.T) {
	c := newCtxWithBehavior(t, "openai-chat",
		`{"model":"mock-gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `"content":"`+cannedReply+`"`) {
		t.Errorf("body missing canned reply %q: %s", cannedReply, body)
	}
	if !strings.Contains(body, `"finish_reason":"stop"`) {
		t.Errorf("body missing finish_reason stop: %s", body)
	}
	if !strings.Contains(body, `"total_tokens":21`) {
		t.Errorf("body missing usage: %s", body)
	}
}

func TestDoRequest_DefaultBehaviorEqualsOpenAIChat(t *testing.T) {
	// Empty behavior header should fall back to openai-chat.
	c := newCtxWithBehavior(t, "", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), cannedReply) {
		t.Errorf("default behavior did not produce openai-chat body")
	}
}

func TestDoRequest_StreamFlagInBodyProducesSSE(t *testing.T) {
	// stream:true in the request body should make openai-chat emit SSE
	// even though the behavior is the non-stream one.
	c := newCtxWithBehavior(t, "openai-chat",
		`{"model":"mock-gpt-4o","stream":true}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil,
		strings.NewReader(`{"model":"mock-gpt-4o","stream":true}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "data: ") {
		t.Errorf("stream body missing data: prefix: %s", body)
	}
	if !strings.Contains(body, "[DONE]") {
		t.Errorf("stream body missing [DONE]: %s", body)
	}
}

func TestDoRequest_ForceStreamBehavior(t *testing.T) {
	// openai-stream forces SSE even without stream:true in the body.
	c := newCtxWithBehavior(t, "openai-stream",
		`{"model":"mock-gpt-4o"}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := bodyString(t, resp)
	// Content is now split across multiple delta chunks (mirrors real
	// token-by-token streaming). Assert on the first chunk + the
	// structural markers, not on a single full-content chunk.
	for _, want := range []string{
		`"role":"assistant"`,
		`"content":"Hello"`, // first delta chunk
		`"content":" from"`, // second delta chunk (space-prefixed)
		`"finish_reason":"stop"`,
		`"total_tokens":21`,
		"[DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("stream body missing %q:\n%s", want, body)
		}
	}
	// Verify the full canned reply is recoverable by concatenating all
	// content deltas (this is what the OpenAI StreamHandler does).
	if !strings.Contains(reassembleContentDeltas(body), cannedReply) {
		t.Errorf("concatenated content deltas do not reconstruct the canned reply:\ngot: %s", reassembleContentDeltas(body))
	}
}

func TestDoRequest_ToolCall(t *testing.T) {
	c := newCtxWithBehavior(t, "openai-tool-call",
		`{"model":"mock-gpt-4o"}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `"tool_calls"`) {
		t.Fatalf("tool-call body missing tool_calls: %s", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Errorf("tool-call body missing finish_reason tool_calls: %s", body)
	}
	// The tool call must carry a function name the relay pipeline can
	// route on.
	if !strings.Contains(body, `"name":"get_weather"`) {
		t.Errorf("tool-call body missing function name: %s", body)
	}
}

func TestDoRequest_ResponsesNonStream(t *testing.T) {
	c := newCtxWithBehavior(t, "openai-responses", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := bodyString(t, resp)
	// Responses shape markers
	for _, want := range []string{
		`"object":"response"`,
		`"status":"completed"`,
		`"type":"output_text"`,
		`"input_tokens":19`,
		`"output_tokens":7`,
		cannedReply,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("responses body missing %q:\n%s", want, body)
		}
	}
}

func TestDoRequest_ResponsesStreamForced(t *testing.T) {
	// openai-responses-stream forces SSE regardless of stream flag.
	c := newCtxWithBehavior(t, "openai-responses-stream", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := bodyString(t, resp)
	// Responses SSE carries event: lines alongside data: lines.
	// Assert the COMPLETE official event sequence is present (see
	// synthesizeResponsesStream doc comment for the full list).
	for _, want := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
		`"type":"response.completed"`,
		`"input_tokens":19`,
		"[DONE]",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("responses stream body missing %q:\n%s", want, body)
		}
	}
	// Multiple delta chunks should be present (content split across
	// chunks, not a single blob).
	deltaCount := strings.Count(body, "event: response.output_text.delta")
	if deltaCount < 2 {
		t.Errorf("expected >=2 output_text.delta events (multi-chunk), got %d:\n%s", deltaCount, body)
	}
}

func TestDoRequest_ResponsesStreamFlagInBody(t *testing.T) {
	// stream:true in body with openai-responses behavior should also
	// produce the Responses SSE shape.
	c := newCtxWithBehavior(t, "openai-responses", `{"stream":true}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil,
		strings.NewReader(`{"model":"mock-gpt-4o","stream":true}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, "event: response.completed") {
		t.Errorf("stream body missing response.completed event:\n%s", body)
	}
}

func TestDoRequest_ResponsesToolCall(t *testing.T) {
	// Responses API with a function_call output item (not a message).
	c := newCtxWithBehavior(t, "openai-responses-tool-call", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{"model":"mock-gpt-4o"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body := bodyString(t, resp)
	// function_call output shape markers
	for _, want := range []string{
		`"object":"response"`,
		`"type":"function_call"`,
		`"name":"get_weather"`,
		`"call_id":"call_mock"`,
		`"arguments"`,
		`"status":"completed"`,
		`"input_tokens":19`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("responses tool-call body missing %q:\n%s", want, body)
		}
	}
	// Must NOT contain a message output (that's the text-response shape).
	if strings.Contains(body, `"type":"message"`) {
		t.Errorf("tool-call response should not contain a message output:\n%s", body)
	}
}

func TestDoRequest_ErrorStatuses(t *testing.T) {
	cases := []struct {
		behavior string
		wantCode int
		wantType string
	}{
		{"error-429", http.StatusTooManyRequests, "rate_limit_exceeded"},
		{"error-500", http.StatusInternalServerError, "server_error"},
		{"error-400", http.StatusBadRequest, "invalid_request_error"},
	}
	for _, tc := range cases {
		t.Run(tc.behavior, func(t *testing.T) {
			c := newCtxWithBehavior(t, tc.behavior, `{}`)
			a := &Adaptor{}
			resp, err := a.DoRequest(c, nil, strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("DoRequest: %v", err)
			}
			if resp.StatusCode != tc.wantCode {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantCode)
			}
			body := bodyString(t, resp)
			var env struct {
				Error struct {
					Message string `json:"message"`
					Type    string `json:"type"`
					Code    string `json:"code"`
					Param   any    `json:"param"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("error body not valid JSON: %v\n%s", err, body)
			}
			if env.Error.Type != tc.wantType {
				t.Errorf("error.type = %q, want %q", env.Error.Type, tc.wantType)
			}
			if env.Error.Message == "" {
				t.Errorf("error.message empty in body: %s", body)
			}
			// The code field must be a non-empty machine-readable
			// string that clients switch on (not null/empty).
			if env.Error.Code == "" {
				t.Errorf("error.code empty in body (should be a descriptive string): %s", body)
			}
		})
	}
}

func TestDoRequest_EmptyBody(t *testing.T) {
	c := newCtxWithBehavior(t, "empty", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if body := bodyString(t, resp); body != "" {
		t.Errorf("empty body should be \"\", got %q", body)
	}
}

func TestDoRequest_UnknownBehaviorIsError(t *testing.T) {
	// An unknown behavior header must surface as a 500 so a misconfigured
	// test fails loudly instead of silently succeeding.
	c := newCtxWithBehavior(t, "bogus", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 for unknown behavior", resp.StatusCode)
	}
	if !strings.Contains(bodyString(t, resp), "unknown behavior") {
		t.Errorf("body should mention unknown behavior")
	}
}

func TestDoRequest_EchoesModelFromRequest(t *testing.T) {
	c := newCtxWithBehavior(t, "openai-chat", `{}`)
	a := &Adaptor{}
	resp, err := a.DoRequest(c, nil,
		strings.NewReader(`{"model":"mock-gpt-4o-mini"}`))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	body := bodyString(t, resp)
	if !strings.Contains(body, `"model":"mock-gpt-4o-mini"`) {
		t.Errorf("response should echo the requested model: %s", body)
	}
}

func TestSynthesizeStreamChunksIsParseable(t *testing.T) {
	// The StreamHandler parses each `data: ` line as JSON (except
	// [DONE]). Make sure every chunk is valid so the relay pipeline
	// doesn't log parse errors during integration tests.
	raw := synthesizeStreamChunks("mock-gpt-4o", "hello")
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(payload), &v); err != nil {
			t.Errorf("invalid stream chunk %q: %v", payload, err)
		}
	}
}

func TestGetModelListAndChannelName(t *testing.T) {
	a := &Adaptor{}
	if name := a.GetChannelName(); name != "mock" {
		t.Errorf("GetChannelName = %q, want mock", name)
	}
	models := a.GetModelList()
	if len(models) == 0 {
		t.Errorf("GetModelList returned empty list")
	}
	// GetModelList must return a fresh slice so callers can't mutate the
	// package-level DefaultModelList.
	models[0] = "mutated"
	if DefaultModelList[0] == "mutated" {
		t.Errorf("GetModelList returned the package-level slice by reference")
	}
}

func TestConvertRequestPassthrough(t *testing.T) {
	a := &Adaptor{}
	in := &model.GeneralOpenAIRequest{Model: "mock-gpt-4o"}
	out, err := a.ConvertRequest(nil, 0, in)
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if out != in {
		t.Errorf("ConvertRequest must return the request unchanged")
	}
}

func TestConvertRequestNil(t *testing.T) {
	a := &Adaptor{}
	if _, err := a.ConvertRequest(nil, 0, nil); err == nil {
		t.Errorf("ConvertRequest(nil) must error")
	}
}
