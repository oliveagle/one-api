package controller

// Tier-2 provider tests: OpenAI-compatible providers whose adaptor
// branches in openai.Adaptor.GetRequestURL / GetFullRequestURL /
// SetupCommonRequestHeader. Each test pins one URL-rewriting or
// header quirk that a passthrough test cannot see.
//
// Quirks pinned here:
//   - Minimax        proprietary path /v1/text/chatcompletion_v2
//   - Doubao         /api/v3/chat/completions
//   - Novita         base is used verbatim + /chat/completions (no /v1)
//   - BaiduV2        /v2/chat/completions
//   - AliBailian     /compatible-mode/v1/chat/completions
//   - GeminiV2       strips the client's /v1 prefix before joining
//   - OpenAICompatible  strips /v1 and tolerates trailing slash in base
//   - AIHubMix       de-duplicates /v1 when base already ends in /v1
//   - Cloudflare AI Gateway + OpenAI channel type  strips /v1
//   - Cloudflare AI Gateway + Azure channel type   strips
//                    /openai/deployments
//   - channel Headers field overrides Content-Type and adds custom
//                    headers (SetupCommonRequestHeader)
//   - stream requests without client Accept get
//                    "Accept: text/event-stream" stamped upstream

import (
	"net/http"
	"strings"
	"net/http/httptest"
	"testing"

	"github.com/songquanpeng/one-api/relay/channeltype"
)

// runProviderChat wires a provider stack, registers one canned
// upstream handler on prefix, posts a chat request for model, and
// returns the capture + client response. It is the common body of the
// Tier-2 tests: one request, one canned upstream, assert on the
// capture.
func runProviderChat(t *testing.T, opts providerStackOptions, model string, extraBody map[string]any) (*upstreamCapture, *httptest.ResponseRecorder) {
	t.Helper()
	r, mt := setupProviderStack(t, opts)
	var captured *upstreamCapture
	mt.Match(http.MethodPost, opts.baseURL,
		captureUpstream(t, &captured, http.StatusOK,
			standardOpenAIChatResponse(model)))
	mods := map[string]any{"model": model}
	for k, v := range extraBody {
		mods[k] = v
	}
	rec := doRelayRequest(t, r, "Bearer sk-test", "", basicChatBody(mods))
	return captured, rec
}

func TestProvider_Minimax_ProprietaryChatPath(t *testing.T) {
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.Minimax,
		baseURL:     "https://api.minimax.chat",
		models:      "abab6.5s-chat",
	}, "abab6.5s-chat", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Minimax is OpenAI-shaped but hangs off a proprietary endpoint —
	// NOT /v1/chat/completions.
	if got := captured.URL.Path; got != "/v1/text/chatcompletion_v2" {
		t.Errorf("Minimax path = %q, want /v1/text/chatcompletion_v2", got)
	}
	if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
		t.Errorf("Authorization = %q, want Bearer", auth)
	}
}

func TestProvider_Doubao_APIv3Path(t *testing.T) {
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.Doubao,
		baseURL:     "https://ark.cn-beijing.volces.com",
		models:      "Doubao-pro-32k",
	}, "Doubao-pro-32k", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/api/v3/chat/completions" {
		t.Errorf("Doubao path = %q, want /api/v3/chat/completions", got)
	}
}

func TestProvider_Novita_BaseUsedVerbatimWithoutV1(t *testing.T) {
	// A custom base that does NOT end in a version segment: Novita's
	// URL builder appends /chat/completions directly, so the upstream
	// must see exactly base + /chat/completions (no injected /v1).
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.Novita,
		baseURL:     "https://proxy.example/novita-root",
		models:      "meta-llama/llama-3-8b-instruct",
	}, "meta-llama/llama-3-8b-instruct", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.String(); got != "https://proxy.example/novita-root/chat/completions" {
		t.Errorf("Novita URL = %q, want https://proxy.example/novita-root/chat/completions", got)
	}
}

func TestProvider_BaiduV2_V2Path(t *testing.T) {
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.BaiduV2,
		baseURL:     "https://qianfan.baidubce.com",
		models:      "ernie-4.0-8k",
	}, "ernie-4.0-8k", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/v2/chat/completions" {
		t.Errorf("BaiduV2 path = %q, want /v2/chat/completions", got)
	}
}

func TestProvider_AliBailian_CompatibleModePath(t *testing.T) {
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.AliBailian,
		baseURL:     "https://dashscope.example.com",
		models:      "qwen-turbo",
	}, "qwen-turbo", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.Path; got != "/compatible-mode/v1/chat/completions" {
		t.Errorf("AliBailian path = %q, want /compatible-mode/v1/chat/completions", got)
	}
}

