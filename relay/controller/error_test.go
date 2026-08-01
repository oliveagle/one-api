package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ToMessage extracts a human-readable message from a heterogeneous upstream
// error body. Each vendor uses a different JSON shape; we have to find the
// non-empty one and ignore the rest.
func TestToMessage_PicksFirstNonEmpty(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"openai_format", `{"error":{"message":"rate limited"}}`, "rate limited"},
		{"message_top_level", `{"message":"oh no"}`, "oh no"},
		{"msg_top_level", `{"msg":"different vendor"}`, "different vendor"},
		{"err_top_level", `{"err":"another one"}`, "another one"},
		{"error_msg_field", `{"error_msg":"fourth vendor"}`, "fourth vendor"},
		{"baidu_header", `{"header":{"message":"baidu style"}}`, "baidu style"},
		{"response_error", `{"response":{"error":{"message":"nested"}}}`, "nested"},
		{"empty_keeps_default", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r GeneralErrorResponse
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := r.ToMessage(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ToMessage priority order: error.message > message > msg > err > error_msg
// > header.message > response.error.message. A regression in the order would
// surface the wrong message to the user (e.g. a generic "rate limited" loses
// to a specific "context_length_exceeded").
func TestToMessage_PriorityOrder(t *testing.T) {
	body := `{"error":{"message":"a"},` +
		`"message":"b",` +
		`"msg":"c",` +
		`"err":"d",` +
		`"error_msg":"e",` +
		`"header":{"message":"f"},` +
		`"response":{"error":{"message":"g"}}}`
	var r GeneralErrorResponse
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := r.ToMessage(); got != "a" {
		t.Fatalf("priority broken: got %q want a", got)
	}
}

// RelayErrorHandler must return a structured error with the upstream status
// code preserved so the relay layer can echo it back to the client.
func TestRelayErrorHandler_PreservesStatusCode(t *testing.T) {
	resp := httptest.NewRecorder().Result()
	resp.StatusCode = http.StatusTooManyRequests
	resp.Body = http.NoBody
	defer resp.Body.Close()

	got := RelayErrorHandler(resp)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got.StatusCode, http.StatusTooManyRequests)
	}
	if got.Error.Type != "upstream_error" {
		t.Fatalf("type = %q, want upstream_error", got.Error.Type)
	}
}

// An OpenAI-shaped body must override the default "upstream_error" message so
// the user sees the real reason rather than a generic "bad response".
func TestRelayErrorHandler_OpenAIShape(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{},
		Body:       httpBody(`{"error":{"message":"rate limit reached","type":"rate_limit_error","param":null,"code":"rate_limit"}}`),
	}
	got := RelayErrorHandler(resp)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.StatusCode != 429 {
		t.Fatalf("status = %d", got.StatusCode)
	}
	if got.Error.Message != "rate limit reached" {
		t.Fatalf("message = %q, want rate limit reached", got.Error.Message)
	}
	if got.Error.Type != "rate_limit_error" {
		t.Fatalf("type = %q", got.Error.Type)
	}
}

// Custom upstream format: when upstream returns just `{"msg": "..."}` we
// must still produce a sensible relay error.
func TestRelayErrorHandler_MsgField(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Header:     http.Header{},
		Body:       httpBody(`{"msg":"backend exploded"}`),
	}
	got := RelayErrorHandler(resp)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.Error.Message != "backend exploded" {
		t.Fatalf("message = %q, want backend exploded", got.Error.Message)
	}
}

// Empty JSON object body triggers the fallback "bad response status code %d".
func TestRelayErrorHandler_EmptyJSON(t *testing.T) {
	resp := &http.Response{
		StatusCode: 502,
		Header:     http.Header{},
		Body:       httpBody("{}"),
	}
	got := RelayErrorHandler(resp)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if !strings.Contains(got.Error.Message, "502") {
		t.Fatalf("message %q should mention status 502", got.Error.Message)
	}
}

// Non-JSON garbage body returns the upstream error without any message
// decoding. This is current behaviour; documenting it so a future change
// is intentional.
func TestRelayErrorHandler_GarbageBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Header:     http.Header{},
		Body:       httpBody("not-json"),
	}
	got := RelayErrorHandler(resp)
	if got == nil {
		t.Fatal("expected non-nil error")
	}
	if got.Error.Message != "" {
		t.Fatalf("expected empty message for non-JSON body, got %q", got.Error.Message)
	}
}

// httpBody wraps a string in an io.ReadCloser.
func httpBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}
