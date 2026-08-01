package router

import (
	"embed"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

//go:embed web/build/testbuild/index.html
var testBuildFS embed.FS

const testTheme = "testbuild"

// withTestTheme temporarily points config.Theme at the embedded
// test fixture directory and restores the previous value on cleanup.
// SetWebRouter composes its paths from config.Theme (web/build/<theme>),
// so without this swap the test would reach into the production
// web/build/default tree and bloat the test binary. We embed a
// dedicated fixture under router/web/build/testbuild/ instead so the
// embed path matches what SetWebRouter asks fs.Sub for.
func withTestTheme(t *testing.T) {
	t.Helper()
	prev := config.Theme
	config.Theme = testTheme
	t.Cleanup(func() { config.Theme = prev })
}

// TestSetWebRouter_RegistersStaticFiles checks that the static
// middleware wired by SetWebRouter answers requests for files that
// exist in the embedded build directory. We embed a tiny fixture
// directory under testdata/webtest/ rather than reusing the
// production web/build/ tree so the test stays self-contained.
//
// gin-contrib/static serves files at the same path the middleware is
// mounted at, so we hit "/" (which serves index.html) and assert the
// payload round-trips through the middleware chain intact.
func TestSetWebRouter_RegistersStaticFiles(t *testing.T) {
	withTestTheme(t)
	engine := newTestEngine(t)
	SetWebRouter(engine, testBuildFS)

	rec := recordResponse(engine, "GET", "/")
	if rec.Code != 200 {
		t.Fatalf("GET /: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "one-api test build") {
		t.Fatalf("expected embedded index.html payload, got %q", body)
	}
}

// TestSetWebRouter_NoRouteServesIndex exercises the NoRoute fallback:
// requests for paths that don't match any registered route (and that
// aren't /v1 or /api, which would go to RelayNotFound) should be
// served from index.html. This is what makes the SPA work: a user
// navigates to /some/deep/link and the front-end router takes over.
func TestSetWebRouter_NoRouteServesIndex(t *testing.T) {
	withTestTheme(t)
	engine := newTestEngine(t)
	SetWebRouter(engine, testBuildFS)

	for _, path := range []string{"/dashboard", "/pricing", "/random/deep/path"} {
		rec := recordResponse(engine, "GET", path)
		if rec.Code != 200 {
			t.Errorf("GET %s: got %d, want 200 (NoRoute should serve index.html)", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "one-api test build") {
			t.Errorf("GET %s: body did not contain index.html payload; got %q", path, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("GET %s: Cache-Control header = %q, want %q", path, got, "no-cache")
		}
	}
}

// TestSetWebRouter_NoRouteRelayNotFound verifies the carve-out in
// the NoRoute handler: requests under /v1/* or /api/* that miss the
// relay/api routers must be answered by RelayNotFound, not by the
// SPA index. The handler emits an OpenAI-style error envelope
// (`{"error":{"message":"Invalid URL ...","type":"invalid_request_error"}}`)
// with a 404 status — that's the signal we assert on.
func TestSetWebRouter_NoRouteRelayNotFound(t *testing.T) {
	withTestTheme(t)
	engine := newTestEngine(t)
	SetWebRouter(engine, testBuildFS)

	for _, path := range []string{"/v1/does-not-exist", "/api/does-not-exist"} {
		rec := recordResponse(engine, "GET", path)
		if rec.Code != 404 {
			t.Errorf("GET %s: expected 404 from RelayNotFound, got %d", path, rec.Code)
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET %s: expected JSON content-type from RelayNotFound, got %q", path, rec.Header().Get("Content-Type"))
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"type":"invalid_request_error"`) {
			t.Errorf("GET %s: expected RelayNotFound envelope, got %q", path, body)
		}
		if strings.Contains(body, "one-api test build") {
			t.Errorf("GET %s: served SPA payload instead of RelayNotFound; the /v1 /api carve-out regressed", path)
		}
	}
}

// TestSetWebRouter_GzipMiddlewareActive checks that the global
// gzip middleware is in the chain — the simplest probe is an
// Accept-Encoding: gzip header on the request, which should not
// cause the request to be rejected.
func TestSetWebRouter_GzipMiddlewareActive(t *testing.T) {
	withTestTheme(t)
	engine := newTestEngine(t)
	SetWebRouter(engine, testBuildFS)

	req := newPreflightRequest("GET", "/")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptestRecorder(engine, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200 with Accept-Encoding: gzip, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
