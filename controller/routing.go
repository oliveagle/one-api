package controller

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/routing"
)

// busynessScore computes a composite score for a channel. Higher = busier =
// shown first. Disabled and quota-exhausted channels are sunk to the bottom;
// rate-limited channels are penalised but less severely; healthy channels are
// sorted by active sessions, cumulative requests, configured priority, and
// response speed.
func busynessScore(ch routing.ChannelState) float64 {
	switch {
	case ch.Status == model.ChannelStatusManuallyDisabled:
		return -20000
	case ch.Status == model.ChannelStatusAutoDisabled:
		return -15000
	}

	score := 0.0

	// Quota exhausted (balance tracked and <= 0).
	if ch.Balance <= 0 && ch.Balance != 0 {
		score -= 5000
	}

	// Rate-limited (cooling down).
	if !ch.CoolingUntil.IsZero() && time.Now().Before(ch.CoolingUntil) {
		score -= 2000
	}

	// Active sessions (primary load indicator).
	score += float64(ch.Sessions) * 100

	// Cumulative requests (historical activity).
	score += float64(ch.Requests) * 0.1

	// Configured priority weight.
	score += float64(ch.Priority) * 10

	// Response speed bonus: faster channel gets higher bonus.
	if ch.ResponseTime > 0 && ch.ResponseTime < 10000 {
		score += float64(10000-ch.ResponseTime) / 10
	}

	return score
}

// GetRoutingStatus returns the realtime session-sticky routing status: whether
// the feature is enabled, the participating models, and the live session
// registry + per-channel health (active session count and cooldown state),
// sorted by busyness score descending.
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

	// Collect unique channel ids from both channels and sessions.
	seen := make(map[int]bool)
	for _, ch := range channels {
		seen[ch.ChannelId] = true
	}
	for _, s := range sessions {
		seen[s.ChannelId] = true
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	// Build a lookup: channelId -> DB channel.
	dbChannels := make(map[int]*model.Channel, len(ids))
	for _, id := range ids {
		ch, err := model.GetChannelById(id, false)
		if err == nil {
			dbChannels[id] = ch
		}
	}

	// Enrich each ChannelState with DB fields and compute busyness score.
	for i := range channels {
		ch := &channels[i]
		if dbc, ok := dbChannels[ch.ChannelId]; ok {
			ch.Name = dbc.Name
			ch.ResponseTime = dbc.ResponseTime
			ch.Balance = dbc.Balance
			ch.Status = dbc.Status
			if dbc.Priority != nil {
				ch.Priority = *dbc.Priority
			}
		}
		ch.Busyness = busynessScore(*ch)
	}

	// Sort by busyness descending (busiest first).
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Busyness > channels[j].Busyness
	})

	// Build channel_names for the sessions table.
	channelNames := make(map[int]string, len(dbChannels))
	for id, dbc := range dbChannels {
		channelNames[id] = dbc.Name
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
			"failure_threshold":     config.StickyFailureThreshold,
			"channel_names":         channelNames,
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
