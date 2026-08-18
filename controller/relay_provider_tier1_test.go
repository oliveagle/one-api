package controller

// Tier-1 provider tests: channel types whose adaptor is a plain OpenAI
// passthrough (apitype.OpenAI, no branch in openai.Adaptor). These pin
// the contract that EVERY OpenAI-compatible channel type builds
// {baseURL}/v1/chat/completions and authenticates with
// "Authorization: Bearer <channel key>".
//
// Two groups:
//
//  1. Explicit base URL — pins URL joining + Bearer auth per channel
//     type (a new entry in channeltype that accidentally reroutes one
//     of these shows up here).
//  2. Empty base URL — pins the DEFAULT base URL fallback
//     (relay_meta.go: if meta.BaseURL == "" use
//     channeltype.ChannelBaseURLs[channelType]). This is where e.g.
//     Groq's "/openai" path segment and Novita's "/v3/openai" segment
//     live — drift there silently breaks every channel using defaults.

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/songquanpeng/one-api/relay/channeltype"
)

// TestProvider_Tier1_OpenAIPassthrough_ExplicitBaseURL runs the full
// relay stack for every channel type that must behave exactly like
// plain OpenAI when an explicit base URL is configured.
func TestProvider_Tier1_OpenAIPassthrough_ExplicitBaseURL(t *testing.T) {
	passthroughTypes := map[string]int{
		"API2D":       channeltype.API2D,
		"CloseAI":     channeltype.CloseAI,
		"OpenAISB":    channeltype.OpenAISB,
		"OpenAIMax":   channeltype.OpenAIMax,
		"OhMyGPT":     channeltype.OhMyGPT,
		"Ails":        channeltype.Ails,
		"AIProxy":     channeltype.AIProxy,
		"API2GPT":     channeltype.API2GPT,
		"AIGC2D":      channeltype.AIGC2D,
		"FastGPT":     channeltype.FastGPT,
		"AI360":       channeltype.AI360,
		"Moonshot":    channeltype.Moonshot,
		"Baichuan":    channeltype.Baichuan,
		"Mistral":     channeltype.Mistral,
		"Groq":        channeltype.Groq,
		"LingYiWanWu": channeltype.LingYiWanWu,
		"StepFun":     channeltype.StepFun,
		"DeepSeek":    channeltype.DeepSeek,
		"TogetherAI":  channeltype.TogetherAI,
		"SiliconFlow": channeltype.SiliconFlow,
		"XAI":         channeltype.XAI,
		"XunfeiV2":    channeltype.XunfeiV2,
		"Custom":      channeltype.Custom,
	}

	for name, ct := range passthroughTypes {
		t.Run(name, func(t *testing.T) {
			r, mt := setupProviderStack(t, providerStackOptions{
				channelType: ct,
				baseURL:     "https://passthrough.example.com",
				models:      "test-model",
			})

			var captured *upstreamCapture
			mt.Match(http.MethodPost, "https://passthrough.example.com",
				captureUpstream(t, &captured, http.StatusOK,
					standardOpenAIChatResponse("test-model")))

			rec := doRelayRequest(t, r, "Bearer sk-test", "",
				basicChatBody(map[string]any{"model": "test-model"}))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if captured == nil {
				t.Fatal("MockTransport did not capture the upstream request")
			}
			if got := captured.URL.Path; got != "/v1/chat/completions" {
				t.Errorf("%s upstream path = %q, want /v1/chat/completions", name, got)
			}
			if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
				t.Errorf("%s Authorization = %q, want 'Bearer upstream-key'", name, auth)
			}
			if ct := captured.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("%s Content-Type = %q, want application/json", name, ct)
			}
		})
	}
}

