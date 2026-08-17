# ADR-0003: Provider Adapter 抽象与注册表

- 日期: 2026-08-17
- 状态: ✅ 已决策
- 决策者: oliveagle
- 相关代码: `relay/adaptor/openai`, `relay/adaptor/provider`, `relay/adaptor.go`

## 1. 背景 (Context)

现有系统已有两层抽象:

1. `channeltype`: 用户配置的具体渠道 ID(OpenAI、MiniMax、Doubao、Azure ...)。
2. `apitype`: 协议家族 ID(OpenAI-compatible、Anthropic、Gemini、Ollama ...)，
   由 `channeltype.ToAPIType` 映射，`relay.GetAdaptor` 据此选择协议适配器。

问题在于 `openai.Adaptor` 内部又按 `meta.ChannelType` 分叉:

- URL 特例: Azure / MiniMax / Doubao / Novita / BaiduV2 / AliBailian / GeminiOpenAICompatible
- 鉴权特例: Azure
- Header 特例: OpenRouter
- 元数据特例: `GetCompatibleChannelMeta` 的巨型 switch

同一个 provider 的规则散落在 `GetRequestURL`、`SetupRequestHeader`、`GetModelList`、`GetChannelName`
四处，新增一个 provider 至少要改 3 个文件。结果是协议层与 provider 层混杂，职责不清晰。

## 2. 备选方案 (Options)

### A. 每个 provider 独立实现 `adaptor.Adaptor` ❌

把 MiniMax、Doubao、DeepSeek 等各自从 `openai.Adaptor` 拆出完整 Adaptor。

- ✅ 语义最直观
- ❌ 大量复制 OpenAI 协议处理(`StreamHandler`、`Handler`、`ConvertRequest`)
- ❌ `relay/adaptor.go` 会从 20 个协议适配器膨胀为 40+ provider 类
- ❌ 修改 OpenAI 协议行为需要同步多处

### B. 只加 provider options/hook，不显式建模 ❌

在 `openai.Adaptor` 上加 `ProviderOptions` 结构，仍然集中注册。

- ✅ 改动最小
- ❌ 只是换了字段名，没有解决职责混杂与新增 provider 需要改多处的问题
- ❌ 无法用接口约束 provider 能力边界

### C. 协议适配器 + Provider Descriptor / Registry ✅ 选中

在 `openai.Adaptor` 之下增加一层 **provider adapter**:

```go
type Descriptor struct {
    ChannelType int
    Name        string
    Models      []string
    RequestURL  func(*meta.Meta) (string, error)
    SetupHeader func(*gin.Context, *http.Request, *meta.Meta) error
}

type Registry struct { ... }

func (r *Registry) Register(d Descriptor) error
func (r *Registry) Get(channelType int) (Descriptor, bool)
func (r *Registry) MustGet(channelType int) Descriptor
func (r *Registry) ChannelTypes() []int
func (r *Registry) Meta(channelType int) ProviderMeta
```

职责划分:

| 层 | 职责 | 示例 |
|----|------|------|
| `relay.GetAdaptor` | 协议家族路由 | OpenAI / Anthropic / Gemini |
| `openai.Adaptor` | OpenAI 协议转换与响应处理 | Chat Completions JSON/SSE |
| `provider.Registry` | provider 差异化规则 | URL、鉴权、专属 header、模型列表 |
| `provider.Descriptor` | 单个 provider 的显式 adapter | MiniMax 用 `/v1/text/chatcompletion_v2` |

OpenAI-compatible provider 不再修改 `openai.Adaptor` 内部逻辑，而是注册一个
Descriptor，包含该 provider 的 URL、鉴权和 header 规则。协议层保持通用，
provider 差异集中在一处。

### 为什么不用 map[string]Descriptor

- ChannelType 是 int，已有类型系统约束
- 用 `int` 键避免字符串拼写错误
- 与现有 `channeltype` / `apitype` 体系一致

## 3. 决策结果 (Decision)

采用方案 C，分两阶段落地:

### 阶段 1: 抽象 + 注册表 + 迁移 OpenAI-compatible providers

1. 新增 `relay/adaptor/provider` 包，定义 `Descriptor` / `Registry`
2. 新增 `relay/adaptor/openai/provider.go`，把 `GetRequestURL` 中的 provider 特例
   迁移为 Descriptor
3. 新增 `relay/adaptor/openai/provider_test.go`，验证:
   - 每个 channeltype 都有对应 Descriptor
   - URL 构造正确
   - 鉴权 header 正确
   - Registry 不可重复注册、不可注册零值
4. `openai.Adaptor.GetRequestURL` / `SetupRequestHeader` 改为查 Registry

### 阶段 2: 逐步迁移元数据

`GetCompatibleChannelMeta` 与 `CompatibleChannels` 迁移到 Registry，使
`GetModelList` / `GetChannelName` 也走同一注册表。

## 4. 非目标 (Non-Goals)

- 不一次性重写全部 20 个协议 adaptor
- 不改变 `relay.GetAdaptor` 的外部接口
- 不改变 `channeltype` / `apitype` 枚举
- 不改变 OpenAI-compatible provider 的上游行为
- 不在本 ADR 中处理非 HTTP 协议（AWS SDK）的 provider 差异

## 5. 约束边界 (Constraints)

### 架构隔离约束声明

| 约束 | 本决议的立场 | 说明 |
|------|------------|------|
| 1. 无循环依赖 | ✅ 遵守 | `provider` 包只依赖 `gin`/`http`/`meta`，不反向依赖 `openai` |
| 2. 分层向下依赖 | ✅ 遵守 | `controller → relay → adaptor/openai → adaptor/provider` |
| 3. God package 阈值 | ✅ 遵守 | `openai` 包 10 文件，新增 2 个文件后仍低于 100 文件；单文件 <500 行 |
| 4. 主题域边界清晰 | ✅ 遵守 | provider 差异隔离在 `provider` 域，协议处理留在 `openai` 域 |
| 5. bridge/adapter 显式化 | ✅ 遵守 | 每个 provider 显式注册 Descriptor，不再隐式 switch |
| 6. 测试跟随生产代码 | ✅ 遵守 | `provider_test.go` 与生产代码同包同 commit |

### 其他约束

- Descriptor 必须是值类型，避免注册后外部修改
- Registry 必须并发安全（初始化期写入，运行期只读）
- 注册失败必须 panic，防止带病启动
- 现有行为 100% 兼容，通过单测逐一断言 URL/header

## 6. 验证 (Verification)

- `go test ./relay/adaptor/provider ./relay/adaptor/openai ./relay ./controller`
- `go vet ./...`
- 全量 `go test ./...`
