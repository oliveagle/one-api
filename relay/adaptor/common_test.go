package adaptor

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
)

// stubAdaptor is a minimal Adaptor implementation for exercising the
// package-level helpers DoRequestHelper depends on (GetRequestURL and
// SetupRequestHeader). It does not need to satisfy the rest of the Adaptor
// interface — DoRequestHelper only calls those two plus the package's own
// DoRequest.
type stubAdaptor struct {
	url         string
	urlErr      error
	headerErr   error
	headerCalls *http.Request
}

func (s stubAdaptor) Init(*meta.Meta) {}
func (s stubAdaptor) GetRequestURL(*meta.Meta) (string, error) {
	return s.url, s.urlErr
}
func (s stubAdaptor) SetupRequestHeader(_ *gin.Context, req *http.Request, _ *meta.Meta) error {
	s.headerCalls = req
	return s.headerErr
}
func (s stubAdaptor) ConvertRequest(*gin.Context, int, *model.GeneralOpenAIRequest) (any, error) {
	return nil, nil
}
func (s stubAdaptor) ConvertImageRequest(*model.ImageRequest) (any, error) { return nil, nil }
func (s stubAdaptor) DoRequest(*gin.Context, *meta.Meta, io.Reader) (*http.Response, error) {
	return nil, nil
}
func (s stubAdaptor) DoResponse(*gin.Context, *http.Response, *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	return nil, nil
}
func (s stubAdaptor) GetModelList() []string { return nil }
func (s stubAdaptor) GetChannelName() string { return "stub" }

// SetupCommonRequestHeader must copy Content-Type and Accept verbatim from
// the incoming request. If upstream returns a JSON response the relayed
// request still has to carry the same Content-Type, otherwise the upstream
// 415s.
func TestSetupCommonRequestHeader_CopiesContentTypeAndAccept(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
	c.Request.Header.Set("Accept", "text/event-stream")

	req, err := http.NewRequest(http.MethodPost, "http://upstream/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	SetupCommonRequestHeader(c, req, &meta.Meta{IsStream: false})

	if got := req.Header.Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q, want application/json; charset=utf-8", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
}

// SSE streaming without an explicit Accept: the helper must fill in
// text/event-stream so the upstream knows we want the chunked protocol.
func TestSetupCommonRequestHeader_StreamFillsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	// No Accept header.

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	SetupCommonRequestHeader(c, req, &meta.Meta{IsStream: true})

	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", got)
	}
}

// SSE streaming with an explicit Accept must win over the SSE default — the
// caller asked for something specific, don't override it.
func TestSetupCommonRequestHeader_StreamRespectsExplicitAccept(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Accept", "application/json")

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	SetupCommonRequestHeader(c, req, &meta.Meta{IsStream: true})

	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json (caller's value preserved)", got)
	}
}

// Non-stream requests must NOT auto-fill SSE. Some upstreams (Anthropic) treat
// any text/event-stream Accept as a hint to keep the response open forever.
func TestSetupCommonRequestHeader_NonStreamLeavesAcceptAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	SetupCommonRequestHeader(c, req, &meta.Meta{IsStream: false})

	if got := req.Header.Get("Accept"); got != "" {
		t.Errorf("Accept = %q, want empty (non-stream)", got)
	}
}

// DoRequestHelper must return the wrapped error from GetRequestURL so the
// caller can distinguish "wrong URL" from "network failed".
func TestDoRequestHelper_PropagatesGetRequestURLError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	a := stubAdaptor{urlErr: errors.New("no upstream")}
	_, err := DoRequestHelper(a, c, &meta.Meta{}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "get request url failed") {
		t.Fatalf("expected wrapped GetRequestURL error, got %v", err)
	}
}

// SetupRequestHeader errors must short-circuit DoRequestHelper before any
// network call is made.
func TestDoRequestHelper_PropagatesSetupHeaderError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	a := stubAdaptor{url: "http://upstream", headerErr: errors.New("bad auth")}
	_, err := DoRequestHelper(a, c, &meta.Meta{}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "setup request header failed") {
		t.Fatalf("expected wrapped SetupRequestHeader error, got %v", err)
	}
}

