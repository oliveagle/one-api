package validator

import (
	"math"
	"testing"

	"github.com/songquanpeng/one-api/relay/model"
	"github.com/songquanpeng/one-api/relay/relaymode"
)

func TestValidateTextRequest_RejectsNegativeMaxTokens(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "gpt-4o", MaxTokens: -1}
	if err := ValidateTextRequest(r, relaymode.ChatCompletions); err == nil {
		t.Fatal("expected error for negative MaxTokens")
	}
}

func TestValidateTextRequest_RejectsMaxTokensOverflow(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "gpt-4o", MaxTokens: math.MaxInt32}
	if err := ValidateTextRequest(r, relaymode.ChatCompletions); err == nil {
		t.Fatal("expected error for MaxTokens overflow")
	}
}

func TestValidateTextRequest_RejectsMissingModel(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Messages: []model.Message{{Role: "user", Content: "hi"}}}
	if err := ValidateTextRequest(r, relaymode.ChatCompletions); err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestValidateTextRequest_ChatCompletions_RequiresMessages(t *testing.T) {
	cases := []struct {
		name string
		req  *model.GeneralOpenAIRequest
	}{
		{"nil messages", &model.GeneralOpenAIRequest{Model: "gpt-4o"}},
		{"empty messages", &model.GeneralOpenAIRequest{Model: "gpt-4o", Messages: []model.Message{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTextRequest(tc.req, relaymode.ChatCompletions); err == nil {
				t.Fatal("expected error for missing/empty messages")
			}
		})
	}
}

func TestValidateTextRequest_ChatCompletions_AcceptsValid(t *testing.T) {
	r := &model.GeneralOpenAIRequest{
		Model:    "gpt-4o",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}
	if err := ValidateTextRequest(r, relaymode.ChatCompletions); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTextRequest_Completions_RequiresPrompt(t *testing.T) {
	// The validator's "" check matches empty-string prompt but not nil.
	// nil Prompt slips through this branch — documenting current behaviour.
	r := &model.GeneralOpenAIRequest{Model: "gpt-3.5-turbo-instruct"}
	if err := ValidateTextRequest(r, relaymode.Completions); err != nil {
		t.Fatalf("nil Prompt is allowed (current behaviour): %v", err)
	}
	r.Prompt = ""
	if err := ValidateTextRequest(r, relaymode.Completions); err == nil {
		t.Fatal("empty-string prompt should be rejected")
	}
	r.Prompt = "Tell me a story"
	if err := ValidateTextRequest(r, relaymode.Completions); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTextRequest_Embeddings_AcceptsAnything(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "text-embedding-3-large"}
	if err := ValidateTextRequest(r, relaymode.Embeddings); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTextRequest_Moderations_RequiresInput(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "text-moderation-latest"}
	if err := ValidateTextRequest(r, relaymode.Moderations); err != nil {
		t.Fatalf("nil Input is allowed (current behaviour): %v", err)
	}
	r.Input = ""
	if err := ValidateTextRequest(r, relaymode.Moderations); err == nil {
		t.Fatal("empty-string input should be rejected")
	}
	r.Input = "some text"
	if err := ValidateTextRequest(r, relaymode.Moderations); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTextRequest_Edits_RequiresInstruction(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "gpt-4o", Input: "buffer"}
	if err := ValidateTextRequest(r, relaymode.Edits); err == nil {
		t.Fatal("expected error for missing instruction")
	}
	r.Instruction = "fix typos"
	if err := ValidateTextRequest(r, relaymode.Edits); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Unknown / non-text modes must skip the per-mode body check and only enforce
// the global MaxTokens / Model checks.
func TestValidateTextRequest_UnknownMode_SkipsBodyCheck(t *testing.T) {
	r := &model.GeneralOpenAIRequest{Model: "gpt-4o"}
	if err := ValidateTextRequest(r, relaymode.ImagesGenerations); err != nil {
		t.Fatalf("images generations has no required body fields: %v", err)
	}
	if err := ValidateTextRequest(r, relaymode.AudioSpeech); err != nil {
		t.Fatalf("audio speech has no required body fields: %v", err)
	}
	if err := ValidateTextRequest(r, relaymode.Unknown); err != nil {
		t.Fatalf("unknown mode has no required body fields: %v", err)
	}
}

// Embeddings requires Model — the global check is enforced before the
// per-mode switch. The router doesn't actually rely on skipping this; it
// sets Model before invoking the validator in production.
func TestValidateTextRequest_Embeddings_RequiresModel(t *testing.T) {
	r := &model.GeneralOpenAIRequest{}
	if err := ValidateTextRequest(r, relaymode.Embeddings); err == nil {
		t.Fatal("Model is required even for embeddings (current behaviour)")
	}
}

// MaxTokens zero is allowed (means "provider's default").
func TestValidateTextRequest_MaxTokensZeroIsOK(t *testing.T) {
	r := &model.GeneralOpenAIRequest{
		Model:     "gpt-4o",
		Messages:  []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens: 0,
	}
	if err := ValidateTextRequest(r, relaymode.ChatCompletions); err != nil {
		t.Fatalf("zero MaxTokens should be allowed: %v", err)
	}
}
