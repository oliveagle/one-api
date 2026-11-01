# ADR-0005: Command Code CLI 协议适配器（/alpha/generate）

- 日期: 2026-11-01
- 状态: ✅ 已决策
- 决策者: oliveagle
- 相关代码: `relay/adaptor/commandcode`, `relay/channeltype/define.go`, `relay/apitype/define.go`

## 1. 背景 (Context)

Command Code 是一个 coding-agent 订阅服务（Go 套餐）。用户希望把它当作 one-api 的
普通渠道使用：`/v1/chat/completions` 打到本机 one-api，由 one-api 转成 Command Code
上游格式。

调研（2026-11-01，实测）发现它**不是一个 OpenAI-compatible 的 provider**：

| 端点 | 结果 |
|---|---|
| `POST /provider/v1/chat/completions` | 403 `Your Go plan doesn't include API access` |
| `POST /provider/v1/messages` | 403 同上 |
| `POST /alpha/generate` | ✅ 唯一可用 |

也就是说 **Go 套餐没有 Provider API**，唯一可用的入口是 CLI 私有的
`/alpha/generate`。此前用户是用 magpie 网关（opencode 插件）做这层转换的；本 ADR
决定把转换**下沉到 one-api 内部**，做成一个 tier-3 全专有格式 adaptor，
magpie 不再是链路上必需的一环。

`/alpha/generate` 的协议与 OpenAI 有几处硬性差异（全部实测确认）：

1. **必须 `params.stream: true`**。`stream:false` → 400
   `Proxy use detected. This endpoint only serves CLI.`。
   → 非流式请求也必须在**上游侧**流式，由 adaptor 聚合后回吐成一次性 JSON。
2. **请求体不是 OpenAI schema**，而是 CLI 私有 envelope：
   `{config:{workingDir,date,environment,structure,isGitRepo,currentBranch,mainBranch,gitStatus,recentCommits}, memory, taste, skills, permissionMode, params:{model,messages,tools,system,max_tokens,stream}}`。
   `config` 里 `currentBranch`/`mainBranch`/`gitStatus`/`recentCommits` 缺失会 400（schema 校验）。
3. **tools 是扁平结构** `{name,description,input_schema}`，不是 OpenAI 的嵌套
   `{type:"function",function:{...}}`。
4. **`tool_choice` 只接受对象** `{type:"auto"|"any"|"tool",name?}`。字符串
   `"required"`/`"auto"` 会 400（`expected object, received string`）；
   `{"type":"none"}` 也会 400。
5. **响应是「每行一个 JSON 对象」的事件流**，不是 SSE 的 `data:` 前缀格式。
   事件类型：`start` / `start-step` / `text-delta` / `reasoning-delta` /
   `tool-call`（携带完整 input 对象） / `finish-step`（带 `usage`） /
   `finish`（带 `totalUsage`） / `abort` / `error`。
   注意 usage 字段名在两个事件里不同：`finish` 用 `totalUsage`，`finish-step` 用 `usage`。
6. **必需 CLI 头**：`Authorization: Bearer <key>`、`x-api-key: <key>`、
   `User-Agent: cli`、`x-command-code-version: <cli 版本>`、
   `x-cli-environment: production`、`x-project-slug: <slug>`、
   `x-taste-learning: false`、`x-session-id: <uuid>`。
7. `max_tokens` 上限 200000，超了**整请求** 400。

## 2. 备选方案 (Options)

### A. 保持 magpie 网关，one-api 只做转发 ❌

把 one-api 配成普通 OpenAI-compatible 渠道指到 magpie。

- ✅ one-api 侧零改动
- ❌ 链路上多一个必须常驻的第三方进程，magpie 挂了渠道就全断
- ❌ 工具调用 / 推理内容 / usage 的语义在 magpie 里被再翻译一次，出错难定位
- ❌ 用户明确要求「给 one-api 直接添加」

### B. 在 one-api 里加通用协议转换层 ❌

加一个「OpenAI → 任意厂商」的通用转换层。

- ❌ **违反仓库硬规则**：本仓库已于 2026-08 删除 Responses↔Chat 的转换层
  （见 AGENTS.md 与 ADR-0001），核心主张是「宁可 503 拒绝并 failover，
  也不做通用转换」。复活通用层会让这条规则形同虚设。
- ❌ 通用层无法表达 CLI envelope 里那些**私有必填字段**
  （`config.currentBranch` 等）与**私有头**（`x-command-code-version` 等）。

### C. 做一个 Command Code 专属 tier-3 adaptor ✅ 选中

按仓库既有的 tier-3 模式（Anthropic / Gemini / Ali / Baidu / Tencent …）
新写一个 `relay/adaptor/commandcode` 包：

- 自带 `model.go`（私有 wire 类型）
- 自带 `convert.go`（OpenAI 请求 → CLI envelope）
- 自带 `main.go`（CLI 事件流 → OpenAI 响应 / SSE chunk）
- 自带 `models.go`（Go 套餐模型清单）
- 真实事件流 fixture 作为 testdata，测试全程**不联网**

这与仓库的「每家 provider 的差异写在自己的 adaptor 里」一致，也符合 ADR-0003
确立的「provider 差异集中在 provider 域、协议处理留在协议域」的分层。

## 3. 决策结果 (Decision)

采用方案 C。

### 3.1 接入点

