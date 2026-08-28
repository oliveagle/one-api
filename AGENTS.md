# AGENTS.md — Guidance for AI Coding Agents

This file orients contributors (human and agent) working on this repo.
Read it before changing relay behavior or adding channel support.

## Three test categories — the development model

The relay sits between coding-agent clients (which are standardizing on
the OpenAI **Responses API**, `POST /v1/responses`) and upstream channels
whose capabilities vary. There are exactly three data-flow paths through
the relay, and each one is pinned by its own category of integration
test. **When you change channel/provider behavior, extend the relevant
categories before touching anything else.**

| Category | Client speaks | Upstream speaks | Channel config | Test file |
|---|---|---|---|---|
| **1. responses → responses** | Responses API | Responses API (native) | `support_responses: true` | `relay_mock_categories_test.go` |
| **2. chat → chat** | Chat Completions | Chat Completions | (default) | `relay_mock_integration_test.go` |
| **3. responses on chat-only** | Responses API | Chat Completions only | (no flag) | `relay_mock_categories_test.go` |

All three categories run against the **built-in mock channel**
(`channeltype.Mock` / `apitype.Mock`), whose adaptor
(`relay/adaptor/mock/adaptor.go`) synthesizes upstream responses
in-process — no network. Behavior is selected per-request via the
`X-Mock-Behavior` header.

### The rule: pin 1 & 2, keep 3 refusing

Protocol conversion between the Responses and Chat Completions APIs has
been REMOVED (2026-08): a Responses request that lands on a channel whose
upstream does not natively serve the Responses API is refused with 503
(`responses_unsupported_on_channel`) so the relay's failover walks the
rest of the pool; symmetrically, a chat request landing on a
`responses_only` channel (or an `OpenAIResponses`-type channel) is
refused with 503 (`chat_unsupported_on_channel`). Pools are split by
wire protocol instead (e.g. coding_resps / coding_chat).

When you add or change how a provider behaves:

1. **First, extend Category 1 and/or Category 2** to pin the provider's
   *native* response shape (add a new `X-Mock-Behavior` value + a test
   that asserts on the exact shape the provider returns). These are the
   ground truth — they capture what real upstreams actually emit.
2. **Keep Category 3 pinning the refusal + failover semantics** — never
   resurrect conversion; if a model must serve both wires, give it both
   pool names.

### How the mock channel works

- `relay/adaptor/mock/adaptor.go` — the adaptor. `DoRequest` builds a
  canned `*http.Response` based on `X-Mock-Behavior`; `DoResponse`
  delegates to the OpenAI `Handler`/`StreamHandler` so usage/quota run
  through the real code paths. It **never touches the network**.
- Accepted behaviors: `openai-chat`, `openai-stream`, `openai-tool-call`,
  `openai-responses`, `openai-responses-stream`, `error-429`,
  `error-500`, `error-400`, `empty`. Add new ones when a provider shape
  isn't covered.
- The integration harness lives in
  `controller/relay_mock_integration_test.go` (`setupMockRelayStack` /
  `setupMockRelayStackWithOptions`). It seeds User+Token+Channel into
  SQLite, enables the in-memory channel cache, and wires the real
  `TokenAuth → Distribute → controller.Relay` chain.

### Where the Responses passthrough lives

- `relay/controller/responses.go` — `relayResponsesCreate` forwards the
  body byte-for-byte when `upstreamSupportsResponses(meta)` is true;
  otherwise it refuses with 503 `responses_unsupported_on_channel`
  (retryable, so failover reaches a capable channel). There is NO
  conversion layer anymore — `responses_convert*.go` were deleted.
- `upstreamSupportsResponses` returns true for
  `config.support_responses`, `config.responses_only`, the
  `OpenAIResponses` channel type (new: upstream serves the Responses
  API natively; chat requests to it are refused with 503
  `chat_unsupported_on_channel` by `RelayTextHelper`), and AIHubMix
  (see docs/adr/0001-openai-responses-api-passthrough.md).
- `relay/controller/text.go` — `RelayTextHelper` refuses chat requests
  on `responses_only`/`OpenAIResponses` channels with 503 so failover
  reaches a chat channel.
- `model.ChannelConfig.SupportResponses` / `.ResponsesOnly` — the
  per-channel flags.
- Channel-name model addressing (codex /model use case): request model
  `"channel-name"` serves the channel's `config.default_model`, and
  `"channel-name/model"` serves any model from that channel's list.
  Resolved in `middleware.resolveChannelAddressedModel` BEFORE pool
  routing; a name the pool can already route (`model.IsPoolRoutable`)
  is never hijacked, so the feature is purely additive. The synthetic
  mapping rides the normal model_mapping machinery
  (`ctxkey.ModelMappingOverride`), and /v1/models advertises
  addressable channel names. Pinned by TestChannelAddressing_* in
  `relay_mock_categories_test.go`.

