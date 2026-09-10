package controller

// Unit tests for sanitizeResponsesToolCallArgs — the repair that keeps a
// session from being permanently wedged when an upstream model emits a
// function_call whose arguments JSON is malformed. See the function's doc
// comment in responses.go for the incident record, and the Category 1
// integration tests in controller/relay_mock_categories_test.go
// (TestCategory1_PoisonedHistoryArguments_*) for the end-to-end pin.

import (
	"encoding/json"
	"testing"

	"github.com/songquanpeng/one-api/relay/adaptor/mock"
)

// sanitizeBody parses body the same way getResponsesRequestBody does and
// runs the sanitizer, returning (changed, re-marshaled body).
func sanitizeBody(t *testing.T, body string) (bool, string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("test body not JSON: %v", err)
	}
	changed := sanitizeResponsesToolCallArgs(raw)
	out, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return changed, string(out)
}

// inputArgumentsOf extracts the arguments string of the first function_call
// item found in the sanitized body ("" when none).
func inputArgumentsOf(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Input []struct {
			Type      string `json:"type"`
			Arguments string `json:"arguments"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("sanitized body not JSON: %v", err)
	}
	for _, item := range parsed.Input {
		if item.Type == "function_call" {
			return item.Arguments
		}
	}
	return ""
}

func TestSanitize_TruncatedArgumentsRepaired(t *testing.T) {
	// The real 2026-09-09 poison: exec_command truncated mid-string at 178
	// bytes — no closing quote, no closing brace.
	body := `{"model":"m","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"run it"}]},` +
		`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":` +
		jsonQuote(mock.PoisonedToolCallArguments) + `},` +
		`{"type":"function_call_output","call_id":"call_1","output":"failed to parse function arguments: EOF"}]}`
	changed, out := sanitizeBody(t, body)
	if !changed {
		t.Fatalf("sanitizer must repair the truncated arguments")
	}
	if got := inputArgumentsOf(t, out); got != "{}" {
		t.Errorf("arguments = %q, want %q", got, "{}")
	}
	// The rest of the item must survive the round-trip.
	var parsed struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("sanitized body not JSON: %v", err)
	}
	call := parsed.Input[1]
	if call["name"] != "exec_command" || call["call_id"] != "call_1" {
		t.Errorf("item fields lost during repair: %v", call)
	}
	if parsed.Input[2]["type"] != "function_call_output" {
		t.Errorf("sibling items must be untouched, got %v", parsed.Input[2])
	}
}

func TestSanitize_InvalidJSONLiteralRepaired(t *testing.T) {
	// The second real-world shape: write_stdin with Python's `none` where
	// JSON demands `null`.
	body := `{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"write_stdin","arguments":"{\"session_id\": none, \"yield_time_ms\": 1000}"}]}`
	changed, out := sanitizeBody(t, body)
	if !changed {
		t.Fatalf("sanitizer must repair the invalid literal")
	}
	if got := inputArgumentsOf(t, out); got != "{}" {
		t.Errorf("arguments = %q, want %q", got, "{}")
	}
}

func TestSanitize_MissingArgumentsAdded(t *testing.T) {
	// ark's literal complaint: "missing `input.arguments`".
	body := `{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"shell"}]}`
	changed, out := sanitizeBody(t, body)
	if !changed {
		t.Fatalf("sanitizer must add missing arguments")
	}
	if got := inputArgumentsOf(t, out); got != "{}" {
		t.Errorf("arguments = %q, want %q", got, "{}")
	}
}

func TestSanitize_HealthyArgumentsUntouched(t *testing.T) {
	body := `{"model":"m","input":[{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":[\"ls\"]}"}]}`
	changed, out := sanitizeBody(t, body)
	if changed {
		t.Fatalf("healthy arguments must not be repaired")
	}
	if got := inputArgumentsOf(t, out); got != `{"cmd":["ls"]}` {
		t.Errorf("healthy input must survive semantically untouched, got %s", out)
	}
}

func TestSanitize_MixedHistoryOnlyBadItemRepaired(t *testing.T) {
	// One healthy call + one poisoned call: only the bad item may change.
	body := `{"model":"m","input":[` +
		`{"type":"function_call","call_id":"c1","name":"shell","arguments":"{\"cmd\":[\"ls\"]}"},` +
		`{"type":"function_call","call_id":"c2","name":"exec_command","arguments":` +
		jsonQuote(`{"cmd": "cd /tmp && timeout 3 /tmp/mockd-test jwks 2>&1 | head -10; ls -la`) + `}]}`
	changed, out := sanitizeBody(t, body)
	if !changed {
		t.Fatalf("sanitizer must repair the one bad item in a mixed history")
	}
	var parsed struct {
		Input []struct {
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"input"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Input[0].Arguments != `{"cmd":["ls"]}` {
		t.Errorf("healthy sibling item must be untouched, got %q", parsed.Input[0].Arguments)
	}
	if parsed.Input[1].Arguments != "{}" {
		t.Errorf("repaired item arguments = %q, want {}", parsed.Input[1].Arguments)
	}
}

func TestSanitize_CustomToolCallFreeformInputUntouched(t *testing.T) {
	// custom_tool_call.input is freeform text (apply_patch bodies are raw
	// patches, not JSON) — the sanitizer must NOT touch it.
	body := `{"model":"m","input":[{"type":"custom_tool_call","call_id":"c1","name":"apply_patch","input":"*** Begin Patch\n*** not json at all"}]}`
	changed, _ := sanitizeBody(t, body)
	if changed {
		t.Fatalf("custom_tool_call freeform input must never be json-validated")
	}
}

func TestSanitize_StringInputAndMissingInputNoOps(t *testing.T) {
	if changed, _ := sanitizeBody(t, `{"model":"m","input":"plain prompt"}`); changed {
		t.Errorf("string-form input must be a no-op")
	}
	if changed, _ := sanitizeBody(t, `{"model":"m"}`); changed {
		t.Errorf("body without input must be a no-op")
	}
}

func TestSanitize_ArrayWithNonObjectEntriesNoOp(t *testing.T) {
	// Defensive: junk entries in the input array must not panic or trip
	// the sanitizer into a rewrite.
	body := `{"model":"m","input":["a string entry",42,{"type":"message","role":"user"}]}`
	changed, _ := sanitizeBody(t, body)
	if changed {
		t.Errorf("non-function_call entries must be a no-op")
	}
}

// jsonQuote encodes s as a JSON string literal (the `arguments` field is a
// JSON document wrapped in a JSON string).
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
