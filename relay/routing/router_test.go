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

	// After failover the session should be re-pinned to the alternative node.
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
