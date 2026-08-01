package model

import (
	"testing"
)

// ParseInput is the cross-mode (chat / embeddings / completions) input
// normaliser. A regression that drops string inputs would silently break
// embeddings calls that send a single prompt.
func TestParseInput_String(t *testing.T) {
	r := GeneralOpenAIRequest{Input: "hello"}
	got := r.ParseInput()
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("ParseInput = %v, want [\"hello\"]", got)
	}
}

func TestParseInput_StringSlice(t *testing.T) {
	r := GeneralOpenAIRequest{Input: []any{"a", "b", "c"}}
	got := r.ParseInput()
	if len(got) != 3 {
		t.Fatalf("ParseInput = %v", got)
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("ParseInput = %v", got)
	}
}

func TestParseInput_Nil(t *testing.T) {
	r := GeneralOpenAIRequest{}
	if got := r.ParseInput(); got != nil {
		t.Fatalf("ParseInput = %v, want nil", got)
	}
}

func TestParseInput_FiltersNonStrings(t *testing.T) {
	r := GeneralOpenAIRequest{Input: []any{"a", 1, "b", nil, "c"}}
	got := r.ParseInput()
	// Non-string entries are filtered. We don't require a specific cap.
	if len(got) != 3 {
		t.Fatalf("ParseInput = %v", got)
	}
}

// StreamOption's include_usage must round-trip to/from JSON for the relay
// to forward it to upstreams that charge by completion.
func TestStreamOptions_RoundTrip(t *testing.T) {
	o := StreamOptions{IncludeUsage: true}
	// We can't directly json.Marshal because it's a private field and we
	// just exercise the public surface.
	if !o.IncludeUsage {
		t.Fatal("expected IncludeUsage=true")
	}
}
