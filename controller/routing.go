package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/relay/routing"
)

// GetRoutingStatus returns the realtime session-sticky routing status: whether
// the feature is enabled, the participating models, and the live session
// registry + per-channel health (active session count and cooldown state).
func GetRoutingStatus(c *gin.Context) {
	router := routing.DefaultRouter()
	sessions := router.Store().Snapshot()
	channels := router.Store().ChannelStates()

	var models []string
	for _, m := range strings.Split(config.StickyModels, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		models = []string{"*"}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"enabled":               router.Enabled(),
			"sticky_enabled":        config.StickyRoutingEnabled,
			"memory_cache_enabled":  config.MemoryCacheEnabled,
			"models":                models,
			"cooldown_seconds":      config.StickyCooldownSeconds,
			"session_ttl_seconds":   config.StickySessionTTLSeconds,
			"session_id_header":     config.SessionIdHeader,
			"session_id_body_field": config.SessionIdBodyField,
			"fingerprint_enabled":   config.SessionFingerprintEnabled,
			"token_fallback":        config.StickyFallbackToToken,
			"sessions":              sessions,
			"channels":              channels,
		},
	})
}

// DeleteRoutingSession unbinds a specific session (identified by session_key),
// so the next request from that session is re-pinned to a fresh node.
func DeleteRoutingSession(c *gin.Context) {
	var req struct {
		SessionKey string `json:"session_key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.SessionKey) == "" {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "无效的 session_key",
		})
		return
	}
	removed := routing.DefaultRouter().Store().ForgetSession(strings.TrimSpace(req.SessionKey))
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"removed": removed,
		},
	})
}

// ClearRoutingSessions unbinds every sticky session, resetting the live
// session registry and channel cooldowns.
func ClearRoutingSessions(c *gin.Context) {
	routing.DefaultRouter().Store().Clear()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "已清空所有 routing 会话",
	})
}
