package apitype

const (
	OpenAI = iota
	Anthropic
	PaLM
	Baidu
	Zhipu
	Ali
	Xunfei
	AIProxyLibrary
	Tencent
	Gemini
	Ollama
	AwsClaude
	Coze
	Cohere
	Cloudflare
	DeepL
	VertexAI
	Proxy
	Replicate
	// Mock is a built-in channel whose adaptor synthesizes responses
	// in-process (it never touches the network). It exists so the full
	// relay pipeline — TokenAuth, Distribute, RelayTextHelper, quota
	// accounting, streaming — can be exercised end-to-end in tests
	// without depending on a real upstream provider. The concrete
	// behavior (OpenAI chat / stream / error / tool-call) is selected
	// per-request via the X-Mock-Behavior header, see
	// relay/adaptor/mock/adaptor.go.
	Mock

	Dummy // this one is only for count, do not add any channel after this
)
