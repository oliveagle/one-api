package model

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolCallArguments(t *testing.T) {
	raw := `{
		"model": "coding_medium",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": null, "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "shell", "arguments": {"command": "ls"}}},
				{"id": "call_2", "type": "function", "function": {"name": "shell", "arguments": "{\"command\":\"pwd\"}"}},
				{"id": "call_3", "type": "function", "function": {"name": "shell", "arguments": null}}
			]},
			{"role": "tool", "tool_call_id": "call_1", "content": "result"}
		]
	}`
	var req GeneralOpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	req.NormalizeToolCallArguments()

	calls := req.Messages[1].ToolCalls
	if len(calls) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(calls))
	}

	// object arguments -> JSON string
	got, ok := calls[0].Function.Arguments.(string)
	if !ok {
		t.Fatalf("call_1 arguments not string: %T", calls[0].Function.Arguments)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("call_1 arguments not valid JSON: %v", err)
	}
	if m["command"] != "ls" {
		t.Fatalf("call_1 arguments mismatch: %v", m)
	}

	// already-string stays string
	if s, ok := calls[1].Function.Arguments.(string); !ok || s != `{"command":"pwd"}` {
		t.Fatalf("call_2 not preserved: %#v", calls[1].Function.Arguments)
	}

	// nil stays nil
	if calls[2].Function.Arguments != nil {
		t.Fatalf("call_3 expected nil, got %#v", calls[2].Function.Arguments)
	}
}

func TestRepairOrphanedToolCalls(t *testing.T) {
	tests := []struct {
		name          string
		input         []Message
		expectedLen   int
		expectedRoles []string
	}{
		{
			name: "no orphaned calls - all responses present",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_1", Function: Function{Name: "test"}},
				}},
				{Role: "tool", ToolCallId: "call_1", Content: "result"},
			},
			expectedLen:   3,
			expectedRoles: []string{"user", "assistant", "tool"},
		},
		{
			name: "orphaned call - missing tool response",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_1", Function: Function{Name: "test"}},
				}},
			},
			expectedLen:   3,
			expectedRoles: []string{"user", "assistant", "tool"},
		},
		{
			name: "multiple orphaned calls in one assistant message",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_1", Function: Function{Name: "test1"}},
					{Id: "call_2", Function: Function{Name: "test2"}},
					{Id: "call_3", Function: Function{Name: "test3"}},
				}},
			},
			expectedLen:   5,
			expectedRoles: []string{"user", "assistant", "tool", "tool", "tool"},
		},
		{
			name: "partial responses - some orphaned",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_1", Function: Function{Name: "test1"}},
					{Id: "call_2", Function: Function{Name: "test2"}},
				}},
				{Role: "tool", ToolCallId: "call_1", Content: "result1"},
			},
			expectedLen:   4,
			expectedRoles: []string{"user", "assistant", "tool", "tool"},
		},
		{
			name: "multiple assistant messages with tool calls",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_1", Function: Function{Name: "test1"}},
				}},
				{Role: "tool", ToolCallId: "call_1", Content: "result1"},
				{Role: "assistant", ToolCalls: []Tool{
					{Id: "call_2", Function: Function{Name: "test2"}},
				}},
			},
			expectedLen:   5,
			expectedRoles: []string{"user", "assistant", "tool", "assistant", "tool"},
		},
		{
			name: "assistant without tool calls - no repair needed",
			input: []Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: "hello"},
			},
			expectedLen:   2,
			expectedRoles: []string{"user", "assistant"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &GeneralOpenAIRequest{
				Model:    "test-model",
				Messages: tt.input,
			}

			req.RepairOrphanedToolCalls()

			if len(req.Messages) != tt.expectedLen {
				t.Errorf("expected %d messages, got %d", tt.expectedLen, len(req.Messages))
			}

			for i, msg := range req.Messages {
				if i >= len(tt.expectedRoles) {
					break
				}
				if msg.Role != tt.expectedRoles[i] {
					t.Errorf("message %d: expected role %s, got %s", i, tt.expectedRoles[i], msg.Role)
				}
				// Check synthetic tool responses have the expected content
				if msg.Role == "tool" && msg.Content == "Tool execution was not recorded" {
					// This is a synthetic response, verify it has a tool_call_id
					if msg.ToolCallId == "" {
						t.Errorf("synthetic tool response at index %d has empty tool_call_id", i)
					}
				}
			}
		})
	}
}

func TestRepairOrphanedToolCalls_NilRequest(t *testing.T) {
	var req *GeneralOpenAIRequest
	// Should not panic
	req.RepairOrphanedToolCalls()
}

func TestRepairOrphanedToolCalls_EmptyMessages(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Model:    "test-model",
		Messages: []Message{},
	}
	req.RepairOrphanedToolCalls()
	if len(req.Messages) != 0 {
		t.Errorf("expected 0 messages, got %d", len(req.Messages))
	}
}

