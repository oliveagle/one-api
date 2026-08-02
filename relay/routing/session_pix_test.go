package routing

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
)

// newRequestFromFile builds a gin context carrying one of the captured pix
// request bodies. The captures in testdata/ are real payloads recorded from
// `pi`/`pix` (OpenAI JS SDK) through a logging proxy: they carry no session id
// header and no session field in the body, which is exactly why sticky routing
// used to degrade to random load balancing for these clients.
func newRequestFromFile(t *testing.T, name string) *gin.Context {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// Reproduce the header set pix actually sends: nothing session-identifying.
	c.Request.Header.Set("User-Agent", "OpenAI/JS 6.26.0")
	c.Request.Header.Set("X-Stainless-Lang", "js")
	c.Request.Header.Set("X-Stainless-Retry-Count", "0")
	c.Request.Header.Set("X-Stainless-OS", "MacOS")
	return c
}

func withStickyDefaults(t *testing.T) {
	t.Helper()
	oldHeader, oldField, oldFP := config.SessionIdHeader, config.SessionIdBodyField, config.SessionFingerprintEnabled
	config.SessionIdHeader = "X-Session-Id"
	config.SessionIdBodyField = "session_id"
	config.SessionFingerprintEnabled = true
	t.Cleanup(func() {
		config.SessionIdHeader, config.SessionIdBodyField, config.SessionFingerprintEnabled = oldHeader, oldField, oldFP
	})
}

// The regression that motivated the fingerprint: pix sends no session id, so
// the header/body sources yield nothing and every request was unpinned.
func TestResolveSession_PixSendsNoExplicitSessionID(t *testing.T) {
	withStickyDefaults(t)
	config.SessionFingerprintEnabled = false
	key, source := ResolveSession(newRequestFromFile(t, "pix_turn1.json"))
	if key != "" || source != SourceNone {
		t.Fatalf("expected no explicit session id from a pix payload, got key=%q source=%q", key, source)
	}
}

// Core requirement: consecutive turns of one pix session must map to the same
// sticky key, so they pin to the same upstream node.
func TestResolveSession_PixSameSessionAcrossTurns(t *testing.T) {
	withStickyDefaults(t)
	turn1, src1 := ResolveSession(newRequestFromFile(t, "pix_turn1.json"))
	turn2, src2 := ResolveSession(newRequestFromFile(t, "pix_turn2.json"))
	if turn1 == "" {
		t.Fatal("turn 1 produced no session key; sticky routing would fall back to random")
	}
	if src1 != SourceFingerprint || src2 != SourceFingerprint {
		t.Fatalf("expected fingerprint source, got %q / %q", src1, src2)
	}
	if turn1 != turn2 {
		t.Fatalf("same pix session produced different keys: %q vs %q", turn1, turn2)
	}
}

// A different pix session (different first user message) must not collide, so
// separate sessions can still spread across nodes.
func TestResolveSession_PixDifferentSessionsDiffer(t *testing.T) {
	withStickyDefaults(t)
	a, _ := ResolveSession(newRequestFromFile(t, "pix_turn1.json"))
	b, _ := ResolveSession(newRequestFromFile(t, "pix_other_session.json"))
	if a == "" || b == "" {
		t.Fatalf("both sessions need keys, got %q and %q", a, b)
	}
	if a == b {
		t.Fatalf("distinct pix sessions collided on key %q", a)
	}
}

// An explicit header must still win over the fingerprint.
func TestResolveSession_HeaderWinsOverFingerprint(t *testing.T) {
	withStickyDefaults(t)
	c := newRequestFromFile(t, "pix_turn1.json")
	c.Request.Header.Set("X-Session-Id", "explicit-1")
	key, source := ResolveSession(c)
	if key != "explicit-1" || source != SourceHeader {
		t.Fatalf("got key=%q source=%q, want explicit-1/header", key, source)
	}
}

