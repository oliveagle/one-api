// Package controller
//
// opencode_responses_convert.go — opencode 专用的 Responses ↔ Chat Completions 协议转换
//
// 背景：cx (codex) 发送 Responses API 请求，但 opencode 只提供 Chat Completions。
// 这个文件专门做 Responses → Chat → Responses 的双向转换，仅用于 opencode channel，
// 不是通用的协议转换层。
//
// 转换逻辑参考 bitx-proxy 的 MoonBit 实现（src/api/convert/openai_convert.mbt），
// 重新用 Go 实现并适配 one-api 的 relay 架构。
package controller

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/model"
)

// SSE data line prefix（与 openai 包保持一致，但定义在 controller 包内避免循环依赖）
const (
	opencodeDataPrefix       = "data: "
	opencodeDataPrefixLen    = len(opencodeDataPrefix)
	opencodeDone             = "[DONE]"
	opencodeMaxStreamLineLen = 1024 * 1024
)

// ============================================================================
// opencode 专用：Responses API → Chat Completions 请求转换
// ============================================================================

// opencodeResponsesToChatRequest 把 Responses API 请求转换为 Chat Completions 请求。
// 仅用于 opencode channel，不做通用处理。
//
// 转换规则（对齐 bitx-proxy openai_convert.mbt to_chat_wire_json）：
//   - instructions → system message (messages[0])
//   - input[] 中的 message → Chat messages
//   - input[] 中的 function_call → assistant tool_calls
//   - input[] 中的 function_call_output → tool message
//   - tools: flat {name,desc,params} → nested {function:{name,desc,params}}
func opencodeResponsesToChatRequest(req *ResponsesRequest) *model.GeneralOpenAIRequest {
	chat := &model.GeneralOpenAIRequest{
		Model:  req.Model,
		Stream: req.Stream,
	}

	// instructions → system message（对齐 bitx: system 是 messages[0]）
	if req.Instructions != "" {
		chat.Messages = append(chat.Messages, model.Message{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	// input[] → messages（对齐 bitx: from_chat_response_json / message_to_wire 的逆向）
	chat.Messages = append(chat.Messages, opencodeConvertInputToMessages(req.Input)...)

	// tools: flat → nested（对齐 bitx: to_wire_tools_json）
	if len(req.Tools) > 0 {
		chat.Tools = make([]model.Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			chat.Tools = append(chat.Tools, model.Tool{
				Type: "function",
				Function: model.Function{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				},
			})
		}
	}

	// Note: bitx-proxy does NOT include tool_choice in the request.
	// The opencode upstream may not support this field.

	// max_output_tokens → max_tokens
	if req.MaxOutput > 0 {
		chat.MaxTokens = req.MaxOutput
	}

	// Note: bitx-proxy does NOT add stream_options. The opencode upstream
	// returns usage in the final chunk without requiring stream_options.
	// Adding it may cause rejection on some providers.

	return chat
}

// opencodeConvertInputToMessages 把 Responses API 的 input 数组转换为 Chat messages。
//
// 对齐 bitx openai_convert.mbt 的消息格式：
//   - string → user message
//   - message(role=user) → user message
//   - message(role=assistant) + content → assistant message
//   - message(role=assistant) + tool_calls → assistant message with tool_calls
//   - function_call → assistant message with tool_calls
//   - function_call_output → tool message
func opencodeConvertInputToMessages(input any) []model.Message {
	if input == nil {
		return nil
	}

	switch v := input.(type) {
	case string:
		return []model.Message{{Role: "user", Content: v}}
	case []any:
		return opencodeConvertInputArray(v)
	default:
		return nil
	}
}

func opencodeConvertInputArray(items []any) []model.Message {
	var msgs []model.Message
	for _, item := range items {
		switch v := item.(type) {
		case string:
			msgs = append(msgs, model.Message{Role: "user", Content: v})
		case map[string]any:
			msgs = append(msgs, opencodeConvertInputItem(v)...)
		}
	}
	return msgs
}

func opencodeConvertInputItem(item map[string]any) []model.Message {
	itemType, _ := item["type"].(string)

	switch itemType {
	case "message":
		return opencodeConvertInputMessage(item)
	case "function_call":
		return opencodeConvertInputFunctionCall(item)
	case "function_call_output":
		return opencodeConvertInputFunctionCallOutput(item)
	default:
		// No "type" field but has "role" — treat as a shorthand message item.
		// The Responses API allows {role, content} without explicit type="message".
		if _, hasRole := item["role"].(string); hasRole {
			return opencodeConvertInputMessage(item)
		}
		return nil
	}
}

// opencodeConvertInputMessage 转换 Responses API 的 message item。
func opencodeConvertInputMessage(item map[string]any) []model.Message {
	role, _ := item["role"].(string)
	if role == "" {
		role = "user"
	}

	content := opencodeExtractContentText(item["content"])

	// bitx-proxy sets content to Json::null() when empty.
	// omitempty would drop "" (empty string), so use nil to produce "content": null.
	var contentAny any
	if content != "" {
		contentAny = content
	}
	msg := model.Message{
		Role:    role,
		Content: contentAny,
	}

	// assistant 消息可能携带 tool_calls
	if role == "assistant" {
		if tcRaw, ok := item["tool_calls"].([]any); ok && len(tcRaw) > 0 {
			msg.ToolCalls = make([]model.Tool, 0, len(tcRaw))
			for _, tc := range tcRaw {
				if tcMap, ok := tc.(map[string]any); ok {
					tool := model.Tool{
						Type: "function",
						Function: model.Function{
							Name:      opencodeString(tcMap, "name"),
							Arguments: opencodeString(tcMap, "arguments"),
						},
					}
					if id, ok := tcMap["id"].(string); ok {
						tool.Id = id
					}
					msg.ToolCalls = append(msg.ToolCalls, tool)
				}
			}
		}
	}

	return []model.Message{msg}
}

// opencodeConvertInputFunctionCall 转换 Responses API 的 function_call item。
func opencodeConvertInputFunctionCall(item map[string]any) []model.Message {
	id, _ := item["id"].(string)
	name, _ := item["name"].(string)
	args, _ := item["arguments"].(string)

	return []model.Message{{
		Role: "assistant",
		ToolCalls: []model.Tool{{
			Id:   id,
			Type: "function",
			Function: model.Function{
				Name:      name,
				Arguments: args,
			},
		}},
	}}
}

// opencodeConvertInputFunctionCallOutput 转换 Responses API 的 function_call_output item。
func opencodeConvertInputFunctionCallOutput(item map[string]any) []model.Message {
	callID, _ := item["call_id"].(string)
	output, _ := item["output"].(string)

	return []model.Message{{
		Role:       "tool",
		Content:    output,
		ToolCallId: callID,
	}}
}

// opencodeExtractContentText 从 Responses API 的 content 数组中提取文本。
func opencodeExtractContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, block := range v {
			if blockMap, ok := block.(map[string]any); ok {
				if text, ok := blockMap["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

// ============================================================================
// opencode 专用：Chat Completions 响应 → Responses API 响应转换（非流式）
// ============================================================================

// opencodeChatToResponsesResponse 把 Chat Completions 非流式响应转换为 Responses API 响应。
func opencodeChatToResponsesResponse(chatResp map[string]any, requestModel string) map[string]any {
	usage := opencodeMapUsage(chatResp["usage"])

	outputItems := []any{}
	status := "completed"
	stopReason := "stop"

	if choices, ok := chatResp["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if fr, ok := choice["finish_reason"].(string); ok {
				stopReason = opencodeMapFinishReason(fr)
			}

			if msg, ok := choice["message"].(map[string]any); ok {
				text := opencodeString(msg, "content")
				if text == "" {
					text = opencodeString(msg, "reasoning")
				}
				if text != "" {
					outputItems = append(outputItems, map[string]any{
						"type": "message",
						"id":   opencodeGenID("msg"),
						"role": "assistant",
						"content": []any{map[string]any{
							"type": "output_text",
							"text": text,
						}},
					})
				}

				// tool_calls → function_call items
				if tcRaw, ok := msg["tool_calls"].([]any); ok {
					for _, tc := range tcRaw {
						if tcMap, ok := tc.(map[string]any); ok {
							fn, _ := tcMap["function"].(map[string]any)
							outputItems = append(outputItems, map[string]any{
								"type":      "function_call",
								"id":        opencodeGenID("fc"),
								"call_id":   opencodeString(tcMap, "id"),
								"name":      opencodeString(fn, "name"),
								"arguments": opencodeString(fn, "arguments"),
							})
						}
					}
				}
			}
		}
	}

	return map[string]any{
		"id":          opencodeGenID("resp"),
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"model":       requestModel,
		"status":      status,
		"output":      outputItems,
		"usage":       usage,
		"stop_reason": stopReason,
	}
}

// ============================================================================
// opencode 专用：Chat Completions SSE stream → Responses API SSE stream 转换
// ============================================================================

// opencodeChatStreamToResponsesStream 把 Chat 流式 SSE 行转换为 Responses API 事件。
func opencodeChatStreamToResponsesStream(chatLine string, streamState *opencodeStreamState) []string {
	if len(chatLine) < opencodeDataPrefixLen || chatLine[:opencodeDataPrefixLen] != opencodeDataPrefix {
		return nil
	}

	payload := strings.TrimSpace(chatLine[opencodeDataPrefixLen:])
	if payload == opencodeDone || payload == "" {
		return nil
	}

	var chunk openai.ChatCompletionsStreamResponse
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil
	}

	var events []string

	// 首个 chunk: response.created
	if !streamState.started {
		streamState.started = true
		streamState.respId = opencodeGenID("resp")
		events = append(events, opencodeSSEEvent("response.created", map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id":         streamState.respId,
				"object":     "response",
				"created_at": time.Now().Unix(),
				"model":      streamState.model,
				"status":     "in_progress",
				"output":     []any{},
			},
		}))
		events = append(events, opencodeSSEEvent("response.in_progress", map[string]any{
			"type": "response.in_progress",
			"response": map[string]any{
				"id":         streamState.respId,
				"object":     "response",
				"created_at": time.Now().Unix(),
				"model":      streamState.model,
				"status":     "in_progress",
				"output":     []any{},
			},
		}))
	}

	if len(chunk.Choices) == 0 && chunk.Usage == nil {
		return events
	}

	for _, choice := range chunk.Choices {
		delta := choice.Delta

		text := opencodeStringFromAny(delta.Content)
		if text == "" {
			text = opencodeStringFromAny(delta.Reasoning)
		}

		if text != "" {
			streamState.textContent += text
			if !streamState.hasTextItem {
				streamState.hasTextItem = true
				streamState.textItemId = opencodeGenID("msg")
				events = append(events, opencodeSSEEvent("response.output_item.added", map[string]any{
					"type":         "response.output_item.added",
					"output_index": 0,
					"item": map[string]any{
						"type":    "message",
						"id":      streamState.textItemId,
						"status":  "in_progress",
						"role":    "assistant",
						"content": []any{},
					},
				}))
				events = append(events, opencodeSSEEvent("response.content_part.added", map[string]any{
					"type":          "response.content_part.added",
					"item_id":       streamState.textItemId,
					"output_index":  0,
					"content_index": 0,
					"part": map[string]any{
						"type":        "output_text",
						"text":        "",
						"annotations": []any{},
					},
				}))
			}
			events = append(events, opencodeSSEEvent("response.output_text.delta", map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       streamState.textItemId,
				"output_index":  0,
				"content_index": 0,
				"delta":         text,
			}))
		}

		// tool_calls delta → per-index function_call items.
		//
		// Chat models may emit several tool calls in one turn (parallel tool
		// calls), interleaving their argument deltas across chunks. Each delta
		// carries the tool-call `index`; tracking state per index keeps every
		// call's id/name/arguments separate instead of concatenating them into
		// a single, invalid function_call.
		if delta.ToolCalls != nil {
			for _, tc := range delta.ToolCalls {
				st := opencodeToolStateFor(streamState, tc)
				if tc.Id != "" && st.callId == "" {
					st.callId = tc.Id
				}
				if tc.Function.Name != "" && st.name == "" {
					st.name = tc.Function.Name
				}

				if !st.added && (st.callId != "" || st.name != "") {
					st.added = true
					events = append(events, opencodeSSEEvent("response.output_item.added", map[string]any{
						"type":         "response.output_item.added",
						"output_index": st.outputIndex,
						"item": map[string]any{
							"type":      "function_call",
							"id":        st.itemId,
							"status":    "in_progress",
							"call_id":   st.callId,
							"name":      st.name,
							"arguments": st.args,
						},
					}))
				}

				if argDelta := opencodeToolArgsString(tc.Function.Arguments); argDelta != "" {
					st.args += argDelta
					events = append(events, opencodeSSEEvent("response.function_call_arguments.delta", map[string]any{
						"type":         "response.function_call_arguments.delta",
						"item_id":      st.itemId,
						"output_index": st.outputIndex,
						"call_id":      st.callId,
						"delta":        argDelta,
						"arguments":    argDelta,
					}))
				}
			}
		}

		// finish_reason → completed
		if choice.FinishReason != nil && !streamState.completed {
			streamState.completed = true
			if streamState.hasTextItem {
				events = append(events, opencodeSSEEvent("response.output_text.done", map[string]any{
					"type":          "response.output_text.done",
					"item_id":       streamState.textItemId,
					"output_index":  0,
					"content_index": 0,
					"text":          streamState.textContent,
				}))
				events = append(events, opencodeSSEEvent("response.content_part.done", map[string]any{
					"type":          "response.content_part.done",
					"item_id":       streamState.textItemId,
					"output_index":  0,
					"content_index": 0,
					"part": map[string]any{
						"type":        "output_text",
						"text":        streamState.textContent,
						"annotations": []any{},
					},
				}))
				events = append(events, opencodeSSEEvent("response.output_item.done", map[string]any{
					"type":         "response.output_item.done",
					"output_index": 0,
					"item": map[string]any{
						"type":   "message",
						"id":     streamState.textItemId,
						"status": "completed",
						"role":   "assistant",
						"content": []any{map[string]any{
							"type": "output_text",
							"text": streamState.textContent,
						}},
					},
				}))
			}
			// Close every open function_call item (there may be several).
			for _, key := range streamState.toolOrder {
				st := streamState.toolCalls[key]
				if st.done {
					continue
				}
				st.done = true
				events = append(events, opencodeSSEEvent("response.output_item.done", map[string]any{
					"type":         "response.output_item.done",
					"output_index": st.outputIndex,
					"item":         st.outputItem("completed"),
				}))
			}

			var usageData any
			if chunk.Usage != nil {
				usageData = opencodeMapUsage(chunk.Usage)
			} else {
				usageData = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
			}

			events = append(events, opencodeSSEEvent("response.completed", map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":         streamState.respId,
					"object":     "response",
					"created_at": time.Now().Unix(),
					"model":      streamState.model,
					"status":     "completed",
					"output":     opencodeBuildOutput(streamState),
					"usage":      usageData,
				},
			}))
		}
	}

	return events
}

