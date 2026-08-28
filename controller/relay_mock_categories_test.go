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
//   Category 3 — responses request on a chat-only channel (REFUSED)
//     Client speaks Responses; the channel's upstream only speaks Chat
//     Completions (support_responses NOT set). Protocol conversion has
//     been REMOVED: the relay refuses with 503 (retryable) so failover
//     can reach a responses-capable channel; symmetrically, a chat
//     request landing on a responses_only channel is refused so failover
//     reaches a chat channel. Pools are split by wire protocol
//     (e.g. coding_resps / coding_chat) instead of converting.
//
// DEVELOPMENT MODE (see AGENTS.md):
// When changing channel/provider behavior, extend categories 1 and 2
// FIRST to pin the provider's native shapes. Category 3 pins the
// refusal + failover semantics. This stops the
// conversion path from drifting away from what real providers return.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
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
// Category 3 — responses request on a chat-only channel
//
// Protocol conversion between the Responses and Chat Completions APIs has
// been REMOVED: a /v1/responses request that lands on a channel whose
// upstream does not natively serve the Responses API is refused with 503
// (a retryable status) so the relay's failover can walk the rest of the
// pool; when every channel for the model is chat-only the client sees the
// 503. The chat direction is symmetric: a chat request landing on a
// responses_only channel is likewise refused so failover reaches a chat
// channel.
// ===========================================================================

func TestCategory3_ResponsesOnChatOnlyChannel_Refused(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true, // supportResponses deliberately FALSE
	})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses", basicResponsesBody())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no conversion; single chat-only channel); body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	errObj, _ := resp["error"].(map[string]any)
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "does not support the Responses API") {
		t.Errorf("error.message = %q, want it to name the unsupported channel", msg)
	}
	if code, _ := errObj["code"].(string); code != "responses_unsupported_on_channel" {
		t.Errorf("error.code = %q, want responses_unsupported_on_channel", code)
	}
}

// TestCategory3_FailoverReachesResponsesCapableChannel pins the 503-driven
// failover: with one chat-only and one responses-capable channel serving the
// same model, a Responses request must end in 200 regardless of which channel
// the random pick starts on.
func TestCategory3_FailoverReachesResponsesCapableChannel(t *testing.T) {
	// Give the relay budget to retry past the chat-only channel.
	prevRetry := config.RetryTimes
	config.RetryTimes = 3
	t.Cleanup(func() { config.RetryTimes = prevRetry })
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true,
		extraResponsesChannel:  true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses", basicResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover onto the responses-capable channel; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if obj, _ := resp["object"].(string); obj != "response" {
		t.Errorf("object = %q, want response (native passthrough shape)", obj)
	}
}

// TestCategory3_ChatOnResponsesOnlyChannel_Refused pins the symmetric guard
// in RelayTextHelper: chat-completions requests may only be served by
// chat-capable channels.
func TestCategory3_ChatOnResponsesOnlyChannel_Refused(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		responsesOnly: true,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":"`+mockModelName+`","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (chat refused on responses-only channel); body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	errObj, _ := resp["error"].(map[string]any)
	if code, _ := errObj["code"].(string); code != "chat_unsupported_on_channel" {
		t.Errorf("error.code = %q, want chat_unsupported_on_channel", code)
	}
}

// ===========================================================================
// Cross-category sanity: the SAME channel flips between passthrough and
// refusal based solely on support_responses. This pins the routing decision
// so a config regression can't silently resurrect conversion.
// ===========================================================================

func TestCategoryRouting_FlagControlsPassthroughVsRefusal(t *testing.T) {
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
	t.Run("support_responses_false_refused_with_503", func(t *testing.T) {
		r := setupMockRelayStackWithOptions(t, mockStackOptions{
			registerResponsesRoute: true,
		})
		rec := doRelayRequestTo(t, r, "/v1/responses",
			"Bearer sk-test", "openai-responses", basicResponsesBody())
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status=%d body=%s — conversion is gone; a chat-only channel must refuse", rec.Code, rec.Body.String())
		}
	})
}
