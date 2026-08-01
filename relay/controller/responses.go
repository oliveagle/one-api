// Package controller is a package for handling the relay controller
package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/billing"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// ResponsesRequest is the subset of the OpenAI Responses API request that the
// relay needs in order to route and bill. Everything else is forwarded
// untouched, so stateful fields (previous_response_id, store) and server-side
// tools keep working. See docs/adr/0001-openai-responses-api-passthrough.md.
type ResponsesRequest struct {
	Model        string `json:"model"`
	Stream       bool   `json:"stream,omitempty"`
	Input        any    `json:"input,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	MaxOutput    int    `json:"max_output_tokens,omitempty"`
}

// ResponsesUsage mirrors the Responses API usage block. The field names differ
// from ChatCompletions: input_tokens / output_tokens rather than
// prompt_tokens / completion_tokens.
type ResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// ToUsage maps upstream Responses accounting onto the internal representation
// so the existing billing path can consume it unchanged.
func (u ResponsesUsage) ToUsage() *relaymodel.Usage {
	return &relaymodel.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// responsesEnvelope is the non-streaming response shape, reduced to the usage
// block. The body itself is streamed back verbatim, so nothing else is decoded.
type responsesEnvelope struct {
	Usage *ResponsesUsage `json:"usage"`
}

// estimateResponsesPromptTokens approximates the prompt size for pre-consumption.
// The authoritative count comes from the upstream usage block afterwards; this
// only has to be close enough to guard the quota.
func estimateResponsesPromptTokens(request *ResponsesRequest) int {
	tokens := 0
	if request.Input != nil {
		tokens += openai.CountTokenInput(request.Input, request.Model)
	}
	if request.Instructions != "" {
		tokens += openai.CountTokenText(request.Instructions, request.Model)
	}
	return tokens
}

// maxResponsesStreamLine bounds a single SSE line. Responses frames can embed
// large tool payloads, so the default 64KB scanner limit is too small.
const maxResponsesStreamLine = 1024 * 1024

// usageFromSSELine returns the usage block if this SSE data line carries one.
// Usage appears on terminal events (response.completed) and may be nested under
// "response", so both shapes are checked. Non-usage lines return nil.
func usageFromSSELine(line string) *ResponsesUsage {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}
	var event struct {
		Usage    *ResponsesUsage `json:"usage"`
		Response *struct {
			Usage *ResponsesUsage `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return nil
	}
	if event.Usage != nil && event.Usage.TotalTokens > 0 {
		return event.Usage
	}
	if event.Response != nil && event.Response.Usage != nil && event.Response.Usage.TotalTokens > 0 {
		return event.Response.Usage
	}
	return nil
}

// RelayResponsesHelper dispatches Responses API requests to the appropriate
// handler based on HTTP method and path. POST /v1/responses creates a new
// response (with billing), while GET/DELETE/cancel/input_items are passthrough
// operations without billing.
func RelayResponsesHelper(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	path := c.Request.URL.Path

	// POST /responses/:id/cancel - Cancel an in-progress response
	if c.Request.Method == http.MethodPost && strings.HasSuffix(path, "/cancel") {
		return relayResponsesCancel(c)
	}

	// GET /responses/:id/input_items - List input items of a response
	if c.Request.Method == http.MethodGet && strings.HasSuffix(path, "/input_items") {
		return relayResponsesInputItems(c)
	}

	// POST /responses - Create a new response (with billing)
	if c.Request.Method == http.MethodPost {
		return relayResponsesCreate(c)
	}

	// GET /responses/:id - Retrieve a response
	if c.Request.Method == http.MethodGet {
		return relayResponsesGet(c)
	}

	// DELETE /responses/:id - Delete a response
	if c.Request.Method == http.MethodDelete {
		return relayResponsesDelete(c)
	}

	return openai.ErrorWrapper(fmt.Errorf("unsupported method for Responses API"), "unsupported_method", http.StatusMethodNotAllowed)
}

// relayResponsesCreate relays POST /v1/responses as a passthrough. The request
// body is forwarded byte-for-byte and the response is returned as-is; only
// `model` and `stream` are inspected, for routing and billing.
func relayResponsesCreate(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)

	var request ResponsesRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		return openai.ErrorWrapper(err, "invalid_responses_request", http.StatusBadRequest)
	}
	// `model` is optional in the Responses spec (only Authorization is required),
	// but one-api needs it to pick a channel: CacheGetRandomSatisfiedChannel keys
	// on group+model. Requests without it are already rejected upstream of this
	// helper by middleware.Distribute with 503 "no channel available", the same
	// way /v1/chat/completions behaves, so no check is duplicated here.
	meta.IsStream = request.Stream

	// Map the model name the same way the text path does, then rewrite it in the
	// forwarded body so the upstream receives the mapped name.
	meta.OriginModelName = request.Model
	mappedModel, _ := getMappedModelName(request.Model, meta.ModelMapping)
	request.Model = mappedModel
	meta.ActualModelName = mappedModel

	modelRatio := billingratio.GetModelRatio(request.Model, meta.ChannelType)
	groupRatio := billingratio.GetGroupRatio(meta.Group)
	ratio := modelRatio * groupRatio

	promptTokens := estimateResponsesPromptTokens(&request)
	meta.PromptTokens = promptTokens

	// postConsumeQuota only reads .Model off this struct, so a minimal value is
	// enough to reuse the existing billing path.
	billingRequest := &relaymodel.GeneralOpenAIRequest{
		Model:     request.Model,
		MaxTokens: request.MaxOutput,
	}
	preConsumedQuota, bizErr := preConsumeQuota(ctx, billingRequest, promptTokens, ratio, meta)
	if bizErr != nil {
		logger.Warnf(ctx, "preConsumeQuota failed: %+v", *bizErr)
		return bizErr
	}

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	requestBody, err := getResponsesRequestBody(c, &request)
	if err != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(err, "convert_request_failed", http.StatusInternalServerError)
	}

	resp, err := adaptor.DoRequest(c, meta, requestBody)
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return RelayErrorHandler(resp)
	}

	usage, respErr := relayResponsesResponse(c, resp)
	if respErr != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return respErr
	}

	go postConsumeQuota(ctx, usage, meta, billingRequest, ratio, preConsumedQuota, modelRatio, groupRatio, false)
	return nil
}

