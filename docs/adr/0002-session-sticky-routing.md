# ADR-0002: Agent Session-Sticky 路由与故障转移

- 日期: 2026-08-02
- 状态: ✅ 已决策
- 决策者: oliveagle
- 相关代码: `relay/routing/`, `middleware/distributor.go`, `controller/relay.go`, `model/cache.go`

## 1. 背景 (Context)

所有 channel 都通过 `model_mapping` 把 `coding_medium` 映射到各上游模型。当前 one-api
的渠道选择是 **随机轮训**:`CacheGetRandomSatisfiedChannel` 在每个同优先级 channel 之间
随机挑一个,失败重试时再随机挑一个。

这对 coding agent 会话不理想:

- 同一 agent session 的多轮请求会落在**不同**上游节点,导致会话级上下文(prompt 缓存、
  KV memory、工具历史、会话内状态)反复丢失/重建,性能与一致性都差。
- 随机轮训无法感知节点健康度,限流、额度用完的节点仍会被随机选中。

## 2. 问题 (Problem)

需要一个 **session-sticky** 的路由模块:

1. 同一 agent session 的请求始终路由到**同一个上游节点**;
2. 当该节点返回可重试错误(限流 429、额度用完、5xx)时,**故障转移**到其它健康节点,
   并把 session 重新固定(pin)到新节点,后续请求继续走新节点;
3. 不破坏现有的非会话请求(继续随机轮训)与指定渠道逻辑。

## 3. 方案 (Solution)

新增独立路由包 `relay/routing`,由 `Router` + `Store` 组成:

- `Store`(进程内,线程安全):保存 `session -> channelId` 绑定,以及节点失败后的
  **冷却期**(cooldown),避免会话立刻弹回刚失败的节点。
- `Router`:
  - `Choose(group, model, session)` — 首次请求为会话随机挑选并**固定**一个节点;后续请求
    命中已固定且仍有效的节点则直接复用。
  - `ChooseAlternative(group, model, session, exclude)` — 会话已固定节点失败时,挑一个
    **排除已失败节点**的健康节点并重新固定。
  - `Fail(channelId)` — 失败后将该节点放入冷却期。
- `channelProvider` 接口抽象了渠道候选查询,使选择逻辑可脱离数据库做单元测试;生产实现
  复用内存渠道缓存 `model.CacheGetSatisfiedChannels`。

### 3.1 Session 标识提取

`routing.ResolveSession(c)` 返回 `(sessionKey, source)`,按顺序取第一个非空值:

1. **header**:配置的 `SESSION_ID_HEADER`(默认 `X-Session-Id`),随后是一组
   well-known 别名(`X-Conversation-Id`、`X-Chat-Id`、`X-Thread-Id`、`Session-Id`、
   `X-Agent-Session-Id` 等),因为不同 agent 前端拼写不一致。
2. **body**:显式 session id — 配置的 `SESSION_ID_BODY_FIELD`、`session_id`、
   `session`、`conversation_id`、`previous_response_id`(Responses API),以及
   `metadata.{session_id,session,conversation_id,thread_id}`。
3. **fingerprint**:会话指纹(见 3.1.1)。

无标识则返回空串,请求退化为随机轮训。

#### 3.1.1 会话指纹(fingerprint)—— 为什么必须有

**实测结论(抓包证据)**:通过日志代理录下 `pix`(pi coding agent,基于 OpenAI JS SDK
6.26.0)的真实请求,它**完全不发送任何会话标识**:

- header 只有 `User-Agent: OpenAI/JS`、`X-Stainless-{Lang,OS,Arch,Runtime,
  Runtime-Version,Package-Version,Retry-Count,Timeout}`、`Authorization`、
  `Content-Type` 等 —— 其中 `X-Stainless-*` 是 SDK 环境指纹,**同一台机器上所有会话
  完全相同**,无法区分会话;
- body 顶层只有 `model` / `messages` / `stream` / `stream_options` / `store` /
  `max_completion_tokens` / `tools` —— 没有 `session_id`,也没有 `metadata`。

所以仅靠 header/body 提取,`sessionKey` 恒为空,sticky 路由对 pix **完全不生效**,
每轮请求都退化成随机轮训(这正是"pix 进去之后仍然是随机的 provider"的根因)。