// ============================================================================
// 共用辅助函数
// ============================================================================

// opencodeStreamState 维护 opencode 流式转换的状态。
type opencodeStreamState struct {
	started     bool
	completed   bool
	respId      string
	model       string
	hasTextItem bool
	textItemId  string
	textContent string

	// Parallel tool calls: one state per upstream tool-call index. Chat
	// providers interleave the argument deltas of concurrent calls, so state
	// must be keyed by index rather than shared across the whole stream.
	toolCalls map[int]*opencodeToolCallState
	// toolOrder preserves first-seen order so output is deterministic and the
	// emitted output_index values line up with the final response.output array.
	toolOrder []int
	// nextOutputIndex is the next output_index handed to a new item. Index 0 is
	// reserved for the assistant text message, so this starts at 1.
	nextOutputIndex int
	// nextSyntheticIndex supplies unique keys for upstreams that reuse index 0
	// for every call instead of incrementing it.
	nextSyntheticIndex int
}

// opencodeToolCallState accumulates one streamed function_call.
type opencodeToolCallState struct {
	itemId      string
	callId      string
	name        string
	args        string
	outputIndex int
	added       bool
	done        bool
}

// outputItem renders the function_call item for output_item.*/response.output.
func (st *opencodeToolCallState) outputItem(status string) map[string]any {
	return map[string]any{
		"type":      "function_call",
		"id":        st.itemId,
		"status":    status,
		"call_id":   st.callId,
		"name":      st.name,
		"arguments": st.args,
	}
}

