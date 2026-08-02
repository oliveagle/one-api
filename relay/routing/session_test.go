package routing

import (
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

func TestSessionKeyFromBody_TopLevelSessionID(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	got := sessionKeyFromBody([]byte(`{"model":"coding_medium","session_id":"abc123"}`))
	if got != "abc123" {
		t.Fatalf("got %q want abc123", got)
	}
}

func TestSessionKeyFromBody_SessionField(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	got := sessionKeyFromBody([]byte(`{"session":"def456"}`))
	if got != "def456" {
		t.Fatalf("got %q want def456", got)
	}
}

func TestSessionKeyFromBody_Metadata(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	got := sessionKeyFromBody([]byte(`{"metadata":{"session_id":"nested-1"}}`))
	if got != "nested-1" {
		t.Fatalf("got %q want nested-1", got)
	}
}

func TestSessionKeyFromBody_CustomField(t *testing.T) {
	config.SessionIdBodyField = "conversation_id"
	got := sessionKeyFromBody([]byte(`{"conversation_id":"conv-9"}`))
	if got != "conv-9" {
		t.Fatalf("got %q want conv-9", got)
	}
}

func TestSessionKeyFromBody_InvalidJSON(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	if got := sessionKeyFromBody([]byte(`{invalid`)); got != "" {
		t.Fatalf("expected empty for invalid JSON, got %q", got)
	}
}

func TestSessionKeyFromBody_PreferHeaderLikeOrder(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	got := sessionKeyFromBody([]byte(`{"session_id":"a","session":"b","metadata":{"session_id":"c"}}`))
	if got != "a" {
		t.Fatalf("top-level session_id should win, got %q", got)
	}
}

func TestSessionKeyFromBody_TrimsWhitespace(t *testing.T) {
	config.SessionIdBodyField = "session_id"
	got := sessionKeyFromBody([]byte(`{"session_id":"  sp1  "}`))
	if strings.TrimSpace(got) != "sp1" || got != "sp1" {
		t.Fatalf("got %q want trimmed sp1", got)
	}
}