## Test infrastructure notes

- **No network in tests.** CI runs `go test -race -count=1` hermetically.
  The mock channel and `testutil.MockTransport` exist to keep it that way.
- **`relay/controller.PostConsumeQuotaSynchronous`** — a test hook that
  runs quota settlement inline instead of as a detached goroutine. The
  integration harness flips it to `true`; production leaves it `false`.
  This eliminates a data race on global state that the race detector
  flags when goroutines outlive the test.
- **`model.BatchUpdate()`** — flushes staged quota increments to the DB.
  Tests call it after a request to assert on `User.UsedQuota`.
- **`config.ApproximateTokenEnabled`** — the harness sets this so token
  counting doesn't depend on the tiktoken encoder (which downloads BPE
  files on first use — a network dependency).

## Provider-specific tests (real adaptors)

The mock-channel tests above verify the *relay pipeline* but bypass the
real adaptor factory (they hardcode `apitype.Mock`). To catch bugs in a
**specific provider's adaptor** — Azure's deployment URL, OpenRouter's
attribution headers, the dot-stripping in Azure model names, etc. — use
the provider-specific harness in
`controller/relay_provider_integration_test.go`.

### How it works

`setupProviderStack(t, opts)` seeds a channel with a **real channeltype**
(e.g. `channeltype.Azure`), so `relay.GetAdaptor` returns the production
adaptor. The outbound HTTP is intercepted by `testutil.MockTransport`,
which is installed as `client.HTTPClient`. The test registers a handler
that:

1. **Captures the request** the adaptor built (URL path, query, headers)
   so you can assert on the provider-specific adaptation.
2. **Returns a canned response** so no network is needed.

This is the layer that catches "the Azure URL builder drifted" or
"OpenRouter stopped sending attribution headers" — regressions the mock
channel cannot surface.

### Adding a provider test

1. Pick the `channeltype` constant and the base URL the provider expects.
2. Call `setupProviderStack` with `providerStackOptions{channelType, baseURL, models, configJSON}`.
3. Register a `MockTransport.Match` handler on the URL prefix the adaptor
   will build. Inside the handler, assert on `r.URL.Path`,
   `r.Header.Get("Authorization")` / `"api-key"`, etc.
