package commandcode

import (
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/relay/model"
)

// defaultMaxTokens is the CLI's own default when the client asks for none.
const defaultMaxTokens = 64000

// maxOutputTokens is the most /alpha/generate takes for any model; asking for
// more fails the WHOLE request ("Too big: expected number to be <=200000 at
// params.max_tokens"), so it is clamped rather than passed through.
const maxOutputTokens = 200_000

// ConvertRequest rewrites an OpenAI chat request as /alpha/generate's body.
//
// The endpoint only serves what looks like the CLI's own call: stream must be
// true (a non-streaming request is refused as "Proxy use detected"), so the
// flag is forced on here and the caller aggregates the reply instead.
func ConvertRequest(request *model.GeneralOpenAIRequest) (*GenerateRequest, error) {
	if request == nil {
		return nil, fmt.Errorf("request is nil")
	}

	var system []string
	out := make([]GenerateMessage, 0, len(request.Messages))
	// names maps a tool call id back to its tool: a tool result names the tool
	// it answers, and OpenAI's shape does not say so on the result itself.
	names := map[string]string{}
	var results []GeneratePart
	flushResults := func() {
		if len(results) > 0 {
			out = append(out, GenerateMessage{Role: "tool", Content: results})
			results = nil
		}
	}

	for _, m := range request.Messages {
		switch m.Role {
		case "system", "developer":
			if text := m.StringContent(); text != "" {
				system = append(system, text)
			}
			continue
		case "tool":
			results = append(results, GeneratePart{
				Type:       "tool-result",
				ToolCallID: m.ToolCallId,
				ToolName:   toolNameOf(names, m.ToolCallId),
				Output:     &ToolResultBody{Type: "text", Value: m.StringContent()},
			})
			continue
		}
		flushResults()

		if m.Role == "assistant" {
			parts := make([]GeneratePart, 0, len(m.ToolCalls)+2)
			if thought := reasoningOf(m); thought != "" {
				parts = append(parts, GeneratePart{Type: "reasoning", Text: thought})
			}
			if text := m.StringContent(); text != "" {
				parts = append(parts, GeneratePart{Type: "text", Text: text})
			}
			for _, call := range m.ToolCalls {
				names[call.Id] = call.Function.Name
				parts = append(parts, GeneratePart{
					Type:       "tool-call",
					ToolCallID: call.Id,
					ToolName:   call.Function.Name,
					Input:      parseArguments(call.Function.Arguments),
				})
			}
			if len(parts) > 0 {
				out = append(out, GenerateMessage{Role: "assistant", Content: parts})
			}
			continue
		}

		parts := contentParts(m)
		if len(parts) > 0 {
			out = append(out, GenerateMessage{Role: "user", Content: parts})
		}
	}
	flushResults()

	tools := convertTools(request.Tools)
	// tool_choice "none": the endpoint has no "none" type (it rejects the
	// object outright), so the only way to say it is to declare no tools.
	if choice, ok := request.ToolChoice.(string); ok && choice == "none" {
		tools = nil
	}

	params := GenerateParams{
		Model:     request.Model,
		Messages:  out,
		Tools:     tools,
		System:    strings.Join(system, "\n\n"),
		MaxTokens: clampMaxTokens(request),
		// Forced: the endpoint refuses a non-streaming call.
		Stream:      true,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stop:        request.Stop,
		ToolChoice:  convertToolChoice(request.ToolChoice),
	}
	if request.ReasoningEffort != nil {
		params.ReasoningEffort = *request.ReasoningEffort
	}

	return &GenerateRequest{
		Config: GenerateConfig{
			WorkingDir:    "/",
			Date:          time.Now().Format("2006-01-02"),
			Environment:   runtime.GOOS,
			Structure:     []any{},
			IsGitRepo:     false,
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []string{},
		},
		Memory:         nil,
		Taste:          nil,
		Skills:         nil,
		PermissionMode: "standard",
		Params:         params,
	}, nil
}

