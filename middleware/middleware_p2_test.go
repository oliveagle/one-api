package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/helper"
)

func TestRelayPanicRecover_NoPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RelayPanicRecover())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", rec.Body.String())
	}
}

func TestRelayPanicRecover_CatchesPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RelayPanicRecover())
	r.GET("/panic", func(c *gin.Context) {
		panic("test panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Panic detected") {
		t.Errorf("body should mention panic, got %q", rec.Body.String())
	}
}

func TestSetUpLogger_Format(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestId())
	SetUpLogger(r)

	var buf bytes.Buffer
	r.Use(gin.LoggerWithWriter(&buf))

	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "[GIN]") {
		t.Errorf("log should contain [GIN], got %q", logOutput)
	}
	if !strings.Contains(logOutput, "200") {
		t.Errorf("log should contain status 200, got %q", logOutput)
	}
}

func TestAbortWithMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/test", nil)
	c.Set(helper.RequestIdKey, "req-123")

	abortWithMessage(c, http.StatusBadRequest, "invalid input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "invalid input") {
		t.Errorf("body should contain error message, got %q", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "req-123") {
		t.Errorf("body should contain request ID, got %q", w.Body.String())
	}
}
