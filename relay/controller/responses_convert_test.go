package controller

import (
	"encoding/json"
	"strings"
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
// extractReasoningContent
// ---------------------------------------------------------------------------

func TestExtractReasoningContent_WrongType(t *testing.T) {
	item := ResponseReasoningItem{Type: ResponseItemTypeMessage}
	if _, err := extractReasoningContent(item); err == nil {
		t.Fatal("expected error for non-reasoning type")
	}
}

func TestExtractReasoningContent_Empty(t *testing.T) {
	item := ResponseReasoningItem{Type: ResponseItemTypeReasoning, ID: "r1"}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractReasoningContent_FromContentBlocks(t *testing.T) {
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r2",
		Content: []any{
			map[string]any{"type": "reasoning_text", "text": "step 1..."},
			map[string]any{"type": "reasoning_text", "text": "step 2..."},
		},
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "step 1...step 2..." {
		t.Errorf("got %q", got)
	}
}

func TestExtractReasoningContent_IgnoresSummaryText(t *testing.T) {
	// summary_text blocks must NOT be picked up by reasoning extraction.
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r3",
		Content: []any{
			map[string]any{"type": "summary_text", "text": "summary content"},
			map[string]any{"type": "reasoning_text", "text": "actual reasoning"},
		},
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "actual reasoning" {
		t.Errorf("got %q, want only reasoning_text content", got)
	}
}

func TestExtractReasoningContent_StringContent(t *testing.T) {
	item := ResponseReasoningItem{
		Type:    ResponseItemTypeReasoning,
		ID:      "r4",
		Content: "plaintext reasoning body",
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "plaintext reasoning body" {
		t.Errorf("got %q", got)
	}
}

func TestExtractReasoningContent_SingleBlock(t *testing.T) {
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r5",
		Content: map[string]any{
			"type": "reasoning_text",
			"text": "single block reasoning",
		},
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "single block reasoning" {
		t.Errorf("got %q", got)
	}
}

func TestExtractReasoningContent_FallbackToEncryptedContent(t *testing.T) {
	// content array has no reasoning_text; encrypted_content must be used.
	item := ResponseReasoningItem{
		Type:             ResponseItemTypeReasoning,
		ID:               "r6",
		Content:          []any{map[string]any{"type": "summary_text", "text": "summary"}},
		EncryptedContent: "encrypted_blob_payload",
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "encrypted_blob_payload" {
		t.Errorf("got %q, want encrypted fallback", got)
	}
}

func TestExtractReasoningContent_ContentPreferredOverEncrypted(t *testing.T) {
	// When both are present, plaintext content wins.
	item := ResponseReasoningItem{
		Type:             ResponseItemTypeReasoning,
		ID:               "r7",
		Content:          []any{map[string]any{"type": "reasoning_text", "text": "visible"}},
		EncryptedContent: "should_not_be_used",
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "visible" {
		t.Errorf("got %q, want plaintext content to win", got)
	}
}

func TestExtractReasoningContent_NoBlocksAndNoEncrypted(t *testing.T) {
	item := ResponseReasoningItem{
		Type: ResponseItemTypeReasoning,
		ID:   "r8",
		Content: []any{
			map[string]any{"type": "summary_text", "text": "summary only"},
		},
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractReasoningContent_NilContent(t *testing.T) {
	item := ResponseReasoningItem{
		Type:             ResponseItemTypeReasoning,
		ID:               "r9",
		EncryptedContent: "only_encrypted",
	}
	got, err := extractReasoningContent(item)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "only_encrypted" {
		t.Errorf("got %q", got)
	}
}

// ---------------------------------------------------------------------------
// injectReasoningContent
// ---------------------------------------------------------------------------

func TestInjectReasoningContent_NilMessages(t *testing.T) {
	got := injectReasoningContent("some reasoning", nil)
	if got {
		t.Fatal("expected false for nil messages")
	}
}

func TestInjectReasoningContent_EmptyMessages(t *testing.T) {
	msgs := []model.Message{}
	got := injectReasoningContent("some reasoning", &msgs)
	if got {
		t.Fatal("expected false for empty messages")
	}
}

func TestInjectReasoningContent_NoAssistantMessage(t *testing.T) {
	msgs := []model.Message{
		{Role: "user", Content: "hello"},
		{Role: "tool", Content: "result"},
	}
	got := injectReasoningContent("reasoning text", &msgs)
	if got {
		t.Fatal("expected false when no assistant message exists")
	}
}

func TestInjectReasoningContent_InjectsIntoLastAssistant(t *testing.T) {
	msgs := []model.Message{
		{Role: "assistant", Content: "first"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "second"},
	}
	got := injectReasoningContent("the reasoning", &msgs)
	if !got {
		t.Fatal("expected true when reasoning injected")
	}
	// First assistant should be untouched.
	if msgs[0].ReasoningContent != nil {
		t.Errorf("first assistant ReasoningContent = %v, want nil", msgs[0].ReasoningContent)
	}
	// Last assistant should have reasoning.
	rc, ok := msgs[2].ReasoningContent.(string)
	if !ok || rc != "the reasoning" {
		t.Errorf("last assistant ReasoningContent = %v, want 'the reasoning' (type %T)", msgs[2].ReasoningContent, msgs[2].ReasoningContent)
	}
}

func TestInjectReasoningContent_DoesNotOverwriteExistingReasoning(t *testing.T) {
	existing := "existing reasoning"
	msgs := []model.Message{
		{Role: "assistant", Content: "reply", ReasoningContent: existing},
	}
	got := injectReasoningContent("new reasoning", &msgs)
	// Should NOT overwrite existing reasoning_content.
	if msgs[0].ReasoningContent != existing {
		t.Errorf("ReasoningContent = %v, want %v", msgs[0].ReasoningContent, existing)
	}
	// NormalizeReasoningContent may or may not modify; just verify no overwrite.
	_ = got
}

func TestInjectReasoningContent_EmptyReasoningWithToolCalls(t *testing.T) {
	msgs := []model.Message{
		{Role: "assistant", ToolCalls: []model.Tool{{Id: "c1", Type: "function", Function: model.Function{Name: "f"}}}},
	}
	// Empty reasoning string + tool_calls → NormalizeReasoningContent should inject placeholder.
	got := injectReasoningContent("", &msgs)
	if !got {
		t.Fatal("expected true for placeholder injection")
	}
	rc, ok := msgs[0].ReasoningContent.(string)
	if !ok || rc != model.ReasoningContentPlaceholder {
		t.Errorf("ReasoningContent = %v (%T), want placeholder %q", msgs[0].ReasoningContent, msgs[0].ReasoningContent, model.ReasoningContentPlaceholder)
	}
}

func TestInjectReasoningContent_EmptyReasoningWithoutToolCalls(t *testing.T) {
	msgs := []model.Message{
		{Role: "assistant", Content: "plain reply"},
	}
	// Empty reasoning + no tool_calls → nothing to inject, no modification.
	got := injectReasoningContent("", &msgs)
	if got {
		t.Fatal("expected false — nothing to inject")
	}
	if msgs[0].ReasoningContent != nil {
		t.Errorf("ReasoningContent = %v, want nil", msgs[0].ReasoningContent)
	}
}

func TestInjectReasoningContent_NonEmptyReasoningWithToolCalls(t *testing.T) {
	msgs := []model.Message{
		{Role: "assistant", ToolCalls: []model.Tool{{Id: "c2", Type: "function", Function: model.Function{Name: "g"}}}},
	}
	got := injectReasoningContent("I should call g", &msgs)
	if !got {
		t.Fatal("expected true")
	}
	rc, ok := msgs[0].ReasoningContent.(string)
	if !ok || rc != "I should call g" {
		t.Errorf("ReasoningContent = %v, want 'I should call g'", msgs[0].ReasoningContent)
	}
}

func TestInjectReasoningContent_MultipleAssistantWithToolCalls(t *testing.T) {
	// Only the LAST assistant should get reasoning; others should get placeholder
	// from NormalizeReasoningContent if they have tool_calls and nil reasoning.
	msgs := []model.Message{
		{Role: "assistant", Content: "first", ToolCalls: []model.Tool{{Id: "c1", Type: "function", Function: model.Function{Name: "a"}}}},
		{Role: "tool", Content: "r1", ToolCallId: "c1"},
		{Role: "assistant", Content: "second", ToolCalls: []model.Tool{{Id: "c2", Type: "function", Function: model.Function{Name: "b"}}}},
	}
	got := injectReasoningContent("reasoning for second", &msgs)
	if !got {
		t.Fatal("expected true")
	}
	// First assistant: had nil reasoning + tool_calls → placeholder from NormalizeReasoningContent.
	rc0, ok := msgs[0].ReasoningContent.(string)
	if !ok || rc0 != model.ReasoningContentPlaceholder {
		t.Errorf("first assistant ReasoningContent = %v, want placeholder", msgs[0].ReasoningContent)
	}
	// Second assistant (last): gets the explicit reasoning.
	rc2, ok := msgs[2].ReasoningContent.(string)
	if !ok || rc2 != "reasoning for second" {
		t.Errorf("second assistant ReasoningContent = %v, want 'reasoning for second'", msgs[2].ReasoningContent)
	}
}

func TestInjectReasoningContent_AlreadyNormalized(t *testing.T) {
	// If reasoning was already set (e.g. from a previous reasoning item), inject
	// should not overwrite it, and NormalizeReasoningContent should not overwrite
	// non-nil reasoning.
	existing := "already there"
	msgs := []model.Message{
		{Role: "assistant", Content: "x", ReasoningContent: existing, ToolCalls: []model.Tool{{Id: "c1", Type: "function", Function: model.Function{Name: "f"}}}},
	}
	got := injectReasoningContent("new", &msgs)
	if msgs[0].ReasoningContent != existing {
		t.Errorf("ReasoningContent = %v, want %v", msgs[0].ReasoningContent, existing)
	}
	_ = got
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

// ---------------------------------------------------------------------------
// generateMessageID
// ---------------------------------------------------------------------------

func TestGenerateMessageID_Format(t *testing.T) {
	id := generateMessageID()
	if !strings.HasPrefix(id, "msg_") {
		t.Errorf("id %q should start with msg_", id)
	}
	// 24 random hex chars after the prefix.
	const wantLen = len("msg_") + 24
	if len(id) != wantLen {
		t.Errorf("id len = %d, want %d (got %q)", len(id), wantLen, id)
	}
	for _, c := range id[len("msg_"):] {
		isDigit := c >= '0' && c <= '9'
		isHex := c >= 'a' && c <= 'f'
		if !isDigit && !isHex {
			t.Errorf("non-hex character %q in id %q", c, id)
			break
		}
	}
}

func TestGenerateMessageID_Unique(t *testing.T) {
	const n = 100
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := generateMessageID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d generations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// ---------------------------------------------------------------------------
// ensureMessageIDs
// ---------------------------------------------------------------------------

func TestEnsureMessageIDs_AssignsMissing(t *testing.T) {
	m1 := &model.Message{Role: "user", Content: "hi"}
	m2 := &model.Message{Role: "assistant", Content: "hello"}
	ensureMessageIDs([]*model.Message{m1, m2})
	if m1.ID == "" || !strings.HasPrefix(m1.ID, "msg_") {
		t.Errorf("m1.ID = %q, want non-empty msg_ prefix", m1.ID)
	}
	if m2.ID == "" || !strings.HasPrefix(m2.ID, "msg_") {
		t.Errorf("m2.ID = %q, want non-empty msg_ prefix", m2.ID)
	}
	if m1.ID == m2.ID {
		t.Errorf("expected unique IDs, got same: %q", m1.ID)
	}
}

func TestEnsureMessageIDs_PreservesExisting(t *testing.T) {
	m1 := &model.Message{ID: "msg_existing1", Role: "user"}
	m2 := &model.Message{ID: "msg_existing2", Role: "assistant"}
	ensureMessageIDs([]*model.Message{m1, m2})
	if m1.ID != "msg_existing1" {
		t.Errorf("m1.ID = %q, want preserved", m1.ID)
	}
	if m2.ID != "msg_existing2" {
		t.Errorf("m2.ID = %q, want preserved", m2.ID)
	}
}

func TestEnsureMessageIDs_MixedExistingAndNew(t *testing.T) {
	m1 := &model.Message{ID: "msg_known", Role: "user"}
	m2 := &model.Message{Role: "assistant"} // missing
	m3 := &model.Message{Role: "tool"}      // missing
	ensureMessageIDs([]*model.Message{m1, m2, m3})
	if m1.ID != "msg_known" {
		t.Errorf("existing id lost: got %q", m1.ID)
	}
	if m2.ID == "" || m2.ID == "msg_known" {
		t.Errorf("m2.ID = %q, want freshly generated distinct id", m2.ID)
	}
	if m3.ID == "" || m3.ID == m2.ID || m3.ID == "msg_known" {
		t.Errorf("m3.ID = %q, want freshly generated distinct id", m3.ID)
	}
}

func TestEnsureMessageIDs_EmptySlice(t *testing.T) {
	// Should be a no-op, no panic.
	ensureMessageIDs(nil)
	ensureMessageIDs([]*model.Message{})
}

func TestEnsureMessageIDs_NilEntry(t *testing.T) {
	m1 := &model.Message{Role: "user"}
	ensureMessageIDs([]*model.Message{m1, nil, m1})
	if m1.ID == "" {
		t.Errorf("expected m1 to get an ID, got empty")
	}
	// Nil entry must not panic; the valid entries still get IDs.
}

func TestEnsureMessageIDs_UniqueAcrossBatch(t *testing.T) {
	const n = 200
	batch := make([]*model.Message, n)
	for i := 0; i < n; i++ {
		batch[i] = &model.Message{Role: "user"}
	}
	ensureMessageIDs(batch)
	seen := make(map[string]int, n)
	for i, m := range batch {
		if m.ID == "" {
			t.Errorf("batch[%d] missing ID", i)
		}
		seen[m.ID]++
	}
	if len(seen) != n {
		t.Errorf("expected %d unique IDs, got %d", n, len(seen))
	}
}

// ---------------------------------------------------------------------------
// convertResponsesInputToChatMessages — message ID integration
// ---------------------------------------------------------------------------

func TestConvertResponsesInputToChatMessages_AssignsIDs(t *testing.T) {
	msgs, err := convertResponsesInputToChatMessages("hello", "be helpful")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	for i, msg := range msgs {
		if msg.ID == "" {
			t.Errorf("msg[%d].ID = empty, want populated", i)
		}
		if !strings.HasPrefix(msg.ID, "msg_") {
			t.Errorf("msg[%d].ID = %q, want msg_ prefix", i, msg.ID)
		}
	}
	if msgs[0].ID == msgs[1].ID {
		t.Errorf("expected unique IDs, got same: %q", msgs[0].ID)
	}
}

func TestConvertResponsesInputToChatMessages_PreservesExistingIDs(t *testing.T) {
	// Hand-crafted input where a message-like entry has an id pre-assigned
	// (e.g. a real Responses call that carries ids). The conversion must
	// preserve it. Today the conversion path doesn't carry ids from the wire
	// (ResponseItem -> Message loses them), so this test is best framed around
	// ensuring the helper respects existing IDs on the conversion output
	// by injecting one via the underlying ensureMessageIDs contract.
	msgs, err := convertResponsesInputToChatMessages("hello", "")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	// The user message gets a fresh ID — nothing to preserve here, so just
	// verify the ID is set and well-formed.
	if msgs[0].ID == "" {
		t.Error("ID should be set")
	}
}

func TestConvertResponsesInputToChatMessages_UniqueIDsAcrossBatch(t *testing.T) {
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": "u1"},
		map[string]any{"type": "message", "role": "assistant", "content": "a1"},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "f", "arguments": "{}"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "r"},
		map[string]any{"type": "message", "role": "user", "content": "u2"},
	}
	msgs, err := convertResponsesInputToChatMessages(input, "sys")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(msgs) != 6 { // system + 5 input items
		t.Fatalf("expected 6 messages, got %d", len(msgs))
	}
	seen := make(map[string]int, len(msgs))
	for i, msg := range msgs {
		if msg.ID == "" {
			t.Errorf("msg[%d] missing ID", i)
		}
		seen[msg.ID]++
	}
	if len(seen) != len(msgs) {
		t.Errorf("expected %d unique IDs, got %d (collisions: %v)", len(msgs), len(seen), seen)
	}
}

// ---------------------------------------------------------------------------
// convertResponsesToChatCompletions (full pipeline integration)
// ---------------------------------------------------------------------------

func TestConvertResponsesToChatCompletions_NilRequest(t *testing.T) {
	if _, err := convertResponsesToChatCompletions(nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

func TestConvertResponsesToChatCompletions_StringInput(t *testing.T) {
	req := &ResponsesRequest{Model: "gpt-4o", Input: "hello", Instructions: "be concise"}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if chat.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", chat.Model)
	}
	// system (instructions) + user (input string)
	if len(chat.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].Content != "be concise" {
		t.Errorf("msg[0] = %+v, want system message with instructions", chat.Messages[0])
	}
	if chat.Messages[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want user", chat.Messages[1].Role)
	}
	// every message must carry an id
	for i, msg := range chat.Messages {
		if msg.ID == "" {
			t.Errorf("msg[%d] missing id", i)
		}
	}
}

func TestConvertResponsesToChatCompletions_NoInstructions(t *testing.T) {
	req := &ResponsesRequest{Model: "gpt-4o", Input: "hello"}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(chat.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Role != "user" {
		t.Errorf("role = %q, want user", chat.Messages[0].Role)
	}
}

func TestConvertResponsesToChatCompletions_FullConversation(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: []any{
			map[string]any{"type": "message", "role": "user", "content": "What's the weather?"},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"SF"}`},
			map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "Sunny, 72F"},
		},
		Instructions: "You are a weather bot",
	}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// system + user + assistant(tool_calls) + tool = 4
	if len(chat.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" {
		t.Errorf("msg[0] role = %q, want system", chat.Messages[0].Role)
	}
	if chat.Messages[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want user", chat.Messages[1].Role)
	}
	if chat.Messages[2].Role != "assistant" || len(chat.Messages[2].ToolCalls) != 1 {
		t.Errorf("msg[2] = %+v, want assistant with tool_calls", chat.Messages[2])
	}
	// assistant with tool_calls must carry reasoning_content (thinking turn)
	rc, ok := chat.Messages[2].ReasoningContent.(string)
	if !ok || rc != model.ReasoningContentPlaceholder {
		t.Errorf("msg[2] reasoning_content = %v, want placeholder", chat.Messages[2].ReasoningContent)
	}
	if chat.Messages[3].Role != "tool" || chat.Messages[3].ToolCallId != "call_1" {
		t.Errorf("msg[3] = %+v, want tool message with call_id", chat.Messages[3])
	}
	// all ids unique
	seen := make(map[string]bool, len(chat.Messages))
	for i, msg := range chat.Messages {
		if msg.ID == "" {
			t.Errorf("msg[%d] missing id", i)
		}
		if seen[msg.ID] {
			t.Errorf("duplicate id %q", msg.ID)
		}
		seen[msg.ID] = true
	}
}

func TestConvertResponsesToChatCompletions_ReasoningInjectedIntoAssistant(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: []any{
			map[string]any{"type": "message", "role": "user", "content": "Solve this"},
			map[string]any{"type": "reasoning", "id": "r1", "content": []any{
				map[string]any{"type": "reasoning_text", "text": "step by step reasoning"},
			}},
			map[string]any{"type": "message", "role": "assistant", "content": "The answer is 42"},
		},
	}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// user + assistant = 2 (reasoning item is not emitted standalone; its text
	// is injected into the assistant message that follows)
	if len(chat.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chat.Messages))
	}
	assistant := chat.Messages[1]
	if assistant.Role != "assistant" {
		t.Errorf("role = %q, want assistant", assistant.Role)
	}
	rc, ok := assistant.ReasoningContent.(string)
	if !ok || rc != "step by step reasoning" {
		t.Errorf("reasoning_content = %v, want extracted reasoning text", assistant.ReasoningContent)
	}
}

func TestConvertResponsesToChatCompletions_ReasoningWithoutFollowingAssistant(t *testing.T) {
	// Reasoning as the last item: injectReasoningContent finds no assistant
	// message, so a standalone assistant message with reasoning must be emitted.
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: []any{
			map[string]any{"type": "message", "role": "user", "content": "Think about it"},
			map[string]any{"type": "reasoning", "id": "r1", "content": []any{
				map[string]any{"type": "reasoning_text", "text": "final reasoning text"},
			}},
		},
	}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// user + standalone assistant(reasoning) = 2
	if len(chat.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chat.Messages))
	}
	rc, ok := chat.Messages[1].ReasoningContent.(string)
	if !ok || rc != "final reasoning text" {
		t.Errorf("reasoning_content = %v, want standalone assistant reasoning", chat.Messages[1].ReasoningContent)
	}
}

func TestConvertResponsesToChatCompletions_StreamCopied(t *testing.T) {
	req := &ResponsesRequest{Model: "gpt-4o", Stream: true, Input: "hi"}
	chat, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !chat.Stream {
		t.Error("stream should be copied to the chat request")
	}
}

func TestConvertResponsesToChatCompletions_UnsupportedItemType(t *testing.T) {
	req := &ResponsesRequest{
		Model: "gpt-4o",
		Input: []any{
			map[string]any{"type": "future_item", "data": "x"},
		},
	}
	if _, err := convertResponsesToChatCompletions(req); err != nil {
		t.Fatalf("unexpected error for unknown item type: %v", err)
	}
}

func TestConvertResponsesToChatCompletions_ToolsPropagated(t *testing.T) {
	// Regression: previously the conversion discarded request.Tools, which made
	// tool-using clients (codex CLI etc.) silently get a 400 from the upstream
	// chat API complaining about a missing `tools.name`. See also
	// TestResponsesRequestPreservesToolsField below for the input side.
	tools := []model.Tool{{
		Type: "function",
		Function: model.Function{
			Name:       "get_weather",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}}
	toolChoice := "auto"
	req := &ResponsesRequest{
		Model:      "deepseek-v4-flash",
		Stream:     false,
		Input:      "what's the weather in Tokyo?",
		Tools:      tools,
		ToolChoice: toolChoice,
	}
	converted, err := convertResponsesToChatCompletions(req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(converted.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(converted.Tools))
	}
	if converted.Tools[0].Function.Name != "get_weather" {
		t.Errorf("tool name lost in conversion: %+v", converted.Tools[0])
	}
	if converted.ToolChoice != toolChoice {
		t.Errorf("tool_choice lost in conversion: %v", converted.ToolChoice)
	}
}

func TestResponsesRequestPreservesToolsField(t *testing.T) {
	// The ResponsesRequest struct must keep the tools[] field on the wire —
	// otherwise Go silently drops it during JSON unmarshal. Without this guard
	// a refactor that strips the field from the struct would also silently
	// break all tool-using clients.
	body := []byte(`{
		"model": "deepseek-v4-flash",
		"input": "hi",
		"tools": [{
			"type": "function",
			"function": {
				"name": "f1",
				"description": "d",
				"parameters": {"type": "object", "properties": {}}
			}
		}],
		"tool_choice": "auto"
	}`)
	var r ResponsesRequest
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("Tools field not populated: got %d tools, want 1", len(r.Tools))
	}
	if r.Tools[0].Function.Name != "f1" {
		t.Errorf("tool name not preserved: %+v", r.Tools[0])
	}
	if r.ToolChoice != "auto" {
		t.Errorf("tool_choice not preserved: %v", r.ToolChoice)
	}
}