// TestProvider_Tier1_DefaultBaseURLFallback pins the default upstream
// base URL each channel type falls back to when the channel has no
// base_url configured (channeltype.ChannelBaseURLs). Several providers
// have non-root default paths (Groq /openai, Novita /v3/openai) —
// changing those defaults breaks every channel relying on them.
func TestProvider_Tier1_DefaultBaseURLFallback(t *testing.T) {
	cases := []struct {
		name        string
		channelType int
		model       string
		// wantFullURL is the exact URL the adaptor must build. The
		// MockTransport handler is registered on the scheme+host part;
		// the full-URL assertion pins path segments from the default.
		wantFullURL string
	}{
		{
			name:        "DeepSeek defaults to api.deepseek.com",
			channelType: channeltype.DeepSeek, model: "deepseek-chat",
			wantFullURL: "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:        "Groq defaults to api.groq.com/openai",
			channelType: channeltype.Groq, model: "llama-3.1-8b-instant",
			wantFullURL: "https://api.groq.com/openai/v1/chat/completions",
		},
		{
			name:        "Novita defaults to api.novita.ai/v3/openai and appends /chat/completions (no extra /v1)",
			channelType: channeltype.Novita, model: "meta-llama/llama-3-8b-instruct",
			wantFullURL: "https://api.novita.ai/v3/openai/chat/completions",
		},
		{
			name:        "Moonshot defaults to api.moonshot.cn",
			channelType: channeltype.Moonshot, model: "moonshot-v1-8k",
			wantFullURL: "https://api.moonshot.cn/v1/chat/completions",
		},
		{
			name:        "XAI defaults to api.x.ai",
			channelType: channeltype.XAI, model: "grok-beta",
			wantFullURL: "https://api.x.ai/v1/chat/completions",
		},
		{
			name:        "TogetherAI defaults to api.together.xyz",
			channelType: channeltype.TogetherAI, model: "meta-llama/Llama-3-8b-chat-hf",
			wantFullURL: "https://api.together.xyz/v1/chat/completions",
		},
		{
			name:        "SiliconFlow defaults to api.siliconflow.cn",
			channelType: channeltype.SiliconFlow, model: "Qwen/Qwen2.5-7B-Instruct",
			wantFullURL: "https://api.siliconflow.cn/v1/chat/completions",
		},
		{
			name:        "OpenRouter defaults to openrouter.ai/api",
			channelType: channeltype.OpenRouter, model: "openai/gpt-4o-mini",
			wantFullURL: "https://openrouter.ai/api/v1/chat/completions",
		},
		{
			name:        "XunfeiV2 defaults to spark-api-open.xf-yun.com (OpenAI-compatible Spark v3.1+)",
			channelType: channeltype.XunfeiV2, model: "generalv3.5",
			wantFullURL: "https://spark-api-open.xf-yun.com/v1/chat/completions",
		},
		{
			name:        "Minimax defaults to api.minimax.chat and uses its proprietary path",
			channelType: channeltype.Minimax, model: "abab6.5s-chat",
			wantFullURL: "https://api.minimax.chat/v1/text/chatcompletion_v2",
		},
		{
			name:        "Doubao defaults to ark.cn-beijing.volces.com with /api/v3 path",
			channelType: channeltype.Doubao, model: "Doubao-pro-32k",
			wantFullURL: "https://ark.cn-beijing.volces.com/api/v3/chat/completions",
		},
		{
			name:        "BaiduV2 defaults to qianfan.baidubce.com with /v2 path",
			channelType: channeltype.BaiduV2, model: "ernie-4.0-8k",
			wantFullURL: "https://qianfan.baidubce.com/v2/chat/completions",
		},
		{
			name:        "AliBailian defaults to dashscope.aliyuncs.com compatible-mode",
			channelType: channeltype.AliBailian, model: "qwen-turbo",
			wantFullURL: "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
		{
			name:        "GeminiOpenAICompatible defaults to generativelanguage.googleapis.com/v1beta/openai with /v1 stripped",
			channelType: channeltype.GeminiOpenAICompatible, model: "gemini-2.0-flash",
			wantFullURL: "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, mt := setupProviderStack(t, providerStackOptions{
				channelType: tc.channelType,
				baseURL:     "", // no channel base URL → default fallback
				models:      tc.model,
			})

			// Register on scheme://host so whatever path the adaptor
			// builds reaches a handler; the assertion below pins the
			// exact full URL.
			u, err := url.Parse(tc.wantFullURL)
			if err != nil {
				t.Fatalf("bad wantFullURL: %v", err)
			}
			prefix := u.Scheme + "://" + u.Host
			var captured *upstreamCapture
			mt.Match(http.MethodPost, prefix,
				captureUpstream(t, &captured, http.StatusOK,
					standardOpenAIChatResponse(tc.model)))

			rec := doRelayRequest(t, r, "Bearer sk-test", "",
				basicChatBody(map[string]any{"model": tc.model}))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if captured == nil {
				t.Fatal("MockTransport did not capture the upstream request")
			}
			if got := captured.URL.String(); got != tc.wantFullURL {
				t.Errorf("default base URL build = %q, want %q", got, tc.wantFullURL)
			}
			if auth := captured.Header.Get("Authorization"); auth != "Bearer upstream-key" {
				t.Errorf("Authorization = %q, want 'Bearer upstream-key'", auth)
			}
		})
	}
}
