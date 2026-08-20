package model

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

// NormalizeMessageRoles maps the OpenAI "developer" role back to "system".
//
// The o-series SDKs (and codex-cli's chat emitter) send the system prompt
// as role "developer" — the successor name OpenAI introduced for "system".
// Every other upstream we relay to (dashscope, volc, minimax, kimi,
// zhipu, anthropic-family converters, ...) only accepts the classic four
// roles and rejects the request with e.g.
//
//	The parameter `messages.role` ... invalid value: `developer`,
//	supported values are: `system`, `assistant`, `user`, `tool`
//
// OpenAI itself still accepts "system" on every chat model, so an
// unconditional mapping at the ingress is safe everywhere.
//
// Returns true if any role was rewritten.
func (r *GeneralOpenAIRequest) NormalizeMessageRoles() bool {
	if r == nil {
		return false
	}
	modified := false
	for i := range r.Messages {
		if r.Messages[i].Role == "developer" {
			r.Messages[i].Role = "system"
			modified = true
		}
	}
	return modified
}

// NormalizeMessageContentTypes renames Responses API content part types to
// their Chat Completions equivalents.
//
// The Responses API labels text parts "input_text"/"output_text"; the Chat
// Completions schema only knows "text"/"image_url"/... Requests converted
// from Responses to Chat (relayResponsesConvertToChat) carry the part types
// verbatim, and strict upstreams reject them, e.g. Volcano Ark coding v3
// (observed 2026-08-19):
//
//	The parameter `messages.content.type` ... invalid value: `input_text`,
//	supported values are: `text`, `image_url`, `video_url`, `input_audio`, `file`
//
// (Ark's chat endpoint accepts a "text"-typed parts array fine — only the
// Responses type names are rejected — so renaming is enough; no need to
// collapse text-only arrays to plain strings.)
//
// String content, non-part arrays and non-text parts (image_url, ...) are
// left untouched. Returns true if any part type was rewritten.
func (r *GeneralOpenAIRequest) NormalizeMessageContentTypes() bool {
	if r == nil {
		return false
	}
	modified := false
	for i := range r.Messages {
		parts, ok := r.Messages[i].Content.([]any)
		if !ok {
			continue
		}
		for _, part := range parts {
			obj, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch obj["type"] {
			case "input_text", "output_text":
				obj["type"] = "text"
				modified = true
			}
		}
	}
	return modified
}

// RepairToolCallFunctionNames gives assistant tool_calls with EMPTY
// function names a usable name, so replayed history passes upstream
// validation. Empty names reach the history the same way empty ids do:
// an upstream's `name: null` continuation fragments clobber the name
// captured from the first fragment, and clients persist and replay the
// damaged turns (observed: MiniMax rejecting with
// "invalid params, function name is empty (2013)").
//
// When the request carries exactly ONE tool, a nameless call is that
// tool with near certainty and inherits its name; otherwise a
// distinguishable placeholder (unknown_tool_<n>) is assigned — the name
// in replayed history is context for the model, not a dispatch target.
//
// Returns true if any names were rewritten.
func (r *GeneralOpenAIRequest) RepairToolCallFunctionNames() bool {
	if r == nil {
		return false
	}
	singleTool := ""
	if len(r.Tools) == 1 {
		singleTool = r.Tools[0].Function.Name
	}
	modified := false
	seq := 0
	for i := range r.Messages {
		for j := range r.Messages[i].ToolCalls {
			tc := &r.Messages[i].ToolCalls[j]
			if strings.TrimSpace(tc.Function.Name) == "" {
				seq++
				if singleTool != "" {
					tc.Function.Name = singleTool
				} else {
					tc.Function.Name = fmt.Sprintf("unknown_tool_%d", seq)
				}
				modified = true
			}
		}
	}
	return modified
}