// opencodeToolStateFor resolves (and lazily creates) the state for one streaming
// tool-call delta. Providers normally increment `index`, but some send every
// call as index 0; when the same slot is reused for a call that already has a
// different name, treat the delta as a brand new tool call.
func opencodeToolStateFor(s *opencodeStreamState, tc model.Tool) *opencodeToolCallState {
	if s.toolCalls == nil {
		s.toolCalls = map[int]*opencodeToolCallState{}
	}

	key := tc.Index
	if st, ok := s.toolCalls[key]; ok {
		if tc.Function.Name != "" && st.name != "" && st.name != tc.Function.Name {
			key = s.nextSyntheticIndex
			s.nextSyntheticIndex++
		}
	}

	st, ok := s.toolCalls[key]
	if !ok {
		st = &opencodeToolCallState{
			itemId:      opencodeGenID("fc"),
			outputIndex: s.nextOutputIndex,
		}
		s.nextOutputIndex++
		s.toolCalls[key] = st
		s.toolOrder = append(s.toolOrder, key)
	}
	return st
}

// opencodeBuildOutput assembles the response.output array from stream state:
// the assistant text message first (output_index 0), then each function_call.
func opencodeBuildOutput(s *opencodeStreamState) []any {
	var items []any
	if s.hasTextItem {
		items = append(items, map[string]any{
			"type":   "message",
			"id":     s.textItemId,
			"status": "completed",
			"role":   "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        s.textContent,
				"annotations": []any{},
			}},
		})
	}
	for _, key := range s.toolOrder {
		items = append(items, s.toolCalls[key].outputItem("completed"))
	}
	if items == nil {
		items = []any{}
	}
	return items
}

