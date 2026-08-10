package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/constant/role"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func TestGetMappedModelName(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		mapping map[string]string
		want    string
		mapped  bool
	}{
		{"nil mapping", "gpt-4o", nil, "gpt-4o", false},
		{"missing mapping", "gpt-4o", map[string]string{"other": "x"}, "gpt-4o", false},
		{"empty mapping", "gpt-4o", map[string]string{"gpt-4o": ""}, "gpt-4o", false},
		{"mapped", "gpt-4o", map[string]string{"gpt-4o": "deployment"}, "deployment", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, mapped := getMappedModelName(tc.model, tc.mapping)
			if got != tc.want || mapped != tc.mapped {
				t.Fatalf("getMappedModelName() = (%q, %v), want (%q, %v)", got, mapped, tc.want, tc.mapped)
			}
		})
	}
}

func TestGetPreConsumedQuota(t *testing.T) {
	old := config.PreConsumedQuota
	config.PreConsumedQuota = 100
	t.Cleanup(func() { config.PreConsumedQuota = old })

	cases := []struct {
		name   string
		max    int
		prompt int
		ratio  float64
		want   int64
	}{
		{"default allowance", 0, 20, 2, 240},
		{"includes max tokens", 30, 20, 2, 300},
		{"fractional ratio truncates", 1, 0, 0.5, 50},
		{"free model", 100, 100, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &relaymodel.GeneralOpenAIRequest{MaxTokens: tc.max}
			if got := getPreConsumedQuota(r, tc.prompt, tc.ratio); got != tc.want {
				t.Fatalf("getPreConsumedQuota() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestIsErrorHappened(t *testing.T) {
	jsonResponse := func(status int) *http.Response {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}}
	}
	cases := []struct {
		name string
		meta *meta.Meta
		resp *http.Response
		want bool
	}{
		{"nil response", &meta.Meta{ChannelType: channeltype.OpenAI}, nil, true},
		{"aws nil response", &meta.Meta{ChannelType: channeltype.AwsClaude}, nil, false},
		{"ok", &meta.Meta{ChannelType: channeltype.OpenAI}, jsonResponse(http.StatusOK), false},
		{"created", &meta.Meta{ChannelType: channeltype.Replicate}, jsonResponse(http.StatusCreated), false},
		{"bad status", &meta.Meta{ChannelType: channeltype.OpenAI}, jsonResponse(http.StatusBadGateway), true},
		{"stream json", &meta.Meta{ChannelType: channeltype.OpenAI, IsStream: true}, jsonResponse(http.StatusOK), true},
		{"replicate stream json", &meta.Meta{ChannelType: channeltype.Replicate, IsStream: true}, jsonResponse(http.StatusOK), false},
		{"deepl stream json", &meta.Meta{ChannelType: channeltype.DeepL, IsStream: true}, jsonResponse(http.StatusOK), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isErrorHappened(tc.meta, tc.resp); got != tc.want {
				t.Fatalf("isErrorHappened() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSetSystemPrompt(t *testing.T) {
	if setSystemPrompt(context.Background(), &relaymodel.GeneralOpenAIRequest{}, "") {
		t.Fatal("empty prompt should not mutate request")
	}
	if setSystemPrompt(context.Background(), &relaymodel.GeneralOpenAIRequest{}, "system") {
		t.Fatal("request without messages should not be mutated")
	}

	r := &relaymodel.GeneralOpenAIRequest{Messages: []relaymodel.Message{{Role: role.System, Content: "old"}, {Role: "user", Content: "hi"}}}
	if !setSystemPrompt(context.Background(), r, "new") || len(r.Messages) != 2 || r.Messages[0].Content != "new" {
		t.Fatalf("system prompt was not rewritten: %+v", r.Messages)
	}

	r = &relaymodel.GeneralOpenAIRequest{Messages: []relaymodel.Message{{Role: "user", Content: "hi"}}}
	if !setSystemPrompt(context.Background(), r, "new") || len(r.Messages) != 2 || r.Messages[0].Role != role.System || r.Messages[0].Content != "new" {
		t.Fatalf("system prompt was not prepended: %+v", r.Messages)
	}
}

func TestGetAndValidateTextRequestDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name  string
		mode  int
		path  string
		body  string
		param string
		want  string
	}{
		{"moderation", relaymode.Moderations, "/v1/moderations", `{"input":"safe"}`, "", "text-moderation-latest"},
		{"embedding route model", relaymode.Embeddings, "/v1/models/embed/embeddings", `{"input":"text"}`, "embed", "embed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			if tc.param != "" {
				c.Params = gin.Params{{Key: "model", Value: tc.param}}
			}
			r, _, err := getAndValidateTextRequest(c, tc.mode)
			if err != nil {
				t.Fatalf("getAndValidateTextRequest: %v", err)
			}
			if r.Model != tc.want {
				t.Fatalf("Model = %q, want %q", r.Model, tc.want)
			}
		})
	}
}

func TestGetAndValidateTextRequestRejectsInvalidJSON(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	if _, _, err := getAndValidateTextRequest(c, relaymode.ChatCompletions); err == nil {
		t.Fatal("invalid JSON should return an error")
	}
}
