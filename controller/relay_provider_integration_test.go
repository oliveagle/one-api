package controller

// Provider-specific integration tests using REAL adaptors.
//
// Unlike the mock-channel tests (which synthesize responses in-process),
// these tests seed a channel with a REAL channeltype (e.g. Azure, plain
// OpenAI) so the production adaptor factory returns the real adaptor
// (openai.Adaptor, anthropic.Adaptor, ...). The outbound HTTP call is
// intercepted by testutil.MockTransport, which lets us:
//
//   1. Return a canned upstream response so no network is needed.
//   2. ASSERT on the request the adaptor actually built — the URL path,
//      query string, and auth header — which is exactly where per-provider
//      adaptation differences live (Azure's deployment URL + api-key
//      header vs OpenAI's /v1/chat/completions + Bearer token, etc.).
//
// This is the layer that catches "the Azure URL builder drifted" or
// "Minimax auth header changed" — bugs the mock channel can't surface
// because it bypasses the real adaptor entirely.
//
// ADDING A PROVIDER TEST:
//   1. Pick the channeltype constant (relay/channeltype/define.go).
//   2. Call setupProviderStack with that channeltype and the base URL
//      the provider expects.
//   3. Register a MockTransport handler on the URL prefix the adaptor
//      will build. Inside the handler, assert on the request shape
//      (r.URL.Path, r.Header.Get("Authorization"), etc.).
//   4. POST a chat request and assert the response comes back parsed
//      correctly (the real adaptor's DoResponse handles the upstream
//      body).

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/testutil"
	"github.com/songquanpeng/one-api/middleware"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/channeltype"
	relaycontroller "github.com/songquanpeng/one-api/relay/controller"
)

// providerStackOptions configures setupProviderStack for a specific
// upstream provider.
type providerStackOptions struct {
	// channelType is the REAL channel type (channeltype.Azure,
	// channeltype.OpenAI, etc.). This determines which adaptor the
	// factory dispatches to.
	channelType int
	// baseURL is the upstream base URL the channel is configured with.
	// The test's MockTransport handler must match the full URL the
	// adaptor builds from this base. An empty string makes the relay
	// fall back to the channel type's default base URL
	// (channeltype.ChannelBaseURLs).
	baseURL string
	// models is the CSV of models the channel serves.
	models string
	// configJSON is the channel's Config field (for providers that need
	// extra config, e.g. Azure's api_version).
	configJSON string
	// key overrides the channel's upstream key (default "upstream-key").
	// Providers with structured keys need this: tencent expects
	// "appId|secretId|secretKey", baidu "apiKey|secretKey", zhipu
	// "id.secret".
	key string
	// headersJSON is the channel's Headers field — extra headers the
	// relay stamps on every upstream request (see
	// SetupCommonRequestHeader).
	headersJSON string
}