func TestResolveSession_WellKnownAliasHeaders(t *testing.T) {
	withStickyDefaults(t)
	for _, name := range []string{"X-Conversation-Id", "X-Chat-Id", "X-Thread-Id", "Session-Id", "X-Agent-Session-Id"} {
		c := newRequestFromFile(t, "pix_turn1.json")
		c.Request.Header.Set(name, "alias-"+name)
		key, source := ResolveSession(c)
		if key != "alias-"+name || source != SourceHeader {
			t.Fatalf("header %s: got key=%q source=%q", name, key, source)
		}
	}
}

// Regression: the old body-field lookup unmarshalled into map[string]string,
// which always failed on a real chat payload (arrays/objects/numbers present),
// silently ignoring SESSION_ID_BODY_FIELD.
func TestResolveSession_CustomBodyFieldInRealisticPayload(t *testing.T) {
	withStickyDefaults(t)
	config.SessionIdBodyField = "conversation_id"
	body := `{"model":"coding_medium","conversation_id":"conv-77","stream":true,
	          "max_completion_tokens":64000,"tools":[{"type":"function"}],
	          "messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	key, source := ResolveSession(c)
	if key != "conv-77" || source != SourceBody {
		t.Fatalf("got key=%q source=%q, want conv-77/body", key, source)
	}
}

func TestResolveSession_FingerprintStableWhenContentIsMultipart(t *testing.T) {
	withStickyDefaults(t)
	mk := func(extra string) *gin.Context {
		body := `{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"sys"}]},` +
			`{"role":"user","content":[{"type":"text","text":"first ask"}]}` + extra + `]}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(body)))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	first, _ := ResolveSession(mk(""))
	grown, _ := ResolveSession(mk(`,{"role":"assistant","content":"ok"},{"role":"tool","content":"out","tool_call_id":"t1"}`))
	if first == "" {
		t.Fatal("multipart content produced no fingerprint")
	}
	if first != grown {
		t.Fatalf("fingerprint changed as conversation grew: %q vs %q", first, grown)
	}
}

func TestResolveSession_ResponsesAPIStyleBody(t *testing.T) {
	withStickyDefaults(t)
	mk := func(extra string) *gin.Context {
		body := `{"model":"m","instructions":"you are helpful","input":[{"role":"user","content":"start here"}` + extra + `]}`
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(body)))
		c.Request.Header.Set("Content-Type", "application/json")
		return c
	}
	a, srcA := ResolveSession(mk(""))
	b, _ := ResolveSession(mk(`,{"role":"assistant","content":"more"}`))
	if a == "" || srcA != SourceFingerprint {
		t.Fatalf("responses payload: key=%q source=%q", a, srcA)
	}
	if a != b {
		t.Fatalf("responses fingerprint unstable: %q vs %q", a, b)
	}
}

func TestResolveSession_PreviousResponseIDIsExplicit(t *testing.T) {
	withStickyDefaults(t)
	body := `{"model":"m","previous_response_id":"resp_abc","input":"next"}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	key, source := ResolveSession(c)
	if key != "resp_abc" || source != SourceBody {
		t.Fatalf("got key=%q source=%q", key, source)
	}
}

func TestResolveSession_MetadataSessionID(t *testing.T) {
	withStickyDefaults(t)
	body := `{"model":"m","metadata":{"session_id":"meta-5","other":123},"messages":[{"role":"user","content":"x"}]}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(body)))
	c.Request.Header.Set("Content-Type", "application/json")
	key, source := ResolveSession(c)
	if key != "meta-5" || source != SourceBody {
		t.Fatalf("got key=%q source=%q", key, source)
	}
}

func TestResolveSession_FingerprintDisabled(t *testing.T) {
	withStickyDefaults(t)
	config.SessionFingerprintEnabled = false
	key, source := ResolveSession(newRequestFromFile(t, "pix_turn1.json"))
	if key != "" || source != SourceNone {
		t.Fatalf("fingerprint disabled should yield no key, got %q/%q", key, source)
	}
}

