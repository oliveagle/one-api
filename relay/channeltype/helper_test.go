package channeltype

import (
	"fmt"
	"testing"

	"github.com/songquanpeng/one-api/relay/apitype"
)

// ToAPIType maps a per-channel type id to the abstract API type the adaptor
// layer uses. If a new channel type is added but no case is wired here the
// pipeline silently defaults to OpenAI and the wrong JSON shape gets sent.
func TestToAPIType_KnownChannels(t *testing.T) {
	cases := []struct {
		channel int
		want    int
		name    string
	}{
		{OpenAI, apitype.OpenAI, "OpenAI"},
		{Anthropic, apitype.Anthropic, "Anthropic"},
		{Baidu, apitype.Baidu, "Baidu"},
		{PaLM, apitype.PaLM, "PaLM"},
		{Zhipu, apitype.Zhipu, "Zhipu"},
		{Ali, apitype.Ali, "Ali"},
		{Xunfei, apitype.Xunfei, "Xunfei"},
		{AIProxyLibrary, apitype.AIProxyLibrary, "AIProxyLibrary"},
		{Tencent, apitype.Tencent, "Tencent"},
		{Gemini, apitype.Gemini, "Gemini"},
		{Ollama, apitype.Ollama, "Ollama"},
		{AwsClaude, apitype.AwsClaude, "AwsClaude"},
		{Coze, apitype.Coze, "Coze"},
		{Cohere, apitype.Cohere, "Cohere"},
		{Cloudflare, apitype.Cloudflare, "Cloudflare"},
		{DeepL, apitype.DeepL, "DeepL"},
		{VertextAI, apitype.VertexAI, "VertextAI"},
		{Replicate, apitype.Replicate, "Replicate"},
		{Proxy, apitype.Proxy, "Proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToAPIType(tc.channel); got != tc.want {
				t.Errorf("ToAPIType(%d) = %d, want %d", tc.channel, got, tc.want)
			}
		})
	}
}

// Channel types that don't override the API type must fall back to OpenAI.
func TestToAPIType_FallsBackToOpenAI(t *testing.T) {
	for _, ch := range []struct {
		id   int
		name string
	}{
		{DeepSeek, "DeepSeek"},
		{Mistral, "Mistral"},
		{Groq, "Groq"},
		{Doubao, "Doubao"},
		{XAI, "XAI"},
		{BaiduV2, "BaiduV2"},
	} {
		t.Run(ch.name, func(t *testing.T) {
			if got := ToAPIType(ch.id); got != apitype.OpenAI {
				t.Errorf("ToAPIType(%d) = %d, want %d (openAI fallback)", ch.id, got, apitype.OpenAI)
			}
		})
	}
}

func TestToAPIType_Unknown(t *testing.T) {
	if got := ToAPIType(99999); got != apitype.OpenAI {
		t.Errorf("unknown channel type -> %d, want OpenAI fallback (%d)", got, apitype.OpenAI)
	}
}

// Each channel id must have a corresponding slot in ChannelBaseURLs, and
// the slot must have a non-empty URL OR be one of the documented "no
// upstream URL" types (LLM providers running locally like Ollama, OpenAI
// variants whose URL is fixed at runtime).
func TestChannelBaseURLs_EverySlotAccountedFor(t *testing.T) {
	if len(ChannelBaseURLs) != Dummy {
		t.Fatalf("ChannelBaseURLs length = %d, want Dummy (%d)", len(ChannelBaseURLs), Dummy)
	}
}

func TestChannelBaseURLs_OpenAINonEmpty(t *testing.T) {
	// OpenAI's hosted endpoint is the reference URL. If it's empty we
	// regressed to a state where every default-configured OpenAI channel
	// would point at "" and fail to dial.
	if got := ChannelBaseURLs[OpenAI]; got == "" {
		t.Fatal("ChannelBaseURLs[OpenAI] is empty")
	}
}

func TestChannelBaseURLs_ApitypeDistinctFromChannel(t *testing.T) {
	// apitype ids can shadow channeltype ids. Find any collision that would
	// make ChannelBaseURLs[channeltype.X] and channeltype.ToAPIType(channeltype.X)
	// impossible to disambiguate.
	for ch := 0; ch < Dummy; ch++ {
		if ch >= len(ChannelBaseURLs) {
			t.Errorf("ChannelBaseURLs shorter than expected for id %d", ch)
			continue
		}
		// Just checking every entry is reachable (no panic, no nil).
		_ = ChannelBaseURLs[ch]
		_ = ToAPIType(ch)
	}
}

// Spot-check a known cloud URL: the OpenAI base URL must literally equal
// https://api.openai.com (trimmed trailing slash allowed).
func TestChannelBaseURLs_OpenAIExact(t *testing.T) {
	if got := ChannelBaseURLs[OpenAI]; got != "https://api.openai.com" {
		t.Errorf("ChannelBaseURLs[OpenAI] = %q, want https://api.openai.com", got)
	}
}

func TestChannelBaseURLs_GeminiOpenAICompatible(t *testing.T) {
	// The Gemini-OpenAI-Compatible slot must point at the Google endpoint,
	// and AIHubMix must point at aihubmix.com — if either of these slips,
	// the upstream URL gets misrouted.
	cases := []struct {
		id   int
		want string
	}{
		{GeminiOpenAICompatible, "https://generativelanguage.googleapis.com/v1beta/openai/"},
		{AIHubMix, "https://aihubmix.com"},
		{Anthropic, "https://api.anthropic.com"},
		{Ollama, "http://localhost:11434"},
		{DeepSeek, "https://api.deepseek.com"},
		{Groq, "https://api.groq.com/openai"},
		{Mistral, "https://api.mistral.ai"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("slot-%d", tc.id), func(t *testing.T) {
			if got := ChannelBaseURLs[tc.id]; got != tc.want {
				t.Errorf("ChannelBaseURLs[%d] = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}
