package controller

// End-to-end integration tests for the mock channel.
//
// CATEGORY 2 — chat -> chat (passthrough).
//
// Client speaks Chat Completions; the channel's upstream also speaks
// Chat Completions. These tests pin the legacy/compat data-flow path
// and the mock channel's OpenAI Chat shapes (non-stream, stream,
// tool_calls, errors, auth/routing failures, quota). See
// relay_mock_categories_test.go for the other two categories
// (responses->responses and responses->chat) and AGENTS.md for the
// three-category testing model.
//
// The harness (setupMockRelayStack / setupMockRelayStackWithOptions)
// spins up a real gin engine wired with the production middleware chain
// — TokenAuth -> Distribute -> controller.Relay -> (relay pipeline) ->
// mock.Adaptor — and drives it with httptest. The mock adaptor
// synthesizes responses in-process, so the whole relay pipeline (quota
// pre/post-consume, model mapping, streaming render, error handling,
// routing) is exercised without any network dependency.
//
// Behavior the mock channel should exhibit is selected per-request via
// the X-Mock-Behavior header (see relay/adaptor/mock/adaptor.go).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"fmt"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/testutil"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/channeltype"
	relaycontroller "github.com/songquanpeng/one-api/relay/controller"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// mockChannelBaseURL is the BaseURL the seeded mock channel advertises.
// It is never contacted — the mock adaptor synthesizes responses
// in-process — but it must be non-empty so meta.GetByContext does not
// fall back to the channeltype default (which is "http://mock" anyway).
const mockChannelBaseURL = "http://mock"

// mockModelName is the model the seeded channel serves. Requests must
// ask for this model so Distribute routes to the mock channel.
const mockModelName = "mock-gpt-4o"

// seedTokenKey is the token key stored in the DB. The HTTP client sends
// "Bearer sk-test"; TokenAuth strips "Bearer "/"sk-" and splits on "-"
// to arrive at this bare key. See middleware/auth.go.
const seedTokenKey = "test"

// mockStackOptions configures setupMockRelayStackWithOptions. The
// zero value produces a chat-only stack (the original behavior).
type mockStackOptions struct {
	// supportResponses seeds the channel with config.support_responses
	// =true so POST /v1/responses is forwarded as native Responses
	// passthrough. When false, /v1/responses requests are refused with 503
	// (protocol conversion was removed) and the relay's failover is expected
	// to find a responses-capable channel or surface the error.
	supportResponses bool
	// responsesOnly seeds the channel with config.responses_only=true so
	// POST /v1/chat/completions is refused with 503 by RelayTextHelper —
	// chat requests may only land on chat-capable channels.
	responsesOnly bool
	// extraResponsesChannel seeds a SECOND mock channel (id 2) with
	// support_responses=true serving the same model, so a stack whose
	// primary channel is chat-only can still demonstrate failover onto a
	// responses-capable channel.
	extraResponsesChannel bool
	// registerResponsesRoute adds POST /v1/responses to the gin engine
	// in addition to /v1/chat/completions. Tests that hit the
	// Responses endpoint must set this.
	registerResponsesRoute bool
	// defaultModel seeds config.default_model so the channel is
	// addressable by its bare name ("mock-channel") — the codex /model
	// channel-selection use case.
	defaultModel string
}

// setupMockRelayStack is the convenience wrapper for chat-only tests
// (test category 2). It mirrors the original signature.
func setupMockRelayStack(t *testing.T) *gin.Engine {
	t.Helper()
	return setupMockRelayStackWithOptions(t, mockStackOptions{})
}

