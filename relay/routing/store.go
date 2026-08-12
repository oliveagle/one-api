package routing

import (
	"sort"
	"sync"
	"time"
)

// SessionRecord is the observable state of one sticky-routed session, exposed
// to the routing management page via Snapshot().
type SessionRecord struct {
	SessionKey          string    `json:"session_key"`
	Group               string    `json:"group"`
	Model               string    `json:"model"`
	ChannelId           int       `json:"channel_id"`
	Requests            int64     `json:"requests"`
	Failures            int64     `json:"failures"`
	ConsecutiveFailures int64     `json:"consecutive_failures"` // reset to 0 on each success
	FirstSeen           time.Time `json:"first_seen"`
	LastSeen            time.Time `json:"last_seen"`
}

// ChannelState is the aggregated observable state of one upstream node
// (channel) across all sessions, exposed to the routing management page.
// Display fields (Name, ResponseTime, Balance, Status, Priority, Requests)
// are populated by the controller from the DB after building the snapshot.
type ChannelState struct {
	ChannelId    int       `json:"channel_id"`
	Name         string    `json:"name"`
	Sessions     int       `json:"sessions"`
	Requests     int64     `json:"requests"`      // cumulative requests across sessions on this channel
	Failures     int64     `json:"failures"`      // cumulative failures
	ResponseTime int       `json:"response_time"` // ms, from DB
	Balance      float64   `json:"balance"`       // USD, from DB
	Status       int       `json:"status"`        // 1=enabled,2=manual-disabled,3=auto-disabled
	Priority     int64     `json:"priority"`      // configured priority weight
	CoolingUntil time.Time `json:"cooling_until"`
	Busyness     float64   `json:"busyness"` // composite sort score
}

// Store holds the in-memory session -> channel sticky bindings, a registry of
// session records for observability, and a per-channel cooldown used after a
// failover.
//
// NOTE: like the channel memory cache, the store is process-local. In a
// multi-instance deployment each instance keeps its own bindings, so a session
// may land on different nodes across instances. If strict cross-instance
// stickiness is required, back the store with a shared key/value store (e.g.
// Redis) behind the same interface.
type Store struct {
	mu              sync.RWMutex
	bindings        map[string]*SessionRecord
	cooldown        map[int]time.Time
	channelSessions map[int]int
	// sessionTTL is how long a session record is kept after its last request
	// before being pruned. Zero/negative disables pruning.
	sessionTTL time.Duration
}

func NewStore() *Store {
	return &Store{
		bindings:        make(map[string]*SessionRecord),
		cooldown:        make(map[int]time.Time),
		channelSessions: make(map[int]int),
	}
}

// Get returns the channel id currently pinned to key.
func (s *Store) Get(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.bindings[key]
	if !ok {
		return 0, false
	}
	return rec.ChannelId, true
}

// GetConsecutiveFailures returns the number of consecutive failures for the
// session bound to key. Used by the Router to decide whether to keep the
// binding or migrate the session to a different channel.
func (s *Store) GetConsecutiveFailures(key string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rec, ok := s.bindings[key]; ok {
		return rec.ConsecutiveFailures
	}
	return 0
}

// Touch records a routed request for a session, (re)pinning the session to
// channelId. It updates the session record and the per-channel session counts.
func (s *Store) Touch(key, sessionKey, group, model string, channelId int) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.bindings[key]
	if !ok {
		rec = &SessionRecord{
			SessionKey: sessionKey,
			Group:      group,
			Model:      model,
			ChannelId:  channelId,
			FirstSeen:  now,
		}
		s.bindings[key] = rec
		// A brand-new session landing on this channel bumps its session count.
		s.incChannelLocked(channelId)
	} else if rec.ChannelId != channelId {
		s.decChannelLocked(rec.ChannelId)
		rec.ChannelId = channelId
		// The session migrated to a new channel: move the count.
		s.incChannelLocked(channelId)
	}
	rec.Requests++
	rec.ConsecutiveFailures = 0 // success resets the streak
	rec.LastSeen = now
	s.pruneLocked(now)
}

// Fail records a failure for the session bound to key on channelId and marks
// that channel as cooling down until the given time.
func (s *Store) Fail(key string, channelId int, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !until.IsZero() {
		s.cooldown[channelId] = until
	}
	if rec, ok := s.bindings[key]; ok {
		rec.Failures++
		rec.ConsecutiveFailures++
		rec.LastSeen = time.Now()
	}
}

// Forget removes the binding for a specific routing key.
func (s *Store) Forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetLocked(key)
}

// ForgetSession removes every binding whose session key equals sessionKey.
// It returns the number of removed sessions.
func (s *Store) ForgetSession(sessionKey string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, rec := range s.bindings {
		if rec.SessionKey == sessionKey {
			s.forgetLocked(key)
			removed++
		}
	}
	return removed
}

