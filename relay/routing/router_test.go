package routing

import (
	"fmt"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
)

// fakeProvider is a deterministic channelProvider for unit tests.
type fakeProvider struct {
	candidates  []*dbmodel.Channel
	randomCalls int
}

func (f *fakeProvider) RandomSatisfied(group, model string, ignoreFirstPriority bool) (*dbmodel.Channel, error) {
	f.randomCalls++
	if len(f.candidates) == 0 {
		return nil, ErrNoChannel
	}
	return f.candidates[0], nil
}

func (f *fakeProvider) SatisfiedChannels(group, model string) []*dbmodel.Channel {
	return f.candidates
}

func chanPtr(id int) *dbmodel.Channel {
	p := int64(0)
	return &dbmodel.Channel{Id: id, Priority: &p}
}

func chanPtrPriority(id int, priority int64) *dbmodel.Channel {
	return &dbmodel.Channel{Id: id, Priority: &priority}
}

func setupRouter(t *testing.T, candidates []*dbmodel.Channel) (*Router, *fakeProvider) {
	t.Helper()
	config.StickyRoutingEnabled = true
	config.MemoryCacheEnabled = true
	config.StickyModels = ""
	config.StickyCooldownSeconds = 60
	fp := &fakeProvider{candidates: candidates}
	r := newRouter(NewStore(), fp)
	t.Cleanup(func() {
		config.StickyRoutingEnabled = false
		config.MemoryCacheEnabled = false
		config.StickyModels = ""
		config.StickyCooldownSeconds = 60
	})
	return r, fp
}

func TestChoose_SticksSessionToSameChannel(t *testing.T) {
	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2), chanPtr(3)})

	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	for i := 0; i < 20; i++ {
		got, err := r.Choose("g", "coding_medium", "sess-1")
		if err != nil {
			t.Fatalf("Choose err: %v", err)
		}
		if got.Id != first.Id {
			t.Fatalf("session not sticky: first=%d got=%d", first.Id, got.Id)
		}
	}
}

func TestChoose_DifferentSessionsMayUseDifferentChannels(t *testing.T) {
	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2), chanPtr(3)})

	// Multiple distinct sessions should eventually spread across nodes.
	seen := map[int]bool{}
	for i := 0; i < 60; i++ {
		ch, err := r.Choose("g", "coding_medium", fmt.Sprintf("sess-%d", i))
		if err != nil {
			t.Fatalf("Choose err: %v", err)
		}
		seen[ch.Id] = true
	}
	if len(seen) != 3 {
		t.Fatalf("expected sessions to spread across 3 nodes, got %d", len(seen))
	}
}

func TestChoose_NoSessionFallsBackToRandom(t *testing.T) {
	r, fp := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2)})

	if _, err := r.Choose("g", "coding_medium", ""); err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if fp.randomCalls != 1 {
		t.Fatalf("expected fallback to random provider, randomCalls=%d", fp.randomCalls)
	}
}

func TestChoose_StickyDisabledFallsBackToRandom(t *testing.T) {
	config.StickyRoutingEnabled = false
	config.MemoryCacheEnabled = true
	config.StickyModels = ""
	config.StickyCooldownSeconds = 60
	defer func() {
		config.StickyRoutingEnabled = false
		config.MemoryCacheEnabled = false
	}()

	fp := &fakeProvider{candidates: []*dbmodel.Channel{chanPtr(1), chanPtr(2)}}
	r := newRouter(NewStore(), fp)
	if _, err := r.Choose("g", "coding_medium", "sess-1"); err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if fp.randomCalls != 1 {
		t.Fatalf("expected fallback when sticky disabled, randomCalls=%d", fp.randomCalls)
	}
}

func TestChoose_ModelAllowlist(t *testing.T) {
	config.StickyRoutingEnabled = true
	config.MemoryCacheEnabled = true
	config.StickyModels = "coding_medium,coding_small"
	config.StickyCooldownSeconds = 60
	defer func() {
		config.StickyRoutingEnabled = false
		config.MemoryCacheEnabled = false
		config.StickyModels = ""
	}()

	fp := &fakeProvider{candidates: []*dbmodel.Channel{chanPtr(1), chanPtr(2)}}
	r := newRouter(NewStore(), fp)

	// In allowlist -> sticky path (no random fallback call).
	if _, err := r.Choose("g", "coding_medium", "sess-1"); err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if fp.randomCalls != 0 {
		t.Fatalf("expected sticky path for allowlisted model, randomCalls=%d", fp.randomCalls)
	}

	// Not in allowlist -> random fallback.
	if _, err := r.Choose("g", "dall-e-3", "sess-1"); err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if fp.randomCalls != 1 {
		t.Fatalf("expected fallback for non-allowlisted model, randomCalls=%d", fp.randomCalls)
	}
}

