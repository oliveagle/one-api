package model

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/config"
)

// seedChannelCacheForTest replaces the in-memory channel cache with a fixed
// (group, model) -> channels map. The slice is expected to be sorted by
// priority descending, as InitChannelCache would produce.
func seedChannelCacheForTest(group, model string, channels []*Channel) {
	channelSyncLock.Lock()
	defer channelSyncLock.Unlock()
	group2model2channels = map[string]map[string][]*Channel{
		group: {
			model: channels,
		},
	}
	// Mirror InitChannelCache's name index so channel-name addressing tests
	// exercise the same lookup path production uses.
	name2channel = make(map[string][]*Channel)
	for _, ch := range channels {
		name := strings.TrimSpace(ch.Name)
		if name != "" {
			name2channel[name] = append(name2channel[name], ch)
		}
	}
}

func TestCacheGetRandomSatisfiedChannel_PrefersTopPriorityTier(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, priority int64) *Channel {
		p := priority
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}

	// Two normal channels (priority 0) + two last-resort channels (negative).
	// Sorted by priority descending, as InitChannelCache does.
	seedChannelCacheForTest("g", "m", []*Channel{
		mk(1, 0),
		mk(2, 0),
		mk(3, -100000),
		mk(4, -100000),
	})

	// Primary selection must never pick a last-resort (negative priority) channel.
	for i := 0; i < 200; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id == 3 || ch.Id == 4 {
			t.Fatalf("primary selection picked last-resort channel %d", ch.Id)
		}
	}

	// Explicit retry (ignoreFirstPriority) may fall through to the lower tier.
	seen := map[int]bool{}
	for i := 0; i < 100; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		seen[ch.Id] = true
	}
	if !seen[3] && !seen[4] {
		t.Fatalf("retry never reached last-resort tier, seen=%v", seen)
	}
}

func TestCacheGetRandomSatisfiedChannel_AllLastResortStillServed(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, priority int64) *Channel {
		p := priority
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}

	// Only last-resort channels exist for this model — they must still be served.
	seedChannelCacheForTest("g", "m", []*Channel{
		mk(3, -100000),
		mk(4, -100000),
	})

	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != 3 && ch.Id != 4 {
			t.Fatalf("unexpected channel %d", ch.Id)
		}
	}
}

func TestCacheGetRandomSatisfiedChannelExcluding_AvoidsExcludedAndCooling(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()
	ResetChannelCooldowns()

	mk := func(id int) *Channel {
		p := int64(0)
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}
	seedChannelCacheForTest("g", "m", []*Channel{mk(1), mk(2), mk(3)})

	// exclude 1, cool 2 → only 3 may be picked.
	MarkChannelCooldown(2, time.Now().Add(time.Minute))
	exclude := map[int]bool{1: true}
	for i := 0; i < 100; i++ {
		ch, err := CacheGetRandomSatisfiedChannelExcluding("g", "m", false, exclude)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ch.Id != 3 {
			t.Fatalf("pick landed on blocked channel %d (want 3)", ch.Id)
		}
	}

	// All candidates blocked (excluded or cooling) → graceful fallback to
	// the plain random pick instead of "channel not found".
	ResetChannelCooldowns()
	MarkChannelCooldown(1, time.Now().Add(time.Minute))
	MarkChannelCooldown(2, time.Now().Add(time.Minute))
	MarkChannelCooldown(3, time.Now().Add(time.Minute))
	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannelExcluding("g", "m", false, nil)
		if err != nil {
			t.Fatalf("fallback should still return a channel, got: %v", err)
		}
		if ch.Id < 1 || ch.Id > 3 {
			t.Fatalf("fallback picked unknown channel %d", ch.Id)
		}
	}

	// Cooldown expiry is honored lazily.
	ResetChannelCooldowns()
	if ChannelCoolingDown(2) {
		t.Fatal("stale cooldown survived reset")
	}
	// Past deadlines are not stored at all.
	MarkChannelCooldown(1, time.Now().Add(-time.Second))
	if ChannelCoolingDown(1) {
		t.Fatal("expired cooldown must not count as cooling")
	}
}

