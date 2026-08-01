package controller

import (
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/model"
)

// seedChannel inserts a channel row.
func seedChannel(t *testing.T, id int, name, key string, status int, chType int) *model.Channel {
	t.Helper()
	ch := &model.Channel{
		Id:          id,
		Name:        name,
		Type:        chType,
		Key:         key,
		Status:      status,
		Models:      "gpt-3.5-turbo",
		CreatedTime: helper.GetTimestamp(),
	}
	if err := model.DB.Create(ch).Error; err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return ch
}

// GetAllChannels returns the list of non-deleted channels.
func TestGetAllChannels_Empty(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	GetAllChannels(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
}

// GetAllChannels with seeded rows returns them.
func TestGetAllChannels_ReturnsChannels(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "openai-1", "sk-1", model.ChannelStatusEnabled, 1)
	seedChannel(t, 2, "anthropic-1", "sk-2", model.ChannelStatusEnabled, 2)

	c, rec := withUserContext(t, 99, 10, "admin")
	GetAllChannels(c)
	body := rec.Body.String()
	if !strings.Contains(body, "openai-1") || !strings.Contains(body, "anthropic-1") {
		t.Fatalf("missing channels: %s", body)
	}
}

// SearchChannels returns channels matching the keyword.
func TestSearchChannels(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "openai-1", "sk-1", model.ChannelStatusEnabled, 1)
	seedChannel(t, 2, "anthropic-1", "sk-2", model.ChannelStatusEnabled, 2)

	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewGetRequest("/api/channel/search?keyword=openai")
	SearchChannels(c)
	body := rec.Body.String()
	if !strings.Contains(body, "openai-1") {
		t.Fatalf("missing openai match: %s", body)
	}
	if strings.Contains(body, "anthropic-1") {
		t.Fatalf("keyword filter leaked: %s", body)
	}
}

// GetChannel with a real id returns the channel payload.
func TestGetChannel_Found(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "alpha", "sk-1", model.ChannelStatusEnabled, 1)

	c, rec := withUserContext(t, 99, 10, "admin")
	c.Params = append(c.Params, ginParam("id", "1"))
	GetChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "alpha") {
		t.Fatalf("missing name: %s", rec.Body.String())
	}
}

// GetChannel with a bad id returns failure.
func TestGetChannel_BadID(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	c.Params = append(c.Params, ginParam("id", "not-num"))
	GetChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// GetChannel with an unknown id returns failure.
func TestGetChannel_NotFound(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	c.Params = append(c.Params, ginParam("id", "999"))
	GetChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// AddChannel with bad JSON fails.
func TestAddChannel_BadJSON(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	AddChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}

// AddChannel with a single key inserts one row.
func TestAddChannel_SingleKey(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewPostJSONRequest("/api/channel/",
		`{"name":"alpha","type":1,"key":"sk-only-one"}`)
	AddChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var ch model.Channel
	if err := model.DB.First(&ch, "name = ?", "alpha").Error; err != nil {
		t.Fatalf("channel not created: %v", err)
	}
	if ch.Key != "sk-only-one" {
		t.Fatalf("key = %q, want sk-only-one", ch.Key)
	}
}

// AddChannel with newline-separated keys creates one channel per key.
func TestAddChannel_MultiKey(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewPostJSONRequest("/api/channel/",
		`{"name":"alpha","type":1,"key":"sk-1\nsk-2\nsk-3"}`)
	AddChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var channels []model.Channel
	if err := model.DB.Where("name = ?", "alpha").Find(&channels).Error; err != nil {
		t.Fatalf("query channels: %v", err)
	}
	if len(channels) != 3 {
		t.Fatalf("channels = %d, want 3", len(channels))
	}
}

// AddChannel skips empty lines in keys.
func TestAddChannel_EmptyLines(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewPostJSONRequest("/api/channel/",
		`{"name":"alpha","type":1,"key":"sk-1\n\nsk-2\n"}`)
	AddChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var channels []model.Channel
	_ = model.DB.Where("name = ?", "alpha").Find(&channels)
	if len(channels) != 2 {
		t.Fatalf("channels = %d, want 2 (empty lines skipped)", len(channels))
	}
}

// DeleteChannel with a real id removes the row.
func TestDeleteChannel_Success(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "alpha", "sk-1", model.ChannelStatusEnabled, 1)

	c, rec := withUserContext(t, 99, 10, "admin")
	c.Params = append(c.Params, ginParam("id", "1"))
	DeleteChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var ch model.Channel
	if err := model.DB.First(&ch, "id = ?", 1).Error; err == nil {
		t.Fatalf("channel still exists after delete")
	}
}

// DeleteDisabledChannel deletes all disabled channels and returns the count.
// Without any channels in the DB the count is 0.
func TestDeleteDisabledChannel_NoRows(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	DeleteDisabledChannel(c)
	body := rec.Body.String()
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("expected success, got %s", body)
	}
	if !strings.Contains(body, `"data":0`) {
		t.Fatalf("expected data:0, got %s", body)
	}
}

// DeleteDisabledChannel returns the count of channels deleted.
func TestDeleteDisabledChannel_WithDisabled(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "alpha", "sk-1", model.ChannelStatusManuallyDisabled, 1)
	seedChannel(t, 2, "beta", "sk-2", model.ChannelStatusEnabled, 1)
	seedChannel(t, 3, "gamma", "sk-3", model.ChannelStatusAutoDisabled, 1)

	c, rec := withUserContext(t, 99, 10, "admin")
	DeleteDisabledChannel(c)
	body := rec.Body.String()
	if !strings.Contains(body, `"success":true`) {
		t.Fatalf("expected success, got %s", body)
	}
	// data should report "deleted rows": the count of disabled channels
	if !strings.Contains(body, `"data":2`) {
		t.Fatalf("expected data:2, got %s", body)
	}
	var remaining []model.Channel
	_ = model.DB.Find(&remaining).Error
	for _, ch := range remaining {
		if ch.Status != 1 {
			t.Fatalf("enabled channel %d status = %d", ch.Id, ch.Status)
		}
	}
}

// UpdateChannel updates fields in place.
func TestUpdateChannel_Success(t *testing.T) {
	setupMockDB(t)
	seedChannel(t, 1, "alpha", "sk-old", model.ChannelStatusEnabled, 1)

	c, rec := withUserContext(t, 99, 10, "admin")
	c.Request = httptestNewPostJSONRequest("/api/channel/",
		`{"id":1,"name":"alpha","type":1,"key":"sk-new","status":1,"models":"gpt-4"}`)
	UpdateChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":true`) {
		t.Fatalf("expected success, got %s", rec.Body.String())
	}
	var ch model.Channel
	_ = model.DB.First(&ch, "id = ?", 1)
	if ch.Key != "sk-new" {
		t.Fatalf("key = %q, want sk-new", ch.Key)
	}
	if ch.Models != "gpt-4" {
		t.Fatalf("models = %q, want gpt-4", ch.Models)
	}
}

// UpdateChannel with bad JSON returns failure.
func TestUpdateChannel_BadJSON(t *testing.T) {
	setupMockDB(t)
	c, rec := withUserContext(t, 99, 10, "admin")
	UpdateChannel(c)
	if !strings.Contains(rec.Body.String(), `"success":false`) {
		t.Fatalf("expected failure, got %s", rec.Body.String())
	}
}