func TestProvider_GeminiOpenAICompatible_StripsClientV1Prefix(t *testing.T) {
	// The client posts /v1/chat/completions; the Gemini OpenAI-compat
	// endpoint already lives under .../openai/, so the adaptor strips
	// the /v1 from the REQUEST path (not from the base) before joining.
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.GeminiOpenAICompatible,
		baseURL:     "https://gemini.example/v1beta/openai/",
		models:      "gemini-2.0-flash",
	}, "gemini-2.0-flash", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Trailing slash on the base must be trimmed too, or the URL gets
	// a double slash.
	if got := captured.URL.String(); got != "https://gemini.example/v1beta/openai/chat/completions" {
		t.Errorf("GeminiV2 URL = %q, want https://gemini.example/v1beta/openai/chat/completions", got)
	}
}

func TestProvider_OpenAICompatible_StripsV1AndTrailingSlash(t *testing.T) {
	// The "custom OpenAI-compatible" channel type joins base + request
	// path with the /v1 stripped and a trailing slash on the base
	// trimmed — otherwise custom gateways 404 on /openai//chat/...
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.OpenAICompatible,
		baseURL:     "https://llm.example.com/openai/",
		models:      "custom-model",
	}, "custom-model", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.String(); got != "https://llm.example.com/openai/chat/completions" {
		t.Errorf("OpenAICompatible URL = %q, want https://llm.example.com/openai/chat/completions", got)
	}
}

func TestProvider_AIHubMix_NormalizesDoubledV1(t *testing.T) {
	// AIHubMix is documented as https://aihubmix.com/v1 but the relay's
	// request path already carries /v1/...; naive joining produces
	// /v1/v1/chat/completions → 404. Both base forms must normalize to
	// the same upstream URL.
	for _, baseURL := range []string{"https://aihubmix.com/v1", "https://aihubmix.com/v1/", "https://aihubmix.com"} {
		captured, rec := runProviderChat(t, providerStackOptions{
			channelType: channeltype.AIHubMix,
			baseURL:     baseURL,
			models:      "gpt-4o-mini",
		}, "gpt-4o-mini", nil)

		if rec.Code != http.StatusOK {
			t.Fatalf("base %q: status = %d, want 200; body=%s", baseURL, rec.Code, rec.Body.String())
		}
		if got := captured.URL.String(); got != "https://aihubmix.com/v1/chat/completions" {
			t.Errorf("base %q: AIHubMix URL = %q, want https://aihubmix.com/v1/chat/completions", baseURL, got)
		}
		if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
			t.Errorf("base %q: Authorization = %q, want Bearer", baseURL, auth)
		}
	}
}

func TestProvider_CloudflareGateway_StripsV1ForOpenAI(t *testing.T) {
	// Routing OpenAI through a Cloudflare AI Gateway: GetFullRequestURL
	// special-cases gateway.ai.cloudflare.com bases for the OpenAI
	// channel type by stripping the client's /v1 prefix.
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.OpenAI,
		baseURL:     "https://gateway.ai.cloudflare.com/v1/acct/gw/openai",
		models:      "gpt-4o-mini",
	}, "gpt-4o-mini", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.String(); got != "https://gateway.ai.cloudflare.com/v1/acct/gw/openai/chat/completions" {
		t.Errorf("gateway URL = %q, want .../openai/chat/completions (no /v1)", got)
	}
}

func TestProvider_CloudflareGateway_StripsDeploymentsPrefixForAzure(t *testing.T) {
	// Azure through a Cloudflare AI Gateway: the Azure URL builder
	// produces /openai/deployments/{model}/chat/completions?..., which
	// the gateway special-case strips down to /{model}/chat/...
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.Azure,
		baseURL:     "https://gateway.ai.cloudflare.com/v1/acct/gw/azure",
		models:      "gpt-4o",
		configJSON:  `{"api_version":"2024-06-01"}`,
	})

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://gateway.ai.cloudflare.com",
		captureUpstream(t, &captured, http.StatusOK,
			standardOpenAIChatResponse("gpt-4o")))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gpt-4o"}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.URL.String(); got != "https://gateway.ai.cloudflare.com/v1/acct/gw/azure/gpt-4o/chat/completions?api-version=2024-06-01" {
		t.Errorf("gateway Azure URL = %q, want .../azure/gpt-4o/chat/completions?api-version=2024-06-01", got)
	}
	// The gateway branch belongs to GetFullRequestURL; header handling
	// must stay Azure's (api-key, not Bearer).
	if k := captured.Header.Get("api-key"); k != "upstream-key" {
		t.Errorf("api-key header = %q, want 'upstream-key'", k)
	}
}

