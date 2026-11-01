package commandcode

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/meta"
)

// The fixtures in testdata/ are verbatim captures from the real endpoint (one
// per reply shape: text, tool call, reasoning). They pin the event vocabulary
// this adaptor parses: if Command Code renames an event the capture breaks and
// we find out before a client does.

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func newGinRecorder(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, rec
}

func upstreamResp(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

// TestHandleResponse_NonStreamAggregates is the shape an ordinary chat client
// gets: the endpoint always streams, so the adaptor has to assemble the whole
// reply into one chat.completion.
func TestHandleResponse_NonStreamAggregates(t *testing.T) {
	c, rec := newGinRecorder(t)
	usage, errResp := HandleResponse(c, upstreamResp(loadFixture(t, "generate-text.sse")), &meta.Meta{
		IsStream:        false,
		ActualModelName: "deepseek/deepseek-v4.1-flash",
	})
	if errResp != nil {
		t.Fatalf("HandleResponse: %+v", errResp)
	}

	var reply struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role             string `json:"role"`
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	if reply.Object != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", reply.Object)
	}
	if len(reply.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(reply.Choices))
	}
	if got := reply.Choices[0].Message.Content; got != "PONG" {
		t.Errorf("content = %q, want PONG", got)
	}
	if got := reply.Choices[0].FinishReason; got != "stop" {
		t.Errorf("finish_reason = %q, want stop", got)
	}
	// Usage must come from the stream's finish event, not be estimated: the
	// upstream counts cache reads into inputTokens exactly as OpenAI does.
	if reply.Usage.PromptTokens != 7587 || reply.Usage.CompletionTokens != 4 || reply.Usage.TotalTokens != 7591 {
		t.Errorf("usage = %+v, want 7587/4/7591 (from the finish event)", reply.Usage)
	}
	if usage == nil || usage.PromptTokens != 7587 {
		t.Errorf("returned usage = %+v, want the upstream's", usage)
	}
}

func TestHandleResponse_NonStreamToolCall(t *testing.T) {
	c, rec := newGinRecorder(t)
	usage, errResp := HandleResponse(c, upstreamResp(loadFixture(t, "generate-tool-call.sse")), &meta.Meta{
		IsStream:        false,
		ActualModelName: "deepseek/deepseek-v4.1-flash",
	})
	if errResp != nil {
		t.Fatalf("HandleResponse: %+v", errResp)
	}

	var reply struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Id       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	choice := reply.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", choice.FinishReason)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(choice.Message.ToolCalls))
	}
	call := choice.Message.ToolCalls[0]
	if call.Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want get_weather", call.Function.Name)
	}
	// The endpoint hands the arguments over already parsed; chat completions
	// requires them as a JSON string.
	if !strings.Contains(call.Function.Arguments, "Beijing") {
		t.Errorf("arguments = %q, want the JSON string with the city", call.Function.Arguments)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Errorf("arguments are not valid JSON: %v (%q)", err, call.Function.Arguments)
	}
	// The pre-tool text the model emitted is kept in content.
	if !strings.Contains(choice.Message.Content, "weather") {
		t.Errorf("content = %q, want the preamble text", choice.Message.Content)
	}
	if usage == nil || usage.CompletionTokens == 0 {
		t.Errorf("usage = %+v, want the upstream's counts", usage)
	}
}

func TestHandleResponse_StreamRendersChunks(t *testing.T) {
	c, rec := newGinRecorder(t)
	usage, errResp := HandleResponse(c, upstreamResp(loadFixture(t, "generate-reasoning.sse")), &meta.Meta{
		IsStream:        true,
		ActualModelName: "deepseek/deepseek-v4.1-flash",
	})
	if errResp != nil {
		t.Fatalf("HandleResponse: %+v", errResp)
	}
	body := rec.Body.String()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content-type = %q, want text/event-stream", ct)
	}
	if !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Errorf("stream does not end with [DONE]: %q", tail(body, 120))
	}

	var sawReasoning, sawContent, sawFinish bool
	var finishReason string
	var lastUsage map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Role             string `json:"role"`
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("chunk is not JSON: %v (%s)", err, line)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("object = %q, want chat.completion.chunk", chunk.Object)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.ReasoningContent != "" {
				sawReasoning = true
			}
			if ch.Delta.Content != "" {
				sawContent = true
			}
			if ch.FinishReason != nil {
				sawFinish = true
				finishReason = *ch.FinishReason
			}
		}
		if chunk.Usage != nil {
			lastUsage = chunk.Usage
		}
	}
	if !sawReasoning {
		t.Error("no reasoning_content delta was rendered (the endpoint's reasoning-delta events were dropped)")
	}
	if !sawContent {
		t.Error("no content delta was rendered")
	}
	if !sawFinish || finishReason != "stop" {
		t.Errorf("finish chunk = %v/%q, want one with finish_reason stop", sawFinish, finishReason)
	}
	// The last chunk carries usage, which is what the relay bills on for
	// streamed replies.
	if lastUsage == nil {
		t.Fatal("no chunk carried usage")
	}
	if got, _ := lastUsage["prompt_tokens"].(float64); got == 0 {
		t.Errorf("usage.prompt_tokens = %v, want the upstream's count", lastUsage["prompt_tokens"])
	}
	if usage == nil || usage.TotalTokens == 0 {
		t.Errorf("returned usage = %+v", usage)
	}
}

