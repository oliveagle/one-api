package router

import (
	"strings"
	"testing"
)

// apiExpectedRoutes is the full set of (method, path) pairs that
// SetApiRouter is contracted to register on the /api prefix. The
// table intentionally mirrors the literal registration calls in
// router/api.go so that any drift between the route table and the
// production code shows up as a failing test.
//
// We deliberately list every entry (instead of spot-checking a few)
// because a typo or accidental deletion in router/api.go is exactly
// the regression this test is designed to catch — "404 on a route
// that should exist" is one of the most common production bugs in
// this codebase.
var apiExpectedRoutes = []struct {
	Method string
	Path   string
}{
	{"GET", "/api/status"},
	{"GET", "/api/models"},
	{"GET", "/api/notice"},
	{"GET", "/api/about"},
	{"GET", "/api/home_page_content"},
	{"GET", "/api/verification"},
	{"GET", "/api/reset_password"},
	{"POST", "/api/user/reset"},
	{"GET", "/api/oauth/github"},
	{"GET", "/api/oauth/oidc"},
	{"GET", "/api/oauth/lark"},
	{"GET", "/api/oauth/state"},
	{"GET", "/api/oauth/wechat"},
	{"GET", "/api/oauth/wechat/bind"},
	{"GET", "/api/oauth/email/bind"},
	{"POST", "/api/topup"},

	{"POST", "/api/user/register"},
	{"POST", "/api/user/login"},
	{"GET", "/api/user/logout"},
	{"GET", "/api/user/dashboard"},
	{"GET", "/api/user/self"},
	{"PUT", "/api/user/self"},
	{"DELETE", "/api/user/self"},
	{"GET", "/api/user/token"},
	{"GET", "/api/user/aff"},
	{"POST", "/api/user/topup"},
	{"GET", "/api/user/available_models"},

	{"GET", "/api/user/"},
	{"GET", "/api/user/search"},
	{"GET", "/api/user/:id"},
	{"POST", "/api/user/"},
	{"POST", "/api/user/manage"},
	{"PUT", "/api/user/"},
	{"DELETE", "/api/user/:id"},

	{"GET", "/api/option/"},
	{"PUT", "/api/option/"},

	{"GET", "/api/channel/"},
	{"GET", "/api/channel/search"},
	{"GET", "/api/channel/models"},
	{"GET", "/api/channel/:id"},
	{"GET", "/api/channel/test"},
	{"GET", "/api/channel/test/:id"},
	{"GET", "/api/channel/update_balance"},
	{"GET", "/api/channel/update_balance/:id"},
	{"POST", "/api/channel/"},
	{"PUT", "/api/channel/"},
	{"DELETE", "/api/channel/disabled"},
	{"DELETE", "/api/channel/:id"},

	{"GET", "/api/token/"},
	{"GET", "/api/token/search"},
	{"GET", "/api/token/:id"},
	{"POST", "/api/token/"},
	{"PUT", "/api/token/"},
	{"DELETE", "/api/token/:id"},

	{"GET", "/api/redemption/"},
	{"GET", "/api/redemption/search"},
	{"GET", "/api/redemption/:id"},
	{"POST", "/api/redemption/"},
	{"PUT", "/api/redemption/"},
	{"DELETE", "/api/redemption/:id"},

	{"GET", "/api/log/"},
	{"DELETE", "/api/log/"},
	{"GET", "/api/log/stat"},
	{"GET", "/api/log/self/stat"},
	{"GET", "/api/log/search"},
	{"GET", "/api/log/self"},
	{"GET", "/api/log/self/search"},

	{"GET", "/api/group/"},

	{"GET", "/api/routing/status"},
	{"DELETE", "/api/routing/session"},
	{"DELETE", "/api/routing/sessions"},
}

// TestSetApiRouter_RegistersExpectedRoutes asserts that every route
// that SetApiRouter promises to expose actually shows up in the
// engine's route table. This is a static structural check: it does
// not need a database, Redis, or any auth header — the goal is to
// catch wiring mistakes (typo'd paths, missing methods, accidentally
// removed routes) before they reach integration tests.
func TestSetApiRouter_RegistersExpectedRoutes(t *testing.T) {
	engine := newTestEngine(t)
	SetApiRouter(engine)

	routes := engine.Routes()
	for _, want := range apiExpectedRoutes {
		got := findRoute(routes, want.Method, want.Path)
		if got == nil {
			t.Errorf("missing route %s %s", want.Method, want.Path)
			continue
		}
		// Each registered route must carry at least one handler —
		// gin's RouterGroup guarantees this when we use GET/POST/etc,
		// but we re-check here so that a future refactor that swaps
		// in a degenerate registration can't silently regress.
		if got.Handler == "" {
			t.Errorf("route %s %s has no handler", want.Method, want.Path)
		}
	}
}

// TestSetApiRouter_NoExtraRoutes guards against accidental drift in
// the other direction: if someone adds a route without updating the
// expected table, the contract test above won't catch it but a stale
// production list might. We allow a small tolerance to keep the test
// focused — listing every conceivable future addition would defeat
// the purpose. The current API router ships exactly the entries in
// apiExpectedRoutes plus the catch-all NoRoute handler gin
// synthesises automatically, which has method "" and path "".
func TestSetApiRouter_NoExtraRoutes(t *testing.T) {
	engine := newTestEngine(t)
	SetApiRouter(engine)

	routes := engine.Routes()
	known := make(map[string]bool, len(apiExpectedRoutes))
	for _, r := range apiExpectedRoutes {
		known[r.Method+" "+r.Path] = true
	}

	extra := 0
	for _, r := range routes {
		key := r.Method + " " + r.Path
		if key == " " {
			// gin's auto-synthesised NoRoute handler — skip.
			continue
		}
		if !known[key] {
			t.Logf("unexpected route registered: %s %s", r.Method, r.Path)
			extra++
		}
	}
	if extra > 0 {
		t.Fatalf("found %d unregistered routes; update apiExpectedRoutes", extra)
	}
}

// TestSetApiRouter_PublicStatus exercises the only API endpoint that
// can run end-to-end without auth, Redis, or a database: GET /api/status.
// It reads from config.SystemName etc. which are package-level vars —
// the default zero values are valid for this handler so we do not need
// to seed anything.
func TestSetApiRouter_PublicStatus(t *testing.T) {
	engine := newTestEngine(t)
	SetApiRouter(engine)

	rec := recordResponse(engine, "GET", "/api/status")
	if rec.Code != 200 {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("status body missing success flag: %s", body)
	}
	if !strings.Contains(body, `"data":{`) {
		t.Fatalf("status body missing data envelope: %s", body)
	}
}
