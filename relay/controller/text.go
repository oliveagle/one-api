package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/billing"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
)

// PostConsumeQuotaSynchronous, when true, makes RelayTextHelper run
// postConsumeQuota synchronously instead of in a detached goroutine.
// Production leaves it false (the quota settle is not on the request's
// critical path). Tests that drive the full relay pipeline flip it to
// true so the goroutine cannot outlive the test and race the global
// state (common.RedisEnabled, batchUpdateStores, model.DB) that the
// next test's setup mutates — see controller/relay_mock_integration_test.go.
var PostConsumeQuotaSynchronous bool

func RelayTextHelper(c *gin.Context) *model.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)
	// get & validate textRequest
	textRequest, requestModified, err := getAndValidateTextRequest(c, meta.Mode)
	if err != nil {
		logger.Errorf(ctx, "getAndValidateTextRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "invalid_text_request", http.StatusBadRequest)
	}
	meta.IsStream = textRequest.Stream

	// map model name
	meta.OriginModelName = textRequest.Model
	textRequest.Model, _ = getMappedModelName(textRequest.Model, meta.ModelMapping)
	meta.ActualModelName = textRequest.Model
	// set system prompt if not empty
	systemPromptReset := setSystemPrompt(ctx, textRequest, meta.ForcedSystemPrompt)
	// get model ratio & group ratio
	modelRatio := billingratio.GetModelRatio(textRequest.Model, meta.ChannelType)
	groupRatio := billingratio.GetGroupRatio(meta.Group)
	ratio := modelRatio * groupRatio
	// pre-consume quota
	promptTokens := getPromptTokens(textRequest, meta.Mode)
	meta.PromptTokens = promptTokens
	preConsumedQuota, bizErr := preConsumeQuota(ctx, textRequest, promptTokens, ratio, meta)
	if bizErr != nil {
		logger.Warnf(ctx, "preConsumeQuota failed: %+v", *bizErr)
		return bizErr
	}

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// get request body
	requestBody, err := getRequestBody(c, meta, textRequest, adaptor, requestModified)
	if err != nil {
		return openai.ErrorWrapper(err, "convert_request_failed", http.StatusInternalServerError)
	}

	// do request
	resp, err := adaptor.DoRequest(c, meta, requestBody)
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return RelayErrorHandler(resp)
	}

	// set routing headers before response so client can see which channel/model was used
	channelName := c.GetString(ctxkey.ChannelName)
	if channelName != "" {
		c.Writer.Header().Set("X-Upstream-AI-Channel", channelName)
	}
	if meta.ActualModelName != "" {
		c.Writer.Header().Set("X-Upstream-AI-Model", meta.ActualModelName)
	}

	// do response
	usage, respErr := adaptor.DoResponse(c, resp, meta)
	if respErr != nil {
		logger.Errorf(ctx, "respErr is not nil: %+v", *respErr)
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return respErr
	}
	// post-consume quota
	if PostConsumeQuotaSynchronous {
		postConsumeQuota(ctx, usage, meta, textRequest, ratio, preConsumedQuota, modelRatio, groupRatio, systemPromptReset)
	} else {
		go postConsumeQuota(ctx, usage, meta, textRequest, ratio, preConsumedQuota, modelRatio, groupRatio, systemPromptReset)
	}
	return nil
}

func getRequestBody(c *gin.Context, meta *meta.Meta, textRequest *model.GeneralOpenAIRequest, adaptor adaptor.Adaptor, requestModified bool) (io.Reader, error) {
	// Normalize tool_choice to a value strict upstreams (Console Go / opencode-go,
	// codex API) accept. Those upstreams only accept the strings "none", "auto",
	// "required" or an OpenAI tool choice object, and reject anything else with:
	//   tool_choice: expected one of `none`, `auto`, `required` or a tool choice object
	// codex-cli sends the non-standard "any" (and may send other non-standard
	// strings); map any such value to "required" so the request is accepted.
	needConvert := false
	if tc, ok := textRequest.ToolChoice.(string); ok {
		switch tc {
		case "none", "auto", "required":
			// already a valid value, leave untouched
		default:
			textRequest.ToolChoice = "required"
			needConvert = true
		}
	}

	// If the request body was modified in-memory (e.g. orphaned tool calls repaired,
	// tool call arguments normalized), we must re-serialize from the struct instead
	// of passing through the original raw body.
	if requestModified {
		needConvert = true
	}

	if !needConvert && !config.EnforceIncludeUsage &&
		meta.APIType == apitype.OpenAI &&
		meta.OriginModelName == meta.ActualModelName &&
		meta.ChannelType != channeltype.Baichuan &&
		meta.ForcedSystemPrompt == "" {
		// no need to convert request for openai
		return c.Request.Body, nil
	}

	// get request body
	var requestBody io.Reader
	convertedRequest, err := adaptor.ConvertRequest(c, meta.Mode, textRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request failed: %s\n", err.Error())
		return nil, err
	}
	jsonData, err := json.Marshal(convertedRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request json_marshal_failed: %s\n", err.Error())
		return nil, err
	}
	logger.Debugf(c.Request.Context(), "converted request: \n%s", string(jsonData))
	requestBody = bytes.NewBuffer(jsonData)
	return requestBody, nil
}