// opencodeToolArgsString normalizes a streamed tool_call arguments value to a
// string. Compliant providers send a partial JSON string; a few send an object,
// which is re-marshaled so it is not silently dropped.
func opencodeToolArgsString(v any) string {
	switch args := v.(type) {
	case nil:
		return ""
	case string:
		return args
	default:
		b, err := json.Marshal(args)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// opencodeGenID 生成 Responses API 风格的 ID。
func opencodeGenID(prefix string) string {
	return fmt.Sprintf("%s_%d_%06d", prefix, time.Now().UnixMilli(), rand.Intn(1000000))
}

// opencodeMapFinishReason 把 Chat finish_reason 映射为 Responses stop_reason。
func opencodeMapFinishReason(chatReason string) string {
	switch chatReason {
	case "stop":
		return "stop"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		if chatReason != "" {
			return chatReason
		}
		return "stop"
	}
}

// opencodeMapUsage 把 Chat usage 映射为 Responses usage。
// Handles both *model.Usage (from streaming) and map[string]any (from raw JSON).
func opencodeMapUsage(usage any) map[string]any {
	if usage == nil {
		return map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		}
	}
	if u, ok := usage.(*model.Usage); ok && u != nil {
		return map[string]any{
			"input_tokens":  u.PromptTokens,
			"output_tokens": u.CompletionTokens,
			"total_tokens":  u.TotalTokens,
		}
	}
	if m, ok := usage.(map[string]any); ok {
		result := map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
			"total_tokens":  0,
		}
		// Chat Completions upstream returns prompt_tokens/completion_tokens
		if v, ok := m["prompt_tokens"].(float64); ok {
			result["input_tokens"] = int(v)
		}
		if v, ok := m["completion_tokens"].(float64); ok {
			result["output_tokens"] = int(v)
		}
		if v, ok := m["total_tokens"].(float64); ok {
			result["total_tokens"] = int(v)
		}
		return result
	}
	return map[string]any{
		"input_tokens":  0,
		"output_tokens": 0,
		"total_tokens":  0,
	}
}

// opencodeSSEEvent 生成 Responses API SSE 行。
func opencodeSSEEvent(eventName string, data any) string {
	jsonBytes, _ := json.Marshal(data)
	return fmt.Sprintf("data: %s\n\n", string(jsonBytes))
}

// opencodeString 从 map 中安全取 string 值。
func opencodeString(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// opencodeStringFromAny 从 any 中提取 string。
func opencodeStringFromAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