// Happy path: HTTPClient is redirected to a real httptest server and the
// returned response must carry the upstream status code.
func TestDoRequestHelper_HappyPath(t *testing.T) {
	// Swap in a client that points at our test server. Restore on cleanup.
	oldClient := client.HTTPClient
	t.Cleanup(func() { client.HTTPClient = oldClient })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	client.HTTPClient = srv.Client()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	a := stubAdaptor{url: srv.URL + "/v1/chat/completions"}
	resp, err := DoRequestHelper(a, c, &meta.Meta{}, strings.NewReader(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("DoRequestHelper: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Errorf("body = %q", string(body))
	}
}

// Network errors (connection refused) must surface wrapped so the caller can
// log "do request failed" without losing the underlying message.
func TestDoRequestHelper_DoRequestError(t *testing.T) {
	oldClient := client.HTTPClient
	t.Cleanup(func() { client.HTTPClient = oldClient })

	// A client pointed at a port that is guaranteed to be closed (loopback,
	// ephemeral). The scheme and transport don't matter — opening the conn
	// fails.
	client.HTTPClient = &http.Client{}

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	a := stubAdaptor{url: "http://127.0.0.1:1/v1/chat/completions"}
	_, err := DoRequestHelper(a, c, &meta.Meta{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("expected connection error")
	}
	if !strings.Contains(err.Error(), "do request failed") {
		t.Fatalf("error chain should mention 'do request failed', got %q", err.Error())
	}
}

// closeTrackingBody records whether Close was called, so tests can assert
// that DoRequest released the request body on the success path.
type closeTrackingBody struct {
	io.Reader
	closed bool
}

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

// DoRequest must close req.Body and c.Request.Body even on the success path
// so a slow upstream cannot leak file descriptors. We assert by feeding
// bodies that record their Close calls. Nil request bodies (GET-style
// upstream calls) must also not panic.
func TestDoRequest_ClosesBodies(t *testing.T) {
	oldClient := client.HTTPClient
	t.Cleanup(func() { client.HTTPClient = oldClient })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	client.HTTPClient = srv.Client()

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	clientBody := &closeTrackingBody{Reader: strings.NewReader(`{"model":"x"}`)}
	c.Request.Body = clientBody

	reqBody := &closeTrackingBody{Reader: strings.NewReader(`{"model":"x"}`)}
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"model":"x"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Body = reqBody
	resp, err := DoRequest(c, req)
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	defer resp.Body.Close()

	if !reqBody.closed {
		t.Error("req.Body was not closed")
	}
	if !clientBody.closed {
		t.Error("c.Request.Body was not closed")
	}

	// A request with a nil body must not panic inside DoRequest.
	getReq, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := DoRequest(c, getReq); err != nil {
		t.Fatalf("DoRequest with nil body: %v", err)
	}
}

// SetupCommonRequestHeader must inject channel-configured custom headers.
func TestSetupCommonRequestHeader_InjectsCustomHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	m := &meta.Meta{
		IsStream: false,
		Headers:  map[string]string{"User-Agent": "opencode", "X-Custom": "test-value"},
	}
	SetupCommonRequestHeader(c, req, m)

	if got := req.Header.Get("User-Agent"); got != "opencode" {
		t.Errorf("User-Agent = %q, want opencode", got)
	}
	if got := req.Header.Get("X-Custom"); got != "test-value" {
		t.Errorf("X-Custom = %q, want test-value", got)
	}
}

// Custom headers must override same-name headers set by the common setup.
func TestSetupCommonRequestHeader_OverridesSameNameHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept", "text/event-stream")

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	// The channel configures Accept to something different — it should win.
	m := &meta.Meta{
		IsStream: true,
		Headers:  map[string]string{"Accept": "application/json"},
	}
	SetupCommonRequestHeader(c, req, m)

	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json (channel override)", got)
	}
}

// When no headers are configured (nil map), nothing extra is injected.
func TestSetupCommonRequestHeader_NilHeadersNoInjection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Request.Header.Set("Content-Type", "application/json")

	req, err := http.NewRequest(http.MethodPost, "http://upstream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	m := &meta.Meta{IsStream: false, Headers: nil}
	SetupCommonRequestHeader(c, req, m)

	// No custom headers should have been added.
	if got := req.Header.Get("User-Agent"); got != "" {
		t.Errorf("User-Agent = %q, want empty (no injection)", got)
	}
	if got := req.Header.Get("X-Custom"); got != "" {
		t.Errorf("X-Custom = %q, want empty (no injection)", got)
	}
}
