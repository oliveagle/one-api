package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
)

func TestGetStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	oldVersion := common.Version
	oldSystemName := config.SystemName
	t.Cleanup(func() {
		common.Version = oldVersion
		config.SystemName = oldSystemName
	})
	common.Version = "test-v1.0"
	config.SystemName = "Test API"

	r := gin.New()
	r.GET("/api/status", GetStatus)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response missing data field: %+v", resp)
	}
	if data["version"] != "test-v1.0" {
		t.Errorf("version = %v, want test-v1.0", data["version"])
	}
	if data["system_name"] != "Test API" {
		t.Errorf("system_name = %v, want Test API", data["system_name"])
	}
}

func TestGetNotice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	config.OptionMapRWMutex.Lock()
	if config.OptionMap == nil {
		config.OptionMap = make(map[string]string)
	}
	config.OptionMap["Notice"] = "Test notice content"
	config.OptionMapRWMutex.Unlock()

	r := gin.New()
	r.GET("/api/notice", GetNotice)

	req := httptest.NewRequest(http.MethodGet, "/api/notice", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGetAbout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	config.OptionMapRWMutex.Lock()
	if config.OptionMap == nil {
		config.OptionMap = make(map[string]string)
	}
	config.OptionMap["About"] = "Test about content"
	config.OptionMapRWMutex.Unlock()

	r := gin.New()
	r.GET("/api/about", GetAbout)

	req := httptest.NewRequest(http.MethodGet, "/api/about", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGetHomePageContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	config.OptionMapRWMutex.Lock()
	if config.OptionMap == nil {
		config.OptionMap = make(map[string]string)
	}
	config.OptionMap["HomePageContent"] = "Welcome!"
	config.OptionMapRWMutex.Unlock()

	r := gin.New()
	r.GET("/api/home", GetHomePageContent)

	req := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestGetGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	r := gin.New()
	r.GET("/api/groups", GetGroups)

	req := httptest.NewRequest(http.MethodGet, "/api/groups", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
