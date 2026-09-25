package controller

import (
	"encoding/json"
	"strings"
	"testing"
)

// sseJSON strips the "data: " prefix and trailing newlines from one SSE line.
func sseJSON(t *testing.T, line string) map[string]any {
	t.Helper()
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, opencodeDataPrefix) {
		t.Fatalf("line is not a data event: %q", line)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(line, opencodeDataPrefix)), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	return m
}

// TestOpencodeChatStreamToResponsesStream_ParallelToolCalls guards the
// regression where two tool calls in one turn collapsed into a single
// function_call whose arguments were the concatenation of both
// (`{"city":"Beijing"}{"tz":"UTC"}`) and whose second call_id was dropped.
func TestOpencodeChatStreamToResponsesStream_ParallelToolCalls(t *testing.T) {
	state := &opencodeStreamState{model: "mimo-v2.6-flash", nextOutputIndex: 1}

	lines := []string{
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`,
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Beijing\"}"}}]}}]}`,
		`data: {"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"tz\":\"UTC\"}"}}]}}]}`,
		`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}

	var events []map[string]any
	for _, line := range lines {
		for _, ev := range opencodeChatStreamToResponsesStream(line, state) {
			events = append(events, sseJSON(t, ev))
		}
	}

	addedByCallID := map[string]map[string]any{}
	for _, ev := range events {
		if ev["type"] != "response.output_item.added" {
			continue
		}
		item, _ := ev["item"].(map[string]any)
		if item["type"] != "function_call" {
			continue
		}
		callID, _ := item["call_id"].(string)
		addedByCallID[callID] = item
	}
	if len(addedByCallID) != 2 {
		t.Fatalf("want 2 distinct function_call items, got %d: %#v", len(addedByCallID), addedByCallID)
	}
	if addedByCallID["call_a"]["name"] != "get_weather" || addedByCallID["call_b"]["name"] != "get_time" {
		t.Fatalf("tool names not kept per call: %#v", addedByCallID)
	}

	// Every arguments delta must carry a non-empty call_id belonging to one of
	// the two calls, and never bleed arguments across calls.
	argsByItem := map[string]string{}
	for _, ev := range events {
		if ev["type"] != "response.function_call_arguments.delta" {
			continue
		}
		callID, _ := ev["call_id"].(string)
		if callID == "" {
			t.Fatalf("arguments delta without call_id: %#v", ev)
		}
		if _, ok := addedByCallID[callID]; !ok {
			t.Fatalf("arguments delta references unknown call_id %q", callID)
		}
		itemID, _ := ev["item_id"].(string)
		delta, _ := ev["delta"].(string)
		argsByItem[itemID] += delta
	}
	if got := argsByItem[addedByCallID["call_a"]["id"].(string)]; got != `{"city":"Beijing"}` {
		t.Fatalf("call_a arguments = %q", got)
	}
	if got := argsByItem[addedByCallID["call_b"]["id"].(string)]; got != `{"tz":"UTC"}` {
		t.Fatalf("call_b arguments = %q", got)
	}

	// The terminal response.completed must list both function_calls.
	var completed map[string]any
	for _, ev := range events {
		if ev["type"] == "response.completed" {
			completed = ev
		}
	}
	if completed == nil {
		t.Fatal("no response.completed event emitted")
	}
	resp, _ := completed["response"].(map[string]any)
	output, _ := resp["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("response.completed output = %d items, want 2: %#v", len(output), output)
	}
	args := map[string]string{}
	for _, it := range output {
		m, _ := it.(map[string]any)
		callID, _ := m["call_id"].(string)
		a, _ := m["arguments"].(string)
		args[callID] = a
	}
	if args["call_a"] != `{"city":"Beijing"}` || args["call_b"] != `{"tz":"UTC"}` {
		t.Fatalf("response.completed arguments not per-call: %#v", args)
	}
}
