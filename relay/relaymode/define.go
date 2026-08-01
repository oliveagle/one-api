package relaymode

const (
	Unknown = iota
	ChatCompletions
	Completions
	Embeddings
	Moderations
	ImagesGenerations
	Edits
	AudioSpeech
	AudioTranscription
	AudioTranslation
	// Proxy is a special relay mode for proxying requests to custom upstream
	Proxy
	// Responses is the OpenAI Responses API (POST /v1/responses). It is relayed
	// as a passthrough: the request body is forwarded untouched and the response
	// is returned as-is, so stateful features (previous_response_id, store,
	// server-side tools) keep working. Only upstreams that implement the endpoint
	// natively can serve it; see docs/adr/0001-openai-responses-api-passthrough.md
	Responses
)