// RepairToolCallIDs assigns synthetic ids to assistant tool_calls that carry
// EMPTY ids, and rewrites the paired tool responses' tool_call_ids so the
// call/response pairing survives. Without this, history replayed from clients
// that lost ids upstream rejects the whole request with
//
//	invalid params, duplicate tool_call id:  (2013)
//	missing messages.tool_calls.id parameter
//
// Empty ids reach clients when an upstream streams tool_call continuation
// fragments whose explicit `id: null` clobbers the id captured from the first
// fragment (observed: xiaomi mimo via one-api; the harness then persists and
// replays the empty ids). Repairing here also heals sessions whose logs
// already contain the corrupt turns.
//
// Pairing is positional: the tool messages immediately following an assistant
// message respond to its tool_calls in order, so the Nth empty tool_call_id
// receives the Nth synthetic id.
//
// Returns true if any ids were modified.
func (r *GeneralOpenAIRequest) RepairToolCallIDs() bool {
	if r == nil || len(r.Messages) == 0 {
		return false
	}
	modified := false
	seq := 0
	for i := range r.Messages {
		msg := &r.Messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		// Synthetic ids for this assistant message's empty tool_calls,
		// in call order, awaiting their paired tool responses.
		var pendingSynthetic []string
		for j := range msg.ToolCalls {
			if strings.TrimSpace(msg.ToolCalls[j].Id) == "" {
				seq++
				id := fmt.Sprintf("call_oneapi_%d", seq)
				msg.ToolCalls[j].Id = id
				pendingSynthetic = append(pendingSynthetic, id)
				modified = true
			}
		}
		if len(pendingSynthetic) == 0 {
			continue
		}
		// Rewrite empty tool_call_ids in the tool messages that follow,
		// consuming the synthetic ids in order.
		next := 0
		for k := i + 1; k < len(r.Messages) && r.Messages[k].Role == "tool"; k++ {
			toolMsg := &r.Messages[k]
			if strings.TrimSpace(toolMsg.ToolCallId) == "" && next < len(pendingSynthetic) {
				toolMsg.ToolCallId = pendingSynthetic[next]
				next++
				modified = true
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
//
//	invalid_request_error: The `reasoning_content` in the thinking mode must
//	be passed back to the API.
//
// We cannot recover the original hidden reasoning, so we inject a stable
// placeholder. Verified against opencode-go, zhipu GLM-5.2, xiaomi, volc and
// minimax: all accept the placeholder, and only *missing* reasoning_content
// is the trigger for the 400 (present-but-arbitrary is fine).
const ReasoningContentPlaceholder = "Tool execution in progress"

// NormalizeReasoningContent ensures every thinking-mode assistant message
// (one that carries tool_calls and an empty content) includes a
// `reasoning_content` field, unless the channel opted out.
//
// Echo collapse (always on): thinking upstreams ECHO the injected
// placeholder back as part of their own reasoning output ("<think>Tool
// execution in progress..."), clients persist that, and each subsequent
// turn replays and re-echoes it — measured snowballing to 745 repeats in
// one real session (oleworkstation01, 2026-08-20). Every placeholder
// occurrence is therefore stripped from incoming reasoning_content before
// the request is forwarded; content that consisted ONLY of placeholders is
// dropped so the injection below can restore exactly one copy when the
// channel requires it.
//
// skipInjection (channel config `skip_reasoning_injection`) disables the
// placeholder injection for channels that do not require reasoning_content
// back (verified: dashscope/volc/minimax/xiaomi/kimi accept turns without
// it). Set it on echo-prone channels to stop the placeholder from ever
// entering their context.
func (r *GeneralOpenAIRequest) NormalizeReasoningContent(skipInjection bool) bool {
	if r == nil {
		return false
	}
	modified := false
	for i := range r.Messages {
		msg := &r.Messages[i]
		if msg.Role != "assistant" {
			continue
		}
		if s, ok := msg.ReasoningContent.(string); ok {
			// A lone exact placeholder is already the canonical injected
			// form — leave it (keeps the pass idempotent).
			if s != ReasoningContentPlaceholder && strings.Contains(s, ReasoningContentPlaceholder) {
				collapsed := strings.TrimSpace(
					strings.ReplaceAll(s, ReasoningContentPlaceholder, ""))
				if collapsed == "" {
					msg.ReasoningContent = nil
				} else {
					msg.ReasoningContent = collapsed
				}
				modified = true
			}
		}
		if !skipInjection && len(msg.ToolCalls) > 0 && isEmptyReasoning(msg.ReasoningContent) {
			msg.ReasoningContent = ReasoningContentPlaceholder
			modified = true
		}
	}
	return modified
}

func isEmptyReasoning(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}
