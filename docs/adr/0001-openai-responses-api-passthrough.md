# ADR-0001: OpenAI Responses API 支持(透传方案)

- 日期: 2026-08-01
- 状态: ✅ 已决策
- 决策者: oliveagle
- 相关代码: `router/relay.go`, `relay/relaymode/`, `relay/controller/responses.go`

## 1. 背景 (Context)

用户询问 one-api 是否支持 OpenAI Responses API(`POST /v1/responses`)。实测结论:

| 层 | 状态 | 证据 |
|----|------|------|
| `router/relay.go` | 无 `/v1/responses` 路由 | 路由表全文无匹配 |
| `relay/relaymode/define.go` | 无 `Responses` 常量 | 11 个 mode 常量,无 Responses |
| `relay/relaymode/helper.go` | `GetByPath` 无映射 | 落到 `Unknown` |
| 全仓库 | `grep -rn "v1/responses"` **零匹配** | — |
| 本地实例 (3793) | **404** `Invalid URL (POST /v1/responses)` | 实测 |
| 上游 aihubmix | **200 已支持** | 实测,返回 `object:"response"` |

缺口 100% 在 one-api 侧,不是上游限制。

Responses API 是 OpenAI 2026 年主推的接口,与 ChatCompletions 的关键差异:

- 输入是 `input`(string 或 item 数组)+ `instructions`,不是 `messages`
- 输出是 `output[]`(item 数组,含 `message` / `reasoning` / `function_call` 等类型),不是 `choices[]`
- usage 字段名是 `input_tokens` / `output_tokens`,**不是** `prompt_tokens` / `completion_tokens`
- 具备 **stateful** 能力:`previous_response_id`、`store`、服务端内置 tools(web_search / file_search / code_interpreter)
- 流式事件是 `response.output_text.delta` 等语义化事件,**不是** chat 的 `choices[].delta`

## 2. 问题 (Problem)

现有文本管线 `RelayTextHelper` 完全绑死 `GeneralOpenAIRequest`:

- `getAndValidateTextRequest` 只反序列化为 chat 结构
- `getPromptTokens` 按 relayMode switch,只认 `Messages` / `Prompt` / `Input`
- `openai.StreamHandler` 解析 chat SSE(`choices[].delta`)
- 计费依赖预扣 `promptTokens` + 响应 `usage` 回冲,而 Responses 的 usage 字段名不同

直接把 `/v1/responses` 挂到 `RelayTextHelper` 会导致:请求被 chat 结构吞掉字段、token 计数为 0、响应解析失败。

## 3. 备选方案 (Options)

### A. 透传 (Passthrough) — 约 80 行 ✅ 选中

新增 `relaymode.Responses` + 路由,请求体原样透传给上游,响应原样回吐。
按 `input` 估算 token 预扣,从响应 `usage.input_tokens` / `output_tokens` 回冲。

- ✅ 改动小、风险低,不触碰现有 chat 管线
- ✅ 非流式 + 流式都能通(SSE 直接 copy,无需翻译事件)
- ✅ stateful 特性(`previous_response_id` / `store` / 内置 tools)天然可用,因为不做语义改写
- ✅ 计费、模型路由、负载均衡、渠道重试全部复用现有机制
- ❌ 只对**原生支持 Responses 的上游**有效(OpenAI / aihubmix);不支持的上游会返回其自身的 404
- ❌ `model_mapping` 只能改模型名,不做请求体深度改写

### B. 完整转换层 — 约 600–1000 行 ❌ 未选

新增 `ResponsesRequest` / `ResponsesResponse` 类型,双向转换 Responses ↔ ChatCompletions,
使所有 channel 都能接受 Responses 请求。

- ✅ 任何上游都能用
- ❌ **stateful 语义无法忠实映射**:`previous_response_id` 要求服务端保存历史,chat 是无状态的;
  `store` / 内置 tools 在 chat 上没有对应物。强行转换等于**对客户端撒谎** —— 请求看似被接受,
  实际语义被静默丢弃。这是本方案被否的**主要理由**。
- ❌ 需维护一套 SSE 事件翻译(chat delta → response.output_text.delta),回归面广
- ❌ 工作量与收益不匹配(当前实际上游 aihubmix 已原生支持)

### C. 复用现有 Proxy 模式 — 0 行 ❌ 未选

`POST /v1/oneapi/proxy/<channelid>/v1/responses` 当前即可用。

- ✅ 零代码
- ❌ 需手填 channel id,**不参与模型路由 / 负载均衡 / 故障转移**
- ❌ `RelayProxyHelper` 丢弃 usage → **完全不计费**(对多用户实例是漏洞)