// setupProviderStack is the REAL-adaptor counterpart to
// setupMockRelayStack. It seeds a channel with a real channeltype so
// the production adaptor factory returns the real adaptor, then
// intercepts the outbound HTTP via testutil.MockTransport.
//
// The returned MockTransport is pre-installed as client.HTTPClient; the
// test registers handlers on it to respond to (and assert on) the
// adaptor's outbound requests.
func setupProviderStack(t *testing.T, opts providerStackOptions) (*gin.Engine, *testutil.MockTransport) {
	t.Helper()

	testutil.DisableRedis(t)
	gormDB := testutil.NewMockDBForCommon(t)
	model.DB = gormDB
	model.LOG_DB = gormDB

	// Synchronous quota settlement (same rationale as the mock stack).
	prevSync := relaycontroller.PostConsumeQuotaSynchronous
	relaycontroller.PostConsumeQuotaSynchronous = true
	t.Cleanup(func() { relaycontroller.PostConsumeQuotaSynchronous = prevSync })

	emptySubnet := ""
	if err := model.DB.Create(&model.User{
		Id: 1, Username: "provider-user",
		Password: "x", Role: model.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default",
		Quota: 1_000_000_000, AffCode: "paff",
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := model.DB.Create(&model.Token{
		Key: seedTokenKey, UserId: 1, Status: model.TokenStatusEnabled,
		RemainQuota: 1_000_000_000, UnlimitedQuota: true,
		ExpiredTime: -1, Name: "provider-token", Subnet: &emptySubnet,
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	baseURL := opts.baseURL
	channelKey := opts.key
	if channelKey == "" {
		channelKey = "upstream-key"
	}
	ch := &model.Channel{
		Id: 1, Type: opts.channelType, Name: "provider-channel",
		Status: model.ChannelStatusEnabled, Group: "default",
		Models: opts.models, BaseURL: &baseURL,
		Key: channelKey, Config: opts.configJSON, Headers: &opts.headersJSON,
	}
	if err := ch.Insert(); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	prevMem := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	t.Cleanup(func() { config.MemoryCacheEnabled = prevMem })
	model.InitChannelCache()

	prevApprox := config.ApproximateTokenEnabled
	config.ApproximateTokenEnabled = true
	t.Cleanup(func() { config.ApproximateTokenEnabled = prevApprox })

	// Intercept outbound HTTP. The real adaptor will build a request
	// and hand it to client.HTTPClient.Do(); we swap in a MockTransport
	// so the test sees the request and returns a canned response.
	mt := testutil.NewMockTransport(t)
	prevClient := client.HTTPClient
	client.HTTPClient = &http.Client{Transport: mt}
	t.Cleanup(func() { client.HTTPClient = prevClient })

	r := gin.New()
	r.Use(middleware.RelayPanicRecover())
	grp := r.Group("/v1")
	grp.Use(middleware.TokenAuth(), middleware.Distribute())
	grp.POST("/chat/completions", Relay)
	// The Responses API endpoint: needed by tests that drive the
	// responses passthrough path (e.g. the OpenAIResponses channel type).
	grp.POST("/responses", Relay)
	return r, mt
}

// standardOpenAIChatResponse is a minimal but valid OpenAI Chat
// Completions non-stream response body that the openai.Handler can
// parse. Provider tests return this from the MockTransport handler.
func standardOpenAIChatResponse(modelName string) []byte {
	body := map[string]any{
		"id":      "chatcmpl-provider-test",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   modelName,
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": "OK from upstream"},
		}},
		"usage": map[string]any{
			"prompt_tokens": 5, "completion_tokens": 4, "total_tokens": 9,
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// capturingHandler returns an http.Handler that records the request it
// receives (so the test can assert on URL/headers) and responds with
// the given canned body.
func capturingHandler(t *testing.T, captured **http.Request, status int, body []byte) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = r.Clone(r.Context())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

// upstreamCapture records everything about an outbound upstream request
// the adaptor built: method, URL, headers, and the fully-read body.
type upstreamCapture struct {
	Method string
	URL    *url.URL
	Header http.Header
	Body   []byte
}

// captureUpstream returns an http.Handler that snapshots the upstream
// request (reading the body — do this at handler time, it is consumed
// afterwards) and responds with the canned body.
func captureUpstream(t *testing.T, cap **upstreamCapture, status int, respBody []byte) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream request body: %v", err)
		}
		*cap = &upstreamCapture{
			Method: r.Method,
			URL:    r.URL,
			Header: r.Header.Clone(),
			Body:   body,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	})
}

// decodedBody unmarshals the captured upstream request body as JSON.
// Fatal if the body is not valid JSON.
func (u *upstreamCapture) decodedBody(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(u.Body, &m); err != nil {
		t.Fatalf("upstream request body is not JSON: %v\n%s", err, u.Body)
	}
	return m
}

// ===========================================================================
// Baseline: plain OpenAI. Establishes that the harness wires correctly
// when the adaptor does nothing fancy — Bearer auth, standard URL.
// ===========================================================================

func TestProvider_OpenAI_StandardURLAndAuth(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.OpenAI,
		baseURL:     "https://api.openai.com",
		models:      "gpt-4o-mini",
	})

	var captured *http.Request
	mt.Match(http.MethodPost, "https://api.openai.com",
		capturingHandler(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("gpt-4o-mini")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gpt-4o-mini"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("MockTransport did not capture the upstream request")
	}
	// The OpenAI adaptor builds {base}/v1/chat/completions.
	if got := captured.URL.Path; got != "/v1/chat/completions" {
		t.Errorf("upstream URL path = %q, want /v1/chat/completions", got)
	}
	// Standard Bearer auth with the channel's upstream key.
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("upstream Authorization = %q, want 'Bearer upstream-key'", auth)
	}
	// Response should be parsed by openai.Handler and forwarded.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, rec.Body.String())
	}
	if obj, _ := resp["object"].(string); obj != "chat.completion" {
		t.Errorf("object = %q, want chat.completion", obj)
	}
}

