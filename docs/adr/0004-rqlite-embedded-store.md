# ADR-0004: RQLite 内嵌存储 (Embedded Store)

- 日期: 2026-08-24
- 状态: ✅ 已决策
- 决策者: oliveagle
- 相关代码: `model/main.go`, `common/database.go`, `common/init.go`, `store/rqlite/`, `submodules/rqlite`, `docker-compose.yml`

## 1. 背景 (Context)

one-api 目前支持三种外部数据库: SQLite (单文件, 无网络) / MySQL / PostgreSQL
(`model/main.go: chooseDB`)。对 NAS / 单节点部署者, SQLite 已经够用; 但 RQLite
提供了**单机 SQLite + 集群复制/备份**的组合, 是 SQLite 的严格超集:

- 单节点 (`rqlited --single` 模式): 一个进程内嵌 raft + SQLite, 数据与
  备份 (snapshot) 都在本地目录;
- 多节点: 同样的 HTTP API, 复制、选举、S3 备份开箱即用。

目标: 让 one-api 可以把 `SQL_DSN` 指向本地 RQLite (内嵌), 获得
SQLite 级别的部署简单性 + RQLite 的备份/集群能力, 且**不破坏现有
SQLite/MySQL/Postgres 行为**。

rqlite 源码以 git submodule 引入: `submodules/rqlite` (v10.3.0-dev,
commit `dccd6cc7`)。

## 2. 备选方案 (Options)

### A. 外部 rqlite 进程 + gorm MySQL driver ❌

RQLite 是 SQLite, 不是 MySQL。MySQL 协议不兼容 (GORM 的 mysql driver
走 `github.com/go-sql-driver/mysql` 的握手协议), 无法复用。

### B. 外部 rqlite 进程 + 手写 `database/sql` driver (HTTP) ❌ (本轮)

可行但工作量大: 需要实现 gorm dialector 或 database/sql driver
(解析 RQLite HTTP 响应、处理 `?level=strong`、事务映射)。本轮不做,
留作后续 ADR; 但**本轮的 DSN 解析与 driver 注册方式与该方案兼容
(`rqlite://` 前缀 + 本地端口), 升级时只换 driver 实现**。

### C. 内嵌 RQLite store (in-process) ✅ (本轮选定)

把 `github.com/rqlite/rqlite/v10/store` 直接链进 one-api 进程:

- 单节点模式: `store.New` + `Bootstrap` 单 voter, 无网络监听;
- 数据通过 `database/sql` (gorm sqlite driver 语义) 访问: store 内部
  维护的 SQLite 文件在 `dataDir/rqlite.db`, 我们用一个轻量
  `database/sql` 适配层把 RQLite 的 query 结果桥接到 gorm
  (见 §3 决策点)。

理由:
1. 与现有 "SQLite 单文件" 部署形态一致, 用户无感 (仍是本地文件);
2. 获得 RQLite 的 snapshot / backup / (未来) 集群能力;
3. 不引入新端口 (单节点模式无 HTTP/raft 监听);
4. submodule 源码可见, 升级/打 patch 自由。

代价:
- 二进制体积 + ~30MB (raft/boltdb/mattn sqlite 静态链接);
- CGO: RQLite store 内部使用 `mattn/go-sqlite3` (CGO)。内嵌 RQLite
  路径**必须** `CGO_ENABLED=1` 构建; 现有 `build-linux` 目标
  `CGO_ENABLED=0` 的产物不含 RQLite 路径 (build tag 隔离) → 见 D5。

## 3. 决策点与方案 (Decisions)

### D1. DSN 语法

`SQL_DSN` 以 `rqlite://` 开头 → 走内嵌 RQLite 路径:

```
rqlite://[dir]?[key=value&...]
```

- `dir` (默认 `./data/rqlite`): 数据目录, 内含 `rqlite.db` + raft 状态 +
  snapshot。
- 可选参数 (本轮只认 `dir`, 其余留扩展位):
  - `node_id` (默认 `one-api-<hostname>`)
  - `raft_addr` (单节点模式**不需要**, 多节点未来用)
  - `http_addr` (同上, 未来; 本轮单节点不监听)

