package model

import "encoding/json"

// NormalizeToolCallArguments coerces every tool_calls[].function.arguments in
// the request to a JSON string, as the OpenAI spec requires.
//
// Some clients (notably the codex-cli 0.142 chat emitter)
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

// RepairOrphanedToolCalls fixes conversation histories where tool calls and
// tool responses are mismatched. It handles two cases:
//
//  1. Missing tool responses: an assistant message contains tool_calls but not
//     all of them have corresponding role="tool" response messages. Upstream
//     providers reject such requests with:
//     "an assistant message with 'tool_calls' must be followed by tool messages
//     responding to each 'tool_call_id'"
//     For each missing tool_call_id, a synthetic tool response is inserted.
//
//  2. Orphaned tool responses: a tool message references a tool_call_id that
//     does not exist in any preceding assistant message. Upstream providers
//     (notably Kimi/Moonshot) reject such requests with:
//     "tool call id <id> is not found"
//     Orphaned tool responses are removed.
//
// Returns true if any modifications were made.
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

	// Second pass: remove orphaned tool responses whose tool_call_id does not
	// match any assistant tool_call in the conversation. This handles the case
	// where codex-cli sends a tool response for an ID that was never issued by
	// an assistant message (e.g. after context truncation or session resume).
	// Upstream providers like Kimi reject these with "tool call id X is not found".
	cleaned := make([]Message, 0, len(repaired))

	// First, build the set of all valid tool_call_ids from assistant messages
	validToolCallIDs := make(map[string]bool)
	for _, msg := range repaired {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tc.Id != "" {
					validToolCallIDs[tc.Id] = true
				}
			}
		}
	}

	// Then filter out tool messages referencing unknown IDs
	for _, msg := range repaired {
		if msg.Role == "tool" && msg.ToolCallId != "" && !validToolCallIDs[msg.ToolCallId] {
			modified = true
			continue // skip this orphaned tool response
		}
		cleaned = append(cleaned, msg)
	}

	if modified {
		r.Messages = cleaned
	}
	return modified
}

// RemoveOrphanedToolResponses is a convenience wrapper that returns the count
// of orphaned tool responses removed. Useful for logging.
func (r *GeneralOpenAIRequest) CountOrphanedToolResponses() int {
	if r == nil || len(r.Messages) == 0 {
		return 0
	}

	validToolCallIDs := make(map[string]bool)
	for _, msg := range r.Messages {
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				if tc.Id != "" {
					validToolCallIDs[tc.Id] = true
				}
			}
		}
	}

	count := 0
	for _, msg := range r.Messages {
		if msg.Role == "tool" && msg.ToolCallId != "" && !validToolCallIDs[msg.ToolCallId] {
			count++
		}
	}
	return count
}

// ReasoningContentPlaceholder is injected into assistant messages that made
// tool calls but were emitted without a `reasoning_content` field.
//
// Background: thinking/reasoning-capable upstreams (DeepSeek-V4-Flash on
// Console Go / opencode-go, GLM-5.2, etc.) require that every "thinking mode"
// assistant message carry a `reasoning_content` back to the API. A thinking
// turn is an assistant message whose content is empty and which issued
// tool_calls. When a client (e.g. the codex-cli chat emitter) drops the
// reasoning_content for such a turn, the upstream rejects the whole request
// with:
//   invalid_request_error: The `reasoning_content` in the thinking mode must
//   be passed back to the API.
//
// We cannot recover the original hidden reasoning, so we inject a stable
// placeholder. Verified against opencode-go, zhipu GLM-5.2, xiaomi, volc and
// minimax: all accept the placeholder, and only *missing* reasoning_content
// is the trigger for the 400 (present-but-arbitrary is fine).
const ReasoningContentPlaceholder = "Tool execution in progress"

// NormalizeReasoningContent ensures every thinking-mode assistant message
// (one that carries tool_calls and an empty content) includes a
// `reasoning_content` field. Returns true if any message was modified.
func (r *GeneralOpenAIRequest) NormalizeReasoningContent() bool {
	if r == nil {
		return false
	}
	modified := false
	for i := range r.Messages {
		msg := &r.Messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		hasReasoning := msg.ReasoningContent != nil
		if !hasReasoning {
			msg.ReasoningContent = ReasoningContentPlaceholder
			modified = true
		}
	}
	return modified
}
