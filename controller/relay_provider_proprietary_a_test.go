package controller

// Tier-3 provider tests, part A: providers with fully proprietary
// wire formats — anthropic, gemini (native), zhipu, vertexai.
//
// These exercise the REAL adaptor's ConvertRequest (OpenAI →
// proprietary body), GetRequestURL, SetupRequestHeader auth, AND
// DoResponse (proprietary response → OpenAI shape back to the
// client). The canned upstream bodies below are fixtures of the
// provider's actual response format; the canned-request assertions
// pin the conversion output shape.

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/vertexai"
	"github.com/songquanpeng/one-api/relay/channeltype"
)

// ===========================================================================
// Anthropic — /v1/messages, x-api-key auth, version headers, system
// hoisting, content blocks, max_tokens defaulting.
// ===========================================================================

// anthropicChatResponse is a minimal valid Anthropic Messages API
// response body.
func anthropicChatResponse() []byte {
	body := map[string]any{
		"id":            "msg_provider_test",
		"type":          "message",
		"role":          "assistant",
		"model":         "claude-3-5-sonnet-20241022",
		"content":       []map[string]any{{"type": "text", "text": "hi from claude"}},
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 3, "output_tokens": 5},
	}
	b, _ := json.Marshal(body)
	return b
}

func TestProvider_Anthropic_MessagesURLAndHeaders(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Anthropic,
		baseURL:     "https://api.anthropic.com",
		models:      "claude-3-5-sonnet-20241022",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.anthropic.com",
		captureUpstream(t, &captured, http.StatusOK, anthropicChatResponse()))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model":      "claude-3-5-sonnet-20241022",
			"max_tokens": 1024,
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/v1/messages" {
		t.Errorf("Anthropic path = %q, want /v1/messages", got)
	}
	// Anthropic uses x-api-key, NOT Authorization: Bearer.
	if k := captured.Header.Get("x-api-key"); k != "upstream-key" {
		t.Errorf("x-api-key = %q, want 'upstream-key'", k)
	}
	if auth := captured.Header.Get("Authorization"); auth != "" {
		t.Errorf("Anthropic must not send Authorization, got %q", auth)
	}
	// Default version + beta headers.
	if v := captured.Header.Get("anthropic-version"); v != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", v)
	}
	// claude-3-5-sonnet* overrides the beta header (8k max-tokens beta).
	if b := captured.Header.Get("anthropic-beta"); b != "max-tokens-3-5-sonnet-2024-07-15" {
		t.Errorf("anthropic-beta = %q, want max-tokens-3-5-sonnet-2024-07-15 for claude-3-5-sonnet*", b)
	}

	body := captured.decodedBody(t)
	// The system message is hoisted to the top-level system field.
	if sys, _ := body["system"].(string); sys != "You are terse." {
		t.Errorf("system = %v, want hoisted 'You are terse.'", body["system"])
	}
	// Messages become content-block arrays; no system entry inside.
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v, want only the non-system message", body["messages"])
	}
	msg := msgs[0].(map[string]any)
	if role, _ := msg["role"].(string); role != "user" {
		t.Errorf("message role = %q, want user", role)
	}
	content, _ := msg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks = %v, want exactly one", msg["content"])
	}
	block := content[0].(map[string]any)
	if ty, _ := block["type"].(string); ty != "text" {
		t.Errorf("content block type = %q, want text", ty)
	}
	if txt, _ := block["text"].(string); txt != "hi" {
		t.Errorf("content block text = %q, want 'hi'", txt)
	}
	if mt2, _ := body["max_tokens"].(float64); mt2 != 1024 {
		t.Errorf("max_tokens = %v, want 1024 forwarded", body["max_tokens"])
	}

	// The client sees an OpenAI-shaped response.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not JSON: %v\n%s", err, rec.Body.String())
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("client choices = %v", resp["choices"])
	}
	msg0 := choices[0].(map[string]any)["message"].(map[string]any)
	if c, _ := msg0["content"].(string); c != "hi from claude" {
		t.Errorf("client content = %q, want 'hi from claude'", c)
	}
	usage := resp["usage"].(map[string]any)
	if pt, _ := usage["prompt_tokens"].(float64); pt != 3 {
		t.Errorf("prompt_tokens = %v, want 3", usage["prompt_tokens"])
	}
}