func TestRepairOrphanedToolCalls_IntegrationWithNormalize(t *testing.T) {
	// Test that both functions work together
	raw := `{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "tool_calls": [
				{"id": "call_1", "type": "function", "function": {"name": "test", "arguments": {"arg": "value"}}},
				{"id": "call_2", "type": "function", "function": {"name": "test2", "arguments": "already-string"}}
			]}
		]
	}`

	var req GeneralOpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Apply both transformations
	req.NormalizeToolCallArguments()
	req.RepairOrphanedToolCalls()

	// Should have 4 messages: user, assistant, tool, tool
	if len(req.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(req.Messages))
	}

	// Verify tool responses were added
	if req.Messages[2].Role != "tool" || req.Messages[2].ToolCallId != "call_1" {
		t.Errorf("message 2: expected tool response for call_1, got role=%s tool_call_id=%s",
			req.Messages[2].Role, req.Messages[2].ToolCallId)
	}
	if req.Messages[3].Role != "tool" || req.Messages[3].ToolCallId != "call_2" {
		t.Errorf("message 3: expected tool response for call_2, got role=%s tool_call_id=%s",
			req.Messages[3].Role, req.Messages[3].ToolCallId)
	}

	// Verify arguments were normalized
	calls := req.Messages[1].ToolCalls
	if s, ok := calls[0].Function.Arguments.(string); !ok {
		t.Errorf("call_1 arguments not normalized to string: %T", calls[0].Function.Arguments)
	} else {
		var m map[string]any
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Errorf("call_1 arguments not valid JSON: %v", err)
		}
	}
	if s, ok := calls[1].Function.Arguments.(string); !ok || s != "already-string" {
		t.Errorf("call_2 arguments not preserved: %#v", calls[1].Function.Arguments)
	}
}

func TestNormalizeReasoningContent(t *testing.T) {
	raw := `{
		"model": "coding_medium",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "reasoning_content": "kept", "tool_calls": [{"id": "c1", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c1", "content": "r"},
			{"role": "assistant", "content": "", "tool_calls": [{"id": "c2", "type": "function", "function": {"name": "f", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "c2", "content": "r"},
			{"role": "assistant", "content": "plain answer"}
		]
	}`
	var req GeneralOpenAIRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	modified := req.NormalizeReasoningContent()
	if !modified {
		t.Fatalf("expected modified=true")
	}

	// msg[1]: had reasoning_content -> preserved
	if req.Messages[1].ReasoningContent != "kept" {
		t.Fatalf("existing reasoning_content not preserved: %#v", req.Messages[1].ReasoningContent)
	}
	// msg[3]: thinking turn without reasoning_content -> placeholder injected
	if rc, ok := req.Messages[3].ReasoningContent.(string); !ok || rc != ReasoningContentPlaceholder {
		t.Fatalf("missing reasoning_content not injected: %#v", req.Messages[3].ReasoningContent)
	}
	// msg[5]: assistant with content only (not a thinking turn) -> untouched
	if req.Messages[5].ReasoningContent != nil {
		t.Fatalf("plain assistant answer should not get reasoning_content: %#v", req.Messages[5].ReasoningContent)
	}

	// second call: already normalized -> no more modification
	if req.NormalizeReasoningContent() {
		t.Fatalf("expected no modification on second pass")
	}
}

func TestRepairOrphanedToolCalls_RemovesOrphanedToolResponses(t *testing.T) {
	// Simulates the codex-cli scenario where a tool response references a
	// tool_call_id that was never issued by any assistant message.
	// Upstream providers like Kimi reject these with "tool call id X is not found".
	req := &GeneralOpenAIRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []Tool{
				{Id: "exec_command:1", Function: Function{Name: "exec_command"}},
			}},
			{Role: "tool", ToolCallId: "exec_command:1", Content: "result1"},
			// This tool response references a non-existent tool_call_id
			{Role: "tool", ToolCallId: "exec_command:2", Content: "orphaned result"},
		},
	}

	modified := req.RepairOrphanedToolCalls()
	if !modified {
		t.Fatalf("expected modified=true")
	}

	// Should have 3 messages: user, assistant, tool (only exec_command:1)
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(req.Messages))
	}

	// Verify the orphaned tool response was removed
	for i, msg := range req.Messages {
		if msg.Role == "tool" && msg.ToolCallId == "exec_command:2" {
			t.Errorf("message %d: orphaned tool response for exec_command:2 should have been removed", i)
		}
	}
}

