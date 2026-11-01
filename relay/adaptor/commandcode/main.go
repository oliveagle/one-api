package commandcode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/common/render"
	"github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
)

const (
	// DefaultBaseURL is where /alpha/generate lives; a channel may override it
	// (Base URL) but normally doesn't.
	DefaultBaseURL = "https://api.commandcode.ai"
	generatePath   = "/alpha/generate"
)

// finishReasons maps the endpoint's finish reason onto chat completions'.
var finishReasons = map[string]string{
	"tool-calls":     "tool_calls",
	"length":         "length",
	"content-filter": "content_filter",
}

// HandleResponse turns the endpoint's line-delimited event stream into an
// OpenAI reply: SSE chat chunks when the client asked for a stream, one
// aggregated chat.completion when it did not. The upstream is always
// streaming — the endpoint refuses anything else — so the non-stream case is
// built by reading the whole stream.
func HandleResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	defer resp.Body.Close()

	id := "chatcmpl-" + strings.ReplaceAll(sessionID(), "-", "")[:24]
	created := time.Now().Unix()
	modelName := meta.ActualModelName
	if modelName == "" {
		modelName = meta.OriginModelName
	}

	if !meta.IsStream {
		content, reasoning, calls, stop, usage, err := collect(resp.Body)
		if err != nil {
			return nil, err
		}
		message := model.Message{Role: "assistant", Content: content}
		if reasoning != "" {
			message.ReasoningContent = reasoning
		}
		if len(calls) > 0 {
			message.ToolCalls = calls
		}
		reply := openai.TextResponse{
			Id:      id,
			Object:  "chat.completion",
			Created: created,
			Model:   modelName,
			Choices: []openai.TextResponseChoice{{
				Index:        0,
				Message:      message,
				FinishReason: stop,
			}},
			Usage: usage,
		}
		body, merr := json.Marshal(reply)
		if merr != nil {
			return nil, openai.ErrorWrapper(merr, "marshal_response_body_failed", http.StatusInternalServerError)
		}
		c.Writer.Header().Set("Content-Type", "application/json")
		c.Writer.WriteHeader(http.StatusOK)
		if _, werr := c.Writer.Write(body); werr != nil {
			return nil, openai.ErrorWrapper(werr, "copy_response_body_failed", http.StatusInternalServerError)
		}
		return &usage, nil
	}

	return streamResponse(c, resp.Body, id, created, modelName)
}