func TestProvider_Anthropic_DefaultMaxTokensAndLegacyBeta(t *testing.T) {
	// Non-3.5-sonnet model: default anthropic-beta header, and
	// max_tokens defaults to 4096 when the client sends none (Anthropic
	// REQUIRES max_tokens; omitting it is an upstream 400).
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Anthropic,
		baseURL:     "https://api.anthropic.com",
		models:      "claude-3-haiku-20240307",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.anthropic.com",
		captureUpstream(t, &captured, http.StatusOK, anthropicChatResponse()))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "claude-3-haiku-20240307"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if b := captured.Header.Get("anthropic-beta"); b != "messages-2023-12-15" {
		t.Errorf("anthropic-beta = %q, want default messages-2023-12-15", b)
	}
	body := captured.decodedBody(t)
	if mt2, _ := body["max_tokens"].(float64); mt2 != 4096 {
		t.Errorf("max_tokens = %v, want defaulted 4096", body["max_tokens"])
	}
}

// ===========================================================================
// Gemini (native API) — :generateContent action URLs with per-model
// version selection, x-goog-api-key auth, contents/parts conversion,
// systemInstruction for supporting models.
// ===========================================================================

func geminiChatResponse() []byte {
	body := map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": []map[string]any{{"text": "hi from gemini"}}},
			"finishReason": "STOP",
			"index":        0,
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 3, "candidatesTokenCount": 5, "totalTokenCount": 8},
	}
	b, _ := json.Marshal(body)
	return b
}

func TestProvider_Gemini_GenerateContentURLAndBody(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Gemini,
		baseURL:     "https://generativelanguage.googleapis.com",
		models:      "gemini-2.0-flash,gemini-pro",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://generativelanguage.googleapis.com",
		captureUpstream(t, &captured, http.StatusOK, geminiChatResponse()))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "gemini-2.0-flash",
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// gemini-1.5/2.0 models force v1beta regardless of config.
	if got := captured.URL.Path; got != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Errorf("Gemini path = %q, want /v1beta/models/gemini-2.0-flash:generateContent", got)
	}
	// Gemini auth is the x-goog-api-key header, not Bearer.
	if k := captured.Header.Get("x-goog-api-key"); k != "upstream-key" {
		t.Errorf("x-goog-api-key = %q, want 'upstream-key'", k)
	}
	if auth := captured.Header.Get("Authorization"); auth != "" {
		t.Errorf("Gemini must not send Authorization, got %q", auth)
	}

	body := captured.decodedBody(t)
	// gemini-2.0-flash supports systemInstruction: the system message
	// must move there instead of a contents entry. NOTE: the wire key
	// is snake_case (system_instruction), not camelCase.
	if si, ok := body["system_instruction"].(map[string]any); ok {
		parts, _ := si["parts"].([]any)
		if len(parts) != 1 {
			t.Errorf("system_instruction.parts = %v, want one", si["parts"])
		} else if p0, _ := parts[0].(map[string]any); p0["text"] != "You are terse." {
			t.Errorf("system_instruction text = %v, want 'You are terse.'", p0["text"])
		}
	} else {
		t.Errorf("system_instruction missing for gemini-2.0-flash: %s", captured.Body)
	}
	contents, _ := body["contents"].([]any)
	// Current conversion appends a dummy model "Okay" turn after the
	// message following a hoisted system message (shouldAddDummyModel
	// stays set across the continue). Pin the actual shape; see the
	// dummy-message comment in gemini ConvertRequest.
	if len(contents) != 2 {
		t.Fatalf("contents = %v, want user message + trailing dummy model turn", body["contents"])
	}
	content := contents[0].(map[string]any)
	if role, _ := content["role"].(string); role != "user" {
		t.Errorf("contents[0].role = %q, want user", role)
	}
	dummy := contents[1].(map[string]any)
	if role, _ := dummy["role"].(string); role != "model" {
		t.Errorf("contents[1].role = %q, want dummy model turn", role)
	}
	// Safety settings are stamped for the four classic categories.
	if _, ok := body["safety_settings"]; !ok {
		t.Errorf("safety_settings missing: %s", captured.Body)
	}

	// Client sees OpenAI shape.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not JSON: %v\n%s", err, rec.Body.String())
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("client choices = %v", resp["choices"])
	}
	msg0 := choices[0].(map[string]any)["message"].(map[string]any)
	if c, _ := msg0["content"].(string); c != "hi from gemini" {
		t.Errorf("client content = %q, want 'hi from gemini'", c)
	}
}

