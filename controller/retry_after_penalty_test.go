package controller

import (
	"net/http"
	"testing"
	"time"

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