func (s *Store) forgetLocked(key string) {
	if rec, ok := s.bindings[key]; ok {
		s.decChannelLocked(rec.ChannelId)
		delete(s.bindings, key)
	}
}

// CoolDown marks channelId as unavailable until the given time.
func (s *Store) CoolDown(channelId int, until time.Time) {
	if until.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldown[channelId] = until
}

// IsCooledDown reports whether channelId is currently cooled down at time now.
func (s *Store) IsCooledDown(channelId int, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	until, ok := s.cooldown[channelId]
	if !ok {
		return false
	}
	return now.Before(until)
}

// Snapshot returns a copy of the current session records, sorted by last seen
// (most recent first), for the routing management page.
func (s *Store) Snapshot() []SessionRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]SessionRecord, 0, len(s.bindings))
	for _, rec := range s.bindings {
		records = append(records, *rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].LastSeen.After(records[j].LastSeen)
	})
	return records
}

// ChannelStates returns the aggregated per-channel state (active session count,
// cumulative request/failure counts, and cooldown expiry), sorted by channel
// id, for the routing management page. Display fields (Name, ResponseTime,
// etc.) are populated by the controller from the DB.
func (s *Store) ChannelStates() []ChannelState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now()

	// Aggregate per-channel request and failure counts from session bindings.
	channelRequests := make(map[int]int64)
	channelFailures := make(map[int]int64)
	for _, rec := range s.bindings {
		channelRequests[rec.ChannelId] += rec.Requests
		channelFailures[rec.ChannelId] += rec.Failures
	}

	seen := make(map[int]bool, len(s.channelSessions)+len(s.cooldown))
	ids := make([]int, 0, len(s.channelSessions)+len(s.cooldown))
	for id := range s.channelSessions {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// Include nodes that are cooling down even when they carry no sessions.
	// After a failover the session moves away, so the failed node would
	// otherwise disappear from the routing page while still being skipped.
	for id, until := range s.cooldown {
		if now.Before(until) && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	states := make([]ChannelState, 0, len(ids))
	for _, id := range ids {
		state := ChannelState{
			ChannelId: id,
			Sessions:  s.channelSessions[id],
			Requests:  channelRequests[id],
			Failures:  channelFailures[id],
		}
		if until, ok := s.cooldown[id]; ok && now.Before(until) {
			state.CoolingUntil = until
		}
		states = append(states, state)
	}
	return states
}

// Clear drops all bindings, session records, cooldowns and channel counts.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = make(map[string]*SessionRecord)
	s.cooldown = make(map[int]time.Time)
	s.channelSessions = make(map[int]int)
}

// SetSessionTTL configures how long stale session records are kept. A value <=
// 0 disables pruning.
func (s *Store) SetSessionTTL(ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionTTL = ttl
}

func (s *Store) incChannelLocked(id int) {
	s.channelSessions[id]++
}

func (s *Store) decChannelLocked(id int) {
	if v, ok := s.channelSessions[id]; ok {
		if v <= 1 {
			delete(s.channelSessions, id)
		} else {
			s.channelSessions[id] = v - 1
		}
	}
}

// pruneLocked removes session records that have not been seen for the TTL.
func (s *Store) pruneLocked(now time.Time) {
	if s.sessionTTL <= 0 {
		return
	}
	cutoff := now.Add(-s.sessionTTL)
	for key, rec := range s.bindings {
		if rec.LastSeen.Before(cutoff) {
			s.forgetLocked(key)
		}
	}
}

// Rebind forcibly points key at channelId, creating the record if needed. It is
// the explicit-user-choice counterpart to Touch: it does not increment the
// request counter, because no request has been routed yet.
func (s *Store) Rebind(key, sessionKey, group, model string, channelId int) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.bindings[key]
	if !ok {
		s.bindings[key] = &SessionRecord{
			SessionKey: sessionKey,
			Group:      group,
			Model:      model,
			ChannelId:  channelId,
			FirstSeen:  now,
			LastSeen:   now,
		}
		s.incChannelLocked(channelId)
		return
	}
	if rec.ChannelId != channelId {
		s.decChannelLocked(rec.ChannelId)
		rec.ChannelId = channelId
		s.incChannelLocked(channelId)
	}
	rec.LastSeen = now
}

// ClearCooldown removes any cooldown recorded for channelId. Used when a user
// explicitly selects a node, which overrides the automatic health judgement.
func (s *Store) ClearCooldown(channelId int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooldown, channelId)
}

// IsCooledDownNow reports whether channelId is cooling down at the current time.
func (s *Store) IsCooledDownNow(channelId int) bool {
	return s.IsCooledDown(channelId, time.Now())
}
