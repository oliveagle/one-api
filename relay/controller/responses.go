// Package controller is a package for handling the relay controller
package controller

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	dbmodel "github.com/songquanpeng/one-api/model"
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
// through untouched. Channels that do not are refused with 503 so the relay's
// failover can walk the rest of the pool — protocol conversion between the
// Responses and Chat Completions APIs has been removed (pools are split by
// wire protocol instead, e.g. coding_resps / coding_chat).
//
// A channel opts in to passthrough via the `support_responses` channel config
// flag (or `responses_only` for upstreams without a chat endpoint). The
// OpenAIResponses channel type implies native Responses support, and the
// AIHubMix channel type is additionally treated as native because the
// passthrough path was originally verified against it (see
// docs/adr/0001-openai-responses-api-passthrough.md).
func upstreamSupportsResponses(meta *meta.Meta) bool {
	if meta == nil {
		return false
	}
	if meta.Config.SupportResponses || meta.Config.ResponsesOnly {
		return true
	}
	// OpenCode channels: 内部做 Responses→Chat 转换，自动识别为 Responses 能力。
	if meta.ChannelType == channeltype.OpenCode {
		return true
	}
	return meta.ChannelType == channeltype.AIHubMix || meta.ChannelType == channeltype.OpenAIResponses
}

// relayResponsesCreate relays POST /v1/responses. The body is forwarded
// byte-for-byte and the response is returned as-is; only `model` and `stream`
// are inspected, for routing and billing. Channels whose upstream does not
// natively serve the Responses API are refused with 503 so the relay's
// failover can land the request on a responses-capable channel — protocol
// conversion between the Responses and Chat Completions APIs no longer exists.
func relayResponsesCreate(c *gin.Context) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)

	var request ResponsesRequest
	if err := common.UnmarshalBodyReusable(c, &request); err != nil {
		return openai.ErrorWrapper(err, "invalid_responses_request", http.StatusBadRequest)
	}
	// Mirror middleware.getRequestModel's trim: the body's model must match
	// what routing selected (some clients emit a leading space), so mapping
	// lookups and the forwarded model are consistent.
	request.Model = strings.TrimSpace(request.Model)
	// `model` is optional in the Responses spec (only Authorization is required),
	// but one-api needs it to pick a channel: CacheGetRandomSatisfiedChannel keys
	// on group+model. Requests without it are already rejected upstream of this
	// helper by middleware.Distribute with 503 "no channel available", the same
	// way /v1/chat/completions behaves, so no check is duplicated here.
	meta.IsStream = request.Stream

	// opencode 旧配置走 Responses → Chat 转换；新配置直接 passthrough。
	if isOpencodeChannel(c) {
		return relayResponsesOpencodeCreate(c, &request)
	}

	// No conversion: a Responses request may only be served by a channel whose
	// upstream natively speaks the Responses API. 503 (not 400) so the relay's
	// failover keeps walking the pool; if every channel for this model is
	// chat-only the client sees this error and knows the model is chat-wire
	// only.
	if !upstreamSupportsResponses(meta) {
		return openai.ErrorWrapper(fmt.Errorf(
			"channel %q (model %q) does not support the Responses API; responses↔chat conversion has been removed — route this model through a responses-capable channel",
			c.GetString(ctxkey.ChannelName), request.Model), "responses_unsupported_on_channel", http.StatusServiceUnavailable)
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
		errResp := RelayErrorHandler(resp)
		applyRateLimitCooldown(c, meta, resp, errResp)
		return errResp
	}

	usage, respErr := relayResponsesResponse(c, resp)
	if respErr != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		applyRateLimitCooldown(c, meta, nil, respErr)
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
//
// Additionally, the channel's reasoning_effort_map (a JSON object in the
// channel config) rewrites non-standard reasoning effort values the upstream
// rejects — e.g. vLLM qwen3.8 wants "xhigh" where OpenAI uses "high" — and
// malformed function_call arguments in the input history are repaired in
// place (see sanitizeResponsesToolCallArgs), which is the one repair that
// must run even on otherwise byte-for-byte bodies: the request is undeliverable
// without it.
func getResponsesRequestBody(c *gin.Context, request *ResponsesRequest) (io.Reader, error) {
	original, err := common.GetRequestBody(c)
	if err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}
	changed := false

	// Model rewrite
	current, _ := json.Marshal(request.Model)
	if existing, ok := raw["model"]; !ok || !bytes.Equal(existing, current) {
		raw["model"] = current
		changed = true
	}

	// Reasoning effort rewrite (channel config: {"reasoning_effort_map": {"high":"xhigh"}})
	if effortMap := reasoningEffortMap(c); len(effortMap) > 0 {
		if reasoningRaw, ok := raw["reasoning"]; ok {
			var reasoning struct {
				Effort string `json:"effort"`
			}
			if err := json.Unmarshal(reasoningRaw, &reasoning); err == nil && reasoning.Effort != "" {
				if mapped, ok := effortMap[reasoning.Effort]; ok && mapped != reasoning.Effort {
					newReasoning, _ := json.Marshal(map[string]string{"effort": mapped, "summary": "auto"})
					raw["reasoning"] = newReasoning
					changed = true
				}
			}
		}
	}

	// Repair poisoned function_call arguments in the input history: every
	// real upstream validator rejects the whole request otherwise, and
	// clients replay history each turn, wedging the session on every
	// channel. See sanitizeResponsesToolCallArgs for the incident record.
	if sanitizeResponsesToolCallArgs(raw) {
		changed = true
		logger.Warnf(c.Request.Context(), "responses: repaired malformed function_call arguments in input history")
	}

	// opencode session headers: opencode.ai/zen/go rejects requests without
	// x-opencode-session/request/client. These live in HTTP headers (not the
	// body), so we stash them on the gin context for the adaptor to apply.
	if isOpencodeChannel(c) {
		c.Set(ctxkey.OpencodeSession, opencodeSessionID())
		c.Set(ctxkey.OpencodeRequest, opencodeRequestID())
	}

	if !changed {
		return bytes.NewReader(original), nil
	}
	rewritten, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(rewritten), nil
}

