// Package commandcode adapts Command Code's CLI endpoint
// (POST https://api.commandcode.ai/alpha/generate) to the OpenAI Chat
// Completions protocol.
//
// Command Code sells plans (Go / GOAT / Pro / Max ...). Every plan but Go
// is also served on the ordinary Provider API (/provider/v1/chat/completions,
// /provider/v1/messages, /provider/v1/responses) which a proxy can speak
// directly — those need no adaptor, a plain "OpenAI 兼容" channel works.
//
// A Go plan's key has NO Provider API: both /provider/v1/chat/completions and
// /provider/v1/messages answer 403 "Your Go plan doesn't include API access".
// The only endpoint such a key is taken at is /alpha/generate, in the CLI's
// own request format, and that endpoint refuses anything that does not look
// like the CLI: `params.stream` must be true (a plain non-streaming request is
// rejected with 400 "Proxy use detected. This endpoint only serves CLI.").
// So this adaptor
//
//   - rewrites an OpenAI chat request into the CLI's {config, memory, taste,
//     skills, permissionMode, params} envelope, always with stream:true, and
//   - turns the reply's line-delimited event stream back into an OpenAI
//     response — chat chunks when the client asked for a stream, one
//     aggregated chat.completion when it did not.
//
// The version header is pinned to the CLI release this was verified against;
// bump CLI_VERSION when Command Code changes the shape (they may tighten the
// proxy check at any time — stream:true is a shape check, not a secret).
package commandcode

import "encoding/json"

// GenerateRequest is the body /alpha/generate takes.
type GenerateRequest struct {
	Config         GenerateConfig `json:"config"`
	Memory         any            `json:"memory"`
	Taste          any            `json:"taste"`
	Skills         any            `json:"skills"`
	PermissionMode string         `json:"permissionMode"`
	Params         GenerateParams `json:"params"`
}

// GenerateConfig is the working-context block the CLI fills from the repo it
// runs in. A proxy has no repo, so every field is a neutral empty value (the
// upstream validates their presence and types, not their content).
type GenerateConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []any    `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

// GenerateParams is the chat request proper — Anthropic-ish content parts and
// tool declarations, plus `stream` which must be true.
type GenerateParams struct {
	Model           string              `json:"model"`
	Messages        []GenerateMessage   `json:"messages"`
	Tools           []GenerateTool      `json:"tools"`
	System          string              `json:"system"`
	MaxTokens       int                 `json:"max_tokens"`
	Stream          bool                `json:"stream"`
	Temperature     *float64            `json:"temperature,omitempty"`
	TopP            *float64            `json:"top_p,omitempty"`
	Stop            any                 `json:"stop,omitempty"`
	ReasoningEffort string              `json:"reasoning_effort,omitempty"`
	ToolChoice      *GenerateToolChoice `json:"tool_choice,omitempty"`
}

// GenerateToolChoice is the only tool_choice shape the endpoint accepts: an
// object with type "auto" | "any" | "tool" (+ name). The OpenAI strings
// ("auto"/"required"/"none") are rejected with a schema validation error.
type GenerateToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// GenerateMessage is one turn. Content is always a list of typed parts.
type GenerateMessage struct {
	Role    string         `json:"role"`
	Content []GeneratePart `json:"content"`
}

// GeneratePart is a text / reasoning / tool-call / tool-result / image part.
type GeneratePart struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	Image    string `json:"image,omitempty"`
	MimeType string `json:"mimeType,omitempty"`

	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Input      any             `json:"input,omitempty"`
	Output     *ToolResultBody `json:"output,omitempty"`
}

// ToolResultBody is a tool result's payload.
type ToolResultBody struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// GenerateTool is a tool declaration: flat, with a JSON Schema under
// input_schema (not OpenAI's nested function.parameters).
type GenerateTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// event is one line of the reply stream. Only the fields this adaptor reads
// are declared; everything else (start-step, text-start/end, tool-input-*,
// provider-metadata, providerMetadata routing blobs ...) is ignored.
type event struct {
	Type string `json:"type"`

	// text-delta / reasoning-delta payload.
	Text string `json:"text"`

	// tool-call payload: the call arrives whole, arguments already parsed.
	ToolCallID       string          `json:"toolCallId"`
	ToolName         string          `json:"toolName"`
	Input            json.RawMessage `json:"input"`
	ProviderExecuted bool            `json:"providerExecuted"`

	// finish / abort.
	FinishReason string    `json:"finishReason"`
	TotalUsage   *genUsage `json:"totalUsage"`
	Error        *genError `json:"error"`
	Usage        *genUsage `json:"usage"`
}

// genError is an in-stream error ({"message":..,"statusCode":..} or a bare
// string, which fails the struct decode and falls back to a generic message).
type genError struct {
	Message    string `json:"message"`
	StatusCode int    `json:"statusCode"`
	Type       string `json:"type"`
	Code       string `json:"code"`
}

// genUsage is the AI-SDK usage block. inputTokens already counts cache reads,
// as OpenAI's prompt_tokens does.
type genUsage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	TotalTokens       int `json:"totalTokens"`
	InputTokenDetails struct {
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
		NoCacheTokens    int `json:"noCacheTokens"`
	} `json:"inputTokenDetails"`
	OutputTokenDetails struct {
		ReasoningTokens int `json:"reasoningTokens"`
		TextTokens      int `json:"textTokens"`
	} `json:"outputTokenDetails"`
}

// marshalGenerateBody renders the request body. Tests use it to pin the exact
// wire envelope (fields the endpoint's schema requires but Go would otherwise
// omit).
func marshalGenerateBody(req *GenerateRequest) []byte {
	b, _ := json.Marshal(req)
	return b
}
