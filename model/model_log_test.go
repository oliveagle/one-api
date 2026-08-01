package model

import (
	"context"
	"testing"
	"time"
)

func TestLogCRUD(t *testing.T) {
	setupMockDB(t)

	log := &Log{
		UserId:           1,
		CreatedAt:        time.Now().Unix(),
		Type:             LogTypeConsume,
		Content:          "test log entry",
		Username:         "testuser",
		TokenName:        "test-token",
		ModelName:        "gpt-4o",
		Quota:            100,
		PromptTokens:     10,
		CompletionTokens: 20,
	}
	if err := DB.Create(log).Error; err != nil {
		t.Fatalf("create log: %v", err)
	}
	if log.Id == 0 {
		t.Fatal("log.Id should be set after create")
	}

	var found Log
	if err := DB.First(&found, log.Id).Error; err != nil {
		t.Fatalf("find log: %v", err)
	}
	if found.Content != "test log entry" {
		t.Errorf("content = %q, want test log entry", found.Content)
	}
}

func TestRecordConsumeLog(t *testing.T) {
	setupMockDB(t)

	RecordConsumeLog(context.Background(), &Log{
		UserId:           1,
		CreatedAt:        time.Now().Unix(),
		Type:             LogTypeConsume,
		Content:          "consume test",
		Username:         "testuser",
		TokenName:        "test-token",
		ModelName:        "gpt-4o",
		Quota:            50,
		PromptTokens:     5,
		CompletionTokens: 10,
	})

	var count int64
	DB.Model(&Log{}).Count(&count)
	if count == 0 {
		t.Fatal("expected at least one log entry")
	}
}

func TestOptionCRUD(t *testing.T) {
	setupMockDB(t)

	opt := &Option{
		Key:   "test_option_key",
		Value: "test_value",
	}
	if err := DB.Create(opt).Error; err != nil {
		t.Fatalf("create option: %v", err)
	}

	var found Option
	if err := DB.Where("key = ?", "test_option_key").First(&found).Error; err != nil {
		t.Fatalf("find option: %v", err)
	}
	if found.Value != "test_value" {
		t.Errorf("value = %q, want test_value", found.Value)
	}
}

func TestAllOption(t *testing.T) {
	setupMockDB(t)

	DB.Create(&Option{Key: "opt1", Value: "val1"})
	DB.Create(&Option{Key: "opt2", Value: "val2"})

	options, err := AllOption()
	if err != nil {
		t.Fatalf("AllOption: %v", err)
	}
	if len(options) < 2 {
		t.Fatalf("expected at least 2 options, got %d", len(options))
	}
}

func TestRedemptionCRUD(t *testing.T) {
	setupMockDB(t)

	redemption := &Redemption{
		Key:         "redemption-test-key-123",
		Status:      RedemptionCodeStatusEnabled,
		Name:        "test-redemption",
		Quota:       1000,
		CreatedTime: time.Now().Unix(),
	}
	if err := DB.Create(redemption).Error; err != nil {
		t.Fatalf("create redemption: %v", err)
	}
	if redemption.Id == 0 {
		t.Fatal("redemption.Id should be set after create")
	}

	var found Redemption
	if err := DB.Where("key = ?", "redemption-test-key-123").First(&found).Error; err != nil {
		t.Fatalf("find redemption: %v", err)
	}
	if found.Name != "test-redemption" {
		t.Errorf("name = %q, want test-redemption", found.Name)
	}
}

func TestGetAllRedemptions(t *testing.T) {
	setupMockDB(t)

	DB.Create(&Redemption{Key: "key1", Name: "r1", Quota: 100, CreatedTime: time.Now().Unix()})
	DB.Create(&Redemption{Key: "key2", Name: "r2", Quota: 200, CreatedTime: time.Now().Unix()})

	redemptions, err := GetAllRedemptions(0, 10)
	if err != nil {
		t.Fatalf("GetAllRedemptions: %v", err)
	}
	if len(redemptions) != 2 {
		t.Fatalf("expected 2 redemptions, got %d", len(redemptions))
	}
}

func TestSearchRedemptions(t *testing.T) {
	setupMockDB(t)

	DB.Create(&Redemption{Key: "key-search", Name: "searchable", Quota: 100, CreatedTime: time.Now().Unix()})

	redemptions, err := SearchRedemptions("search")
	if err != nil {
		t.Fatalf("SearchRedemptions: %v", err)
	}
	if len(redemptions) == 0 {
		t.Fatal("expected at least one redemption")
	}
}
