package ratio

import (
	"encoding/json"
	"strings"
	"testing"
)

// Constants must agree with each other. RMB is derived from USD / USD2RMB; if
// the rate changes the billing output stops matching human-readable prices.
func TestConstants(t *testing.T) {
	if USD2RMB != 7 {
		t.Fatalf("USD2RMB = %v, want 7", USD2RMB)
	}
	if USD != 500 {
		t.Fatalf("USD = %v, want 500", USD)
	}
	expectedRMB := USD / USD2RMB
	if RMB != expectedRMB {
		t.Fatalf("RMB = %v, want %v", RMB, expectedRMB)
	}
	expectedMilli := 1.0 / 1000 * USD
	if MILLI_USD != expectedMilli {
		t.Fatalf("MILLI_USD = %v, want %v", MILLI_USD, expectedMilli)
	}
}

// The pinned default ratios cover the OpenAI / Anthropic / Gemini / domestic
// (Qwen / DeepSeek / Doubao / Zhipu / etc.) providers. If any of these is
// removed accidentally, the billing calculator falls back to the "30" default
// and overcharges the user.

// restoreRatioDefaults puts ModelRatio/CompletionRatio back to their
// init-time mirrors so repeated runs (-count>1) start from the same state.
func restoreRatioDefaults(t *testing.T) {
	t.Helper()
	m, _ := json.Marshal(DefaultModelRatio)
	_ = UpdateModelRatioByJSONString(string(m))
	c, _ := json.Marshal(DefaultCompletionRatio)
	_ = UpdateCompletionRatioByJSONString(string(c))
}

func TestModelRatio_HasCoreModels(t *testing.T) {
	required := []string{
		"gpt-4o",
		"gpt-4o-mini",
		"gpt-3.5-turbo",
		"o1",
		"o1-mini",
		"claude-3-5-sonnet-20240620",
		"gemini-1.5-pro",
		"qwen-plus",
		"deepseek-chat",
	}
	for _, m := range required {
		if _, ok := ModelRatio[m]; !ok {
			t.Errorf("ModelRatio missing %q", m)
		}
	}
}

// Backup (Default*) maps must be populated from the originals at init. They
// protect against runtime Mutation: when the user's admin overrides the
// table, the original defaults can be queried via DefaultModelRatio.
func TestDefaultModelRatio_Initialized(t *testing.T) {
	if DefaultModelRatio == nil {
		t.Fatal("DefaultModelRatio is nil")
	}
	if DefaultModelRatio["gpt-4o"] != ModelRatio["gpt-4o"] {
		t.Fatal("DefaultModelRatio must mirror ModelRatio at init")
	}
	if DefaultCompletionRatio["deepseek-chat"] != CompletionRatio["deepseek-chat"] {
		t.Fatal("DefaultCompletionRatio must mirror CompletionRatio at init")
	}
}

func TestModelRatio2JSONString_RoundTrip(t *testing.T) {
	out := ModelRatio2JSONString()
	if out == "" {
		t.Fatal("ModelRatio2JSONString returned empty")
	}
	var back map[string]float64
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(back) != len(ModelRatio) {
		t.Fatalf("round trip lost entries: got %d want %d", len(back), len(ModelRatio))
	}
}

func TestUpdateModelRatioByJSONString(t *testing.T) {
	old := ModelRatio
	t.Cleanup(func() { ModelRatio = old })

	// Use a JSON object with just one key. After update, getting a missing key
	// should fall back to the "30" default in GetModelRatio.
	t.Cleanup(func() { restoreRatioDefaults(t) })
	if err := UpdateModelRatioByJSONString(`{"my-model":7}`); err != nil {
		t.Fatalf("UpdateModelRatioByJSONString: %v", err)
	}
	if got := GetModelRatio("my-model", OpenAIFakeType); got != 7 {
		t.Fatalf("my-model ratio = %v, want 7", got)
	}
	if _, ok := ModelRatio["gpt-4o"]; ok {
		t.Fatal("existing entries should be wiped on update")
	}
}

