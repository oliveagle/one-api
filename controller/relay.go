package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/middleware"
	dbmodel "github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/monitor"
	"github.com/songquanpeng/one-api/relay/controller"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
	"github.com/songquanpeng/one-api/relay/routing"
)

// https://platform.openai.com/docs/api-reference/chat

func relayHelper(c *gin.Context, relayMode int) *model.ErrorWithStatusCode {
	var err *model.ErrorWithStatusCode
	switch relayMode {
	case relaymode.ImagesGenerations:
		err = controller.RelayImageHelper(c, relayMode)
	case relaymode.AudioSpeech:
		fallthrough
	case relaymode.AudioTranslation:
		fallthrough
	case relaymode.AudioTranscription:
		err = controller.RelayAudioHelper(c, relayMode)
	case relaymode.Proxy:
		err = controller.RelayProxyHelper(c, relayMode)
	case relaymode.Responses:
		err = controller.RelayResponsesHelper(c)
	default:
		err = controller.RelayTextHelper(c)
	}
	return err
}

func Relay(c *gin.Context) {
	ctx := c.Request.Context()
	relayMode := relaymode.GetByPath(c.Request.URL.Path)
	if config.DebugEnabled {
		requestBody, _ := common.GetRequestBody(c)
		logger.Debugf(ctx, "request body: %s", string(requestBody))
	}
	channelId := c.GetInt(ctxkey.ChannelId)
	userId := c.GetInt(ctxkey.Id)
	bizErr := relayHelper(c, relayMode)
	if bizErr == nil {
		monitor.Emit(channelId, true)
		return
	}
	lastFailedChannelId := channelId
	channelName := c.GetString(ctxkey.ChannelName)
	group := c.GetString(ctxkey.Group)
	originalModel := c.GetString(ctxkey.OriginalModel)
	go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
	requestId := c.GetString(helper.RequestIdKey)
	// retryable describes the *error*; retryTimes describes the configuration.
	// Keep them separate: cooldown depends on the former, the retry loop on both.
	retryable := shouldRetry(c, bizErr.StatusCode) || upstreamQuirk400(bizErr)
	retryTimes := config.RetryTimes
	// 429s deserve a deeper retry budget: they are transient by nature and
	// the back-off between attempts gives the upstream time to recover.
	// Non-429 errors (5xx etc.) keep the configured budget.
	if retryable && bizErr.StatusCode == http.StatusTooManyRequests && retryTimes < 6 {
		retryTimes = 6
	}
	if !retryable {
		logger.Errorf(ctx, "relay error happen, status code is %d, won't retry in this case", bizErr.StatusCode)
		retryTimes = 0
	}
	sessionKey := c.GetString(ctxkey.SessionKey)
	router := routing.DefaultRouter()
	exclude := make(map[int]bool)
	exclude[lastFailedChannelId] = true
	// After a retryable error, cool the failed node down so this session (and
	// others) do not immediately bounce back to it. Non-retryable errors (e.g.
	// client 400s) are the client's fault, not the node's, so they do not cool
	// the node down.
	//
	// This is keyed on whether the error itself is retryable, NOT on whether
	// retries are enabled. Cooldown outlives this request: it also steers the
	// *next* request away from a node that just failed. Gating it on
	// retryTimes > 0 meant that with RetryTimes=0 a sticky session stayed pinned
	// to a node known to be broken (e.g. an upstream whose monthly quota is
	// exhausted) and kept hitting it, with the router none the wiser.
	if retryable && !wireMismatch(bizErr) {
		router.Fail(group, originalModel, sessionKey, lastFailedChannelId)
	}
	if retryable {
		markChannelPenalty(lastFailedChannelId, bizErr)
	}

	for i := retryTimes; i > 0; i-- {
		var channel *dbmodel.Channel
		var err error
		if sessionKey != "" && router.Enabled() {
			// Sticky failover: pick a different healthy node and re-pin the
			// session to it.
			channel, err = router.ChooseAlternative(group, originalModel, sessionKey, exclude)
		} else {
			channel, err = dbmodel.CacheGetRandomSatisfiedChannelExcluding(group, originalModel, i != retryTimes, exclude)
		}
		if err != nil {
			logger.Errorf(ctx, "choose channel for retry failed: %+v", err)
			break
		}
		logger.Infof(ctx, "using channel #%d to retry (remain times %d)", channel.Id, i)
		if channel.Id == lastFailedChannelId {
			continue
		}
		exclude[channel.Id] = true
		middleware.SetupContextForSelectedChannel(c, channel, originalModel)
		requestBody, err := common.GetRequestBody(c)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
		bizErr = relayHelper(c, relayMode)
		if bizErr == nil {
			return
		}
		channelId := c.GetInt(ctxkey.ChannelId)
		lastFailedChannelId = channelId
		channelName := c.GetString(ctxkey.ChannelName)
		// Only cool the node down (and keep trying other nodes) on retryable
		// errors that indicate a node problem. A non-retryable error (e.g. a
		// client 400) is not the node's fault, so stop retrying.
		if shouldRetry(c, bizErr.StatusCode) || upstreamQuirk400(bizErr) {
			if !wireMismatch(bizErr) {
				router.Fail(group, originalModel, sessionKey, channelId)
			}
			markChannelPenalty(channelId, bizErr)
			go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
			// Back off before the next retry on 429: rate-limit windows are
			// often sub-second, and an immediate re-dispatch to a different
			// channel (or the same one after cooldown expiry) succeeds
			// where an instant retry would just burn another attempt.
			if bizErr.StatusCode == http.StatusTooManyRequests {
				select {
				case <-time.After(backoff429):
				case <-c.Request.Context().Done():
					return
				}
			}
		} else {
			go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
			break
		}
	}
	if bizErr != nil {
		if bizErr.StatusCode == http.StatusTooManyRequests {
			bizErr.Error.Message = describeUpstream429(bizErr.Error.Message)
		}

		// BUG: bizErr is in race condition
		bizErr.Error.Message = helper.MessageWithRequestId(bizErr.Error.Message, requestId)
		c.JSON(bizErr.StatusCode, gin.H{
			"error": bizErr.Error,
		})
	}
}

