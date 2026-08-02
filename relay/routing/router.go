package routing

import (
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
)

// ErrNoChannel is returned when no eligible channel can be found.
var ErrNoChannel = errors.New("no satisfied channel")

// channelProvider abstracts the channel lookup used by the Router so the sticky
// selection logic can be unit tested without a database. The default
// implementation reads the in-memory channel cache.
type channelProvider interface {
	// RandomSatisfied mirrors model.CacheGetRandomSatisfiedChannel.
	RandomSatisfied(group, model string, ignoreFirstPriority bool) (*dbmodel.Channel, error)
	// SatisfiedChannels mirrors model.CacheGetSatisfiedChannels.
	SatisfiedChannels(group, model string) []*dbmodel.Channel
}

// dbChannelProvider is the production implementation backed by the shared
// channel cache.
type dbChannelProvider struct{}

func (dbChannelProvider) RandomSatisfied(group, model string, ignoreFirstPriority bool) (*dbmodel.Channel, error) {
	return dbmodel.CacheGetRandomSatisfiedChannel(group, model, ignoreFirstPriority)
}

func (dbChannelProvider) SatisfiedChannels(group, model string) []*dbmodel.Channel {
	return dbmodel.CacheGetSatisfiedChannels(group, model)
}

// Router performs session-sticky routing with failover on top of the shared
// channel cache. A session is pinned to a single upstream node ("channel") so
// that session-local state stays warm; if that node later fails with a
// retryable error, the relay fails over to another node and re-pins the session
// to it.
type Router struct {
	store    *Store
	provider channelProvider
}

// NewRouter builds a Router backed by the default channel cache. When store is
// nil a new empty Store is created.
func NewRouter(store *Store) *Router {
	return newRouter(store, dbChannelProvider{})
}

func newRouter(store *Store, provider channelProvider) *Router {
	if store == nil {
		store = NewStore()
	}
	store.SetSessionTTL(time.Duration(config.StickySessionTTLSeconds) * time.Second)
	return &Router{store: store, provider: provider}
}

// Store exposes the underlying sticky store (used for diagnostics/tests).
func (r *Router) Store() *Store {
	return r.store
}

// Enabled reports whether session-sticky routing is active at all. It requires
// both the feature flag and the in-memory channel cache (which provides the
// candidate list and the process-local store).
func (r *Router) Enabled() bool {
	return config.StickyRoutingEnabled && config.MemoryCacheEnabled
}

// StickyAppliesTo reports whether a request for model with the given session
// key will actually be sticky-routed. Used for diagnostics/logging so an
// operator can tell at a glance why a request was or was not pinned.
func (r *Router) StickyAppliesTo(model, session string) bool {
	return session != "" && r.enabledFor(model)
}

// enabledFor reports whether sticky routing applies to the given model.
func (r *Router) enabledFor(model string) bool {
	if !r.Enabled() {
		return false
	}
	if config.StickyModels == "" {
		return true
	}
	for _, m := range strings.Split(config.StickyModels, ",") {
		if strings.TrimSpace(m) == model {
			return true
		}
	}
	return false
}

func (r *Router) key(group, model, session string) string {
	return group + "\x00" + model + "\x00" + session
}

// Choose selects the channel for a request. When the request belongs to a
// session and sticky routing is enabled for the model, it returns the already
// pinned channel (when still valid and not cooled down) or pins a fresh one.
// Otherwise it falls back to the default random selection.
func (r *Router) Choose(group, model, session string) (*dbmodel.Channel, error) {
	if session == "" || !r.enabledFor(model) {
		return r.provider.RandomSatisfied(group, model, false)
	}
	return r.chooseSticky(group, model, session)
}

func (r *Router) chooseSticky(group, model, session string) (*dbmodel.Channel, error) {
	candidates := r.provider.SatisfiedChannels(group, model)
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}
	now := time.Now()
	key := r.key(group, model, session)
	if id, ok := r.store.Get(key); ok {
		if ch := findChannel(candidates, id); ch != nil && !r.store.IsCooledDown(id, now) {
			// Record the hit as well as the miss: this keeps the request
			// counter meaningful and, critically, refreshes LastSeen so the
			// TTL pruner does not evict a session that is still active.
			r.store.Touch(key, session, group, model, ch.Id)
			return ch, nil
		}
		// stale binding: the node is gone for this model or still cooling down
		r.store.Forget(key)
	}
	// Prefer non-cooled-down nodes for a fresh binding so a session does not
	// immediately land on a node that recently failed. If every node is cooling
	// down, fall back to the full list rather than failing the request.
	var eligible []*dbmodel.Channel
	for _, ch := range candidates {
		if !r.store.IsCooledDown(ch.Id, now) {
			eligible = append(eligible, ch)
		}
	}
	if len(eligible) == 0 {
		eligible = candidates
	}
	ch := pickFirstPriority(eligible)
	r.store.Touch(key, session, group, model, ch.Id)
	return ch, nil
}

// ChooseAlternative selects a failover channel for a session, excluding the
// currently pinned/failed node and any other channels that already failed in
// this request, then re-pins the session to the chosen node. It is used by the
// relay retry loop after a retryable error.
func (r *Router) ChooseAlternative(group, model, session string, exclude map[int]bool) (*dbmodel.Channel, error) {
	if session == "" || !r.enabledFor(model) {
		return r.provider.RandomSatisfied(group, model, true)
	}
	candidates := r.provider.SatisfiedChannels(group, model)
	if len(candidates) == 0 {
		return nil, ErrNoChannel
	}
	key := r.key(group, model, session)
	now := time.Now()
	var eligible []*dbmodel.Channel
	for _, ch := range candidates {
		if exclude != nil && exclude[ch.Id] {
			continue
		}
		if r.store.IsCooledDown(ch.Id, now) {
			continue
		}
		eligible = append(eligible, ch)
	}
	if len(eligible) == 0 {
		return nil, ErrNoChannel
	}
	ch := eligible[rand.Intn(len(eligible))]
	r.store.Touch(key, session, group, model, ch.Id)
	return ch, nil
}

// Fail puts channelId into cooldown after a retryable error so the session does
// not immediately bounce back to a node that just failed. When session is
// non-empty it also records the failure against the session record.
func (r *Router) Fail(group, model, session string, channelId int) {
	var until time.Time
	if config.StickyCooldownSeconds > 0 {
		until = time.Now().Add(time.Duration(config.StickyCooldownSeconds) * time.Second)
		r.store.CoolDown(channelId, until)
	}
	if session != "" {
		r.store.Fail(r.key(group, model, session), channelId, until)
	}
}

func findChannel(candidates []*dbmodel.Channel, id int) *dbmodel.Channel {
	for _, ch := range candidates {
		if ch.Id == id {
			return ch
		}
	}
	return nil
}

// pickFirstPriority returns a random channel from the top-priority tier. It
// mirrors the priority handling of model.CacheGetRandomSatisfiedChannel.
func pickFirstPriority(candidates []*dbmodel.Channel) *dbmodel.Channel {
	endIdx := len(candidates)
	if first := candidates[0].GetPriority(); first > 0 {
		endIdx = 1
		for endIdx < len(candidates) && candidates[endIdx].GetPriority() == first {
			endIdx++
		}
	}
	return candidates[rand.Intn(endIdx)]
}

// defaultRouter is the process-wide router shared by the distributor middleware
// and the relay retry loop so bindings stay consistent.
var defaultRouter = NewRouter(nil)

// DefaultRouter returns the process-wide session-sticky router.
func DefaultRouter() *Router {
	return defaultRouter
}
