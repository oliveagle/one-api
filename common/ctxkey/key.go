package ctxkey

const (
	Config            = "config"
	Id                = "id"
	Username          = "username"
	Role              = "role"
	Status            = "status"
	Channel           = "channel"
	ChannelId         = "channel_id"
	SpecificChannelId = "specific_channel_id"
	RequestModel      = "request_model"
	ConvertedRequest  = "converted_request"
	OriginalModel     = "original_model"
	Group             = "group"
	ModelMapping      = "model_mapping"
	ChannelName       = "channel_name"
	TokenId           = "token_id"
	TokenName         = "token_name"
	BaseURL           = "base_url"
	AvailableModels   = "available_models"
	KeyRequestBody    = "key_request_body"
	SystemPrompt      = "system_prompt"
	Headers           = "headers"
	SessionKey        = "session_key"
	SessionSource     = "session_source"
	// ConvertedFromResponses marks requests that the Responses -> Chat
	// conversion produced. The relay pipeline uses this to force the
	// request body to flow through the OpenAI adaptor's ConvertRequest
	// (which applies per-channel tool schema adaptations like the
	// opencode-go flat tools shape) instead of taking the fast path that
	// would return the raw body verbatim.
	ConvertedFromResponses = "converted_from_responses"
)
