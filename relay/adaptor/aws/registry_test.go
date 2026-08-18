package aws

import (
	"testing"

	// Both subpackages declare "package aws" — alias them explicitly
	// (same as registry.go).
	claude "github.com/songquanpeng/one-api/relay/adaptor/aws/claude"
	llama3 "github.com/songquanpeng/one-api/relay/adaptor/aws/llama3"
)

// AWS Bedrock has no HTTP surface in this relay (the adaptor talks to
// the real Bedrock SDK client), so its quirks are pinned at the
// registry/model-ID layer instead: which sub-adaptor a model name
// dispatches to, and the one-api → Bedrock inference-profile ID
// mapping (the ":0" / "-v1:0" suffixes are easy to fat-finger).

func TestGetAdaptor_DispatchesByModelFamily(t *testing.T) {
	cases := []struct {
		model string
		want  AwsModelType
	}{
		{"claude-3-5-sonnet-20241022", AwsClaude},
		{"claude-instant-1.2", AwsClaude},
		{"llama3-8b-8192", AwsLlama3},
		{"llama3-70b-8192", AwsLlama3},
		{"totally-unknown-model", 0},
	}
	for _, tc := range cases {
		got := GetAdaptor(tc.model)
		switch tc.want {
		case AwsClaude:
			if _, ok := got.(*claude.Adaptor); !ok {
				t.Errorf("GetAdaptor(%q) = %T, want claude.Adaptor", tc.model, got)
			}
		case AwsLlama3:
			if _, ok := got.(*llama3.Adaptor); !ok {
				t.Errorf("GetAdaptor(%q) = %T, want llama3.Adaptor", tc.model, got)
			}
		default:
			if got != nil {
				t.Errorf("GetAdaptor(%q) = %T, want nil for unknown model", tc.model, got)
			}
		}
	}
}

func TestClaude_BedrockModelIDMapping(t *testing.T) {
	// Pin the mapping entries other code (and users) rely on: latest
	// aliases must point at the same Bedrock IDs as their dated
	// versions, and the -v2:0 generation matters for 3.5 sonnet.
	want := map[string]string{
		"claude-3-5-sonnet-20241022": "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"claude-3-5-sonnet-latest":   "anthropic.claude-3-5-sonnet-20241022-v2:0",
		"claude-3-5-haiku-20241022":  "anthropic.claude-3-5-haiku-20241022-v1:0",
		"claude-3-haiku-20240307":    "anthropic.claude-3-haiku-20240307-v1:0",
		"claude-2.1":                 "anthropic.claude-v2:1",
	}
	for relay, bedrock := range want {
		if got := claude.AwsModelIDMap[relay]; got != bedrock {
			t.Errorf("AwsModelIDMap[%q] = %q, want %q", relay, got, bedrock)
		}
	}
}

func TestLlama3_BedrockModelIDMapping(t *testing.T) {
	want := map[string]string{
		"llama3-8b-8192":  "meta.llama3-8b-instruct-v1:0",
		"llama3-70b-8192": "meta.llama3-70b-instruct-v1:0",
	}
	for relay, bedrock := range want {
		if got := llama3.AwsModelIDMap[relay]; got != bedrock {
			t.Errorf("AwsModelIDMap[%q] = %q, want %q", relay, got, bedrock)
		}
	}
}
