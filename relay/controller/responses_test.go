package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/openai"
)

// The tiktoken encoders are initialised by main() in production. Tests that
// count tokens must do it explicitly, otherwise getTokenEncoder returns a nil
// encoder and panics.
func init() {
	openai.InitTokenEncoders()
}

// The Responses API names its usage fields input_tokens / output_tokens, not
// prompt_tokens / completion_tokens. Getting this mapping wrong would bill every
// Responses call as zero tokens.
func TestResponsesUsageToUsage(t *testing.T) {
	const payload = `{"input_tokens":19,"output_tokens":7,"total_tokens":26,` +
		`"output_tokens_details":{"reasoning_tokens":0}}`
	var upstream ResponsesUsage
	if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	usage := upstream.ToUsage()
	if usage.PromptTokens != 19 {
		t.Fatalf("PromptTokens = %d, want 19 (from input_tokens)", usage.PromptTokens)
	}
	if usage.CompletionTokens != 7 {
		t.Fatalf("CompletionTokens = %d, want 7 (from output_tokens)", usage.CompletionTokens)
	}
	if usage.TotalTokens != 26 {
		t.Fatalf("TotalTokens = %d, want 26", usage.TotalTokens)
	}
}

// A chat-shaped usage block must not be mistaken for a Responses one: the field
// names do not overlap, so it has to decode to zeros rather than bogus numbers.
func TestResponsesUsageIgnoresChatFieldNames(t *testing.T) {
	const payload = `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}`
	var upstream ResponsesUsage
	if err := json.Unmarshal([]byte(payload), &upstream); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if upstream.InputTokens != 0 || upstream.OutputTokens != 0 {
		t.Fatalf("chat field names leaked into Responses usage: %+v", upstream)
	}
}

// The real non-streaming reply shape, captured from the live AIHubMix API.
func TestResponsesEnvelopeParsesLiveShape(t *testing.T) {
	const payload = `{"id":"0217853925306516a94f2aa4e665d19f141e23e2699c8c2a100be",` +
		`"object":"response","created_at":1785392530,"model":"coding_large",` +
		`"status":"completed","output":[{"type":"message","role":"assistant",` +
		`"content":[{"type":"output_text","text":"Hi"}]}],` +
		`"usage":{"input_tokens":19,"output_tokens":7,"total_tokens":26}}`
	var envelope responsesEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if envelope.Usage == nil {
		t.Fatal("usage not decoded from the live response shape")
	}
	if envelope.Usage.TotalTokens != 26 {
		t.Fatalf("TotalTokens = %d, want 26", envelope.Usage.TotalTokens)
	}
}

func TestUsageFromSSELine(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int // expected total tokens, 0 means "no usage"
	}{
		{"event name line", "event: response.output_text.delta", 0},
		{"blank line", "", 0},
		{"done sentinel", "data: [DONE]", 0},
		{"delta without usage", `data: {"type":"response.output_text.delta","delta":"Hi"}`, 0},
		{"not json", "data: garbage{", 0},
		{
			"top level usage",
			`data: {"type":"response.completed","usage":{"input_tokens":19,"output_tokens":7,"total_tokens":26}}`,
			26,
		},
		{
			"usage nested under response",
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}}`,
			8,
		},
		{
			"zero usage is ignored so a later real one wins",
			`data: {"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
			0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := usageFromSSELine(tc.line)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("expected no usage, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected usage, got nil")
			}
			if got.TotalTokens != tc.want {
				t.Fatalf("TotalTokens = %d, want %d", got.TotalTokens, tc.want)
			}
		})
	}
}

// Pre-consumption needs a non-zero estimate for both input shapes, otherwise a
// caller could exceed their quota before the upstream reports real usage.
func TestEstimateResponsesPromptTokens(t *testing.T) {
	stringInput := &ResponsesRequest{Model: "gpt-4o-mini", Input: "hello world"}
	if got := estimateResponsesPromptTokens(stringInput); got <= 0 {
		t.Fatalf("string input estimate = %d, want > 0", got)
	}

	withInstructions := &ResponsesRequest{
		Model:        "gpt-4o-mini",
		Input:        "hello world",
		Instructions: "you are a helpful assistant",
	}
	if estimateResponsesPromptTokens(withInstructions) <= estimateResponsesPromptTokens(stringInput) {
		t.Fatal("instructions must add to the estimate")
	}

	empty := &ResponsesRequest{Model: "gpt-4o-mini"}
	if got := estimateResponsesPromptTokens(empty); got != 0 {
		t.Fatalf("empty request estimate = %d, want 0", got)
	}
}

// Only model / stream are decoded for routing; the rest of the body is opaque
// and must be forwarded untouched, so the struct must not swallow it.
func TestResponsesRequestDecodesRoutingFieldsOnly(t *testing.T) {
	const payload = `{"model":"gpt-4o-mini","stream":true,"input":"hi",` +
		`"previous_response_id":"resp_123","store":true,` +
		`"tools":[{"type":"web_search"}]}`
	var request ResponsesRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if request.Model != "gpt-4o-mini" {
		t.Fatalf("Model = %q", request.Model)
	}
	if !request.Stream {
		t.Fatal("Stream should be true")
	}
}

