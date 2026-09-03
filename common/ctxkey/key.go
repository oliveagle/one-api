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
	// ModelMappingOverride carries the extended model mapping produced by
	// channel-name addressing (middleware.Distribute); merged into
	// ctxkey.ModelMapping by SetupContextForSelectedChannel.
	ModelMappingOverride = "model_mapping_override"
	ChannelName          = "channel_name"
	TokenId              = "token_id"
	// TokenRPMLimit carries the token's per-minute relay request cap from
	// TokenAuth to the RPM middleware.
	TokenRPMLimit   = "token_rpm_limit"
	TokenName       = "token_name"
	BaseURL         = "base_url"
	AvailableModels = "available_models"
	KeyRequestBody  = "key_request_body"
	SystemPrompt    = "system_prompt"
	Headers         = "headers"
	SessionKey      = "session_key"
	SessionSource   = "session_source"
)
