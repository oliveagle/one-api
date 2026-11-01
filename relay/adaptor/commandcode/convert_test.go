package commandcode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/relay/model"
)

func strptr(s string) *string { return &s }

func TestConvertRequest_BasicText(t *testing.T) {
	req := &model.GeneralOpenAIRequest{
		Model: "deepseek/deepseek-v4.1-flash",
		Messages: []model.Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "say PONG"},
		},
		MaxTokens:   64,
		Temperature: func() *float64 { v := 0.3; return &v }(),
	}
	out, err := ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	// The endpoint refuses a non-streaming call ("Proxy use detected"), so the
	// adaptor must always ask for a stream even when the client didn't.
	if !out.Params.Stream {
		t.Fatal("params.stream must always be true")
	}
	if out.Params.System != "be terse" {
		t.Errorf("system = %q, want %q (hoisted out of messages)", out.Params.System, "be terse")
	}
	if len(out.Params.Messages) != 1 || out.Params.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user turn", out.Params.Messages)
	}
	if got := out.Params.Messages[0].Content[0]; got.Type != "text" || got.Text != "say PONG" {
		t.Errorf("content part = %+v, want text/say PONG", got)
	}
	if out.Params.MaxTokens != 64 {
		t.Errorf("max_tokens = %d, want 64", out.Params.MaxTokens)
	}
	if out.Params.Temperature == nil || *out.Params.Temperature != 0.3 {
		t.Errorf("temperature = %v, want 0.3", out.Params.Temperature)
	}
	// The config block's fields are all validated by the upstream (a missing
	// currentBranch/mainBranch/gitStatus/recentCommits is a 400), so they must
	// always be present — checking the wire form is the only way to see it.
	raw := marshalGenerateBody(out)
	for _, key := range []string{`"currentBranch"`, `"mainBranch"`, `"gitStatus"`, `"recentCommits"`, `"structure"`, `"permissionMode"`, `"memory"`, `"taste"`, `"skills"`, `"tools"`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("wire body is missing %s: %s", key, raw)
		}
	}
}