func TestGetChannelByName_CachedPath(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, name, group string) *Channel {
		p := int64(0)
		g := group
		return &Channel{Id: id, Name: name, Group: g, Priority: &p}
	}
	seedChannelCacheForTest("default", "m", []*Channel{
		mk(1, "volc-1", "default"),
		mk(2, "other-pool", "other"),
	})

	if ch, err := GetChannelByName("volc-1", "default"); err != nil || ch.Id != 1 {
		t.Fatalf("cached lookup: ch=%v err=%v, want id 1", ch, err)
	}
	// Name trims are tolerated (client quirks).
	if ch, err := GetChannelByName(" volc-1 ", "default"); err != nil || ch.Id != 1 {
		t.Fatalf("trimmed lookup: ch=%v err=%v", ch, err)
	}
	// Group must match: same name in another group is invisible.
	if _, err := GetChannelByName("other-pool", "default"); err == nil {
		t.Fatal("channel of another group must not resolve")
	}
	if ch, err := GetChannelByName("other-pool", "other"); err != nil || ch.Id != 2 {
		t.Fatalf("cross-group: ch=%v err=%v", ch, err)
	}
	// Unknown names error out.
	if _, err := GetChannelByName("nope", "default"); err == nil {
		t.Fatal("unknown name must error")
	}
}

// TestBillingModeRouting pins the cost-tier routing contract: plan channels
// form the first-choice tier, pay_as_you_go channels drop exactly one tier
// below their configured priority, and the retry path (ignoreFirstPriority)
// reaches them.
func TestBillingModeRouting(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, name, billingMode string, priority int64) *Channel {
		p := priority
		cfg := fmt.Sprintf(`{"billing_mode":%q}`, billingMode)
		return &Channel{Id: id, Name: name, Priority: &p, Config: cfg}
	}

	// Same model served by two plan + two pay_as_you_go channels.
	seedChannelCacheForTest("default", "m", []*Channel{
		mk(1, "plan-a", "plan", 0),
		mk(2, "plan-b", "plan", 0),
		mk(3, "payg-a", "pay_as_you_go", 0),
		mk(4, "payg-b", "pay_as_you_go", 0),
	})

	// First-choice selection must never land on a pay_as_you_go channel.
	for i := 0; i < 100; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("default", "m", false)
		if err != nil {
			t.Fatalf("primary pick: %v", err)
		}
		if ch.Id == 3 || ch.Id == 4 {
			t.Fatalf("primary pick hit pay_as_you_go channel %d; want plan-only", ch.Id)
		}
	}

	// Retry (ignoreFirstPriority) must reach the pay_as_you_go tier.
	seenPayg := false
	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("default", "m", true)
		if err != nil {
			t.Fatalf("retry pick: %v", err)
		}
		if ch.Id == 3 || ch.Id == 4 {
			seenPayg = true
		}
	}
	if !seenPayg {
		t.Fatal("retry path never reached a pay_as_you_go channel")
	}

	// Default (no billing_mode set) = plan tier.
	seedChannelCacheForTest("default", "m", []*Channel{
		mk(1, "plan-a", "plan", 0),
		{Id: 5, Name: "default-mode", Priority: func() *int64 { p := int64(0); return &p }()},
	})
	ch, _ := CacheGetRandomSatisfiedChannel("default", "m", false)
	if ch == nil {
		t.Fatal("pick failed")
	}
	// Both should be in tier 0 (default mode = plan).
	for i := 0; i < 20; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("default", "m", false)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if ch.Id != 1 && ch.Id != 5 {
			t.Fatalf("unexpected channel %d; want 1 or 5 (both plan tier)", ch.Id)
		}
	}

	// Only pay_as_you_go serves the model → primary picks it (no plan
	// alternative exists, so the tier boundary doesn't help).
	seedChannelCacheForTest("default", "m2", []*Channel{
		mk(6, "only-payg", "pay_as_you_go", 0),
	})
	ch, err := CacheGetRandomSatisfiedChannel("default", "m2", false)
	if err != nil || ch == nil {
		t.Fatalf("only-payg model should still be servable: %v", err)
	}
	if ch.Id != 6 {
		t.Fatalf("want channel 6, got %d", ch.Id)
	}
}