func TestProvider_Gemini_VersionDefaultsAndOverride(t *testing.T) {
	// Non-1.5/2.0 models use config.GeminiVersion (default "v1") or the
	// channel's api_version override.
	cases := []struct {
		name       string
		configJSON string
		model      string
		wantPath   string
	}{
		{
			name:     "legacy model defaults to v1",
			model:    "gemini-pro",
			wantPath: "/v1/models/gemini-pro:generateContent",
		},
		{
			name:       "channel api_version overrides the default",
			configJSON: `{"api_version":"v1beta"}`,
			model:      "gemini-pro",
			wantPath:   "/v1beta/models/gemini-pro:generateContent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, mt := setupProviderStack(t, providerStackOptions{
				channelType: channeltype.Gemini,
				baseURL:     "https://generativelanguage.googleapis.com",
				models:      "gemini-pro",
				configJSON:  tc.configJSON,
			})
			var captured *upstreamCapture
			mt.Match(http.MethodPost, "https://generativelanguage.googleapis.com",
				captureUpstream(t, &captured, http.StatusOK, geminiChatResponse()))

			rec := doRelayRequest(t, r, "Bearer sk-test", "",
				basicChatBody(map[string]any{"model": tc.model}))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			if got := captured.URL.Path; got != tc.wantPath {
				t.Errorf("path = %q, want %q", got, tc.wantPath)
			}
		})
	}
}

// ===========================================================================
// Zhipu — glm-* models use the v4 OpenAI-shaped endpoint with a
// locally-generated HS256 JWT; everything else falls back to the v3
// model-api endpoint. v4 requests get TopP/Temperature clamped to
// [0,1].
// ===========================================================================

func TestProvider_Zhipu_V4URLAndJWTTokens(t *testing.T) {
	// Zhipu keys are "<id>.<secret>"; the JWT is generated locally
	// (no network token exchange).
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Zhipu,
		baseURL:     "https://open.bigmodel.cn",
		models:      "glm-4-flash",
		key:         "zhipu-id-42.zhipu-secret-abc",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://open.bigmodel.cn",
		captureUpstream(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("glm-4-flash")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model":       "glm-4-flash",
			"top_p":       1.5,  // out of range — must clamp
			"temperature": -0.5, // out of range — must clamp
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/paas/v4/chat/completions" {
		t.Errorf("Zhipu v4 path = %q, want /api/paas/v4/chat/completions", got)
	}

	// QUIRK: the Authorization header carries the RAW JWT — zhipu's
	// adaptor does NOT prepend "Bearer ". Pin the bare-token shape and
	// the payload claims.
	auth := captured.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		t.Fatalf("Authorization = %q, zhipu sends the bare JWT without Bearer prefix", auth)
	}
	parts := strings.Split(auth, ".")
	if len(parts) != 3 {
		t.Fatalf("Authorization is not a JWS triple: %q", auth)
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode JWT header: %v", err)
	}
	var hdr map[string]any
	if err := json.Unmarshal(headerJSON, &hdr); err != nil {
		t.Fatalf("parse JWT header: %v", err)
	}
	if alg, _ := hdr["alg"].(string); alg != "HS256" {
		t.Errorf("JWT alg = %v, want HS256", hdr["alg"])
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadJSON, &payload); err != nil {
		t.Fatalf("parse JWT payload: %v", err)
	}
	if ak, _ := payload["api_key"].(string); ak != "zhipu-id-42" {
		t.Errorf("JWT api_key = %v, want 'zhipu-id-42'", payload["api_key"])
	}
	if _, ok := payload["exp"]; !ok {
		t.Errorf("JWT payload missing exp: %v", payload)
	}

	// Zhipu clamps sampling params into [0,1] for its own API limits.
	body := captured.decodedBody(t)
	if tp, _ := body["top_p"].(float64); tp != 1 {
		t.Errorf("top_p = %v, want clamped to 1", body["top_p"])
	}
	if temp, _ := body["temperature"].(float64); temp != 0 {
		t.Errorf("temperature = %v, want clamped to 0", body["temperature"])
	}
}

