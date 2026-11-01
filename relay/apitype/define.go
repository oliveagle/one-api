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
	// CommandCode is Command Code's CLI endpoint (/alpha/generate), which
	// speaks neither OpenAI nor any other public protocol: the adaptor
	// converts chat completions to and from the CLI's own request/event
	// format. See relay/adaptor/commandcode.
	CommandCode

	Dummy // this one is only for count, do not add any channel after this
)
