package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/ctxkey"
)

// Regression: every upstream 429 used to be rewritten to a single
// "上游负载已饱和" string. That erased the difference between short-lived
// concurrency throttling and an exhausted quota, so an operator could not tell
// from the response why the request failed. The real case that surfaced this was
// an upstream replying "Monthly usage limit reached. Resets in 3 days." — advice
// to "retry shortly" was actively wrong there.
func TestDescribeUpstream429_PreservesUpstreamReason(t *testing.T) {
	upstream := "Monthly usage limit reached. Resets in 3 days. " +
		"To continue using this model now, enable usage from your available balance: https://example.test/go"
	got := describeUpstream429(upstream)

	if !strings.Contains(got, "当前分组上游负载已饱和") {
		t.Fatalf("generic hint should be retained, got %q", got)
	}
	if !strings.Contains(got, "Monthly usage limit reached") {
		t.Fatalf("upstream reason must survive, got %q", got)
	}
	if !strings.Contains(got, "Resets in 3 days") {
		t.Fatalf("upstream detail must survive, got %q", got)
	}
	// The actionable link is part of the reason and must not be truncated away.
	if !strings.Contains(got, "https://example.test/go") {
		t.Fatalf("upstream link must survive, got %q", got)
	}
}

func TestDescribeUpstream429_EmptyUpstreamFallsBackToGeneric(t *testing.T) {
	if got := describeUpstream429(""); got != "当前分组上游负载已饱和，请稍后再试" {
		t.Fatalf("got %q", got)
	}
	if got := describeUpstream429("   \n\t "); got != "当前分组上游负载已饱和，请稍后再试" {
		t.Fatalf("whitespace-only upstream should fall back, got %q", got)
	}
}

func TestDescribeUpstream429_DistinguishesThrottlingFromQuota(t *testing.T) {
	throttle := describeUpstream429("Rate limit exceeded, please slow down")
	quota := describeUpstream429("Monthly usage limit reached. Resets in 3 days.")
	if throttle == quota {
		t.Fatal("two different upstream causes must not produce identical messages")
	}
	if !strings.Contains(throttle, "Rate limit exceeded") || !strings.Contains(quota, "Monthly usage limit") {
		t.Fatalf("each cause must be visible:\n  %q\n  %q", throttle, quota)
	}
}

// shouldRetry drives the cooldown decision after the fix, so pin down which
// statuses are treated as "the node misbehaved".
func TestShouldRetry_DrivesCooldownDecision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(specificChannel bool) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		if specificChannel {
			c.Set(ctxkey.SpecificChannelId, "7")
		}
		return c
	}

	// 429 is the case from the incident: the node is over quota, so the session
	// must be steered away from it.
	if !shouldRetry(newCtx(false), http.StatusTooManyRequests) {
		t.Fatal("429 must be retryable so the failed node gets cooled down")
	}
	if !shouldRetry(newCtx(false), http.StatusInternalServerError) {
		t.Fatal("5xx must be retryable")
	}
	// A client error is the caller's fault; cooling the node would punish a
	// healthy upstream.
	if shouldRetry(newCtx(false), http.StatusBadRequest) {
		t.Fatal("400 must not be retryable")
	}
	if shouldRetry(newCtx(false), http.StatusOK) {
		t.Fatal("2xx must not be retryable")
	}
	// When the caller pinned a specific channel there is nowhere to fail over to.
	if shouldRetry(newCtx(true), http.StatusTooManyRequests) {
		t.Fatal("an explicitly targeted channel must not trigger failover")
	}
}
