package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/relay/routing"
)

// These endpoints let an API client (e.g. a coding agent such as pi/pix)
// inspect and change the upstream node its own session is pinned to. They are
// token-authenticated rather than admin-authenticated, and every operation is
// scoped to the caller's own session key, so a client can only ever move its
// own session.

// clientRoutingRequest is the shared request shape. The session is identified
// exactly like a relay request identifies it, so the agent does not need to know
// how one-api derives session identity.
type clientRoutingRequest struct {
	Model string `json:"model"`
	// Channel is a channel id or channel name. Used by the pin endpoint.
	Channel string `json:"channel"`
}

// clientRouting is the resolved identity of a client routing request.
type clientRouting struct {
	Group   string
	Model   string
	Session string
	Channel string
}

// resolveClientRouting extracts the (group, model, session) triple that
// identifies the caller's sticky binding, plus the requested channel.
//
// The request body is read exactly once here. gin's ShouldBindJSON consumes
// c.Request.Body, so parsing it a second time in a handler would silently see
// an empty body and drop fields such as `channel`.
func resolveClientRouting(c *gin.Context) (clientRouting, error) {
	return resolveClientRoutingWithSession(c, false)
}

// resolveClientRoutingWithSession parses and validates the request, then
// resolves the caller's group.
//
// Validation runs first and in cheapest-first order: a request that is missing
// `model`, names a model the token may not use, or (when requireSession) carries
// no session id is rejected without touching the cache or database.
func resolveClientRoutingWithSession(c *gin.Context, requireSession bool) (clientRouting, error) {
	out, err := parseClientRoutingRequest(c)
	if err != nil {
		return out, err
	}
	if requireSession && out.Session == "" {
		return out, errMissingSession()
	}
	group, err := model.CacheGetUserGroup(c.GetInt(ctxkey.Id))
	if err != nil {
		return out, err
	}
	out.Group = group
	return out, nil
}

// errMissingSession explains which header the client must send. Pin/cycle/unpin
// are session-scoped, so without a session there is nothing to move.
func errMissingSession() error {
	return errors.New("缺少会话标识，请通过 " + sessionHeaderName() + " 头指定")
}

// parseClientRoutingRequest reads and validates everything that comes straight
// off the request, with no cache or database access.
//
// The body is read exactly once here. gin's ShouldBindJSON consumes
// c.Request.Body, so parsing it a second time in a handler would silently see an
// empty body and drop fields such as `channel`.
func parseClientRoutingRequest(c *gin.Context) (clientRouting, error) {
	var out clientRouting
	var req clientRoutingRequest
	// The body is optional (GET listing uses query params); ignore decode errors.
	_ = c.ShouldBindJSON(&req)

	out.Model = strings.TrimSpace(req.Model)
	if out.Model == "" {
		out.Model = strings.TrimSpace(c.Query("model"))
	}
	if out.Model == "" {
		return out, errors.New("model is required")
	}
	if avail := c.GetString(ctxkey.AvailableModels); avail != "" && !tokenAllowsModel(out.Model, avail) {
		return out, errors.New("该令牌无权使用模型：" + out.Model)
	}

	out.Channel = strings.TrimSpace(req.Channel)
	if out.Channel == "" {
		out.Channel = strings.TrimSpace(c.Query("channel"))
	}

	out.Session = strings.TrimSpace(c.GetHeader(sessionHeaderName()))
	if out.Session == "" {
		out.Session = strings.TrimSpace(c.Query("session"))
	}
	return out, nil
}

// tokenAllowsModel mirrors the token model allowlist check performed by
// middleware.TokenAuth, so a client cannot inspect or move routing for a model
// its token may not use.
func tokenAllowsModel(modelName, models string) bool {
	for _, m := range strings.Split(models, ",") {
		if modelName == m {
			return true
		}
	}
	return false
}

func sessionHeaderName() string {
	if name := strings.TrimSpace(config.SessionIdHeader); name != "" {
		return name
	}
	return "X-Session-Id"
}

// GetClientRoutingNodes lists the upstream nodes able to serve the caller's
// model, marking which one the caller's session is currently pinned to.
//
// GET /v1/oneapi/routing/nodes?model=coding_medium
func GetClientRoutingNodes(c *gin.Context) {
	rc, err := resolveClientRouting(c)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	router := routing.DefaultRouter()
	nodes := router.Nodes(rc.Group, rc.Model, rc.Session)
	if len(nodes) == 0 {
		clientRoutingError(c, http.StatusServiceUnavailable,
			"当前分组 "+rc.Group+" 下对于模型 "+rc.Model+" 无可用渠道")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"model":          rc.Model,
			"group":          rc.Group,
			"session":        rc.Session,
			"sticky_enabled": router.StickyAppliesTo(rc.Model, rc.Session),
			"session_header": sessionHeaderName(),
			"nodes":          nodes,
		},
	})
}

// PinClientRoutingNode pins the caller's session to a specific node.
//
// POST /v1/oneapi/routing/pin  {"model":"coding_medium","channel":"minimax"}
func PinClientRoutingNode(c *gin.Context) {
	rc, err := resolveClientRoutingWithSession(c, true)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	if rc.Channel == "" {
		clientRoutingError(c, http.StatusBadRequest, "channel is required")
		return
	}
	router := routing.DefaultRouter()
	channelId, err := router.ParseChannelId(rc.Group, rc.Model, rc.Channel)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	ch, err := router.Pin(rc.Group, rc.Model, rc.Session, channelId)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	respondWithPinnedNode(c, router, rc.Group, rc.Model, rc.Session, ch.Id, ch.Name)
}

// CycleClientRoutingNode rotates the caller's session to the next eligible node.
//
// POST /v1/oneapi/routing/cycle  {"model":"coding_medium"}
func CycleClientRoutingNode(c *gin.Context) {
	rc, err := resolveClientRoutingWithSession(c, true)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	router := routing.DefaultRouter()
	ch, err := router.Next(rc.Group, rc.Model, rc.Session)
	if err != nil {
		clientRoutingError(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	respondWithPinnedNode(c, router, rc.Group, rc.Model, rc.Session, ch.Id, ch.Name)
}

// UnpinClientRoutingNode drops the caller's binding so routing goes back to
// automatic selection.
//
// POST /v1/oneapi/routing/unpin  {"model":"coding_medium"}
func UnpinClientRoutingNode(c *gin.Context) {
	rc, err := resolveClientRoutingWithSession(c, true)
	if err != nil {
		clientRoutingError(c, http.StatusBadRequest, err.Error())
		return
	}
	removed := routing.DefaultRouter().Store().ForgetSession(rc.Session)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "已解除会话绑定，后续请求将自动选择渠道",
		"data":    gin.H{"removed": removed},
	})
}

func respondWithPinnedNode(c *gin.Context, router *routing.Router, group, requestModel, session string, channelId int, channelName string) {
	var upstream string
	for _, n := range router.Nodes(group, requestModel, session) {
		if n.ChannelId == channelId {
			upstream = n.UpstreamModel
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"channel_id":     channelId,
			"channel":        channelName,
			"upstream_model": upstream,
			"model":          requestModel,
			"sticky_enabled": router.StickyAppliesTo(requestModel, session),
		},
	})
}

// clientRoutingError replies in the OpenAI error shape, since these routes live
// under /v1 and clients already parse that format.
func clientRoutingError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
		"error": gin.H{
			"message": message,
			"type":    "one_api_error",
		},
	})
}
