package middleware

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/routing"
)

// resolveChannelAddressedModel resolves channel-name model addressing:
// "channel-name" serves the channel's configured default_model, and
// "channel-name/model" serves any model from that channel's list. It lets
// clients whose picker only switches models — not providers (e.g. codex
// /model) — pick a specific channel.
//
// Pool routing always wins: a request model the pool can already route is
// NEVER treated as channel addressing, so the feature is purely additive.
// Returns (nil, "", nil) to continue normal pool routing, a non-nil error
// for a malformed address, or the addressed channel with its real model.
func resolveChannelAddressedModel(group, requestModel string) (*model.Channel, string, error) {
	if requestModel == "" || !looksLikeChannelAddress(requestModel) {
		return nil, "", nil
	}
	if model.IsPoolRoutable(group, requestModel) {
		return nil, "", nil
	}
	name, subModel := splitChannelAddress(requestModel)
	ch, err := model.GetChannelByName(name, group)
	if err != nil {
		// No channel of that name in this group: not an address at all.
		return nil, "", nil
	}
	if subModel != "" {
		if !ch.ModelServed(subModel) {
			return nil, "", fmt.Errorf("模型 %q 不在渠道 %q 的模型列表中", subModel, name)
		}
		return ch, resolveThroughMapping(ch, subModel), nil
	}
	cfg, cfgErr := ch.LoadConfig()
	if cfgErr != nil || cfg.DefaultModel == "" {
		// Bare channel name without a default: fall through to pool routing
		// instead of hijacking a potentially real model name.
		return nil, "", nil
	}
	// default_model names an UPSTREAM model directly (e.g. volc-1 →
	// deepseek-v4-flash) — it need not appear in the channel's exposed
	// model list. If it does (and has a mapping entry), resolve through it.
	return ch, resolveThroughMapping(ch, cfg.DefaultModel), nil
}

// resolveThroughMapping maps the addressed model through the channel's own
// model_mapping when it is a listed name with a mapping entry, so
// "channel/listed-model" and default_model=listed-model behave exactly like
// requesting that model through the channel normally would.
func resolveThroughMapping(ch *model.Channel, model string) string {
	if mapping := ch.GetModelMapping(); mapping != nil {
		if mapped, ok := mapping[model]; ok && mapped != "" {
			return mapped
		}
	}
	return model
}

// looksLikeChannelAddress reports whether the model could be channel
// addressing: a bare name or exactly one "name/model" segment pair with
// non-empty parts.
func looksLikeChannelAddress(requestModel string) bool {
	_, sub := splitChannelAddress(requestModel)
	// Bare names only address when they collide with no pool model, which
	// IsPoolRoutable decides; any single-slash shape is a candidate.
	return !strings.Contains(sub, "/")
}

func splitChannelAddress(requestModel string) (name, subModel string) {
	if i := strings.Index(requestModel, "/"); i >= 0 {
		return requestModel[:i], requestModel[i+1:]
	}
	return requestModel, ""
}

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
			// Channel-name model addressing: clients whose picker switches
			// models but not providers (e.g. codex /model) reach one specific
			// channel by requesting "channel-name" (serves the channel's
			// configured default_model) or "channel-name/model" (any model
			// from the channel's list). Names that the pool can already
			// route are never hijacked — addressing is purely additive.
			if ch, realModel, addrErr := resolveChannelAddressedModel(userGroup, requestModel); addrErr != nil {
				abortWithMessage(c, http.StatusBadRequest, addrErr.Error())
				return
			} else if ch != nil {
				c.Set(ctxkey.SpecificChannelId, strconv.Itoa(ch.Id))
				channel = ch
				// Extend the channel's model mapping with the synthetic
				// entry so the existing rewrite + billing machinery maps the
				// channel-name model onto the real upstream model. Picked up
				// by SetupContextForSelectedChannel below.
				override := ch.GetModelMapping()
				if override == nil {
					override = make(map[string]string)
				}
				override[requestModel] = realModel
				c.Set(ctxkey.ModelMappingOverride, override)
			}
			if channel == nil {
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
	mapping := channel.GetModelMapping()
	if mapping == nil {
		mapping = make(map[string]string)
	}
	// Channel-name addressing (see Distribute) deposits an extended mapping
	// here; merge it so the synthetic "channel-name" model rewrites onto the
	// real upstream model through the same machinery as model_mapping.
	if override, ok := c.Get(ctxkey.ModelMappingOverride); ok {
		if extra, ok := override.(map[string]string); ok {
			for k, v := range extra {
				mapping[k] = v
			}
		}
	}
	c.Set(ctxkey.ModelMapping, mapping)
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
