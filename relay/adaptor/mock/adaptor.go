// Package mock implements a built-in channel whose adaptor synthesizes
// upstream responses in-process. It never opens a network connection.
//
// The mock channel exists so the full relay pipeline — TokenAuth,
// Distribute, RelayTextHelper, quota accounting, streaming render — can
// be exercised end-to-end in tests without depending on a real provider.
//
// Behavior is selected per-request via the X-Mock-Behavior header on the
// inbound (client) request:
//
//	"" / "openai-chat"     non-stream OpenAI chat completion (streams if
//	                       the request has "stream":true)
//	"openai-stream"       forced SSE stream (ignores the stream flag)
//	"openai-tool-call"    non-stream response carrying a tool_call
//	"error-429"           HTTP 429 + OpenAI rate-limit error envelope
//	"error-500"           HTTP 500 + OpenAI server-error envelope
//	"error-400"           HTTP 400 + OpenAI invalid-request envelope
//	"empty"               HTTP 200 with an empty body (robustness probe)
//
// Responses are OpenAI-shaped so DoResponse can delegate to the OpenAI
// adaptor's Handler / StreamHandler and run the real usage/quota code.
package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

var _ adaptor.Adaptor = new(Adaptor)

const (
	channelName = "mock"

	// BehaviorHeader selects which canned response DoRequest synthesizes.
	// See the package doc for the list of accepted values.
	BehaviorHeader = "X-Mock-Behavior"
)

// DefaultModelList is the set of model names the mock channel advertises.
// Tests seed a channel with one of these so Distribute can route to it.
var DefaultModelList = []string{"mock-gpt-4o", "mock-gpt-4o-mini"}

// cannedReply is the non-stream chat content the mock returns. Tests can
// assert on this exact string.
const cannedReply = "Hello from the mock channel."

type Adaptor struct{}

func (a *Adaptor) Init(meta *meta.Meta) {}

func (a *Adaptor) GetRequestURL(meta *meta.Meta) (string, error) {
	// The mock never contacts the network, but DoRequestHelper and the
	// request logger still call this. Return the configured base URL so
	// logs are informative.
	return meta.BaseURL, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Request, meta *meta.Meta) error {
	adaptor.SetupCommonRequestHeader(c, req, meta)
	// The mock doesn't authenticate upstream, but mirror the OpenAI
	// adaptor's Authorization shape so request capture logs look real.
	req.Header.Set("Authorization", "Bearer "+meta.APIKey)
	return nil
}

func (a *Adaptor) ConvertRequest(c *gin.Context, relayMode int, request *model.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, fmt.Errorf("mock: request is nil")
	}
	// The mock speaks OpenAI's wire format verbatim — no conversion.
	return request, nil
}

func (a *Adaptor) ConvertImageRequest(request *model.ImageRequest) (any, error) {
	if request == nil {
		return nil, fmt.Errorf("mock: image request is nil")
	}
	return request, nil
}

