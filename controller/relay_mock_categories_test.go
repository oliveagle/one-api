package controller

// Three categories of mock-channel integration tests, one per data-flow
// path through the relay. See AGENTS.md ("Three test categories") for
// the full rationale; the short version:
//
// The product direction is to standardize on the OpenAI Responses API
// (/v1/responses) inside coding agents. But individual channels/upstreams
// vary in what they natively support, so the relay has three distinct
// data-flow paths that MUST each be pinned by tests before touching the
// others:
//
//   Category 1 — responses -> responses (passthrough)
//     Client speaks Responses; the channel's upstream also speaks
//     Responses natively (channel config support_responses=true). The
//     request body is forwarded byte-for-byte and the Responses-shaped
//     response comes back untouched. This is the path coding agents will
//     hit once the ecosystem catches up.
//
//   Category 2 — chat -> chat (passthrough)
//     Client speaks Chat Completions; the upstream speaks Chat
//     Completions. This is the legacy/compat path. These tests live in
//     relay_mock_integration_test.go (TestRelayMock_*).
//
//   Category 3 — responses -> chat (conversion)
//     Client speaks Responses; the upstream only speaks Chat Completions
//     (support_responses NOT set). relayResponsesCreate converts the
//     Responses request into a Chat Completions request and delegates to
//     RelayTextHelper. This is the transition path that lets agents
//     adopt the Responses API today even when channels haven't upgraded.
//
// DEVELOPMENT MODE (see AGENTS.md):
// When changing channel/provider behavior, extend categories 1 and 2
// FIRST to pin the provider's native shapes, then improve category 3
// (the conversion) against those pinned shapes. This stops the
// conversion path from drifting away from what real providers return.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/model"
)

// basicResponsesBody returns a minimal Responses API request body for
// the mock model. The Responses API uses "input" (string or item array)
// instead of "messages". Override fields via mods (later wins).
func basicResponsesBody(mods ...map[string]any) string {
	m := map[string]any{
		"model": mockModelName,
		"input": "Hello from a Responses client.",
	}
	for _, mod := range mods {
		for k, v := range mod {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ===========================================================================
// Category 1 — responses -> responses (passthrough)
//
// The channel has support_responses=true, so relayResponsesCreate
// forwards the Responses request verbatim and the mock adaptor (driven
// by X-Mock-Behavior: openai-responses) synthesizes a Responses-shaped
// reply. The client should see object:"response" with output[] and
// usage.input_tokens — NOT a Chat Completions shape.
// ===========================================================================

func TestCategory1_ResponsesToResponses_NonStream(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		supportResponses:       true,
		registerResponsesRoute: true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses", basicResponsesBody())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	// Must be a Responses envelope, NOT a Chat Completions one.
	if obj, _ := resp["object"].(string); obj != "response" {
		t.Errorf("object = %q, want \"response\" (Responses shape)", obj)
	}
	if _, ok := resp["choices"]; ok {
		t.Errorf("response must NOT contain \"choices\" (that's Chat shape); got %s", rec.Body.String())
	}
	output, _ := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("expected non-empty output array")
	}
	// usage must use Responses field names (input_tokens, not prompt_tokens)
	usage, _ := resp["usage"].(map[string]any)
	if it, _ := usage["input_tokens"].(float64); it == 0 {
		t.Errorf("expected input_tokens > 0, got %v", usage)
	}
	if _, hasPrompt := usage["prompt_tokens"]; hasPrompt {
		t.Errorf("usage must use input_tokens not prompt_tokens (Responses shape)")
	}
}

func TestCategory1_ResponsesToResponses_Stream(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		supportResponses:       true,
		registerResponsesRoute: true,
	})
	body := basicResponsesBody(map[string]any{"stream": true})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	out := rec.Body.String()
	// Responses SSE carries event: lines (unlike Chat's data:-only).
	for _, want := range []string{
		"event: response.created",
		"event: response.completed",
		`"type":"response.completed"`,
		"[DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stream body missing %q:\n%s", want, out)
		}
	}
}

// ===========================================================================
// Category 3 — responses -> chat (conversion)
//
// The channel does NOT set support_responses, so relayResponsesCreate
// converts the Responses request to Chat Completions and delegates to
// RelayTextHelper. The mock adaptor synthesizes a Chat Completions
// response (default openai-chat behavior). The chat pipeline's output
// is then converted BACK into Responses format by
// chatToResponsesWriter, so the client always sees Responses shape on
// /v1/responses regardless of which path served it.
// ===========================================================================