// describeUpstream429 builds the client-facing message for an upstream 429.
//
// A 429 can mean very different things — short-lived concurrency throttling
// ("retry in a few seconds") or an exhausted quota ("retry in 3 days, or switch
// upstream"). This previously replaced every 429 with a single "上游负载已饱和"
// string, which erased that distinction and made the real cause invisible to
// both users and operators.
//
// The generic hint is kept (it is the right advice for plain throttling) but the
// upstream's own explanation is always appended, so the actual reason survives.
func describeUpstream429(upstream string) string {
	const generic = "当前分组上游负载已饱和，请稍后再试"
	upstream = strings.TrimSpace(upstream)
	if upstream == "" {
		return generic
	}
	return generic + "（上游返回：" + upstream + "）"
}

func shouldRetry(c *gin.Context, statusCode int) bool {
	if _, ok := c.Get(ctxkey.SpecificChannelId); ok {
		return false
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode/100 == 5 {
		return true
	}
	if statusCode == http.StatusBadRequest {
		return false
	}
	if statusCode/100 == 2 {
		return false
	}
	return true
}

// upstreamQuirk400 reports whether a 400 from the upstream is really a
// transient server-side issue rather than the client's fault. Some
// upstreams (volcengine ark) intermittently route Responses-API requests to
// a backend that demands an internal "partial" parameter that is not part
// of the OpenAI spec — the same request succeeds on retry. Without this,
// the relay surfaces a bogus 400 to the client instead of failing over.
func upstreamQuirk400(err *model.ErrorWithStatusCode) bool {
	if err == nil || err.StatusCode != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(err.Error.Message)
	// volc ark: "The request failed because it is missing `partial` parameter"
	return strings.Contains(msg, "missing `partial` parameter")
}

func processChannelRelayError(ctx context.Context, userId int, channelId int, channelName string, err model.ErrorWithStatusCode) {
	logger.Errorf(ctx, "relay error (channel id %d, user id: %d): %s", channelId, userId, err.Message)
	// https://platform.openai.com/docs/guides/error-codes/api-errors
	if monitor.ShouldDisableChannel(&err.Error, err.StatusCode) {
		monitor.DisableChannel(channelId, channelName, err.Message)
	} else {
		monitor.Emit(channelId, false)
	}
}

func RelayNotImplemented(c *gin.Context) {
	err := model.Error{
		Message: "API not implemented",
		Type:    "one_api_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

func RelayNotFound(c *gin.Context) {
	err := model.Error{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}

// wireMismatch reports whether err is a protocol-mismatch refusal
// (responses request on a chat-only channel, or the reverse). These 503s are
// the POOL's fault (mixed wire protocols), not the channel's: the channel
// must not be sticky-cooled for them — with homogeneous pools they never
// fire, and with mixed pools they would unfairly sideline healthy channels.
func wireMismatch(err *model.ErrorWithStatusCode) bool {
	if err == nil {
		return false
	}
	return err.Error.Code == "responses_unsupported_on_channel" ||
		err.Error.Code == "chat_unsupported_on_channel"
}

// ---------------------------------------------------------------------------
// 429 routing penalties
// ---------------------------------------------------------------------------

// quota429Re matches throttles caused by exhausted account quota rather than
// short-lived concurrency limits (volc "exceeded the monthly usage quota",
// kimi "reached your weekly (7-day) usage limit", OpenAI "quota").
var quota429Re = regexp.MustCompile(`(?i)(quota|usage limit|usage quota|billing|exceeded.*limit|reached.*limit)`)

// resetAtRe extracts the upstream's advertised quota reset time, e.g.
// "It will reset at 2026-08-27 23:59:59 +0800 CST" (volc) or an RFC3339 ts.
var resetAtRe = regexp.MustCompile(`reset (?:at|on)\s+([0-9]{4}-[0-9]{2}-[0-9]{2}[ T][0-9]{2}:[0-9]{2}:[0-9]{2}(?:[.,][0-9]+)?(?:\s*[+-][0-9]{4})?(?:\s*[A-Z]{2,5})?)`)

const (
	// quotaCooldownFallback applies when the upstream names no reset time.
	quotaCooldownFallback = 15 * time.Minute
	// quotaCooldownMax caps quota penalties: skipping a channel until a
	// monthly reset on the strength of one 429 is too aggressive — an hourly
	// re-probe costs one rejected request and self-heals the pool.
	quotaCooldownMax = 4 * time.Hour
	// rateLimitCooldown applies to plain (non-quota) 429 throttles.
	rateLimitCooldown = 60 * time.Second
	// 429RetryBackoff is the pause between retry attempts after a 429.
	// Long enough for a rate-limit window to clear, short enough that
	// codex doesn't time out (its own timeout is ~30s).
	backoff429 = 2 * time.Second
)

var penaltyMu sync.Mutex

// markChannelPenalty records a routing cooldown for a channel whose upstream
// returned 429: quota-exhausted channels are skipped until their advertised
// reset (capped at quotaCooldownMax, with a fallback window when no reset
// time is parseable), plain rate limits for a short window. Non-429 errors
// carry no penalty — 5xx cooldowns are the sticky store's business, and
// 4xx's are usually the client's fault.
func markChannelPenalty(channelId int, err *model.ErrorWithStatusCode) {
	if err == nil || err.StatusCode != http.StatusTooManyRequests {
		return
	}
	var until time.Time
	if quota429Re.MatchString(err.Error.Message) {
		until = time.Now().Add(quotaCooldownFallback)
		if m := resetAtRe.FindStringSubmatch(err.Error.Message); m != nil {
			ts := strings.TrimSpace(m[1])
			if parsed, perr := time.Parse("2006-01-02 15:04:05 -0700 MST", ts); perr == nil && parsed.After(time.Now()) {
				until = parsed
			} else if parsed, perr := time.Parse(time.RFC3339, ts); perr == nil && parsed.After(time.Now()) {
				until = parsed
			}
		}
		if d := time.Until(until); d > quotaCooldownMax {
			until = time.Now().Add(quotaCooldownMax)
		}
	} else {
		cooldown := rateLimitCooldown
		if ms := err.Error.RetryAfterMs; ms > 0 {
			cooldown = time.Duration(ms) * time.Millisecond
			if cooldown > quotaCooldownMax {
				cooldown = quotaCooldownMax
			}
		}
		until = time.Now().Add(cooldown)
	}
	penaltyMu.Lock()
	defer penaltyMu.Unlock()
	dbmodel.MarkChannelCooldown(channelId, until)
}
