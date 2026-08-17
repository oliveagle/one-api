package openai

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/adaptor/ai360"
	"github.com/songquanpeng/one-api/relay/adaptor/alibailian"
	"github.com/songquanpeng/one-api/relay/adaptor/baichuan"
	"github.com/songquanpeng/one-api/relay/adaptor/baiduv2"
	"github.com/songquanpeng/one-api/relay/adaptor/deepseek"
	"github.com/songquanpeng/one-api/relay/adaptor/doubao"
	"github.com/songquanpeng/one-api/relay/adaptor/geminiv2"
	"github.com/songquanpeng/one-api/relay/adaptor/groq"
	"github.com/songquanpeng/one-api/relay/adaptor/lingyiwanwu"
	"github.com/songquanpeng/one-api/relay/adaptor/minimax"
	"github.com/songquanpeng/one-api/relay/adaptor/mistral"
	"github.com/songquanpeng/one-api/relay/adaptor/moonshot"
	"github.com/songquanpeng/one-api/relay/adaptor/novita"
	"github.com/songquanpeng/one-api/relay/adaptor/openrouter"
	"github.com/songquanpeng/one-api/relay/adaptor/provider"
	"github.com/songquanpeng/one-api/relay/adaptor/siliconflow"
	"github.com/songquanpeng/one-api/relay/adaptor/stepfun"
	"github.com/songquanpeng/one-api/relay/adaptor/togetherai"
	"github.com/songquanpeng/one-api/relay/adaptor/xai"
	"github.com/songquanpeng/one-api/relay/adaptor/xunfeiv2"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// ProviderRegistry is the single authoritative registry for OpenAI-compatible
// provider differences. Every provider-specific URL, authentication header, or
// extra header lives here; the OpenAI adaptor consults it instead of carrying
// its own switch statements.
var ProviderRegistry = buildProviderRegistry()

func buildProviderRegistry() *provider.Registry {
	r := provider.NewRegistry()

	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.Azure,
		Name:        "azure",
		Models:      ModelList,
		RequestURL:  azureRequestURL,
		SetupHeader: azureSetupHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.Minimax,
		Name:        "minimax",
		Models:      minimax.ModelList,
		RequestURL:  minimax.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.Doubao,
		Name:        "doubao",
		Models:      doubao.ModelList,
		RequestURL:  doubao.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.Novita,
		Name:        "novita",
		Models:      novita.ModelList,
		RequestURL:  novita.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.BaiduV2,
		Name:        "baiduv2",
		Models:      baiduv2.ModelList,
		RequestURL:  baiduv2.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.AliBailian,
		Name:        "alibailian",
		Models:      alibailian.ModelList,
		RequestURL:  alibailian.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.GeminiOpenAICompatible,
		Name:        "geminiv2",
		Models:      geminiv2.ModelList,
		RequestURL:  geminiv2.GetRequestURL,
		SetupHeader: defaultBearerHeader,
	})
	r.MustRegister(provider.Descriptor{
		ChannelType: channeltype.OpenRouter,
		Name:        "openrouter",
		Models:      openrouter.ModelList,
		RequestURL:  defaultRequestURL,
		SetupHeader: openRouterSetupHeader,
	})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.AI360, Name: "360", Models: ai360.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.Baichuan, Name: "baichuan", Models: baichuan.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.DeepSeek, Name: "deepseek", Models: deepseek.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.Groq, Name: "groq", Models: groq.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.LingYiWanWu, Name: "lingyiwanwu", Models: lingyiwanwu.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.Mistral, Name: "mistralai", Models: mistral.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.Moonshot, Name: "moonshot", Models: moonshot.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.SiliconFlow, Name: "siliconflow", Models: siliconflow.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.StepFun, Name: "stepfun", Models: stepfun.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.TogetherAI, Name: "together.ai", Models: togetherai.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.XAI, Name: "xai", Models: xai.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})
	r.MustRegister(provider.Descriptor{ChannelType: channeltype.XunfeiV2, Name: "xunfeiv2", Models: xunfeiv2.ModelList, RequestURL: defaultRequestURL, SetupHeader: defaultBearerHeader})

	// Fallback covers OpenAI, AIHubMix, OpenAICompatible, and every other
	// channel that shares the default OpenAI URL shape.
	if err := r.SetFallback(provider.Descriptor{
		ChannelType: channeltype.OpenAI,
		Name:        "openai",
		Models:      ModelList,
		RequestURL:  defaultRequestURL,
		SetupHeader: defaultBearerHeader,
	}); err != nil {
		panic(err)
	}

	r.Freeze()
	return r
}

func defaultRequestURL(m *meta.Meta) (string, error) {
	return GetFullRequestURL(m.BaseURL, m.RequestURLPath, m.ChannelType), nil
}

func defaultBearerHeader(_ *gin.Context, req *http.Request, m *meta.Meta) error {
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	return nil
}

func azureRequestURL(m *meta.Meta) (string, error) {
	if m.Mode == relaymode.ImagesGenerations {
		return fmt.Sprintf("%s/openai/deployments/%s/images/generations?api-version=%s",
			m.BaseURL, m.ActualModelName, m.Config.APIVersion), nil
	}
	requestURL := strings.Split(m.RequestURLPath, "?")[0]
	requestURL = fmt.Sprintf("%s?api-version=%s", requestURL, m.Config.APIVersion)
	task := strings.TrimPrefix(requestURL, "/v1/")
	modelName := strings.ReplaceAll(m.ActualModelName, ".", "")
	requestURL = fmt.Sprintf("/openai/deployments/%s/%s", modelName, task)
	return GetFullRequestURL(m.BaseURL, requestURL, m.ChannelType), nil
}

func azureSetupHeader(_ *gin.Context, req *http.Request, m *meta.Meta) error {
	req.Header.Set("api-key", m.APIKey)
	return nil
}

func openRouterSetupHeader(_ *gin.Context, req *http.Request, m *meta.Meta) error {
	if err := defaultBearerHeader(nil, req, m); err != nil {
		return err
	}
	req.Header.Set("HTTP-Referer", "https://github.com/songquanpeng/one-api")
	req.Header.Set("X-Title", "One API")
	return nil
}