// streamResponse renders the endpoint's events as chat.completion.chunk SSE.
func streamResponse(c *gin.Context, body io.Reader, id string, created int64, modelName string) (*model.Usage, *model.ErrorWithStatusCode) {
	common.SetEventStreamHeaders(c)

	writeChunk := func(delta model.Message, finishReason *string, usage *model.Usage) {
		chunk := openai.ChatCompletionsStreamResponse{
			Id:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   modelName,
			Choices: []openai.ChatCompletionsStreamResponseChoice{{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			}},
			Usage: usage,
		}
		if err := render.ObjectData(c, chunk); err != nil {
			logger.SysError("commandcode: render chunk: " + err.Error())
		}
	}

	started := false
	toolIndex := 0
	sawTool := false
	var usage model.Usage
	receivedUsage := false
	stop := ""
	failed := false

	readErr := eachEvent(body, func(ev *event) error {
		switch ev.Type {
		case "text-delta":
			if ev.Text == "" {
				return nil
			}
			if !started {
				started = true
				writeChunk(model.Message{Role: "assistant", Content: ""}, nil, nil)
			}
			writeChunk(model.Message{Content: ev.Text}, nil, nil)
		case "reasoning-delta":
			if ev.Text == "" {
				return nil
			}
			if !started {
				started = true
				writeChunk(model.Message{Role: "assistant", Content: ""}, nil, nil)
			}
			writeChunk(model.Message{ReasoningContent: ev.Text}, nil, nil)
		case "tool-call":
			if ev.ProviderExecuted {
				// Run by the server, its result comes beside it — nothing for
				// the client to call.
				return nil
			}
			toolCallID := ev.ToolCallID
			if toolCallID == "" {
				toolCallID = "call_" + strings.ReplaceAll(sessionID(), "-", "")[:24]
			}
			if !started {
				started = true
				writeChunk(model.Message{Role: "assistant", Content: ""}, nil, nil)
			}
			sawTool = true
			writeChunk(model.Message{ToolCalls: []model.Tool{{
				Index: toolIndex,
				Id:    toolCallID,
				Type:  "function",
				Function: model.Function{
					Name:      ev.ToolName,
					Arguments: argumentsJSON(ev.Input),
				},
			}}}, nil, nil)
			toolIndex++
		case "finish":
			if u := toUsage(ev.TotalUsage); u != nil {
				usage, receivedUsage = *u, true
			}
			stop = ev.FinishReason
		case "finish-step":
			if !receivedUsage {
				if u := toUsage(ev.Usage); u != nil {
					usage, receivedUsage = *u, true
				}
			}
		case "abort":
			stop = "stop"
			return errStopIteration
		case "error":
			failed = true
			if !started {
				return &streamAbort{err: streamError(ev)}
			}
			// The client already has content; say so in-band rather than
			// throwing away what it was shown.
			data, _ := json.Marshal(gin.H{"error": gin.H{
				"message": streamError(ev).Error.Message,
				"type":    streamError(ev).Error.Type,
				"code":    streamError(ev).Error.Code,
			}})
			render.StringData(c, string(data))
			return errStopIteration
		}
		return nil
	})

	if abort, ok := readErr.(*streamAbort); ok {
		return nil, abort.err
	}
	if readErr != nil && readErr != errStopIteration {
		logger.SysError("commandcode: read stream: " + readErr.Error())
	}

	finish := stopReason(stop, sawTool)
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	if receivedUsage {
		writeChunk(model.Message{}, &finish, &usage)
	} else {
		writeChunk(model.Message{}, &finish, nil)
	}
	render.Done(c)

	if failed {
		// Reported in-band above; usage is still what the upstream counted.
		return &usage, nil
	}
	return &usage, nil
}

// collect reads the whole event stream into one aggregated reply.
func collect(body io.Reader) (content string, reasoning string, calls []model.Tool, stop string, usage model.Usage, errOut *model.ErrorWithStatusCode) {
	var text, thought strings.Builder
	receivedUsage := false
	sawTool := false

	readErr := eachEvent(body, func(ev *event) error {
		switch ev.Type {
		case "text-delta":
			text.WriteString(ev.Text)
		case "reasoning-delta":
			thought.WriteString(ev.Text)
		case "tool-call":
			if ev.ProviderExecuted {
				return nil
			}
			sawTool = true
			toolCallID := ev.ToolCallID
			if toolCallID == "" {
				toolCallID = "call_" + strings.ReplaceAll(sessionID(), "-", "")[:24]
			}
			calls = append(calls, model.Tool{
				Index: len(calls),
				Id:    toolCallID,
				Type:  "function",
				Function: model.Function{
					Name:      ev.ToolName,
					Arguments: argumentsJSON(ev.Input),
				},
			})
		case "finish":
			if u := toUsage(ev.TotalUsage); u != nil {
				usage, receivedUsage = *u, true
			}
			stop = ev.FinishReason
		case "finish-step":
			if !receivedUsage {
				if u := toUsage(ev.Usage); u != nil {
					usage, receivedUsage = *u, true
				}
			}
		case "abort":
			stop = "stop"
			return errStopIteration
		case "error":
			return &streamAbort{err: streamError(ev)}
		}
		return nil
	})

	if abort, ok := readErr.(*streamAbort); ok {
		return "", "", nil, "", model.Usage{}, abort.err
	}
	if readErr != nil && readErr != errStopIteration {
		return "", "", nil, "", model.Usage{}, openai.ErrorWrapper(readErr, "read_response_body_failed", http.StatusBadGateway)
	}

	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	return text.String(), thought.String(), calls, stopReason(stop, sawTool), usage, nil
}

