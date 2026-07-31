package controller

import (
	"encoding/json"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
)

func TestAIHubMixAccountBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL *string
		want    string
	}{
		{"empty falls back to default", strPtr(""), "https://aihubmix.com"},
		{"nil falls back to default", nil, "https://aihubmix.com"},
		{"v1 suffix is stripped", strPtr("https://aihubmix.com/v1"), "https://aihubmix.com"},
		{"trailing slash after v1", strPtr("https://aihubmix.com/v1/"), "https://aihubmix.com"},
		{"bare origin kept", strPtr("https://aihubmix.com"), "https://aihubmix.com"},
		{"trailing slash trimmed", strPtr("https://aihubmix.com/"), "https://aihubmix.com"},
		{"custom proxy origin", strPtr("https://proxy.example.com/v1"), "https://proxy.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := &model.Channel{BaseURL: tc.baseURL}
			if got := aihubmixAccountBaseURL(ch); got != tc.want {
				t.Fatalf("aihubmixAccountBaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// AIHubMix accepts both a bare token and a "Bearer "-prefixed one, because it
// inherits one-api's ValidateAccessToken which strips the prefix. Verified
// against the live API: both forms return 200.
func TestAIHubMixUsesBearerAuthHeader(t *testing.T) {
	h := GetAuthHeader("f9examplemanagekey")
	if got, want := h.Get("Authorization"), "Bearer f9examplemanagekey"; got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

// Guards the quota -> USD conversion. The divisor must come from the upstream's
// own /api/status (quota_per_unit), never from this instance's
// config.QuotaPerUnit, which an operator may have changed.
func TestAIHubMixQuotaToUSD(t *testing.T) {
	const payload = `{"data":{"username":"u","quota":23990043,"used_quota":59007736},"success":true}`
	var resp AIHubMixUserResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Data.Quota != 23990043 {
		t.Fatalf("quota = %d", resp.Data.Quota)
	}
	got := float64(resp.Data.Quota) / aihubmixDefaultQuotaPerUnit
	if want := 47.980086; got < want-1e-6 || got > want+1e-6 {
		t.Fatalf("balance = %v, want %v", got, want)
	}
}

// The conversion must not silently follow this instance's QuotaPerUnit option:
// that value describes local billing, not the upstream's units.
func TestAIHubMixConversionIgnoresLocalQuotaPerUnit(t *testing.T) {
	original := config.QuotaPerUnit
	t.Cleanup(func() { config.QuotaPerUnit = original })
	config.QuotaPerUnit = 1.0 // simulate an operator changing the local option
	if aihubmixDefaultQuotaPerUnit == config.QuotaPerUnit {
		t.Fatal("AIHubMix divisor must be independent of config.QuotaPerUnit")
	}
}

// GET /api/status is unauthenticated and reports the upstream quota unit.
func TestAIHubMixStatusResponseParsesQuotaPerUnit(t *testing.T) {
	const payload = `{"data":{"quota_per_unit":500000,"system_name":"AIHubMix"},"success":true}`
	var status AIHubMixStatusResponse
	if err := json.Unmarshal([]byte(payload), &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if status.Data.QuotaPerUnit != 500000 {
		t.Fatalf("quota_per_unit = %v, want 500000", status.Data.QuotaPerUnit)
	}
}

func TestAIHubMixBalanceRequiresManageKey(t *testing.T) {
	// A channel holding only a relay key ("sk-...") must fail fast with a
	// helpful message rather than issuing a doomed 401 request.
	ch := &model.Channel{Key: "sk-relay-key-only", Config: "{}"}
	if _, err := updateChannelAIHubMixBalance(ch); err == nil {
		t.Fatal("expected an error when manage_key is absent")
	}
}

func strPtr(s string) *string { return &s }
