package controller

// Tier-3 provider tests, part B: ali, baidu, tencent, cohere, coze,
// deepl, ollama, palm, aiproxy, cloudflare.
//
// Same model as part A: real adaptor, canned proprietary upstream
// fixtures, assertions on the converted request (URL, auth, body
// shape) and on the OpenAI-shaped body handed back to the client.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/relay/channeltype"
)

// ===========================================================================
// Ali (DashScope native) — generation endpoint, -internet model
// suffix → enable_search, lowercased roles, TopP clamp at 0.9999.
// ===========================================================================

func TestProvider_Ali_GenerationEndpointAndSearchSuffix(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Ali,
		baseURL:     "https://dashscope.aliyuncs.com",
		models:      "qwen-turbo-internet",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://dashscope.aliyuncs.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"output": {"choices": [{"message": {"role": "assistant", "content": "hi from qwen"}, "finish_reason": "stop"}]},
			"usage": {"input_tokens": 3, "output_tokens": 5},
			"request_id": "req-1"
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "qwen-turbo-internet",
			// Ali rejects TopP > 0.9999 — the clamp is load-bearing.
			"top_p": 1.0,
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "User", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/v1/services/aigc/text-generation/generation" {
		t.Errorf("Ali path = %q, want /api/v1/services/aigc/text-generation/generation", got)
	}
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want Bearer", auth)
	}

	body := captured.decodedBody(t)
	// The -internet suffix is stripped from the model name and flipped
	// into parameters.enable_search.
	if m, _ := body["model"].(string); m != "qwen-turbo" {
		t.Errorf("model = %q, want 'qwen-turbo' (suffix -internet stripped)", m)
	}
	params, _ := body["parameters"].(map[string]any)
	if es, _ := params["enable_search"].(bool); !es {
		t.Errorf("parameters.enable_search = %v, want true for -internet models", params["enable_search"])
	}
	if tp, _ := params["top_p"].(float64); tp != 0.9999 {
		t.Errorf("parameters.top_p = %v, want clamped 0.9999", params["top_p"])
	}
	if rf, _ := params["result_format"].(string); rf != "message" {
		t.Errorf("parameters.result_format = %v, want message", params["result_format"])
	}
	input, _ := body["input"].(map[string]any)
	msgs, _ := input["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("input.messages = %v, want system+user kept", input["messages"])
	}
	// Roles are lowercased on the wire.
	if r0, _ := msgs[0].(map[string]any)["role"].(string); r0 != "system" {
		t.Errorf("first message role = %q, want lowercased 'system'", r0)
	}
	if r1, _ := msgs[1].(map[string]any)["role"].(string); r1 != "user" {
		t.Errorf("second message role = %q, want lowercased 'user' (was 'User')", r1)
	}
}

// ===========================================================================
// Baidu (ERNIE v1) — access-token OAuth exchange + wenxinworkshop
// endpoint mapping + system-message hoisting.
// ===========================================================================