`LOG_SQL_DSN` 同样支持 `rqlite://`, 复用同一套 open 逻辑
(两个独立 store 实例, 不同 dir)。

### D2. 数据访问层: 自建 gorm Dialector over store 的 Request API ✅

RQLite v10 **没有** `database/sql` driver 导出 (其 mattn driver 仅内部
用于 store 自己的读写连接池, 不暴露给外部用户)。外部访问路径只有:

- HTTP API (`/db/execute`, `/db/query`, `/db`) — 需要监听端口;
- `store.Store` 的进程内 API: `Request(ctx, *proto.ExecuteQueryRequest)`
  (写+读, 走 raft consensus) 与 `Query(ctx, *proto.QueryRequest)`
  (纯读, 走本地 read pool)。

因此内嵌路径**必须自建 gorm Dialector**, 实现 `gorm.Dialector` 接口,
把 gorm 的 `*sql.DB` 需求替换为直接调用 `Store.Request()`:

```
gorm.DB (dialector=rqlite)
  └─ rqliteConn (实现 driver.Conn 语义: Prepare/Exec/Query)
       └─ *store.Store.Request / .Query
```

具体:
1. `store/rqlite/dialector.go` — 实现 `gorm.Dialector`:
   - `Initialize(db *gorm.DB)`: 不接 `*sql.DB`, 而是把
     `*store.Store` 包装成 gorm 的 `Dialector` 后端 (通过
     `gorm.Config.Conn` 或自定义 `DriverConn`)。
   - 由于 gorm 的 `Dialector` 接口要求返回 `*gorm.DB` 且底层是
     `database/sql`, 实际做法是: 实现一个最小
     `database/sql/driver` 包 (name=`rqlite`), 注册到
     `sql.Register`, 其 `Open` 返回一个基于 `Store.Request()` 的
     `driver.Conn`。这样 gorm 的 `sql.Open("rqlite", dsn)` 就能用。
2. 这个 `driver.Conn` 实现 `driver.Conn` / `driver.Execer` /
   `driver.Queryer` 三个接口:
   - `Prepare(query)`: 返回 `driver.Stmt` (不做真正的预编译,
     把 SQL 存起来, 执行时再发);
   - `Exec(query, args)`: 调 `Store.Request()` 单语句写;
   - `Query(query, args)`: 先调 `Store.Request()` (force query),
     或判断只读后走 `Store.Query()`;
   - `Close()`: no-op (连接池由 gorm 管理, 底层 store 只关一次)。
3. `args` 转 proto `Parameter`: 按类型分发 (int64/float64/bool/
   []byte/string/nil), 与 RQLite HTTP API 的参数语义一致。
4. 事务: gorm 的 `BEGIN/COMMIT/ROLLBACK` 通过 `Store.Request()`
   的 `Transaction=true` 批量执行实现 (单语句时退化为普通执行;
   多语句事务时 gorm 会在同一个 `*sql.Conn` 上 Begin, 我们的
   driver 需要支持 `driver.ConnBeginTx` 接口, 把 begin/commit
   映射到 RQLite 的 `?transaction=true` 批量请求)。

> 📌 引用决议: ✅ 已决-D2 (自建 database/sql driver + gorm dialector over Store.Request)


### D3. 方言分支 (SQLite-like)

RQLite 单节点 = SQLite 方言。现有代码按
`common.UsingSQLite / UsingMySQL / UsingPostgreSQL` 分支
(`group` / `key` 列名引用、`DATE_FORMAT` vs `TO_CHAR` vs `strftime`、
`RANDOM()` vs `RAND()`、`ifnull` vs `COALESCE`、`FOR UPDATE` 等)。

**新增 `common.UsingRQLite = true`**, 且所有 SQLite 分支同时
`|| common.UsingRQLite` (RQLite 是 SQLite 方言, 行为与 UsingSQLite
完全一致)。不复制分支逻辑, 只做 flag 叠加。

