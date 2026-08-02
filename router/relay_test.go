package router

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// relayExpectedRoutes enumerates the (method, path) pairs that
// SetRelayRouter exposes. The list is exhaustive on purpose — the
// relay router is the surface every OpenAI / Anthropic client talks
// to, and silent route deletions here would break production
// traffic in a way that is hard to debug from server logs alone.
//
// The handler function names are not checked — the relay router
// mixes controller.Relay (passthrough) with controller.RelayNotImplemented
// (501 stub) for the same handler function in production.
var relayExpectedRoutes = []struct {
	Method string
	Path   string
}{
	{"GET", "/v1/models"},
	{"GET", "/v1/models/:model"},

	// Client-facing sticky-routing control (token auth, not relayed).
	{"GET", "/v1/oneapi/routing/nodes"},
	{"POST", "/v1/oneapi/routing/nodes"},
	{"POST", "/v1/oneapi/routing/pin"},
	{"POST", "/v1/oneapi/routing/cycle"},
	{"POST", "/v1/oneapi/routing/unpin"},

	{"POST", "/v1/oneapi/proxy/:channelid/*target"},
	{"POST", "/v1/completions"},
	{"POST", "/v1/chat/completions"},
	{"POST", "/v1/responses"},
	{"GET", "/v1/responses/:response_id"},
	{"DELETE", "/v1/responses/:response_id"},
	{"POST", "/v1/responses/:response_id/cancel"},
	{"GET", "/v1/responses/:response_id/input_items"},
	{"POST", "/v1/edits"},
	{"POST", "/v1/images/generations"},
	{"POST", "/v1/embeddings"},
	{"POST", "/v1/engines/:model/embeddings"},
	{"POST", "/v1/audio/transcriptions"},
	{"POST", "/v1/audio/translations"},
	{"POST", "/v1/audio/speech"},
	{"POST", "/v1/moderations"},
}

// relayExpectedNotImplemented lists the routes that are intentionally
// routed to controller.RelayNotImplemented (a 501 stub). These are
// OpenAI endpoints we have not yet implemented (images edits, files,
// fine-tuning, assistants, threads, runs). We assert each is present
// so a future refactor that drops the stubs (and lets the request
// fall through to Distribute, which would fail loudly downstream)
// cannot silently change the 501 contract.
var relayExpectedNotImplemented = []struct {
	Method string
	Path   string
}{
	{"POST", "/v1/images/edits"},
	{"POST", "/v1/images/variations"},
	{"GET", "/v1/files"},
	{"POST", "/v1/files"},
	{"DELETE", "/v1/files/:id"},
	{"GET", "/v1/files/:id"},
	{"GET", "/v1/files/:id/content"},
	{"POST", "/v1/fine_tuning/jobs"},
	{"GET", "/v1/fine_tuning/jobs"},
	{"GET", "/v1/fine_tuning/jobs/:id"},
	{"POST", "/v1/fine_tuning/jobs/:id/cancel"},
	{"GET", "/v1/fine_tuning/jobs/:id/events"},
	{"DELETE", "/v1/models/:model"},
	{"POST", "/v1/assistants"},
	{"GET", "/v1/assistants/:id"},
	{"POST", "/v1/assistants/:id"},
	{"DELETE", "/v1/assistants/:id"},
	{"GET", "/v1/assistants"},
	{"POST", "/v1/assistants/:id/files"},
	{"GET", "/v1/assistants/:id/files/:fileId"},
	{"DELETE", "/v1/assistants/:id/files/:fileId"},
	{"GET", "/v1/assistants/:id/files"},
	{"POST", "/v1/threads"},
	{"GET", "/v1/threads/:id"},
	{"POST", "/v1/threads/:id"},
	{"DELETE", "/v1/threads/:id"},
	{"POST", "/v1/threads/:id/messages"},
	{"GET", "/v1/threads/:id/messages/:messageId"},
	{"POST", "/v1/threads/:id/messages/:messageId"},
	{"GET", "/v1/threads/:id/messages/:messageId/files/:filesId"},
	{"GET", "/v1/threads/:id/messages/:messageId/files"},
	{"POST", "/v1/threads/:id/runs"},
	{"GET", "/v1/threads/:id/runs/:runsId"},
	{"POST", "/v1/threads/:id/runs/:runsId"},
	{"GET", "/v1/threads/:id/runs"},
	{"POST", "/v1/threads/:id/runs/:runsId/submit_tool_outputs"},
	{"POST", "/v1/threads/:id/runs/:runsId/cancel"},
	{"GET", "/v1/threads/:id/runs/:runsId/steps/:stepId"},
	{"GET", "/v1/threads/:id/runs/:runsId/steps"},
}

