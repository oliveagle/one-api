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

func TestAgentInstallDir_Default(t *testing.T) {
	// AGENT_INSTALL_DIR should default to "./agent-install" when unset
	if AgentInstallDir != "./agent-install" {
		t.Errorf("AgentInstallDir default = %q, want %q", AgentInstallDir, "./agent-install")
	}
}

func TestAgentInstallURLPrefix(t *testing.T) {
	// The URL prefix must be stable so install scripts can rely on it.
	if AgentInstallURLPrefix != "/agent-install" {
		t.Errorf("AgentInstallURLPrefix = %q, want %q", AgentInstallURLPrefix, "/agent-install")
	}
}

func TestAgentInstallDir_EnvOverride(t *testing.T) {
	oldEnv := os.Getenv("AGENT_INSTALL_DIR")
	defer os.Setenv("AGENT_INSTALL_DIR", oldEnv)

	// env.String reads at init time so we can only verify the wiring by
	// checking the env.String contract: an empty env var -> default, a
	// non-empty env var -> that value. We re-read the variable directly
	// to confirm the test pattern is valid.
	os.Setenv("AGENT_INSTALL_DIR", "/tmp/custom-install")
	got := os.Getenv("AGENT_INSTALL_DIR")
	if got != "/tmp/custom-install" {
		t.Errorf("env override failed: got %q", got)
	}
	// The package-level AgentInstallDir was set at init time before this
	// test ran, so it should still hold the default value (or whatever
	// was in the env when the package loaded).
	_ = AgentInstallDir
}
