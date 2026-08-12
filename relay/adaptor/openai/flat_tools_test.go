package openai

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/relay/model"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// TestConvertRequest_FlattenToolsForOpencode verifies the OpenAI adaptor
// rewrites tools into the flat shape ({"name":..., "parameters":...})
// for upstreams that reject the standard OpenAI Chat Completions
// tools schema. The detection is URL-based: any opencode.ai endpoint.
func TestConvertRequest_FlattenToolsForOpencode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &Adaptor{ChannelType: 50}

	tools := []relaymodel.Tool{{
		Type: "function",
		Function: relaymodel.Function{
			Name:        "f1",
			Description: "d",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}}
	req := &model.GeneralOpenAIRequest{
		Model: "deepseek-v4-flash",
		Tools: tools,
	}

	for _, tc := range []struct {
		name     string
		baseURL  string
		wantFlat bool
	}{
		{"opencode-go uses flat", "https://opencode.ai/zen/go/v1", true},
		{"zen variant", "https://opencode.ai/zen/v1", true},
		{"openrouter keeps standard", "https://openrouter.ai/api/v1", false},
		{"empty URL keeps standard", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			c.Set(ctxkey.BaseURL, tc.baseURL)

			out, err := a.ConvertRequest(c, 0, req)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			if tc.wantFlat {
				_, isShim := out.(struct {
					*model.GeneralOpenAIRequest
					Tools []map[string]any `json:"tools,omitempty"`
				})
				if !isShim {
					t.Fatalf("expected flat-tool shim type, got %T", out)
				}
			} else {
				if out != req {
					t.Errorf("expected passthrough (same pointer), got %T", out)
				}
			}
		})
	}
}

func TestConvertRequest_FlattenToolsDropsNameless(t *testing.T) {
	// If a tool is missing Function.Name we can't synthesise one, so the
	// flattener drops it. The request must still go through cleanly.
	gin.SetMode(gin.TestMode)
	a := &Adaptor{ChannelType: 50}

	req := &model.GeneralOpenAIRequest{
		Model: "deepseek-v4-flash",
		Tools: []relaymodel.Tool{
			{Type: "function", Function: relaymodel.Function{Name: "ok", Description: "good"}},
			{Type: "function", Function: relaymodel.Function{Name: "", Description: "no-name"}},
		},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(ctxkey.BaseURL, "https://opencode.ai/zen/go/v1")

	out, err := a.ConvertRequest(c, 0, req)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	shim, ok := out.(struct {
		*model.GeneralOpenAIRequest
		Tools []map[string]any `json:"tools,omitempty"`
	})
	if !ok {
		t.Fatalf("expected shim, got %T", out)
	}
	if len(shim.Tools) != 1 {
		t.Errorf("expected 1 tool after dropping nameless, got %d", len(shim.Tools))
	}
	if name, _ := shim.Tools[0]["name"].(string); name != "ok" {
		t.Errorf("expected tool name 'ok', got %v", shim.Tools[0]["name"])
	}
}