// sanitizeResponsesToolCallArgs repairs function_call items in the
// request's `input` history whose `arguments` payload is missing or not
// valid JSON. It returns true when any item was repaired.
//
// DATA SOURCE: production incidents of 2026-09-09. Upstream models
// occasionally emit a function_call whose arguments JSON is truncated
// mid-string (qwen3.8-27b on vLLM: an exec_command call cut off at 178
// bytes with no closing quote) or carries a non-JSON literal (write_stdin
// with "session_id": none). Downstream validators then reject the ENTIRE
// request — vLLM's chat_utils json.loads answers "Unterminated string
// starting at: line 1 column 9 (char 8)", volcengine ark answers
// "missing `input.arguments` (MissingParameter)" — and because coding
// agents replay the full history on every turn, the session is wedged on
// every channel it is routed to. Replacing the single malformed payload
// with "{}" turns a permanent 400 into one recoverable turn; the tool
// output already in history ("failed to parse function arguments: ...")
// tells the model its call never executed, so it re-issues the call.
//
// Healthy requests are untouched: the function returns false and the
// caller keeps forwarding the original bytes verbatim.
func sanitizeResponsesToolCallArgs(raw map[string]json.RawMessage) bool {
	inputRaw, ok := raw["input"]
	if !ok {
		return false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		// `input` as a plain string (prompt form) or a non-array shape:
		// nothing to sanitize.
		return false
	}
	repaired := false
	for i, itemRaw := range items {
		var probe struct {
			Type      string  `json:"type"`
			Arguments *string `json:"arguments"`
		}
		if err := json.Unmarshal(itemRaw, &probe); err != nil {
			continue
		}
		// Only function_call items carry JSON-in-a-string arguments;
		// custom_tool_call.input is freeform text and must NOT be
		// JSON-validated, and structured action objects are already valid.
		if probe.Type != "function_call" {
			continue
		}
		if probe.Arguments != nil && json.Valid([]byte(*probe.Arguments)) {
			continue
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			continue
		}
		item["arguments"] = json.RawMessage(`"{}"`)
		newItem, err := json.Marshal(item)
		if err != nil {
			continue
		}
		items[i] = newItem
		repaired = true
	}
	if !repaired {
		return false
	}
	newInput, err := json.Marshal(items)
	if err != nil {
		return false
	}
	raw["input"] = newInput
	return true
}

// reasoningEffortMap reads the channel's reasoning_effort_map from the gin
// context config. Returns nil when not configured.
func reasoningEffortMap(c *gin.Context) map[string]string {
	cfg, ok := c.Get(ctxkey.Config)
	if !ok {
		return nil
	}
	channelCfg, ok := cfg.(dbmodel.ChannelConfig)
	if !ok {
		return nil
	}
	if channelCfg.ReasoningEffortMap == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(channelCfg.ReasoningEffortMap), &m); err != nil {
		return nil
	}
	return m
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

// isOpencodeChannel checks if the current request is routed to an opencode channel.
// opencode channels are identified by their base_url containing the opencode path.
func isOpencodeChannel(c *gin.Context) bool {
	return c.GetInt(ctxkey.Channel) == channeltype.OpenCode
}

