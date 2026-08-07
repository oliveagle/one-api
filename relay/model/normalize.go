package model

import "encoding/json"

// NormalizeToolCallArguments coerces every tool_calls[].function.arguments in
// the request to a JSON string, as the OpenAI spec requires.
//
// Some clients (notably the codex-cli 0.142 chat-completions emitter)
// serialise function.arguments as a JSON *object*. Every upstream we relay to
// (dashscope, volc, minimax, ollama, ...) rejects that with a 400, so one-api
// normalises once at the ingress so all upstreams work.
//
// Existing string values are left untouched; nil is left as-is.
func (r *GeneralOpenAIRequest) NormalizeToolCallArguments() {
	if r == nil {
		return
	}
	for i := range r.Messages {
		msg := &r.Messages[i]
		for j := range msg.ToolCalls {
			args := msg.ToolCalls[j].Function.Arguments
			switch args.(type) {
			case nil, string:
				// already valid
			default:
				if b, err := json.Marshal(args); err == nil {
					msg.ToolCalls[j].Function.Arguments = string(b)
				}
			}
		}
	}
}
