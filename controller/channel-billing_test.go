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

// The management API rejects the "Bearer " prefix with 401, so the header must
// carry the bare token.
func TestGetRawAuthHeaderHasNoBearerPrefix(t *testing.T) {
	h := GetRawAuthHeader("fd-example-token")
	if got := h.Get("Authorization"); got != "fd-example-token" {
		t.Fatalf("Authorization = %q, want bare token without Bearer prefix", got)
	}
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

// Guards the quota -> USD conversion: AIHubMix reports integer quota units and
// $1 == 500000 units.
func TestAIHubMixQuotaToUSD(t *testing.T) {
	const payload = `{"data":{"username":"u","quota":29071257,"used_quota":286403484},"success":true}`
	var resp AIHubMixUserResponse
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Fatal("expected success=true")
	}
	if resp.Data.Quota != 29071257 {
		t.Fatalf("quota = %d", resp.Data.Quota)
	}
	got := float64(resp.Data.Quota) / config.QuotaPerUnit
	if want := 58.142514; got < want-1e-6 || got > want+1e-6 {
		t.Fatalf("balance = %v, want %v", got, want)
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
