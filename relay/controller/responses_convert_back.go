package controller

// Chat → Responses response conversion.
//
// When a Responses API request is converted to Chat Completions
// (relayResponsesConvertToChat, for channels whose upstream does not
// implement POST /v1/responses), the chat pipeline writes a Chat
// Completions body — but the client spoke the Responses protocol and
// expects a Responses-shaped reply. This file converts the output of
// the chat pipeline back into Responses format:
//
//   - non-stream: the full chat.completion JSON is converted into a
//     single response object (output[] with message / function_call
//     items, usage renamed to input_tokens/output_tokens).
//   - stream: chat.completion.chunk SSE frames are translated into the
//     Responses streaming event sequence (response.created,
//     response.output_item.added, response.output_text.delta,
//     response.output_item.done, response.completed, ...), mirroring
//     the event vocabulary the mock channel's native Responses stream
//     uses (see relay/adaptor/mock synthesizeResponsesStream).
//
// The hook is chatToResponsesWriter, a gin.ResponseWriter wrapper
// swapped in around RelayTextHelper. Errors returned by the chat
// pipeline bypass the wrapper (the writer is restored before
// controller.Relay renders the error envelope), so error bodies stay
// in the shared OpenAI error shape.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/random"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// ===========================================================================
// Non-stream conversion
// ===========================================================================

// convertChatCompletionToResponses converts a Chat Completions
// non-stream body into a Responses API body. Model falls back to
// fallbackModel when the chat body carries none (some upstreams omit
// it).
func convertChatCompletionToResponses(body []byte, fallbackModel string) ([]byte, error) {
	var chat openai.TextResponse
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, fmt.Errorf("parse chat completion: %w", err)
	}

	model := chat.Model
	if model == "" {
		model = fallbackModel
	}
	id := chat.Id
	if id == "" {
		id = "resp_" + random.GetUUID()
	}

	output := make([]map[string]any, 0, 2)
	for _, choice := range chat.Choices {
		msg := choice.Message
		if msg.StringContent() != "" {
			output = append(output, map[string]any{
				"type":   "message",
				"id":     generateMessageID(),
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{{
					"type":        "output_text",
					"text":        msg.StringContent(),
					"annotations": []any{},
				}},
			})
		}
		for _, tc := range msg.ToolCalls {
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        tc.Id,
				"call_id":   tc.Id,
				"name":      tc.Function.Name,
				"arguments": tc.Function.Arguments,
				"status":    "completed",
			})
		}
	}
	if len(output) == 0 {
		// Defensive: a chat body with no content and no tool calls
		// still deserves a well-formed (empty) Responses output array.
		output = []map[string]any{}
	}

	resp := map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": chat.Created,
		"model":      model,
		"status":     "completed",
		"output":     output,
		"usage": map[string]any{
			"input_tokens":  chat.Usage.PromptTokens,
			"output_tokens": chat.Usage.CompletionTokens,
			"total_tokens":  chat.Usage.TotalTokens,
		},
	}
	return json.Marshal(resp)
}

// ===========================================================================
// Stream translation
// ===========================================================================

// streamToolAccum buffers one in-flight tool call: the name arrives in
// the first delta fragment, the arguments stream in piecemeal.
type streamToolAccum struct {
	id    string
	name  string
	args  strings.Builder
	added bool
}

// chatToResponsesStreamTranslator is the incremental state machine
// turning chat.completion.chunk SSE payloads into Responses events.
//
// NOTE on tool call indices: the chat wire format carries
// tool_calls[].index, but the relay's model.Message (used to decode the
// delta) has no Index field, so parallel tool calls cannot be told
// apart by index. We accumulate sequentially instead: a fragment WITH
// an id starts a new call, fragments without one continue the current
// call. Providers emit each call's fragments contiguously, which makes
// this correct for the sequential (and in practice parallel) cases.
type chatToResponsesStreamTranslator struct {
	model string

	respID    string
	created   bool // response.created emitted
	msgOpen   bool // message output item open
	nextIndex int  // next output_index
	text      strings.Builder
	tools     []*streamToolAccum // in arrival order
	usage     *relaymodel.Usage
	completed bool
}

func newChatToResponsesStreamTranslator(model string) *chatToResponsesStreamTranslator {
	return &chatToResponsesStreamTranslator{
		model:     model,
		respID:    "resp_" + random.GetUUID(),
		nextIndex: 0,
	}
}

func (t *chatToResponsesStreamTranslator) frame(out *bytes.Buffer, event string, payload map[string]any) {
	payload["type"] = event
	b, _ := json.Marshal(payload)
	fmt.Fprintf(out, "event: %s\ndata: %s\n\n", event, b)
}

func (t *chatToResponsesStreamTranslator) baseResponse(status string) map[string]any {
	return map[string]any{
		"id": t.respID, "object": "response",
		"model": t.model, "status": status,
	}
}

