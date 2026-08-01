package controller

import "testing"

func TestGetTextFromJSON(t *testing.T) {
	body := []byte(`{"text":"hello"}`)
	got, err := getTextFromJSON(body)
	if err != nil {
		t.Fatalf("getTextFromJSON: %v", err)
	}
	if got != "hello" {
		t.Fatalf("getTextFromJSON = %q, want hello", got)
	}
	if _, err := getTextFromJSON([]byte("not json")); err == nil {
		t.Fatal("invalid JSON should return error")
	}
}

func TestGetTextFromText(t *testing.T) {
	if got, _ := getTextFromText([]byte("hello\n")); got != "hello" {
		t.Fatalf("trailing newline should be trimmed: %q", got)
	}
	if got, _ := getTextFromText([]byte("hello")); got != "hello" {
		t.Fatalf("plain text should pass through: %q", got)
	}
}

func TestGetTextFromSRT(t *testing.T) {
	body := []byte("1\n00:00:00,000 --> 00:00:02,000\nfirst line\n2\n00:00:02,000 --> 00:00:04,000\nsecond line\n")
	got, err := getTextFromSRT(body)
	if err != nil {
		t.Fatalf("getTextFromSRT: %v", err)
	}
	if got != "first linesecond line" {
		t.Fatalf("SRT text concatenated: %q", got)
	}
}

func TestGetTextFromVTT(t *testing.T) {
	body := []byte("WEBVTT\n\n00:00:00,000 --> 00:00:02,000\nfirst\n\n00:00:02,000 --> 00:00:04,000\nsecond\n")
	got, err := getTextFromVTT(body)
	if err != nil {
		t.Fatalf("getTextFromVTT: %v", err)
	}
	if got != "firstsecond" {
		t.Fatalf("VTT text concatenated: %q", got)
	}
}

func TestGetTextFromVerboseJSON(t *testing.T) {
	body := []byte(`{"task":"transcribe","language":"en","text":"hi there"}`)
	got, err := getTextFromVerboseJSON(body)
	if err != nil {
		t.Fatalf("getTextFromVerboseJSON: %v", err)
	}
	if got != "hi there" {
		t.Fatalf("verbose JSON text = %q", got)
	}
	if _, err := getTextFromVerboseJSON([]byte("not json")); err == nil {
		t.Fatal("invalid verbose JSON should return error")
	}
}