指纹方案:取请求中**随会话增长而不变的前缀**做 SHA-256:

- system / developer 指令(agent 的 system prompt),加上
- **第一条** user 消息。

同一 agent 会话的每一轮都会完整重放这段前缀、只在后面追加 assistant/tool 消息,
因此指纹在会话内恒定;不同会话的首条 user 消息不同,因此指纹互不相同。
Responses API 风格的 body 用 `instructions` + `input` 首个条目等价处理。
指纹 key 带 `fp:` 前缀,便于在管理页面区分来源。

`content` 以原始 JSON(`json.RawMessage`)参与哈希,因此 `"content":"text"` 与
`"content":[{"type":"text",...}]` 两种形态都稳定。

> 关掉指纹:`SESSION_FINGERPRINT_ENABLED=false`,此时要求客户端显式带 session id。

### 3.2 集成点

- `middleware/distributor.go` 的 `Distribute`:用 `ResolveSession` 取 session key 与来源,
  用 `Router.Choose` 选渠道(替代原来的 `CacheGetRandomSatisfiedChannel`);
  指定渠道(SpecificChannelId)路径保持不变。key 与 source 都写入 gin context
  (`ctxkey.SessionKey` / `ctxkey.SessionSource`),并打一条 debug 日志说明本次请求
  是否真的走了 sticky(`StickyAppliesTo`)。

  > 修复:此前只有 header/body 来源的 key 会写入 context,token 兜底的 key 不会,
  > 导致重试循环读不到 sessionKey、故障转移退回随机挑选。现在**任何**来源的 key 都写入。
- `controller/relay.go` 的 `Relay` 重试循环:当 `sessionKey` 存在且 sticky 启用时,
  用 `Router.ChooseAlternative` 做故障转移并把 session 重新固定;仅在可重试错误时把失败
  节点 `Fail`(冷却),非可重试错误(如客户端 400)不会冷却节点。

### 3.3 配置项(`common/config`)

| 环境变量 | 默认 | 说明 |
|----------|------|------|
| `STICKY_ROUTING_ENABLED` | `false` | 总开关 |
| `STICKY_MODELS` | 空(全部) | 参与 sticky 的模型白名单,逗号分隔 |
| `SESSION_ID_HEADER` | `X-Session-Id` | session id header 名 |
| `SESSION_ID_BODY_FIELD` | `session_id` | 请求体顶层字段名(兜底) |
| `STICKY_COOLDOWN_SECONDS` | `60` | 失败节点冷却期,0 表示禁用 |
| `SESSION_FINGERPRINT_ENABLED` | `true` | 无显式 session id 时用会话指纹兜底(pix 等客户端必需) |
| `STICKY_FALLBACK_TO_TOKEN` | `false` | 连指纹都取不到时,按 API token 固定节点(粒度粗,默认关) |

### 3.4 故障转移语义

会话固定节点返回可重试错误时:

1. `Router.Fail` 将该节点冷却 `STICKY_COOLDOWN_SECONDS`;
2. `Router.ChooseAlternative` 从候选池中**排除**本次已失败的节点(以及仍在冷却的节点),
   随机挑一个并重新固定;
3. 若同一请求内后续又失败,继续排除已尝试节点,直到无可选节点或达到 `RetryTimes` 上限。

## 4. 备选方案 (Options)

### A. 纯内存 Store(选中)

复用 one-api 已有的**单实例内存渠道缓存**模式,Store 也放进程内。
- ✅ 实现简单,与现有 channel cache 生命周期一致,零外部依赖
- ❌ 多实例部署时各实例独立绑定,跨实例 session 可能落不同节点

### B. Redis 后端 Store(未选,预留)

把 `Store` 换成 Redis 键值实现,支持跨实例强一致 sticky。
- ✅ 多实例场景严格 sticky
- ❌ 依赖 Redis,增加运维与测试成本;当前单实例部署用不到

`Store` 已封装成独立类型,后续可在不改变 `Router` 接口的前提下替换为 Redis 实现。

### C. 失败后不重固定(未选)

失败转移仅本次请求用别的节点,但 session 仍保留原固定节点。
- ❌ 每次请求都会先打失败节点再转移,反复抖动,违背故障转移初衷。

## 5. 测试

