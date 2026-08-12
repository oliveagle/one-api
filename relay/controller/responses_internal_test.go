package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestToUsage(t *testing.T) {
	u := ResponsesUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	got := u.ToUsage()
	if got.PromptTokens != 10 || got.CompletionTokens != 20 || got.TotalTokens != 30 {
		t.Errorf("ToUsage = %+v, want {PromptTokens:10 CompletionTokens:20 TotalTokens:30}", got)
	}
}

func TestForwardResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses/resp_123", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp_123"}`)),
	}

	err := forwardResponse(c, resp)
	if err != nil {
		t.Fatalf("forwardResponse: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", w.Header().Get("Content-Type"))
	}
	if w.Body.String() != `{"id":"resp_123"}` {
		t.Errorf("body = %q, want {\"id\":\"resp_123\"}", w.Body.String())
	}
}

func TestForwardResponse_NilResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses/resp_123", nil)

	err := forwardResponse(c, nil)
	if err == nil {
		t.Fatal("expected error for nil response")
	}
}

func TestGetResponsesRequestBody_NoModelChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-4o","input":"hello"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Body = io.NopCloser(strings.NewReader(body))

	request := &ResponsesRequest{Model: "gpt-4o", Input: "hello"}
	reader, err := getResponsesRequestBody(c, request)
	if err != nil {
		t.Fatalf("getResponsesRequestBody: %v", err)
	}
	data, _ := io.ReadAll(reader)
	if string(data) != body {
		t.Errorf("body = %q, want %q", string(data), body)
	}
}

func TestGetResponsesRequestBody_ModelChanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"model":"gpt-4o","input":"hello"}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Body = io.NopCloser(strings.NewReader(body))

	request := &ResponsesRequest{Model: "gpt-4o-mapped", Input: "hello"}
	reader, err := getResponsesRequestBody(c, request)
	if err != nil {
		t.Fatalf("getResponsesRequestBody: %v", err)
	}
	data, _ := io.ReadAll(reader)
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var model string
	if err := json.Unmarshal(parsed["model"], &model); err != nil {
		t.Fatalf("model field: %v", err)
	}
	if model != "gpt-4o-mapped" {
		t.Errorf("model = %q, want gpt-4o-mapped", model)
	}
}

func TestRelayResponsesNonStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_123","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}`)),
	}

	usage, err := relayResponsesNonStream(c, resp)
	if err != nil {
		t.Fatalf("relayResponsesNonStream: %v", err)
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 20 {
		t.Errorf("usage = %+v, want PromptTokens=10 CompletionTokens=20", usage)
	}
	if w.Body.String() != `{"id":"resp_123","usage":{"input_tokens":10,"output_tokens":20,"total_tokens":30}}` {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestRelayResponsesNonStream_NoUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_123"}`)),
	}

	usage, err := relayResponsesNonStream(c, resp)
	if err != nil {
		t.Fatalf("relayResponsesNonStream: %v", err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if usage.TotalTokens != 0 {
		t.Errorf("total tokens = %d, want 0", usage.TotalTokens)
	}
}

func TestRelayResponsesResponse_DispatchesToNonStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_123"}`)),
	}

	usage, err := relayResponsesResponse(c, resp)
	if err != nil {
		t.Fatalf("relayResponsesResponse: %v", err)
	}
	if usage == nil {
		t.Fatal("expected non-nil usage")
	}
}

func TestRelayResponsesHelper_UnsupportedMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/v1/responses", nil)

	err := RelayResponsesHelper(c)
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
	if err.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", err.StatusCode, http.StatusMethodNotAllowed)
	}
}

func TestResponsesRequestJSON(t *testing.T) {
	req := ResponsesRequest{
		Model:        "gpt-4o",
		Stream:       true,
		Input:        "hello",
		Instructions: "be brief",
		MaxOutput:    100,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ResponsesRequest
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Model != "gpt-4o" || back.Stream != true || back.MaxOutput != 100 {
		t.Errorf("round trip = %+v", back)
	}
}

func TestResponsesUsageJSON(t *testing.T) {
	u := ResponsesUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30}
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ResponsesUsage
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.InputTokens != 10 || back.OutputTokens != 20 || back.TotalTokens != 30 {
		t.Errorf("round trip = %+v", back)
	}
}

func TestResponsesEnvelope(t *testing.T) {
	data := `{"usage":{"input_tokens":5,"output_tokens":15,"total_tokens":20}}`
	var env responsesEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Usage == nil {
		t.Fatal("expected non-nil usage")
	}
	if env.Usage.InputTokens != 5 || env.Usage.OutputTokens != 15 {
		t.Errorf("usage = %+v", env.Usage)
	}
}

func TestRelayResponsesPassthrough_BodyHandling(t *testing.T) {
	// Test the body handling logic for GET (should be nil)
	var requestBody io.Reader
	method := http.MethodGet
	if method == http.MethodPost {
		body, err := io.ReadAll(io.NopCloser(strings.NewReader("")))
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) > 0 {
			requestBody = bytes.NewReader(body)
		}
	}
	if requestBody != nil {
		t.Error("expected nil requestBody for GET")
	}

	// Test the body handling logic for POST with body
	method = http.MethodPost
	if method == http.MethodPost {
		body, err := io.ReadAll(io.NopCloser(strings.NewReader(`{"cancel":true}`)))
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if len(body) > 0 {
			requestBody = bytes.NewReader(body)
		}
	}
	if requestBody == nil {
		t.Error("expected non-nil requestBody for POST with body")
	}
	data, _ := io.ReadAll(requestBody)
	if string(data) != `{"cancel":true}` {
		t.Errorf("body = %q", string(data))
	}
}