// DoRequest synthesizes an *http.Response based on the X-Mock-Behavior
// header. It does NOT call client.HTTPClient — the whole point is to
// avoid the network so the pipeline can be tested hermetically.
//
// Note: callers (DoRequestHelper) will build an outbound *http.Request
// and ask us to send it, but we ignore that request entirely and read
// the inbound client request (c.Request) for behavior selection, since
// that's where X-Mock-Behavior lives.
func (a *Adaptor) DoRequest(c *gin.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error) {
	behavior := ""
	if c.Request != nil {
		behavior = c.Request.Header.Get(BehaviorHeader)
	}
	// Read the body once and reuse it: isStreamRequest and
	// parseModelFromBody both called io.ReadAll before, which consumed
	// the reader on the first call and left the second seeing EOF.
	rawBody, err := io.ReadAll(requestBody)
	if err != nil {
		return nil, fmt.Errorf("mock: read request body: %w", err)
	}
	stream := isStreamRaw(rawBody)
	modelName := parseModelFromRaw(rawBody)
	if modelName == "" && meta != nil {
		modelName = meta.ActualModelName
	}

	switch behavior {
	case "", "openai-chat":
		if stream {
			return newSSEResponse(synthesizeStreamChunks(modelName, cannedReply)), nil
		}
		return newJSONResponse(http.StatusOK, synthesizeChatResponse(modelName, cannedReply, false)), nil
	case "openai-stream":
		return newSSEResponse(synthesizeStreamChunks(modelName, cannedReply)), nil
	case "openai-tool-call":
		return newJSONResponse(http.StatusOK, synthesizeChatResponse(modelName, "", true)), nil
	case "openai-responses":
		// Native Responses API shape (object:"response", output[],
		// usage with input_tokens/output_tokens). Used by the
		// responses->responses passthrough test category. If the
		// request asked for streaming, emit the Responses SSE shape.
		if stream {
			return newSSEResponse(synthesizeResponsesStream(modelName, cannedReply)), nil
		}
		return newJSONResponse(http.StatusOK, synthesizeResponsesResponse(modelName, cannedReply)), nil
	case "openai-responses-stream":
		return newSSEResponse(synthesizeResponsesStream(modelName, cannedReply)), nil
	case "openai-responses-tool-call":
		// Responses API with a function_call output item (instead of a
		// message). Mirrors the Responses shape for tool invocation:
		// output[].type == "function_call" with name + arguments.
		return newJSONResponse(http.StatusOK, synthesizeResponsesToolCallResponse(modelName)), nil
	case "error-429":
		return newJSONResponse(http.StatusTooManyRequests, synthesizeErrorBody("rate limited by mock", "rate_limit_exceeded")), nil
	case "error-500":
		return newJSONResponse(http.StatusInternalServerError, synthesizeErrorBody("mock upstream blew up", "server_error")), nil
	case "error-400":
		return newJSONResponse(http.StatusBadRequest, synthesizeErrorBody("mock bad request", "invalid_request_error")), nil
	case "empty":
		return newBytesResponse(http.StatusOK, "text/plain", nil), nil
	default:
		// Unknown behavior → surface loudly as a 500 so a misconfigured
		// test fails instead of silently succeeding.
		return newJSONResponse(http.StatusInternalServerError, synthesizeErrorBody(
			fmt.Sprintf("mock: unknown behavior %q", behavior), "invalid_mock_behavior")), nil
	}
}

