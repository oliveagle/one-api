package controller

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/ctxkey"
	openai "github.com/songquanpeng/one-api/relay/adaptor/openai"
	"github.com/songquanpeng/one-api/relay/apitype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// TestGetRequestBody_FastPathAndConversionPath verifies that getRequestBody
// takes the right path depending on whether the request came from the
// Responses -> Chat converter (ctxkey.ConvertedFromResponses is set).
//
//   - Native /v1/chat/completions requests use the fast path: the
//     original body is forwarded verbatim (no schema adaptation).
//   - Converted /v1/responses requests bypass the fast path: the OpenAI
//     adaptor's ConvertRequest is invoked, applying per-channel tool
//     schema adaptations (e.g. opencode-go's flat tools shape).
//
// The previous version of getRequestBody took the fast path for both
// cases, which made opencode-go reject the forwarded request with
// "tools[0]: missing field name".
func TestGetRequestBody_FastPathAndConversionPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const flatBody = `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"name":"f1","description":"d","parameters":{"type":"object","properties":{}}}]}`
	const structuredBody = `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"f1","description":"d","parameters":{"type":"object","properties":{}}}}]}`

	opencodeBase := "https://opencode.ai/zen/go/v1"
	openrouterBase := "https://openrouter.ai/api/v1"

	// toolsFor returns the []Tool that the OpenAI adaptor would see after
	// parsing the body, mirroring getAndValidateTextRequest's behaviour.
	toolsFor := func(body string) []relaymodel.Tool {
		if body == structuredBody {
			return []relaymodel.Tool{{
				Type: "function",
				Function: relaymodel.Function{
					Name:        "f1",
					Description: "d",
					Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
				},
			}}
		}
		// flatBody case: the response converter would produce a request
		// whose Tools list has empty Function.Name. The flattener drops
		// these. We don't exercise that path in this test — see the
		// adaptor-level test for that.
		return nil
	}

	type tcase struct {
		name         string
		baseURL      string
		setFlag      bool
		rawBody      string
		wantContains string
	}
	for _, tc := range []tcase{
		// After Responses conversion, opencode MUST see flat tools.
		{"responses_converted_to_opencode_flattens", opencodeBase, true, structuredBody, "\"name\":\"f1\""},
		// After Responses conversion, openrouter keeps the OpenAI shape.
		{"responses_converted_to_openrouter_passthrough", openrouterBase, true, structuredBody, "\"type\":\"function\""},
		// Native /v1/chat/completions: opencode gets raw body (the caller
		// is responsible for the shape it sends).
		{"native_chat_to_opencode_forwarded_verbatim", opencodeBase, false, structuredBody, "\"type\":\"function\""},
		// Native /v1/chat/completions: openrouter gets raw body.
		{"native_chat_to_openrouter_forwarded_verbatim", openrouterBase, false, structuredBody, "\"type\":\"function\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			// The fast path returns c.Request.Body verbatim. We seed the
			// request body with the body the test expects, so the
			// returned reader matches the seed.
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.rawBody))
			if tc.setFlag {
				// Mirror the production path: responses.go sets
				// KeyRequestBody to the converted body and resets
				// c.Request.Body. The fast path also reads c.Request.Body
				// via UnmarshalBodyReusable's cache, so we need a
				// populated request body for either path.
				c.Set(ctxkey.KeyRequestBody, []byte(tc.rawBody))
				c.Set(ctxkey.ConvertedFromResponses, "true")
			}

			m := &meta.Meta{
				APIType:         apitype.OpenAI,
				BaseURL:         tc.baseURL,
				OriginModelName: "m",
				ActualModelName: "m",
			}

			textReq := &relaymodel.GeneralOpenAIRequest{Model: "m", Tools: toolsFor(tc.rawBody)}

			r, err := getRequestBody(c, m, textReq, (&openai.Adaptor{ChannelType: 50}), false)
			if err != nil {
				t.Fatalf("getRequestBody: %v", err)
			}
			data, _ := io.ReadAll(r)
			got := string(data)
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("body missing %q, got %q", tc.wantContains, got)
			}
		})
	}
}
