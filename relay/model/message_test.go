package model

import "testing"

func TestMessage_StringContent(t *testing.T) {
	m := Message{Content: "hello"}
	if !m.IsStringContent() {
		t.Fatal("IsStringContent should be true for string content")
	}
	if got := m.StringContent(); got != "hello" {
		t.Fatalf("StringContent = %q, want hello", got)
	}
}

func TestMessage_StringContent_NonString(t *testing.T) {
	// Content typed as []any means we should fall through to "".
	m := Message{Content: []any{}}
	if m.IsStringContent() {
		t.Fatal("IsStringContent should be false for []any content")
	}
	if got := m.StringContent(); got != "" {
		t.Fatalf("StringContent = %q, want empty (non-string content)", got)
	}
}

func TestMessage_StringContent_AnyOther(t *testing.T) {
	m := Message{Content: 123}
	if m.IsStringContent() {
		t.Fatal("IsStringContent should be false for non-string content")
	}
	if got := m.StringContent(); got != "" {
		t.Fatalf("StringContent = %q, want empty", got)
	}
}

func TestMessage_StringContent_FromList(t *testing.T) {
	m := Message{Content: []any{
		map[string]any{"type": ContentTypeText, "text": "foo"},
		map[string]any{"type": ContentTypeImageURL, "image_url": map[string]any{"url": "x"}},
		map[string]any{"type": ContentTypeText, "text": " bar"},
	}}
	if got := m.StringContent(); got != "foo bar" {
		t.Fatalf("StringContent = %q, want 'foo bar' (text parts concatenated)", got)
	}
}

func TestMessage_ParseContent_StringShortcut(t *testing.T) {
	m := Message{Content: "hi"}
	parts := m.ParseContent()
	if len(parts) != 1 {
		t.Fatalf("len(parts) = %d", len(parts))
	}
	if parts[0].Type != ContentTypeText || parts[0].Text != "hi" {
		t.Fatalf("part[0] = %+v", parts[0])
	}
}

func TestMessage_ParseContent_Multimodal(t *testing.T) {
	m := Message{Content: []any{
		map[string]any{"type": ContentTypeText, "text": "describe"},
		map[string]any{"type": ContentTypeImageURL, "image_url": map[string]any{"url": "https://x/y.png"}},
		map[string]any{"type": "ignored_unknown_type"},
	}}
	parts := m.ParseContent()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d (%+v)", len(parts), parts)
	}
	if parts[0].Type != ContentTypeText || parts[0].Text != "describe" {
		t.Fatalf("text part wrong: %+v", parts[0])
	}
	if parts[1].Type != ContentTypeImageURL || parts[1].ImageURL == nil || parts[1].ImageURL.Url != "https://x/y.png" {
		t.Fatalf("image part wrong: %+v", parts[1])
	}
}

func TestMessage_ParseContent_NilForUnknown(t *testing.T) {
	// A scalar content must return nil (no parts).
	m := Message{Content: 42}
	parts := m.ParseContent()
	if parts != nil {
		t.Fatalf("expected nil parts for non-list scalar, got %+v", parts)
	}
}

func TestMessage_ParseContent_SkipsNonMapEntries(t *testing.T) {
	// A list with a non-map entry should not crash; skip it.
	m := Message{Content: []any{"raw-string", 5, nil,
		map[string]any{"type": ContentTypeText, "text": "hello"},
	}}
	parts := m.ParseContent()
	if len(parts) != 1 || parts[0].Text != "hello" {
		t.Fatalf("parts = %+v", parts)
	}
}

// Constants must stay stable because the JSON decoder reuses them as the
// "type" string for content blocks.
func TestContentTypeConstants(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{ContentTypeText, "text"},
		{ContentTypeImageURL, "image_url"},
		{ContentTypeInputAudio, "input_audio"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("constant = %q, want %q", tc.got, tc.want)
		}
	}
}