- `relay/routing/router_test.go`:`Router` 的 sticky、allowlist、故障转移重固定、冷却、
  stale 绑定重建、优先级选择等逻辑,用 `fakeProvider` 注入候选渠道,无需数据库。
- `relay/routing/session_test.go`:session key 提取(header 兜底字段、嵌套 metadata、自定义字段)。
- 现有 `controller`、`middleware`、`relay/...`、`model` 测试均保持通过。

## 6. 影响 (Impact)

- 未启用 `STICKY_ROUTING_ENABLED` 时,行为与原来完全一致(随机轮训)。
- 启用后,仅带 session 标识且模型在 `STICKY_MODELS` 白名单内的请求走 sticky 路径。

## 7. 管理页面与实时状态 (Routing 页面)

新增 **Routing 管理页面**（`/routing`,仅管理员）用于查看与管理 session-sticky 路由的实时状态:

- **路由配置状态**:是否启用(`STICKY_ROUTING_ENABLED`)、参与模型白名单(`STICKY_MODELS`)、
  失败冷却秒数、会话保留 TTL、session id header。
- **会话状态**:每个活跃 session 的 session_key、模型、分组、绑定的渠道 ID、请求数、
  失败数、最后活动时间,按最后活动倒序;支持单条 **解绑**(下次请求重新固定到新节点)。
- **渠道状态**:每个渠道当前承载的会话数、是否处于冷却期(最近失败)。
- **实时性**:前端每 5 秒自动轮询 `/api/routing/status`(可开关),刷新按钮手动刷新,
  “清空所有会话”按钮重置全部绑定与冷却。

### 7.1 后端 API(`router/api.go`,均需 `AdminAuth`)

| 方法 | 路径 | 说明 |
|------|------|------|
| GET    | `/api/routing/status` | 返回配置状态 + 会话快照 + 渠道状态 |
| DELETE | `/api/routing/session` | 解绑单个 session(请求体 `{"session_key":"..."}`) |
| DELETE | `/api/routing/sessions` | 清空所有 routing 会话与冷却 |

### 7.2 观测数据来源

`relay/routing.Store` 现在同时维护:
- `bindings`:session -> `SessionRecord`(含 requests / failures / first_seen / last_seen);
- `channelSessions`:渠道 -> 活跃会话计数(会话迁移时正确移动);
- `cooldown`:渠道 -> 冷却截止时间。

`Snapshot()` / `ChannelStates()` 提供给管理页面。会话记录按 `STICKY_SESSION_TTL_SECONDS`
(默认 24h)自动清理,避免无限增长。

修复:
- **sticky 命中也要 `Touch`**。此前只有"新建绑定"才写 store,命中已固定节点直接返回,
  于是 `requests` 永远停在 1,更严重的是 `LastSeen` 不再推进 —— TTL 清理会把**正在活跃**
  的会话剪掉,导致它被重新随机固定。命中路径现在也记账(且不会重复计 channel 会话数)。
- **冷却中但已无会话的节点仍要展示**。故障转移后会话已迁走,原节点 `channelSessions`
  计数归零,旧实现只遍历 `channelSessions`,该节点会从页面消失,运维无法解释流量为何绕开它。

## 8. 端到端验证 (Verification)

真实 `pix` 会话(经日志代理录制 + `X-Oneapi-Channel` 响应头观测):

| 场景 | 结果 |
|------|------|
| 单会话 7 轮(逐步追加 tool 调用) | 7 轮全部落在 `openrouter`,routing 页面显示 1 个 session / requests=7 / 1 个 channel |
| 4 个不同 pix 会话 | 分别落到 `aihubmix` / `minimax` / `openrouter` / `volc-t-1`(证明不是退化成"整个 token 钉死一个节点") |
| 固定节点被禁用后 | 会话重新固定到健康节点(`openrouter` → `aihubmix`)并保持稳定 |
| 固定节点持续 5xx(`RetryTimes=3`) | 请求内故障转移成功,会话稳定在 `volc-t-1`,session 记录 failures=1 |

单元测试:`relay/routing/session_pix_test.go` 用 `testdata/` 下**真实录制**的 pix 请求体
断言"同会话跨轮同 key、不同会话不同 key、显式 header 优先于指纹",并覆盖命中记账与
冷却节点可见性两处修复。