func TestRepairOrphanedToolCalls_MixedOrphanedAndValid(t *testing.T) {
	// Test a complex scenario with both missing and orphaned tool responses
	req := &GeneralOpenAIRequest{
		Model: "test-model",
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []Tool{
				{Id: "call_1", Function: Function{Name: "test1"}},
				{Id: "call_2", Function: Function{Name: "test2"}},
				{Id: "call_3", Function: Function{Name: "test3"}},
			}},
			{Role: "tool", ToolCallId: "call_1", Content: "result1"},
			// call_2 is missing - should get synthetic response
			// call_3 is missing - should get synthetic response
			{Role: "tool", ToolCallId: "call_99", Content: "orphaned"},
		},
	}

	modified := req.RepairOrphanedToolCalls()
	if !modified {
		t.Fatalf("expected modified=true")
	}

	// Should have: user, assistant, tool(call_1), tool(call_2 synthetic), tool(call_3 synthetic)
	// The orphaned call_99 should be removed
	if len(req.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(req.Messages))
	}

	// Verify call_99 was removed
	for i, msg := range req.Messages {
		if msg.Role == "tool" && msg.ToolCallId == "call_99" {
			t.Errorf("message %d: orphaned tool response for call_99 should have been removed", i)
		}
	}

	// Verify synthetic responses were added for call_2 and call_3
	foundCall2 := false
	foundCall3 := false
	for _, msg := range req.Messages {
		if msg.Role == "tool" && msg.ToolCallId == "call_2" && msg.Content == "Tool execution was not recorded" {
			foundCall2 = true
		}
		if msg.Role == "tool" && msg.ToolCallId == "call_3" && msg.Content == "Tool execution was not recorded" {
			foundCall3 = true
		}
	}
	if !foundCall2 {
		t.Error("expected synthetic tool response for call_2")
	}
	if !foundCall3 {
		t.Error("expected synthetic tool response for call_3")
	}
}

// --- RepairToolCallIDs ------------------------------------------------------

// The observed ds2/xiaomi-mimo failure: tool_call continuation fragments
// carry `id: null`, the client clobbers its captured id, and the replayed
// history carries tool_calls with EMPTY ids plus tool responses with empty
// tool_call_ids. Upstreams reject that with "duplicate tool_call id: " /
// "missing messages.tool_calls.id".
func TestRepairToolCallIDs_SynthesizesForEmptyIDsAndPairsResponses(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Messages: []Message{
			{Role: "user", Content: "weather in Tokyo and Paris"},
			{Role: "assistant", ToolCalls: []Tool{
				{Id: "", Type: "function", Function: Function{Name: "get_weather", Arguments: `{"city":"Tokyo"}`}},
				{Id: "", Type: "function", Function: Function{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
			}},
			{Role: "tool", ToolCallId: "", Content: `{"temp":18}`},
			{Role: "tool", ToolCallId: "", Content: `{"temp":21}`},
			{Role: "assistant", Content: "Tokyo 18, Paris 21"},
		},
	}

	if !req.RepairToolCallIDs() {
		t.Fatal("expected modification for empty tool call ids")
	}

	callIDs := make([]string, 0, 2)
	for _, tc := range req.Messages[1].ToolCalls {
		if tc.Id == "" {
			t.Error("assistant tool_call id still empty after repair")
		}
		callIDs = append(callIDs, tc.Id)
	}
	if callIDs[0] == callIDs[1] {
		t.Errorf("synthetic ids must be unique, got %q twice", callIDs[0])
	}
	// Paired tool responses receive the synthetic ids positionally.
	if req.Messages[2].ToolCallId != callIDs[0] {
		t.Errorf("tool response 1 tool_call_id = %q, want %q", req.Messages[2].ToolCallId, callIDs[0])
	}
	if req.Messages[3].ToolCallId != callIDs[1] {
		t.Errorf("tool response 2 tool_call_id = %q, want %q", req.Messages[3].ToolCallId, callIDs[1])
	}
}

func TestRepairToolCallIDs_LeavesValidHistoryUntouched(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []Tool{
				{Id: "call_valid_1", Type: "function", Function: Function{Name: "f", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallId: "call_valid_1", Content: "ok"},
		},
	}
	if req.RepairToolCallIDs() {
		t.Error("valid history must not be modified")
	}
	if req.Messages[1].ToolCalls[0].Id != "call_valid_1" {
		t.Errorf("valid id was rewritten: %q", req.Messages[1].ToolCalls[0].Id)
	}
}

// Integration: after RepairToolCallIDs, RepairOrphanedToolCalls must see a
// consistent call/response pairing (no synthetic responses inserted).
func TestRepairToolCallIDs_IntegrationWithOrphanRepair(t *testing.T) {
	req := &GeneralOpenAIRequest{
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", ToolCalls: []Tool{
				{Id: "", Type: "function", Function: Function{Name: "f", Arguments: "{}"}},
			}},
			{Role: "tool", ToolCallId: "", Content: "ok"},
		},
	}
	req.RepairToolCallIDs()
	// The pairing is intact, so orphan repair must not grow the history.
	before := len(req.Messages)
	req.RepairOrphanedToolCalls()
	if len(req.Messages) != before {
		t.Errorf("orphan repair modified a repaired history: %d -> %d messages", before, len(req.Messages))
	}
}
