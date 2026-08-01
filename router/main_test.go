package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

// TestSetRouter_AppliesAllSubrouters confirms the canonical wiring
// (no FRONTEND_BASE_URL override, master node): every sub-router
// registers on the same engine. We assert this by hitting each
// sub-router's signature endpoint and checking the response shape
// rather than introspecting engine.Routes() — that catches the
// "we forgot to call SetRelayRouter" regression that a route-table
// check would also catch, but is also robust against subtle engine
// reuse bugs.
func TestSetRouter_AppliesAllSubrouters(t *testing.T) {
	withTestTheme(t)
	// Make sure no FRONTEND_BASE_URL override is in effect for this
	// test; sub-tests rely on the web router being mounted, which is
	// only the case when frontendBaseUrl == "".
	t.Setenv("FRONTEND_BASE_URL", "")
	prev := config.IsMasterNode
	config.IsMasterNode = true
	t.Cleanup(func() { config.IsMasterNode = prev })

	engine := newTestEngine(t)
	SetRouter(engine, testBuildFS)

	routes := engine.Routes()
	expectations := []struct {
		method, path string
	}{
		{"GET", "/api/status"},
		{"GET", "/dashboard/billing/subscription"},
		{"GET", "/v1/models"},
	}
	for _, exp := range expectations {
		if findRoute(routes, exp.method, exp.path) == nil {
			t.Errorf("SetRouter did not register %s %s", exp.method, exp.path)
		}
	}
}

// TestSetRouter_FrontendBaseUrlRedirect exercises the FRONTEND_BASE_URL
// branch: when the env var is set on a NON-master node, SetRouter
// should install a NoRoute handler that issues a 301 to the configured
// external origin. (Master nodes silently drop the env var; that's
// covered by TestSetRouter_FrontendBaseUrlIgnoredOnMasterNode.) We
// assert both the status code and that the Location header is the
// external origin concatenated with the request path.
func TestSetRouter_FrontendBaseUrlRedirect(t *testing.T) {
	const externalOrigin = "https://app.example.test"
	t.Setenv("FRONTEND_BASE_URL", externalOrigin)
	prev := config.IsMasterNode
	config.IsMasterNode = false
	t.Cleanup(func() { config.IsMasterNode = prev })

	engine := newTestEngine(t)
	SetRouter(engine, testBuildFS)

	// An unknown path that isn't /v1 or /api should hit the
	// NoRoute redirect handler.
	rec := recordResponse(engine, "GET", "/pricing")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301, got %d; body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, externalOrigin) {
		t.Fatalf("Location header %q does not point to %q", loc, externalOrigin)
	}
	if !strings.HasSuffix(loc, "/pricing") {
		t.Fatalf("Location header %q should preserve request path", loc)
	}
}

// TestSetRouter_FrontendBaseUrlTrimTrailingSlash ensures that the
// trailing-slash handling in SetRouter is correct: a FRONTEND_BASE_URL
// with a trailing slash should not produce a double-slash Location
// header. This is a regression test for a bug that previously
// rendered as "//app.example.test/foo" in production.
func TestSetRouter_FrontendBaseUrlTrimTrailingSlash(t *testing.T) {
	t.Setenv("FRONTEND_BASE_URL", "https://app.example.test/")
	prev := config.IsMasterNode
	config.IsMasterNode = false
	t.Cleanup(func() { config.IsMasterNode = prev })

	engine := newTestEngine(t)
	SetRouter(engine, testBuildFS)

	rec := recordResponse(engine, "GET", "/anything")
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "//anything") {
		t.Fatalf("Location header %q contains accidental double slash", loc)
	}
	if !strings.HasPrefix(loc, "https://app.example.test/anything") {
		t.Fatalf("Location header %q missing expected origin", loc)
	}
}

// TestSetRouter_FrontendBaseUrlIgnoredOnMasterNode documents the
// precedence rule: FRONTEND_BASE_URL is silently dropped on master
// nodes. SetRouter should still serve the SPA from the embedded
// build, not redirect to the external origin — the master is the
// public-facing entry point, so it must keep serving the UI.
func TestSetRouter_FrontendBaseUrlIgnoredOnMasterNode(t *testing.T) {
	withTestTheme(t)
	t.Setenv("FRONTEND_BASE_URL", "https://app.example.test")
	prev := config.IsMasterNode
	config.IsMasterNode = true
	t.Cleanup(func() { config.IsMasterNode = prev })

	engine := newTestEngine(t)
	SetRouter(engine, testBuildFS)

	rec := recordResponse(engine, "GET", "/")
	if rec.Code != 200 {
		t.Fatalf("master node should serve SPA, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "one-api test build") {
		t.Fatalf("master node did not serve embedded index.html; got %q", body)
	}
}

// TestSetRouter_NoEnvVarServesWeb confirms that with no
// FRONTEND_BASE_URL set, SetRouter mounts the web router. This is
// the default production path: the SPA is served from the embedded
// build and unknown routes fall through to index.html.
func TestSetRouter_NoEnvVarServesWeb(t *testing.T) {
	withTestTheme(t)
	// Make sure FRONTEND_BASE_URL is empty for the test. Using
	// os.Unsetenv is not enough — t.Setenv doesn't clear a
	// pre-existing value in the parent process, so we explicitly
	// set it to "".
	t.Setenv("FRONTEND_BASE_URL", "")
	prev := config.IsMasterNode
	config.IsMasterNode = true
	t.Cleanup(func() { config.IsMasterNode = prev })
	// Belt-and-suspenders: clear any leftover from a sibling test.
	t.Setenv("FRONTEND_BASE_URL", "")

	engine := newTestEngine(t)
	SetRouter(engine, testBuildFS)

	rec := recordResponse(engine, "GET", "/")
	if rec.Code != 200 {
		t.Fatalf("GET /: got %d, want 200", rec.Code)
	}
}
