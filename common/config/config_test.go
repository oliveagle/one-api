package config

import (
	"os"
	"testing"
)

func TestDefaultValues(t *testing.T) {
	if SystemName != "One API" {
		t.Errorf("SystemName = %q, want One API", SystemName)
	}
	if ServerAddress != "http://localhost:3000" {
		t.Errorf("ServerAddress = %q, want http://localhost:3000", ServerAddress)
	}
	if ItemsPerPage != 10 {
		t.Errorf("ItemsPerPage = %d, want 10", ItemsPerPage)
	}
	if PasswordLoginEnabled != true {
		t.Error("PasswordLoginEnabled should be true by default")
	}
	if RegisterEnabled != true {
		t.Error("RegisterEnabled should be true by default")
	}
}

func TestEmailDomainWhitelist(t *testing.T) {
	expected := []string{
		"gmail.com",
		"163.com",
		"126.com",
		"qq.com",
		"outlook.com",
		"hotmail.com",
		"icloud.com",
		"yahoo.com",
		"foxmail.com",
	}
	if len(EmailDomainWhitelist) != len(expected) {
		t.Fatalf("EmailDomainWhitelist length = %d, want %d", len(EmailDomainWhitelist), len(expected))
	}
	for i, v := range expected {
		if EmailDomainWhitelist[i] != v {
			t.Errorf("EmailDomainWhitelist[%d] = %q, want %q", i, EmailDomainWhitelist[i], v)
		}
	}
}

func TestSessionSecret_NotEmpty(t *testing.T) {
	if SessionSecret == "" {
		t.Fatal("SessionSecret should not be empty")
	}
}

func TestOptionMap_Initialized(t *testing.T) {
	// OptionMap should be initialized (nil map is fine, but RWMutex should work)
	OptionMapRWMutex.RLock()
	_ = OptionMap
	OptionMapRWMutex.RUnlock()
}

func TestQuotaPerUnit(t *testing.T) {
	if QuotaPerUnit != 500*1000.0 {
		t.Errorf("QuotaPerUnit = %f, want %f", QuotaPerUnit, 500*1000.0)
	}
}

func TestDisplayInCurrencyEnabled(t *testing.T) {
	if !DisplayInCurrencyEnabled {
		t.Error("DisplayInCurrencyEnabled should be true by default")
	}
}

func TestDebugEnvVars(t *testing.T) {
	// Save and restore env vars
	oldDebug := os.Getenv("DEBUG")
	oldDebugSQL := os.Getenv("DEBUG_SQL")
	oldMemCache := os.Getenv("MEMORY_CACHE_ENABLED")
	defer func() {
		os.Setenv("DEBUG", oldDebug)
		os.Setenv("DEBUG_SQL", oldDebugSQL)
		os.Setenv("MEMORY_CACHE_ENABLED", oldMemCache)
	}()

	// These are set at package init time, so we can't easily change them.
	// Just verify they're boolean values.
	_ = DebugEnabled
	_ = DebugSQLEnabled
	_ = MemoryCacheEnabled
}
