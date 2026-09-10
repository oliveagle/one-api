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
	mockadaptor "github.com/songquanpeng/one-api/relay/adaptor/mock"
	dbmodel "github.com/songquanpeng/one-api/model"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/routing"
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
// Category 1 — poisoned function_call arguments (upstream glitch shape)
//
// DATA SOURCE: 2026-09-09 incident. Upstream models occasionally emit a
// function_call whose arguments JSON is truncated mid-string (qwen3.8-27b
// on vLLM, streamed out with HTTP 200) or carries a non-JSON literal.
// Coding agents replay the full output history on every later request, and
// every upstream validator then hard-400s the WHOLE request — vLLM's
// chat_utils json.loads ("Unterminated string starting at: line 1 column 9
// (char 8)") or volcengine ark ("missing `input.arguments`
// MissingParameter") — wedging the session on every channel. The relay
// repairs the single malformed item before forwarding
// (relayResponsesCreate → sanitizeResponsesToolCallArgs); these tests pin
// that behavior end-to-end against the mock's strict-input validator.
// ===========================================================================

// poisonedHistoryInput builds a Responses input array shaped like the real
// incident: user turn, the model's poisoned function_call, and codex's own
// parse-failure tool output. args is the (possibly malformed) arguments
// payload; omitArguments drops the field entirely (ark's literal
// "missing input.arguments" complaint).
func poisonedHistoryInput(args string, omitArguments bool) []map[string]any {
	call := map[string]any{
		"type":    "function_call",
		"id":      "fc_1",
		"call_id": "call_1",
		"name":    "exec_command",
	}
	if !omitArguments {
		call["arguments"] = args
	}
	return []map[string]any{
		{"type": "message", "role": "user", "content": []map[string]any{
			{"type": "input_text", "text": "run the jwks check"},
		}},
		call,
		{"type": "function_call_output", "call_id": "call_1",
			"output": "failed to parse function arguments: EOF while parsing a string at line 1 column 178"},
	}
}

func TestCategory1_ResponsesPoisonedToolCall_ForwardedVerbatim(t *testing.T) {
	// Ground truth (AGENTS.md rule 1): the upstream's poisoned RESPONSE
	// must reach the client untouched — the relay repairs requests, never
	// responses. The client (codex) is what records the item into history.
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		supportResponses:       true,
		registerResponsesRoute: true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses-poisoned-tool-call", basicResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (upstreams stream the poison out as a success); body=%s",
			rec.Code, rec.Body.String())
	}
	var resp struct {
		Output []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	if len(resp.Output) != 1 || resp.Output[0].Type != "function_call" {
		t.Fatalf("expected one function_call output, got %s", rec.Body.String())
	}
	if resp.Output[0].Arguments != mockadaptor.PoisonedToolCallArguments {
		t.Errorf("upstream responses must be forwarded verbatim — arguments were mutated:\n got %q\nwant %q",
			resp.Output[0].Arguments, mockadaptor.PoisonedToolCallArguments)
	}
}

func TestCategory1_PoisonedHistoryArguments_SanitizedBeforeUpstream(t *testing.T) {
	cases := []struct {
		name string
		args string
		omit bool
	}{
		{name: "truncated string (the vLLM Unterminated-string incident)", args: mockadaptor.PoisonedToolCallArguments},
		{name: "invalid JSON literal (write_stdin 'none')", args: `{"session_id": none, "yield_time_ms": 1000}`},
		{name: "missing arguments (ark MissingParameter)", omit: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := setupMockRelayStackWithOptions(t, mockStackOptions{
				supportResponses:       true,
				registerResponsesRoute: true,
			})
			body := basicResponsesBody(map[string]any{
				"input": poisonedHistoryInput(tc.args, tc.omit),
			})
			// openai-responses-strict-input rejects any history that still
			// carries malformed arguments, so a 200 here proves the
			// sanitizer repaired the item before forwarding. Without the
			// repair this request 400s on every real upstream and the
			// session is wedged forever.
			rec := doRelayRequestTo(t, r, "/v1/responses",
				"Bearer sk-test", "openai-responses-strict-input", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (sanitizer must repair the poisoned item); body=%s",
					rec.Code, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("response not JSON: %v", err)
			}
			if obj, _ := resp["object"].(string); obj != "response" {
				t.Errorf("object = %q, want \"response\"", obj)
			}
		})
	}
}

