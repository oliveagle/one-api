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

// synthesizeStreamChunks builds an OpenAI-compatible SSE stream: one
// role chunk, one or more content delta chunks, a final stop chunk, an
// optional usage chunk, and the [DONE] sentinel.
func synthesizeStreamChunks(modelName, content string) []byte {
	var buf bytes.Buffer
	writeChunk := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(&buf, "data: %s\n\n", b)
	}

	// role
	writeChunk(map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"created": 1700000000, "model": modelName,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}}},
	})
	// content (single delta keeps assertions simple; the OpenAI
	// StreamHandler concatenates choice.Delta.Content across chunks)
	if content != "" {
		writeChunk(map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": 1700000000, "model": modelName,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": content}}},
		})
	}
	stop := "stop"
	// finish
	writeChunk(map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"created": 1700000000, "model": modelName,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": stop}},
	})
	// usage (matches the non-stream numbers)
	writeChunk(map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk",
		"created": 1700000000, "model": modelName,
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     9,
			"completion_tokens": 12,
			"total_tokens":      21,
		},
	})
	buf.WriteString("data: [DONE]\n\n")
	return buf.Bytes()
}

// synthesizeErrorBody returns an OpenAI-shaped error envelope that
// RelayErrorHandler will parse into a friendly client-facing error.
func synthesizeErrorBody(message, errType string) []byte {
	body := map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   "",
			"code":    nil,
		},
	}
	out, _ := json.Marshal(body)
	return out
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

// synthesizeResponsesStream builds an OpenAI Responses API SSE stream.
// The Responses streaming protocol interleaves "event:" lines with
// "data:" lines (unlike Chat Completions which is data-only). The relay
// forwards frames verbatim and only scans data lines for a usage block
// via usageFromSSELine, which accepts usage at the top level OR nested
// under "response". We emit a response.completed frame carrying usage
// at the end so billing settles correctly.
//
// Frame sequence (minimal but protocol-valid):
//
//	event: response.created
//	data: {"type":"response.created","response":{"id":"resp_mock",...}}
//	event: response.output_text.delta
//	data: {"type":"response.output_text.delta","delta":"<text>"}
//	event: response.completed
//	data: {"type":"response.completed","response":{...,"usage":{...}}}
//	data: [DONE]
func synthesizeResponsesStream(modelName, text string) []byte {
	var buf bytes.Buffer
	writeFrame := func(event string, payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(&buf, "event: %s\n", event)
		fmt.Fprintf(&buf, "data: %s\n\n", b)
	}

	respSkeleton := map[string]any{
		"id": "resp_mock", "object": "response",
		"created": 1700000000, "model": modelName, "status": "in_progress",
	}
	// response.created
	writeFrame("response.created", map[string]any{
		"type":     "response.created",
		"response": respSkeleton,
	})
	// output text delta
	writeFrame("response.output_text.delta", map[string]any{
		"type":  "response.output_text.delta",
		"delta": text,
	})
	// response.completed — carries the final usage block the relay
	// extracts for billing (usageFromSSELine matches response.usage).
	completed := map[string]any{
		"id": "resp_mock", "object": "response",
		"created": 1700000000, "model": modelName, "status": "completed",
		"output": []map[string]any{{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text}},
		}},
		"usage": map[string]any{
			"input_tokens":  19,
			"output_tokens": 7,
			"total_tokens":  26,
		},
	}
	writeFrame("response.completed", map[string]any{
		"type":     "response.completed",
		"response": completed,
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
