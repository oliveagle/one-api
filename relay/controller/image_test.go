package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
	"github.com/songquanpeng/one-api/relay/meta"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

func TestGetImageRequestDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"draw a cat"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	r, err := getImageRequest(c, 0)
	if err != nil {
		t.Fatalf("getImageRequest: %v", err)
	}
	if r.Model != "dall-e-2" || r.Size != "1024x1024" || r.N != 1 {
		t.Fatalf("defaults = model %q, size %q, n %d", r.Model, r.Size, r.N)
	}
}

func TestGetImageRequestPreservesValues(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"prompt":"cat","model":"dall-e-3","size":"1792x1024","n":2}`))
	c.Request.Header.Set("Content-Type", "application/json")
	r, err := getImageRequest(c, 0)
	if err != nil {
		t.Fatalf("getImageRequest: %v", err)
	}
	if r.Model != "dall-e-3" || r.Size != "1792x1024" || r.N != 2 {
		t.Fatalf("request values changed: %+v", r)
	}
}

func TestValidateImageRequest(t *testing.T) {
	cases := []struct {
		name string
		req  relaymodel.ImageRequest
		code string
	}{
		{"missing prompt", relaymodel.ImageRequest{Model: "dall-e-2", Size: "1024x1024", N: 1}, "prompt_missing"},
		{"unsupported size", relaymodel.ImageRequest{Prompt: "cat", Model: "dall-e-2", Size: "1792x1024", N: 1}, "size_not_supported"},
		{"prompt too long", relaymodel.ImageRequest{Prompt: strings.Repeat("x", 1001), Model: "dall-e-2", Size: "1024x1024", N: 1}, "prompt_too_long"},
		{"invalid count", relaymodel.ImageRequest{Prompt: "cat", Model: "dall-e-3", Size: "1024x1024", N: 2}, "n_not_within_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateImageRequest(&tc.req, &meta.Meta{})
			if err == nil || err.Error.Code != tc.code {
				t.Fatalf("validateImageRequest() = %+v, want code %q", err, tc.code)
			}
		})
	}

	valid := &relaymodel.ImageRequest{Prompt: "cat", Model: "dall-e-2", Size: "1024x1024", N: 1}
	if err := validateImageRequest(valid, &meta.Meta{}); err != nil {
		t.Fatalf("valid image request rejected: %+v", err)
	}
}

func TestImageValidationUnknownModelIsPermissive(t *testing.T) {
	if !isValidImageSize("custom-model", "custom-size") {
		t.Fatal("unknown models should permit provider-defined sizes")
	}
	if !isValidImagePromptLength("custom-model", 100000) {
		t.Fatal("unknown models should permit provider-defined prompt lengths")
	}
	if !isWithinRange("custom-model", 100) {
		t.Fatal("unknown models should permit provider-defined image counts")
	}
}

func TestGetImageCostRatio(t *testing.T) {
	if _, err := getImageCostRatio(nil); err == nil {
		t.Fatal("nil request should return an error")
	}
	cases := []struct {
		name    string
		request relaymodel.ImageRequest
		want    float64
	}{
		{"unknown defaults to one", relaymodel.ImageRequest{Model: "custom", Size: "x"}, 1},
		{"dall-e-3 standard", relaymodel.ImageRequest{Model: "dall-e-3", Size: "1024x1024"}, billingratio.ImageSizeRatios["dall-e-3"]["1024x1024"]},
		{"dall-e-3 hd square", relaymodel.ImageRequest{Model: "dall-e-3", Size: "1024x1024", Quality: "hd"}, billingratio.ImageSizeRatios["dall-e-3"]["1024x1024"] * 2},
		{"dall-e-3 hd wide", relaymodel.ImageRequest{Model: "dall-e-3", Size: "1792x1024", Quality: "hd"}, billingratio.ImageSizeRatios["dall-e-3"]["1792x1024"] * 1.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := getImageCostRatio(&tc.request)
			if err != nil || got != tc.want {
				t.Fatalf("getImageCostRatio() = (%v, %v), want %v", got, err, tc.want)
			}
		})
	}
}
