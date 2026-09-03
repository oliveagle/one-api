package middleware

import (
	"testing"
	"time"
)

func TestRPMLimiter_SlidingWindow(t *testing.T) {
	prev := rateLimitNow
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	rateLimitNow = func() time.Time { return base }
	t.Cleanup(func() { rateLimitNow = prev })
	ResetRPMLimiter()

	const id, limit = 1, 2
	if !tokenRPMLimiter.Allow(id, limit) || !tokenRPMLimiter.Allow(id, limit) {
		t.Fatal("first two requests must fit the window")
	}
	if tokenRPMLimiter.Allow(id, limit) {
		t.Fatal("third request inside the window must be rejected")
	}
	// Rejections are not counted: advancing time by the full window drains.
	rateLimitNow = func() time.Time { return base.Add(rpmWindow + time.Second) }
	if !tokenRPMLimiter.Allow(id, limit) {
		t.Fatal("after the window slides the token must be allowed again")
	}
	// Sliding property: 30s-old event + fresh one still counts both.
	ResetRPMLimiter()
	rateLimitNow = func() time.Time { return base }
	tokenRPMLimiter.Allow(id, limit)
	rateLimitNow = func() time.Time { return base.Add(30 * time.Second) }
	tokenRPMLimiter.Allow(id, limit)
	rateLimitNow = func() time.Time { return base.Add(31 * time.Second) }
	if tokenRPMLimiter.Allow(id, limit) {
		t.Fatal("two events inside 60s must saturate a limit-2 window")
	}
}

func TestRPMLimiter_ZeroLimitDisables(t *testing.T) {
	ResetRPMLimiter()
	for i := 0; i < 100; i++ {
		if !tokenRPMLimiter.Allow(7, 0) {
			t.Fatal("limit 0 must never throttle")
		}
	}
}

func TestRPMLimiter_TokensAreIndependent(t *testing.T) {
	ResetRPMLimiter()
	if !tokenRPMLimiter.Allow(1, 1) || tokenRPMLimiter.Allow(1, 1) {
		t.Fatal("token 1 limited at 1 rpm")
	}
	if !tokenRPMLimiter.Allow(2, 1) {
		t.Fatal("token 2 must not be affected by token 1's budget")
	}
}
