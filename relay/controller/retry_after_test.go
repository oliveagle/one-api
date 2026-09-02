package controller

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfterMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"120", 120000},
		{"0", 0},
		{"-5", 0},
		{"junk", 0},
	}
	for _, c := range cases {
		if got := parseRetryAfterMs(c.in); got != c.want {
			t.Errorf("parseRetryAfterMs(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	// HTTP-date form: 2 minutes in the future resolves to ~120000ms.
	future := time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat)
	got := parseRetryAfterMs(future)
	if got < 110000 || got > 125000 {
		t.Errorf("http-date form = %d, want ~120000", got)
	}
}

func TestRelayErrorHandlerCapturesRetryAfter(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		header string
		want   int64
	}{
		{"429 with header", 429, "45", 45000},
		{"429 without header", 429, "", 0},
		{"500 with header ignored", 500, "45", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.status,
				Header:     http.Header{},
				Body:       http.NoBody,
			}
			if tt.header != "" {
				resp.Header.Set("Retry-After", tt.header)
			}
			err := RelayErrorHandler(resp)
			if err.RetryAfterMs != tt.want {
				t.Fatalf("RetryAfterMs = %d, want %d", err.RetryAfterMs, tt.want)
			}
		})
	}
}
