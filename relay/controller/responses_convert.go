package controller

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
//
// Content holds the reasoning content blocks (e.g. type "reasoning_text").
// EncryptedContent carries an opaque encrypted payload that some providers
// emit instead of plaintext content.
type ResponseReasoningItem struct {
	Type             ResponseItemType `json:"type"`
	ID               string           `json:"id,omitempty"`
	Summary          any              `json:"summary,omitempty"`
	Content          any              `json:"content,omitempty"`
	EncryptedContent string           `json:"encrypted_content,omitempty"`
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
// Role is passed through as-is (user, assistant, system); content blocks
// are flattened onto the Chat vocabulary (see flattenResponsesContent) —
// the Responses block types must NOT leak into the chat wire format.
func convertMessageItem(item ResponseMessageItem) ([]relaymodel.Message, error) {
	msg := relaymodel.Message{
		Role:    item.Role,
		Content: flattenResponsesContent(item.Content),
	}
	return []relaymodel.Message{msg}, nil
}

// flattenResponsesContent maps Responses content blocks onto the Chat
// Completions content vocabulary. The Responses API carries content as
// typed blocks (input_text / output_text / input_image); chat requests
// only accept a plain string or text/image_url parts — strict upstreams
// (xiaomi mimo and friends) reject Responses-shaped blocks
// (`type:"input_text"`) with a 400 and a non-OpenAI error body.
//
// Text-only content collapses to a plain string (universally accepted);
// content containing images becomes chat-format parts.
func flattenResponsesContent(content any) any {
	blocks, ok := content.([]any)
	if !ok {
		// Already a plain string or another passthrough shape.
		return content
	}
	var parts []map[string]any
	hasImage := false
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "input_text", "text", "output_text", "summary_text":
			parts = append(parts, map[string]any{"type": "text", "text": m["text"]})
		case "input_image":
			hasImage = true
			url := m["image_url"]
			if u, ok := url.(map[string]any); ok {
				url = u["url"]
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
	}
	if !hasImage {
		var sb strings.Builder
		for _, p := range parts {
			if t, ok := p["text"].(string); ok {
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return parts
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

// ReasoningTextBlockType is the content block type that carries plaintext
// reasoning in a Reasoning item's content array.
const ReasoningTextBlockType = "reasoning_text"

// extractReasoningContent extracts the reasoning text from a Responses API
// Reasoning item for the reasoning_content field.
//
// The plaintext reasoning lives in the item's content array under blocks of
// type "reasoning_text"; their text fields are concatenated in order. When the
// content array yields no text, the opaque encrypted_content payload is used as
// a fallback so the reasoning turn is still visible to the upstream.
//
// Returns an error if the item is not a Reasoning item. Returns an empty
// string when no reasoning text is available.
func extractReasoningContent(item ResponseReasoningItem) (string, error) {
	if item.Type != ResponseItemTypeReasoning {
		return "", fmt.Errorf("item is not a reasoning item: %q", item.Type)
	}

	if text := flattenReasoningTextBlocks(item.Content); text != "" {
		return text, nil
	}
	if item.EncryptedContent != "" {
		return item.EncryptedContent, nil
	}
	return "", nil
}

// flattenReasoningTextBlocks concatenates the text of every content block of
// type ReasoningTextBlockType. Content may be a plain string, a single content
// block object, or an array of content blocks. Non-reasoning blocks (e.g.
// "summary_text") are ignored.
func flattenReasoningTextBlocks(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var text string
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				if part := textFromBlock(m); part != "" {
					text += part
				}
			}
		}
		return text
	default:
		if block, ok := content.(map[string]any); ok {
			return textFromBlock(block)
		}
		// Marshal back to JSON to handle json.RawMessage or other types.
		raw, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		var block map[string]any
		if err := json.Unmarshal(raw, &block); err == nil {
			return textFromBlock(block)
		}
		return ""
	}
}

// textFromBlock returns the text of a single content block if it is of type
// ReasoningTextBlockType, otherwise an empty string.
func textFromBlock(block map[string]any) string {
	if block == nil {
		return ""
	}
	if t, ok := block["type"].(string); ok && t == ReasoningTextBlockType {
		if text, ok := block["text"].(string); ok {
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

// injectReasoningContent injects the extracted reasoning text into the last
// assistant message in the messages slice and normalizes all thinking-mode
// turns.
//
// The function performs three steps:
//
//  1. Find the last role=assistant message. If reasoning is non-empty, its
//     reasoning_content field is set to the reasoning text.
//
//  2. Delegate to NormalizeReasoningContent which walks every assistant message
//     with tool_calls and, if reasoning_content is still nil, injects
//     ReasoningContentPlaceholder ("Tool execution in progress") so upstreams
//     that require thinking-mode back-pass never reject the request.
//
//  3. Returns true if any message was modified (either by the injection or by
//     NormalizeReasoningContent).
//
// The pointer-to-slice signature allows in-place modification of the caller's
// message array without copying.
func injectReasoningContent(reasoning string, messages *[]relaymodel.Message) bool {
	if messages == nil || len(*messages) == 0 {
		return false
	}

	modified := false

	// Step 1: find the last assistant message and inject reasoning_content.
	lastAssistantIdx := -1
	for i := len(*messages) - 1; i >= 0; i-- {
		if (*messages)[i].Role == role.Assistant {
			lastAssistantIdx = i
			break
		}
	}
	if lastAssistantIdx == -1 {
		return false
	}

	if reasoning != "" {
		msg := &(*messages)[lastAssistantIdx]
		if msg.ReasoningContent == nil {
			msg.ReasoningContent = reasoning
			modified = true
		}
	}

	// Step 2: normalize all thinking-mode turns using the existing helper.
	// A thinking turn is an assistant message with tool_calls and missing
	// reasoning_content; the helper injects the placeholder.
	tmpReq := &relaymodel.GeneralOpenAIRequest{Messages: *messages}
	if tmpReq.NormalizeReasoningContent() {
		modified = true
	}

	return modified
}

// convertInstructionsToSystemMessage converts the Responses API `instructions`
// field into a Chat Completions system message. The instructions carry the
// top-level developer/system directive for the whole request.
//
// Returns nil when instructions is empty: no system message should be emitted
// for an absent directive, keeping the converted message array clean.
func convertInstructionsToSystemMessage(instructions string) *relaymodel.Message {
	if instructions == "" {
		return nil
	}
	return &relaymodel.Message{
		Role:    role.System,
		Content: instructions,
	}
}

// generateMessageID generates a unique message identifier of the form
// "msg_" followed by 24 random hexadecimal characters (96 bits of entropy),
// which is plenty to guarantee collision-free IDs within a single request.
//
// crypto/rand is used so the values are cryptographically unpredictable. In the
// astronomically unlikely event the random source fails, a timestamp-based
// fallback keeps the ID unique on a per-call basis.
func generateMessageID() string {
	var b [12]byte // 12 bytes -> 24 hex characters
	if _, err := rand.Read(b[:]); err == nil {
		return "msg_" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("msg_%024x", time.Now().UnixNano())
}

// ensureMessageIDs walks the message list and assigns a freshly generated
// unique ID to every message that does not already carry one. Messages with an
// existing ID are left untouched.
//
// Uniqueness is enforced within the whole request: the function tracks every
// seen ID (both pre-existing and freshly generated) and retries generation on
// the unlikely event of a collision, so no two messages in the request ever
// share an ID.
func ensureMessageIDs(messages []*relaymodel.Message) {
	seen := make(map[string]struct{}, len(messages))
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.ID != "" {
			seen[msg.ID] = struct{}{}
			continue
		}

		// Generate until we get one not already used in this request.
		for {
			id := generateMessageID()
			if _, dup := seen[id]; !dup {
				msg.ID = id
				seen[id] = struct{}{}
				break
			}
		}
	}
}

// convertResponsesInputToChatMessages converts a Responses API request's input
// and instructions fields into a slice of Chat Completions messages. This is
// the main entry point for Responses -> Chat conversion.
//
// The instructions field, if non-empty, becomes a system message prepended to
// the result (always first in the array). The input array is converted item by
// item and appended after it.
func convertResponsesInputToChatMessages(input any, instructions string) ([]relaymodel.Message, error) {
	detailed, err := parseInputItemsDetailed(input)
	if err != nil {
		return nil, fmt.Errorf("parse input items: %w", err)
	}

	var messages []relaymodel.Message

	// instructions -> system message, always at the head of the array
	if sys := convertInstructionsToSystemMessage(instructions); sys != nil {
		messages = append(messages, *sys)
	}

	for i, item := range detailed {
		msgs, err := convertResponseItemToMessage(item)
		if err != nil {
			return nil, fmt.Errorf("item[%d]: %w", i, err)
		}
		messages = append(messages, msgs...)
	}

	// Assign a unique ID to every message that does not already carry one.
	// The pointer slice aliases the value slice in place, so the generated
	// IDs are visible on the returned messages with no copy.
	ptrs := make([]*relaymodel.Message, len(messages))
	for i := range messages {
		ptrs[i] = &messages[i]
	}
	ensureMessageIDs(ptrs)

	return messages, nil
}

// convertResponsesToChatCompletions converts a Responses API request into a
// Chat Completions request. It is the integration entry point used when the
// selected upstream does not natively implement the Responses API (e.g. an
// opencode-go channel): the Responses `input` / `instructions` fields are
// translated into a `messages` array that any Chat Completions endpoint
// understands.
//
// The conversion pipeline, in order:
//
//  1. parseInputItems      - decode the input array into typed ResponseItems
//  2. convertInstructionsToSystemMessage - prepend a system message
//  3. extractReasoningContent - collect reasoning text from reasoning items
//  4. injectReasoningContent  - place reasoning on the assistant turn and
//     normalise every thinking-mode turn
//  5. ensureMessageIDs      - assign a unique id to every message
//
// Model and stream are copied across unchanged; the caller is responsible for
// model-name mapping and billing, which the chat pipeline handles identically.
func convertResponsesToChatCompletions(request *ResponsesRequest) (*relaymodel.GeneralOpenAIRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("nil ResponsesRequest")
	}

	// 1. parseInputItems - decode the input array into typed items.
	items, err := parseInputItemsDetailed(request.Input)
	if err != nil {
		return nil, fmt.Errorf("parse input items: %w", err)
	}

	// 2. convertInstructionsToSystemMessage - system message first.
	messages := make([]relaymodel.Message, 0, len(items)+1)
	if sys := convertInstructionsToSystemMessage(request.Instructions); sys != nil {
		messages = append(messages, *sys)
	}

	// 3. extractReasoningContent - reasoning items contribute their text but
	// are not emitted as standalone messages; the text is injected into the
	// assistant turn that follows them.
	var reasoning string
	for i, item := range items {
		r, isReasoning := item.(ResponseReasoningItem)
		if !isReasoning {
			msgs, err := convertResponseItemToMessage(item)
			if err != nil {
				return nil, fmt.Errorf("item[%d]: %w", i, err)
			}
			messages = append(messages, msgs...)
			continue
		}
		if text, err := extractReasoningContent(r); err == nil && text != "" {
			reasoning = text
		}
	}

	// 4. injectReasoningContent - place the extracted reasoning on the last
	// assistant message (e.g. the tool-call turn), and normalise every
	// thinking-mode turn. If no assistant message exists to receive the text,
	// emit a standalone assistant message carrying it so it is never lost.
	if !injectReasoningContent(reasoning, &messages) && reasoning != "" {
		messages = append(messages, relaymodel.Message{
			Role:             role.Assistant,
			ReasoningContent: reasoning,
		})
	}

	// 5. ensureMessageIDs - every message carries a unique id.
	ptrs := make([]*relaymodel.Message, len(messages))
	for i := range messages {
		ptrs[i] = &messages[i]
	}
	ensureMessageIDs(ptrs)

	return &relaymodel.GeneralOpenAIRequest{
		Model:      request.Model,
		Stream:     request.Stream,
		Messages:   messages,
		Tools:      request.Tools,
		ToolChoice: request.ToolChoice,
	}, nil
}
