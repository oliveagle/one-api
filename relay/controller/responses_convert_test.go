package controller

import (
	"encoding/json"
	"testing"

	"github.com/songquanpeng/one-api/relay/model"
)

// ---------------------------------------------------------------------------
// parseInputItems
// ---------------------------------------------------------------------------

func TestParseInputItems_Nil(t *testing.T) {
	items, err := parseInputItems(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if items != nil {
		t.Fatalf("expected nil, got %v", items)
	}
}

func TestParseInputItems_StringInput(t *testing.T) {
	items, err := parseInputItems("hello world")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 1 || items[0].Type != ResponseItemTypeMessage {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestParseInputItems_ArrayOfMessages(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "message", "role": "assistant", "content": "hello"},
	}
	items, err := parseInputItems(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for i, item := range items {
		if item.Type != ResponseItemTypeMessage {
			t.Errorf("item[%d] type = %q, want message", i, item.Type)
		}
	}
}

func TestParseInputItems_MixedTypes(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "sunny"},
	}
	raw, _ := json.Marshal(input)
	items, err := parseInputItems(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].Type != ResponseItemTypeMessage {
		t.Errorf("item[0] type = %q, want message", items[0].Type)
	}
	if items[1].Type != ResponseItemTypeFunctionCall {
		t.Errorf("item[1] type = %q, want function_call", items[1].Type)
	}
	if items[2].Type != ResponseItemTypeFunctionCallOutput {
		t.Errorf("item[2] type = %q, want function_call_output", items[2].Type)
	}
}