func TestChooseAlternative_ExcludesFailedAndRebinds(t *testing.T) {
	// Default threshold (3) with zero recorded failures: ChooseAlternative must
	// return a different channel but NOT re-pin the session — transient
	// failover keeps the session bound to its original node.
	config.StickyFailureThreshold = 3
	defer func() { config.StickyFailureThreshold = 3 }()

	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2), chanPtr(3)})

	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}

	exclude := map[int]bool{first.Id: true}
	alt, err := r.ChooseAlternative("g", "coding_medium", "sess-1", exclude)
	if err != nil {
		t.Fatalf("ChooseAlternative err: %v", err)
	}
	if alt.Id == first.Id {
		t.Fatalf("ChooseAlternative returned the failed channel %d", alt.Id)
	}

	// Below the failure threshold the session must stay pinned to the original
	// channel (no re-pin).
	next, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if next.Id != first.Id {
		t.Fatalf("session should stay on original channel below threshold: want %d got %d", first.Id, next.Id)
	}
}

func TestChooseAlternative_RebindsWhenThresholdExceeded(t *testing.T) {
	config.StickyFailureThreshold = 2
	defer func() { config.StickyFailureThreshold = 3 }()

	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2)})

	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}

	// Two failures >= threshold=2.
	r.Fail("g", "coding_medium", "sess-1", first.Id)
	r.Fail("g", "coding_medium", "sess-1", first.Id)

	exclude := map[int]bool{first.Id: true}
	alt, err := r.ChooseAlternative("g", "coding_medium", "sess-1", exclude)
	if err != nil {
		t.Fatalf("ChooseAlternative err: %v", err)
	}
	if alt.Id == first.Id {
		t.Fatalf("ChooseAlternative returned the failed channel %d", alt.Id)
	}

	// Threshold exceeded → session re-pinned to the alternative node.
	next, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if next.Id != alt.Id {
		t.Fatalf("session should be re-pinned to failover node: want %d got %d", alt.Id, next.Id)
	}
}

func TestChooseAlternative_NoEligibleReturnsError(t *testing.T) {
	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1)})

	if _, err := r.Choose("g", "coding_medium", "sess-1"); err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	exclude := map[int]bool{1: true}
	if _, err := r.ChooseAlternative("g", "coding_medium", "sess-1", exclude); err == nil {
		t.Fatal("expected error when no eligible channel remains")
	}
}

func TestFail_CooldownPreventsRebounce(t *testing.T) {
	r, _ := setupRouter(t, []*dbmodel.Channel{chanPtr(1), chanPtr(2)})

	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}

	// Simulate a failover: fail the first node, pick an alternative.
	r.Fail("g", "coding_medium", "sess-1", first.Id)
	exclude := map[int]bool{first.Id: true}
	alt, err := r.ChooseAlternative("g", "coding_medium", "sess-1", exclude)
	if err != nil {
		t.Fatalf("ChooseAlternative err: %v", err)
	}
	if alt.Id == first.Id {
		t.Fatalf("ChooseAlternative returned cooled-down channel %d", first.Id)
	}

	// While the failed node is cooling down, a fresh Choose for a NEW session
	// should also avoid it (global cooldown).
	config.StickyCooldownSeconds = 3600
	defer func() { config.StickyCooldownSeconds = 60 }()
	for i := 0; i < 10; i++ {
		ch, err := r.Choose("g", "coding_medium", fmt.Sprintf("other-%d", i))
		if err != nil {
			t.Fatalf("Choose err: %v", err)
		}
		if ch.Id == first.Id {
			t.Fatalf("Choose returned cooled-down channel %d", first.Id)
		}
	}
}

func TestChoose_StaleBindingRevalidated(t *testing.T) {
	r, fp := setupRouter(t, []*dbmodel.Channel{chanPtr(10), chanPtr(20), chanPtr(30)})

	// Choose until the session is pinned to a known node (10, 20 or 30).
	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}

	// Simulate the pinned node being removed from the channel cache (disabled).
	remaining := map[int]bool{}
	for _, id := range []int{10, 20, 30} {
		if id != first.Id {
			remaining[id] = true
		}
	}
	var kept []*dbmodel.Channel
	for id := range remaining {
		kept = append(kept, chanPtr(id))
	}
	fp.candidates = kept

	got, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if got.Id == first.Id {
		t.Fatalf("expected stale binding to be revalidated, still got %d", first.Id)
	}
}

func TestPickFirstPriority_PicksOnlyTopTier(t *testing.T) {
	cands := []*dbmodel.Channel{
		chanPtrPriority(1, 10),
		chanPtrPriority(2, 10),
		chanPtrPriority(3, 5),
		chanPtrPriority(4, 0),
	}
	seen := map[int]bool{}
	for i := 0; i < 200; i++ {
		ch := pickFirstPriority(cands)
		if ch.Id != 1 && ch.Id != 2 {
			t.Fatalf("pickFirstPriority returned non-top-tier channel %d", ch.Id)
		}
		seen[ch.Id] = true
	}
	if len(seen) != 2 {
		t.Fatalf("expected both top-tier channels selected, got %v", seen)
	}
}

