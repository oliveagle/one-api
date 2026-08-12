package controller

import (
	"encoding/json"
	"fmt"

	"github.com/songquanpeng/one-api/relay/constant/role"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// ResponseItemType enumerates the known item types in the Responses API input array.
type ResponseItemType string

const (
	ResponseItemTypeMessage            ResponseItemType = "message"
	ResponseItemTypeFunctionCall       ResponseItemType = "function_call"
	ResponseItemTypeFunctionCallOutput ResponseItemType = "function_call_output"
	ResponseItemTypeReasoning          ResponseItemType = "reasoning"
)

// ResponseItem is the common envelope for every element in the Responses API
// input array. The Type field determines which concrete shape to decode into.
type ResponseItem struct {
	Type ResponseItemType `json:"type"`
}

// ResponseMessageItem represents a message in the Responses API input.
// Role is "user", "assistant", or "system". Content can be a string or
// an array of content blocks (text, image, etc.).
type ResponseMessageItem struct {
	Type    ResponseItemType `json:"type"`
	Role    string           `json:"role"`
	Content any              `json:"content"`
}

// ResponseFunctionCallItem represents a function_call item produced by the model.
type ResponseFunctionCallItem struct {
	Type      ResponseItemType `json:"type"`
	ID        string           `json:"id,omitempty"`
	CallID    string           `json:"call_id"`
	Name      string           `json:"name"`
	Arguments string           `json:"arguments"`
}

// ResponseFunctionCallOutputItem represents the output of a function call,
// supplied by the caller to feed back into the conversation.
type ResponseFunctionCallOutputItem struct {
	Type   ResponseItemType `json:"type"`
	CallID string           `json:"call_id"`
	Output string           `json:"output"`
}

// ResponseReasoningItem represents an internal reasoning step. Reasoning items
// are converted to assistant messages with reasoning_content populated.
type ResponseReasoningItem struct {
	Type    ResponseItemType `json:"type"`
	ID      string           `json:"id,omitempty"`
	Summary any              `json:"summary,omitempty"`
	Content any              `json:"content,omitempty"`
}

// parseInputItems parses the `input` field of a Responses API request into a
// typed slice of ResponseItem variants. The input field accepts either:
//   - a plain string (treated as a single user message)
//   - an array of ResponseItem objects
//
// Returns an empty slice (not nil) for empty input.
func parseInputItems(input any) ([]ResponseItem, error) {
	if input == nil {
		return nil, nil
	}

	// The input field may arrive as a raw JSON message (json.RawMessage) because
	// the parent request struct decodes `Input` as `any`, or as an already-decoded
	// Go value. Normalize to bytes first.
	var raw []byte
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case string:
		// A plain string input is a single user message; wrap it.
		return []ResponseItem{{Type: ResponseItemTypeMessage}}, nil
	default:
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("marshal input: %w", err)
		}
	}

	// Try to unmarshal as an array of raw items.
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		// Not an array — try as a single string (quoted JSON string).
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 == nil {
			return []ResponseItem{{Type: ResponseItemTypeMessage}}, nil
		}
		return nil, fmt.Errorf("input is neither a string nor an array: %w", err)
	}

	items := make([]ResponseItem, 0, len(rawItems))
	for i, rawItem := range rawItems {
		var probe ResponseItem
		if err := json.Unmarshal(rawItem, &probe); err != nil {
			return nil, fmt.Errorf("item[%d]: unmarshal type: %w", i, err)
		}
		items = append(items, ResponseItem{Type: probe.Type})
	}
	return items, nil
}

// parseInputItemsDetailed parses the input field and returns fully decoded
// items. Unlike parseInputItems (which only extracts the type), this returns
// the concrete variant structs for conversion.
func parseInputItemsDetailed(input any) ([]any, error) {
	if input == nil {
		return nil, nil
	}

	var raw []byte
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case string:
		msg := ResponseMessageItem{
			Type:    ResponseItemTypeMessage,
			Role:    "user",
			Content: v,
		}
		return []any{msg}, nil
	default:
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("marshal input: %w", err)
		}
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 == nil {
			msg := ResponseMessageItem{
				Type:    ResponseItemTypeMessage,
				Role:    "user",
				Content: s,
			}
			return []any{msg}, nil
		}
		return nil, fmt.Errorf("input is neither a string nor an array: %w", err)
	}

	items := make([]any, 0, len(rawItems))
	for i, rawItem := range rawItems {
		var probe ResponseItem
		if err := json.Unmarshal(rawItem, &probe); err != nil {
			return nil, fmt.Errorf("item[%d]: unmarshal type: %w", i, err)
		}
		switch probe.Type {
		case ResponseItemTypeMessage:
			var msg ResponseMessageItem
			if err := json.Unmarshal(rawItem, &msg); err != nil {
				return nil, fmt.Errorf("item[%d]: unmarshal message: %w", i, err)
			}
			items = append(items, msg)
		case ResponseItemTypeFunctionCall:
			var fc ResponseFunctionCallItem
			if err := json.Unmarshal(rawItem, &fc); err != nil {
				return nil, fmt.Errorf("item[%d]: unmarshal function_call: %w", i, err)
			}
			items = append(items, fc)
		case ResponseItemTypeFunctionCallOutput:
			var fco ResponseFunctionCallOutputItem
			if err := json.Unmarshal(rawItem, &fco); err != nil {
				return nil, fmt.Errorf("item[%d]: unmarshal function_call_output: %w", i, err)
			}
			items = append(items, fco)
		case ResponseItemTypeReasoning:
			var r ResponseReasoningItem
			if err := json.Unmarshal(rawItem, &r); err != nil {
				return nil, fmt.Errorf("item[%d]: unmarshal reasoning: %w", i, err)
			}
			items = append(items, r)
		default:
			// Unknown types are skipped with a nil placeholder so array
			// indices stay aligned with the original input.
			items = append(items, nil)
		}
	}
	return items, nil
}

