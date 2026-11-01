package commandcode

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/relay/adaptor"
	"github.com/songquanpeng/one-api/relay/meta"
	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

// CLI_VERSION is the Command Code CLI release whose /alpha/generate request
// shape and headers this adaptor reproduces. Bump it when the endpoint starts
// refusing older clients.
const CLI_VERSION = "1.72.2"

// ChannelName is how the adaptor reports itself (and how channels show up in
// /v1/models as the owner).
const ChannelName = "commandcode"

// Adaptor relays the CLI's /alpha/generate endpoint as Chat Completions.
type Adaptor struct{}

func (a *Adaptor) Init(meta *meta.Meta) {}

func (a *Adaptor) GetRequestURL(meta *meta.Meta) (string, error) {
	base := strings.TrimSuffix(meta.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return base + generatePath, nil
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, req *http.Request, meta *meta.Meta) error {
	adaptor.SetupCommonRequestHeader(c, req, meta)
	// The CLI sends the key both ways; the endpoint reads either.
	req.Header.Set("Authorization", "Bearer "+meta.APIKey)
	req.Header.Set("x-api-key", meta.APIKey)
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-command-code-version", CLI_VERSION)
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-project-slug", "root")
	req.Header.Set("x-taste-learning", "false")
	req.Header.Set("x-session-id", sessionID())
	if meta.Config.CommandCodeZDR {
		// Zero Data Retention, as the CLI asks it with CMD_ZDR=1: only
		// upstreams that keep nothing may serve the request (and it usually
		// costs more; a model with none fails).
		req.Header.Set("x-cmd-zdr", "1")
	}
	return nil
}

func (a *Adaptor) ConvertRequest(c *gin.Context, relayMode int, request *model.GeneralOpenAIRequest) (any, error) {
	return ConvertRequest(request)
}

func (a *Adaptor) ConvertImageRequest(request *model.ImageRequest) (any, error) {
	return nil, fmt.Errorf("%s: image generation is not supported", ChannelName)
}

func (a *Adaptor) DoRequest(c *gin.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error) {
	return adaptor.DoRequestHelper(a, c, meta, requestBody)
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	if meta.Mode != relaymode.ChatCompletions && meta.Mode != relaymode.Completions {
		return nil, unsupportedMode(resp, meta.Mode)
	}
	return HandleResponse(c, resp, meta)
}

func (a *Adaptor) GetModelList() []string { return ModelList }

func (a *Adaptor) GetChannelName() string { return ChannelName }

// sessionID returns a fresh x-session-id. The CLI keeps one per run; one per
// request is the safest equivalent for a shared relay (the upstream only uses
// it for affinity and telemetry).
func sessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	// RFC 4122 v4 shape, so any uuid-shaped validation upstream accepts it.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
