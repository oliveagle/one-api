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
| **3. responses → chat** | Responses API | Chat Completions only | (no flag) | `relay_mock_categories_test.go` |

All three categories run against the **built-in mock channel**
(`channeltype.Mock` / `apitype.Mock`), whose adaptor
(`relay/adaptor/mock/adaptor.go`) synthesizes upstream responses
in-process — no network. Behavior is selected per-request via the
`X-Mock-Behavior` header.

### The rule: pin 1 & 2, then improve 3

When you add or change how a provider behaves:

1. **First, extend Category 1 and/or Category 2** to pin the provider's
   *native* response shape (add a new `X-Mock-Behavior` value + a test
   that asserts on the exact shape the provider returns). These are the
   ground truth — they capture what real upstreams actually emit.
2. **Then, improve Category 3** (the responses→chat conversion) so the
   converted output matches what a Responses-API client expects, validated
   against the shapes pinned in step 1.

Doing it in the other order (changing the conversion without first
pinning the native shapes) is how the conversion path drifts away from
reality and silently breaks coding agents.

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

### Where the conversion lives

- `relay/controller/responses.go` — `relayResponsesCreate` branches on
  `upstreamSupportsResponses(meta)`: if true, passthrough; if false,
  convert via `relayResponsesConvertToChat` → `RelayTextHelper`.
- `relay/controller/responses_convert.go` — the conversion core
  (`convertResponsesToChatCompletions`, input→messages mapping). Unit
  tests in `responses_convert_test.go`.
- `model.ChannelConfig.SupportResponses` — the per-channel opt-in flag.
- `common/ctxkey.ConvertedFromResponses` — marks converted requests so
  the chat pipeline forces the slow (per-channel adaptor) body path.

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
