package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
)

// Session key source labels, reported for observability so operators can see
// *why* a request was (or was not) pinned to a node.
const (
	SourceNone        = ""
	SourceHeader      = "header"
	SourceBody        = "body"
	SourceFingerprint = "fingerprint"
)

// wellKnownSessionHeaders are additional headers checked after the configured
// SessionIdHeader. Different agent frontends use different names, so we accept
// the common ones rather than forcing a single spelling.
var wellKnownSessionHeaders = []string{
	"X-Session-Id",
	"X-Session-ID",
	"Session-Id",
	"X-Conversation-Id",
	"X-Conversation-ID",
	"Conversation-Id",
	"X-Chat-Id",
	"X-Thread-Id",
	"X-Agent-Session-Id",
}

// sessionMessage is one chat message, with the content kept as raw JSON so the
// fingerprint is stable regardless of whether the client sends a plain string
// or the multi-part content array form.
type sessionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// sessionBody holds every request-body field consulted when deriving a session
// identity: explicit session ids first, then the conversation prefix used to
// build a fingerprint.
type sessionBody struct {
	SessionID          string                     `json:"session_id"`
	Session            string                     `json:"session"`
	ConversationID     string                     `json:"conversation_id"`
	PreviousResponseID string                     `json:"previous_response_id"`
	Metadata           map[string]json.RawMessage `json:"metadata"`
	Messages           []sessionMessage           `json:"messages"`
	Instructions       json.RawMessage            `json:"instructions"`
	Input              json.RawMessage            `json:"input"`
}

// SessionKeyFromRequest extracts the agent session identifier from the request.
// It is a thin wrapper over ResolveSession kept for callers that do not care
// about where the identifier came from.
func SessionKeyFromRequest(c *gin.Context) string {
	key, _ := ResolveSession(c)
	return key
}

// ResolveSession derives the sticky-routing session identity for a request and
// reports which source produced it.
//
// Sources are tried in order and the first non-empty value wins:
//  1. The configured session id header (default X-Session-Id), then other
//     well-known session/conversation headers.
//  2. An explicit session id in the JSON body (configured field, session_id,
//     session, conversation_id, previous_response_id, metadata.session_id).
//  3. A conversation fingerprint derived from the request's stable prefix
//     (system prompt + first user message). This is the path that makes
//     sticky routing work for OpenAI-compatible agents such as pi/pix, which
//     send no session identifier at all: within one agent session that prefix
//     is byte-identical on every turn, and it differs between sessions.
//
// When nothing can be derived, an empty key is returned, which disables sticky
// routing for the request (falling back to random load balancing).
func ResolveSession(c *gin.Context) (string, string) {
	if v := sessionFromHeaders(c); v != "" {
		return v, SourceHeader
	}
	body, err := common.GetRequestBody(c)
	if err != nil || len(body) == 0 {
		return SourceNone, SourceNone
	}
	var parsed sessionBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SourceNone, SourceNone
	}
	if v := explicitSessionID(body, &parsed); v != "" {
		return v, SourceBody
	}
	if !config.SessionFingerprintEnabled {
		return SourceNone, SourceNone
	}
	if v := fingerprintFromBody(&parsed); v != "" {
		return v, SourceFingerprint
	}
	return SourceNone, SourceNone
}

// sessionKeyFromBody returns the explicit session id carried in a JSON body,
// or the conversation fingerprint when no explicit id is present and
// fingerprinting is enabled. Empty when the body yields no identity.
func sessionKeyFromBody(body []byte) string {
	var parsed sessionBody
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if v := explicitSessionID(body, &parsed); v != "" {
		return v
	}
	if !config.SessionFingerprintEnabled {
		return ""
	}
	return fingerprintFromBody(&parsed)
}

// sessionFromHeaders checks the configured header first, then the well-known
// aliases.
func sessionFromHeaders(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	if name := strings.TrimSpace(config.SessionIdHeader); name != "" {
		if v := strings.TrimSpace(c.Request.Header.Get(name)); v != "" {
			return v
		}
	}
	for _, name := range wellKnownSessionHeaders {
		if v := strings.TrimSpace(c.Request.Header.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

// explicitSessionID returns a session id that the client stated outright.
func explicitSessionID(body []byte, parsed *sessionBody) string {
	// Prefer the configured field name if it differs from the default.
	if field := strings.TrimSpace(config.SessionIdBodyField); field != "" && field != "session_id" {
		if v := stringField(body, field); v != "" {
			return v
		}
	}
	for _, v := range []string{
		parsed.SessionID,
		parsed.Session,
		parsed.ConversationID,
		parsed.PreviousResponseID,
	} {
		if v := strings.TrimSpace(v); v != "" {
			return v
		}
	}
	for _, key := range []string{"session_id", "session", "conversation_id", "thread_id"} {
		if raw, ok := parsed.Metadata[key]; ok {
			if v := rawString(raw); v != "" {
				return v
			}
		}
	}
	return ""
}

// stringField reads a top-level field as a string without requiring every
// other field in the object to also be a string. The previous implementation
// unmarshalled into map[string]string, which always failed for real chat
// payloads (they contain arrays/objects/numbers) and silently disabled the
// configured field name.
func stringField(body []byte, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	return rawString(raw)
}

func rawString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

// fingerprintFromBody derives a stable identifier for a conversation from the
// parts of the request that do not change as the conversation grows:
//
//   - the system/developer instructions, and
//   - the first user message.
//
// Every later turn of the same agent session replays that identical prefix and
// only appends assistant/tool messages, so the fingerprint is constant for the
// session while differing between sessions.
func fingerprintFromBody(parsed *sessionBody) string {
	var systemPart, userPart []byte
	for _, m := range parsed.Messages {
		switch strings.ToLower(strings.TrimSpace(m.Role)) {
		case "system", "developer":
			if systemPart == nil {
				systemPart = m.Content
			}
		case "user", "human":
			if userPart == nil {
				userPart = m.Content
			}
		}
		if systemPart != nil && userPart != nil {
			break
		}
	}
	// Responses API style payloads carry instructions/input instead of messages.
	if systemPart == nil && len(parsed.Instructions) > 0 {
		systemPart = parsed.Instructions
	}
	if userPart == nil && len(parsed.Input) > 0 {
		userPart = firstInputEntry(parsed.Input)
	}
	if len(systemPart) == 0 && len(userPart) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write(systemPart)
	h.Write([]byte{0})
	h.Write(userPart)
	return "fp:" + hex.EncodeToString(h.Sum(nil))[:32]
}

// firstInputEntry returns the first element of a Responses API `input` array,
// or the raw value when it is not an array. Only the first entry is used so the
// fingerprint stays stable as the conversation grows.
func firstInputEntry(input json.RawMessage) json.RawMessage {
	var arr []json.RawMessage
	if err := json.Unmarshal(input, &arr); err != nil {
		return input
	}
	for _, entry := range arr {
		var msg sessionMessage
		if err := json.Unmarshal(entry, &msg); err == nil {
			role := strings.ToLower(strings.TrimSpace(msg.Role))
			if role != "" && role != "user" && role != "human" {
				continue
			}
			if len(msg.Content) > 0 {
				return msg.Content
			}
		}
		return entry
	}
	return nil
}
