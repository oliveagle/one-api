package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/testutil"
)

// newPreflightRequest builds a CORS preflight OPTIONS request with
// the Origin and Access-Control-Request-Method headers a browser
// would send. The router/CORS middleware short-circuits on the
// OPTIONS method before any auth runs, so it's a cheap way to probe
// the middleware chain without having to satisfy TokenAuth.
func newPreflightRequest(method, path string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", method)
	return req
}

// httptestRecorder wraps engine.ServeHTTP into a one-liner that
// callers use when they need to build their own request (for example
// for preflight or rate-limit headers that httptest.NewRequest alone
// does not set).
func httptestRecorder(engine *gin.Engine, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// newTestEngine returns a gin engine wired into TestMode with Redis
// disabled. Each call returns a fresh engine; callers are expected to
// mount whichever router they want to exercise on top of it.
//
// The setup is intentionally minimal: we deliberately do NOT install
// the session middleware (router/api.go only references it via the
// TurnstileCheck middleware, which we want to keep inert during
// routing tests by leaving TurnstileCheckEnabled unset).
func newTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	testutil.DisableRedis(t)
	return gin.New()
}

// findRoute looks up a registered (method, path) tuple in the engine's
// route table and returns it, or nil if no match.
func findRoute(routes gin.RoutesInfo, method, path string) *gin.RouteInfo {
	for i := range routes {
		if routes[i].Method == method && routes[i].Path == path {
			return &routes[i]
		}
	}
	return nil
}

// recordResponse issues an in-memory HTTP request against engine via
// httptest.NewRecorder and returns the recorder so the caller can
// inspect status / body / headers. It does not call any global init.
func recordResponse(engine *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}