func TestProvider_Baidu_TokenExchangeAndEndpointMapping(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Baidu,
		baseURL:     "https://aip.baidubce.com",
		models:      "ERNIE-4.0-8K",
		// Baidu keys are "apiKey|secretKey" for the OAuth exchange.
		key: "baidu-api-key|baidu-secret-key",
	})

	// The OAuth token exchange goes through ImpatientHTTPClient (NOT
	// the shared HTTPClient) — swap that too so the test stays
	// hermetic.
	prevImpatient := client.ImpatientHTTPClient
	client.ImpatientHTTPClient = &http.Client{Transport: mt}
	t.Cleanup(func() { client.ImpatientHTTPClient = prevImpatient })

	// 1. Token exchange: POST /oauth/2.0/token with client credentials
	//    embedded in the query string.
	mt.Match(http.MethodPost, "https://aip.baidubce.com/oauth/2.0/token",
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("grant_type") != "client_credentials" {
				t.Errorf("oauth grant_type = %q, want client_credentials", q.Get("grant_type"))
			}
			if q.Get("client_id") != "baidu-api-key" || q.Get("client_secret") != "baidu-secret-key" {
				t.Errorf("oauth client credentials wrong: %v", q)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"mock-access-token","expires_in":3600}`))
		}))

	// 2. Chat call: ERNIE-4.0-8K maps to the completions_pro endpoint
	//    with the access token as a query parameter.
	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://aip.baidubce.com/rpc/2.0/ai_custom/v1/wenxinworkshop",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"id": "as-r1", "object": "chat.completion", "created": 1700000000,
			"result": "hi from ernie",
			"usage": {"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model":             "ERNIE-4.0-8K",
			"frequency_penalty": 0.5,
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/rpc/2.0/ai_custom/v1/wenxinworkshop/chat/completions_pro" {
		t.Errorf("Baidu path = %q, want .../chat/completions_pro (ERNIE-4.0-8K mapping)", got)
	}
	if tok := captured.URL.Query().Get("access_token"); tok != "mock-access-token" {
		t.Errorf("access_token query = %q, want the exchanged 'mock-access-token'", tok)
	}

	body := captured.decodedBody(t)
	// The system message is hoisted out of messages into the top-level
	// system field.
	if sys, _ := body["system"].(string); sys != "You are terse." {
		t.Errorf("system = %v, want hoisted 'You are terse.'", body["system"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v, want system removed", body["messages"])
	}
	// frequency_penalty is renamed penalty_score for ERNIE.
	if ps, _ := body["penalty_score"].(float64); ps != 0.5 {
		t.Errorf("penalty_score = %v, want 0.5 (from frequency_penalty)", body["penalty_score"])
	}

	if !strings.Contains(rec.Body.String(), "hi from ernie") {
		t.Errorf("client response missing converted content: %s", rec.Body.String())
	}
}

// ===========================================================================
// Tencent Hunyuan — TC3 signed Authorization header, X-TC-* headers,
// PascalCase body.
// ===========================================================================

func TestProvider_Tencent_TC3HeadersAndPascalCaseBody(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Tencent,
		baseURL:     "https://hunyuan.example.com",
		models:      "hunyuan-lite",
		// Tencent keys are "appId|secretId|secretKey".
		key: "12345|secret-id-x|secret-key-y",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://hunyuan.example.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"Response": {
				"Id": "1", "RequestId": "r-1",
				"Choices": [{"Message": {"Role": "assistant", "Content": "hi from hunyuan"}, "FinishReason": "stop"}],
				"Usage": {"PromptTokens": 3, "CompletionTokens": 5, "TotalTokens": 8}
			}
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "hunyuan-lite"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The Tencent endpoint is the bare root; the action rides in
	// headers instead of the path.
	if got := captured.URL.Path; got != "/" {
		t.Errorf("Tencent path = %q, want '/'", got)
	}
	if a := captured.Header.Get("X-TC-Action"); a != "ChatCompletions" {
		t.Errorf("X-TC-Action = %q, want ChatCompletions", a)
	}
	if v := captured.Header.Get("X-TC-Version"); v != "2023-09-01" {
		t.Errorf("X-TC-Version = %q, want 2023-09-01", v)
	}
	if ts := captured.Header.Get("X-TC-Timestamp"); ts == "" {
		t.Error("X-TC-Timestamp is empty, want unix seconds")
	}
	// Authorization is the computed TC3-HMAC-SHA256 signature (not a
	// bearer key); the secret id must appear in the credential scope.
	auth := captured.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "TC3-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want TC3-HMAC-SHA256 prefix", auth)
	}
	if !strings.Contains(auth, "secret-id-x") {
		t.Errorf("Authorization credential missing secret id: %q", auth)
	}

	// The body uses Tencent's PascalCase schema.
	body := captured.decodedBody(t)
	if m, _ := body["Model"].(string); m != "hunyuan-lite" {
		t.Errorf("Model = %v, want hunyuan-lite (PascalCase field)", body["Model"])
	}
	msgs, _ := body["Messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("Messages = %v, want one", body["Messages"])
	}
	msg := msgs[0].(map[string]any)
	if msg["Role"] != "user" || msg["Content"] != "hi" {
		t.Errorf("Messages[0] = %v, want {Role:user, Content:hi}", msg)
	}

	if !strings.Contains(rec.Body.String(), "hi from hunyuan") {
		t.Errorf("client response missing converted content: %s", rec.Body.String())
	}
}

// ===========================================================================
// Cohere — /v1/chat, role remapping, last-user-message extraction,
// -internet model suffix → web-search connector.
// ===========================================================================

func TestProvider_Cohere_ChatRolesAndInternetConnector(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Cohere,
		baseURL:     "https://api.cohere.ai",
		models:      "command-r-plus-internet",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.cohere.ai",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"response_id": "resp-1",
			"text": "hi from cohere",
			"finish_reason": "COMPLETE",
			"meta": {"tokens": {"input_tokens": 3, "output_tokens": 5}}
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "command-r-plus-internet",
			"messages": []map[string]any{
				{"role": "system", "content": "You are terse."},
				{"role": "assistant", "content": "hello"},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/v1/chat" {
		t.Errorf("Cohere path = %q, want /v1/chat", got)
	}
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want Bearer", auth)
	}

	body := captured.decodedBody(t)
	// Only the LAST user message becomes `message`; everything else
	// lands in chat_history with Cohere role names.
	if m, _ := body["message"].(string); m != "hi" {
		t.Errorf("message = %v, want 'hi' (last user message)", body["message"])
	}
	hist, _ := body["chat_history"].([]any)
	if len(hist) != 2 {
		t.Fatalf("chat_history = %v, want system+assistant entries", body["chat_history"])
	}
	if role, _ := hist[0].(map[string]any)["role"].(string); role != "SYSTEM" {
		t.Errorf("chat_history[0].role = %q, want SYSTEM", role)
	}
	if role, _ := hist[1].(map[string]any)["role"].(string); role != "CHATBOT" {
		t.Errorf("chat_history[1].role = %q, want CHATBOT (assistant)", role)
	}
	// The -internet suffix is stripped and a web-search connector is
	// attached instead.
	if m, _ := body["model"].(string); m != "command-r-plus" {
		t.Errorf("model = %q, want 'command-r-plus' (suffix stripped)", m)
	}
	conns, _ := body["connectors"].([]any)
	if len(conns) != 1 {
		t.Fatalf("connectors = %v, want the web-search connector", body["connectors"])
	}
	if id, _ := conns[0].(map[string]any)["id"].(string); id != "web-search" {
		t.Errorf("connectors[0].id = %q, want web-search", id)
	}

	if !strings.Contains(rec.Body.String(), "hi from cohere") {
		t.Errorf("client response missing converted content: %s", rec.Body.String())
	}
}

// ===========================================================================
// Coze — bot- prefix trimming, forced user id from channel config,
// last message → query.
// ===========================================================================

func TestProvider_Coze_BotIDTrimAndConfiguredUser(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Coze,
		baseURL:     "https://api.coze.com",
		models:      "bot-728432",
		configJSON:  `{"user_id":"configured-user-1"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.coze.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"code": 0, "msg": "", "conversation_id": "conv-1",
			"messages": [
				{"role": "assistant", "type": "verbose", "content": "thinking...", "content_type": "text"},
				{"role": "assistant", "type": "answer", "content": "hi from coze", "content_type": "text"}
			]
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "bot-728432",
			"messages": []map[string]any{
				{"role": "user", "content": "earlier question"},
				{"role": "assistant", "content": "earlier answer"},
				{"role": "user", "content": "hi"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/open_api/v2/chat" {
		t.Errorf("Coze path = %q, want /open_api/v2/chat", got)
	}

	body := captured.decodedBody(t)
	// The "bot-" prefix is trimmed for bot_id.
	if b, _ := body["bot_id"].(string); b != "728432" {
		t.Errorf("bot_id = %q, want '728432' (bot- prefix trimmed)", b)
	}
	// The user field is FORCED from channel config, overriding any
	// client-provided user.
	if u, _ := body["user"].(string); u != "configured-user-1" {
		t.Errorf("user = %q, want channel-configured 'configured-user-1'", u)
	}
	// Only the last message becomes the query; earlier turns become
	// chat_history.
	if q, _ := body["query"].(string); q != "hi" {
		t.Errorf("query = %q, want 'hi' (last message)", q)
	}
	hist, _ := body["chat_history"].([]any)
	if len(hist) != 2 {
		t.Fatalf("chat_history = %v, want the two earlier turns", body["chat_history"])
	}

	// Only type=answer messages produce client-visible content; the
	// verbose message must be dropped.
	if !strings.Contains(rec.Body.String(), "hi from coze") {
		t.Errorf("client response missing answer content: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "thinking...") {
		t.Errorf("client response must not contain verbose message: %s", rec.Body.String())
	}
}

// ===========================================================================
// DeepL — translate endpoint, DeepL-Auth-Key scheme, target language
// parsed from the model name.
// ===========================================================================

func TestProvider_DeepL_AuthSchemeAndTargetLang(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.DeepL,
		baseURL:     "https://api.deepl.example.com",
		models:      "deepl-en",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.deepl.example.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"translations": [{"detected_source_language": "EN", "text": "Hallo"}]
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "deepl-en"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/v2/translate" {
		t.Errorf("DeepL path = %q, want /v2/translate", got)
	}
	// DeepL uses its own auth scheme — NOT Bearer.
	if auth := captured.Header.Get("Authorization"); auth != "DeepL-Auth-Key upstream-key" {
		t.Errorf("Authorization = %q, want 'DeepL-Auth-Key upstream-key'", auth)
	}

	body := captured.decodedBody(t)
	// The message content goes in as a single-element text array.
	if texts, _ := body["text"].([]any); len(texts) != 1 || texts[0] != "hi" {
		t.Errorf("text = %v, want [\"hi\"]", body["text"])
	}
	// The target language is the substring after the first '-' in the
	// model name, passed through verbatim (lowercase here; DeepL's API
	// is case-insensitive).
	if tl, _ := body["target_lang"].(string); tl != "en" {
		t.Errorf("target_lang = %q, want 'en' parsed from model name", tl)
	}
	if !strings.Contains(rec.Body.String(), "Hallo") {
		t.Errorf("client response missing translation: %s", rec.Body.String())
	}
}

// ===========================================================================
// Ollama — /api/chat, max_tokens folded into options.num_predict.
// ===========================================================================

func TestProvider_Ollama_ChatEndpointAndOptionsMapping(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Ollama,
		baseURL:     "http://localhost:11434",
		models:      "llama3:latest",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "http://localhost:11434",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"model": "llama3:latest",
			"message": {"role": "assistant", "content": "hi from ollama"},
			"done": true,
			"prompt_eval_count": 3,
			"eval_count": 5
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model":       "llama3:latest",
			"max_tokens":  128,
			"temperature": 0.7,
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/chat" {
		t.Errorf("Ollama path = %q, want /api/chat", got)
	}

	body := captured.decodedBody(t)
	// Sampling knobs move under options; max_tokens becomes
	// options.num_predict.
	opts, _ := body["options"].(map[string]any)
	if np, _ := opts["num_predict"].(float64); np != 128 {
		t.Errorf("options.num_predict = %v, want 128 (from max_tokens)", opts["num_predict"])
	}
	if temp, _ := opts["temperature"].(float64); temp != 0.7 {
		t.Errorf("options.temperature = %v, want 0.7", opts["temperature"])
	}
	if s, _ := body["stream"].(bool); s {
		t.Errorf("stream = %v, want false", body["stream"])
	}
	if !strings.Contains(rec.Body.String(), "hi from ollama") {
		t.Errorf("client response missing content: %s", rec.Body.String())
	}
}

// ===========================================================================
// PaLM — hardcoded chat-bison-001 model, x-goog-api-key, author 0/1
// encoding, topK fed from MaxTokens.
// ===========================================================================

func TestProvider_PaLM_HardcodedModelAndAuthorEncoding(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.PaLM,
		baseURL:     "https://generativelanguage.googleapis.com",
		models:      "PaLM-2",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://generativelanguage.googleapis.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"candidates": [{"author": "1", "content": "hi from palm"}]
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model":      "PaLM-2",
			"max_tokens": 256,
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The endpoint model is HARDCODED to chat-bison-001 regardless of
	// the requested model name.
	if got := captured.URL.Path; got != "/v1beta2/models/chat-bison-001:generateMessage" {
		t.Errorf("PaLM path = %q, want /v1beta2/models/chat-bison-001:generateMessage", got)
	}
	if k := captured.Header.Get("x-goog-api-key"); k != "upstream-key" {
		t.Errorf("x-goog-api-key = %q, want 'upstream-key'", k)
	}
	if auth := captured.Header.Get("Authorization"); auth != "" {
		t.Errorf("PaLM must not send Authorization, got %q", auth)
	}

	body := captured.decodedBody(t)
	// PaLM encodes roles as author "0" (user) / "1" (model), and —
	// quirk — feeds MaxTokens into topK.
	prompt, _ := body["prompt"].(map[string]any)
	msgs, _ := prompt["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("prompt.messages = %v, want one", prompt["messages"])
	}
	if a, _ := msgs[0].(map[string]any)["author"].(string); a != "0" {
		t.Errorf("prompt.messages[0].author = %q, want '0' (user)", a)
	}
	if tk, _ := body["topK"].(float64); tk != 256 {
		t.Errorf("topK = %v, want 256 (fed from max_tokens)", body["topK"])
	}
	if !strings.Contains(rec.Body.String(), "hi from palm") {
		t.Errorf("client response missing content: %s", rec.Body.String())
	}
}

// ===========================================================================
// AIProxyLibrary — /api/library/ask, only the LAST message is sent as
// the query, library id injected from channel config.
// ===========================================================================

func TestProvider_AIProxyLibrary_LastMessageQueryAndLibraryID(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.AIProxyLibrary,
		baseURL:     "https://api.aiproxy.example.com",
		models:      "gpt-4o-mini",
		configJSON:  `{"library_id":"lib-42"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.aiproxy.example.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(`{
			"success": true, "answer": "hi from aiproxy",
			"documents": [], "errCode": 0
		}`)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{
			"model": "gpt-4o-mini",
			"messages": []map[string]any{
				{"role": "user", "content": "earlier question"},
				{"role": "user", "content": "final question"},
			},
		}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/library/ask" {
		t.Errorf("AIProxy path = %q, want /api/library/ask", got)
	}
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want Bearer", auth)
	}

	body := captured.decodedBody(t)
	// Only the final message content is forwarded as the query —
	// earlier turns are DROPPED, and no messages array is sent.
	if q, _ := body["query"].(string); q != "final question" {
		t.Errorf("query = %q, want 'final question' (last message only)", q)
	}
	if _, has := body["messages"]; has {
		t.Errorf("aiproxy body must not carry a messages array: %s", captured.Body)
	}
	if lib, _ := body["libraryId"].(string); lib != "lib-42" {
		t.Errorf("libraryId = %q, want channel-configured 'lib-42'", lib)
	}
	if !strings.Contains(rec.Body.String(), "hi from aiproxy") {
		t.Errorf("client response missing answer: %s", rec.Body.String())
	}
}

// ===========================================================================
// Cloudflare Workers AI — account-scoped URL from channel config, or
// the AI Gateway passthrough form.
// ===========================================================================

func TestProvider_Cloudflare_AccountScopedURL(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Cloudflare,
		baseURL:     "https://api.cloudflare.com",
		models:      "@cf/meta/llama-3.1-8b-instruct",
		configJSON:  `{"user_id":"cf-account-7"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.cloudflare.com",
		captureUpstream(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("@cf/meta/llama-3.1-8b-instruct")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "@cf/meta/llama-3.1-8b-instruct"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// The account id comes from channel config and rides in the URL
	// path.
	if got := captured.URL.Path; got != "/client/v4/accounts/cf-account-7/ai/v1/chat/completions" {
		t.Errorf("Cloudflare path = %q, want /client/v4/accounts/cf-account-7/ai/v1/chat/completions", got)
	}
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want Bearer", auth)
	}
}

func TestProvider_Cloudflare_AIGatewayPassthroughURL(t *testing.T) {
	// With a gateway base URL the account path segment is skipped
	// entirely — the base URL is used as-is.
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Cloudflare,
		baseURL:     "https://gateway.ai.cloudflare.com/v1/acct/gw/workers-ai",
		models:      "@cf/meta/llama-3.1-8b-instruct",
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://gateway.ai.cloudflare.com",
		captureUpstream(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("@cf/meta/llama-3.1-8b-instruct")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "@cf/meta/llama-3.1-8b-instruct"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.String(); got != "https://gateway.ai.cloudflare.com/v1/acct/gw/workers-ai/v1/chat/completions" {
		t.Errorf("Cloudflare gateway URL = %q, want base used verbatim + /v1/chat/completions", got)
	}
}