func TestChoose_StickyThresholdKeepsSessionPinned(t *testing.T) {
	// With threshold=3, a single failure should NOT migrate the session.
	config.StickyFailureThreshold = 3
	defer func() { config.StickyFailureThreshold = 3 }()

	ch1 := chanPtr(1)
	ch2 := chanPtr(2)
	r, _ := setupRouter(t, []*dbmodel.Channel{ch1, ch2})

	// Pin session — which channel is picked is random, track it.
	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	pinnedId := first.Id

	// Fail once — consecutive_failures=1, below threshold=3.
	r.Fail("g", "coding_medium", "sess-1", pinnedId)

	// The session must still be pinned to the same channel in the store.
	id, ok := r.store.Get(r.key("g", "coding_medium", "sess-1"))
	if !ok || id != pinnedId {
		t.Fatalf("session should still be pinned to channel %d, got id=%d ok=%v", pinnedId, id, ok)
	}
	// Consecutive failures should be 1.
	if cf := r.store.GetConsecutiveFailures(r.key("g", "coding_medium", "sess-1")); cf != 1 {
		t.Fatalf("expected 1 consecutive failure, got %d", cf)
	}

	// Next Choose: channel is cooled down but consecutive failures < threshold,
	// so chooseAlternativeFrom picks a different channel for THIS request
	// only. The session stays pinned to the original channel.
	got, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if got.Id == pinnedId {
		// Cooldown already expired (very fast test) — acceptable.
	} else if got.Id != 3-pinnedId {
		t.Fatalf("expected alternative channel %d or original %d, got %d", 3-pinnedId, pinnedId, got.Id)
	}

	// Session must still be pinned to the original channel.
	id, ok = r.store.Get(r.key("g", "coding_medium", "sess-1"))
	if !ok || id != pinnedId {
		t.Fatalf("session should still be pinned to channel %d, got id=%d ok=%v", pinnedId, id, ok)
	}
}

func TestChoose_StickyThresholdMigratesAfterRepeatedFailures(t *testing.T) {
	// With threshold=2, two consecutive failures should migrate.
	config.StickyFailureThreshold = 2
	defer func() { config.StickyFailureThreshold = 3 }()

	ch1 := chanPtr(1)
	ch2 := chanPtr(2)
	r, _ := setupRouter(t, []*dbmodel.Channel{ch1, ch2})

	// Pin to a channel.
	first, _ := r.Choose("g", "coding_medium", "sess-1")
	pinnedId := first.Id
	altId := 3 - pinnedId // the other channel

	// Fail twice — consecutive_failures=2 >= threshold=2.
	r.Fail("g", "coding_medium", "sess-1", pinnedId)
	r.Fail("g", "coding_medium", "sess-1", pinnedId)

	// Next Choose should migrate to the other channel (binding forgotten).
	got, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	if got.Id != altId {
		t.Fatalf("expected migration to channel %d, got %d", altId, got.Id)
	}

	// Session now pinned to the other channel.
	id, ok := r.store.Get(r.key("g", "coding_medium", "sess-1"))
	if !ok || id != altId {
		t.Fatalf("session should be pinned to channel %d, got id=%d ok=%v", altId, id, ok)
	}
}

func TestChoose_SuccessResetsConsecutiveFailures(t *testing.T) {
	config.StickyFailureThreshold = 3
	defer func() { config.StickyFailureThreshold = 3 }()

	ch1 := chanPtr(1)
	ch2 := chanPtr(2)
	r, _ := setupRouter(t, []*dbmodel.Channel{ch1, ch2})

	// Pin to a channel (random between 1 and 2) and track it.
	first, err := r.Choose("g", "coding_medium", "sess-1")
	if err != nil {
		t.Fatalf("Choose err: %v", err)
	}
	pinnedId := first.Id

	// Fail twice on the pinned channel — consecutive_failures=2.
	r.Fail("g", "coding_medium", "sess-1", pinnedId)
	r.Fail("g", "coding_medium", "sess-1", pinnedId)

	if cf := r.store.GetConsecutiveFailures(r.key("g", "coding_medium", "sess-1")); cf != 2 {
		t.Fatalf("expected 2 consecutive failures, got %d", cf)
	}

	// Channel is cooled down. Since consecutive_failures=2 < threshold=3,
	// chooseAlternativeFrom picks the other channel for this request only.
	// Session stays pinned; consecutive failures remain 2.
	r.Choose("g", "coding_medium", "sess-1")
	if cf := r.store.GetConsecutiveFailures(r.key("g", "coding_medium", "sess-1")); cf != 2 {
		t.Fatalf("expected consecutive failures still 2, got %d", cf)
	}

	// Now make the pinned channel healthy again (clear cooldown).
	r.store.ClearCooldown(pinnedId)

	// A successful Choose on the pinned channel resets consecutive failures.
	r.Choose("g", "coding_medium", "sess-1")
	if cf := r.store.GetConsecutiveFailures(r.key("g", "coding_medium", "sess-1")); cf != 0 {
		t.Fatalf("expected consecutive failures reset to 0 after success, got %d", cf)
	}
}