## 4. 决策结果 (Decision)

**采用方案 A(透传),同时支持非流式与流式。**

理由:
1. 覆盖真实场景 —— 实际上游 aihubmix 已原生支持,透传即可用
2. 保住计费 —— 从上游 usage 精确回冲,不像 C 方案漏计费
3. 诚实 —— 不像 B 方案伪造 stateful 语义;不支持的上游会自然报错,而非静默降级
4. 风险可控 —— 不修改现有 chat 管线,新增独立 helper

### 实现要点

- `relaymode.Responses` 新常量 + `GetByPath` 映射 `/v1/responses`
- `router/relay.go` 注册 `relayV1Router.POST("/responses", controller.Relay)`
- `relayHelper` 新 case → `controller.RelayResponsesHelper`
- 新文件 `relay/controller/responses.go`:独立管线,不复用 `getAndValidateTextRequest`
- 请求体透传:仅解析 `model` / `stream` 用于路由与计费,其余字段不动
- 预扣:按 `input` + `instructions` 估 token(复用 `openai.CountTokenInput`)
- 回冲:解析响应 `usage.input_tokens` / `output_tokens` 映射到内部 `Usage.PromptTokens` / `CompletionTokens`
- 流式:SSE 直接 copy 到客户端,同时扫描事件提取末尾 usage 用于回冲

### 已知不支持(显式声明,不假装支持)

- 非原生支持 Responses 的上游(DeepSeek / Ali / Baidu 等)会返回其自身错误,one-api 不做兼容转换
- 不做 Responses ↔ ChatCompletions 互转

## 5. 约束边界 (Constraints)

### 5.1 架构隔离约束声明

| 约束 | 本决议的立场 | 说明 |
|------|------------|------|
| 1. 无循环依赖 | ✅ 遵守 | `relay/controller/responses.go` 依赖方向与既有 `text.go` / `proxy.go` 完全一致:controller → relay/adaptor → relay/model。不新增反向边。 |
| 2. 分层向下依赖 | ✅ 遵守 | 严格 `router → controller → relay/controller → relay/adaptor`。新增 helper 位于既有 `relay/controller` 层,不跨层直调。 |
| 3. God package 阈值 (≤100 文件/包, ≤500 行/文件) | ✅ 遵守 | 新增独立文件 `responses.go`(约 150 行),不塞进已有 `text.go`(当前 120 行)。`relay/controller` 包当前 10 文件,远低于阈值。 |
| 4. 主题域边界清晰 | ✅ 遵守 | Responses 是独立 relay mode,与 chat / image / audio / proxy 并列,不侵入其他域的代码路径。 |
| 5. bridge/adapter 显式化 | ✅ 遵守 | 复用既有 `adaptor.Adaptor` 接口,不新增隐式跨层调用。透传不需要新 adapter 方法,因此**不扩展 Adaptor 接口**(避免迫使 20+ 个 adaptor 实现空方法)。 |
| 6. 测试文件跟随 | ✅ 遵守 | 同 commit 提交 `relay/relaymode/helper_test.go`(路径映射)与 `relay/controller/responses_test.go`(usage 解析 / token 估算)。 |

### 5.2 其他约束

- 不修改 `GeneralOpenAIRequest`(避免影响所有 chat 路径)
- 不扩展 `Adaptor` 接口(20+ 实现会被迫改)
- 计费口径与 chat 一致:`model_ratio` 按模型名查,与端点无关

## 6. 实现期发现 (Implementation Notes)

实现与验证过程中发现两点,补记于此:

### 6.1 SSE 必须逐行原样透传,不能用 `render.StringData`

chat 流式路径用 `common/render.StringData`,它只发 `data:` 帧。
但 Responses 协议的语义事件名在**独立的 `event:` 行**上
(`event: response.output_text.delta`),客户端靠它做分派。
若沿用 chat 惯用法,`event:` 行会被静默丢弃,客户端收到一串无名 data 帧。

因此 `relayResponsesStream` 用 `bufio.Scanner` 逐行 `Fprintf` + `Flush` 原样写出。
实测确认 4 个 `event:` 行全部保留。
另外把 scanner buffer 提到 1MB(`maxResponsesStreamLine`),因为 Responses 帧可能
内嵌较大的 tool payload,默认 64KB 不够。

### 6.2 缺 `model` 时返回 503 而非 400 — 属既有行为,不在本决议范围

`middleware.shouldCheckModel` 没有 `/v1/responses` 分支。但补上它**无效**:
`getRequestModel` 仅在 unmarshal 失败时返回 error,model 为空字符串时返回 `nil`,
所以 `shouldCheckModel` 对该场景是死代码。

