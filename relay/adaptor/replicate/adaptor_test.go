package replicate

import (
	"testing"

	"github.com/songquanpeng/one-api/relay/model"
)

// Replicate's HTTP flow cannot run hermetically (hardcoded
// api.replicate.com host + http.DefaultClient + 3s polling), so its
// request-conversion quirks are pinned here at the unit level:
// stream-only gate, prompt flattening, and sampling defaults.

func ptr[T any](v T) *T { return &v }

func TestConvertRequest_RejectsNonStream(t *testing.T) {
	a := &Adaptor{}
	_, err := a.ConvertRequest(nil, 0, &model.GeneralOpenAIRequest{Stream: false})
	if err == nil {
		t.Fatal("ConvertRequest must reject stream=false (replicate endpoint is stream-only)")
	}
}

func TestConvertRequest_FlattensMessagesAndDefaults(t *testing.T) {
	a := &Adaptor{}
	converted, err := a.ConvertRequest(nil, 0, &model.GeneralOpenAIRequest{
		Stream: true,
		Messages: []model.Message{
			{Role: "system", Content: "You are terse."},
			{Role: "user", Content: "hi"},
		},
		// MaxTokens zero — must default to 500.
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	req, ok := converted.(ReplicateChatRequest)
	if !ok {
		t.Fatalf("converted is %T, want ReplicateChatRequest", converted)
	}
	// Messages are flattened into a single "role: content\n" prompt.
	wantPrompt := "system: You are terse.\nuser: hi\n"
	if req.Input.Prompt != wantPrompt {
		t.Errorf("prompt = %q, want %q", req.Input.Prompt, wantPrompt)
	}
	// Defaults when the client omits sampling knobs.
	if req.Input.MaxTokens != 500 {
		t.Errorf("max_tokens default = %d, want 500", req.Input.MaxTokens)
	}
	if req.Input.Temperature != 1.0 {
		t.Errorf("temperature default = %v, want 1.0", req.Input.Temperature)
	}
	if req.Input.TopP != 1.0 {
		t.Errorf("top_p default = %v, want 1.0", req.Input.TopP)
	}
}

func TestConvertRequest_ForwardsSamplingKnobs(t *testing.T) {
	a := &Adaptor{}
	converted, err := a.ConvertRequest(nil, 0, &model.GeneralOpenAIRequest{
		Stream:           true,
		Messages:         []model.Message{{Role: "user", Content: "hi"}},
		MaxTokens:        128,
		Temperature:      ptr(0.2),
		TopP:             ptr(0.9),
		PresencePenalty:  ptr(0.1),
		FrequencyPenalty: ptr(0.2),
	})
	if err != nil {
		t.Fatalf("ConvertRequest: %v", err)
	}
	req := converted.(ReplicateChatRequest)
	if req.Input.MaxTokens != 128 || req.Input.Temperature != 0.2 ||
		req.Input.TopP != 0.9 || req.Input.PresencePenalty != 0.1 || req.Input.FrequencyPenalty != 0.2 {
		t.Errorf("sampling knobs not forwarded: %+v", req.Input)
	}
}
