package routing

import (
	"testing"
	"time"
)

func TestStore_TouchTracksSessionAndChannelState(t *testing.T) {
	s := NewStore()

	key1 := s.keyHelper("g", "coding_medium", "sess-1")
	key2 := s.keyHelper("g", "coding_medium", "sess-2")

	s.Touch(key1, "sess-1", "g", "coding_medium", 10)
	s.Touch(key1, "sess-1", "g", "coding_medium", 10)
	s.Touch(key1, "sess-1", "g", "coding_medium", 10)
	s.Touch(key2, "sess-2", "g", "coding_medium", 20)

	if id, ok := s.Get(key1); !ok || id != 10 {
		t.Fatalf("key1 not pinned to 10: ok=%v id=%d", ok, id)
	}

	states := s.ChannelStates()
	if len(states) != 2 {
		t.Fatalf("expected 2 channel states, got %d", len(states))
	}
	for _, st := range states {
		switch st.ChannelId {
		case 10:
			if st.Sessions != 1 {
				t.Fatalf("channel 10 sessions = %d, want 1", st.Sessions)
			}
		case 20:
			if st.Sessions != 1 {
				t.Fatalf("channel 20 sessions = %d, want 1", st.Sessions)
			}
		default:
			t.Fatalf("unexpected channel %d", st.ChannelId)
		}
	}

	// Touch key1 again pinned to a different channel should migrate the count.
	s.Touch(key1, "sess-1", "g", "coding_medium", 20)
	states = s.ChannelStates()
	byID := map[int]int{}
	for _, st := range states {
		byID[st.ChannelId] = st.Sessions
	}
	if byID[10] != 0 {
		t.Fatalf("channel 10 should have 0 sessions after migration, got %d", byID[10])
	}
	if byID[20] != 2 {
		t.Fatalf("channel 20 should have 2 sessions, got %d", byID[20])
	}

	snap := s.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("expected 2 session records, got %d", len(snap))
	}
	expected := map[string]int64{"sess-1": 4, "sess-2": 1}
	for _, rec := range snap {
		if rec.ChannelId != 20 {
			t.Fatalf("all sessions should be on channel 20 after migration, got %d", rec.ChannelId)
		}
		if rec.Requests != expected[rec.SessionKey] {
			t.Fatalf("session %s requests = %d, want %d", rec.SessionKey, rec.Requests, expected[rec.SessionKey])
		}
	}
}

func TestStore_FailRecordsFailureAndCooldown(t *testing.T) {
	s := NewStore()
	key := s.keyHelper("g", "coding_medium", "sess-1")
	s.Touch(key, "sess-1", "g", "coding_medium", 10)

	until := time.Now().Add(30 * time.Second)
	s.Fail(key, 10, until)

	if !s.IsCooledDown(10, time.Now()) {
		t.Fatal("channel 10 should be cooling down")
	}
	recs := s.Snapshot()
	if len(recs) != 1 || recs[0].Failures != 1 {
		t.Fatalf("expected 1 failure on session, got %+v", recs)
	}
}

func TestStore_ForgetSession(t *testing.T) {
	s := NewStore()
	s.Touch(s.keyHelper("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	s.Touch(s.keyHelper("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)
	s.Touch(s.keyHelper("g", "other-model", "sess-1"), "sess-1", "g", "other-model", 20)
	s.Touch(s.keyHelper("g", "coding_medium", "sess-2"), "sess-2", "g", "coding_medium", 10)

	if n := s.ForgetSession("sess-1"); n != 2 {
		t.Fatalf("ForgetSession removed %d, want 2", n)
	}
	if _, ok := s.Get(s.keyHelper("g", "coding_medium", "sess-1")); ok {
		t.Fatal("sess-1 coding_medium should be removed")
	}
	if _, ok := s.Get(s.keyHelper("g", "other-model", "sess-1")); ok {
		t.Fatal("sess-1 other-model should be removed")
	}
	if _, ok := s.Get(s.keyHelper("g", "coding_medium", "sess-2")); !ok {
		t.Fatal("sess-2 should remain")
	}
}

func TestStore_PruneExpired(t *testing.T) {
	s := NewStore()
	s.SetSessionTTL(1 * time.Second)
	s.Touch(s.keyHelper("g", "coding_medium", "sess-1"), "sess-1", "g", "coding_medium", 10)

	// Force lastSeen into the past.
	s.mu.Lock()
	s.bindings[s.keyHelper("g", "coding_medium", "sess-1")].LastSeen = time.Now().Add(-2 * time.Second)
	s.mu.Unlock()

	// A new touch triggers pruning.
	s.Touch(s.keyHelper("g", "coding_medium", "sess-2"), "sess-2", "g", "coding_medium", 20)

	if _, ok := s.Get(s.keyHelper("g", "coding_medium", "sess-1")); ok {
		t.Fatal("expired session should have been pruned")
	}
	if _, ok := s.Get(s.keyHelper("g", "coding_medium", "sess-2")); !ok {
		t.Fatal("new session should remain")
	}
}

// keyHelper builds the same routing key used by the Router.
func (s *Store) keyHelper(group, model, session string) string {
	return group + "\x00" + model + "\x00" + session
}

func TestStore_CooldownExpiry(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.CoolDown(7, now.Add(10*time.Second))
	if !s.IsCooledDown(7, now.Add(5*time.Second)) {
		t.Fatal("expected cooled down within window")
	}
	if s.IsCooledDown(7, now.Add(11*time.Second)) {
		t.Fatal("expected not cooled down after expiry")
	}
}