// setupMockRelayStackWithOptions builds a production-shaped gin engine
// backed by a fresh SQLite DB seeded with a User, Token, and a Mock
// channel. It returns the engine ready to serve httptest requests.
//
// The cleanup (Redis flag, MemoryCacheEnabled, gin mode) is registered
// via t.Cleanup so tests are hermetic and parallel-safe.
func setupMockRelayStackWithOptions(t *testing.T, opts mockStackOptions) *gin.Engine {
	t.Helper()

	// 1. Hermetic infra: disable Redis, fresh SQLite with all models.
	testutil.DisableRedis(t)
	gormDB := testutil.NewMockDBForCommon(t)
	model.DB = gormDB
	model.LOG_DB = gormDB

	// Run postConsumeQuota synchronously for the lifetime of this test.
	// RelayTextHelper otherwise launches it as a detached goroutine
	// (text.go:96); that goroutine reads global state
	// (common.RedisEnabled, batchUpdateStores, model.DB) which the next
	// test's setup mutates, and the race detector flags it. Flipping
	// this switch settles quota inline before the handler returns, so
	// no goroutine can outlive the test. Restored on cleanup.
	prevSync := relaycontroller.PostConsumeQuotaSynchronous
	relaycontroller.PostConsumeQuotaSynchronous = true
	t.Cleanup(func() { relaycontroller.PostConsumeQuotaSynchronous = prevSync })

	// 2. Seed a User. The quota is deliberately huge so pre-consume
	// never trips the balance check.
	emptySubnet := ""
	user := &model.User{
		Id:       1,
		Username: "mock-user",
		Password: "not-used-for-token-auth",
		Role:     model.RoleCommonUser,
		Status:   model.UserStatusEnabled,
		Group:    "default",
		Quota:    1_000_000_000,
		AffCode:  "mockaff",
	}
	if err := model.DB.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// 3. Seed a Token. Key is the BARE key ("test"); clients send
	// "Bearer sk-test". UnlimitedQuota avoids the post-consume drain
	// racing the RemainQuota assertion in error-path tests.
	token := &model.Token{
		Key:            seedTokenKey,
		UserId:         1,
		Status:         model.TokenStatusEnabled,
		RemainQuota:    1_000_000_000,
		UnlimitedQuota: true,
		ExpiredTime:    -1,
		Name:           "mock-token",
		Subnet:         &emptySubnet,
	}
	if err := model.DB.Create(token).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// 4. Seed a Mock channel. channel.Insert also writes Ability rows,
	// which InitChannelCache reads to discover the (group, model)
	// adjacency. A bare DB.Create without abilities would leave the
	// channel invisible to the router.
	baseURL := mockChannelBaseURL
	channelCfg := ""
	if opts.supportResponses {
		// Opt the channel into native Responses passthrough so
		// relayResponsesCreate forwards the body untouched. A channel
		// without this flag refuses /v1/responses with 503 (protocol
		// conversion was removed).
		channelCfg = `{"support_responses":true}`
	}
	if opts.responsesOnly {
		// responses_only upstreams have no chat endpoint: chat requests
		// are refused with 503 so the relay fails over to a chat channel.
		channelCfg = `{"responses_only":true}`
	}
	if opts.defaultModel != "" {
		channelCfg = fmt.Sprintf(`{"support_responses":true,"default_model":%q}`, opts.defaultModel)
	}
	channel := &model.Channel{
		Id:      1,
		Type:    channeltype.Mock,
		Name:    "mock-channel",
		Status:  model.ChannelStatusEnabled,
		Group:   "default",
		Models:  mockModelName,
		BaseURL: &baseURL,
		Key:     "not-used-by-mock",
		Config:  channelCfg,
	}
	if err := channel.Insert(); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if opts.extraResponsesChannel {
		// A second, responses-capable channel serving the same model so
		// failover tests can start on the chat-only channel and land here.
		extra := &model.Channel{
			Id:      2,
			Type:    channeltype.Mock,
			Name:    "mock-channel-responses",
			Status:  model.ChannelStatusEnabled,
			Group:   "default",
			Models:  mockModelName,
			BaseURL: &baseURL,
			Key:     "not-used-by-mock",
			Config:  `{"support_responses":true}`,
		}
		if err := extra.Insert(); err != nil {
			t.Fatalf("seed responses channel: %v", err)
		}
	}

	// 5. Populate the in-memory channel cache so Distribute can route.
	// Without MemoryCacheEnabled=true the cache lookups short-circuit
	// to nil and every request would 503 with "no available channel".
	prevMemCache := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	t.Cleanup(func() { config.MemoryCacheEnabled = prevMemCache })
	model.InitChannelCache()

	// Use the approximate token counter so we don't depend on the
	// tiktoken encoder (which needs to download BPE files on first
	// use — a network dependency CI forbids). With ApproximateToken
	// Enabled, getTokenNum returns len(text)*0.38 without touching the
	// global defaultTokenEncoder, which is nil until InitTokenEncoders
	// runs (a main.go startup step).
	prevApprox := config.ApproximateTokenEnabled
	config.ApproximateTokenEnabled = true
	t.Cleanup(func() { config.ApproximateTokenEnabled = prevApprox })

	// 6. Build the gin engine mirroring router/relay.go's relayV1Router
	//    shape: RelayPanicRecover + TokenAuth + Distribute -> controller.Relay.
	r := gin.New()
	r.Use(middleware.RelayPanicRecover())
	relayGroup := r.Group("/v1")
	relayGroup.Use(middleware.TokenAuth(), middleware.Distribute())
	{
		relayGroup.POST("/chat/completions", Relay)
		if opts.registerResponsesRoute {
			// The Responses API: POST creates, GET/DELETE/etc are CRUD.
			// Tests drive the create path; the others route to the same
			// handler and dispatch internally on method.
			relayGroup.POST("/responses", Relay)
		}
	}
	return r
}

