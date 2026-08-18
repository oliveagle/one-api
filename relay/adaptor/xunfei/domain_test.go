package xunfei

import (
	"net/url"
	"strings"
	"testing"
)

// Xunfei Spark v1 speaks websocket to a HARDCODED wss:// host, so the
// full relay stack cannot be pointed at a test server. Its quirks
// that CAN be pinned hermetically live here: model-name → API version
// resolution, version → domain mapping, the special /chat/{variant}
// URL shapes, and the structure of the HMAC-signed auth URL.

func TestParseAPIVersionByModelName(t *testing.T) {
	cases := map[string]string{
		// Friendly names resolve to their documented versions.
		"Spark-Lite":      "v1.1",
		"Spark-Pro":       "v3.1",
		"Spark-Pro-128K":  "v3.1-128K",
		"Spark-Max":       "v3.5",
		"Spark-Max-32K":   "v3.5-32K",
		"Spark-4.0-Ultra": "v4.0",
		// Anything else falls back to the substring after the first
		// dash — that is how "generalv3.5" style custom names ride
		// through.
		"foo-v9.9": "v9.9",
	}
	for model, want := range cases {
		if got := parseAPIVersionByModelName(model); got != want {
			t.Errorf("parseAPIVersionByModelName(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestApiVersion2Domain(t *testing.T) {
	cases := map[string]string{
		"v1.1":      "lite",
		"v2.1":      "generalv2",
		"v3.1":      "generalv3",
		"v3.1-128K": "pro-128k",
		"v3.5":      "generalv3.5",
		"v3.5-32K":  "max-32k",
		"v4.0":      "4.0Ultra",
		// Unknown versions get the general<v> prefix.
		"v9.9": "generalv9.9",
	}
	for version, want := range cases {
		if got := apiVersion2domain(version); got != want {
			t.Errorf("apiVersion2domain(%q) = %q, want %q", version, got, want)
		}
	}
}

func TestGetXunfeiAuthUrl_URLShapes(t *testing.T) {
	cases := []struct {
		apiVersion string
		wantPrefix string
	}{
		// Most versions use /{version}/chat.
		{"v3.5", "wss://spark-api.xf-yun.com/v3.5/chat"},
		{"v4.0", "wss://spark-api.xf-yun.com/v4.0/chat"},
		// The 128K/32K variants live under /chat/{variant} instead.
		{"v3.1-128K", "wss://spark-api.xf-yun.com/chat/pro-128k"},
		{"v3.5-32K", "wss://spark-api.xf-yun.com/chat/max-32k"},
	}
	for _, tc := range cases {
		domain, authUrl := getXunfeiAuthUrl(tc.apiVersion, "app-key", "app-secret")
		if !strings.HasPrefix(authUrl, tc.wantPrefix+"?") {
			t.Errorf("version %s: authUrl = %q, want prefix %q", tc.apiVersion, authUrl, tc.wantPrefix)
		}
		u, err := url.Parse(authUrl)
		if err != nil {
			t.Fatalf("version %s: parse authUrl: %v", tc.apiVersion, err)
		}
		q := u.Query()
		// The signature rides as three query params: host, date, and a
		// base64 authorization blob.
		if q.Get("host") != "spark-api.xf-yun.com" {
			t.Errorf("version %s: host param = %q", tc.apiVersion, q.Get("host"))
		}
		if q.Get("date") == "" {
			t.Errorf("version %s: date param missing", tc.apiVersion)
		}
		if q.Get("authorization") == "" {
			t.Errorf("version %s: authorization param missing", tc.apiVersion)
		}
		// The domain is fed into the WS request body, not the URL.
		if domain == "" {
			t.Errorf("version %s: domain is empty", tc.apiVersion)
		}
	}
}
