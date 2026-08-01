package testutil

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// MockHTTPClient returns an *http.Client whose RoundTripper responds to
// requests without touching the network. Callers register expected
// request/response pairs via Respond(), or use Match() to register a
// wildcard responder.
//
// Tests should construct the client via this helper rather than calling
// http.DefaultClient so that http.Get / http.Post etc. inside the
// production code can be redirected through the mock via the global
// http.DefaultTransport override (see InstallDefaultTransport).
//
// All calls are concurrency-safe and may be used from t.Parallel() tests.
func NewMockHTTPClient(t *testing.T) *http.Client {
	t.Helper()
	m := newMockTransportWithT(t)
	t.Cleanup(m.Close)
	return &http.Client{Transport: m}
}

// NewMockTransport returns the underlying RoundTripper so callers that
// need to share a single transport between multiple *http.Client
// instances can do so without leaking state across tests.
func NewMockTransport(t *testing.T) *MockTransport {
	t.Helper()
	m := newMockTransportWithT(t)
	t.Cleanup(m.Close)
	return m
}

// MockTransport is an http.RoundTripper that dispatches to handlers
// registered via Respond or Match. Unhandled requests produce a clear
// error via t.Fatal so test wiring bugs surface immediately instead of
// silently falling through to the network.
type MockTransport struct {
	t        *testing.T
	mu       sync.Mutex
	exact    map[string]http.Handler // method+URL → handler
	patterns []matchEntry
}

// matchEntry is a registered wildcard responder; later entries override
// earlier ones (most-recent-wins), matching httprouter semantics.
type matchEntry struct {
	method  string
	prefix  string
	handler http.Handler
}

func newMockTransport() *MockTransport {
	return &MockTransport{exact: map[string]http.Handler{}}
}

// newMockTransportWithT is identical to newMockTransport but stores *t
// so that Respond/Match/RoundTrip can route test-failure signals back to
// the originating test. Public wrappers (NewMockHTTPClient /
// NewMockTransport) pass the caller's t through.
func newMockTransportWithT(t *testing.T) *MockTransport {
	m := newMockTransport()
	m.t = t
	return m
}

// Respond registers a fixed response for the exact (method, url) pair.
// Pass an http.HandlerFunc, an http.Handler, or use NewBytesHandler to
// build one from raw bytes + status.
func (m *MockTransport) Respond(method, url string, handler http.Handler) {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exact[strings.ToUpper(method)+" "+url] = handler
}

// Match registers a fallback responder that matches by HTTP method and
// URL prefix. The first match wins; later Match calls are checked first
// (LIFO), so the most specific responder should be registered last.
func (m *MockTransport) Match(method, prefix string, handler http.Handler) {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.patterns = append([]matchEntry{{strings.ToUpper(method), prefix, handler}}, m.patterns...)
}

// RoundTrip satisfies http.RoundTripper.
func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	key := req.Method + " " + req.URL.String()
	if h, ok := m.exact[key]; ok {
		m.mu.Unlock()
		return runHandler(h, req)
	}
	for _, p := range m.patterns {
		if p.method == req.Method && strings.HasPrefix(req.URL.String(), p.prefix) {
			m.mu.Unlock()
			return runHandler(p.handler, req)
		}
	}
	m.mu.Unlock()
	m.t.Errorf("testutil: unhandled mock HTTP request: %s %s (register via MockTransport.Respond/Match)", req.Method, req.URL.String())
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(bytes.NewReader([]byte("unhandled mock request"))),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

// Close releases internal state. Hooked to t.Cleanup automatically.
func (m *MockTransport) Close() {}

// NewBytesHandler returns an http.Handler that always responds with the
// given status, headers and body bytes. Content-Length is set if not
// provided in headers.
func NewBytesHandler(status int, headers http.Header, body []byte) http.Handler {
	if headers == nil {
		headers = http.Header{}
	}
	headers.Set("Content-Length", itoaBytes(uint64(len(body))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, vs := range headers {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	})
}

// NewJSONHandler is a convenience wrapper for application/json
// responses.
func NewJSONHandler(status int, body []byte) http.Handler {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return NewBytesHandler(status, h, body)
}

func runHandler(h http.Handler, req *http.Request) (*http.Response, error) {
	// The recorded handler expects an http.Request with a populated URL;
	// RoundTrip has already done that for us.
	rec := &recordingResponseWriter{header: http.Header{}}
	h.ServeHTTP(rec, req)
	return &http.Response{
		StatusCode: rec.status,
		Header:     rec.header,
		Body:       io.NopCloser(bytes.NewReader(rec.body.Bytes())),
		Request:    req,
	}, nil
}

type recordingResponseWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *recordingResponseWriter) Header() http.Header { return r.header }
func (r *recordingResponseWriter) WriteHeader(s int)   { r.status = s }
func (r *recordingResponseWriter) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(p)
}

// ParseURL is a tiny helper that wraps url.Parse and fatals on error.
// Saves boilerplate at every Respond call site.
func ParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("testutil: bad URL %q: %v", raw, err)
	}
	return u
}

// itoaBytes is a local helper that converts an unsigned integer to its
// base-10 string form without importing strconv at this scope.
func itoaBytes(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