func TestCategory3_ResponsesToChat_NonStream(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		// supportResponses deliberately FALSE → conversion kicks in.
		registerResponsesRoute: true,
	})
	// No X-Mock-Behavior: the conversion routes through RelayTextHelper
	// which hits the mock's default openai-chat behavior.
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "", basicResponsesBody())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	// The client spoke the Responses protocol, so the converted reply
	// must come back in Responses shape — the chat pipeline's output is
	// converted back by chatToResponsesWriter.
	if obj, _ := resp["object"].(string); obj != "response" {
		t.Errorf("object = %q, want \"response\" (converted back from chat)", obj)
	}
	output, _ := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("expected non-empty output[] (Responses shape), got %v", resp["output"])
	}
	msg, _ := output[0].(map[string]any)
	if ty, _ := msg["type"].(string); ty != "message" {
		t.Errorf("output[0].type = %q, want message", ty)
	}
	content, _ := msg["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("output[0].content empty: %v", msg["content"])
	}
	part, _ := content[0].(map[string]any)
	if ty, _ := part["type"].(string); ty != "output_text" {
		t.Errorf("content[0].type = %q, want output_text", ty)
	}
	if txt, _ := part["text"].(string); txt == "" {
		t.Errorf("content[0].text empty — mock reply text lost in conversion")
	}
	// Responses usage field names.
	usage, _ := resp["usage"].(map[string]any)
	if it, _ := usage["input_tokens"].(float64); it == 0 {
		t.Errorf("usage.input_tokens = %v, want > 0 (mapped from prompt_tokens)", usage["input_tokens"])
	}
}

func TestCategory3_ResponsesToChat_Stream(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true, // supportResponses=false → convert
	})
	body := basicResponsesBody(map[string]any{"stream": true})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	// The chat SSE stream must be translated into the Responses event
	// vocabulary so Responses clients (codex et al.) can parse it.
	for _, want := range []string{
		"event: response.created",
		"event: response.output_item.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.output_item.done",
		"event: response.completed",
		"data: [DONE]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("converted stream missing %q:\n%s", want, out)
		}
	}
	// The completed event carries the usage block mapped from chat
	// prompt/completion tokens.
	if !strings.Contains(out, "\"input_tokens\"") {
		t.Errorf("response.completed missing Responses-style usage (input_tokens):\n%s", out)
	}
}

// TestCategory3_ResponsesToChat_ToolCallRoundTrip pins the tool-call
// conversion path: a Responses request carrying tools goes upstream as
// a Chat Completions tools array, the chat tool_calls reply comes back
// as a Responses function_call output item (what codex-style clients
// act on).
func TestCategory3_ResponsesToChat_ToolCallRoundTrip(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true, // supportResponses=false → convert
	})
	body := basicResponsesBody(map[string]any{
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get weather",
				"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
			},
		}},
	})
	rec := doRelayRequestTo(t, r, "/v1/responses", "Bearer sk-test", "openai-tool-call", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	if obj, _ := resp["object"].(string); obj != "response" {
		t.Fatalf("object = %q, want response", obj)
	}
	output, _ := resp["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("output empty: %v", resp["output"])
	}
	call, _ := output[0].(map[string]any)
	if ty, _ := call["type"].(string); ty != "function_call" {
		t.Fatalf("output[0].type = %q, want function_call (chat tool_calls converted back)", ty)
	}
	if name, _ := call["name"].(string); name != "get_weather" {
		t.Errorf("function_call.name = %q, want get_weather", name)
	}
	if callID, _ := call["call_id"].(string); callID == "" {
		t.Errorf("function_call.call_id empty — clients need it to send the tool output back")
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call["arguments"].(string)), &args); err != nil {
		t.Errorf("function_call.arguments not valid JSON: %v", call["arguments"])
	}
}

func TestCategory3_ResponsesToChat_QuotaAccounting(t *testing.T) {
	// The conversion path bills through RelayTextHelper, so quota must
	// settle on User.UsedQuota exactly like a native chat request. This
	// guards that conversion doesn't accidentally skip billing.
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "", basicResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	model.BatchUpdate()
	var user model.User
	if err := model.DB.First(&user, 1).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if user.UsedQuota <= 0 {
		t.Errorf("User.UsedQuota = %d, want > 0 (conversion must bill)", user.UsedQuota)
	}
}

// ===========================================================================
// Cross-category sanity: the SAME channel flips between passthrough and
// conversion based solely on support_responses. This pins the routing
// decision so a config regression can't silently swap the path.
// ===========================================================================

func TestCategoryRouting_FlagControlsPassthroughVsConversion(t *testing.T) {
	t.Run("support_responses_true_returns_responses_shape", func(t *testing.T) {
		r := setupMockRelayStackWithOptions(t, mockStackOptions{
			supportResponses:       true,
			registerResponsesRoute: true,
		})
		rec := doRelayRequestTo(t, r, "/v1/responses",
			"Bearer sk-test", "openai-responses", basicResponsesBody())
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["object"] != "response" {
			t.Errorf("support_responses=true should passthrough Responses shape, got object=%v", resp["object"])
		}
	})
	t.Run("support_responses_false_converts_to_chat", func(t *testing.T) {
		r := setupMockRelayStackWithOptions(t, mockStackOptions{
			registerResponsesRoute: true,
		})
		rec := doRelayRequestTo(t, r, "/v1/responses",
			"Bearer sk-test", "", basicResponsesBody())
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		if resp["object"] != "response" {
			t.Errorf("support_responses=false converts to chat upstream but must reply in Responses shape, got object=%v", resp["object"])
		}
	})
}
