package model

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`

	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}

type Error struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    any    `json:"code"`
	// RetryAfterMs carries the upstream Retry-After header (milliseconds)
	// for 429 responses. Transport-only: never serialized to clients, but
	// the 429 routing penalty consults it so throttle windows match what
	// the upstream asked for instead of a fixed guess.
	RetryAfterMs int64 `json:"-"`
}

type ErrorWithStatusCode struct {
	Error
	StatusCode int `json:"status_code"`
}