func TestHandleResponse_InBandErrorBeforeOutput(t *testing.T) {
	// An error event before any content must fail the request (not render a
	// half-empty 200), so the relay's failover can try another channel.
	body := []byte(`{"type":"start"}` + "\n" +
		`{"type":"error","error":{"message":"MODEL_NOT_IN_PLAN: Claude Opus 5 available in Provider and above plans","statusCode":403}}` + "\n")
	c, _ := newGinRecorder(t)
	_, errResp := HandleResponse(c, upstreamResp(body), &meta.Meta{IsStream: true, ActualModelName: "claude-opus-5"})
	if errResp == nil {
		t.Fatal("expected an error")
	}
	if errResp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", errResp.StatusCode)
	}
	if errResp.Error.Code != "model_not_in_plan" {
		t.Errorf("code = %v, want model_not_in_plan", errResp.Error.Code)
	}
}

func TestHandleResponse_InBandErrorAfterOutput(t *testing.T) {
	// After content has been rendered the client already saw a reply: report
	// the error in-band instead of discarding it.
	body := []byte(`{"type":"text-delta","text":"partial"}` + "\n" +
		`{"type":"error","error":{"message":"upstream fell over","statusCode":500}}` + "\n" +
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":3,"outputTokens":1,"totalTokens":4}}` + "\n")
	c, rec := newGinRecorder(t)
	_, errResp := HandleResponse(c, upstreamResp(body), &meta.Meta{IsStream: true, ActualModelName: "m"})
	if errResp != nil {
		t.Fatalf("an in-band error after output must not fail the request: %+v", errResp)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "partial") {
		t.Errorf("stream lost the rendered content: %s", out)
	}
	if !strings.Contains(out, "upstream fell over") {
		t.Errorf("stream does not report the error in-band: %s", out)
	}
}

func TestHandleResponse_StreamEndedWithoutFinish(t *testing.T) {
	// A reply cut short (the upstream closed the stream early) must still end
	// with a finish chunk and [DONE], not hang or truncate the client's view.
	body := []byte(`{"type":"text-delta","text":"hel"}` + "\n")
	c, rec := newGinRecorder(t)
	usage, errResp := HandleResponse(c, upstreamResp(body), &meta.Meta{IsStream: true, ActualModelName: "m"})
	if errResp != nil {
		t.Fatalf("HandleResponse: %+v", errResp)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("stream has no finish chunk: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream has no [DONE]: %s", out)
	}
	if usage == nil {
		t.Error("usage must not be nil even without a finish event")
	}
}

func TestHandleResponse_SkipNonJSONLines(t *testing.T) {
	body := []byte(": keep-alive\n\n" +
		`{"type":"text-delta","text":"ok"}` + "\n" +
		"garbage-that-is-not-json\n" +
		`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}` + "\n")
	c, rec := newGinRecorder(t)
	_, errResp := HandleResponse(c, upstreamResp(body), &meta.Meta{IsStream: false, ActualModelName: "m"})
	if errResp != nil {
		t.Fatalf("HandleResponse: %+v", errResp)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if reply.Choices[0].Message.Content != "ok" {
		t.Errorf("content = %q, want ok", reply.Choices[0].Message.Content)
	}
}

func TestSessionIDShape(t *testing.T) {
	id := sessionID()
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("session id %q is not uuid shaped", id)
	}
	if id[14] != '4' {
		t.Errorf("session id %q is not v4", id)
	}
	if sessionID() == id {
		t.Error("session ids must differ per call")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
