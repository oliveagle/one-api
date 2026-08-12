package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/routing"
)

func TestGetRoutingStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	// Seed a channel so the controller can look up its name and enrich the
	// ChannelState / channel_names.
	if err := model.DB.Create(&model.Channel{
		Id:           10,
		Name:         "test-upstream-a",
		Type:         1,
		Status:       model.ChannelStatusEnabled,
		ResponseTime: 800,
		Balance:      5.0,
	}).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}

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
			Enabled      bool                    `json:"enabled"`
			SessionTTL   int                     `json:"session_ttl_seconds"`
			ChannelNames map[int]string          `json:"channel_names"`
			Sessions     []routing.SessionRecord `json:"sessions"`
			Channels     []routing.ChannelState  `json:"channels"`
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
	// Channel 10's name must be enriched from the DB.
	if resp.Data.ChannelNames[10] != "test-upstream-a" {
		t.Fatalf("channel 10 name not enriched, got %q", resp.Data.ChannelNames[10])
	}
	// The enriched ChannelState for 10 must carry the DB metadata.
	var ch10 *routing.ChannelState
	for i := range resp.Data.Channels {
		if resp.Data.Channels[i].ChannelId == 10 {
			ch10 = &resp.Data.Channels[i]
			break
		}
	}
	if ch10 == nil {
		t.Fatalf("channel 10 missing from channels, got %+v", resp.Data.Channels)
	}
	if ch10.Name != "test-upstream-a" || ch10.Balance != 5.0 || ch10.ResponseTime != 800 || ch10.Status != model.ChannelStatusEnabled {
		t.Fatalf("channel 10 not enriched with DB metadata: %+v", *ch10)
	}
}

func TestGetRoutingStatusSortsByBusyness(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupMockDB(t)

	// Channel 10: enabled, fast, healthy -> highest busyness.
	// Channel 20: enabled but quota exhausted -> sunk.
	// Channel 30: auto-disabled -> sunk lowest.
	if err := model.DB.Create(&[]model.Channel{
		{Id: 10, Name: "busy-a", Type: 1, Status: model.ChannelStatusEnabled, ResponseTime: 200, Balance: 50},
		{Id: 20, Name: "quota-out", Type: 1, Status: model.ChannelStatusEnabled, ResponseTime: 5000, Balance: 0},
		{Id: 30, Name: "disabled-c", Type: 1, Status: model.ChannelStatusAutoDisabled, ResponseTime: 300, Balance: 50},
	}).Error; err != nil {
		t.Fatalf("seed channels: %v", err)
	}

	store := routing.DefaultRouter().Store()
	store.Clear()
	// 3 sessions on channel 10, 1 session on 20, 1 on 30.
	store.Touch(storeKey("g", "m", "s1"), "s1", "g", "m", 10)
	store.Touch(storeKey("g", "m", "s2"), "s2", "g", "m", 10)
	store.Touch(storeKey("g", "m", "s3"), "s3", "g", "m", 10)
	store.Touch(storeKey("g", "m", "s4"), "s4", "g", "m", 20)
	store.Touch(storeKey("g", "m", "s5"), "s5", "g", "m", 30)
	defer store.Clear()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/routing/status", nil)
	GetRoutingStatus(c)

	var resp struct {
		Data struct {
			Channels []routing.ChannelState `json:"channels"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data.Channels) != 3 {
		t.Fatalf("expected 3 channels, got %d", len(resp.Data.Channels))
	}
	// Descending busyness: channel 10 must come first, and the auto-disabled
	// channel 30 must come last.
	if resp.Data.Channels[0].ChannelId != 10 {
		t.Fatalf("expected busiest channel 10 first, got %d", resp.Data.Channels[0].ChannelId)
	}
	if resp.Data.Channels[2].ChannelId != 30 {
		t.Fatalf("expected disabled channel 30 last, got %d", resp.Data.Channels[2].ChannelId)
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
