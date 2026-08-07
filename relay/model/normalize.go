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
// Returns true if any arguments were modified.
func (r *GeneralOpenAIRequest) NormalizeToolCallArguments() bool {
	if r == nil {
		return false
	}
	modified := false
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
					modified = true
				}
			}
		}
	}
	return modified
}

// RepairOrphanedToolCalls fixes conversation histories where an assistant
// message contains tool_calls but not all of them have corresponding
// role="tool" response messages. Upstream providers (OpenAI, Azure, etc.)
// reject such requests with:
//   "an assistant message with 'tool_calls' must be followed by tool messages
//    responding to each 'tool_call_id'"
//
// For each missing tool_call_id, this function inserts a synthetic tool
// response message after all existing tool responses for that assistant message.
// The synthetic response has content "Tool execution was not recorded" so the
// upstream accepts the request while the model understands the tool was not
// actually executed.
//
// Returns true if any synthetic tool responses were inserted.
func (r *GeneralOpenAIRequest) RepairOrphanedToolCalls() bool {
	if r == nil || len(r.Messages) == 0 {
		return false
	}

	modified := false
	repaired := make([]Message, 0, len(r.Messages))
	i := 0

	for i < len(r.Messages) {
		msg := r.Messages[i]
		repaired = append(repaired, msg)

		// Check if this is an assistant message with tool_calls
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Collect all tool_call_ids that need responses (preserve order)
			var neededIDs []string
			for _, tc := range msg.ToolCalls {
				if tc.Id != "" {
					neededIDs = append(neededIDs, tc.Id)
				}
			}

			// Consume all following tool responses
			i++
			for i < len(r.Messages) && r.Messages[i].Role == "tool" {
				toolMsg := r.Messages[i]
				repaired = append(repaired, toolMsg)
				i++
			}

			// Build set of responded IDs from consumed tool messages
			respondedIDs := make(map[string]bool)
			toolStart := len(repaired) - 1
			for toolStart >= 0 && repaired[toolStart].Role == "tool" {
				respondedIDs[repaired[toolStart].ToolCallId] = true
				toolStart--
			}

			// Insert synthetic responses for any missing IDs (in order)
			for _, id := range neededIDs {
				if !respondedIDs[id] {
					syntheticToolMsg := Message{
						Role:       "tool",
						Content:    "Tool execution was not recorded",
						ToolCallId: id,
					}
					repaired = append(repaired, syntheticToolMsg)
					modified = true
				}
			}

			// Don't increment i again since we already did in the loop
			continue
		}

		i++
	}

	if modified {
		r.Messages = repaired
	}
	return modified
}
