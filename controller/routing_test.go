package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/routing"
)

func TestGetRoutingStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Seed the process-wide router store.
	store := routing.DefaultRouter().Store()
	store.Clear()
	store.Touch(storeKey("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	store.Touch(storeKey("g", "coding_medium", "sess-2"), "sess-2", "g", "coding_medium", 20)
	store.Touch(storeKey("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	defer store.Clear()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/routing/status", nil)

	GetRoutingStatus(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Enabled    bool                    `json:"enabled"`
			SessionTTL int                     `json:"session_ttl_seconds"`
			Sessions   []routing.SessionRecord `json:"sessions"`
			Channels   []routing.ChannelState  `json:"channels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, body=%s", rec.Body.String())
	}
	if len(resp.Data.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(resp.Data.Sessions))
	}
	if resp.Data.SessionTTL <= 0 {
		t.Fatalf("expected positive session ttl, got %d", resp.Data.SessionTTL)
	}
	// sess-1 has 2 requests, sess-2 has 1.
	byKey := map[string]int64{}
	for _, s := range resp.Data.Sessions {
		byKey[s.SessionKey] = s.Requests
	}
	if byKey["sess-1"] != 2 || byKey["sess-2"] != 1 {
		t.Fatalf("unexpected request counts: %v", byKey)
	}
	// Channel 10 holds 1 session (sess-1), channel 20 holds 1 (sess-2).
	chByID := map[int]int{}
	for _, ch := range resp.Data.Channels {
		chByID[ch.ChannelId] = ch.Sessions
	}
	if chByID[10] != 1 || chByID[20] != 1 {
		t.Fatalf("unexpected channel session counts: %v", chByID)
	}
}

func TestDeleteRoutingSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := routing.DefaultRouter().Store()
	store.Clear()
	store.Touch(storeKey("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	store.Touch(storeKey("g", "coding_medium", "sess-2"), "sess-2", "g", "coding_medium", 10)
	defer store.Clear()

	body := `{"session_key":"sess-1"}`
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/routing/session", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	DeleteRoutingSession(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Removed int `json:"removed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success || resp.Data.Removed != 1 {
		t.Fatalf("expected removed=1, got %+v body=%s", resp, rec.Body.String())
	}
	if len(store.Snapshot()) != 1 {
		t.Fatalf("expected 1 remaining session, got %d", len(store.Snapshot()))
	}
}

func TestClearRoutingSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := routing.DefaultRouter().Store()
	store.Clear()
	store.Touch(storeKey("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	defer store.Clear()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/api/routing/sessions", nil)
	ClearRoutingSessions(c)

	if len(store.Snapshot()) != 0 {
		t.Fatalf("expected all sessions cleared, got %d", len(store.Snapshot()))
	}
}

func storeKey(group, model, session string) string {
	return group + "\x00" + model + "\x00" + session
}
