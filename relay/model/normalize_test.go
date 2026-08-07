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
