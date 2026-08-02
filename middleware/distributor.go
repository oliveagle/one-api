package middleware

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/routing"
)

type ModelRequest struct {
	Model string `json:"model" form:"model"`
}

func Distribute() func(c *gin.Context) {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		userId := c.GetInt(ctxkey.Id)
		userGroup, _ := model.CacheGetUserGroup(userId)
		c.Set(ctxkey.Group, userGroup)
		var requestModel string
		var channel *model.Channel
		channelId, ok := c.Get(ctxkey.SpecificChannelId)
		if ok {
			id, err := strconv.Atoi(channelId.(string))
			if err != nil {
				abortWithMessage(c, http.StatusBadRequest, "无效的渠道 Id")
				return
			}
			channel, err = model.GetChannelById(id, true)
			if err != nil {
				abortWithMessage(c, http.StatusBadRequest, "无效的渠道 Id")
				return
			}
			if channel.Status != model.ChannelStatusEnabled {
				abortWithMessage(c, http.StatusForbidden, "该渠道已被禁用")
				return
			}
		} else {
			requestModel = c.GetString(ctxkey.RequestModel)
			// Derive the agent session identity and use session-sticky routing
			// so the session sticks to one upstream node, with failover to
			// other nodes handled by the relay retry loop.
			//
			// ResolveSession tries an explicit session id (header, then body)
			// and finally a conversation fingerprint, which is what makes this
			// work for agents like pi/pix that send no session id at all.
			sessionKey, sessionSource := routing.ResolveSession(c)
			if sessionKey == "" && config.StickyFallbackToToken {
				// Last resort: pin per API token. Coarser than per-session, so
				// it is opt-in.
				if tokenId := c.GetInt(ctxkey.TokenId); tokenId > 0 {
					sessionKey = fmt.Sprintf("token:%d", tokenId)
					sessionSource = "token"
				}
			}
			// The retry loop in controller/relay.go reads this to fail over
			// within the same session, so it must be set for *every* derived
			// key, not just header/body ones.
			if sessionKey != "" {
				c.Set(ctxkey.SessionKey, sessionKey)
				c.Set(ctxkey.SessionSource, sessionSource)
			}
			// For GET/DELETE requests (e.g., Responses API CRUD endpoints),
			// there's no request body, so requestModel may be empty.
			// In this case, we still try to find a channel - it will be up to
			// the upstream to handle the request or return an error.
			var err error
			channel, err = routing.DefaultRouter().Choose(userGroup, requestModel, sessionKey)
			if err != nil {
				message := fmt.Sprintf("当前分组 %s 下对于模型 %s 无可用渠道", userGroup, requestModel)
				if channel != nil {
					logger.SysError(fmt.Sprintf("渠道不存在：%d", channel.Id))
					message = "数据库一致性已被破坏，请联系管理员"
				}
				abortWithMessage(c, http.StatusServiceUnavailable, message)
				return
			}
		}
		sessionKey := c.GetString(ctxkey.SessionKey)
		logger.Debugf(ctx, "user id %d, user group: %s, request model: %s, using channel #%d, session %q (source=%s, sticky=%t)",
			userId, userGroup, requestModel, channel.Id, sessionKey,
			c.GetString(ctxkey.SessionSource), routing.DefaultRouter().StickyAppliesTo(requestModel, sessionKey))
		SetupContextForSelectedChannel(c, channel, requestModel)
		c.Next()
	}
}

func SetupContextForSelectedChannel(c *gin.Context, channel *model.Channel, modelName string) {
	c.Set(ctxkey.Channel, channel.Type)
	c.Set(ctxkey.ChannelId, channel.Id)
	c.Set(ctxkey.ChannelName, channel.Name)
	if channel.SystemPrompt != nil && *channel.SystemPrompt != "" {
		c.Set(ctxkey.SystemPrompt, *channel.SystemPrompt)
	}
	c.Set(ctxkey.ModelMapping, channel.GetModelMapping())
	c.Set(ctxkey.Headers, channel.GetHeaders())
	c.Set(ctxkey.OriginalModel, modelName) // for retry
	c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))
	c.Set(ctxkey.BaseURL, channel.GetBaseURL())
	cfg, _ := channel.LoadConfig()
	// this is for backward compatibility
	if channel.Other != nil {
		switch channel.Type {
		case channeltype.Azure:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.Xunfei:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.Gemini:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.AIProxyLibrary:
			if cfg.LibraryID == "" {
				cfg.LibraryID = *channel.Other
			}
		case channeltype.Ali:
			if cfg.Plugin == "" {
				cfg.Plugin = *channel.Other
			}
		}
	}
	c.Set(ctxkey.Config, cfg)
}
