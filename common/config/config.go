package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/songquanpeng/one-api/common/env"

	"github.com/google/uuid"
)

var SystemName = "One API"
var ServerAddress = "http://localhost:3000"
var Footer = ""
var Logo = ""
var TopUpLink = ""
var ChatLink = ""
var QuotaPerUnit = 500 * 1000.0 // $0.002 / 1K tokens
var DisplayInCurrencyEnabled = true
var DisplayTokenStatEnabled = true

// Any options with "Secret", "Token" in its key won't be return by GetOptions

var SessionSecret = uuid.New().String()

var OptionMap map[string]string
var OptionMapRWMutex sync.RWMutex

var ItemsPerPage = 10
var MaxRecentItems = 100

var PasswordLoginEnabled = true
var PasswordRegisterEnabled = true
var EmailVerificationEnabled = false
var GitHubOAuthEnabled = false
var OidcEnabled = false
var WeChatAuthEnabled = false
var TurnstileCheckEnabled = false
var RegisterEnabled = true

var EmailDomainRestrictionEnabled = false
var EmailDomainWhitelist = []string{
	"gmail.com",
	"163.com",
	"126.com",
	"qq.com",
	"outlook.com",
	"hotmail.com",
	"icloud.com",
	"yahoo.com",
	"foxmail.com",
}

var DebugEnabled = strings.ToLower(os.Getenv("DEBUG")) == "true"
var DebugSQLEnabled = strings.ToLower(os.Getenv("DEBUG_SQL")) == "true"
var MemoryCacheEnabled = strings.ToLower(os.Getenv("MEMORY_CACHE_ENABLED")) == "true"

var LogConsumeEnabled = true

var SMTPServer = ""
var SMTPPort = 587
var SMTPAccount = ""
var SMTPFrom = ""
var SMTPToken = ""

var GitHubClientId = ""
var GitHubClientSecret = ""

var LarkClientId = ""
var LarkClientSecret = ""

var OidcClientId = ""
var OidcClientSecret = ""
var OidcWellKnown = ""
var OidcAuthorizationEndpoint = ""
var OidcTokenEndpoint = ""
var OidcUserinfoEndpoint = ""

var WeChatServerAddress = ""
var WeChatServerToken = ""
var WeChatAccountQRCodeImageURL = ""

var MessagePusherAddress = ""
var MessagePusherToken = ""

var TurnstileSiteKey = ""
var TurnstileSecretKey = ""

var QuotaForNewUser int64 = 0
var QuotaForInviter int64 = 0
var QuotaForInvitee int64 = 0
var ChannelDisableThreshold = 5.0
var AutomaticDisableChannelEnabled = false
var AutomaticEnableChannelEnabled = false
var QuotaRemindThreshold int64 = 1000
var PreConsumedQuota int64 = 500
var ApproximateTokenEnabled = false
var RetryTimes = 0

// LogRetentionDays caps how long request/consume logs are kept; the retention
// loop deletes older rows once at startup and then daily. 0 disables cleanup
// (logs grow unboundedly, as they did before this knob existed).
var LogRetentionDays = env.Int("LOG_RETENTION_DAYS", 30)

var RootUserEmail = ""

var IsMasterNode = os.Getenv("NODE_TYPE") != "slave"

var requestInterval, _ = strconv.Atoi(os.Getenv("POLLING_INTERVAL"))
var RequestInterval = time.Duration(requestInterval) * time.Second

var SyncFrequency = env.Int("SYNC_FREQUENCY", 10*60) // unit is second

var BatchUpdateEnabled = false
var BatchUpdateInterval = env.Int("BATCH_UPDATE_INTERVAL", 5)

var RelayTimeout = env.Int("RELAY_TIMEOUT", 0) // unit is second

var GeminiSafetySetting = env.String("GEMINI_SAFETY_SETTING", "BLOCK_NONE")

var Theme = env.String("THEME", "default")
var ValidThemes = map[string]bool{
	"default": true,
	"berry":   true,
	"air":     true,
}

// All duration's unit is seconds
// Shouldn't larger then RateLimitKeyExpirationDuration
var (
	GlobalApiRateLimitNum            = env.Int("GLOBAL_API_RATE_LIMIT", 480)
	GlobalApiRateLimitDuration int64 = 3 * 60

	GlobalWebRateLimitNum            = env.Int("GLOBAL_WEB_RATE_LIMIT", 240)
	GlobalWebRateLimitDuration int64 = 3 * 60

	UploadRateLimitNum            = 10
	UploadRateLimitDuration int64 = 60

	DownloadRateLimitNum            = 10
	DownloadRateLimitDuration int64 = 60

	CriticalRateLimitNum            = 20
	CriticalRateLimitDuration int64 = 20 * 60
)

var RateLimitKeyExpirationDuration = 20 * time.Minute

var EnableMetric = env.Bool("ENABLE_METRIC", false)
var MetricQueueSize = env.Int("METRIC_QUEUE_SIZE", 10)
var MetricSuccessRateThreshold = env.Float64("METRIC_SUCCESS_RATE_THRESHOLD", 0.8)
var MetricSuccessChanSize = env.Int("METRIC_SUCCESS_CHAN_SIZE", 1024)
var MetricFailChanSize = env.Int("METRIC_FAIL_CHAN_SIZE", 128)

