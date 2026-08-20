package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Responses API declares function tools flat; the chat format nests
// them. Tool must decode both and always marshal the nested chat shape.
func TestToolUnmarshal_FlatResponsesShape(t *testing.T) {
	raw := `{"type":"function","name":"shell","description":"Run a command","parameters":{"type":"object"}}`
	var tool Tool
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatalf("unmarshal flat tool: %v", err)
	}
	if tool.Function.Name != "shell" {
		t.Errorf("Function.Name = %q, want shell (flat field must lift)", tool.Function.Name)
	}
	if tool.Function.Description != "Run a command" {
		t.Errorf("Function.Description = %q", tool.Function.Description)
	}
	if tool.Function.Parameters == nil {
		t.Error("Function.Parameters not lifted")
	}
	// Marshaling emits the nested chat shape.
	out, _ := json.Marshal(tool)
	if !strings.Contains(string(out), `"function":{"description":"Run a command","name":"shell"`) {
		t.Errorf("marshal must emit nested chat shape, got %s", out)
	}
}

func TestToolUnmarshal_NestedChatShape(t *testing.T) {
	raw := `{"type":"function","function":{"name":"shell","parameters":{"type":"object"}}}`
	var tool Tool
	if err := json.Unmarshal([]byte(raw), &tool); err != nil {
		t.Fatal(err)
	}
	if tool.Function.Name != "shell" {
		t.Errorf("Function.Name = %q, want shell", tool.Function.Name)
	}
}
