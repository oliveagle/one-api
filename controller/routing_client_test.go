package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
)

// newClientRoutingRequest builds a token-authenticated context for the
// client-facing routing endpoints.
func newClientRoutingRequest(t *testing.T, method, target, body, session string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if session != "" {
		r.Header.Set(sessionHeaderName(), session)
	}
	c.Request = r
	return c, rec
}

// Regression: resolveClientRouting used to leave the handler to re-parse the
// body with ShouldBindJSON. gin consumes Request.Body on the first parse, so the
// second read saw an empty body and `channel` was silently lost, making every
// pin request fail with "channel is required".
func TestParseClientRoutingRequest_ParsesBodyOnce(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin",
		`{"model":"coding_medium","channel":"minimax"}`, "sess-abc")
	rc, err := parseClientRoutingRequest(c)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rc.Model != "coding_medium" {
		t.Fatalf("model = %q", rc.Model)
	}
	if rc.Channel != "minimax" {
		t.Fatalf("channel = %q, want minimax (body parsed only once)", rc.Channel)
	}
	if rc.Session != "sess-abc" {
		t.Fatalf("session = %q", rc.Session)
	}
}

func TestParseClientRoutingRequest_QueryParamsFallback(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodGet,
		"/v1/oneapi/routing/nodes?model=coding_medium&channel=volc-1&session=q-sess", "", "")
	rc, err := parseClientRoutingRequest(c)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rc.Model != "coding_medium" || rc.Channel != "volc-1" || rc.Session != "q-sess" {
		t.Fatalf("got model=%q channel=%q session=%q", rc.Model, rc.Channel, rc.Session)
	}
}

// The session header takes precedence over the query fallback.
func TestParseClientRoutingRequest_HeaderBeatsQuerySession(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodGet,
		"/v1/oneapi/routing/nodes?model=coding_medium&session=from-query", "", "from-header")
	rc, err := parseClientRoutingRequest(c)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rc.Session != "from-header" {
		t.Fatalf("session = %q, want from-header", rc.Session)
	}
}

func TestParseClientRoutingRequest_ModelRequired(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin",
		`{"channel":"minimax"}`, "s")
	if _, err := parseClientRoutingRequest(c); err == nil {
		t.Fatal("missing model should error")
	}
}

// A token restricted to a model list must not be able to inspect or move
// routing for a model it cannot use.
func TestParseClientRoutingRequest_RejectsModelOutsideTokenAllowlist(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin",
		`{"model":"secret-model","channel":"minimax"}`, "s")
	c.Set("available_models", "coding_medium,gpt-4o-mini")
	_, err := parseClientRoutingRequest(c)
	if err == nil {
		t.Fatal("model outside the token allowlist should be rejected")
	}
	if !strings.Contains(err.Error(), "secret-model") {
		t.Fatalf("error should name the model, got %v", err)
	}
}

func TestParseClientRoutingRequest_AllowsModelInsideTokenAllowlist(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin",
		`{"model":"coding_medium","channel":"minimax"}`, "s")
	c.Set("available_models", "coding_medium,gpt-4o-mini")
	if _, err := parseClientRoutingRequest(c); err != nil {
		t.Fatalf("allowlisted model should pass, got %v", err)
	}
}

// Pin/cycle/unpin are session-scoped: without a session there is nothing to
// move, and the error must tell the caller which header to send.
func TestClientRoutingEndpoints_RequireSession(t *testing.T) {
	for name, handler := range map[string]func(*gin.Context){
		"pin":   PinClientRoutingNode,
		"cycle": CycleClientRoutingNode,
		"unpin": UnpinClientRoutingNode,
	} {
		c, rec := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/"+name,
			`{"model":"coding_medium","channel":"minimax"}`, "")
		handler(c)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", name, rec.Code)
		}
		var resp struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if resp.Success {
			t.Fatalf("%s: should not succeed without a session", name)
		}
		if !strings.Contains(resp.Message, sessionHeaderName()) {
			t.Fatalf("%s: message should name the session header, got %q", name, resp.Message)
		}
	}
}

// A pin request with a session but no channel must still be rejected. The
// parser records an empty Channel, which the handler turns into an error before
// it does any routing work.
func TestParseClientRoutingRequest_MissingChannelLeavesItEmpty(t *testing.T) {
	c, _ := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin",
		`{"model":"coding_medium"}`, "sess-1")
	rc, err := parseClientRoutingRequest(c)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rc.Channel != "" {
		t.Fatalf("channel = %q, want empty", rc.Channel)
	}
	if rc.Session != "sess-1" {
		t.Fatalf("session = %q", rc.Session)
	}
}

// Errors are returned in the OpenAI error shape because these routes live under
// /v1 and clients already parse that format. A missing model is rejected before
// any cache/DB access, so this exercises the real handler.
func TestClientRoutingError_UsesOpenAIErrorShape(t *testing.T) {
	c, rec := newClientRoutingRequest(t, http.MethodPost, "/v1/oneapi/routing/pin", `{}`, "s")
	PinClientRoutingNode(c)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var resp struct {
		Success bool `json:"success"`
		Error   struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Success {
		t.Fatal("should not report success")
	}
	if resp.Error.Message == "" || resp.Error.Type != "one_api_error" {
		t.Fatalf("unexpected error shape: %s", rec.Body.String())
	}
}

func TestSessionHeaderName_FallsBackWhenConfigEmpty(t *testing.T) {
	old := config.SessionIdHeader
	t.Cleanup(func() { config.SessionIdHeader = old })

	config.SessionIdHeader = "X-Custom-Session"
	if got := sessionHeaderName(); got != "X-Custom-Session" {
		t.Fatalf("got %q", got)
	}
	config.SessionIdHeader = "   "
	if got := sessionHeaderName(); got != "X-Session-Id" {
		t.Fatalf("blank config should fall back to X-Session-Id, got %q", got)
	}
}

func TestTokenAllowsModel(t *testing.T) {
	if !tokenAllowsModel("coding_medium", "a,coding_medium,b") {
		t.Fatal("should match an exact entry")
	}
	if tokenAllowsModel("coding", "a,coding_medium,b") {
		t.Fatal("must not match on prefix")
	}
	if tokenAllowsModel("x", "") {
		t.Fatal("empty list matches nothing")
	}
}
