package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/ctxkey"
)

// rpmLimiter is a per-token sliding-window request counter. Tokens with
// RPMLimit <= 0 are never limited. The window is in-memory only — a restart
// clears counters, which is the right trade for a per-instance throttle.
type rpmLimiter struct {
	mu     sync.Mutex
	events map[int][]time.Time
}

var tokenRPMLimiter = &rpmLimiter{events: make(map[int][]time.Time)}

// rateLimitNow is swappable so tests can time-travel through the window.
var rateLimitNow = time.Now

const rpmWindow = time.Minute

// Allow records one request for the token and reports whether it fits the
// per-minute limit. Over-budget events are NOT recorded (the rejected
// request must not deepen the hole).
func (l *rpmLimiter) Allow(tokenId, limit int) bool {
	if limit <= 0 {
		return true
	}
	now := rateLimitNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events[tokenId]
	keep := events[:0]
	for _, ts := range events {
		if now.Sub(ts) < rpmWindow {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= limit {
		l.events[tokenId] = keep
		return false
	}
	l.events[tokenId] = append(keep, now)
	return true
}

// nextSlotIn reports how long until the oldest counted event leaves the
// window (the Retry-After hint for a rejected request).
func (l *rpmLimiter) nextSlotIn(tokenId int) time.Duration {
	now := rateLimitNow()
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.events[tokenId]
	if len(events) == 0 {
		return 0
	}
	if wait := rpmWindow - now.Sub(events[0]); wait > 0 {
		return wait
	}
	return 0
}

// ResetRPMLimiter clears all counters (test isolation).
func ResetRPMLimiter() {
	tokenRPMLimiter.mu.Lock()
	defer tokenRPMLimiter.mu.Unlock()
	tokenRPMLimiter.events = make(map[int][]time.Time)
}

// enforceTokenRPM applies the token's RPM limit on relay POSTs. Rejections
// carry Retry-After so well-behaved clients back off instead of hammering.
func enforceTokenRPM(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		return
	}
	limit := c.GetInt(ctxkey.TokenRPMLimit)
	if limit <= 0 {
		return
	}
	tokenId := c.GetInt(ctxkey.TokenId)
	if tokenRPMLimiter.Allow(tokenId, limit) {
		return
	}
	wait := tokenRPMLimiter.nextSlotIn(tokenId)
	c.Header("Retry-After", strconv.FormatInt(maxI64(int64(wait.Seconds()), 1), 10))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": gin.H{
			"message": "request rate limit exceeded for this token (rpm_limit=" + strconv.Itoa(limit) + ")",
			"type":    "one_api_error",
			"code":    "token_rpm_exceeded",
		},
	})
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
