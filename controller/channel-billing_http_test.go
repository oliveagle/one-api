package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/client"
	"github.com/songquanpeng/one-api/model"
)

// aihubmixQuotaPerUnit must be bounded. RELAY_TIMEOUT defaults to 0, which makes
// client.HTTPClient wait forever, so a hung /api/status would block the balance
// refresh indefinitely. The auxiliary lookup uses ImpatientHTTPClient (5s) and
// degrades to the default rather than propagating the failure.
func TestAIHubMixQuotaPerUnitIsBoundedWhenUpstreamHangs(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked // never respond within the test's patience
	}))
	defer func() {
		close(blocked)
		srv.Close()
	}()

	// A short-timeout client stands in for ImpatientHTTPClient so the test stays
	// fast while exercising the same code path.
	original := client.ImpatientHTTPClient
	client.ImpatientHTTPClient = &http.Client{Timeout: 200 * time.Millisecond}
	t.Cleanup(func() { client.ImpatientHTTPClient = original })

	base := srv.URL
	ch := &model.Channel{Type: 52, BaseURL: &base}

	done := make(chan float64, 1)
	go func() { done <- aihubmixQuotaPerUnit(ch) }()

	select {
	case got := <-done:
		if got != aihubmixDefaultQuotaPerUnit {
			t.Fatalf("quota_per_unit = %v, want the default %v on timeout", got, aihubmixDefaultQuotaPerUnit)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("aihubmixQuotaPerUnit did not return: the lookup is unbounded")
	}
}

// A non-200 /api/status must degrade to the default, not abort the refresh.
func TestAIHubMixQuotaPerUnitFallsBackOnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	base := srv.URL
	ch := &model.Channel{Type: 52, BaseURL: &base}
	if got := aihubmixQuotaPerUnit(ch); got != aihubmixDefaultQuotaPerUnit {
		t.Fatalf("quota_per_unit = %v, want default %v", got, aihubmixDefaultQuotaPerUnit)
	}
}

// The upstream value must win when it is available, so that an AIHubMix
// deployment using a different unit is reported correctly.
func TestAIHubMixQuotaPerUnitUsesUpstreamValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" {
			t.Errorf("unexpected path %q, want /api/status", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"quota_per_unit":1000000},"success":true}`))
	}))
	defer srv.Close()

	base := srv.URL
	ch := &model.Channel{Type: 52, BaseURL: &base}
	if got := aihubmixQuotaPerUnit(ch); got != 1000000 {
		t.Fatalf("quota_per_unit = %v, want 1000000 from upstream", got)
	}
}

// trackedBody records whether Close was called.
type trackedBody struct {
	io.ReadCloser
	closed *bool
}

func (b trackedBody) Close() error {
	*b.closed = true
	return b.ReadCloser.Close()
}

// trackingTransport wraps each response body so the test can observe Close.
type trackingTransport struct {
	base   http.RoundTripper
	closed *bool
}

func (t trackingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	res.Body = trackedBody{ReadCloser: res.Body, closed: t.closed}
	return res, nil
}

// GetResponseBody used to return early on a non-200 without closing the body,
// leaking the connection until GC. Every exit path must close it.
func TestGetResponseBodyClosesBodyOnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer srv.Close()

	var closed bool
	originalHTTP := client.HTTPClient
	client.HTTPClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: trackingTransport{base: http.DefaultTransport, closed: &closed},
	}
	t.Cleanup(func() { client.HTTPClient = originalHTTP })

	ch := &model.Channel{Type: 52}
	if _, err := GetResponseBody("GET", srv.URL, ch, http.Header{}); err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !closed {
		t.Fatal("response body was not closed on the non-200 path")
	}
}

// The success path must close the body too.
func TestGetResponseBodyClosesBodyOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	var closed bool
	originalHTTP := client.HTTPClient
	client.HTTPClient = &http.Client{
		Timeout:   5 * time.Second,
		Transport: trackingTransport{base: http.DefaultTransport, closed: &closed},
	}
	t.Cleanup(func() { client.HTTPClient = originalHTTP })

	ch := &model.Channel{Type: 52}
	body, err := GetResponseBody("GET", srv.URL, ch, http.Header{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != `{"success":true}` {
		t.Fatalf("body = %q", body)
	}
	if !closed {
		t.Fatal("response body was not closed on the success path")
	}
}
