package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/ctxkey"
)

func TestRelayNotImplemented(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/edits", RelayNotImplemented)

	req := httptest.NewRequest(http.MethodGet, "/v1/edits", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}
}

func TestRelayNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/unknown", RelayNotFound)

	req := httptest.NewRequest(http.MethodGet, "/v1/unknown", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestShouldRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name       string
		statusCode int
		specificId bool
		want       bool
	}{
		{"no specific channel, 429", http.StatusTooManyRequests, false, true},
		{"no specific channel, 500", http.StatusInternalServerError, false, true},
		{"no specific channel, 502", http.StatusBadGateway, false, true},
		{"no specific channel, 200", http.StatusOK, false, false},
		{"no specific channel, 400", http.StatusBadRequest, false, false},
		{"specific channel, 429", http.StatusTooManyRequests, true, false},
		{"specific channel, 500", http.StatusInternalServerError, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if tc.specificId {
				c.Set(ctxkey.SpecificChannelId, "1")
			}
			if got := shouldRetry(c, tc.statusCode); got != tc.want {
				t.Errorf("shouldRetry(status=%d, specific=%v) = %v, want %v", tc.statusCode, tc.specificId, got, tc.want)
			}
		})
	}
}