func TestCategory1_HealthyToolCallHistory_PassesStrictUpstream(t *testing.T) {
	// No-false-positive pin: a history whose arguments are all valid JSON
	// must reach a strict upstream unchanged (200), i.e. the sanitizer is
	// a no-op on healthy traffic.
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		supportResponses:       true,
		registerResponsesRoute: true,
	})
	input := []map[string]any{
		{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "shell",
			"arguments": `{"cmd":["ls","-la"]}`},
		{"type": "function_call_output", "call_id": "call_1", "output": "total 0"},
	}
	body := basicResponsesBody(map[string]any{"input": input})
	rec := doRelayRequestTo(t, r, "/v1/responses",
		"Bearer sk-test", "openai-responses-strict-input", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for healthy history; body=%s", rec.Code, rec.Body.String())
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

// ===========================================================================
// Channel-name model addressing (the codex /model use case)
//
// A request model that names a channel pins that one channel: the bare
// channel name serves its configured default_model, and
// "channel-name/model" serves any model from the channel's list. Names the
// pool can already route are never hijacked.
// ===========================================================================

func TestChannelAddressing_BareNameServesDefaultModel(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: mockModelName,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":"mock-channel","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	// The upstream must see the channel's default_model, not the bare
	// channel name (the mock echoes the rewritten wire model).
	if m, _ := resp["model"].(string); m != mockModelName {
		t.Errorf("upstream model = %q, want %q (default_model mapping)", m, mockModelName)
	}
}

func TestChannelAddressing_ExplicitModelFromChannelList(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: mockModelName,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":"mock-channel/`+mockModelName+`","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if m, _ := resp["model"].(string); m != mockModelName {
		t.Errorf("upstream model = %q, want %q", m, mockModelName)
	}
}

func TestChannelAddressing_ModelNotInChannelList(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: mockModelName,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		`{"model":"mock-channel/not-a-listed-model","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (model outside the channel's list); body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "不在渠道") {
		t.Errorf("error should name the channel/model mismatch: %s", rec.Body.String())
	}
}

func TestChannelAddressing_BareNameWithoutDefaultFallsThrough(t *testing.T) {
	// No default_model configured: the bare channel name must NOT be
	// hijacked away from pool routing (which then 503s — no ability named
	// "mock-channel").
	r := setupMockRelayStackWithOptions(t, mockStackOptions{})
	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		`{"model":"mock-channel","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (pool routing owns the name); body=%s", rec.Code, rec.Body.String())
	}
}

func TestChannelAddressing_ResponsesWirePassthrough(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel:           mockModelName,
		registerResponsesRoute: true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses", "Bearer sk-test", "openai-responses",
		`{"model":"mock-channel","input":"hi","stream":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// The passthrough rewrites the model onto default_model before the
	// upstream sees it.
	if m, _ := resp["model"].(string); m != mockModelName {
		t.Errorf("upstream model = %q, want %q", m, mockModelName)
	}
}

func TestChannelAddressing_ResolvesThroughChannelMapping(t *testing.T) {
	// "channel/listed-model" must resolve through the channel's own
	// model_mapping, exactly like requesting that model normally would.
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: mockModelName,
		modelMapping: `{"` + mockModelName + `":"mock-upstream-alias"}`,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":"mock-channel/`+mockModelName+`","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if m, _ := resp["model"].(string); m != "mock-upstream-alias" {
		t.Errorf("upstream model = %q, want mock-upstream-alias (chain through model_mapping)", m)
	}
}

func TestChannelAddressing_DefaultModelMayBeUpstreamName(t *testing.T) {
	// default_model names an upstream model directly — it need NOT appear
	// in the channel's exposed model list (e.g. volc-1 → deepseek-v4-flash).
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: "raw-upstream-model",
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":"mock-channel","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if m, _ := resp["model"].(string); m != "raw-upstream-model" {
		t.Errorf("upstream model = %q, want raw-upstream-model", m)
	}
}

func TestChannelAddressing_TrimsSurroundingWhitespace(t *testing.T) {
	// codex 0.150's -m flag can emit a leading space; the relay trims the
	// model name so addressing and pool lookups are immune.
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		defaultModel: mockModelName,
	})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		`{"model":" mock-channel ","messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trimmed to mock-channel); body=%s", rec.Code, rec.Body.String())
	}
}

// ===========================================================================
// 429 routing penalties: quota/rate-limit 429s mark a global cooldown so
// random and sticky picks steer around the throttled channel while
// alternatives exist; the plain pick remains the fallback when everything
// is cooling.
// ===========================================================================