// stopReason normalises the finish reason, and reports tool_calls when the
// model called a tool but the stream said "stop".
func stopReason(raw string, sawTool bool) string {
	if mapped, ok := finishReasons[raw]; ok {
		return mapped
	}
	switch raw {
	case "length", "content_filter", "stop":
		return raw
	case "":
		if sawTool {
			return "tool_calls"
		}
		return "stop"
	}
	if sawTool {
		return "tool_calls"
	}
	return "stop"
}

// toUsage projects the AI-SDK usage block onto OpenAI's. inputTokens already
// counts cache reads, as prompt_tokens does.
func toUsage(u *genUsage) *model.Usage {
	if u == nil {
		return nil
	}
	return &model.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
		CompletionTokensDetails: &model.CompletionTokensDetails{
			ReasoningTokens: u.OutputTokenDetails.ReasoningTokens,
		},
	}
}

// argumentsJSON renders a tool call's arguments as the JSON string chat
// completions require.
func argumentsJSON(input json.RawMessage) string {
	if len(input) == 0 || string(input) == "null" {
		return "{}"
	}
	return string(input)
}

// eachEvent decodes the reply one JSON object per line. A line that isn't JSON
// is skipped: the stream carries nothing else, so this only guards against
// keep-alive noise.
func eachEvent(body io.Reader, fn func(*event) error) error {
	scanner := bufio.NewScanner(body)
	// A single provider-metadata line can carry a whole routing blob; the
	// default 64KiB token limit truncates it and aborts the scan.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			if !json.Valid([]byte(line)) {
				continue
			}
			logger.SysError("commandcode: skip malformed event: " + err.Error())
			continue
		}
		if err := fn(&ev); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// errStopIteration ends the read loop early (abort / in-band error).
var errStopIteration = fmt.Errorf("stop iteration")

// streamAbort carries an error that must fail the request before anything was
// written to the client (so the relay's failover can still act on it).
type streamAbort struct {
	err *model.ErrorWithStatusCode
}

func (e *streamAbort) Error() string { return e.err.Error.Message }

// streamError normalises an in-stream error: quota problems become 429, a
// model the plan doesn't have 403, anything else 502.
func streamError(ev *event) *model.ErrorWithStatusCode {
	status := http.StatusBadGateway
	message := "the reply ended with an error"
	code := "upstream_error"
	if ev != nil && ev.Error != nil {
		if ev.Error.Message != "" {
			message = ev.Error.Message
		}
		if ev.Error.StatusCode >= 400 && ev.Error.StatusCode <= 599 {
			status = ev.Error.StatusCode
		}
		if ev.Error.Code != "" {
			code = ev.Error.Code
		}
	}
	low := strings.ToLower(message)
	switch {
	case strings.Contains(message, "MODEL_NOT_IN_PLAN"):
		status, code = http.StatusForbidden, "model_not_in_plan"
	case strings.Contains(low, "premium_credits_exhausted"), strings.Contains(low, "insufficient credits"):
		status, code = http.StatusPaymentRequired, "insufficient_quota"
	case strings.Contains(low, "rate_limited"), strings.Contains(low, "usage limit"), strings.Contains(low, "window_limit"):
		status, code = http.StatusTooManyRequests, "rate_limit_exceeded"
	}
	return &model.ErrorWithStatusCode{
		Error:      model.Error{Message: message, Type: "upstream_error", Code: code},
		StatusCode: status,
	}
}

// unsupportedMode refuses relay modes the endpoint cannot serve.
func unsupportedMode(resp *http.Response, mode int) *model.ErrorWithStatusCode {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return openai.ErrorWrapper(fmt.Errorf("%s: relay mode %d is not supported", ChannelName, mode),
		"unsupported_relay_mode", http.StatusBadRequest)
}
