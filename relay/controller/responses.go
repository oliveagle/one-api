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
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/relay"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/billing"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
	"github.com/songquanpeng/one-api/relay/channeltype"
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
	// Tools carries the raw tools array from the Responses API request. The
	// schema mirrors OpenAI ChatCompletions (type=function, function={name,...})
	// so the same struct can be re-emitted as-is when forwarding to a chat
	// upstream. Anything exotic (mcp_tool, etc.) round-trips as RawMessage
	// and the upstream is the one that has to interpret it.
	Tools []relaymodel.Tool `json:"tools,omitempty"`
	// ToolChoice is the same shape as ChatCompletions: "auto" / "none" /
	// "required" or an object like {"type":"function","function":{"name":"f"}}.
	// We keep it as raw JSON so anything the Responses API ever adds is
	// preserved.
	ToolChoice any `json:"tool_choice,omitempty"`
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

// upstreamSupportsResponses reports whether the selected channel's upstream
// natively implements the Responses API, in which case the request is passed
// through untouched. Channels that do not (e.g. opencode-go) get an automatic
// Responses -> Chat Completions conversion.
//
// A channel opts in to passthrough via the `support_responses` channel config
// flag. The AIHubMix channel type is additionally treated as native because
// the passthrough path was originally verified against it (see
// docs/adr/0001-openai-responses-api-passthrough.md); everything else defaults
// to conversion.
func upstreamSupportsResponses(meta *meta.Meta) bool {
	if meta == nil {
		return false
	}
	if meta.Config.SupportResponses {
		return true
	}
	return meta.ChannelType == channeltype.AIHubMix
}

// relayResponsesConvertToChat converts a Responses API request into a Chat
// Completions request and delegates to the existing chat pipeline
// (RelayTextHelper), which handles model mapping, billing, streaming and
// response forwarding. The converted body is injected into the request context
// so RelayTextHelper reads it as if the client had called /v1/chat/completions
// directly.
//
// The response direction is converted back: the client spoke the
// Responses protocol, so the chat pipeline's output (body or SSE
// events) is wrapped by chatToResponsesWriter and re-emitted in
// Responses format.
//
// The original request path and body are restored AFTER the chat
// pipeline finishes, so the caller's retry/error handling sees the
// untouched Responses request — but the pipeline itself must observe
// the converted chat request for its whole run (restoring earlier
// would make getAndValidateTextRequest re-read the raw Responses body
// and leak it upstream).
func relayResponsesConvertToChat(c *gin.Context, request *ResponsesRequest) *relaymodel.ErrorWithStatusCode {
	restore, err := convertResponsesRequestToChat(c, request)
	if err != nil {
		return openai.ErrorWrapper(err, "responses_conversion_failed", http.StatusBadRequest)
	}
	origWriter := c.Writer
	convertBack := newChatToResponsesWriter(origWriter, request.Stream, request.Model)
	c.Writer = convertBack
	bizErr := RelayTextHelper(c)
	c.Writer = origWriter
	convertBack.finish(bizErr != nil)
	restore()
	return bizErr
}

// convertResponsesRequestToChat converts a Responses API request into a Chat
// Completions request body and rewrites the request context so RelayTextHelper
// picks it up as a chat request. It returns a restore closure that puts the
// original path and cached body back — the caller MUST invoke it after the
// chat pipeline completes, not before.
func convertResponsesRequestToChat(c *gin.Context, request *ResponsesRequest) (func(), error) {
	ctx := c.Request.Context()

	// Log the original Responses request shape before conversion.
	logger.Debugf(ctx, "converting Responses request to Chat Completions: model=%q stream=%t instructions=%q input=%v",
		request.Model, request.Stream, request.Instructions, request.Input)

	converted, err := convertResponsesToChatCompletions(request)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(converted)
	if err != nil {
		return nil, err
	}
	// Log the converted Chat Completions body at debug level.
	logger.Debugf(ctx, "converted Chat Completions request: %s", string(body))
	// The fast path in getRequestBody returns c.Request.Body verbatim
	// without re-running the OpenAI adaptor's ConvertRequest. That fast
	// path skips per-channel tool schema adaptations (e.g. opencode-go's
	// flat tools shape), so the upstream would see the standard OpenAI
	// shape and reject. We set the modified flag here so the slow path
	// runs, producing the per-channel adjusted body.
	c.Set(ctxkey.ConvertedFromResponses, "true")

	// Remember the original body/path so they can be restored afterwards. The
	// body was cached by UnmarshalBodyReusable when relayResponsesCreate parsed
	// the request.
	originalBody, _ := common.GetRequestBody(c)
	originalPath := c.Request.URL.Path

	// Inject the converted body and rewrite the path so RelayTextHelper derives
	// the ChatCompletions relay mode (billing, validation and the adaptor all
	// switch on it).
	c.Set(ctxkey.KeyRequestBody, body)
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
	c.Request.URL.Path = "/v1/chat/completions"
	restore := func() {
		c.Request.URL.Path = originalPath
		c.Set(ctxkey.KeyRequestBody, originalBody)
	}

	return restore, nil
}

// relayResponsesCreate relays POST /v1/responses. When the selected channel's
// upstream natively supports the Responses API the body is forwarded
// byte-for-byte and the response is returned as-is; only `model` and `stream`
// are inspected, for routing and billing. When the upstream does not support
// the Responses API (e.g. an opencode-go channel), the request is converted to
// a Chat Completions request and handled by the existing chat pipeline.
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

	// If the upstream does not natively implement the Responses API, convert the
	// request to Chat Completions and delegate to the existing chat pipeline.
	if !upstreamSupportsResponses(meta) {
		return relayResponsesConvertToChat(c, &request)
	}

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

	// post-consume quota — honor the same test synchronous hook as the
	// chat path (see RelayTextHelper) so integration tests stay race-free.
	if PostConsumeQuotaSynchronous {
		postConsumeQuota(ctx, usage, meta, billingRequest, ratio, preConsumedQuota, modelRatio, groupRatio, false)
	} else {
		go postConsumeQuota(ctx, usage, meta, billingRequest, ratio, preConsumedQuota, modelRatio, groupRatio, false)
	}
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
