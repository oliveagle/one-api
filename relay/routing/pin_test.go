package routing

import (
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
)

func namedChannel(id int, name string, mapping string) *dbmodel.Channel {
	p := int64(0)
	ch := &dbmodel.Channel{Id: id, Name: name, Priority: &p}
	if mapping != "" {
		ch.ModelMapping = &mapping
	}
	return ch
}

func pinRouter(t *testing.T) (*Router, *Store) {
	t.Helper()
	oldSticky, oldMem, oldModels := config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels
	config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels = true, true, ""
	t.Cleanup(func() {
		config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels = oldSticky, oldMem, oldModels
	})
	store := NewStore()
	r := newRouter(store, &fakeProvider{candidates: []*dbmodel.Channel{
		namedChannel(2, "volc-1", `{"coding_medium":"deepseek-v4-flash"}`),
		namedChannel(5, "minimax", `{"coding_medium":"MiniMax-M3"}`),
		namedChannel(9, "openrouter", `{"coding_medium":"~deepseek/deepseek-v4-flash-latest"}`),
	}})
	return r, store
}

func TestNodes_ReportsUpstreamModelAndCurrent(t *testing.T) {
	r, _ := pinRouter(t)
	// Pin so exactly one node is marked current.
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 5); err != nil {
		t.Fatalf("pin: %v", err)
	}
	nodes := r.Nodes("g", "coding_medium", "fp:s1")
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(nodes))
	}
	byId := map[int]NodeInfo{}
	for _, n := range nodes {
		byId[n.ChannelId] = n
	}
	if byId[5].UpstreamModel != "MiniMax-M3" {
		t.Fatalf("channel 5 upstream = %q, want MiniMax-M3", byId[5].UpstreamModel)
	}
	// The "~" prefix is an openrouter routing marker, not part of the name.
	if byId[9].UpstreamModel != "deepseek/deepseek-v4-flash-latest" {
		t.Fatalf("channel 9 upstream = %q, want ~ stripped", byId[9].UpstreamModel)
	}
	if !byId[5].Current {
		t.Fatal("pinned channel 5 should be marked current")
	}
	if byId[2].Current || byId[9].Current {
		t.Fatal("only the pinned channel may be current")
	}
}

func TestNodes_NoSessionMarksNothingCurrent(t *testing.T) {
	r, _ := pinRouter(t)
	for _, n := range r.Nodes("g", "coding_medium", "") {
		if n.Current {
			t.Fatalf("channel %d marked current without a session", n.ChannelId)
		}
	}
}

// Pinning must actually change where Choose sends the session.
func TestPin_ChooseHonoursExplicitSelection(t *testing.T) {
	r, _ := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 9); err != nil {
		t.Fatalf("pin: %v", err)
	}
	for i := 0; i < 5; i++ {
		ch, err := r.Choose("g", "coding_medium", "fp:s1")
		if err != nil {
			t.Fatalf("choose: %v", err)
		}
		if ch.Id != 9 {
			t.Fatalf("choose returned %d, want the pinned 9", ch.Id)
		}
	}
}

func TestPin_RejectsIneligibleChannel(t *testing.T) {
	r, _ := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 777); err != ErrChannelNotEligible {
		t.Fatalf("got %v, want ErrChannelNotEligible", err)
	}
}

func TestPin_RequiresSession(t *testing.T) {
	r, _ := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "", 5); err == nil {
		t.Fatal("pinning without a session should fail")
	}
}

// An explicit user choice overrides an automatic "this node looks unhealthy"
// cooldown, otherwise the user could not select the node they just asked for.
func TestPin_ClearsCooldown(t *testing.T) {
	r, store := pinRouter(t)
	store.CoolDown(5, time.Now().Add(10*time.Minute))
	if !store.IsCooledDownNow(5) {
		t.Fatal("precondition: channel 5 should be cooling")
	}
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 5); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if store.IsCooledDownNow(5) {
		t.Fatal("explicit pin should clear the cooldown")
	}
	ch, err := r.Choose("g", "coding_medium", "fp:s1")
	if err != nil || ch.Id != 5 {
		t.Fatalf("choose after pin: ch=%v err=%v, want 5", ch, err)
	}
}

func TestNext_RotatesInChannelIdOrderAndWraps(t *testing.T) {
	r, _ := pinRouter(t)
	// Start pinned to the lowest id so rotation order is deterministic.
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 2); err != nil {
		t.Fatalf("pin: %v", err)
	}
	var seq []int
	for i := 0; i < 4; i++ {
		ch, err := r.Next("g", "coding_medium", "fp:s1")
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		seq = append(seq, ch.Id)
	}
	want := []int{5, 9, 2, 5}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("rotation = %v, want %v", seq, want)
		}
	}
}