func TestProvider_ChannelHeaders_OverrideDefaults(t *testing.T) {
	// The channel's Headers field is stamped on every upstream request
	// AFTER the common headers, so it can both add custom headers and
	// override Content-Type (see SetupCommonRequestHeader).
	captured, rec := runProviderChat(t, providerStackOptions{
		channelType: channeltype.OpenAI,
		baseURL:     "https://api.openai.com",
		models:      "gpt-4o-mini",
		headersJSON: `{"X-Custom-Channel-Header":"channel-value","Content-Type":"application/json; charset=utf-8"}`,
	}, "gpt-4o-mini", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := captured.Header.Get("X-Custom-Channel-Header"); got != "channel-value" {
		t.Errorf("X-Custom-Channel-Header = %q, want 'channel-value'", got)
	}
	if ct := captured.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want channel override 'application/json; charset=utf-8'", ct)
	}
}

func TestProvider_Stream_GetsSSEAcceptHeader(t *testing.T) {
	// SetupCommonRequestHeader stamps "Accept: text/event-stream" on
	// stream requests when the client did not send an Accept header —
	// several upstreams (ali, minimax, ...) refuse to stream otherwise.
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.OpenAI,
		baseURL:     "https://api.openai.com",
		models:      "gpt-4o-mini",
	})

	sse := "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://api.openai.com",
		captureUpstream(t, &captured, http.StatusOK, []byte(sse)))

	rec := doRelayRequest(t, r, "Bearer sk-test", "",
		basicChatBody(map[string]any{"model": "gpt-4o-mini", "stream": true}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if accept := captured.Header.Get("Accept"); accept != "text/event-stream" {
		t.Errorf("upstream Accept = %q, want text/event-stream", accept)
	}
	// Plain chat requests take the fast path in getRequestBody: the
	// client's body is forwarded VERBATIM — openai.ConvertRequest's
	// stream_options injection does not run for unconverted requests.
	// Pin that: no stream_options may appear on the wire.
	body := captured.decodedBody(t)
	if _, has := body["stream_options"]; has {
		t.Errorf("fast path must forward the client body verbatim, got stream_options injected: %s", captured.Body)
	}
	if s, _ := body["stream"].(bool); !s {
		t.Errorf("upstream body lost stream=true: %s", captured.Body)
	}
}

// TestProvider_OpenAIResponsesType pins the dedicated "OpenAI Responses"
// channel type (channeltype.OpenAIResponses): /v1/responses passes through to
// {base}/v1/responses with a Bearer key and zero conversion, while a chat
// request is refused with 503 so the relay fails over to a chat channel —
// protocol conversion between the two APIs has been removed.
func TestProvider_OpenAIResponsesType(t *testing.T) {
	r, mt := setupProviderStack(t, providerStackOptions{
		channelType: channeltype.OpenAIResponses,
		baseURL:     "https://resp.mock",
		models:      "k3-test",
	})
	var captured *upstreamCapture
	mt.Match(http.MethodPost, "https://resp.mock",
		captureUpstream(t, &captured, http.StatusOK,
			[]byte(`{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2}}`)))

	t.Run("responses passthrough", func(t *testing.T) {
		captured = nil
		rec := doRelayRequestTo(t, r, "/v1/responses", "Bearer sk-test", "",
			`{"model":"k3-test","input":"hi","stream":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if captured == nil {
			t.Fatal("upstream not hit")
		}
		if got := captured.URL.Path; got != "/v1/responses" {
			t.Errorf("upstream path = %q, want /v1/responses (verbatim passthrough)", got)
		}
		if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
			t.Errorf("Authorization = %q, want Bearer upstream-key", auth)
		}
		if body := string(captured.Body); !strings.Contains(body, `"model":"k3-test"`) {
			t.Errorf("upstream body lost the model: %s", body)
		}
		if obj := rec.Body.String(); !strings.Contains(obj, `"object":"response"`) {
			t.Errorf("client body should be the untouched Responses payload: %s", obj)
		}
	})

	t.Run("chat refused with 503", func(t *testing.T) {
		captured = nil
		rec := doRelayRequest(t, r, "Bearer sk-test", "",
			basicChatBody(map[string]any{"model": "k3-test"}))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (chat refused on a responses-only type); body=%s", rec.Code, rec.Body.String())
		}
		if captured != nil {
			t.Error("upstream must not be hit by a refused chat request")
		}
		if !strings.Contains(rec.Body.String(), "chat_unsupported_on_channel") {
			t.Errorf("error should carry chat_unsupported_on_channel: %s", rec.Body.String())
		}
	})
}