实测 `/v1/chat/completions` 不带 model **同样返回 503**
(`当前分组 default 下对于模型  无可用渠道`),即这是**既有且跨端点一致**的上游行为,
不是 Responses 引入的缺陷。曾尝试的 `shouldCheckModel` 补丁已回滚,
避免留下看似生效实则无效的代码。如需统一改成 400,应另开决议覆盖所有端点。

### 6.3 `model` 在 Responses spec 里是可选的 — 曾误加硬校验,已删除

对照 AIHubMix API reference(`/en/api-reference/openai-compatible/create-a-model-response`)
与 OpenAI migrate-to-responses 指南复核后发现:请求体里**只有 `Authorization` 标注 required**,
`model` 没有 required 标记(对比响应体的 `id` / `object` 明确写 required)。
文档给出的 curl 示例本身就**不含 `model`**。

原实现在 `model == ""` 时直接返回 400 `model is required`,与文档契约冲突。
但实测发现该分支是**不可达的死代码**:`middleware.Distribute` 先于本 helper 执行,
`CacheGetRandomSatisfiedChannel(group, model)` 以 group+model 为键,
缺 model 时直接返回 503 `无可用渠道`。`/v1/chat/completions` 行为完全一致。

结论:one-api **架构上必须有 model 才能选渠道**,这是网关的固有约束而非协议要求。
已删除该 400 校验(避免既违反文档、又永不生效),把拒绝职责留在 middleware 单点,
并加测试锁定"缺 model 时仍能正常反序列化"(用文档原样示例做 fixture)。

### 6.4 文档复核确认的其他两点

- `max_output_tokens` 是 Responses 的正确拼写,Responses 无 `max_tokens`
  (文档全文 0 次匹配)。实现读取前者,已加测试确保 `max_tokens` 不会误populate。
- `store` **默认 true**,`previous_response_id` 由服务端串联上下文。
  这反向印证了透传决策:B 方案(转换层)会把这两个语义静默丢弃。

## 7. 后续任务 (Follow-up)

- [x] 实现 `relay/controller/responses.go` + 路由 + relaymode
- [x] 单测:路径映射、usage 字段映射、input token 估算
- [x] 实机验证:非流式 + 流式,确认计费额度正确扣减(本地 mock upstream,见下)
- [x] 文档:README 标注"Responses API 仅对原生支持的上游有效"
- [x] 扩展 CRUD 端点支持 (2026-08-01 完成)

## 8. CRUD 端点扩展 (2026-08-01)

在初始 `POST /v1/responses` 实现完成后,进一步添加了 Responses API 的完整 CRUD 支持:

### 8.1 新增端点

| 端点 | 方法 | 说明 | 计费 |
|------|------|------|------|
| `/v1/responses` | POST | 创建 response | ✅ 计费 |
| `/v1/responses/:response_id` | GET | 获取 response | ❌ 不计费 |
| `/v1/responses/:response_id` | DELETE | 删除 response | ❌ 不计费 |
| `/v1/responses/:response_id/cancel` | POST | 取消进行中的 response | ❌ 不计费 |
| `/v1/responses/:response_id/input_items` | GET | 列出 input items | ❌ 不计费 |

### 8.2 实现方式

采用**单处理器方法分支**模式:

- `RelayResponsesHelper` 作为分发器,根据 HTTP 方法和路径分发到不同的处理器
- `relayResponsesCreate` 处理 POST /responses (带计费)
- `relayResponsesPassthrough` 处理所有 CRUD 操作 (无计费)
- `forwardResponse` 通用函数负责复制上游响应

### 8.3 设计决策

1. **CRUD 操作不计费**: GET/DELETE/cancel/input_items 是检索和管理操作,不消耗 tokens
2. **复用现有基础设施**: 所有端点共享相同的 relay mode、adaptor、middleware 链
3. **路径参数传递**: Gin 的 `:response_id` 自动传递到 `meta.RequestURLPath`,adaptor 正确转发到上游

### 8.4 代码变更

| 文件 | 变更 |
|------|------|
| `router/relay.go` | 添加 4 个新路由 |
| `middleware/distributor.go` | 添加注释说明 GET/DELETE 无 body 的处理 |
| `relay/controller/responses.go` | 重构为分发器 + 5 个处理器 |
| `router/relay_test.go` | 添加新路由测试 |
| `relay/controller/responses_test.go` | 添加方法分发和路径模式测试 |
| `README.md` | 添加 Responses API 功能说明 |