4. POST a chat request via `doRelayRequest` and assert the response comes
   back parsed correctly (the real adaptor's `DoResponse` handles it).

### Provider tiers (divergence from OpenAI baseline)

- **Tier 1 (OpenAI passthrough)**: `ConvertRequest` returns the request
  unchanged, reuses `openai.Handler`. E.g. plain OpenAI, Zhipu. The
  `TestProvider_Tier1_OpenAIPassthrough_ExplicitBaseURL` table test
  pins every such channel type; `TestProvider_Tier1_DefaultBaseURLFallback`
  pins the default base URL each type falls back to (including Groq's
  `/openai` and Novita's `/v3/openai` path segments).
- **Tier 2 (OpenAI-compatible with quirks)**: same `openai.Adaptor` but
  branches on `channeltype` in `GetRequestURL` / `SetupRequestHeader`.
  E.g. Azure (deployment URL + `api-key` header), OpenRouter (extra
  attribution headers), Minimax/Doubao/BaiduV2/AliBailian/
  GeminiOpenAICompatible (URL path tweaks), OpenAICompatible/AIHubMix
  (path normalization), Cloudflare AI Gateway (prefix stripping).
  Pinned in `relay_provider_integration_test.go` +
  `relay_provider_tier2_test.go` — each branch is a potential
  regression point.
- **Tier 3 (fully proprietary format)**: own `model.go` + real
  conversion (Anthropic, Gemini, Ali, Baidu, Tencent, Cohere, Coze,
  DeepL, Ollama, PaLM, AIProxyLibrary, Cloudflare, Zhipu v3, VertexAI).
  Pinned in `relay_provider_proprietary_a_test.go` (anthropic, gemini,
  zhipu, vertexai) and `relay_provider_proprietary_b_test.go` (the
  rest) with canned fixtures of each provider's proprietary response
  format.

### Provider quirk coverage matrix

| Provider | Pinned quirks | Test |
|---|---|---|
| OpenAI | `/v1/chat/completions` + Bearer; default base | `TestProvider_OpenAI_StandardURLAndAuth`, tier-1 tests |
| Azure | deployment URL (dots stripped), `api-key` header, `api-version` query | `TestProvider_Azure_*` |
| OpenRouter | `HTTP-Referer` + `X-Title` attribution, default base | `TestProvider_OpenRouter_AddsAttributionHeaders`, tier-1 |
| Minimax | `/v1/text/chatcompletion_v2` | tier-2 |
| Doubao | `/api/v3/chat/completions` | tier-2 |
| Novita | base verbatim + `/chat/completions`, default `/v3/openai` | tier-1 + tier-2 |
| BaiduV2 | `/v2/chat/completions` | tier-2 |
| AliBailian | `/compatible-mode/v1/chat/completions` | tier-2 |
| GeminiOpenAICompatible | strips client `/v1` prefix | tier-2 |
| OpenAICompatible | strips `/v1`, trims trailing slash | tier-2 |
| AIHubMix | `/v1/v1` de-duplication | tier-2 |
| Anthropic | `/v1/messages`, `x-api-key`, version/beta headers (incl. claude-3-5-sonnet override), system hoist, max_tokens=4096 default | proprietary-a |
| Gemini (native) | `:generateContent` URL, per-model version (v1beta for 1.5/2.0 + api_version override), `x-goog-api-key`, `system_instruction`, safety settings, snake_case keys, dummy "Okay" model turn | proprietary-a |
| Zhipu | v4 URL + locally-generated HS256 JWT as **bare** Authorization (no Bearer), TopP/Temperature clamp to [0,1], v3 `model-api` fallback | proprietary-a |
| VertexAI | project/region URL, cached GCP token, `:rawPredict` for claude + `anthropic_version: vertex-2023-10-16`, model field omitted | proprietary-a |
| Ali | generation endpoint, `-internet` suffix → `enable_search`, lowercased roles, TopP ≤ 0.9999 clamp | proprietary-b |
| Baidu (v1) | OAuth token exchange (`apiKey\|secretKey`), ERNIE model → endpoint mapping, `access_token` query, system hoist, `penalty_score` rename | proprietary-b |
| Tencent | TC3-HMAC-SHA256 auth, `X-TC-Action/Version/Timestamp` headers, PascalCase body | proprietary-b |
| Cohere | `/v1/chat`, last-user-message extraction, SYSTEM/CHATBOT roles, `-internet` → web-search connector | proprietary-b |
| Coze | `bot-` prefix trim, forced `user` from channel config, last message → query, answer-only filtering | proprietary-b |
| DeepL | `DeepL-Auth-Key` scheme, `target_lang` from model name | proprietary-b |
| Ollama | `/api/chat`, `max_tokens` → `options.num_predict` | proprietary-b |
| PaLM | hardcoded `chat-bison-001`, `x-goog-api-key`, author 0/1, `topK` fed from MaxTokens | proprietary-b |
| AIProxyLibrary | `/api/library/ask`, last-message-only query, `libraryId` from config | proprietary-b |
| Cloudflare | account-scoped URL from `user_id` config, AI-gateway passthrough form | proprietary-b |
| channel Headers | override Content-Type / add custom headers | tier-2 |
| (all) | stream Accept default; fast path forwards client body verbatim (no stream_options injection) | tier-2 |

### Providers that cannot run through the full stack (and why)

These four are pinned at the unit level instead — do NOT try to point
them at `testutil.MockTransport`, their transport is out of reach:

- **AwsClaude** (`relay/adaptor/aws/`): the Bedrock SDK client does
  SigV4-signed calls to region endpoints; `GetRequestURL` returns "".
  Pinned: `aws/registry_test.go` (model → sub-adaptor dispatch,
  Bedrock inference-profile ID maps).
- **Xunfei v1** (`relay/adaptor/xunfei/`): websocket to a hardcoded
  `wss://spark-api.xf-yun.com` host — even non-stream chat dials WS.
  Pinned: `xunfei/domain_test.go` (model → version → domain resolution,
  auth URL shapes). Use XunfeiV2 (OpenAI-compatible HTTP) for
  full-stack coverage.
- **Replicate** (`relay/adaptor/replicate/`): hardcoded
  `https://api.replicate.com` + `http.DefaultClient` (bypasses the
  shared client) + blocking 3s poll loop. Pinned:
  `replicate/adaptor_test.go` (stream-only gate, prompt flattening,
  sampling defaults).
- **Proxy** (`relay/adaptor/proxy/`): raw byte passthrough on a
  dedicated route (`/v1/oneapi/proxy/...`), not the chat pipeline;
  `ConvertRequest` deliberately errors. No quirks to pin beyond the
  byte copy itself.
