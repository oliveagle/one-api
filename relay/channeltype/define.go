package channeltype

const (
	Unknown = iota
	OpenAI
	API2D
	Azure
	CloseAI
	OpenAISB
	OpenAIMax
	OhMyGPT
	Custom
	Ails
	AIProxy
	PaLM
	API2GPT
	AIGC2D
	Anthropic
	Baidu
	Zhipu
	Ali
	Xunfei
	AI360
	OpenRouter
	AIProxyLibrary
	FastGPT
	Tencent
	Gemini
	Moonshot
	Baichuan
	Minimax
	Mistral
	Groq
	Ollama
	LingYiWanWu
	StepFun
	AwsClaude
	Coze
	Cohere
	DeepSeek
	Cloudflare
	DeepL
	TogetherAI
	Doubao
	Novita
	VertextAI
	Proxy
	SiliconFlow
	XAI
	Replicate
	BaiduV2
	XunfeiV2
	AliBailian
	OpenAICompatible
	GeminiOpenAICompatible
	AIHubMix
	// Mock is a built-in channel type whose adaptor synthesizes responses
	// in-process for integration tests. See relay/adaptor/mock/adaptor.go
	// and apitype.Mock for the dispatch wiring.
	Mock
	Dummy
)
