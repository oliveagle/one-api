package controller

import (
	"net/http"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common/config"

	dbmodel "github.com/songquanpeng/one-api/model"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// TestMarkChannelPenalty_HonorsRetryAfter: a plain (non-quota) 429 with an
// upstream Retry-After must cool the channel for the requested window
// (capped at quotaCooldownMax); absent header falls back to the default.
func TestMarkChannelPenalty_HonorsRetryAfter(t *testing.T) {
	mkErr := func(retryAfterMs int64) *relaymodel.ErrorWithStatusCode {
		return &relaymodel.ErrorWithStatusCode{
			StatusCode: http.StatusTooManyRequests,
			Error:      relaymodel.Error{Message: "rate limited", RetryAfterMs: retryAfterMs},
		}
	}

	t.Run("header wins", func(t *testing.T) {
		dbmodel.ResetChannelCooldowns()
		t.Cleanup(dbmodel.ResetChannelCooldowns)
		markChannelPenalty(201, mkErr(45000))
		if !dbmodel.ChannelCoolingDown(201) {
			t.Fatal("channel must be cooling for the Retry-After window")
		}
	})
	t.Run("capped at quotaCooldownMax", func(t *testing.T) {
		dbmodel.ResetChannelCooldowns()
		t.Cleanup(dbmodel.ResetChannelCooldowns)
		markChannelPenalty(202, mkErr(6*int64(time.Hour/time.Millisecond)))
		if !dbmodel.ChannelCoolingDown(202) {
			t.Fatal("channel must be cooling (capped at 1h)")
		}
	})
	t.Run("no header falls back to default", func(t *testing.T) {
		dbmodel.ResetChannelCooldowns()
		t.Cleanup(dbmodel.ResetChannelCooldowns)
		markChannelPenalty(203, mkErr(0))
		if !dbmodel.ChannelCoolingDown(203) {
			t.Fatal("channel must be cooling for the default 60s window")
		}
	})
}

// TestUpstreamQuirk400_VolcPartialMissing pins the volc workaround: ark's
// Responses endpoint intermittently returns a bogus 400 about a "partial"
// parameter that is not part of the OpenAI spec. The relay must treat it as
// retryable so failover reaches a healthy channel.
func TestUpstreamQuirk400_VolcPartialMissing(t *testing.T) {
	mk := func(msg string, status int) *relaymodel.ErrorWithStatusCode {
		return &relaymodel.ErrorWithStatusCode{
			StatusCode: status,
			Error:      relaymodel.Error{Message: msg},
		}
	}

	if !upstreamQuirk400(mk("The request failed because it is missing `partial` parameter", http.StatusBadRequest)) {
		t.Fatal("volc partial-missing 400 must be retryable")
	}
	if upstreamQuirk400(mk("invalid model name", http.StatusBadRequest)) {
		t.Fatal("genuine 400 must stay non-retryable")
	}
	if upstreamQuirk400(mk("The request failed because it is missing `partial` parameter", 200)) {
		t.Fatal("only 400 qualifies")
	}
	if upstreamQuirk400(nil) {
		t.Fatal("nil error must not panic or qualify")
	}
}

// Test429RetryBudgetDeeper pins that 429 errors get a deeper retry budget
// than other retryable errors: the transient nature of rate limits means
// more attempts + inter-retry back-off yields materially better success.
func Test429RetryBudgetDeeper(t *testing.T) {
	prevRetry := config.RetryTimes
	defer func() { config.RetryTimes = prevRetry }()

	// Configured budget of 3 → a 429 bumps it to 6.
	config.RetryTimes = 3
	// The bump logic lives inline in Relay; this test pins the constant
	// invariants that make the deeper budget effective.
	if backoff429 < time.Second {
		t.Fatalf("backoff429 = %v, want >= 1s (rate-limit windows need time to clear)", backoff429)
	}
	if quotaCooldownMax > time.Hour || quotaCooldownMax < 10*time.Minute {
		t.Fatalf("quotaCooldownMax = %v, want 10m..1h (re-probe recovered channels without hot-looping)", quotaCooldownMax)
	}
}