func clampMaxTokens(request *model.GeneralOpenAIRequest) int {
	maxTokens := request.MaxTokens
	if request.MaxCompletionTokens != nil && *request.MaxCompletionTokens > 0 {
		maxTokens = *request.MaxCompletionTokens
	}
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	if maxTokens > maxOutputTokens {
		maxTokens = maxOutputTokens
	}
	return maxTokens
}

// contentParts projects an OpenAI content value (string or part list) onto the
// endpoint's typed parts.
func contentParts(m model.Message) []GeneratePart {
	if text, ok := m.Content.(string); ok {
		if text == "" {
			return nil
		}
		return []GeneratePart{{Type: "text", Text: text}}
	}
	parts := make([]GeneratePart, 0, 4)
	for _, part := range m.ParseContent() {
		switch part.Type {
		case model.ContentTypeText:
			if part.Text != "" {
				parts = append(parts, GeneratePart{Type: "text", Text: part.Text})
			}
		case model.ContentTypeImageURL:
			if part.ImageURL == nil || part.ImageURL.Url == "" {
				continue
			}
			p := GeneratePart{Type: "image", Image: part.ImageURL.Url}
			if mt := dataURLMediaType(part.ImageURL.Url); mt != "" {
				p.MimeType = mt
			}
			parts = append(parts, p)
		}
	}
	return parts
}

var dataURLRe = regexp.MustCompile(`^data:([^;,]+)[;,]`)

func dataURLMediaType(url string) string {
	if m := dataURLRe.FindStringSubmatch(url); len(m) == 2 {
		return m[1]
	}
	return ""
}

// reasoningOf pulls an assistant turn's thinking back out. Chat clients that
// round-trip reasoning send it as "reasoning_content" (DeepSeek) or
// "reasoning" (OpenRouter); the endpoint wants it as a reasoning part.
func reasoningOf(m model.Message) string {
	for _, v := range []any{m.ReasoningContent, m.Reasoning} {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toolNameOf(names map[string]string, callID string) string {
	if name, ok := names[callID]; ok && name != "" {
		return name
	}
	return "unknown"
}

// parseArguments decodes a tool call's arguments. The spec says JSON in a
// string; some clients (codex-cli's chat emitter) send the object itself, and
// a malformed string must not fail the whole request.
func parseArguments(arguments any) any {
	switch v := arguments.(type) {
	case nil:
		return map[string]any{}
	case string:
		if strings.TrimSpace(v) == "" {
			return map[string]any{}
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(v), &out); err != nil {
			return map[string]any{}
		}
		return out
	default:
		return v
	}
}

// convertTools flattens OpenAI's nested tool declarations into the endpoint's
// {name, description, input_schema} shape.
func convertTools(tools []model.Tool) []GenerateTool {
	out := make([]GenerateTool, 0, len(tools))
	for _, t := range tools {
		if t.Function.Name == "" {
			continue
		}
		schema := t.Function.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, GenerateTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: schema,
		})
	}
	return out
}

// convertToolChoice maps OpenAI's tool_choice onto the only shape the endpoint
// accepts (an object). "required" becomes "any"; "none" is handled by dropping
// the tool declarations entirely (see ConvertRequest).
func convertToolChoice(choice any) *GenerateToolChoice {
	switch v := choice.(type) {
	case string:
		switch v {
		case "auto":
			return &GenerateToolChoice{Type: "auto"}
		case "required", "any":
			return &GenerateToolChoice{Type: "any"}
		}
		return nil
	case map[string]any:
		// {"type":"function","function":{"name":"x"}} or {"type":"tool","name":"x"}
		if name, ok := v["name"].(string); ok && name != "" {
			return &GenerateToolChoice{Type: "tool", Name: name}
		}
		if fn, ok := v["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok && name != "" {
				return &GenerateToolChoice{Type: "tool", Name: name}
			}
		}
		if t, ok := v["type"].(string); ok {
			switch t {
			case "auto", "any", "required":
				if t == "required" {
					t = "any"
				}
				return &GenerateToolChoice{Type: t}
			}
		}
	}
	return nil
}