// Rotation must land somewhere even from an unpinned session.
func TestNext_FromUnpinnedSession(t *testing.T) {
	r, _ := pinRouter(t)
	ch, err := r.Next("g", "coding_medium", "fp:fresh")
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	after, err := r.Choose("g", "coding_medium", "fp:fresh")
	if err != nil || after.Id != ch.Id {
		t.Fatalf("rotation did not stick: next=%d choose=%v err=%v", ch.Id, after, err)
	}
}

func TestNext_SkipsCoolingNodes(t *testing.T) {
	r, store := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 2); err != nil {
		t.Fatalf("pin: %v", err)
	}
	store.CoolDown(5, time.Now().Add(10*time.Minute))
	ch, err := r.Next("g", "coding_medium", "fp:s1")
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if ch.Id == 5 {
		t.Fatal("rotation should skip the cooling channel 5")
	}
	if ch.Id != 9 {
		t.Fatalf("expected to land on 9, got %d", ch.Id)
	}
}

// If every alternative is cooling, the user still explicitly asked to move, so
// rotation must not fail outright.
func TestNext_AllCoolingStillRotates(t *testing.T) {
	r, store := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 2); err != nil {
		t.Fatalf("pin: %v", err)
	}
	store.CoolDown(5, time.Now().Add(time.Minute))
	store.CoolDown(9, time.Now().Add(time.Minute))
	ch, err := r.Next("g", "coding_medium", "fp:s1")
	if err != nil {
		t.Fatalf("next should still move, got %v", err)
	}
	if ch.Id == 2 {
		t.Fatal("rotation should have left the current node")
	}
}

func TestNext_SingleCandidateStaysPut(t *testing.T) {
	oldSticky, oldMem := config.StickyRoutingEnabled, config.MemoryCacheEnabled
	config.StickyRoutingEnabled, config.MemoryCacheEnabled = true, true
	t.Cleanup(func() { config.StickyRoutingEnabled, config.MemoryCacheEnabled = oldSticky, oldMem })
	r := newRouter(NewStore(), &fakeProvider{candidates: []*dbmodel.Channel{namedChannel(7, "only", "")}})
	ch, err := r.Next("g", "coding_medium", "fp:s1")
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if ch.Id != 7 {
		t.Fatalf("got %d, want the sole channel 7", ch.Id)
	}
}

func TestUnpin_RestoresAutomaticRouting(t *testing.T) {
	r, store := pinRouter(t)
	if _, err := r.Pin("g", "coding_medium", "fp:s1", 9); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if !r.Unpin("g", "coding_medium", "fp:s1") {
		t.Fatal("unpin should report a removal")
	}
	if _, ok := store.Get(r.key("g", "coding_medium", "fp:s1")); ok {
		t.Fatal("binding should be gone after unpin")
	}
}

func TestParseChannelId_AcceptsIdAndName(t *testing.T) {
	r, _ := pinRouter(t)
	if id, err := r.ParseChannelId("g", "coding_medium", "5"); err != nil || id != 5 {
		t.Fatalf("numeric: id=%d err=%v", id, err)
	}
	if id, err := r.ParseChannelId("g", "coding_medium", "minimax"); err != nil || id != 5 {
		t.Fatalf("by name: id=%d err=%v", id, err)
	}
	if id, err := r.ParseChannelId("g", "coding_medium", "MiniMax"); err != nil || id != 5 {
		t.Fatalf("case-insensitive: id=%d err=%v", id, err)
	}
	if _, err := r.ParseChannelId("g", "coding_medium", "nope"); err == nil {
		t.Fatal("unknown name should error")
	}
	if _, err := r.ParseChannelId("g", "coding_medium", "4242"); err == nil {
		t.Fatal("id not serving this model should error")
	}
	if _, err := r.ParseChannelId("g", "coding_medium", "  "); err == nil {
		t.Fatal("empty target should error")
	}
}

// Rebind is the explicit-choice counterpart of Touch: it must not inflate the
// request counter, and must move the per-channel session count correctly.
func TestRebind_DoesNotCountAsRequest(t *testing.T) {
	s := NewStore()
	s.Touch("k", "sess", "g", "m", 1)
	s.Rebind("k", "sess", "g", "m", 2)
	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("got %d records, want 1", len(snap))
	}
	if snap[0].Requests != 1 {
		t.Fatalf("requests = %d, want 1 (rebind is not a request)", snap[0].Requests)
	}
	if snap[0].ChannelId != 2 {
		t.Fatalf("channel = %d, want 2", snap[0].ChannelId)
	}
	for _, st := range s.ChannelStates() {
		if st.ChannelId == 1 && st.Sessions != 0 {
			t.Fatalf("old channel still holds %d sessions", st.Sessions)
		}
		if st.ChannelId == 2 && st.Sessions != 1 {
			t.Fatalf("new channel holds %d sessions, want 1", st.Sessions)
		}
	}
}