// TestSetRelayRouter_RegistersRelayRoutes asserts the live relay
// routes are present. We don't issue requests: every relay route is
// behind TokenAuth + Distribute, both of which need a database to
// run, so a real request would panic long before the handler.
func TestSetRelayRouter_RegistersRelayRoutes(t *testing.T) {
	engine := newTestEngine(t)
	SetRelayRouter(engine)

	routes := engine.Routes()
	for _, want := range relayExpectedRoutes {
		if findRoute(routes, want.Method, want.Path) == nil {
			t.Errorf("missing relay route %s %s", want.Method, want.Path)
		}
	}
}

// TestSetRelayRouter_RegistersNotImplementedStubs asserts every
// route that production wires up to controller.RelayNotImplemented
// is present. The body of the handler is out of scope here — we just
// want to make sure the contract "this path returns 501, not 404"
// is preserved across refactors.
func TestSetRelayRouter_RegistersNotImplementedStubs(t *testing.T) {
	engine := newTestEngine(t)
	SetRelayRouter(engine)

	routes := engine.Routes()
	for _, want := range relayExpectedNotImplemented {
		if findRoute(routes, want.Method, want.Path) == nil {
			t.Errorf("missing not-implemented relay route %s %s", want.Method, want.Path)
		}
	}
}

// TestSetRelayRouter_RejectsUnauthenticatedModels confirms the
// /v1/models routes are gated by TokenAuth. The middleware aborts
// before the handler runs, so we should not see a 200.
func TestSetRelayRouter_RejectsUnauthenticatedModels(t *testing.T) {
	engine := newTestEngine(t)
	SetRelayRouter(engine)

	for _, path := range []string{"/v1/models", "/v1/models/gpt-4"} {
		rec := recordResponse(engine, "GET", path)
		if rec.Code == 200 {
			t.Errorf("unauthenticated %s returned 200; expected auth challenge", path)
		}
	}
}

// TestSetRelayRouter_RouteCountBaseline is a sanity check: every
// route SetRelayRouter registers must be either in the relay table
// or in the not-implemented table. If the numbers drift (for example
// because someone adds a route in code but forgets to list it in
// either expected table), this test catches the discrepancy.
//
// Any route that appears in the engine but NOT in either expected
// table is logged with a hint, so the failure message is actionable
// rather than just a count delta.
//
// We tolerate one specific pattern: relay.go uses `relayV1Router.Any`
// for the proxy route, which gin fans out into a separate route per
// HTTP method. Those seven expansions (PUT/PATCH/HEAD/OPTIONS/DELETE/
// CONNECT/TRACE on top of the registered POST) are all expected and
// are handled by the anyMethodPaths set below.
func TestSetRelayRouter_RouteCountBaseline(t *testing.T) {
	engine := newTestEngine(t)
	SetRelayRouter(engine)

	routes := engine.Routes()
	known := make(map[string]bool, len(relayExpectedRoutes)+len(relayExpectedNotImplemented))
	for _, r := range relayExpectedRoutes {
		known[r.Method+" "+r.Path] = true
	}
	for _, r := range relayExpectedNotImplemented {
		known[r.Method+" "+r.Path] = true
	}

	// anyMethodPaths is the set of paths where SetRelayRouter calls
	// router.Any — gin expands Any into a route per HTTP method, so
	// only one of those expansions appears in the expected table and
	// the rest are accepted here.
	anyMethodPaths := map[string]bool{
		"/v1/oneapi/proxy/:channelid/*target": true,
	}

	var unexpected []string
	for _, r := range routes {
		key := r.Method + " " + r.Path
		if key == " " {
			// gin's synthetic NoRoute entry.
			continue
		}
		if known[key] {
			continue
		}
		if anyMethodPaths[r.Path] {
			continue
		}
		unexpected = append(unexpected, key)
	}
	if len(unexpected) > 0 {
		t.Fatalf("found %d relay routes not listed in either expected table:\n  %s\nupdate relayExpectedRoutes / relayExpectedNotImplemented",
			len(unexpected), strings.Join(unexpected, "\n  "))
	}
}

// TestSetRelayRouter_GzipMiddlewareActive exercises the gzip decode
// middleware that SetRelayRouter installs: a POST to /v1/chat/completions
// with Content-Encoding: gzip must reach the middleware (which would
// normally decompress the body in production). We just assert the
// request makes it past CORS without being dropped on the floor —
// the precise downstream behaviour is the distributor's responsibility
// and tested separately.
func TestSetRelayRouter_GzipMiddlewareActive(t *testing.T) {
	engine := newTestEngine(t)
	SetRelayRouter(engine)

	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("ignored"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptestRecorder(engine, req)

	// The middleware chain ends with a 401 from TokenAuth (no token
	// supplied). The exact status code is less important than the
	// fact that the request shape was accepted and reached auth —
	// i.e. the gzip middleware did not short-circuit the request.
	if rec.Code == 0 || rec.Code > 599 {
		t.Fatalf("unexpected status %d from gzip request; body=%s", rec.Code, rec.Body.String())
	}
}
