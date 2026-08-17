package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func TestProviderRegistryRegistered(t *testing.T) {
	for _, ch := range []int{
		channeltype.Azure,
		channeltype.Minimax,
		channeltype.Doubao,
		channeltype.Novita,
		channeltype.BaiduV2,
		channeltype.AliBailian,
		channeltype.GeminiOpenAICompatible,
		channeltype.OpenRouter,
		channeltype.AI360,
		channeltype.Baichuan,
		channeltype.DeepSeek,
		channeltype.Groq,
		channeltype.LingYiWanWu,
		channeltype.Mistral,
		channeltype.Moonshot,
		channeltype.SiliconFlow,
		channeltype.StepFun,
		channeltype.TogetherAI,
		channeltype.XAI,
		channeltype.XunfeiV2,
	} {
		if _, ok := ProviderRegistry.Get(ch); !ok {
			t.Fatalf("channel type %d not registered", ch)
		}
	}
}

func TestProviderRegistryFallback(t *testing.T) {
	if d := ProviderRegistry.MustGet(channeltype.OpenAI); d.Name != "openai" {
		t.Fatalf("fallback name = %q, want openai", d.Name)
	}
	if d := ProviderRegistry.MustGet(channeltype.AIHubMix); d.Name != "openai" {
		t.Fatalf("fallback for AIHubMix = %q, want openai", d.Name)
	}
}

func TestProviderRequestURL(t *testing.T) {
	cases := []struct {
		channelType int
		base        string
		mode        int
		path        string
		model       string
		want        string
	}{
		{channeltype.Minimax, "https://api.minimaxi.com", relaymode.ChatCompletions, "/v1/chat/completions", "MiniMax-M3", "https://api.minimaxi.com/v1/text/chatcompletion_v2"},
		{channeltype.Doubao, "https://ark.cn-beijing.volces.com", relaymode.ChatCompletions, "/v1/chat/completions", "doubao-seed-1-6-250615", "https://ark.cn-beijing.volces.com/api/v3/chat/completions"},
		{channeltype.Novita, "https://api.novita.ai/v3/openai", relaymode.ChatCompletions, "/v1/chat/completions", "deepseek/deepseek-v3-0324", "https://api.novita.ai/v3/openai/chat/completions"},
		{channeltype.BaiduV2, "https://qianfan.baidubce.com", relaymode.ChatCompletions, "/v1/chat/completions", "ernie-4.5-8k", "https://qianfan.baidubce.com/v2/chat/completions"},
		{channeltype.AliBailian, "https://dashscope.aliyuncs.com", relaymode.ChatCompletions, "/v1/chat/completions", "qwen-plus", "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"},
		{channeltype.GeminiOpenAICompatible, "https://generativelanguage.googleapis.com/v1beta/openai/", relaymode.ChatCompletions, "/v1/chat/completions", "gemini-2.0-flash", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"},
		{channeltype.OpenRouter, "https://openrouter.ai/api", relaymode.ChatCompletions, "/v1/chat/completions", "openai/gpt-4o-mini", "https://openrouter.ai/api/v1/chat/completions"},
	}
	for _, tc := range cases {
		m := &meta.Meta{BaseURL: tc.base, Mode: tc.mode, RequestURLPath: tc.path, ActualModelName: tc.model, ChannelType: tc.channelType}
		got, err := ProviderRegistry.MustGet(tc.channelType).RequestURL(m)
		if err != nil {
			t.Fatalf("channel %d RequestURL error: %v", tc.channelType, err)
		}
		if got != tc.want {
			t.Fatalf("channel %d RequestURL = %q, want %q", tc.channelType, got, tc.want)
		}
	}
}

func TestAzureRequestURL(t *testing.T) {
	m := &meta.Meta{
		BaseURL:         "https://example.openai.azure.com",
		Mode:            relaymode.ChatCompletions,
		RequestURLPath:  "/v1/chat/completions",
		ActualModelName: "gpt-4o.mini",
		ChannelType:     channeltype.Azure,
	}
	m.Config.APIVersion = "2024-02-01"
	got, err := ProviderRegistry.MustGet(channeltype.Azure).RequestURL(m)
	if err != nil {
		t.Fatalf("azure RequestURL error: %v", err)
	}
	want := "https://example.openai.azure.com/openai/deployments/gpt-4omini/chat/completions?api-version=2024-02-01"
	if got != want {
		t.Fatalf("azure RequestURL = %q, want %q", got, want)
	}

	m.Mode = relaymode.ImagesGenerations
	got, err = ProviderRegistry.MustGet(channeltype.Azure).RequestURL(m)
	if err != nil {
		t.Fatalf("azure image RequestURL error: %v", err)
	}
	want = "https://example.openai.azure.com/openai/deployments/gpt-4o.mini/images/generations?api-version=2024-02-01"
	if got != want {
		t.Fatalf("azure image RequestURL = %q, want %q", got, want)
	}
}

func TestProviderSetupHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	cases := []struct {
		channelType int
		authHeader  string
		authValue   string
		extraKey    string
		extraValue  string
	}{
		{channeltype.Azure, "api-key", "azure-key", "", ""},
		{channeltype.OpenRouter, "Authorization", "Bearer openrouter-key", "X-Title", "One API"},
		{channeltype.Minimax, "Authorization", "Bearer minimax-key", "", ""},
		{channeltype.OpenAI, "Authorization", "Bearer openai-key", "", ""},
	}

	for _, tc := range cases {
		req, err := http.NewRequest(http.MethodPost, "https://example.com", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		m := &meta.Meta{APIKey: strings.TrimPrefix(tc.authValue, "Bearer "), ChannelType: tc.channelType}
		if err := ProviderRegistry.MustGet(tc.channelType).SetupHeader(c, req, m); err != nil {
			t.Fatalf("channel %d SetupHeader error: %v", tc.channelType, err)
		}
		if got := req.Header.Get(tc.authHeader); got != tc.authValue {
			t.Fatalf("channel %d header %s = %q, want %q", tc.channelType, tc.authHeader, got, tc.authValue)
		}
		if tc.extraKey != "" {
			if got := req.Header.Get(tc.extraKey); got != tc.extraValue {
				t.Fatalf("channel %d header %s = %q, want %q", tc.channelType, tc.extraKey, got, tc.extraValue)
			}
		}
	}
}

func TestProviderRegistryFrozen(t *testing.T) {
	if err := ProviderRegistry.Register(ProviderRegistry.MustGet(channeltype.OpenAI)); err == nil {
		t.Fatalf("global registry should reject registration after freeze")
	}
}