// relayResponsesGet relays GET /v1/responses/:response_id as a passthrough.
// No billing is applied since this is a retrieval operation.
func relayResponsesGet(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayResponsesPassthrough(c)
}

// relayResponsesDelete relays DELETE /v1/responses/:response_id as a passthrough.
// No billing is applied since this is a deletion operation.
func relayResponsesDelete(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayResponsesPassthrough(c)
}

// relayResponsesCancel relays POST /v1/responses/:response_id/cancel as a passthrough.
// No billing is applied since this cancels an in-progress response.
func relayResponsesCancel(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayResponsesPassthrough(c)
}

// relayResponsesInputItems relays GET /v1/responses/:response_id/input_items as a passthrough.
// No billing is applied since this is a retrieval operation.
func relayResponsesInputItems(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	return relayResponsesPassthrough(c)
}

// relayResponsesPassthrough is a generic handler for Responses API CRUD operations
// that don't require billing (GET, DELETE, cancel, input_items). It forwards the
// request to the upstream and returns the response verbatim.
func relayResponsesPassthrough(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)

	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// GET/DELETE requests have no body; POST /cancel may have an empty body
	var requestBody io.Reader
	if c.Request.Method == http.MethodPost {
		// For POST /cancel, forward any body as-is
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return openai.ErrorWrapper(err, "read_request_body_failed", http.StatusInternalServerError)
		}
		if len(body) > 0 {
			requestBody = bytes.NewReader(body)
		}
	}

	resp, err := adaptor.DoRequest(c, meta, requestBody)
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}

	return forwardResponse(c, resp)
}

// forwardResponse copies the upstream response to the client verbatim.
func forwardResponse(c *gin.Context, resp *http.Response) *relaymodel.ErrorWithStatusCode {
	if resp == nil {
		return openai.ErrorWrapper(fmt.Errorf("nil response from upstream"), "nil_response", http.StatusInternalServerError)
	}
	defer resp.Body.Close()

	// Copy all response headers
	for key, values := range resp.Header {
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}

	// Write status code
	c.Writer.WriteHeader(resp.StatusCode)

	// Copy body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return openai.ErrorWrapper(err, "read_response_failed", http.StatusInternalServerError)
	}
	if _, err := c.Writer.Write(body); err != nil {
		logger.Errorf(c.Request.Context(), "write response failed: %s", err.Error())
	}

	return nil
}

// getResponsesRequestBody re-encodes the body only when the model name was
// rewritten by model_mapping; otherwise the original bytes are forwarded so no
// field can be lost in a round-trip through our partial struct.
func getResponsesRequestBody(c *gin.Context, request *ResponsesRequest) (io.Reader, error) {
	original, err := common.GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}
	current, _ := json.Marshal(request.Model)
	if existing, ok := raw["model"]; ok && bytes.Equal(existing, current) {
		return bytes.NewReader(original), nil
	}
	raw["model"] = current
	rewritten, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(rewritten), nil
}

// relayResponsesResponse copies the upstream response to the client verbatim and
// extracts the usage block for billing.
func relayResponsesResponse(c *gin.Context, resp *http.Response) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return relayResponsesStream(c, resp)
	}
	return relayResponsesNonStream(c, resp)
}

// relayResponsesStream forwards the SSE stream line by line, flushing as it
// goes so the client sees no added latency, while scanning for the usage block.
//
// The raw lines are written through rather than via render.StringData because
// the Responses protocol carries semantic "event:" lines alongside "data:";
// StringData only emits data frames and would silently drop the event names,
// breaking clients that dispatch on them.
func relayResponsesStream(c *gin.Context, resp *http.Response) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	defer resp.Body.Close()

	common.SetEventStreamHeaders(c)

	var usage *ResponsesUsage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxResponsesStreamLine)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		line := scanner.Text()
		// Preserve the frame exactly, including blank separator lines.
		if _, err := fmt.Fprintf(c.Writer, "%s\n", line); err != nil {
			logger.Errorf(c.Request.Context(), "write stream line failed: %s", err.Error())
			break
		}
		c.Writer.Flush()

		if found := usageFromSSELine(line); found != nil {
			usage = found
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Errorf(c.Request.Context(), "error reading Responses stream: %s", err.Error())
	}

	if usage == nil {
		logger.Warnf(c.Request.Context(), "no usage in Responses stream; billing keeps the pre-consumed estimate")
		return &relaymodel.Usage{}, nil
	}
	return usage.ToUsage(), nil
}

// relayResponsesNonStream returns the body untouched and decodes only usage.
func relayResponsesNonStream(c *gin.Context, resp *http.Response) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "read_response_body_failed", http.StatusInternalServerError)
	}

	for key, values := range resp.Header {
		for _, value := range values {
			c.Writer.Header().Set(key, value)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	if _, err := c.Writer.Write(body); err != nil {
		logger.Errorf(c.Request.Context(), "write response failed: %s", err.Error())
	}

	var envelope responsesEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Usage == nil {
		logger.Warnf(c.Request.Context(), "no usage in Responses reply; billing keeps the pre-consumed estimate")
		return &relaymodel.Usage{}, nil
	}
	return envelope.Usage.ToUsage(), nil
}