// convertResponseItemToMessage converts a single Responses API item into one
// or more Chat Completions messages. Most items map to exactly one message;
// FunctionCallOutput always produces a role=tool message.
func convertResponseItemToMessage(item any) ([]relaymodel.Message, error) {
	switch v := item.(type) {
	case ResponseMessageItem:
		return convertMessageItem(v)
	case ResponseFunctionCallItem:
		return convertFunctionCallItem(v)
	case ResponseFunctionCallOutputItem:
		return convertFunctionCallOutputItem(v)
	case ResponseReasoningItem:
		return convertReasoningItem(v)
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported item type %T", v)
	}
}

// convertMessageItem converts a ResponseMessageItem to a Chat message.
// Role is passed through as-is (user, assistant, system).
func convertMessageItem(item ResponseMessageItem) ([]relaymodel.Message, error) {
	msg := relaymodel.Message{
		Role:    item.Role,
		Content: item.Content,
	}
	return []relaymodel.Message{msg}, nil
}

// convertFunctionCallItem converts a ResponseFunctionCallItem to an assistant
// message with a single tool_calls entry. The assistant message has empty
// content (the Responses API emits tool calls without surrounding text).
func convertFunctionCallItem(item ResponseFunctionCallItem) ([]relaymodel.Message, error) {
	msg := relaymodel.Message{
		Role: role.Assistant,
		ToolCalls: []relaymodel.Tool{
			{
				Id:   item.CallID,
				Type: "function",
				Function: relaymodel.Function{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
			},
		},
	}
	return []relaymodel.Message{msg}, nil
}

// convertFunctionCallOutputItem converts a ResponseFunctionCallOutputItem to a
// role=tool message. The CallID becomes the tool_call_id that links back to the
// corresponding tool_calls entry.
func convertFunctionCallOutputItem(item ResponseFunctionCallOutputItem) ([]relaymodel.Message, error) {
	msg := relaymodel.Message{
		Role:       "tool",
		Content:    item.Output,
		ToolCallId: item.CallID,
	}
	return []relaymodel.Message{msg}, nil
}

// convertReasoningItem converts a ResponseReasoningItem to an assistant message
// with reasoning_content populated. The text representation is derived from the
// summary field if present; otherwise a placeholder is used.
func convertReasoningItem(item ResponseReasoningItem) ([]relaymodel.Message, error) {
	msg := relaymodel.Message{
		Role:             role.Assistant,
		ReasoningContent: extractReasoningText(item),
	}
	return []relaymodel.Message{msg}, nil
}

// extractReasoningText produces a string representation of a reasoning item's
// summary or content for the reasoning_content field. Returns an empty string
// if neither is available.
func extractReasoningText(item ResponseReasoningItem) string {
	// Try summary first — it's the primary text field for reasoning items.
	if item.Summary != nil {
		if text := flattenContentToText(item.Summary); text != "" {
			return text
		}
	}
	if item.Content != nil {
		if text := flattenContentToText(item.Content); text != "" {
			return text
		}
	}
	return ""
}

// flattenContentToText extracts concatenated text from a content value that
// may be a plain string, a single content block, or an array of content blocks.
func flattenContentToText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					text += t
				}
			}
		}
		return text
	default:
		// Marshal back to JSON to handle json.RawMessage or other types.
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return string(raw)
	}
}

// convertResponsesInputToMessages converts a Responses API request's input and
// instructions fields into a slice of Chat Completions messages. This is the
// main entry point for Responses -> Chat conversion.
//
// The instructions field, if non-empty, becomes a system message prepended to
// the result. The input array is converted item by item.
func convertResponsesInputToMessages(input any, instructions string) ([]relaymodel.Message, error) {
	detailed, err := parseInputItemsDetailed(input)
	if err != nil {
		return nil, fmt.Errorf("parse input items: %w", err)
	}

	var messages []relaymodel.Message

	// instructions -> system message
	if instructions != "" {
		messages = append(messages, relaymodel.Message{
			Role:    role.System,
			Content: instructions,
		})
	}

	for i, item := range detailed {
		msgs, err := convertResponseItemToMessage(item)
		if err != nil {
			return nil, fmt.Errorf("item[%d]: %w", i, err)
		}
		messages = append(messages, msgs...)
	}

	return messages, nil
}
