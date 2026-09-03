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
| 6 | 渠道名寻址 DB 直查（无缓存） | GetChannelByName/ChannelAddressedModels | ✅ 完成（InitChannelCache 建 name→channels 索引；缓存关闭时回退 DB；单测含裁剪/跨组/未知名） |
| 7 | 四台 SQLite 手工同步 → rqlite 集群 | 每轮部署的 DB 同步摩擦 | 🔍 已诊断：重启换 node-id 时仅写 peers.json 无法重写已提交 raft 配置（旧 voter 无法达法定人数→无限选举）；需显式 raft.RecoverCluster 或持久化 node-id，属 WIP 设计收尾 |
| 8 | token 级 RPM 限流 | 只有 quota 无频率限制 | ✅ 完成（Token.RPMLimit + 滑动窗口 + 429/Retry-After/token_rpm_exceeded；单测+mock 栈集成测试） |

## 测试要求

- 每项改动：单元 + 集成（controller mock 栈 / provider 栈）双覆盖
- 全套 `go test -race -count=2`（反复运行防 flake）
- AGENTS.md 三类路径的语义不得回退


## 完成记录（2026-09-03）

- 全套 `go test -race -count=2`（37 包，干净检出）**全绿**
- 顺手修复三个预存的测试隔离 flake（-count>1 才暴露）：
  ratio 全局表泄漏 ×4、observability 顺序依赖 ×1
- 新增测试：WAL pragma、retry-after 解析/捕获/惩罚三层、nodes 全局冷却
  可见性、wire-mismatch 不粘性冷却（含故障转移 + 后续请求）


## 事故复盘与配置/日志库隔离（2026-09-03 第三轮）

22:08 生产事故：logs 表损坏页把整个 SQLite 文件拖成 malformed（索引不同步
→ REINDEX 也撞坏页），relay FATAL 崩溃循环。`.recover` 重建 + 丢弃打捞残骸
（lost_and_found 185 万行日志残骸，配置表零损失），主库从 111MB 缩到 **156KB**。

**结论：logs 表会实质影响配置表稳定性**（同文件 = 同损坏域 + 同写锁）。
已实施隔离（你的设计判断：配置集群同步、日志本地不同步）：
- `LOG_SQL_DSN=sqlite://<path>` 新增 SQLite 文件分支（此前裸路径被当
  MySQL DSN，此语法缺口即本轮修复）；独立 WAL、独立 AutoMigrate
- 日志的所有高频写/清理/迁移从此不碰配置库；配置库缩小到可整体同步的量级
- 四台已切换；TestInitLogDB_SqliteFileSplit 钉住行为

## 推进记录（2026-09-03 第二轮）

- #6 渠道名寻址缓存：`name2channel` 索引随 InitChannelCache 重建
  （SYNC_FREQUENCY 周期），MemoryCache 关闭时回退 DB 直查；
  `ChannelAddressedModels` 同步走缓存。TestGetChannelByName_CachedPath
  钉住缓存路径/名字裁剪/跨组隔离/未知名错误。
- #8 token RPM 限流：`Token.RPMLimit`（0=不限，默认）+ 进程内滑动窗口
  （被拒请求不计数）；超限 429 + Retry-After + `token_rpm_exceeded`。
  单测三例（窗口滑动/零禁用/令牌隔离）+ mock 栈集成两例
  （限额触发/零禁用），测试注入时钟可时间旅行。
- #7 rqlite：两个失败测试的根因已定位（见清单行），修复属 WIP 设计
  收尾，未动用户未提交代码。
- 全套 `-race -count=2` 回归 + 干净检出复跑。