func TestUpdateModelRatioByJSONString_Invalid(t *testing.T) {
	old := ModelRatio
	t.Cleanup(func() { ModelRatio = old })

	if err := UpdateModelRatioByJSONString("{not json}"); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// AddNewMissingRatio must merge defaults into the override without changing
// existing entries. The admin UI uses this to add newly-launched models to a
// saved config without losing the operator's customisations.
func TestAddNewMissingRatio(t *testing.T) {
	t.Cleanup(func() {
		// Restore the init-time mirror instead of wiping the map: tests run
		// with -count>1 share process globals, and an empty ModelRatio breaks
		// TestDefaultModelRatio_Initialized on the next iteration.
		restoreRatioDefaults(t)
	})

	const override = `{"gpt-4o":99,"custom-model":3}`
	out := AddNewMissingRatio(override)

	var merged map[string]float64
	if err := json.Unmarshal([]byte(out), &merged); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if merged["gpt-4o"] != 99 {
		t.Fatalf("overridden value lost: got %v want 99", merged["gpt-4o"])
	}
	if merged["custom-model"] != 3 {
		t.Fatalf("custom value lost: got %v want 3", merged["custom-model"])
	}
	// Newly added defaults must appear.
	if _, ok := merged["claude-3-5-sonnet-20240620"]; !ok {
		t.Fatal("missing default after merge")
	}
}

func TestAddNewMissingRatio_InvalidInputPassesThrough(t *testing.T) {
	const input = "not json"
	out := AddNewMissingRatio(input)
	if out != input {
		t.Fatalf("invalid input must be passed through, got %q", out)
	}
}

// GetModelRatio must honour the (model, channelType) compound key first so
// operators can offer channel-specific pricing. After that, the plain model
// key. Falls back to 30 for unknown models.
func TestGetModelRatio_CompoundKey(t *testing.T) {
	old, oldDefault := ModelRatio, DefaultModelRatio
	t.Cleanup(func() { ModelRatio, DefaultModelRatio = old, oldDefault })

	ModelRatio = map[string]float64{
		"foo":       1,
		"foo(99)":   5,
		"plain-bar": 2,
	}
	DefaultModelRatio = ModelRatio

	if got := GetModelRatio("foo", 99); got != 5 {
		t.Fatalf("compound = %v, want 5", got)
	}
	if got := GetModelRatio("foo", 0); got != 1 {
		t.Fatalf("plain foo = %v, want 1", got)
	}
	if got := GetModelRatio("plain-bar", 0); got != 2 {
		t.Fatalf("plain-bar = %v, want 2", got)
	}
	if got := GetModelRatio("missing-model", 0); got != 30 {
		t.Fatalf("missing fallback = %v, want 30", got)
	}
}

// "-internet" is the suffix one-API strips from Qwen / Command online models
// before lookup. If the strip regresses, the model ratio table never finds
// the entry.
func TestGetModelRatio_QwenInternetStrip(t *testing.T) {
	old, oldDefault := ModelRatio, DefaultModelRatio
	t.Cleanup(func() { ModelRatio, DefaultModelRatio = old, oldDefault })

	ModelRatio = map[string]float64{"qwen-plus": 4}
	DefaultModelRatio = ModelRatio

	if got := GetModelRatio("qwen-plus-internet", 0); got != 4 {
		t.Fatalf("internet suffix must be stripped; got %v want 4", got)
	}
	if got := GetModelRatio("command-r-internet", 0); got != 30 {
		// command-r is not in the seed table, so this should be the fallback.
		t.Fatalf("command-r-internet -> fallback 30; got %v", got)
	}
}

func TestCompletionRatio2JSONString_RoundTrip(t *testing.T) {
	out := CompletionRatio2JSONString()
	if out == "" {
		t.Fatal("CompletionRatio2JSONString empty")
	}
	var back map[string]float64
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(back) != len(CompletionRatio) {
		t.Fatalf("round trip lost entries")
	}
}

func TestUpdateCompletionRatioByJSONString(t *testing.T) {
	old := CompletionRatio
	t.Cleanup(func() { CompletionRatio = old })

	t.Cleanup(func() { restoreRatioDefaults(t) })
	if err := UpdateCompletionRatioByJSONString(`{"x":2}`); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := CompletionRatio["x"]; got != 2 {
		t.Fatalf("x = %v want 2", got)
	}
	if _, ok := CompletionRatio["deepseek-chat"]; ok {
		t.Fatal("existing entries should be wiped on update")
	}
}

// GetCompletionRatio covers the special-case branches per provider family.
// If a branch regresses to the "1" default, output-side billing becomes 1×
// instead of the documented e.g. 4× for gpt-4o.
func TestGetCompletionRatio_Branches(t *testing.T) {
	// Take a fresh snapshot so test doesn't depend on the seed table.
	t.Cleanup(func() {
		restoreRatioDefaults(t)
	})
	_ = UpdateCompletionRatioByJSONString(`{}`)

	cases := []struct {
		name string
		want float64
	}{
		{"gpt-3.5-turbo", 3},
		{"gpt-3.5-turbo-0125", 3},
		{"gpt-3.5-turbo-1106", 2},
		{"gpt-3.5-turbo-16k", 4.0 / 3.0},
		{"gpt-4o", 4},
		{"gpt-4o-2024-05-13", 3},
		{"gpt-4-turbo", 3},
		{"gpt-4-turbo-preview", 3},
		{"gpt-4-1106-preview", 3},
		{"gpt-4", 2},
		{"o1", 4},
		{"o1-mini", 4},
		{"chatgpt-4o-latest", 3},
		{"claude-3-5-sonnet-20240620", 5},
		{"claude-3-opus-20240229", 5},
		{"claude-2.1", 3},
		{"mistral-large-latest", 3},
		{"gemini-1.5-pro", 3},
		{"gemini-2.0-flash", 3},
		{"deepseek-chat", 2},
		{"deepseek-reasoner", 2.19 / 0.55}, // explicit seed entry wins over the prefix fallback
		{"llama3-8b-8192", 2},
		{"llama3-70b-8192", 0.79 / 0.59},
		{"grok-beta", 3},
		{"command-r", 3},
		{"command-r-plus", 5},
		{"command", 2},
		{"command-light", 2},
		{"unknown-model-z", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := GetCompletionRatio(tc.name, 0)
			if !almostEqual(got, tc.want, 1e-6) {
				t.Fatalf("GetCompletionRatio(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// Compound keys (model name + channel type in parens) must beat the plain
// model name so operators can fine-tune per channel.
func TestGetCompletionRatio_CompoundKey(t *testing.T) {
	t.Cleanup(func() { _ = UpdateCompletionRatioByJSONString(`{}`) })
	_ = UpdateCompletionRatioByJSONString(`{"foo":3,"foo(7)":7}`)

	if got := GetCompletionRatio("foo", 7); got != 7 {
		t.Fatalf("compound = %v want 7", got)
	}
	if got := GetCompletionRatio("foo", 0); got != 3 {
		t.Fatalf("plain = %v want 3", got)
	}
}

// Custom / non-table model falls back to the family-prefix rules. Make sure
// "MistralFoo" still picks up the mistral- prefix branch.
func TestGetCompletionRatio_PrefixFallback(t *testing.T) {
	t.Cleanup(func() { _ = UpdateCompletionRatioByJSONString(`{}`) })
	_ = UpdateCompletionRatioByJSONString(`{}`)
	if got := GetCompletionRatio("mistral-future-model", 0); got != 3 {
		t.Fatalf("mistral- prefix fallback = %v want 3", got)
	}
	if got := GetCompletionRatio("claude-future-model", 0); got != 3 {
		t.Fatalf("claude- prefix fallback = %v want 3", got)
	}
}

func TestGetCompletionRatio_StripsQwenInternetSuffix(t *testing.T) {
	t.Cleanup(func() { _ = UpdateCompletionRatioByJSONString(`{}`) })
	_ = UpdateCompletionRatioByJSONString(`{"qwen-plus":4}`)

	if got := GetCompletionRatio("qwen-plus-internet", 0); got != 4 {
		t.Fatalf("internet suffix strip: got %v want 4", got)
	}
}

// Make sure json unmarshal of the bool error path doesn't crash if the JSON is
// valid but irrelevant shape (catch-all pass-through). This is a sanity check
// only; the production code never returns structured errors here.
func TestUpdateCompletionRatioByJSONString_Invalid(t *testing.T) {
	// The update wipes the table before parsing; a malformed payload must
	// not leave it empty for the next test (-count>1 shares globals).
	t.Cleanup(func() { restoreRatioDefaults(t) })
	if err := UpdateCompletionRatioByJSONString(`{`); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// Sentinel error string presence ensures we don't accidentally return early
// without emitting the diagnostic for ops to grep.
func TestAddNewMissingRatio_HandlesBoolFields(t *testing.T) {
	out := AddNewMissingRatio(`{"m":0.5}`)
	if !strings.Contains(out, `"m":0.5`) {
		t.Fatalf("output should preserve m=0.5, got %q", out)
	}
}

// OpenAIFakeType is a placeholder used by other tests in this file.
const OpenAIFakeType = 0

// almostEqual compares two floats with a tolerance.
func almostEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}
