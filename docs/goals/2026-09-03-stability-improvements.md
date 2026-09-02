# 目标：one-api 稳定性改进（2026-09-03）

> 来源：全库评审 + 生产日志证据。原则：**稳定可靠优先**，每项改动配套
> 集成测试（mock 栈 / provider 栈），全套测试反复运行。

## 背景结论（已完成的上游问题）

### logs 表无限增长 —— ✅ 已实现定时清理（6ffa38a, f2b72c0）
- `LOG_RETENTION_DAYS`（默认 30 天，0=关闭；options 表可覆盖，下周期生效）
- 启动即清一次 + 每日一次；**1 万行/批**删除，避免全表 DELETE 长锁
- 实证：Mac 上 1 天窗口真实删除 **154,053 行**（15.8 万 → 4,412）
- 计费安全：quota 结算在 `User.UsedQuota`，logs 纯历史

### rqlite 分布式 —— 状态澄清（未完成，未应用）
工作区未提交 WIP（ADR-0004 + store/rqlite/ + cmd/migrate-rqlite）：
- ✅ 编译通过；单机内嵌形态（RQLITE_DIR 进程内 raft+SQLite）基本完成
- ❌ 2 个崩溃恢复测试失败：TestCleanMarkerPreservesDB /
   TestCrashRestartPreservesDB（各 ~61s 超时）
- ❌ **多节点 raft 复制（真正的"分布式"）ADR 明确本轮未做**，仅预留
  `raft_addr`/`http_addr` 扩展位
- ❌ 未应用：四台生产机全部裸 SQLite，无一使用 rqlite:// DSN；
  且要求 CGO 构建（build tag 隔离，CGO_ENABLED=0 产物不含该路径）
- 结论：**不是"实现了没应用"，是"单机版差收尾（2 个失败测试），
  集群版未开始"**。应用前必须先修好失败测试。

## 本轮改进清单

| # | 项 | 证据 | 状态 |
|---|---|---|---|
| 1 | SQLite WAL 模式 | `_busy_timeout` 已设但 journal 为 rollback；NAS 82k 行 SLOW SQL | ✅ 完成 |
| 2 | 模型倍率缺失刷 ERROR（17,420 条）+ 默认 30 计费 | ratio/model.go:708；日志统计 | ✅ 完成（日志降级；**倍率值需按真实价格配 options ModelRatio，业务决策留给 owner**） |
| 3 | 429 全局冷却在 nodes 端点不可见 | pin.go CoolingDown 只读粘性 store | ✅ 完成 |
| 4 | wire 不匹配 503 误伤粘性冷却 | relay.go shouldRetry 分支无差别 router.Fail | ✅ 完成 |
| 5 | Retry-After 头纳入 429 惩罚 | markChannelPenalty 只解析 body 文案 | ✅ 完成（Error.RetryAfterMs + Retry-After 头解析 + 惩罚采用） |
| 6 | 渠道名寻址 DB 直查（无缓存） | GetChannelByName/ChannelAddressedModels | 📋 后续（低频路径，非稳定性问题） |
| 7 | 四台 SQLite 手工同步 → rqlite 集群 | 每轮部署的 DB 同步摩擦 | 📋 依赖 rqlite 收尾（见上） |
| 8 | token 级 RPM 限流 | 只有 quota 无频率限制 | 📋 后续 |

## 测试要求

- 每项改动：单元 + 集成（controller mock 栈 / provider 栈）双覆盖
- 全套 `go test -race -count=2`（反复运行防 flake）
- AGENTS.md 三类路径的语义不得回退


## 完成记录（2026-09-03）

- 全套 `go test -race -count=2`（36 包）**全绿**
- 顺手修复三个预存的测试隔离 flake（-count>1 才暴露）：
  ratio 全局表泄漏 ×4、observability 顺序依赖 ×1
- 新增测试：WAL pragma、retry-after 解析/捕获/惩罚三层、nodes 全局冷却
  可见性、wire-mismatch 不粘性冷却（含故障转移 + 后续请求）