| 位置 | 改动 |
|---|---|
| `relay/apitype/define.go` | 新增 `CommandCode` 协议家族 |
| `relay/channeltype/define.go` | 新增 `CommandCode = 58` 渠道类型 |
| `relay/channeltype/helper.go` | `ToAPIType` → `apitype.CommandCode` |
| `relay/channeltype/url.go` | 新增 base URL 槽位 |
| `relay/adaptor.go` | 注册 `apitype.CommandCode → commandcode.Adaptor` |
| `model/channel.go` | `ChannelConfig.CommandCodeZDR`（`commandcode_zdr`） |
| `web/default/...` | 渠道类型下拉 + ZDR 开关（仅 type 58 显示） |

### 3.2 请求转换（`convert.go`）

- **强制 `stream:true`**，与客户端是否要流式无关；非流式由响应侧聚合。
- `system` / `developer` 消息提升为 `params.system`。
- `params.tools` 扁平化为 `{name,description,input_schema}`。
- `tool_choice` 字符串映射到对象：`required`→`{"type":"any"}`、`auto`→`{"type":"auto"}`、
  `none`→丢弃 tools 并置 `{"type":"auto"}`（上游不接受 `none`）。
- tool result 消息合并为**一条** `role:"tool"` 消息，并按历史里的
  `tool_call_id` **反查 toolName**（CLI envelope 的 tool-result part 需要名字）。
- `max_tokens` 缺省 64000，上限 200000（超限上游会 400，这里先夹住）。
- `reasoning_content` 还原为 reasoning part；图片 data URL 还原为
  `{type:"image",image,mimeType}`。

### 3.3 响应转换（`main.go`）

- 逐行解析事件流（不是 SSE `data:` 解析）。
- `text-delta` → OpenAI content；`reasoning-delta` → `reasoning_content`；
  `tool-call` → `tool_calls`（携带完整 arguments）。
- usage 取 `finish.totalUsage`，缺失时回退 `finish-step.usage`。
- 上游错误映射：`MODEL_NOT_IN_PLAN`→403、额度不足→402、限流→429。
- 流被截断（上游先断）时仍补齐 finish chunk 与 `[DONE]`，不让客户端挂死。
- 非流式请求 → 聚合成一次性 `chat.completion`。

### 3.4 不支持的能力

- **Responses API 不支持**。CLI 只讲 chat。按仓库硬规则，
  **不给该渠道 `support_responses`**；Responses 请求路由到它会得到
  503 `responses_unsupported_on_channel` 并正常 failover 到别的渠道。
- 图片生成不支持（`ConvertImageRequest` 直接报错）。
- 模型清单只含 Go 套餐可用模型（Claude 系列在 Go 套餐下 403 `MODEL_NOT_IN_PLAN`）。

## 4. 非目标 (Non-Goals)

- 不复活通用协议转换层。
- 不做 Responses ↔ Chat 的互转。
- 不在 one-api 里模拟 CLI 的 agent 循环（workingDir / gitStatus 等只是**填占位值**
  以过 schema 校验，one-api 不读本地 git）。
- 不枚举上游全部模型；模型清单以 Go 套餐实测为准，随套餐变动手工更新
  `models.go`。

## 5. 约束边界 (Constraints)

| 约束 | 立场 | 说明 |
|------|------|------|
| 无循环依赖 | ✅ | `commandcode` 只依赖 `relay/adaptor`、`relay/meta`、`relay/model`，不反向依赖 relay 顶层 |
| 分层向下依赖 | ✅ | `controller → relay → adaptor/commandcode` |
| God package 阈值 | ✅ | 新增独立包，6 个文件 1631 行（含测试），未膨胀既有包 |
| bridge/adapter 显式化 | ✅ | 新协议家族独立注册，不在 openai.Adaptor 内加 switch |
| 测试跟随生产代码 | ✅ | 单测 + 真实事件流 fixture + 全管线测试同 commit |
| 测试不联网 | ✅ | 全部用 `testutil.MockTransport` / fixture |
| 不复活转换层 | ✅ | 本 adaptor 是**单 provider 的专有格式实现**，不是通用转换层 |

## 6. 验证 (Verification)

```bash
# 单元 + 全管线（无网络）
go test ./relay/adaptor/commandcode/ ./relay/channeltype/ ./relay/apitype/ ./relay/ ./controller/
go test -race -count=1 ./relay/adaptor/commandcode/ ./relay/channeltype/ ./relay/apitype/ ./relay/
go build ./...
```

端到端（本机 one-api :3794，channel id=34）：

- 非流式文本 → 200，`content` 正确，`usage.prompt_tokens` 来自上游 `finish`。
- 流式文本 → `chat.completion.chunk` 序列 + `finish_reason:"stop"` + usage + `[DONE]`。
- 工具调用（非流式 / 流式）→ `tool_calls` 正确，`finish_reason:"tool_calls"`。
- 多轮 tool result 回填 → 上游能正确消费并作答。

## 7. 已知风险

- **CLI 版本耦合**：`x-command-code-version` 固定为 `1.72.2`。上游若拒绝旧版本
  client，会整体失败（表现为 400/403）。升级方式：改 `CLI_VERSION` 常量并重新构建。
  失败时的排查顺序：先看 one-api 日志里的上游错误体，再比对 CLI 官方版本的
  请求形状。
- **事件流格式是私有实现**：上游改字段名会导致解析退化（表现为空 content 或
  缺 usage），而不是报错。`testdata/*.sse` 里的真实 fixture 会在上游变化后
  第一时间暴露形状差异（人工比对）。
- **额度与限流**由上游判定，one-api 只能把 402/429 透传，不做重试退避。