func TestResolveSession_NoBodyNoKey(t *testing.T) {
	withStickyDefaults(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses/resp_1", nil)
	if key, source := ResolveSession(c); key != "" || source != SourceNone {
		t.Fatalf("got %q/%q", key, source)
	}
}

// A sticky hit must be recorded too, otherwise the session registry reports
// requests=1 forever and, worse, LastSeen never advances so the TTL pruner can
// evict a session that is still actively sending traffic.
func TestChooseSticky_HitRecordsRequestAndRefreshesLastSeen(t *testing.T) {
	oldSticky, oldMem, oldModels := config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels
	config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels = true, true, ""
	t.Cleanup(func() {
		config.StickyRoutingEnabled, config.MemoryCacheEnabled, config.StickyModels = oldSticky, oldMem, oldModels
	})

	store := NewStore()
	r := newRouter(store, &fakeProvider{candidates: []*dbmodel.Channel{
		chanPtr(11), chanPtr(12), chanPtr(13),
	}})

	first, err := r.Choose("g", "coding_medium", "fp:sess-1")
	if err != nil {
		t.Fatalf("first choose: %v", err)
	}
	for i := 0; i < 4; i++ {
		got, err := r.Choose("g", "coding_medium", "fp:sess-1")
		if err != nil {
			t.Fatalf("choose %d: %v", i, err)
		}
		if got.Id != first.Id {
			t.Fatalf("session bounced from %d to %d", first.Id, got.Id)
		}
	}

	snap := store.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected exactly 1 session record, got %d", len(snap))
	}
	if snap[0].Requests != 5 {
		t.Fatalf("requests = %d, want 5 (1 pin + 4 hits)", snap[0].Requests)
	}
	if !snap[0].LastSeen.After(snap[0].FirstSeen) && snap[0].LastSeen.IsZero() {
		t.Fatal("LastSeen was never refreshed on a sticky hit")
	}
	// The channel session count must not be inflated by repeat hits.
	for _, st := range store.ChannelStates() {
		if st.ChannelId == first.Id && st.Sessions != 1 {
			t.Fatalf("channel %d session count = %d, want 1", st.ChannelId, st.Sessions)
		}
	}
}

// After a session fails over off a node, that node carries no sessions but is
// still in cooldown. It must remain visible on the routing page, otherwise an
// operator cannot tell why traffic is avoiding it.
func TestChannelStates_IncludesCoolingNodeWithNoSessions(t *testing.T) {
	s := NewStore()
	s.CoolDown(42, time.Now().Add(2*time.Minute))

	states := s.ChannelStates()
	var found *ChannelState
	for i := range states {
		if states[i].ChannelId == 42 {
			found = &states[i]
		}
	}
	if found == nil {
		t.Fatal("cooling node with no sessions is missing from ChannelStates")
	}
	if found.Sessions != 0 {
		t.Fatalf("sessions = %d, want 0", found.Sessions)
	}
	if found.CoolingUntil.IsZero() {
		t.Fatal("CoolingUntil should be reported")
	}
}

// An expired cooldown on an otherwise unused node must not linger in the view.
func TestChannelStates_OmitsExpiredCooldownWithNoSessions(t *testing.T) {
	s := NewStore()
	s.CoolDown(43, time.Now().Add(-time.Minute))
	for _, st := range s.ChannelStates() {
		if st.ChannelId == 43 {
			t.Fatal("expired cooldown with no sessions should not be listed")
		}
	}
}

// A node that both carries sessions and is cooling down must appear once.
func TestChannelStates_NoDuplicateForCoolingNodeWithSessions(t *testing.T) {
	s := NewStore()
	s.Touch("k", "sess", "g", "coding_medium", 44)
	s.CoolDown(44, time.Now().Add(time.Minute))
	count := 0
	for _, st := range s.ChannelStates() {
		if st.ChannelId == 44 {
			count++
			if st.Sessions != 1 {
				t.Fatalf("sessions = %d, want 1", st.Sessions)
			}
		}
	}
	if count != 1 {
		t.Fatalf("channel 44 listed %d times, want 1", count)
	}
}