// ===========================================================================
// Azure: exercises the Tier-2 divergence. The openai.Adaptor branches
// on channeltype.Azure to build a deployment-based URL and use the
// api-key header instead of Bearer auth. This test catches regressions
// in that branch — something the mock channel cannot do.
// ===========================================================================

func TestProvider_Azure_DeploymentURLAndAPIKeyHeader(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Azure,
		baseURL:     "https://my-resource.openai.azure.com",
		models:      "gpt-4o",
		// Azure needs api_version in the channel config; the adaptor
		// appends it as a query parameter.
		configJSON: `{"api_version":"2024-06-01"}`,
	})

	var captured *http.Request
	mt.Match(http.MethodPost, "https://my-resource.openai.azure.com",
		capturingHandler(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("gpt4o")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gpt-4o"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("MockTransport did not capture the upstream request")
	}

	// Azure URL shape:
	//   /openai/deployments/{model_no_dots}/chat/completions?api-version=...
	// The adaptor strips dots from the model name for the deployment
	// path (e.g. "gpt-3.5-turbo" → "gpt-35-turbo"), see
	// openai/adaptor.go:48. "gpt-4o" has no dots so it passes through
	// unchanged.
	wantPath := "/openai/deployments/gpt-4o/chat/completions"
	if got := captured.URL.Path; got != wantPath {
		t.Errorf("Azure URL path = %q, want %q", got, wantPath)
	}
	// api-version must be the query param from the channel config.
	if v := captured.URL.Query().Get("api-version"); v != "2024-06-01" {
		t.Errorf("api-version query = %q, want 2024-06-01", v)
	}
	// Azure uses the api-key header, NOT Authorization: Bearer.
	if auth := captured.Header.Get("Authorization"); auth != "" {
		t.Errorf("Azure should NOT send Authorization header, got %q", auth)
	}
	if k := captured.Header.Get("api-key"); k != "upstream-key" {
		t.Errorf("api-key header = %q, want 'upstream-key'", k)
	}
}

// TestProvider_Azure_StripsDotsFromModelName verifies the dot-stripping
// behavior in the Azure URL builder (openai/adaptor.go:48): a model
// named "gpt-3.5-turbo" must map to deployment name "gpt-35-turbo".
// This is a real Azure quirk that the mock channel cannot test.
func TestProvider_Azure_StripsDotsFromModelName(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Azure,
		baseURL:     "https://my-resource.openai.azure.com",
		models:      "gpt-3.5-turbo",
		configJSON:  `{"api_version":"2024-06-01"}`,
	})

	var captured *http.Request
	mt.Match(http.MethodPost, "https://my-resource.openai.azure.com",
		capturingHandler(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("gpt-35-turbo")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gpt-3.5-turbo"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("MockTransport did not capture the upstream request")
	}
	// The dot in "gpt-3.5-turbo" must be stripped → "gpt-35-turbo" in
	// the deployment path. This is the Azure deployment naming rule.
	if got := captured.URL.Path; got != "/openai/deployments/gpt-35-turbo/chat/completions" {
		t.Errorf("Azure deployment path = %q; the dot in gpt-3.5-turbo should have been stripped to gpt-35-turbo", got)
	}
}

// ===========================================================================
// OpenRouter: Tier-2 provider that adds HTTP-Referer + X-Title headers
// (openai/adaptor.go:77-79). A regression here would break OpenRouter's
// analytics/attribution; the mock channel can't catch it.
// ===========================================================================

func TestProvider_OpenRouter_AddsAttributionHeaders(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.OpenRouter,
		baseURL:     "https://openrouter.ai/api",
		models:      "openai/gpt-4o-mini",
	})

	var captured *http.Request
	mt.Match(http.MethodPost, "https://openrouter.ai/api",
		capturingHandler(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("openai/gpt-4o-mini")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "openai/gpt-4o-mini"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured == nil {
		t.Fatal("MockTransport did not capture the upstream request")
	}
	// OpenRouter attribution headers — these are the whole point of
	// the channeltype.OpenRouter branch in SetupRequestHeader.
	if ref := captured.Header.Get("HTTP-Referer"); ref != "https://github.com/songquanpeng/one-api" {
		t.Errorf("HTTP-Referer = %q, want the one-api github URL", ref)
	}
	if title := captured.Header.Get("X-Title"); title != "One API" {
		t.Errorf("X-Title = %q, want 'One API'", title)
	}
	// Still uses Bearer auth (OpenRouter is OpenAI-compatible).
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want 'Bearer upstream-key'", auth)
	}
}