// DoResponse delegates to the OpenAI adaptor's handlers so usage
// accounting, streaming render, and quota settlement run through the
// real code paths the rest of the relay pipeline uses.
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode) {
	if meta.IsStream {
		var responseText string
		err, responseText, usage = openai.StreamHandler(c, resp, meta.Mode)
		if usage == nil || usage.TotalTokens == 0 {
			usage = openai.ResponseText2Usage(responseText, meta.ActualModelName, meta.PromptTokens)
		}
		if usage.TotalTokens != 0 && usage.PromptTokens == 0 {
			usage.PromptTokens = meta.PromptTokens
			usage.CompletionTokens = usage.TotalTokens - meta.PromptTokens
		}
		return
	}
	switch meta.Mode {
	case relaymode.ImagesGenerations:
		err, _ = openai.ImageHandler(c, resp)
	default:
		err, usage = openai.Handler(c, resp, meta.PromptTokens, meta.ActualModelName)
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	out := make([]string, len(DefaultModelList))
	copy(out, DefaultModelList)
	return out
}

func (a *Adaptor) GetChannelName() string {
	return channelName
}

// ---------------------------------------------------------------------------
// response synthesis helpers
// ---------------------------------------------------------------------------

// isStreamRequest reports whether the outbound request body asks for a
// streaming response. We sniff the body once and leave it untouched;
// the real relay pipeline re-reads it via UnmarshalBodyReusable's cache.
func isStreamRequest(body io.Reader) bool {
	raw, err := io.ReadAll(body)
	if err != nil {
		return false
	}
	return isStreamRaw(raw)
}

// isStreamRaw is the byte-slice form of isStreamRequest, used after
// DoRequest has already buffered the body once.
func isStreamRaw(raw []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Stream
}

// parseModelFromBody extracts the "model" field from the request body so
// the synthesized response echoes the model the client asked for. We
// only read it for echo purposes — the relay pipeline owns the real
// request lifecycle.
func parseModelFromBody(body io.Reader) string {
	raw, err := io.ReadAll(body)
	if err != nil {
		return ""
	}
	return parseModelFromRaw(raw)
}

func parseModelFromRaw(raw []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Model
}

// synthesizeChatResponse builds an OpenAI Chat Completions JSON body. If
// withToolCall is true the choice carries a tool_call (finish_reason
// "tool_calls") instead of plain text content.
func synthesizeChatResponse(modelName, content string, withToolCall bool) []byte {
	choice := map[string]any{
		"index":         0,
		"finish_reason": "stop",
	}
	if withToolCall {
		choice["message"] = map[string]any{
			"role": "assistant",
			"tool_calls": []map[string]any{{
				"id":   "call_mock",
				"type": "function",
				"function": map[string]any{
					"name":      "get_weather",
					"arguments": `{"location":"San Francisco, CA","unit":"celsius"}`,
				},
			}},
		}
		choice["finish_reason"] = "tool_calls"
	} else {
		choice["message"] = map[string]any{
			"role":    "assistant",
			"content": content,
		}
	}
	// Token counts are deterministic and small so quota math is
	// predictable in assertions. The numbers below roughly reflect the
	// canned reply + a nominal prompt.
	body := map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": 1700000000,
		"model":   modelName,
		"choices": []map[string]any{choice},
		"usage": map[string]any{
			"prompt_tokens":     9,
			"completion_tokens": 12,
			"total_tokens":      21,
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// synthesizeStreamChunks builds an OpenAI Chat Completions SSE stream.
//
// DATA SOURCE: follows the OpenAI Chat Completions streaming spec
// (platform.openai.com docs, "Streaming" section). Real providers emit
// content across multiple delta chunks (token-by-token); we split on
// word boundaries to exercise the relay's StreamHandler, which
// concatenates choice.Delta.Content across chunks.
//
// Frame sequence (data:-only, no event: lines — unlike Responses):
//
//	data: {"choices":[{"delta":{"role":"assistant"}}]}            ← role chunk
//	data: {"choices":[{"delta":{"content":"Hello"}}]}             ← content delta(s)
//	data: {"choices":[{"delta":{"content":" world"}}]}
//	data: {"choices":[{"delta":{},"finish_reason":"stop"}]}       ← finish
//	data: {"choices":[],"usage":{...}}                            ← usage (stream_options.include_usage)
//	data: [DONE]
func synthesizeStreamChunks(modelName, content string) []byte {
	var buf bytes.Buffer
	writeChunk := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(&buf, "data: %s\n\n", b)
	}
	chunkBase := func() map[string]any {
		return map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": 1700000000, "model": modelName,
		}
	}

	// 1. role chunk — first chunk carries the role, content is empty
	writeChunk(func() map[string]any {
		c := chunkBase()
		c["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}
		return c
	}())
	// 2. content deltas — split into multiple chunks to mirror real
	// token-by-token streaming (the OpenAI StreamHandler concatenates
	// choice.Delta.Content across all chunks)
	for _, piece := range splitIntoChunks(content) {
		writeChunk(func() map[string]any {
			c := chunkBase()
			c["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{"content": piece}, "finish_reason": nil}}
			return c
		}())
	}
	// 3. finish chunk
	writeChunk(func() map[string]any {
		c := chunkBase()
		stop := "stop"
		c["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": stop}}
		return c
	}())
	// 4. usage chunk (matches the non-stream numbers; appears when
	// stream_options.include_usage is set, which the OpenAI adaptor
	// forces on for all stream requests — see openai/adaptor.go:90)
	writeChunk(func() map[string]any {
		c := chunkBase()
		c["choices"] = []map[string]any{}
		c["usage"] = map[string]any{
			"prompt_tokens":     9,
			"completion_tokens": 12,
			"total_tokens":      21,
		}
		return c
	}())
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes()
}

// synthesizeErrorBody returns an OpenAI-shaped error envelope that
// RelayErrorHandler will parse into a friendly client-facing error.
//
// DATA SOURCE: OpenAI API error response shape (platform.openai.com docs,
// "Error codes" section). The envelope is {"error": {message, type, param,
// code}}. The relay's RelayErrorHandler (relay/controller/error.go) parses
// errResponse.Error.Message / .Type to surface to the client, so both
// fields must be populated. The `code` field is `any` in the relay's
// model.Error (can be string, number, or null); we use a descriptive
// string matching what OpenAI actually returns for each error class.
func synthesizeErrorBody(message, errType string) []byte {
	// Map the error type to the `code` value OpenAI actually returns.
	// These are the canonical machine-readable codes clients switch on.
	code := "internal_error"
	switch errType {
	case "rate_limit_exceeded":
		code = "rate_limit_exceeded"
	case "server_error":
		code = "internal_server_error"
	case "invalid_request_error":
		code = "invalid_request"
	}
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   nil,
			"code":    code,
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// splitIntoChunks breaks text into multiple pieces for streaming. Real
// providers emit token-by-token; we split on word boundaries which is
// close enough to exercise the relay's delta-concatenation logic while
// keeping the chunks human-readable in test output. A single-word text
// yields a single chunk (which is still a valid stream).
func splitIntoChunks(text string) []string {
	if text == "" {
		return nil
	}
	words := strings.Fields(text)
	if len(words) <= 1 {
		return []string{text}
	}
	chunks := make([]string, 0, len(words))
	for i, w := range words {
		if i == 0 {
			chunks = append(chunks, w)
		} else {
			// Preserve the space before each subsequent word so the
			// concatenated result matches the original text exactly.
			chunks = append(chunks, " "+w)
		}
	}
	return chunks
}

// synthesizeResponsesResponse builds an OpenAI Responses API (non-stream)
// JSON body. The shape mirrors what relayResponsesNonStream decodes:
//
//	{"id":"resp_mock","object":"response",...,"status":"completed",
//	 "output":[{"type":"message","role":"assistant",
//	            "content":[{"type":"output_text","text":"..."}]}],
//	 "usage":{"input_tokens":19,"output_tokens":7,"total_tokens":26}}
//
// Note the Responses usage field names are input_tokens/output_tokens,
// NOT prompt_tokens/completion_tokens — the relay's ResponsesUsage.ToUsage
// maps them onto the internal Usage struct for billing.
func synthesizeResponsesResponse(modelName, text string) []byte {
	body := map[string]any{
		"id":      "resp_mock",
		"object":  "response",
		"created": 1700000000,
		"model":   modelName,
		"status":  "completed",
		"output": []map[string]any{{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{
			"input_tokens":  19,
			"output_tokens": 7,
			"total_tokens":  26,
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// synthesizeResponsesToolCallResponse builds a Responses API non-stream
// body whose output is a function_call item instead of a message.
//
// DATA SOURCE: OpenAI Responses API docs — when the model invokes a
// tool, the output array contains an item of type "function_call" with
// name, arguments (JSON string), call_id, and status. This mirrors the
// Chat Completions tool_calls shape but at the Responses item level.
func synthesizeResponsesToolCallResponse(modelName string) []byte {
	body := map[string]any{
		"id":      "resp_mock",
		"object":  "response",
		"created": 1700000000,
		"model":   modelName,
		"status":  "completed",
		"output": []map[string]any{{
			"type":      "function_call",
			"id":        "fc_mock",
			"call_id":   "call_mock",
			"name":      "get_weather",
			"arguments": `{"location":"San Francisco, CA","unit":"celsius"}`,
			"status":    "completed",
		}},
		"usage": map[string]any{
			"input_tokens":  19,
			"output_tokens": 7,
			"total_tokens":  26,
		},
	}
	out, _ := json.Marshal(body)
	return out
}

// synthesizeResponsesStream builds an OpenAI Responses API SSE stream.
//
// DATA SOURCE: The event sequence below follows the official OpenAI
// Responses streaming event list (see developers.openai.com api docs,
// StreamingEvent union). The relay forwards frames verbatim and only
// scans data lines for a usage block via usageFromSSELine, which accepts
// usage at the top level OR nested under "response.usage".
//
// Complete event sequence for a simple text response (each event is an
// "event:" line followed by a "data:" line, per the SSE spec):
//
//	event: response.created              data: {type, response:{...}}
//	event: response.in_progress          data: {type, response:{...}}
//	event: response.output_item.added    data: {type, output_index:0, item:{type:"message",...}}
//	event: response.content_part.added   data: {type, output_index:0, content_index:0, part:{type:"output_text",...}}
//	event: response.output_text.delta    data: {type, output_index:0, content_index:0, delta:"<chunk>"}
//	  ... (one delta per text chunk)
//	event: response.output_text.done     data: {type, output_index:0, content_index:0, text:"<full>"}
//	event: response.content_part.done    data: {type, output_index:0, content_index:0, part:{...}}
//	event: response.output_item.done     data: {type, output_index:0, item:{...}}
//	event: response.completed            data: {type, response:{...,"usage":{...}}}  ← usage here
//	data: [DONE]
//
// The text is split into multiple delta chunks to mirror how real
// providers stream (token-by-token), exercising the relay's line-by-line
// SSE forwarding. We split on word boundaries for readability.
func synthesizeResponsesStream(modelName, text string) []byte {
	var buf bytes.Buffer
	writeFrame := func(event string, payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(&buf, "event: %s\n", event)
		fmt.Fprintf(&buf, "data: %s\n\n", b)
	}

	const respID = "resp_mock"
	const outputIndex = 0
	const contentIndex = 0

	baseResponse := func(status string) map[string]any {
		return map[string]any{
			"id": respID, "object": "response",
			"created_at": 1700000000, "model": modelName,
			"status": status,
		}
	}

	// 1. response.created
	writeFrame("response.created", map[string]any{
		"type": "response.created", "response": baseResponse("in_progress"),
	})
	// 2. response.in_progress
	writeFrame("response.in_progress", map[string]any{
		"type": "response.in_progress", "response": baseResponse("in_progress"),
	})
	// 3. response.output_item.added — the message item appears
	messageItem := map[string]any{
		"type": "message", "role": "assistant",
		"content": []any{}, "status": "in_progress",
	}
	writeFrame("response.output_item.added", map[string]any{
		"type":         "response.output_item.added",
		"output_index": outputIndex, "item": messageItem,
	})
	// 4. response.content_part.added — the output_text part appears
	textPart := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
	writeFrame("response.content_part.added", map[string]any{
		"type":         "response.content_part.added",
		"output_index": outputIndex, "content_index": contentIndex, "part": textPart,
	})
	// 5. response.output_text.delta — one per text chunk
	for _, chunk := range splitIntoChunks(text) {
		writeFrame("response.output_text.delta", map[string]any{
			"type":         "response.output_text.delta",
			"output_index": outputIndex, "content_index": contentIndex,
			"delta": chunk,
		})
	}
	// 6. response.output_text.done
	writeFrame("response.output_text.done", map[string]any{
		"type":         "response.output_text.done",
		"output_index": outputIndex, "content_index": contentIndex,
		"text": text,
	})
	// 7. response.content_part.done
	writeFrame("response.content_part.done", map[string]any{
		"type":         "response.content_part.done",
		"output_index": outputIndex, "content_index": contentIndex,
		"part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
	})
	// 8. response.output_item.done
	writeFrame("response.output_item.done", map[string]any{
		"type":         "response.output_item.done",
		"output_index": outputIndex,
		"item": map[string]any{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
			"status":  "completed",
		},
	})
	// 9. response.completed — carries the final usage block the relay
	// extracts for billing (usageFromSSELine matches response.usage).
	completed := baseResponse("completed")
	completed["output"] = []map[string]any{{
		"type": "message", "role": "assistant",
		"content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}},
		"status":  "completed",
	}}
	completed["usage"] = map[string]any{
		"input_tokens":  19,
		"output_tokens": 7,
		"total_tokens":  26,
	}
	writeFrame("response.completed", map[string]any{
		"type": "response.completed", "response": completed,
	})
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes()
}

// newJSONResponse assembles an *http.Response with a JSON body. The
// relay pipeline reads Body and StatusCode; Header is populated for
// completeness.
func newJSONResponse(status int, body []byte) *http.Response {
	return newBytesResponse(status, "application/json", body)
}

func newSSEResponse(body []byte) *http.Response {
	return newBytesResponse(http.StatusOK, "text/event-stream", body)
}

func newBytesResponse(status int, contentType string, body []byte) *http.Response {
	h := http.Header{}
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
