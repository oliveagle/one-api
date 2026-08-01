package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/i18n"
)

func TestCache_RootPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Cache())
	r.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "home")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control for / = %q, want no-cache", got)
	}
}

func TestCache_NonRootPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Cache())
	r.GET("/static/style.css", func(c *gin.Context) {
		c.String(http.StatusOK, "body{}")
	})

	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "max-age=604800" {
		t.Errorf("Cache-Control for /static/style.css = %q, want max-age=604800", got)
	}
}

func TestLanguage_Chinese(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Language())
	r.GET("/test", func(c *gin.Context) {
		lang := c.GetString(i18n.ContextKey)
		c.String(http.StatusOK, lang)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() != "zh-CN" {
		t.Errorf("language = %q, want zh-CN", rec.Body.String())
	}
}

func TestLanguage_English(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Language())
	r.GET("/test", func(c *gin.Context) {
		lang := c.GetString(i18n.ContextKey)
		c.String(http.StatusOK, lang)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() != "en" {
		t.Errorf("language = %q, want en", rec.Body.String())
	}
}

func TestLanguage_Default(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Language())
	r.GET("/test", func(c *gin.Context) {
		lang := c.GetString(i18n.ContextKey)
		c.String(http.StatusOK, lang)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() != "en" {
		t.Errorf("language = %q, want en", rec.Body.String())
	}
}

func TestRequestId_SetsHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestId())
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	id := rec.Header().Get(helper.RequestIdKey)
	if id == "" {
		t.Fatal("Request-Id header should be set")
	}
}

func TestRequestId_ContextValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestId())
	r.GET("/test", func(c *gin.Context) {
		id := helper.GetRequestID(c.Request.Context())
		c.String(http.StatusOK, id)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() == "" {
		t.Fatal("request ID should be set in context")
	}
}

func TestGzipDecode_DecompressesBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipDecodeMiddleware())
	r.POST("/test", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, string(body))
	})

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte(`{"hello":"world"}`))
	gz.Close()

	req := httptest.NewRequest(http.MethodPost, "/test", &buf)
	req.Header.Set("Content-Encoding", "gzip")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() != `{"hello":"world"}` {
		t.Errorf("body = %q, want {\"hello\":\"world\"}", rec.Body.String())
	}
}

func TestGzipDecode_NonGzipPassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipDecodeMiddleware())
	r.POST("/test", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.String(http.StatusOK, string(body))
	})

	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader([]byte(`plain text`)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Body.String() != "plain text" {
		t.Errorf("body = %q, want plain text", rec.Body.String())
	}
}

func TestIsModelInList(t *testing.T) {
	cases := []struct {
		name      string
		modelName string
		models    string
		want      bool
	}{
		{"exact match", "gpt-4o", "gpt-4o,gpt-4o-mini", true},
		{"no match", "claude-3", "gpt-4o,gpt-4o-mini", false},
		{"single model", "gpt-4o", "gpt-4o", true},
		{"empty list", "gpt-4o", "", false},
		{"trailing comma", "gpt-4o", "gpt-4o,", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isModelInList(tc.modelName, tc.models); got != tc.want {
				t.Errorf("isModelInList(%q, %q) = %v, want %v", tc.modelName, tc.models, got, tc.want)
			}
		})
	}
}

func TestGetRequestModel_Defaults(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{"moderation default", "/v1/moderations", `{"input":"test"}`, "text-moderation-stable"},
		{"images default", "/v1/images/generations", `{"prompt":"cat"}`, "dall-e-2"},
		{"audio transcription default", "/v1/audio/transcriptions", `{"file":"test"}`, "whisper-1"},
		{"audio translation default", "/v1/audio/translations", `{"file":"test"}`, "whisper-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			model, err := getRequestModel(c)
			if err != nil {
				t.Fatalf("getRequestModel: %v", err)
			}
			if model != tc.want {
				t.Errorf("getRequestModel(%s) = %q, want %q", tc.path, model, tc.want)
			}
		})
	}
}

func TestGetRequestModel_ExplicitModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	model, err := getRequestModel(c)
	if err != nil {
		t.Fatalf("getRequestModel: %v", err)
	}
	if model != "gpt-4o" {
		t.Errorf("getRequestModel = %q, want gpt-4o", model)
	}
}

func TestGetRequestModel_InvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	c.Request.Header.Set("Content-Type", "application/json")

	_, err := getRequestModel(c)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
