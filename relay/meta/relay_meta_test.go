package meta

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// GetByContext must lift every field the relay pipeline expects, including
// Bearer-stripping the Authorization header (one-API refuses to forward the
// user-supplied token to the upstream).
func TestGetByContext_PopulatesFields(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/chat/completions", "Bearer sk-test-token-123")
	c.Set(ctxkey.Id, 42)
	c.Set(ctxkey.Username, "alice")
	c.Set(ctxkey.Role, 1)
	c.Set(ctxkey.Status, 1)
	c.Set(ctxkey.Channel, channeltype.OpenAI)
	c.Set(ctxkey.ChannelId, 7)
	c.Set(ctxkey.SpecificChannelId, 7)
	c.Set(ctxkey.RequestModel, "gpt-4o-mini")
	c.Set(ctxkey.ConvertedRequest, "gpt-4o-mini-actual")
	c.Set(ctxkey.OriginalModel, "gpt-4o-mini")
	c.Set(ctxkey.Group, "default")
	c.Set(ctxkey.ModelMapping, map[string]string{"gpt-4o-mini": "gpt-4o-mini-mapped"})
	c.Set(ctxkey.ChannelName, "openai-primary")
	c.Set(ctxkey.TokenId, 5)
	c.Set(ctxkey.TokenName, "sk-primary")
	c.Set(ctxkey.BaseURL, "https://api.openai.com")
	c.Set(ctxkey.KeyRequestBody, []byte("{}"))
	c.Set(ctxkey.SystemPrompt, "be brief")
	c.Set(ctxkey.AvailableModels, []string{"gpt-4o", "gpt-4o-mini"})

	m := GetByContext(c)
	if m == nil {
		t.Fatal("GetByContext returned nil")
	}
	if m.Mode != relaymode.ChatCompletions {
		t.Errorf("Mode = %d, want %d", m.Mode, relaymode.ChatCompletions)
	}
	if m.ChannelType != channeltype.OpenAI {
		t.Errorf("ChannelType = %d, want %d", m.ChannelType, channeltype.OpenAI)
	}
	if m.ChannelId != 7 {
		t.Errorf("ChannelId = %d, want 7", m.ChannelId)
	}
	if m.TokenId != 5 {
		t.Errorf("TokenId = %d, want 5", m.TokenId)
	}
	if m.TokenName != "sk-primary" {
		t.Errorf("TokenName = %q, want sk-primary", m.TokenName)
	}
	if m.UserId != 42 {
		t.Errorf("UserId = %d, want 42", m.UserId)
	}
	if m.Group != "default" {
		t.Errorf("Group = %q, want default", m.Group)
	}
	if m.OriginModelName != "gpt-4o-mini" {
		t.Errorf("OriginModelName = %q", m.OriginModelName)
	}
	if m.APIType != channeltype.ToAPIType(channeltype.OpenAI) {
		t.Errorf("APIType = %d", m.APIType)
	}
	if m.APIKey != "sk-test-token-123" {
		t.Errorf("APIKey = %q, want sk-test-token-123 (Bearer stripped)", m.APIKey)
	}
	if m.BaseURL != "https://api.openai.com" {
		t.Errorf("BaseURL = %q", m.BaseURL)
	}
	if m.RequestURLPath != "/v1/chat/completions" {
		t.Errorf("RequestURLPath = %q", m.RequestURLPath)
	}
	if m.ForcedSystemPrompt != "be brief" {
		t.Errorf("ForcedSystemPrompt = %q", m.ForcedSystemPrompt)
	}
	if m.StartTime.IsZero() {
		t.Error("StartTime should be set")
	}
	if m.ModelMapping["gpt-4o-mini"] != "gpt-4o-mini-mapped" {
		t.Errorf("ModelMapping not propagated")
	}
}

// If the operator didn't pin a base_url the meta struct must fall back to the
// canonical entry from the channel-type table, otherwise the relay would dial
// an empty host.
func TestGetByContext_BaseURLFallback(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/chat/completions", "")
	c.Set(ctxkey.Channel, channeltype.Anthropic)
	c.Set(ctxkey.BaseURL, "")

	m := GetByContext(c)
	if m.BaseURL != channeltype.ChannelBaseURLs[channeltype.Anthropic] {
		t.Fatalf("BaseURL fallback = %q, want %q", m.BaseURL, channeltype.ChannelBaseURLs[channeltype.Anthropic])
	}
}

// Authorization header may use either exact "Bearer X" or be missing entirely.
// We must not blow up when it is missing.
func TestGetByContext_NoAuthorization(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/chat/completions", "")
	c.Set(ctxkey.Channel, channeltype.OpenAI)

	m := GetByContext(c)
	if m.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty when no Authorization header", m.APIKey)
	}
}

// A schema-less token (no "Bearer " prefix) is forwarded verbatim. This is
// intentional: Anthropic-style x-api-key headers do not use Bearer, and the
// operator can supply a raw key in the meta header by leaving the prefix off.
func TestGetByContext_NonBearerAuth(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/chat/completions", "raw-token-no-prefix")
	c.Set(ctxkey.Channel, channeltype.OpenAI)

	m := GetByContext(c)
	if m.APIKey != "raw-token-no-prefix" {
		t.Fatalf("APIKey = %q, want raw-token-no-prefix", m.APIKey)
	}
}

// /v1/responses must resolve to the Responses relay mode so the routing layer
// doesn't mistake it for a completions call.
func TestGetByContext_PathMappedToMode(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/responses", "")
	c.Set(ctxkey.Channel, channeltype.OpenAI)
	m := GetByContext(c)
	if m.Mode != relaymode.Responses {
		t.Fatalf("Mode = %d, want Responses (%d)", m.Mode, relaymode.Responses)
	}
}

// Set a base_url that overrides the channel default to confirm precedence:
// the per-request config wins.
func TestGetByContext_BaseURLPrecedence(t *testing.T) {
	c, _ := gin.CreateTestContext(nil)
	c.Request = newRequest("GET", "/v1/chat/completions", "")
	c.Set(ctxkey.Channel, channeltype.OpenAI)
	c.Set(ctxkey.BaseURL, "https://my-proxy.example.com")

	m := GetByContext(c)
	if m.BaseURL != "https://my-proxy.example.com" {
		t.Fatalf("BaseURL = %q, want https://my-proxy.example.com", m.BaseURL)
	}
}
