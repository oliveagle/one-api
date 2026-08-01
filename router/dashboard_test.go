package router

import (
	"testing"
)

// dashboardExpectedRoutes is the set of (method, path) pairs that
// SetDashboardRouter is contracted to register. There are exactly
// four routes — each route is exposed under both /dashboard/... and
// /v1/dashboard/... aliases — and listing them explicitly makes it
// obvious at a glance if any of them get dropped in a future refactor.
var dashboardExpectedRoutes = []struct {
	Method string
	Path   string
}{
	{"GET", "/dashboard/billing/subscription"},
	{"GET", "/v1/dashboard/billing/subscription"},
	{"GET", "/dashboard/billing/usage"},
	{"GET", "/v1/dashboard/billing/usage"},
}

// TestSetDashboardRouter_RegistersExpectedRoutes asserts that all
// four billing routes are wired in. We don't need a database or a
// real token — gin just walks the route table — so this catches the
// "I deleted a line in router/dashboard.go" regression cheaply.
func TestSetDashboardRouter_RegistersExpectedRoutes(t *testing.T) {
	engine := newTestEngine(t)
	SetDashboardRouter(engine)

	routes := engine.Routes()
	for _, want := range dashboardExpectedRoutes {
		if findRoute(routes, want.Method, want.Path) == nil {
			t.Errorf("missing dashboard route %s %s", want.Method, want.Path)
		}
	}
}

// TestSetDashboardRouter_RejectsUnauthenticatedRequests confirms that
// the middleware chain on the dashboard routes is intact: a request
// without a token must NOT reach the handler. The TokenAuth middleware
// short-circuits with a 401 JSON body, so we assert the status code
// rather than the handler being invoked.
func TestSetDashboardRouter_RejectsUnauthenticatedRequests(t *testing.T) {
	engine := newTestEngine(t)
	SetDashboardRouter(engine)

	for _, path := range []string{
		"/dashboard/billing/subscription",
		"/v1/dashboard/billing/subscription",
		"/dashboard/billing/usage",
		"/v1/dashboard/billing/usage",
	} {
		rec := recordResponse(engine, "GET", path)
		if rec.Code == 200 {
			t.Errorf("unauthenticated %s returned 200; expected auth challenge", path)
		}
	}
}

// TestSetDashboardRouter_CORSHeadersPresent verifies that the CORS
// middleware that SetDashboardRouter installs writes CORS headers on
// responses. We issue a plain GET with an Origin header — that's
// what a browser actually sends for cross-origin requests. The
// gin-contrib/cors middleware is configured with AllowAllOrigins=true
// and AllowCredentials=true in middleware/cors.go, so the response
// should carry Access-Control-Allow-Origin and the credentials flag.
//
// We assert presence of those headers so that a future refactor that
// accidentally drops the middleware from the chain is caught here
// instead of by a downstream browser-side complaint.
func TestSetDashboardRouter_CORSHeadersPresent(t *testing.T) {
	engine := newTestEngine(t)
	SetDashboardRouter(engine)

	req := newPreflightRequest("GET", "/dashboard/billing/subscription")
	// Strip the preflight-specific header so the request is treated
	// as a regular cross-origin GET rather than a preflight.
	req.Method = "GET"
	req.Header.Del("Access-Control-Request-Method")
	rec := httptestRecorder(engine, req)

	headers := rec.Header()
	if headers.Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("expected Access-Control-Allow-Origin header, got %+v", headers)
	}
	if headers.Get("Access-Control-Allow-Credentials") == "" {
		t.Fatalf("expected Access-Control-Allow-Credentials header, got %+v", headers)
	}
}