// startSequence emits the opening events exactly once, opening the
// message output item at output_index 0.
func (t *chatToResponsesStreamTranslator) startSequence(out *bytes.Buffer) {
	if t.created {
		return
	}
	t.created = true
	t.frame(out, "response.created", map[string]any{"response": t.baseResponse("in_progress")})
	t.frame(out, "response.in_progress", map[string]any{"response": t.baseResponse("in_progress")})
	t.msgOpen = true
	t.nextIndex = 1
	t.frame(out, "response.output_item.added", map[string]any{
		"output_index": 0,
		"item": map[string]any{
			"type": "message", "role": "assistant",
			"content": []any{}, "status": "in_progress",
		},
	})
	t.frame(out, "response.content_part.added", map[string]any{
		"output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	})
}

// closeMessage emits the closing events for the message item.
func (t *chatToResponsesStreamTranslator) closeMessage(out *bytes.Buffer) {
	if !t.msgOpen {
		return
	}
	t.msgOpen = false
	full := t.text.String()
	t.frame(out, "response.output_text.done", map[string]any{
		"output_index": 0, "content_index": 0, "text": full,
	})
	t.frame(out, "response.content_part.done", map[string]any{
		"output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": full, "annotations": []any{}},
	})
	t.frame(out, "response.output_item.done", map[string]any{
		"output_index": 0,
		"item": map[string]any{
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type": "output_text", "text": full, "annotations": []any{},
			}},
		},
	})
}

// Feed processes one chat SSE data payload (the JSON string after
// "data: ", or "[DONE]") and returns Responses SSE bytes to emit.
func (t *chatToResponsesStreamTranslator) Feed(payload string, out *bytes.Buffer) {
	if payload == "[DONE]" {
		t.finish(out)
		return
	}
	var chunk openai.ChatCompletionsStreamResponse
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		// Not a chat chunk — skip rather than break the stream.
		return
	}
	if chunk.Usage != nil {
		t.usage = chunk.Usage
	}
	if len(chunk.Choices) == 0 {
		return
	}
	choice := &chunk.Choices[0]
	delta := choice.Delta

	if delta.Role == "assistant" {
		// role chunk — the start sequence opens lazily on first
		// content/tool fragment below.
	}
	if text := delta.StringContent(); text != "" {
		t.startSequence(out)
		t.text.WriteString(text)
		t.frame(out, "response.output_text.delta", map[string]any{
			"output_index": 0, "content_index": 0, "delta": text,
		})
	}
	for _, tc := range delta.ToolCalls {
		// A fragment carrying an id starts a new tool call; fragments
		// without one continue the current (last) call.
		var accum *streamToolAccum
		if tc.Id != "" {
			accum = &streamToolAccum{id: tc.Id}
			t.tools = append(t.tools, accum)
		} else if len(t.tools) > 0 {
			accum = t.tools[len(t.tools)-1]
		} else {
			// Arguments fragment before any id arrived — tolerate by
			// starting an anonymous call.
			accum = &streamToolAccum{}
			t.tools = append(t.tools, accum)
		}
		if tc.Function.Name != "" {
			accum.name = tc.Function.Name
		}
		// A tool call after text content closes the message item
		// first; tool calls get output_index 1, 2, ...
		t.startSequence(out)
		if !accum.added {
			accum.added = true
			t.closeMessage(out)
			t.frame(out, "response.output_item.added", map[string]any{
				"output_index": t.nextIndex,
				"item": map[string]any{
					"type": "function_call", "id": accum.id, "call_id": accum.id,
					"name": accum.name, "arguments": "", "status": "in_progress",
				},
			})
			t.nextIndex++
		}
		if args := toolCallArgumentsString(tc.Function.Arguments); args != "" {
			accum.args.WriteString(args)
			t.frame(out, "response.function_call_arguments.delta", map[string]any{
				"output_index": t.nextIndex - 1, "item_id": accum.id,
				"delta": args,
			})
		}
	}
	if choice.FinishReason != nil && *choice.FinishReason != "" {
		// The chat stream's final usage chunk (stream_options
		// include_usage) arrives AFTER finish_reason but BEFORE
		// [DONE], so the closing events are deferred until [DONE] (or
		// the writer's finish fallback) to capture the usage.
		t.startSequence(out)
	}
}