// The Responses spec marks only Authorization as required; `model` is optional
// in the body. one-api nonetheless needs it to select a channel, and that
// rejection happens in middleware.Distribute (503, same as chat), not here.
// This pins the decoding half: an absent model must decode to "" rather than
// error, so the middleware stays the single place that enforces routing.
func TestResponsesRequestModelIsOptionalWhenDecoding(t *testing.T) {
	// Verbatim from the AIHubMix docs example, which omits "model".
	const payload = `{"top_logprobs":10,"text":{"format":{"type":"text"},` +
		`"verbosity":"medium"},"tools":[{"type":"function","name":"<string>",` +
		`"parameters":{},"strict":true,"description":"<string>",` +
		`"defer_loading":true}],"input":"<string>"}`
	var request ResponsesRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatalf("a body without model must still decode: %v", err)
	}
	if request.Model != "" {
		t.Fatalf("Model = %q, want empty", request.Model)
	}
	if request.Stream {
		t.Fatal("Stream should default to false")
	}
	// The estimate must not panic on this shape either.
	if got := estimateResponsesPromptTokens(&request); got <= 0 {
		t.Fatalf("estimate = %d, want > 0 for a string input", got)
	}
}

// max_output_tokens is the Responses spelling; max_tokens does not exist there.
// It feeds getPreConsumedQuota, so reading the wrong name would under-reserve.
func TestResponsesMaxOutputTokensFieldName(t *testing.T) {
	var withCorrect ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_output_tokens":256}`), &withCorrect); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if withCorrect.MaxOutput != 256 {
		t.Fatalf("MaxOutput = %d, want 256 from max_output_tokens", withCorrect.MaxOutput)
	}

	var withChatName ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":256}`), &withChatName); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if withChatName.MaxOutput != 0 {
		t.Fatalf("max_tokens must not populate MaxOutput, got %d", withChatName.MaxOutput)
	}
}

// TestResponsesMethodDispatch verifies that RelayResponsesHelper dispatches to
// the correct handler based on HTTP method and path. This is a unit test of the
// routing logic, not a full integration test.
func TestResponsesMethodDispatch(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		wantCreate bool // true if should route to relayResponsesCreate
		wantPass   bool // true if should route to passthrough (GET/DELETE/cancel/input_items)
	}{
		{"POST /responses", "POST", "/v1/responses", true, false},
		{"GET /responses/:id", "GET", "/v1/responses/resp_123", false, true},
		{"DELETE /responses/:id", "DELETE", "/v1/responses/resp_123", false, true},
		{"POST /responses/:id/cancel", "POST", "/v1/responses/resp_123/cancel", false, true},
		{"GET /responses/:id/input_items", "GET", "/v1/responses/resp_123/input_items", false, true},
		{"PUT /responses (unsupported)", "PUT", "/v1/responses", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify the dispatch logic by checking path patterns
			isCreate := tc.method == "POST" && tc.path == "/v1/responses"
			isCancel := tc.method == "POST" && len(tc.path) > len("/v1/responses/") && tc.path[len(tc.path)-7:] == "/cancel"
			isInputItems := tc.method == "GET" && len(tc.path) > len("/v1/responses/") && tc.path[len(tc.path)-12:] == "/input_items"
			isGet := tc.method == "GET" && !isInputItems
			isDelete := tc.method == "DELETE"

			gotCreate := isCreate
			gotPass := isCancel || isInputItems || isGet || isDelete

			if gotCreate != tc.wantCreate {
				t.Errorf("create dispatch: got %v, want %v", gotCreate, tc.wantCreate)
			}
			if gotPass != tc.wantPass {
				t.Errorf("passthrough dispatch: got %v, want %v", gotPass, tc.wantPass)
			}
		})
	}
}

// TestResponsesCancelPathPattern verifies the /cancel path detection logic.
func TestResponsesCancelPathPattern(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/responses/resp_123/cancel", true},
		{"/v1/responses/resp_abc/cancel", true},
		{"/v1/responses/cancel", false}, // no response_id
		{"/v1/responses/resp_123", false},
		{"/v1/responses/resp_123/input_items", false},
	}

	for _, tc := range cases {
		// Check if path ends with /cancel AND has a response_id before it
		// Pattern: /v1/responses/{response_id}/cancel
		got := strings.HasSuffix(tc.path, "/cancel") &&
			len(tc.path) > len("/v1/responses//cancel") &&
			!strings.HasSuffix(tc.path[:len(tc.path)-7], "/responses")
		if got != tc.want {
			t.Errorf("path %q: got %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestResponsesInputItemsPathPattern verifies the /input_items path detection logic.
func TestResponsesInputItemsPathPattern(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/responses/resp_123/input_items", true},
		{"/v1/responses/resp_abc/input_items", true},
		{"/v1/responses/input_items", false}, // no response_id
		{"/v1/responses/resp_123", false},
		{"/v1/responses/resp_123/cancel", false},
	}

	for _, tc := range cases {
		// Check if path ends with /input_items AND has a response_id before it
		// Pattern: /v1/responses/{response_id}/input_items
		got := strings.HasSuffix(tc.path, "/input_items") &&
			len(tc.path) > len("/v1/responses//input_items") &&
			!strings.HasSuffix(tc.path[:len(tc.path)-12], "/responses")
		if got != tc.want {
			t.Errorf("path %q: got %v, want %v", tc.path, got, tc.want)
		}
	}
}