// doRelayRequest fires a POST /v1/chat/completions at the engine with
// the given Authorization header value (use "" to omit it), the
// X-Mock-Behavior header, and a JSON body. Returns the recorder.
func doRelayRequest(t *testing.T, r *gin.Engine, authHeader, behavior, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doRelayRequestTo(t, r, "/v1/chat/completions", authHeader, behavior, body)
}

// doRelayRequestTo is the path-parameterized core of doRelayRequest.
// Used by Responses-endpoint tests which POST to /v1/responses.
func doRelayRequestTo(t *testing.T, r *gin.Engine, path, authHeader, behavior, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if behavior != "" {
		req.Header.Set("X-Mock-Behavior", behavior)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// basicChatBody returns a minimal non-streaming chat request body for
// the mock model. Override fields with the optional mods maps merged in
// order (later wins).
func basicChatBody(mods ...map[string]any) string {
	m := map[string]any{
		"model":    mockModelName,
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	for _, mod := range mods {
		for k, v := range mod {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// reassembleChatStreamContent extracts every choice.delta.content from
// the SSE chat-completion chunks in body and concatenates them. This
// mirrors what the OpenAI StreamHandler does in production and lets
// tests assert on the reconstructed text even though the mock now splits
// content across multiple delta chunks (token-by-token streaming).
func reassembleChatStreamContent(body string) string {
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

// ---------------------------------------------------------------------------
// Happy paths
// ---------------------------------------------------------------------------

func TestRelayMock_NonStreamChat(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat", basicChatBody())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected at least one choice, got %v", resp)
	}
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	if content == "" {
		t.Errorf("expected non-empty content, got %v", msg)
	}
	usage, _ := resp["usage"].(map[string]any)
	if total, _ := usage["total_tokens"].(float64); total == 0 {
		t.Errorf("expected total_tokens > 0, got %v", usage)
	}
}

func TestRelayMock_StreamChat(t *testing.T) {
	r := setupMockRelayStack(t)
	body := basicChatBody(map[string]any{"stream": true})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-stream", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "data: ") {
		t.Errorf("stream body missing 'data: ' prefix:\n%s", out)
	}
	if !strings.Contains(out, "[DONE]") {
		t.Errorf("stream body missing [DONE] terminator:\n%s", out)
	}
	// Content is streamed across multiple delta chunks (token-by-token).
	// The first chunk carries the first word; verify it + that the
	// reassembled content reconstructs the canned reply.
	if !strings.Contains(out, `"content":"Hello"`) {
		t.Errorf("stream body missing first delta chunk 'Hello':\n%s", out)
	}
	if got := reassembleChatStreamContent(out); !strings.Contains(got, "Hello from the mock channel.") {
		t.Errorf("reassembled stream content does not contain canned reply, got: %q", got)
	}
}

func TestRelayMock_ToolCall(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-tool-call", basicChatBody())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("expected at least one choice")
	}
	first, _ := choices[0].(map[string]any)
	if fr, _ := first["finish_reason"].(string); fr != "tool_calls" {
		t.Errorf("finish_reason = %q, want tool_calls", fr)
	}
	msg, _ := first["message"].(map[string]any)
	toolCalls, ok := msg["tool_calls"].([]any)
	if !ok || len(toolCalls) == 0 {
		t.Errorf("expected non-empty tool_calls, got %v", msg)
	}
}

// ---------------------------------------------------------------------------
// Error paths — verify the relay pipeline surfaces upstream errors and
// refunds pre-consumed quota.
// ---------------------------------------------------------------------------

func TestRelayMock_Error429(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-test", "error-429", basicChatBody())

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	// The error envelope should carry the upstream message/type so
	// RelayErrorHandler's parse path is exercised end-to-end.
	body := rec.Body.String()
	if !strings.Contains(body, "rate_limit_exceeded") {
		t.Errorf("error body missing upstream type: %s", body)
	}
}

func TestRelayMock_Error500(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-test", "error-500", basicChatBody())

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "server_error") {
		t.Errorf("error body missing upstream type: %s", rec.Body.String())
	}
}

func TestRelayMock_Error400(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-test", "error-400", basicChatBody())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request_error") {
		t.Errorf("error body missing upstream type: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Middleware coverage — auth + routing failures.
// ---------------------------------------------------------------------------

func TestRelayMock_AuthFailure_NoToken(t *testing.T) {
	r := setupMockRelayStack(t)
	// No Authorization header → TokenAuth must abort before Relay.
	rec := doRelayRequest(t, r, "", "openai-chat", basicChatBody())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelayMock_AuthFailure_BadToken(t *testing.T) {
	r := setupMockRelayStack(t)
	rec := doRelayRequest(t, r, "Bearer sk-does-not-exist", "openai-chat", basicChatBody())

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

func TestRelayMock_NoChannelForModel(t *testing.T) {
	r := setupMockRelayStack(t)
	// Ask for a model the mock channel doesn't serve → Distribute
	// returns 503 "no available channel".
	body := basicChatBody(map[string]any{"model": "this-model-does-not-exist"})
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat", body)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "无可用渠道") {
		t.Errorf("expected 'no available channel' message, got: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Quota accounting — verify a successful request consumes quota via the
// async post-consume goroutine. This guards the full
// pre-consume → relay → post-consume path.
// ---------------------------------------------------------------------------

func TestRelayMock_QuotaAccounting(t *testing.T) {
	r := setupMockRelayStack(t)

	// With PostConsumeQuotaSynchronous=true (set in setup), the handler
	// settles quota inline before returning. postConsumeQuota stages
	// the used-quota increment into batchUpdateStores via addNewRecord;
	// model.BatchUpdate() flushes it to the DB. Quota is accounted
	// against the USER (and channel), not the token, so we assert on
	// User.UsedQuota.
	rec := doRelayRequest(t, r, "Bearer sk-test", "openai-chat", basicChatBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	model.BatchUpdate() // flush staged increments to the DB

	var after model.User
	if err := model.DB.First(&after, 1).Error; err != nil {
		t.Fatalf("load user after: %v", err)
	}
	if after.UsedQuota <= 0 {
		t.Errorf("User.UsedQuota = %d, want > 0 (quota was not consumed)", after.UsedQuota)
	}
}

// ---------------------------------------------------------------------------
// initBatchUpdater starts the background batch updater goroutine in
// production (main.go). In tests we don't want that goroutine racing
// the batchUpdate() call below, so we never start it and instead flush
// synchronously. This is why TestRelayMock_QuotaAccounting calls
// model.BatchUpdate() directly.
// ---------------------------------------------------------------------------