> 📌 引用决议: ✅ 已决-D3 (UsingRQLite 叠加到 SQLite 分支)

### D4. 子模块引入方式

- `git submodule add https://github.com/rqlite/rqlite.git submodules/rqlite`
  (已执行, commit `dccd6cc7`, v10.3.0-dev);
- `go.mod` 加 `replace github.com/rqlite/rqlite/v10 => ./submodules/rqlite`
  (本地源码直接编译, 不走 proxy; 与 `rqlite/go-sqlite3` 的
  `replace` 保持隔离 — 我们只 import `store` / `db` 两个包);
- **不**把 submodules/rqlite 加进 `.gitignore` (submodule 指针必须入库);
- 升级 = 在 submodule 内 `git pull` + bump 指针 commit, 与 ole-release
  的 VERSION 流程独立。

> 📌 引用决议: ✅ 已决-D4 (submodule + replace, 不 vendor)

### D5. 构建策略 (CGO 强制)

RQLite store 内部依赖 `mattn/go-sqlite3` (CGO), 因此:

- **使用 RQLite 路径的构建必须 `CGO_ENABLED=1`**;
- `CGO_ENABLED=0` 构建: `store/rqlite` 包用 build tag 拆成
  `cgo.go` (CGO_ENABLED=1, 真实实现) 与 `cgo_stub.go`
  (CGO_ENABLED=0, `Open` 返回错误 "rqlite support requires
  CGO_ENABLED=1 build"), 保证**可编译**、运行时**明确拒绝**;
- `build-local.sh` / `Makefile build-linux` 现有 `CGO_ENABLED=0`
  行为不变 (产物不含 RQLite 路径); 文档注明 RQLite 需要
  CGO 构建 (`CGO_ENABLED=1 go build`)。
- 本轮**不**改 Makefile 默认, 只加注释 + README 段落。

> 📌 引用决议: ✅ 已决-D5 (CGO 可选, 缺 CGO 时运行时拒绝)

### D6. 单节点 bootstrap 与生命周期

- 启动: `model.InitDB` → `chooseDB` 识别 `rqlite://` →
  `openRQLite(dsn)`:
  1. `os.MkdirAll(dir)`;
  2. `store.New(&store.Config{Dir, DBConf, ID, Logger}, ly)`;
  3. `s.Bootstrap(store.NewServer(id, addr, true))` 单 voter
     (等价 `rqlited --single`);
  4. 等待 `s.WaitForReady()` (有超时, 默认 30s);
  5. `db := s.DB()` 拿到 `*sql.DB`;
  6. 用自建 dialector 包装成 `*gorm.DB`;
  7. 走现有 `AutoMigrateAll` (schema 迁移, 与 SQLite 路径一致)。
- 关闭: `model.CloseDB` → 对 RQLite handle 额外 `s.Close()`。
- **不监听任何端口** (单节点模式 raft/HTTP 均本地, 无网络)。
- 多节点 / 外部 rqlite 进程: 本轮**不实现**, DSN 解析留扩展位
  (见 B 方案备注)。

> 📌 引用决议: ✅ 已决-D6 (单节点 in-process, 无端口; 多节点后续 ADR)

## 4. 非目标 (Non-Goals)

- 不实现 RQLite 集群 (多 voter / join / 选举) — 后续 ADR
- 不实现外部 rqlite 进程 + HTTP driver — 后续 ADR
- 不改现有 SQLite/MySQL/Postgres 行为 (回归 = 现有测试全绿)
- 不把 RQLite 的 HTTP API 暴露给 one-api 前端
- 不改 web 前端 (数据库选择是后端 DSN 语义, 前端无感知)
- 不引入 S3 备份配置 (单节点本地 snapshot 即可)

## 5. 约束边界 (Constraints)

### 架构隔离约束声明

| 约束 | 本决议的立场 | 说明 |
|------|------------|------|
| 1. 无循环依赖 | ✅ 遵守 | 新增 `store/rqlite` 包只依赖 `model` 层使用的 gorm + rqlite submodule, 不反向 import `controller`/`relay`; `model/main.go` 单向 import `store/rqlite` |
| 2. 分层向下依赖 | ✅ 遵守 | `model (业务) → store/rqlite (存储适配) → submodules/rqlite (第三方)`, 严格向下 |
| 3. God package 阈值 | ✅ 遵守 | `model` 包 12 文件, 新增逻辑集中在 `store/rqlite` 3 个文件 (dialector/lifecycle/dsn), 单文件 < 300 行 |
| 4. 主题域边界清晰 | ✅ 遵守 | RQLite 是"存储域", 与 "relay 域" (provider 适配) 完全正交; `common.UsingRQLite` 与现有 3 个 DB flag 同域 |
| 5. bridge/adapter 显式化 | ✅ 遵守 | `store/rqlite` 就是显式 bridge: 把 RQLite store 桥接成 gorm Dialector, 不藏在 model 内部 |
| 6. 测试跟随生产代码 | ✅ 遵守 | `store/rqlite/*_test.go` 与生产代码同包; model 层回归测试 (现有 SQLite 路径) 不变 |

### 其他约束

- `rqlite://` DSN 必须**优先**于现有 `postgres://` / 默认 MySQL / SQLite
  分支 (在 `chooseDB` switch 的第一个 case)。
- RQLite 路径下 `common.UsingSQLite` **不**设为 true (两者是不同实现);
  但方言分支按 D3 叠加 `|| UsingRQLite`。
- 不修改 `AutoMigrateAll` 的签名与行为 (RQLite 走 SQLite 方言, AutoMigrate 兼容)。
- `driver.Conn` 的 `Prepare` 不做真正预编译 (RQLite 无 client-side
  prepared statement 概念), SQL 在每次 Exec/Query 时整体发送;
- 构建: `CGO_ENABLED=0` 时 RQLite 路径必须**可编译** (stub), 运行时
  明确拒绝, 不 panic。
- submodule 指针必须与 go.mod replace 一致; CI 需要
  `git config --global --add safe.directory` 或 `--recurse-submodules`
  (见 §7 验证)。

## 6. 验证 (Verification)

- 编译: `go build ./...` (CGO_ENABLED=1 与 0 各一次)
- 回归: `go test -race -count=1 ./model/... ./controller/...` (现有
  SQLite 路径全绿, 证明 D3 的 flag 叠加没破坏方言分支)
- RQLite 路径: 新增 `store/rqlite/rqlite_test.go` — 启动单节点 store,
  跑 `AutoMigrateAll`, 插一条 User, 读回, 关闭; 断言无网络监听
  (单节点模式)
- DSN 解析: 单测覆盖 `rqlite://` / `postgres://` / 空 / 普通 DSN
  四分支
- 手动: `SQL_DSN=rqlite://./data/rqlite go run .` 启动后,
  `ls ./data/rqlite` 应有 `rqlite.db` + raft 目录;
  访问 `/api/status` 应返回 success:true

## 7. 升级与运维

- **升级 rqlite**: `cd submodules/rqlite && git pull && git commit`
  (bump 指针), 然后 `go mod tidy`。store 的 schema 由 RQLite 自己管理,
  与 one-api 的 AutoMigrate 互不干扰 (one-api 只 CRUD 业务表)。
- **备份**: 单节点模式下 snapshot 在 `dir/snapshots/`; 运维用
  `rqlite backup` 或 RQLite HTTP API (若未来启用) 做逻辑备份。
  本轮不自动备份, 依赖 NAS 现有 rsync / 快照策略。
- **回滚**: 若 RQLite 路径出问题, 改回 `SQL_DSN=postgres://...` 或
  留空 (SQLite) 即可, 数据目录独立, 不影响其他 DB。
- **CI**: 需要在 `.github/workflows` 里加
  `submodules: true` (或 `git config submodule.recurse true`),
  否则 `go build` 找不到 `submodules/rqlite`。

## 8. 实施记录 (Implementation Notes, 2026-08-25)

本轮落地的关键实现细节与 rqlite v10 的坑:

### 8.1 访问层: direct *sql.DB (而非 database/sql driver)

RQLite v10 不导出 `database/sql` driver。最初实现了
`store/rqlite/conn.go` (driver.Conn over Store.Request), 但发现
**Store.Request 对纯读语句有 bug**: 部分 SELECT 经 consensus 路径
返回空结果。最终方案: `OpenStore` 在 store 就绪后用
`sql.Open("rqlite-sqlite3", dbPath)` 打开 store 内部 mattn driver
注册的**直连句柄** (`EmbeddedStore.Direct()`), GORM 走这个直连
(与 gorm sqlite 路径完全同构)。`conn.go` 的 driver 保留作为备用/
测试路径, 读走 `Store.Query` (ro 池), 写走 `Store.Request`。

### 8.2 节点恢复: peers.json + clean_snapshot 标记

- **新数据目录**: 写 `raft/peers.json` (含当前 ephemeral 地址),
  `Store.Open` 触发 rqlite 内置 `RecoverNode` 安装配置。
  (Store.Bootstrap 与首次选举有 race, 不可用。)
- **重启**: 写一个**与当前 db.sqlite 指纹匹配**的 `clean_snapshot`
  标记 (mod_time + size + CRC32), 使 rqlite 走
  `NoSnapshotRestoreOnStart` 快速路径 —— **保留现有 db 文件**
  (数据真相), 只从 peers.json 重建 raft 配置。
  不写/写错该标记会导致 rqlite 从 (可能为空的) snapshot 恢复,
  **覆盖好 db 造成数据丢失** —— 这是本轮踩的最大坑。
- ephemeral raft 端口每次运行都变, peers.json 每次重写为当前地址。

### 8.3 快照: 直接 checkpoint+vacuum db 文件

rqlite v10 的 snapshot 机制在本场景下持续产出**空 snapshot**
(4096 字节, 无表)。原因: store 内部 rw 连接池与我们的 direct
连接池各自持有 WAL 视图, store 的 `Snapshot()` 看不到 direct
写入。**不依赖 rqlite snapshot**, 迁移/关闭前直接对 db 文件
`PRAGMA wal_checkpoint(TRUNCATE)` + `VACUUM`, 保证 db.sqlite
自包含; 重启走 8.2 的快速路径保留该文件。数据可靠性验证
(migrate 后逐表 count 对比 + 独立 sqlite3 连接读文件) 通过。

### 8.4 方言分支

`common.UsingRQLite` 叠加到所有 SQLite 方言分支 (D3):
`model/ability.go` (RANDOM), `model/log.go` (strftime),
`model/user.go` (LIKE 分支)。`group`/`key` 的反引号引用在
SQLite 方言下合法, 无需分支。

### 8.5 构建

- rqlite 需要 **CGO** (mattn/go-sqlite3): `go.mod` 加
  `replace github.com/rqlite/rqlite/v10 => ./submodules/rqlite` +
  `replace github.com/mattn/go-sqlite3 => github.com/rqlite/go-sqlite3 v1.49.0`
  (rqlite 自身对 mattn 的 replace 在 consumer 侧不生效, 必须显式加)。
- go.mod `go 1.26` + `toolchain go1.26.0` (rqlite v10.3 要求)。
- `CGO_ENABLED=0` 构建: `store/rqlite` 有 `registry_stub.go`
  (build tag `!cgo`), 可编译, 运行时 `ErrNoStore` 明确拒绝 (D5)。
- `cmd/migrate-rqlite`: 迁移工具, 逐表 schema+行拷贝, 源表并发
  增长时按"dst >= src 快照值"判定 (避免假 mismatch)。

### 8.6 运行形态 (本次交付)

- 迁移: `~/opt/one-api/one-api.db` → `~/opt/one-api/rqlite/`
  (7 表, 153k+ logs, 逐表验证通过)。
- 3795 实例: `one-api --port 3795` +
  `RQLITE_DIR=~/opt/one-api/rqlite`, 持续运行验证 (多次重启,
  数据不丢)。
- **3794 实例 (supervisord) 全程未触碰**。