func TestConvertRequest_DefaultsAndClamps(t *testing.T) {
	out, err := ConvertRequest(&model.GeneralOpenAIRequest{
		Model:    "m",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if out.Params.MaxTokens != defaultMaxTokens {
		t.Errorf("max_tokens = %d, want the CLI default %d", out.Params.MaxTokens, defaultMaxTokens)
	}

	// More than 200k fails the whole request upstream ("Too big: expected
	// number to be <=200000"), so it is clamped instead of forwarded.
	big := 300000
	out, err = ConvertRequest(&model.GeneralOpenAIRequest{
		Model:               "m",
		Messages:            []model.Message{{Role: "user", Content: "hi"}},
		MaxCompletionTokens: &big,
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if out.Params.MaxTokens != maxOutputTokens {
		t.Errorf("max_tokens = %d, want clamp to %d", out.Params.MaxTokens, maxOutputTokens)
	}
}

func TestConvertRequest_ToolRoundTrip(t *testing.T) {
	req := &model.GeneralOpenAIRequest{
		Model: "m",
		Messages: []model.Message{
			{Role: "user", Content: "weather in Beijing?"},
			{Role: "assistant", ToolCalls: []model.Tool{{
				Id: "call_1", Type: "function",
				Function: model.Function{Name: "get_weather", Arguments: `{"city":"Beijing"}`},
			}}},
			{Role: "tool", ToolCallId: "call_1", Content: "20C"},
			{Role: "user", Content: "thanks"},
		},
		Tools: []model.Tool{{
			Type: "function",
			Function: model.Function{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			},
		}},
		ToolChoice: "required",
	}
	out, err := ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}

	// Tool declarations are flat {name, description, input_schema}, not
	// OpenAI's nested function object.
	if len(out.Params.Tools) != 1 {
		t.Fatalf("tools = %+v, want 1", out.Params.Tools)
	}
	if out.Params.Tools[0].Name != "get_weather" || out.Params.Tools[0].InputSchema == nil {
		t.Errorf("tool = %+v, want name + input_schema", out.Params.Tools[0])
	}
	// "required" is not an accepted object type upstream; it goes as "any".
	if out.Params.ToolChoice == nil || out.Params.ToolChoice.Type != "any" {
		t.Errorf("tool_choice = %+v, want {type:any}", out.Params.ToolChoice)
	}

	// assistant tool-call keeps the id and the parsed input.
	asst := out.Params.Messages[1]
	if asst.Role != "assistant" || len(asst.Content) != 1 || asst.Content[0].Type != "tool-call" {
		t.Fatalf("assistant turn = %+v, want one tool-call part", asst)
	}
	if asst.Content[0].ToolCallID != "call_1" || asst.Content[0].ToolName != "get_weather" {
		t.Errorf("tool-call part = %+v", asst.Content[0])
	}
	if in, ok := asst.Content[0].Input.(map[string]any); !ok || in["city"] != "Beijing" {
		t.Errorf("tool-call input = %#v, want parsed object", asst.Content[0].Input)
	}

	// Tool results are batched into ONE "tool" message that names the tool —
	// OpenAI's own shape doesn't repeat the name, so the adaptor has to look
	// it up from the earlier assistant turn.
	if len(out.Params.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(out.Params.Messages))
	}
	turn := out.Params.Messages[2]
	if turn.Role != "tool" {
		t.Fatalf("turn 2 role = %q, want tool", turn.Role)
	}
	part := turn.Content[0]
	if part.Type != "tool-result" || part.ToolCallID != "call_1" || part.ToolName != "get_weather" {
		t.Errorf("tool-result part = %+v", part)
	}
	if part.Output == nil || part.Output.Value != "20C" {
		t.Errorf("tool-result output = %+v, want 20C", part.Output)
	}
}

func TestConvertRequest_ToolChoiceNoneDropsTools(t *testing.T) {
	// The endpoint rejects {"type":"none"} outright, so "none" can only be
	// expressed by not declaring any tools.
	out, err := ConvertRequest(&model.GeneralOpenAIRequest{
		Model:      "m",
		Messages:   []model.Message{{Role: "user", Content: "hi"}},
		Tools:      []model.Tool{{Type: "function", Function: model.Function{Name: "t"}}},
		ToolChoice: "none",
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if len(out.Params.Tools) != 0 {
		t.Errorf("tools = %+v, want none for tool_choice=none", out.Params.Tools)
	}
	if out.Params.ToolChoice != nil {
		t.Errorf("tool_choice = %+v, want omitted", out.Params.ToolChoice)
	}
}

func TestConvertRequest_ReasoningHistoryAndEffort(t *testing.T) {
	req := &model.GeneralOpenAIRequest{
		Model:           "m",
		ReasoningEffort: strptr("high"),
		Messages: []model.Message{
			{Role: "user", Content: "17*23?"},
			{Role: "assistant", ReasoningContent: "17*20=340, 17*3=51", Content: "391"},
		},
	}
	out, err := ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	if out.Params.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high", out.Params.ReasoningEffort)
	}
	asst := out.Params.Messages[1]
	if len(asst.Content) != 2 || asst.Content[0].Type != "reasoning" || asst.Content[1].Type != "text" {
		t.Fatalf("assistant parts = %+v, want reasoning then text", asst.Content)
	}
}

func TestConvertRequest_Images(t *testing.T) {
	req := &model.GeneralOpenAIRequest{
		Model: "m",
		Messages: []model.Message{{Role: "user", Content: []any{
			map[string]any{"type": "text", "text": "what is this?"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAA"}},
		}}},
	}
	out, err := ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	parts := out.Params.Messages[0].Content
	if len(parts) != 2 || parts[1].Type != "image" || parts[1].MimeType != "image/png" {
		t.Fatalf("parts = %+v, want text + image with mimeType", parts)
	}
}

func TestConvertRequest_NilRequest(t *testing.T) {
	if _, err := ConvertRequest(nil); err == nil {
		t.Fatal("expected an error for a nil request")
	}
}

// The wire body must never carry OpenAI-only fields the endpoint's schema
// rejects; this pins the exact envelope shape against the CLI's.
func TestConvertRequest_WireEnvelope(t *testing.T) {
	out, err := ConvertRequest(&model.GeneralOpenAIRequest{
		Model:     "deepseek/deepseek-v4.1-flash",
		Messages:  []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 16,
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(marshalGenerateBody(out), &wire); err != nil {
		t.Fatalf("wire body is not JSON: %v", err)
	}
	params, ok := wire["params"].(map[string]any)
	if !ok {
		t.Fatalf("params missing: %v", wire)
	}
	if params["stream"] != true {
		t.Errorf("params.stream = %v, want true", params["stream"])
	}
	for _, key := range []string{"model", "messages", "tools", "system", "max_tokens"} {
		if _, ok := params[key]; !ok {
			t.Errorf("params.%s missing", key)
		}
	}
	if _, ok := wire["permissionMode"]; !ok {
		t.Error("permissionMode missing")
	}
	cfg, ok := wire["config"].(map[string]any)
	if !ok {
		t.Fatalf("config missing: %v", wire)
	}
	if cfg["isGitRepo"] != false {
		t.Errorf("config.isGitRepo = %v, want false", cfg["isGitRepo"])
	}
}