// relayResponsesOpencodeCreate handles POST /v1/responses for opencode channels.
// It converts the Responses API request to Chat Completions, sends it to the
// opencode upstream, and converts the Chat response back to Responses format.
//
// This is opencode-specific and NOT a general-purpose conversion layer.
func relayResponsesOpencodeCreate(c *gin.Context, request *ResponsesRequest) *relaymodel.ErrorWithStatusCode {
	ctx := c.Request.Context()
	meta := meta.GetByContext(c)
	meta.IsStream = request.Stream

	// Map the model name the same way the text path does.
	meta.OriginModelName = request.Model
	mappedModel, _ := getMappedModelName(request.Model, meta.ModelMapping)
	request.Model = mappedModel
	meta.ActualModelName = mappedModel

	// Billing: same as the normal Responses path.
	modelRatio := billingratio.GetModelRatio(request.Model, meta.ChannelType)
	groupRatio := billingratio.GetGroupRatio(meta.Group)
	ratio := modelRatio * groupRatio

	promptTokens := estimateResponsesPromptTokens(request)
	meta.PromptTokens = promptTokens

	billingRequest := &relaymodel.GeneralOpenAIRequest{
		Model:     request.Model,
		MaxTokens: request.MaxOutput,
	}
	preConsumedQuota, bizErr := preConsumeQuota(ctx, billingRequest, promptTokens, ratio, meta)
	if bizErr != nil {
		logger.Warnf(ctx, "opencode: preConsumeQuota failed: %+v", *bizErr)
		return bizErr
	}

	// Get adaptor (will be the OpenAI adaptor for opencode).
	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// opencode session headers.
	if isOpencodeChannel(c) {
		c.Set(ctxkey.OpencodeSession, opencodeSessionID())
		c.Set(ctxkey.OpencodeRequest, opencodeRequestID())
	}

	// Step 1: Convert Responses request → Chat request（bitx-proxy 的 to_chat_wire_json 等价逻辑）
	chatRequest := opencodeResponsesToChatRequest(request)

	// Step 2: Let the adaptor transform the Chat request (e.g. stream_options injection).
	converted, err := adaptor.ConvertRequest(c, meta.Mode, chatRequest)
	if err != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(err, "convert_request_failed", http.StatusInternalServerError)
	}

	chatBody, err := json.Marshal(converted)
	if err != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(err, "marshal_chat_request_failed", http.StatusInternalServerError)
	}

	logger.Debugf(ctx, "opencode: converted Responses→Chat request: %s", string(chatBody))

	// Step 3: Send Chat request to opencode upstream.
	origPath := meta.RequestURLPath
	meta.RequestURLPath = "/v1/chat/completions"
	resp, err := adaptor.DoRequest(c, meta, bytes.NewReader(chatBody))
	meta.RequestURLPath = origPath
	if err != nil {
		logger.Errorf(ctx, "opencode: DoRequest failed: %s", err.Error())
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return RelayErrorHandler(resp)
	}

	// Step 4: Convert Chat response → Responses response（bitx-proxy 的 from_chat_response_json / from_chat_chunk_json 等价逻辑）
	usage, respErr := opencodeRelayResponsesFromChatResponse(c, resp, request.Model, meta.IsStream)
	if respErr != nil {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return respErr
	}

	// post-consume quota
	if PostConsumeQuotaSynchronous {
		postConsumeQuota(ctx, usage, meta, billingRequest, ratio, preConsumedQuota, modelRatio, groupRatio, false)
	} else {
		go postConsumeQuota(ctx, usage, meta, billingRequest, ratio, preConsumedQuota, modelRatio, groupRatio, false)
	}
	return nil
}

// opencodeRelayResponsesFromChatResponse 读取 Chat 响应并转换为 Responses 格式。
func opencodeRelayResponsesFromChatResponse(c *gin.Context, resp *http.Response, requestModel string, isStream bool) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	if isStream {
		return opencodeRelayResponsesStreamFromChat(c, resp, requestModel)
	}
	return opencodeRelayResponsesNonStreamFromChat(c, resp, requestModel)
}

// opencodeRelayResponsesNonStreamFromChat 读取完整的 Chat 非流式响应，转换为 Responses 格式。
func opencodeRelayResponsesNonStreamFromChat(c *gin.Context, resp *http.Response, requestModel string) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "read_chat_response_failed", http.StatusInternalServerError)
	}

	// Parse Chat response
	var chatResp map[string]any
	if err := json.Unmarshal(body, &chatResp); err != nil {
		return nil, openai.ErrorWrapper(err, "unmarshal_chat_response_failed", http.StatusInternalServerError)
	}

	// Check for upstream error
	if errObj, ok := chatResp["error"].(map[string]any); ok {
		errMsg, _ := errObj["message"].(string)
		if errMsg != "" {
			return nil, &relaymodel.ErrorWithStatusCode{
				Error: relaymodel.Error{
					Message: errMsg,
					Type:    "upstream_error",
				},
				StatusCode: http.StatusBadGateway,
			}
		}
	}

	// Convert: Chat → Responses（bitx-proxy from_chat_response_json 等价逻辑）
	responsesResp := opencodeChatToResponsesResponse(chatResp, requestModel)

	// Marshal Responses response
	responseBody, err := json.Marshal(responsesResp)
	if err != nil {
		return nil, openai.ErrorWrapper(err, "marshal_responses_response_failed", http.StatusInternalServerError)
	}

	// Write response to client
	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if _, err := c.Writer.Write(responseBody); err != nil {
		logger.Errorf(c.Request.Context(), "opencode: write response failed: %s", err.Error())
	}

	// Extract usage for billing
	usage := opencodeExtractUsage(chatResp)
	return usage, nil
}