// finish emits the closing events: output_item.done for any open tool
// calls, then response.completed carrying the usage block, then the
// terminal [DONE] frame. Idempotent.
func (t *chatToResponsesStreamTranslator) finish(out *bytes.Buffer) {
	if t.completed {
		return
	}
	t.completed = true
	t.startSequence(out)

	for i, accum := range t.tools {
		if !accum.added {
			continue
		}
		t.frame(out, "response.output_item.done", map[string]any{
			"output_index": i + 1,
			"item": map[string]any{
				"type": "function_call", "id": accum.id, "call_id": accum.id,
				"name": accum.name, "arguments": accum.args.String(),
				"status": "completed",
			},
		})
	}
	t.closeMessage(out)

	completed := t.baseResponse("completed")
	completed["output"] = []any{}
	completed["usage"] = map[string]any{
		"input_tokens": 0, "output_tokens": 0, "total_tokens": 0,
	}
	if t.usage != nil {
		completed["usage"] = map[string]any{
			"input_tokens":  t.usage.PromptTokens,
			"output_tokens": t.usage.CompletionTokens,
			"total_tokens":  t.usage.TotalTokens,
		}
	}
	t.frame(out, "response.completed", map[string]any{"response": completed})
	out.WriteString("data: [DONE]\n\n")
}

// toolCallArgumentsString coerces a Tool.Function.Arguments value
// (typed `any` in relaymodel) into the string fragments providers put
// on the wire.
func toolCallArgumentsString(v any) string {
	switch args := v.(type) {
	case string:
		return args
	case nil:
		return ""
	default:
		b, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// ===========================================================================
// gin.ResponseWriter wrapper
// ===========================================================================

// chatToResponsesWriter intercepts everything the chat pipeline writes
// and converts it to Responses format. Non-stream bodies are buffered
// whole and converted in finish(); stream bodies are translated
// incrementally, event by event.
type chatToResponsesWriter struct {
	gin.ResponseWriter // delegates Header()/Status()/Size() etc. to the original

	orig    gin.ResponseWriter
	stream  bool
	model   string
	status  int
	buf     bytes.Buffer // non-stream: whole body; stream: partial line
	pending []byte       // stream: incomplete tail of the last chunk
	tr      *chatToResponsesStreamTranslator
	wrote   bool // finish() already flushed the converted output
}

func newChatToResponsesWriter(orig gin.ResponseWriter, stream bool, model string) *chatToResponsesWriter {
	w := &chatToResponsesWriter{orig: orig, stream: stream, model: model, status: http.StatusOK}
	w.ResponseWriter = orig
	if stream {
		w.tr = newChatToResponsesStreamTranslator(model)
	}
	return w
}

func (w *chatToResponsesWriter) Write(b []byte) (int, error) {
	if w.stream {
		return w.writeStream(b)
	}
	w.buf.Write(b)
	return len(b), nil
}

func (w *chatToResponsesWriter) WriteString(s string) (int, error) {
	if w.stream {
		return w.writeStream([]byte(s))
	}
	w.buf.WriteString(s)
	return len(s), nil
}

func (w *chatToResponsesWriter) WriteHeader(code int) {
	if w.stream {
		w.orig.WriteHeader(code)
		return
	}
	w.status = code
}

func (w *chatToResponsesWriter) WriteHeaderNow() {
	if w.stream {
		w.orig.WriteHeaderNow()
	}
}

func (w *chatToResponsesWriter) Flush() {
	if w.stream {
		w.orig.Flush()
	}
}

// writeStream consumes complete SSE lines, translating each data
// payload; an incomplete tail is kept for the next write.
func (w *chatToResponsesWriter) writeStream(b []byte) (int, error) {
	w.pending = append(w.pending, b...)
	var out bytes.Buffer
	for {
		idx := bytes.IndexByte(w.pending, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSuffix(string(w.pending[:idx]), "\r")
		w.pending = w.pending[idx+1:]
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		w.tr.Feed(strings.TrimPrefix(line, "data: "), &out)
	}
	if out.Len() > 0 {
		w.orig.Write(out.Bytes())
	}
	return len(b), nil
}

// finish flushes the converted output. Called after the chat pipeline
// returns; on error nothing is written (the buffered body belongs to
// the failed attempt and controller.Relay renders the error envelope
// on the restored writer).
func (w *chatToResponsesWriter) finish(failed bool) {
	if failed || w.wrote {
		return
	}
	w.wrote = true
	if w.stream {
		var out bytes.Buffer
		w.tr.finish(&out)
		w.orig.Write(out.Bytes())
		w.orig.Flush()
		return
	}
	// The chat pipeline copies upstream response headers verbatim,
	// including Content-Length for the CHAT body. The converted
	// Responses body has a different length, and net/http suppresses
	// the body entirely when it overruns the declared length — drop it
	// and let the server compute it.
	w.orig.Header().Del("Content-Length")
	body, err := convertChatCompletionToResponses(w.buf.Bytes(), w.model)
	if err != nil {
		// Unparseable chat body: fall back to forwarding it verbatim
		// rather than eating the response.
		w.orig.WriteHeader(w.status)
		w.orig.Write(w.buf.Bytes())
		return
	}
	w.orig.WriteHeader(w.status)
	w.orig.Write(body)
}
