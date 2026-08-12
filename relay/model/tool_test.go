package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestToolUnmarshal_NestedShape asserts the canonical OpenAI ChatCompletions
// shape keeps working — function.name and function.parameters are honored.
func TestToolUnmarshal_NestedShape(t *testing.T) {
	body := []byte(`{
		"type": "function",
		"function": {
			"name": "get_weather",
			"description": "look up weather",
			"parameters": {"type": "object", "properties": {"city": {"type": "string"}}}
		}
	}`)
	var got Tool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "function" {
		t.Errorf("type lost: %q", got.Type)
	}
	if got.Function.Name != "get_weather" {
		t.Errorf("name lost: %q", got.Function.Name)
	}
	if got.Function.Description != "look up weather" {
		t.Errorf("description lost: %q", got.Function.Description)
	}
	if got.Function.Parameters == nil {
		t.Errorf("parameters lost")
	}
	// Round-trip: name must still be in `function.name`.
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"name":"get_weather"`) {
		t.Errorf("round-trip dropped name: %s", out)
	}
	// Sanity: name shouldn't have been moved to the top level.
	if strings.Contains(string(out), `"name":"get_weather","function":`) {
		t.Errorf("name leaked to top level: %s", out)
	}
}

// TestToolUnmarshal_FlatShapeResponses is the regression for `missing
// tools.name`. The OpenAI Responses API uses a flat shape
// (`{type, name, parameters}`); a codex-cli 0.142 tool sender hits this
// path. Without the promotion in (*Tool).UnmarshalJSON, Function.Name is
// empty after unmarshal and the upstream channel rejects with 400.
func TestToolUnmarshal_FlatShapeResponses(t *testing.T) {
	body := []byte(`{
		"type": "function",
		"name": "exec_command",
		"description": "run shell",
		"parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}
	}`)
	var got Tool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "function" {
		t.Errorf("type lost: %q", got.Type)
	}
	if got.Function.Name != "exec_command" {
		t.Errorf("name not promoted from flat shape: %q", got.Function.Name)
	}
	if got.Function.Description != "run shell" {
		t.Errorf("description not promoted: %q", got.Function.Description)
	}
	if got.Function.Parameters == nil {
		t.Errorf("parameters not promoted")
	}
	// Critical: re-marshal must produce the nested shape with name set
	// where the upstream expects it. Otherwise the upstream complains
	// `missing tools.name`.
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"name":"exec_command"`) {
		t.Errorf("flat-shape name did not survive round-trip: %s", out)
	}
	// name must be inside function, not at the top level.
	if strings.Contains(string(out), `"name":"exec_command","function":`) {
		t.Errorf("upstream 400 trigger: name still at top level after round-trip: %s", out)
	}
}

// TestToolUnmarshal_EmptyFunctionObject is the Claude-splice case where
// the relayer stitches an empty "{type: function, function: {}}" placeholder
// into the message stream. That must still round-trip without error.
func TestToolUnmarshal_EmptyFunctionObject(t *testing.T) {
	body := []byte(`{"type":"function","function":{}}`)
	var got Tool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "function" {
		t.Errorf("type lost: %q", got.Type)
	}
	if got.Function.Name != "" {
		t.Errorf("name should be empty, got %q", got.Function.Name)
	}
}

// TestToolUnmarshal_ToolsArrayMixed asserts a request body can mix flat
// and nested shapes in the same array (e.g. one tool from the Responses
// API emitter, another from a fallback path). Each must be promoted
// independently.
func TestToolUnmarshal_ToolsArrayMixed(t *testing.T) {
	body := []byte(`[
		{"type":"function","name":"flat_tool","parameters":{"type":"object"}},
		{"type":"function","function":{"name":"nested_tool","parameters":{"type":"object"}}}
	]`)
	var got []Tool
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tools, got %d", len(got))
	}
	if got[0].Function.Name != "flat_tool" {
		t.Errorf("flat tool name not promoted: %q", got[0].Function.Name)
	}
	if got[1].Function.Name != "nested_tool" {
		t.Errorf("nested tool name lost: %q", got[1].Function.Name)
	}
}
