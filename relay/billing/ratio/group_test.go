package ratio

import (
	"encoding/json"
	"testing"
)

// Default seeded groups must round-trip through JSON unchanged. If anything
// starts filtering them out the cost calculator will silently bill the wrong
// rate for the default user tier.
func TestGroupRatio2JSONString_DefaultSeed(t *testing.T) {
	got := GroupRatio2JSONString()
	if got == "" {
		t.Fatal("GroupRatio2JSONString returned empty string")
	}
	var back map[string]float64
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("invalid JSON returned: %v", err)
	}
	want := map[string]float64{"default": 1, "vip": 1, "svip": 1}
	if len(back) != len(want) {
		t.Fatalf("got %d groups, want %d", len(back), len(want))
	}
	for k, v := range want {
		if back[k] != v {
			t.Fatalf("group %q: got %v, want %v", k, back[k], v)
		}
	}
}

func TestUpdateGroupRatioByJSONString_Valid(t *testing.T) {
	// Save and restore so we don't pollute other tests.
	old := GroupRatio
	t.Cleanup(func() { GroupRatio = old })

	input := `{"a":1.5,"b":2,"c":0.5}`
	if err := UpdateGroupRatioByJSONString(input); err != nil {
		t.Fatalf("UpdateGroupRatioByJSONString: %v", err)
	}
	if got := GetGroupRatio("a"); got != 1.5 {
		t.Fatalf("a = %v, want 1.5", got)
	}
	if got := GetGroupRatio("b"); got != 2 {
		t.Fatalf("b = %v, want 2", got)
	}
	if got := GetGroupRatio("c"); got != 0.5 {
		t.Fatalf("c = %v, want 0.5", got)
	}
}

func TestUpdateGroupRatioByJSONString_ReplacesExisting(t *testing.T) {
	old := GroupRatio
	t.Cleanup(func() { GroupRatio = old })

	if err := UpdateGroupRatioByJSONString(`{"only":1}`); err != nil {
		t.Fatalf("UpdateGroupRatioByJSONString: %v", err)
	}
	if got := GetGroupRatio("default"); got != 1 {
		t.Fatalf("default after replace = %v, want 1 (fallback)", got)
	}
	if _, exists := GroupRatio["default"]; exists {
		t.Fatal("default should not exist after replace")
	}
}

func TestUpdateGroupRatioByJSONString_InvalidJSON(t *testing.T) {
	old := GroupRatio
	t.Cleanup(func() { GroupRatio = old })

	if err := UpdateGroupRatioByJSONString("not json"); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// An unknown group must always fall back to 1 rather than zero or panic, since
// the billing path would otherwise multiply by zero and underbill.
func TestGetGroupRatio_Fallback(t *testing.T) {
	if got := GetGroupRatio("definitely-not-a-real-group"); got != 1 {
		t.Fatalf("fallback = %v, want 1", got)
	}
}

func TestGetGroupRatio_Known(t *testing.T) {
	cases := []string{"default", "vip", "svip"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			if got := GetGroupRatio(name); got != 1 {
				t.Fatalf("GetGroupRatio(%q) = %v, want 1", name, got)
			}
		})
	}
}