var InitialRootToken = os.Getenv("INITIAL_ROOT_TOKEN")

var InitialRootAccessToken = os.Getenv("INITIAL_ROOT_ACCESS_TOKEN")

var GeminiVersion = env.String("GEMINI_VERSION", "v1")

var OnlyOneLogFile = env.Bool("ONLY_ONE_LOG_FILE", false)

// OpenTelemetry 配置
var OtelEnabled = env.Bool("OTEL_ENABLED", false)
var OtelServiceName = env.String("OTEL_SERVICE_NAME", "one-api")
var OtelEndpoint = env.String("OTEL_ENDPOINT", "127.0.0.1:4317")
var OtelInsecure = env.Bool("OTEL_INSECURE", true)
var OtelTracesEnabled = env.Bool("OTEL_TRACES_ENABLED", true)
var OtelMetricsEnabled = env.Bool("OTEL_METRICS_ENABLED", true)
var OtelLogsEnabled = env.Bool("OTEL_LOGS_ENABLED", true)

var RelayProxy = env.String("RELAY_PROXY", "")
var UserContentRequestProxy = env.String("USER_CONTENT_REQUEST_PROXY", "")
var UserContentRequestTimeout = env.Int("USER_CONTENT_REQUEST_TIMEOUT", 30)

var EnforceIncludeUsage = env.Bool("ENFORCE_INCLUDE_USAGE", false)
var TestPrompt = env.String("TEST_PROMPT", "Output only your specific model name with no additional text.")

// Session-sticky routing.
//
// When enabled, requests that carry a session identifier are routed to the
// same upstream channel ("node") for the lifetime of that session, instead of
// being randomly load balanced across all channels. This keeps session-local
// state (prompt caches, KV memory, tool history) warm on a single node.
// If the sticky node later fails with a retryable error (rate limit, quota
// exhausted, 5xx), the relay fails over to another healthy node and re-pins the
// session to it.
var StickyRoutingEnabled = strings.ToLower(os.Getenv("STICKY_ROUTING_ENABLED")) == "true"

// StickyModels is a comma separated allowlist of model names that participate
// in session-sticky routing. Empty means every model participates (only when a
// session identifier is present on the request).
var StickyModels = env.String("STICKY_MODELS", "")

// SessionIdHeader is the HTTP header used to carry the agent session id.
// A coding agent (e.g. Claude Code / Codex) should be configured to send this
// header on every request belonging to the same session.
var SessionIdHeader = env.String("SESSION_ID_HEADER", "X-Session-Id")

// SessionIdBodyField is the top-level JSON body field used as a fallback when
// the session id header is absent.
var SessionIdBodyField = env.String("SESSION_ID_BODY_FIELD", "session_id")

// SessionFingerprintEnabled controls the last-resort session identity source:
// a fingerprint derived from the request's stable conversation prefix (system
// prompt + first user message).
//
// Most OpenAI-compatible coding agents (pi/pix, and anything built on the
// openai JS/python SDK) send no session id at all -- not as a header and not in
// the body -- so header/body extraction alone leaves every request unpinned and
// sticky routing degrades to random load balancing. The fingerprint recovers
// stickiness for those clients because each turn of one agent session replays a
// byte-identical prefix. Set to false to require an explicit session id.
var SessionFingerprintEnabled = env.Bool("SESSION_FINGERPRINT_ENABLED", true)

// StickyFallbackToToken pins requests that have no derivable session identity
// to a node per API token. This keeps a single-token deployment on one node,
// which is coarser than per-session stickiness, so it is off by default: with
// the fingerprint enabled, distinct sessions on the same token should still be
// able to spread across nodes.
var StickyFallbackToToken = env.Bool("STICKY_FALLBACK_TO_TOKEN", false)

// StickyCooldownSeconds is how long a node that failed during a session is
// kept out of the sticky selection, so the session does not immediately bounce
// back to a node that just returned an error. Zero disables the cooldown.
var StickyCooldownSeconds = env.Int("STICKY_COOLDOWN_SECONDS", 60)

// StickySessionTTLSeconds is how long a sticky session record is kept in the
// routing store after its last request before being pruned. This bounds memory
// usage of the realtime session registry. Zero disables pruning.
var StickySessionTTLSeconds = env.Int("STICKY_SESSION_TTL_SECONDS", 24*60*60)

// StickyFailureThreshold is how many consecutive retryable failures a session
// must accumulate on a channel before it is allowed to migrate to a different
// channel. A value of 1 means "switch on the first error" (legacy behaviour).
// Higher values make sessions stickier: a single transient 429 or 5xx will
// cool the channel briefly but keep the session pinned, preserving the prompt
// cache / KV memory. The threshold is per-session per-channel and resets on
// the first successful request.
var StickyFailureThreshold = env.Int("STICKY_FAILURE_THRESHOLD", 3)