func TestParseInputItems_ReasoningType(t *testing.T) {
	input := []any{
		map[string]any{"type": "reasoning", "id": "r_1", "summary": []any{map[string]any{"type": "summary_text", "text": "thinking..."}}},
	}
	items, err := parseInputItems(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 1 || items[0].Type != ResponseItemTypeReasoning {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestParseInputItems_EmptyArray(t *testing.T) {
	input := []any{}
	items, err := parseInputItems(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty, got %v", items)
	}
}

func TestParseInputItems_InvalidJSON(t *testing.T) {
	_, err := parseInputItems(json.RawMessage(`{invalid`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// parseInputItemsDetailed
// ---------------------------------------------------------------------------

func TestParseInputItemsDetailed_String(t *testing.T) {
	items, err := parseInputItemsDetailed("hello")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	msg, ok := items[0].(ResponseMessageItem)
	if !ok {
		t.Fatalf("expected ResponseMessageItem, got %T", items[0])
	}
	if msg.Role != "user" || msg.Content != "hello" {
		t.Errorf("unexpected message: %+v", msg)
	}
}

func TestParseInputItemsDetailed_MessageItem(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "assistant", "content": "I think..."},
	}
	items, err := parseInputItemsDetailed(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	msg, ok := items[0].(ResponseMessageItem)
	if !ok {
		t.Fatalf("expected ResponseMessageItem, got %T", items[0])
	}
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
}

func TestParseInputItemsDetailed_FunctionCall(t *testing.T) {
	input := []any{
		map[string]any{"type": "function_call", "call_id": "c1", "name": "search", "arguments": `{"q":"test"}`},
	}
	items, err := parseInputItemsDetailed(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	fc, ok := items[0].(ResponseFunctionCallItem)
	if !ok {
		t.Fatalf("expected ResponseFunctionCallItem, got %T", items[0])
	}
	if fc.CallID != "c1" || fc.Name != "search" {
		t.Errorf("unexpected: %+v", fc)
	}
}

func TestParseInputItemsDetailed_FunctionCallOutput(t *testing.T) {
	input := []any{
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "result"},
	}
	items, err := parseInputItemsDetailed(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	fco, ok := items[0].(ResponseFunctionCallOutputItem)
	if !ok {
		t.Fatalf("expected ResponseFunctionCallOutputItem, got %T", items[0])
	}
	if fco.CallID != "c1" || fco.Output != "result" {
		t.Errorf("unexpected: %+v", fco)
	}
}

func TestParseInputItemsDetailed_Reasoning(t *testing.T) {
	input := []any{
		map[string]any{"type": "reasoning", "id": "r1", "summary": []any{map[string]any{"type": "summary_text", "text": "step 1"}}},
	}
	items, err := parseInputItemsDetailed(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	r, ok := items[0].(ResponseReasoningItem)
	if !ok {
		t.Fatalf("expected ResponseReasoningItem, got %T", items[0])
	}
	if r.ID != "r1" {
		t.Errorf("id = %q, want r1", r.ID)
	}
}

func TestParseInputItemsDetailed_UnknownType(t *testing.T) {
	input := []any{
		map[string]any{"type": "future_item", "data": "something"},
	}
	items, err := parseInputItemsDetailed(input)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 1 || items[0] != nil {
		t.Fatalf("expected nil for unknown type, got %v", items[0])
	}
}

// ---------------------------------------------------------------------------
// convertResponseItemToMessage
// ---------------------------------------------------------------------------

func TestConvertResponseItemToMessage_Nil(t *testing.T) {
	msgs, err := convertResponseItemToMessage(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected empty, got %v", msgs)
	}
}

func TestConvertResponseItemToMessage_Unsupported(t *testing.T) {
	_, err := convertResponseItemToMessage(42)
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
}

// ---------------------------------------------------------------------------
// convertMessageItem
// ---------------------------------------------------------------------------

func TestConvertMessageItem_UserText(t *testing.T) {
	item := ResponseMessageItem{
		Type:    ResponseItemTypeMessage,
		Role:    "user",
		Content: "hello",
	}
	msgs, err := convertMessageItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "user" {
		t.Errorf("role = %q, want user", msg.Role)
	}
	if msg.Content != "hello" {
		t.Errorf("content = %v, want hello", msg.Content)
	}
}

func TestConvertMessageItem_AssistantText(t *testing.T) {
	item := ResponseMessageItem{
		Type:    ResponseItemTypeMessage,
		Role:    "assistant",
		Content: "I can help with that.",
	}
	msgs, err := convertMessageItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", msgs[0].Role)
	}
}

func TestConvertMessageItem_SystemRole(t *testing.T) {
	item := ResponseMessageItem{
		Type:    ResponseItemTypeMessage,
		Role:    "system",
		Content: "You are a helpful assistant.",
	}
	msgs, err := convertMessageItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msgs[0].Role != "system" {
		t.Errorf("role = %q, want system", msgs[0].Role)
	}
}

func TestConvertMessageItem_ArrayContent(t *testing.T) {
	item := ResponseMessageItem{
		Type: ResponseItemTypeMessage,
		Role: "user",
		Content: []any{
			map[string]any{"type": "input_text", "text": "What is this?"},
		},
	}
	msgs, err := convertMessageItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// Content is preserved as-is (passthrough).
	contentArr, ok := msgs[0].Content.([]any)
	if !ok {
		t.Fatalf("expected content to be []any, got %T", msgs[0].Content)
	}
	if len(contentArr) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(contentArr))
	}
}

// ---------------------------------------------------------------------------
// convertFunctionCallItem
// ---------------------------------------------------------------------------

func TestConvertFunctionCallItem(t *testing.T) {
	item := ResponseFunctionCallItem{
		Type:      ResponseItemTypeFunctionCall,
		CallID:    "call_abc123",
		Name:      "get_weather",
		Arguments: `{"location":"SF"}`,
	}
	msgs, err := convertFunctionCallItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.Id != "call_abc123" {
		t.Errorf("tool call id = %q, want call_abc123", tc.Id)
	}
	if tc.Type != "function" {
		t.Errorf("tool call type = %q, want function", tc.Type)
	}
	if tc.Function.Name != "get_weather" {
		t.Errorf("function name = %q, want get_weather", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"location":"SF"}` {
		t.Errorf("arguments = %v, want {\"location\":\"SF\"}", tc.Function.Arguments)
	}
	// Assistant message for function call should have nil/empty content.
	if msg.Content != nil && msg.Content != "" {
		t.Errorf("content should be empty for function call, got %v", msg.Content)
	}
}

// ---------------------------------------------------------------------------
// convertFunctionCallOutputItem
// ---------------------------------------------------------------------------

func TestConvertFunctionCallOutputItem(t *testing.T) {
	item := ResponseFunctionCallOutputItem{
		Type:   ResponseItemTypeFunctionCallOutput,
		CallID: "call_abc123",
		Output: `{"temperature":72,"unit":"F"}`,
	}
	msgs, err := convertFunctionCallOutputItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "tool" {
		t.Errorf("role = %q, want tool", msg.Role)
	}
	if msg.ToolCallId != "call_abc123" {
		t.Errorf("tool_call_id = %q, want call_abc123", msg.ToolCallId)
	}
	if msg.Content != `{"temperature":72,"unit":"F"}` {
		t.Errorf("content = %v", msg.Content)
	}
}

// ---------------------------------------------------------------------------
// convertReasoningItem
// ---------------------------------------------------------------------------

func TestConvertReasoningItem_WithSummary(t *testing.T) {
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r_001",
		Summary: []any{
			map[string]any{"type": "summary_text", "text": "First, I think..."},
			map[string]any{"type": "summary_text", "text": "Then, I conclude..."},
		},
	}
	msgs, err := convertReasoningItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	reasoning, ok := msg.ReasoningContent.(string)
	if !ok {
		t.Fatalf("reasoning_content should be string, got %T", msg.ReasoningContent)
	}
	if reasoning != "First, I think...Then, I conclude..." {
		t.Errorf("reasoning_content = %q", reasoning)
	}
}

func TestConvertReasoningItem_WithStringSummary(t *testing.T) {
	item := ResponseReasoningItem{
		Type:    ResponseItemTypeReasoning,
		ID:      "r_002",
		Summary: "direct string summary",
	}
	msgs, err := convertReasoningItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	reasoning, ok := msgs[0].ReasoningContent.(string)
	if !ok {
		t.Fatalf("reasoning_content should be string, got %T", msgs[0].ReasoningContent)
	}
	if reasoning != "direct string summary" {
		t.Errorf("reasoning_content = %q", reasoning)
	}
}

func TestConvertReasoningItem_Empty(t *testing.T) {
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r_003",
	}
	msgs, err := convertReasoningItem(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("role = %q, want assistant", msgs[0].Role)
	}
	// Empty reasoning -> empty string.
	if msgs[0].ReasoningContent != "" {
		t.Errorf("reasoning_content = %v, want empty", msgs[0].ReasoningContent)
	}
}

// ---------------------------------------------------------------------------
// convertInstructionsToSystemMessage
// ---------------------------------------------------------------------------

func TestConvertInstructionsToSystemMessage_NonEmpty(t *testing.T) {
	msg := convertInstructionsToSystemMessage("You are a helpful assistant")
	if msg == nil {
		t.Fatal("expected non-nil message for non-empty instructions")
	}
	if msg.Role != "system" {
		t.Errorf("role = %q, want system", msg.Role)
	}
	if msg.Content != "You are a helpful assistant" {
		t.Errorf("content = %v", msg.Content)
	}
}

func TestConvertInstructionsToSystemMessage_Empty(t *testing.T) {
	if msg := convertInstructionsToSystemMessage(""); msg != nil {
		t.Fatalf("expected nil for empty instructions, got %+v", msg)
	}
}

func TestConvertInstructionsToSystemMessage_WhitespaceOnly(t *testing.T) {
	msg := convertInstructionsToSystemMessage("   ")
	if msg == nil {
		t.Fatal("expected non-nil message for whitespace-only instructions (still non-empty string)")
	}
	if msg.Role != "system" {
		t.Errorf("role = %q, want system", msg.Role)
	}
}

// ---------------------------------------------------------------------------
// convertResponsesInputToChatMessages (integration of all pieces)
// ---------------------------------------------------------------------------

func TestConvertResponsesInputToMessages_StringInputWithInstructions(t *testing.T) {
	msgs, err := convertResponsesInputToChatMessages("hello", "Be helpful")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Expect: [system, user]
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("msg[0] role = %q, want system", msgs[0].Role)
	}
	if msgs[0].Content != "Be helpful" {
		t.Errorf("msg[0] content = %v, want 'Be helpful'", msgs[0].Content)
	}
	if msgs[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want user", msgs[1].Role)
	}
	if msgs[1].Content != "hello" {
		t.Errorf("msg[1] content = %v, want 'hello'", msgs[1].Content)
	}
}

func TestConvertResponsesInputToMessages_ArrayInputNoInstructions(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "message", "role": "assistant", "content": "hello!"},
		map[string]any{"type": "message", "role": "user", "content": "bye"},
	}
	msgs, err := convertResponsesInputToChatMessages(input, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// No system message since instructions is empty.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" || msgs[2].Role != "user" {
		t.Errorf("roles = [%s, %s, %s], want [user, assistant, user]", msgs[0].Role, msgs[1].Role, msgs[2].Role)
	}
}

func TestConvertResponsesInputToMessages_FullConversation(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "What's the weather?"},
		map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"SF"}`},
		map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "Sunny, 72F"},
		map[string]any{"type": "message", "role": "assistant", "content": "It's sunny and 72F in SF!"},
	}
	msgs, err := convertResponsesInputToChatMessages(input, "You are a weather bot")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Expected: system + user + assistant(tool_calls) + tool + assistant(text) = 5
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("msg[0] role = %q, want system", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want user", msgs[1].Role)
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 {
		t.Errorf("msg[2] should be assistant with tool_calls, got role=%q tool_calls=%d", msgs[2].Role, len(msgs[2].ToolCalls))
	}
	if msgs[2].ToolCalls[0].Id != "call_1" {
		t.Errorf("tool call id = %q, want call_1", msgs[2].ToolCalls[0].Id)
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallId != "call_1" {
		t.Errorf("msg[3] should be tool with call_id=call_1, got role=%q tool_call_id=%q", msgs[3].Role, msgs[3].ToolCallId)
	}
	if msgs[3].Content != "Sunny, 72F" {
		t.Errorf("msg[3] content = %v, want 'Sunny, 72F'", msgs[3].Content)
	}
	if msgs[4].Role != "assistant" {
		t.Errorf("msg[4] role = %q, want assistant", msgs[4].Role)
	}
}

func TestConvertResponsesInputToMessages_WithReasoning(t *testing.T) {
	input := []any{
		map[string]any{"type": "reasoning", "id": "r1", "summary": []any{map[string]any{"type": "summary_text", "text": "Let me think..."}}},
		map[string]any{"type": "message", "role": "assistant", "content": "The answer is 42."},
	}
	msgs, err := convertResponsesInputToChatMessages(input, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "assistant" {
		t.Errorf("msg[0] role = %q, want assistant", msgs[0].Role)
	}
	if msgs[0].ReasoningContent != "Let me think..." {
		t.Errorf("msg[0] reasoning_content = %v", msgs[0].ReasoningContent)
	}
}

func TestConvertResponsesInputToMessages_NilInput(t *testing.T) {
	msgs, err := convertResponsesInputToChatMessages(nil, "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

func TestConvertResponsesInputToMessages_OnlyInstructions(t *testing.T) {
	msgs, err := convertResponsesInputToChatMessages(nil, "Be concise")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "Be concise" {
		t.Errorf("unexpected msg: %+v", msgs[0])
	}
}

// ---------------------------------------------------------------------------
// flattenContentToText
// ---------------------------------------------------------------------------

func TestFlattenContentToText_String(t *testing.T) {
	if got := flattenContentToText("hello"); got != "hello" {
		t.Errorf("got %q, want hello", got)
	}
}

func TestFlattenContentToText_ArrayBlocks(t *testing.T) {
	content := []any{
		map[string]any{"type": "summary_text", "text": "part1"},
		map[string]any{"type": "summary_text", "text": "part2"},
	}
	if got := flattenContentToText(content); got != "part1part2" {
		t.Errorf("got %q, want part1part2", got)
	}
}

func TestFlattenContentToText_ArrayWithNonTextBlocks(t *testing.T) {
	content := []any{
		map[string]any{"type": "image", "url": "http://example.com/img.png"},
		map[string]any{"type": "summary_text", "text": "visible"},
	}
	if got := flattenContentToText(content); got != "visible" {
		t.Errorf("got %q, want visible", got)
	}
}

func TestFlattenContentToText_EmptyArray(t *testing.T) {
	if got := flattenContentToText([]any{}); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestFlattenContentToText_Nil(t *testing.T) {
	if got := flattenContentToText(nil); got != "" {
		t.Errorf("got %q for nil, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Round-trip: JSON -> parse -> convert -> verify messages
// ---------------------------------------------------------------------------

func TestRoundTrip_JSONParseConvert(t *testing.T) {
	rawJSON := `{
		"model": "gpt-4o",
		"stream": false,
		"instructions": "You are a coding assistant.",
		"input": [
			{"type": "message", "role": "user", "content": "Write a hello world in Go"},
			{"type": "reasoning", "id": "r_1", "summary": [{"type": "summary_text", "text": "Need to use fmt.Println"}]},
			{"type": "message", "role": "assistant", "content": "package main\nimport \"fmt\"\nfunc main() { fmt.Println(\"Hello, World!\") }"},
			{"type": "message", "role": "user", "content": "Add a comment"},
			{"type": "function_call", "call_id": "call_x", "name": "edit_file", "arguments": "{\"path\":\"main.go\"}"},
			{"type": "function_call_output", "call_id": "call_x", "output": "File edited successfully"}
		]
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(rawJSON), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	msgs, err := convertResponsesInputToChatMessages(req.Input, req.Instructions)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	// system + user + reasoning(assistant) + assistant + user + assistant(tool_calls) + tool = 7
	if len(msgs) != 7 {
		t.Fatalf("expected 7 messages, got %d", len(msgs))
	}

	// Verify system message
	if msgs[0].Role != "system" || msgs[0].Content != "You are a coding assistant." {
		t.Errorf("msg[0]: role=%q content=%v", msgs[0].Role, msgs[0].Content)
	}
	// Verify first user message
	if msgs[1].Role != "user" || msgs[1].Content != "Write a hello world in Go" {
		t.Errorf("msg[1]: role=%q content=%v", msgs[1].Role, msgs[1].Content)
	}
	// Verify reasoning converted to assistant with reasoning_content
	if msgs[2].Role != "assistant" {
		t.Errorf("msg[2]: role=%q, want assistant", msgs[2].Role)
	}
	if msgs[2].ReasoningContent != "Need to use fmt.Println" {
		t.Errorf("msg[2] reasoning_content = %v", msgs[2].ReasoningContent)
	}
	// Verify assistant text message
	if msgs[3].Role != "assistant" {
		t.Errorf("msg[3]: role=%q, want assistant", msgs[3].Role)
	}
	// Verify second user message
	if msgs[4].Role != "user" || msgs[4].Content != "Add a comment" {
		t.Errorf("msg[4]: role=%q content=%v", msgs[4].Role, msgs[4].Content)
	}
	// Verify function call -> assistant with tool_calls
	if msgs[5].Role != "assistant" || len(msgs[5].ToolCalls) != 1 {
		t.Errorf("msg[5]: role=%q tool_calls=%d", msgs[5].Role, len(msgs[5].ToolCalls))
	}
	if msgs[5].ToolCalls[0].Function.Name != "edit_file" {
		t.Errorf("tool call name = %q", msgs[5].ToolCalls[0].Function.Name)
	}
	// Verify function call output -> tool message
	if msgs[6].Role != "tool" || msgs[6].ToolCallId != "call_x" {
		t.Errorf("msg[6]: role=%q tool_call_id=%q", msgs[6].Role, msgs[6].ToolCallId)
	}
}

// Ensure unused import doesn't break compilation.
var _ = model.Message{}
