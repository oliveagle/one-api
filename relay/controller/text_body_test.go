package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// fakeAdaptor lets the request body path run without spinning up a real
// upstream. It only implements the surface area getRequestBody actually
// uses; the rest is fine to ignore.
type fakeAdaptor struct {
	converted any
	err       error
}

func (f fakeAdaptor) Init(*meta.Meta)                                                  {}
func (f fakeAdaptor) GetRequestURL(*meta.Meta) (string, error)                         { return "", nil }
func (f fakeAdaptor) SetupRequestHeader(*gin.Context, *http.Request, *meta.Meta) error { return nil }
func (f fakeAdaptor) ConvertRequest(_ *gin.Context, _ int, req *relaymodel.GeneralOpenAIRequest) (any, error) {
	if f.converted != nil {
		return f.converted, f.err
	}
	return req, f.err
}
func (f fakeAdaptor) ConvertImageRequest(*relaymodel.ImageRequest) (any, error) { return nil, nil }
func (f fakeAdaptor) DoRequest(*gin.Context, *meta.Meta, io.Reader) (*http.Response, error) {
	return nil, nil
}
func (f fakeAdaptor) DoResponse(*gin.Context, *http.Response, *meta.Meta) (*relaymodel.Usage, *relaymodel.ErrorWithStatusCode) {
	return nil, nil
}
func (f fakeAdaptor) GetModelList() []string { return nil }
func (f fakeAdaptor) GetChannelName() string { return "fake" }

var _ adaptor.Adaptor = (*fakeAdaptor)(nil)

func TestGetRequestBodyFastPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	want := io.NopCloser(strings.NewReader(`{"model":"gpt-4o-mini"}`))
	c.Request = readCloserRequest("GET", "/v1/chat/completions", want)
	m := &meta.Meta{
		APIType:         0, // apitype.OpenAI
		OriginModelName: "gpt-4o-mini",
		ActualModelName: "gpt-4o-mini",
		ChannelType:     channeltype.OpenAI,
	}
	got, err := getRequestBody(c, m, &relaymodel.GeneralOpenAIRequest{Model: "gpt-4o-mini"}, fakeAdaptor{}, false)
	if err != nil {
		t.Fatalf("getRequestBody: %v", err)
	}
	if got != c.Request.Body {
		t.Fatalf("fast path must return c.Request.Body (got %T, want %T)", got, c.Request.Body)
	}
}

func TestGetRequestBodyForcesConversion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request = readCloserRequest("GET", "/v1/chat/completions", strings.NewReader("{}"))

	cases := []struct {
		name string
		m    *meta.Meta
	}{
		{"different model", &meta.Meta{APIType: 0, OriginModelName: "a", ActualModelName: "b", ChannelType: channeltype.OpenAI}},
		{"baichuan channel", &meta.Meta{APIType: 0, OriginModelName: "a", ActualModelName: "a", ChannelType: channeltype.Baichuan}},
		{"forced system prompt", &meta.Meta{APIType: 0, OriginModelName: "a", ActualModelName: "a", ChannelType: channeltype.OpenAI, ForcedSystemPrompt: "be brief"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := fakeAdaptor{converted: relaymodel.GeneralOpenAIRequest{Model: "out"}}
			got, err := getRequestBody(c, tc.m, &relaymodel.GeneralOpenAIRequest{Model: "x"}, fa, false)
			if err != nil {
				t.Fatalf("getRequestBody: %v", err)
			}
			data, _ := io.ReadAll(got)
			if !bytes.Contains(data, []byte(`"model":"out"`)) {
				t.Fatalf("converted body does not include adapted model: %q", data)
			}
		})
	}
}

func TestGetRequestBodySurfacesError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request = readCloserRequest("GET", "/v1/chat/completions", strings.NewReader("{}"))
	m := &meta.Meta{APIType: 0, OriginModelName: "a", ActualModelName: "b", ChannelType: channeltype.OpenAI}
	fa := fakeAdaptor{err: errors.New("boom")}
	if _, err := getRequestBody(c, m, &relaymodel.GeneralOpenAIRequest{Model: "x"}, fa, false); err == nil {
		t.Fatal("expected error from ConvertRequest to surface")
	}
}

func readCloserRequest(method, path string, body io.Reader) *http.Request {
	r, _ := http.NewRequest(method, path, body)
	return r
}

var _ = json.Marshal

func TestGetRequestBodyNormalizesToolChoice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request = readCloserRequest("GET", "/v1/chat/completions", strings.NewReader("{}"))
	m := &meta.Meta{APIType: 0, OriginModelName: "a", ActualModelName: "b", ChannelType: channeltype.OpenAI}

	cases := []struct {
		name   string
		choice any
		want   string
	}{
		{"any maps to required", "any", "required"},
		{"other non-standard string maps to required", "all", "required"},
		{"empty string maps to required", "", "required"},
		{"valid auto is preserved", "auto", "auto"},
		{"valid none is preserved", "none", "none"},
		{"valid required is preserved", "required", "required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := fakeAdaptor{}
			req := &relaymodel.GeneralOpenAIRequest{Model: "x", ToolChoice: tc.choice}
			got, err := getRequestBody(c, m, req, fa, false)
			if err != nil {
				t.Fatalf("getRequestBody: %v", err)
			}
			data, _ := io.ReadAll(got)
			if !bytes.Contains(data, []byte(`"tool_choice":"`+tc.want+`"`)) {
				t.Fatalf("tool_choice not normalized to %q, body: %s", tc.want, data)
			}
		})
	}
}
