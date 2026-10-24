package controller

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

// defaultSystemOneModel is the model name every soft-decider instance serves by
// default (and the name callers may omit). Routing still works when the caller
// passes an explicit "model" in the JSON body.
const defaultSystemOneModel = "winwinwinbb/soft-decider-421m"

// RelaySystemOne is a transparent passthrough for Laya's System-1 API
// (`POST /v1/systemone`). It is deliberately *not* on the OpenAI relay path:
// the request/response schema is Laya's own, so there is nothing to convert.
// The channel is chosen by the body's "model" (default: soft-decider-421m) and
// the caller's group, which lets several GPU channels serve one model name and
// share the load.
func RelaySystemOne(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "failed to read request body: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	var payload map[string]interface{}
	if len(body) > 0 {
		if jerr := json.Unmarshal(body, &payload); jerr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "request body is not valid JSON: " + jerr.Error(), "type": "invalid_request_error"}})
			return
		}
	}
	modelName := defaultSystemOneModel
	if m, ok := payload["model"].(string); ok && m != "" {
		modelName = m
	}

	group := c.GetString(ctxkey.Group)
	if group == "" {
		if g, gerr := model.GetUserGroup(c.GetInt(ctxkey.Id)); gerr == nil && g != "" {
			group = g
		} else {
			group = "default"
		}
	}

	channel, cerr := model.CacheGetRandomSatisfiedChannel(group, modelName, false)
	if cerr != nil || channel == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"message": "no available SystemOne channel for model " + modelName + " in group " + group,
			"type":    "upstream_error",
		}})
		return
	}

	base := ""
	if channel.BaseURL != nil {
		base = *channel.BaseURL
	}
	url := strings.TrimSuffix(base, "/") + "/v1/systemone"
	req, rerr := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(body))
	if rerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": rerr.Error(), "type": "server_error"}})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if channel.Key != "" {
		req.Header.Set("Authorization", "Bearer "+channel.Key)
	}

	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"message": "SystemOne upstream failed: " + derr.Error(),
			"type":    "upstream_error",
		}})
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	c.Writer.Header().Set("Content-Type", contentType)
	c.Writer.Header().Set("X-OneAPI-Channel-Id", strconv.Itoa(channel.Id))
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(c.Writer, resp.Body)
}
