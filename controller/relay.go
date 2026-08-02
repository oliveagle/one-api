package controller

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

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
	retryable := shouldRetry(c, bizErr.StatusCode)
	retryTimes := config.RetryTimes
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
	if retryable {
		router.Fail(group, originalModel, sessionKey, lastFailedChannelId)
	}

	for i := retryTimes; i > 0; i-- {
		var channel *dbmodel.Channel
		var err error
		if sessionKey != "" && router.Enabled() {
			// Sticky failover: pick a different healthy node and re-pin the
			// session to it.
			channel, err = router.ChooseAlternative(group, originalModel, sessionKey, exclude)
		} else {
			channel, err = dbmodel.CacheGetRandomSatisfiedChannel(group, originalModel, i != retryTimes)
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
		if shouldRetry(c, bizErr.StatusCode) {
			router.Fail(group, originalModel, sessionKey, channelId)
			go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
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