func TestProvider_Zhipu_V3ModelAPIEndpoint(t *testing.T) {
	// Models without the glm- prefix take the legacy v3 per-model
	// endpoint /api/paas/v3/model-api/{model}/invoke with the zhipu
	// proprietary response envelope.
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Zhipu,
		baseURL:     "https://open.bigmodel.cn",
		models:      "chatglm-lite",
		key:         "zhipu-id-42.zhipu-secret-abc",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://open.bigmodel.cn",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"code": 200, "msg": "", "success": true,
			"data": {
				"request_id": "req-1", "task_id": "task-1",
				"choices": [{"role": "assistant", "content": "hi from zhipu", "finish_reason": "stop"}],
				"usage": {"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}
			}
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "chatglm-lite"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/paas/v3/model-api/chatglm-lite/invoke" {
		t.Errorf("Zhipu v3 path = %q, want /api/paas/v3/model-api/chatglm-lite/invoke", got)
	}
	// v3 request body is the proprietary {prompt, temperature, top_p}
	// shape with Incremental=false for non-stream.
	body := captured.decodedBody(t)
	if _, ok := body["prompt"]; !ok {
		t.Errorf("v3 body missing prompt array: %s", captured.Body)
	}
	if inc, _ := body["incremental"].(bool); inc {
		t.Errorf("incremental = %v, want false for non-stream", body["incremental"])
	}

	// The proprietary envelope is converted back to OpenAI shape.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client response not JSON: %v\n%s", err, rec.Body.String())
	}
	choices, _ := resp["choices"].([]any)
	msg0 := choices[0].(map[string]any)["message"].(map[string]any)
	if c, _ := msg0["content"].(string); c != "hi from zhipu" {
		t.Errorf("client content = %q, want 'hi from zhipu'", c)
	}
}

// ===========================================================================
// Vertex AI — project/region URL, Bearer token from the (pre-seeded)
// token cache, per-model sub-protocol (gemini :generateContent vs
// claude :rawPredict with anthropic_version vertex-2023-10-16).
// ===========================================================================

// seedVertexAIToken pre-populates the vertexai token cache for channel
// 1 so SetupRequestHeader skips the real Google IAM round trip.
func seedVertexAIToken(t *testing.T, token string) {
	t.Helper()
	vertexai.Cache.Set("vertexai-token-1", token, 0)
	t.Cleanup(func() { vertexai.Cache.Delete("vertexai-token-1") })
}

func TestProvider_VertexAI_GeminiRawPredictURLAndAuth(t *testing.T) {
	seedVertexAIToken(t, "fake-gcp-access-token")
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.VertextAI,
		baseURL:     "https://vertexai.example.com",
		models:      "gemini-2.0-flash-001",
		configJSON:  `{"region":"us-central1","vertex_ai_project_id":"proj-1"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://vertexai.example.com",
		captureUpstream(t, &captured, http.StatusOK, geminiChatResponse()))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gemini-2.0-flash-001"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	wantPath := "/v1/projects/proj-1/locations/us-central1/publishers/google/models/gemini-2.0-flash-001:generateContent"
	if got := captured.URL.Path; got != wantPath {
		t.Errorf("VertexAI path = %q, want %q", got, wantPath)
	}
	// The channel key is irrelevant here; auth is the cached GCP token.
	if auth := captured.Header.Get("Authorization"); auth != "Bearer fake-gcp-access-token" {
		t.Errorf("Authorization = %q, want 'Bearer fake-gcp-access-token'", auth)
	}
	// Body is gemini contents format.
	body := captured.decodedBody(t)
	if _, ok := body["contents"]; !ok {
		t.Errorf("VertexAI gemini body missing contents: %s", captured.Body)
	}
}

func TestProvider_VertexAI_ClaudeRawPredictAndVersionField(t *testing.T) {
	seedVertexAIToken(t, "fake-gcp-access-token")
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.VertextAI,
		baseURL:     "https://vertexai.example.com",
		models:      "claude-3-5-sonnet-v2@20241022",
		configJSON:  `{"region":"europe-west1","vertex_ai_project_id":"proj-2"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://vertexai.example.com",
		captureUpstream(t, &captured, http.StatusOK, anthropicChatResponse()))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "claude-3-5-sonnet-v2@20241022",
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	wantPath := "/v1/projects/proj-2/locations/europe-west1/publishers/google/models/claude-3-5-sonnet-v2@20241022:rawPredict"
	if got := captured.URL.Path; got != wantPath {
		t.Errorf("VertexAI claude path = %q, want %q", got, wantPath)
	}
	body := captured.decodedBody(t)
	// Vertex claude requests carry the fixed anthropic_version and omit
	// the model field (the model is already in the URL).
	if v, _ := body["anthropic_version"].(string); v != "vertex-2023-10-16" {
		t.Errorf("anthropic_version = %v, want vertex-2023-10-16", body["anthropic_version"])
	}
	if m, has := body["model"]; has && m != "" {
		t.Errorf("model field must be omitted on vertex claude, got %v", m)
	}
	if sys, _ := body["system"].(string); sys != "You are terse." {
		t.Errorf("system = %v, want hoisted 'You are terse.'", body["system"])
	}
}