func TestMarkChannelPenalty_Classification(t *testing.T) {
	reset := func() { dbmodel.ResetChannelCooldowns() }

	t.Run("quota 429 with reset time", func(t *testing.T) {
		reset()
		err := &relaymodel.ErrorWithStatusCode{
			StatusCode: http.StatusTooManyRequests,
			Error:      relaymodel.Error{Message: "You have exceeded the monthly usage quota. It will reset at 2026-08-27 23:59:59 +0800 CST. We recommend upgrading your plan."},
		}
		markChannelPenalty(42, err)
		if !dbmodel.ChannelCoolingDown(42) {
			t.Fatal("quota 429 must cool the channel down")
		}
	})

	t.Run("kimi weekly limit without reset time", func(t *testing.T) {
		reset()
		err := &relaymodel.ErrorWithStatusCode{
			StatusCode: http.StatusTooManyRequests,
			Error:      relaymodel.Error{Message: "You've reached your weekly (7-day) usage limit. Your quota will reset soon."},
		}
		markChannelPenalty(43, err)
		if !dbmodel.ChannelCoolingDown(43) {
			t.Fatal("usage-limit 429 must cool the channel down")
		}
	})

	t.Run("plain rate limit gets a short cooldown", func(t *testing.T) {
		reset()
		err := &relaymodel.ErrorWithStatusCode{
			StatusCode: http.StatusTooManyRequests,
			Error:      relaymodel.Error{Message: "rate limited by mock"},
		}
		markChannelPenalty(44, err)
		if !dbmodel.ChannelCoolingDown(44) {
			t.Fatal("plain 429 must cool the channel down (short window)")
		}
	})

	t.Run("non-429 carries no penalty", func(t *testing.T) {
		reset()
		err := &relaymodel.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      relaymodel.Error{Message: "upstream exploded"},
		}
		markChannelPenalty(45, err)
		if dbmodel.ChannelCoolingDown(45) {
			t.Fatal("5xx must not trigger the 429 penalty registry")
		}
	})
}

func TestRelay429Penalty_CooldownMarkedAndFallbackServes(t *testing.T) {
	// Single-channel stack: a mock 429 fails the request AND marks the
	// cooldown; the NEXT request (healthy behavior) must still be served by
	// the same channel — with no alternative the picker falls back instead
	// of returning "no channel".
	r := setupMockRelayStack(t)

	rec := doRelayRequest(t, r, "Bearer sk-test", "error-429",
		basicChatBody(map[string]any{"model": mockModelName}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if !dbmodel.ChannelCoolingDown(1) {
		t.Fatal("the 429-ing channel must be marked cooling")
	}

	rec = doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		basicChatBody(map[string]any{"model": mockModelName}))
	if rec.Code != http.StatusOK {
		t.Fatalf("follow-up request must fall back to the cooling channel; status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestWireMismatchDoesNotStickyCool pins fairness: a protocol-mismatch 503
// (responses request landing on a chat-only channel) must retry through the
// pool but must NOT put the channel into sticky cooldown — the channel is
// healthy, the pool composition is at fault.
func TestWireMismatchDoesNotStickyCool(t *testing.T) {
	// Budget for the failover walk past the chat-only channel.
	prevRetry := config.RetryTimes
	config.RetryTimes = 3
	t.Cleanup(func() { config.RetryTimes = prevRetry })
	// The sticky router is process-global: earlier tests may have left
	// sticky cooldowns on both channels, which would force the fresh
	// binding onto the chat-only channel with no failover path.
	for _, st := range routing.DefaultRouter().Store().ChannelStates() {
		routing.DefaultRouter().Store().ClearCooldown(st.ChannelId)
	}
	r := setupMockRelayStackWithOptions(t, mockStackOptions{
		registerResponsesRoute: true,
		extraResponsesChannel:  true,
	})
	rec := doRelayRequestTo(t, r, "/v1/responses", "Bearer sk-test", "openai-responses",
		basicResponsesBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover; body=%s", rec.Code, rec.Body.String())
	}
	// Whatever channel refused must not be in sticky cooldown afterwards:
	// both channels stay selectable for a subsequent chat request.
	rec = doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		basicChatBody(map[string]any{"model": mockModelName}))
	if rec.Code != http.StatusOK {
		t.Fatalf("follow-up chat request failed: %d %s", rec.Code, rec.Body.String())
	}
	store := routing.DefaultRouter().Store()
	for _, st := range store.ChannelStates() {
		if !st.CoolingUntil.IsZero() {
			t.Fatalf("channel %d left in sticky cooldown by a wire-mismatch refusal", st.ChannelId)
		}
	}
}

// TestTokenRPMLimit_Exceeded pins the per-token throttle end to end: with
// rpm_limit=2 the third rapid relay POST gets 429 with a Retry-After hint
// and the token_rpm_exceeded code, while a request under the limit sails
// through the whole pipeline.
func TestTokenRPMLimit_Exceeded(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{tokenRPM: 2})

	for i := 1; i <= 2; i++ {
		rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
			basicChatBody(map[string]any{"model": mockModelName}))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d under the limit failed: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
		basicChatBody(map[string]any{"model": mockModelName}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" {
		t.Error("429 must carry a Retry-After header")
	}
	if !strings.Contains(rec.Body.String(), "token_rpm_exceeded") {
		t.Errorf("error should carry token_rpm_exceeded: %s", rec.Body.String())
	}
}

// TestTokenRPMLimit_ZeroDisables: the default (0) never throttles.
func TestTokenRPMLimit_ZeroDisables(t *testing.T) {
	r := setupMockRelayStackWithOptions(t, mockStackOptions{})
	for i := 0; i < 10; i++ {
		rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat",
			basicChatBody(map[string]any{"model": mockModelName}))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d throttled despite rpm_limit=0: %d", i, rec.Code)
		}
	}
}
