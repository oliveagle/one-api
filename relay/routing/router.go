package routing

import (
	"errors"
	"math/rand"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common/config"
	dbmodel "github.com/songquanpeng/one-api/model"
)

// channelProvider abstracts the channel lookup used by the Router so the sticky
// router does not directly depend on the global channel cache.
type channelProvider interface {
	// SatisfiedChannels returns all enabled channels for the given user group
	// and model, ordered by priority (highest first).
	SatisfiedChannels(group, model string) []*dbmodel.Channel
	// RandomSatisfied returns a random channel (the non-sticky fallback).
	RandomSatisfied(group, model string, must bool) (*dbmodel.Channel, error)
}

// ErrNoChannel is returned when no eligible channel is available.
var ErrNoChannel = errors.New("no available channel")

// Router performs session-sticky routing with failover on top of the shared
// channel cache.  It is safe for concurrent use.
type Router struct {
	provider channelProvider
	store    *Store
}

// NewRouter creates a Router backed by the given provider and store.
func NewRouter(provider channelProvider, store *Store) *Router {
	return &Router{provider: provider, store: store}
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
	threshold := int64(config.StickyFailureThreshold)
	if threshold <= 0 {
		threshold = 1
	}

	if id, ok := r.store.Get(key); ok {
		ch := findChannel(candidates, id)
		if ch == nil {
			// channel is gone for this model — full migration
			r.store.Forget(key)
		} else if !r.store.IsCooledDown(id, now) && !dbmodel.ChannelCoolingDown(id) {
			// happy path: channel exists, healthy, and not under a global
			// 429 routing penalty (quota/rate-limit cooldown)
			r.store.Touch(key, session, group, model, ch.Id)
			return ch, nil
		} else if r.store.GetConsecutiveFailures(key) < threshold {
			// cooled down, but not enough consecutive failures to migrate the
			// session — pick an alternative for THIS request only, keep the
			// binding alive so the session returns to this channel once the
			// cooldown expires. Don't Touch (don't re-pin).
			return r.chooseAlternativeFrom(candidates, id)
		} else {
			// exceeded failure threshold — real migration
			r.store.Forget(key)
		}
	}

	// Prefer non-cooled-down nodes for a fresh binding so a session does not
	// immediately land on a node that recently failed. If every node is cooling
	// down, fall back to the full list rather than failing the request.
	var eligible []*dbmodel.Channel
	for _, ch := range candidates {
		if r.store.IsCooledDown(ch.Id, now) || dbmodel.ChannelCoolingDown(ch.Id) {
			continue
		}
		eligible = append(eligible, ch)
	}
	if len(eligible) == 0 {
		eligible = candidates
	}
	ch := pickFirstPriority(eligible)
	r.store.Touch(key, session, group, model, ch.Id)
	return ch, nil
}

// chooseAlternativeFrom picks a channel from candidates that is not the
// excluded id and is not cooled down. It does NOT re-pin the session — used
// for transient failover while keeping the session bound to the original
// channel.
func (r *Router) chooseAlternativeFrom(candidates []*dbmodel.Channel, excludeId int) (*dbmodel.Channel, error) {
	now := time.Now()
	var eligible []*dbmodel.Channel
	for _, ch := range candidates {
		if ch.Id == excludeId {
			continue
		}
		if r.store.IsCooledDown(ch.Id, now) || dbmodel.ChannelCoolingDown(ch.Id) {
			continue
		}
		eligible = append(eligible, ch)
	}
	if len(eligible) == 0 {
		// every other node is cooling down too — fall back to full list
		for _, ch := range candidates {
			if ch.Id != excludeId {
				eligible = append(eligible, ch)
			}
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoChannel
	}
	return eligible[rand.Intn(len(eligible))], nil
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

	// Stickiness: only re-pin the session to the alternative channel when the
	// original channel has accumulated enough consecutive failures to prove it
	// is persistently unhealthy. Below the threshold this is a transient
	// failover — the session keeps its binding so it returns to the original
	// node once the cooldown expires (preserving prompt cache / KV memory).
	threshold := int64(config.StickyFailureThreshold)
	if threshold <= 0 {
		threshold = 1
	}
	if r.store.GetConsecutiveFailures(key) >= threshold {
		r.store.Touch(key, session, group, model, ch.Id)
	}
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

// pickFirstPriority returns a random channel from among the highest-priority
// channels in the slice. Callers must ensure the slice is non-empty.
func pickFirstPriority(channels []*dbmodel.Channel) *dbmodel.Channel {
	if len(channels) == 1 {
		return channels[0]
	}
	bestPriority := channelPriority(channels[0])
	topTier := []*dbmodel.Channel{channels[0]}
	for _, ch := range channels[1:] {
		p := channelPriority(ch)
		if p > bestPriority {
			bestPriority = p
			topTier = []*dbmodel.Channel{ch}
		} else if p == bestPriority {
			topTier = append(topTier, ch)
		}
	}
	return topTier[rand.Intn(len(topTier))]
}

func channelPriority(ch *dbmodel.Channel) int64 {
	if ch.Priority != nil {
		return *ch.Priority
	}
	return 0
}

// DefaultRouter returns the process-wide session-sticky router.
func DefaultRouter() *Router {
	return defaultRouter
}

var defaultRouter = &Router{
	provider: &channelCacheProvider{},
	store:    NewStore(),
}

// newRouter creates a Router with explicit provider and store, used in tests.
func newRouter(store *Store, provider channelProvider) *Router {
	return &Router{provider: provider, store: store}
}

// channelCacheProvider adapts the global channel memory cache to the
// channelProvider interface.
type channelCacheProvider struct{}

func (p *channelCacheProvider) SatisfiedChannels(group, model string) []*dbmodel.Channel {
	return dbmodel.CacheGetSatisfiedChannels(group, model)
}

func (p *channelCacheProvider) RandomSatisfied(group, model string, must bool) (*dbmodel.Channel, error) {
	// nil exclude = apply only the global 429 cooldowns, with the plain
	// random pick as fallback when every candidate is cooling.
	return dbmodel.CacheGetRandomSatisfiedChannelExcluding(group, model, must, nil)
}