// TestBillingModeRouting_ExplicitPriorityInteraction pins that billing-mode
// demotion composes with explicit priorities: a pay_as_you_go channel at
// priority 10 (effective 9) still outranks a plan channel at priority 0
// (effective 0) — the demotion is relative, not absolute.
func TestBillingModeRouting_ExplicitPriorityInteraction(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()

	mk := func(id int, name, billingMode string, priority int64) *Channel {
		p := priority
		cfg := fmt.Sprintf(`{"billing_mode":%q}`, billingMode)
		return &Channel{Id: id, Name: name, Priority: &p, Config: cfg}
	}

	seedChannelCacheForTest("g", "m", []*Channel{
		mk(1, "payg-high", "pay_as_you_go", 10), // effective 9
		mk(2, "plan-low", "plan", 0),            // effective 0
	})

	// Primary pick should always hit the higher effective priority (1).
	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannel("g", "m", false)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if ch.Id != 1 {
			t.Fatalf("want channel 1 (effective priority 9), got %d", ch.Id)
		}
	}

	// ignoreFirstPriority reaches the plan channel.
	ch, err := CacheGetRandomSatisfiedChannel("g", "m", true)
	if err != nil {
		t.Fatalf("retry pick: %v", err)
	}
	if ch.Id != 2 {
		t.Fatalf("retry should reach channel 2, got %d", ch.Id)
	}
}

// TestPicker_CoolingChannelBeatsExcluded pins the three-tier preference:
// a channel that is cooling (soft penalty from a previous request) is still
// a better retry target than a channel that already failed in THIS request
// (hard exclude). Without this, the relay could pick the same failed
// channel twice when all healthy alternatives happen to be cooling.
func TestPicker_CoolingChannelBeatsExcluded(t *testing.T) {
	prev := config.MemoryCacheEnabled
	config.MemoryCacheEnabled = true
	defer func() { config.MemoryCacheEnabled = prev }()
	ResetChannelCooldowns()
	t.Cleanup(ResetChannelCooldowns)

	mk := func(id int) *Channel {
		p := int64(0)
		return &Channel{Id: id, Name: "ch", Priority: &p}
	}
	// Pool: ch1 (excluded), ch2 (excluded), ch3 (cooling), ch4 (cooling)
	seedChannelCacheForTest("g", "m", []*Channel{mk(1), mk(2), mk(3), mk(4)})
	MarkChannelCooldown(3, time.Now().Add(time.Minute))
	MarkChannelCooldown(4, time.Now().Add(time.Minute))

	exclude := map[int]bool{1: true, 2: true}

	// The picker must return ch3 or ch4 (cooling but NOT excluded), never
	// ch1 or ch2 (excluded).
	for i := 0; i < 50; i++ {
		ch, err := CacheGetRandomSatisfiedChannelExcluding("g", "m", false, exclude)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if ch.Id == 1 || ch.Id == 2 {
			t.Fatalf("picked excluded channel %d; want 3 or 4 (cooling but untried)", ch.Id)
		}
	}

	// Everything excluded → graceful fallback returns SOMETHING.
	excludeAll := map[int]bool{1: true, 2: true, 3: true, 4: true}
	ch, err := CacheGetRandomSatisfiedChannelExcluding("g", "m", false, excludeAll)
	if err != nil {
		t.Fatalf("all-excluded fallback should still return a channel: %v", err)
	}
	if ch == nil || ch.Id < 1 || ch.Id > 4 {
		t.Fatalf("fallback returned invalid channel: %v", ch)
	}
}