// opencodeRelayResponsesStreamFromChat 把 Chat 流式响应转换为 Responses 流式事件。
func opencodeRelayResponsesStreamFromChat(c *gin.Context, resp *http.Response, requestModel string) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	defer resp.Body.Close()

	// Set SSE headers for Responses API
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	streamState := &opencodeStreamState{model: requestModel, nextOutputIndex: 1}
	var lastUsage *relaymodel.Usage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), opencodeMaxStreamLineLen)
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		line := scanner.Text()

		// 转换：Chat SSE → Responses SSE（bitx-proxy from_chat_chunk_json 等价逻辑）
		events := opencodeChatStreamToResponsesStream(line, streamState)
		for _, event := range events {
			if _, err := fmt.Fprint(c.Writer, event); err != nil {
				logger.Errorf(c.Request.Context(), "opencode: write stream event failed: %s", err.Error())
				return lastUsage, nil
			}
			c.Writer.Flush()
		}

		// 从 usage chunk 提取 token 用量
		if usage := opencodeExtractUsageFromStreamLine(line); usage != nil {
			lastUsage = usage
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Errorf(c.Request.Context(), "opencode: scan stream failed: %s", err.Error())
	}

	// Guarantee response.completed is emitted even if no finish_reason was received.
	// Some providers send a usage-only final chunk or just [DONE] without finish_reason.
	if !streamState.completed {
		respID := streamState.respId
		if respID == "" {
			respID = opencodeGenID("resp")
		}
		usageData := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
		if lastUsage != nil {
			usageData = opencodeMapUsage(lastUsage)
		}
		completedEvent := opencodeSSEEvent("response.completed", map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"id":         respID,
				"object":     "response",
				"created_at": time.Now().Unix(),
				"model":      streamState.model,
				"status":     "completed",
				"output":     opencodeBuildOutput(streamState),
				"usage":      usageData,
			},
		})
		_, _ = fmt.Fprint(c.Writer, completedEvent)
		c.Writer.Flush()
	}

	if lastUsage == nil {
		lastUsage = &relaymodel.Usage{}
	}

	return lastUsage, nil
}

// opencodeExtractUsage 从 Chat 非流式响应中提取 Usage。
func opencodeExtractUsage(chatResp map[string]any) *relaymodel.Usage {
	u, ok := chatResp["usage"].(map[string]any)
	if !ok {
		return &relaymodel.Usage{}
	}
	usage := &relaymodel.Usage{}
	if pt, ok := u["prompt_tokens"].(float64); ok {
		usage.PromptTokens = int(pt)
	}
	if ct, ok := u["completion_tokens"].(float64); ok {
		usage.CompletionTokens = int(ct)
	}
	if tt, ok := u["total_tokens"].(float64); ok {
		usage.TotalTokens = int(tt)
	}
	return usage
}

// opencodeExtractUsageFromStreamLine 从 Chat 流式 SSE 行中提取 Usage。
func opencodeExtractUsageFromStreamLine(line string) *relaymodel.Usage {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, opencodeDataPrefix) {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(trimmed, opencodeDataPrefix))
	if data == "" || data == opencodeDone {
		return nil
	}
	var chunk struct {
		Usage *relaymodel.Usage `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return nil
	}
	if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
		return chunk.Usage
	}
	return nil
}

var (
	opencodeOnce     sync.Once
	opencodeSessID   string
	opencodeReqAlloc sync.Mutex
	opencodeReqSeq   int64
)

// opencodeSessionID returns a stable per-process session ID.
func opencodeSessionID() string {
	opencodeOnce.Do(func() {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err == nil {
			opencodeSessID = "oneapi-" + hex.EncodeToString(b)
		} else {
			opencodeSessID = fmt.Sprintf("oneapi-%d", time.Now().UnixNano())
		}
	})
	return opencodeSessID
}

// opencodeRequestID returns a unique per-request ID.
func opencodeRequestID() string {
	opencodeReqAlloc.Lock()
	defer opencodeReqAlloc.Unlock()
	opencodeReqSeq++
	return fmt.Sprintf("oneapi-req-%d-%d", time.Now().UnixNano(), opencodeReqSeq)
}
