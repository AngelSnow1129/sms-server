# SMSServer 交接文档

本文面向接手本仓库的人：说明项目现在做到哪一步、为什么是这样设计的、改代码前必须遵守的约束，以及接下来该做什么。

面向使用者的操作说明见 [README.md](../README.md)（自托管）与 [cloudflare/README.md](../cloudflare/README.md)（Workers 版），本文只讲**维护视角**。

## 1. 项目是什么

从短信供应商回调中自动提取验证码并缓存，供调用方按手机号轮询取码的自托管服务。调用方不需要接入各短信平台的 API，只需接收 webhook + 用 `HMAC(手机号)` 取码。

两条部署形态并存，接口协议一致：

| 形态 | 运行环境 | 验证码存储 | 入口 |
|---|---|---|---|
| 自托管 | Docker / 二进制 + MySQL | 内存 或 SQLite | 仓库根 `main.go` |
| Workers | Cloudflare Workers + D1 | D1（唯一事实源） | `cloudflare/` 子目录 |

## 2. 当前状态

### 已完成

| 项 | 说明 |
|---|---|
| 核心链路 | webhook 收短信 → 正则提取验证码 → 写入存储 → 按 `HMAC(手机号)` 阅后即焚取码 |
| 存储抽象层 | `store.Store` 接口 + 内存与 SQLite 两个实现，共用同一组契约用例 |
| MySQL 仓储 | 短信原文落库 `sms_records`，含 `(recipient, created_at DESC)` 索引 |
| CI | `ci.yml`（门禁 + MySQL 集成测试 + 5 平台交叉编译）、`cloudflare.yml`（WASM 构建）、`release.yml`（tag 发布 + GHCR 镜像） |
| 容器化 | 多阶段构建，`CGO_ENABLED=0` + distroless 非 root |
| 日志脱敏 | 不记录验证码明文、完整 token、手机号明文 |

### 本轮刚完成

已提交，工作区干净：

- `store.Store` 抽象落地，`service` 不再直接依赖 `cache`
- `store/sqlite` 实现接入，由 `DB_DRIVER` 选择后端
- `.gitignore` 补齐（运行时库文件、wrangler 产物、AI 工具目录）
- 本文档

**但这些提交在 `cloudflare` 分支上，且本地已与 `origin/main` 分叉** —— 见第 2.1 节。合回主干前请勿假设当前代码等于线上将要运行的代码。当前分支改动与上游 smsforward 特性**从未在同一份代码上共存验证过**。

### 2.1 分支现状与上游分叉（重要）

当前在 **`cloudflare` 分支**，与 `origin/main` 已分叉于 `0c77cbe`：

```
0c77cbe ──┬── 10d7c8e ── 14671cf   (cloudflare, HEAD：本轮改动)
          └── 2cce4f4 ── 562401b ── df633db   (origin/main：smsforward 特性, PR #1)
```

| 侧 | 提交 | 内容 |
|---|---|---|
| 本地（2 个） | `10d7c8e`、`14671cf` | Cloudflare Workers/D1 形态、`store.Store` 抽象、SQLite 后端、`DB_DRIVER`、文档 |
| 上游（3 个） | `2cce4f4`、`562401b`、`df633db` | 模板化验证码提取、`smsforward` 通道 webhook、CI 产物上传 |

**两边互不完整**：`main` 上没有 SQLite 后端与 `store` 抽象；本分支没有上游的模板提取与 smsforward 通道。上游新增了 3 个本地没有的文件：`model/smsforward.go`、`model/otp_template.go`、`docs/WIKI_SMSFORWARD_TEMPLATES.md`。

**合并时会冲突的 5 个文件**（精确冲突点见 3.8）：

```
config/config.go   .env.example   main.go   README.md   service/otp_service.go
```

合并后**必须重跑第 5 节的完整门禁**：当前分支的门禁通过，只因为它的代码里压根不含上游那批功能——两套功能共存尚未被任何一次验证覆盖过。

> 分叉是并行开发产生的，不是错误。合回前请勿在本分支上直接改上游那批文件，否则冲突面会继续扩大。

## 3. 架构与关键决策

### 3.1 分层

```
main → handler → service → {store, repository} → model
```

`store` 只定义接口（`store/store.go`），`cache` 与 `store/sqlite` 是它的实现。这样 `service` 不依赖任何具体存储，**换存储只改 `main.go` 的 `newOTPStore`**。

`handler` 与 `service` 各自通过 `otpService` / `smsRepository` 接口解耦，测试用替身注入，不需要数据库。

### 3.2 `store.Store` 为什么不带 `context.Context` 和 `error`

这是刻意取舍，不是遗漏，改动前请先读 `store/store.go` 的包注释。要点：

- 内存实现没有阻塞 I/O，传入 `ctx` 只能被忽略，反而让调用方误以为有取消/超时语义
- `service.GetOTP` / `ProcessIncomingSMS` 的签名被 `handler` 及其测试替身锁死，此处引入 `ctx` 会逼调用方硬编码 `context.Background()`
- 持久化实现可以在**构造阶段**持有带超时的 ctx（`New(ctx, dsn, ttl)`），不必改接口

若要上抛错误或加 ctx，应作为**独立的破坏性重构**，让 `service` 与 `handler` 一并调整。

### 3.3 `DB_DRIVER` 与两种后端的行为差异

`DB_DRIVER`（`mysql` / `sqlite`）是唯一的后端开关，接线在 `main.go` 的 `newOTPStore` 与 `newSMSRepository`。**默认 `mysql`，不设置时行为与引入 sqlite 前完全一致**——这个默认值是刻意选的，保证升级不产生行为突变。非法值直接启动失败（`Fatal`），不静默回落。

| 维度 | `mysql`（默认） | `sqlite` |
|---|---|---|
| MySQL 连接 | 启动即连接并 `AutoMigrate` | **完全不连接**，无 MySQL 也能启动 |
| 短信原文 `sms_records` | 落库 MySQL | **不持久化**（空实现，见下） |
| 验证码存储 | 内存 `cache.OTPCache` | SQLite `store/sqlite` |
| 重启后验证码 | 丢失 | 未过期的仍在 |
| 读取后 | 物理删除 | **软删除**：`status='read'` + 写 `read_at`，保留 10 分钟供对账 |
| 物理清理 | `Cleanup` 清过期项 | `Cleanup` 清「已读超 600s」+「未读已过期」两类 |

**已知缺口：`sqlite` 模式下短信记录不落库。** `repository.SMSRepository` 依赖 `*gorm.DB` 与 MySQL 方言，纯 Go SQLite 的 GORM 驱动未在本轮接入，因此该分支返回一个 `Create` 恒成功的空实现，并在启动日志里明确打印「短信记录不持久化」。选它是为了让 `sqlite` 模式在无 MySQL 环境可用（这正是它的价值：轻量化部署），代价是没有短信原文留存。**需要原文留存就必须用 `mysql` 模式**——这不是 bug 而是当前的能力边界，文档与日志都已明示。

补齐方式是给仓储层接 GORM 的纯 Go SQLite 驱动（如 `github.com/glebarez/sqlite`），注意不能用需要 CGO 的 `mattn/go-sqlite3`（见 3.6）。

SQLite 的软删除是为了解决纯阅后即焚的诊断盲区：调用方说「没收到码」时，还能查到码是否曾写入、何时被取走。Cloudflare 版是同一套语义。

### 3.3b `OTP_STORE`：验证码存储后端（与 `DB_DRIVER` 解耦）

`DB_DRIVER` 决定**短信记录**存哪里，`OTP_STORE` 决定**验证码**存哪里，二者是两条独立的轴、可任意组合。

| 取值 | 位置 | 重启后 | 多副本 | 说明 |
|---|---|---|---|---|
| `auto`（默认） | `DB_DRIVER=sqlite` → sqlite，否则 memory | — | 否 | 不设时行为与引入前完全一致 |
| `memory` | 进程内 map | 丢失 | 否 | 零依赖 |
| `sqlite` | `SQLITE_PATH` 的 `otps` 表 | 仍在 | 否 | 单副本自托管 |
| `mysql` | MySQL 的 `otps` 表 | 仍在 | **是** | 需 `DB_DRIVER=mysql`；多副本必选 |

`store/mysql` 与 D1 一致用 **Unix 秒**（SQLite 用毫秒是 3.4 的已知偏离）。两个方言差异导致的必要改动：

1. **取码没有 `UPDATE ... RETURNING`**（那是 SQLite/PG 语法）。MySQL 版改为「条件 UPDATE 抢占 → 判 `RowsAffected()==1` → SELECT 取回」。抢占条件与 SQLite 版的 WHERE 完全相同；`token_hash` 是主键，命中只能是 0 或 1 行。已用 50 并发实测**命中恒为 1**（`store/mysql/concurrency_test.go`，需 `-tags=integration`）。
2. **`Raw().Scan(&string)` 零行时不返回 `ErrRecordNotFound`**，只留下空串。若按 error 判命中，未命中的 token 会被当成「命中且码为空串」返回 `ok=true`，直接毁掉阅后即焚。故取码统一走 `COUNT` 判定命中（`sqlCountPendingByToken`），见 `pendingCode` 的注释。**这是踩过的坑，改这个文件时不要退回按 error 判断。**

MySQL 实现复用 `main.go` 已建立的 `*gorm.DB`，不拥有连接，**刻意不提供 `Close()`**。

### 3.4 时间精度：SQLite 用毫秒，D1 与 MySQL 用秒

`store/sqlite` 的四个时间列是 Unix **毫秒**，`cloudflare/migrations/0001_init.sql`（D1）是**秒**。这是刻意的偏离：

`store.Store` 的 TTL 是 `time.Duration`，若落库时截断到秒，亚秒级 TTL 会被压成 0 —— **写入即过期**，静默改变调用方语义。契约用例里就有 30ms/40ms 的 TTL，用秒精度必然失败。

两份库不共享、不存在跨库查询，所以精度不同没有实际影响。**如果将来要让两者互通，必须先统一精度**。

### 3.5 为什么锁 Go 1.21.4

Go 1.21 的 `net/http.ServeMux` **不支持** `{token}` 通配语法（1.22 才引入）。因此 webhook 路由按**前缀**注册（`handler.WebhookPathPrefix`），由处理器从路径里取 token。

这带来一条硬约束：**不能升 `go.mod` 的 go 指令**。CI 显式设 `GOTOOLCHAIN=local`，若版本不匹配会明确报错而非静默切换，防止 CI 悄悄改用 Go 1.22 而使前缀路由的兼容性假设失效。

升级到 Go 1.22+ 时可改用 `r.PathValue("token")`，但那是一次独立改动。

### 3.6 为什么用 `modernc.org/sqlite`

`gorm.io/driver/sqlite` 依赖 `mattn/go-sqlite3`，**需要 CGO**，与 `CGO_ENABLED=0` + distroless 构建不兼容（见 Dockerfile）。`modernc.org/sqlite` 是纯 Go 实现，交叉编译与 distroless 镜像都没问题。

代价是依赖树变大，且**版本必须固定在 v1.29.0**（见 4.2）。

### 3.7 Cloudflare 子目录为什么要内嵌依赖

`cloudflare/` 是独立 Go module，通过 `replace` 指向内嵌的 `cfvendor/workers-go`。

原因是 Deploy to Cloudflare 按钮**只克隆该子目录**，且构建发生在云端、不保证能访问外部 module 代理。所以 `cfvendor/` 必须提交——它不是普通的 vendor 产物，删掉会导致一键部署失败。

`cloudflare/d1sqltest/` 是另一个独立 module（用 `replace ../` 指向父目录），跑针对真实 SQLite 的 SQL 语义实证，同样应当提交。

### 3.8 与上游 smsforward 特性的接口冲突（合并前必读）

本分支与 `origin/main` 改了**同一批函数签名**，且方向相反——不是简单的文本冲突，合并时必须重新接线。这是 2.1 节那 5 个冲突文件的实质内容。

| 位置 | 本分支（HEAD） | 上游 `origin/main` |
|---|---|---|
| `NewOTPService` | `(repo, otpStore store.Store, hmacSecret)` | `(repo, cache *cache.OTPCache, hmacSecret, templates []model.OTPTemplate)` |
| `NewHandler` | `(svc, webhookSecret)` | `(svc, webhookSecret, smsForwardChannels map[string]model.SMSForwardChannel)` |
| `ProcessIncomingSMS` | `(provider, sender, recipient, body, receivedAt)` | `(provider, sender, recipient, body, channelID, source, receivedAt)` |
| `handler.NewRouter` | 两条路由 | 多一条 `SMSForwardPathPrefix`（`/api/v1/webhook/smsforward/`） |
| `config.Config` | 新增 `DBDriver`、`SQLitePath` | 新增 `OTPTemplates`、`SMSForwardChannels` |

**合并时的取法**（两边改动都保留，不要二选一）：

1. `NewOTPService` 取上游的 4 参数版，**把第 2 个参数类型从 `*cache.OTPCache` 改成 `store.Store`** —— 这正是本分支的核心改动，否则抽象层会被合并冲掉。形参名用 `otpStore`
2. `NewHandler` 直接用上游的 3 参数版（本分支没改它的签名，冲突只来自调用点）
3. `ProcessIncomingSMS` 用上游的 7 参数版，模板提取逻辑（`extractCodeWithTemplates`）随之生效
4. `main.go` 的接线要同时满足两边：`newOTPStore(cfg)` 的结果传给 `NewOTPService`，`cfg.OTPTemplates`、`cfg.SMSForwardChannels` 也要传进去
5. `config.Config` 两组字段都要留（互不冲突，只是同一处结构体）

**合并后必做**：第 5 节门禁全量重跑，并补一次针对新增 `smsforward` 路由的冒烟验证。本分支的 `store` 契约测试（`store/store_test.go`）覆盖不到上游的模板提取路径，两套逻辑的交叉行为目前**没有任何测试覆盖**。

> 已经能预见的语义问题：上游模板提取失败时的回退路径，与本分支 `store` 抽象「写入失败只记日志」的语义叠加后，可能出现「既没提取到码也没报错」的静默场景。合并时建议在 `service` 里明确一条：模板未命中且正则也未命中时，要有一条可观测的日志。

## 4. 改动前必须知道的坑

### 4.1 `go mod tidy` 会破坏 Go 版本锁定

**直接跑 `go mod tidy` 会出问题**：它会把 `modernc.org/sqlite` 升到最新版（v1.59.0+），连带把 `go.mod` 的 go 指令抬到 **1.25.0**，直接违反 3.5 的约束，CI 会挂。

正确做法：

```bash
GOTOOLCHAIN=local go mod tidy    # 固定工具链，版本不会被抬
```

并且 `modernc.org/sqlite` 要**固定在 v1.29.0**。改动依赖后务必确认：

```bash
head -3 go.mod        # 必须是 go 1.21.4
grep modernc.org/sqlite go.mod
```

### 4.2 索引不要手写 DDL

MySQL **不支持 `CREATE INDEX IF NOT EXISTS`**，手写会导致启动时报 `Error 1064`，且错误容易被吞掉，索引**静默缺失**。索引一律用 `model.SMSRecord` 上的 GORM 标签声明，由 `AutoMigrate` 幂等创建。

`repository/integration_test.go` 用真实 MySQL 8.0 验证索引真的建成且 `created_at` 为降序——这是历史缺陷的回归用例，别删。

### 4.3 日志脱敏是硬要求

不得记录验证码明文、完整 token、手机号明文。完整 token 等同缓存 key，可反推手机号。日志里只记 `recipient_hash`（HMAC 前 8 位）。详见 [LOGGING.md](LOGGING.md)。

### 4.4 GORM 的 SQL 日志走 stdout，应用日志走 stderr

容器采集日志时两个流都要收集并合流，否则会丢一半。

### 4.5 集成测试会 DropTable

`TEST_MYSQL_DSN` 必须指向**专用测试库**，勿指向开发库或生产库。

## 5. 门禁

提交前在本地跑一遍（等价于 CI 的 verify job）：

```bash
gofmt -l .                                    # 必须无输出
GOTOOLCHAIN=local go mod tidy                 # go.mod / go.sum 须与 CI 一致
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go test -race -shuffle=on ./...
```

`cloudflare/` 是独立 module，需单独跑（`GOOS=js GOARCH=wasm`）。

交叉编译（验证 CGO 未被引入，distroless 镜像依赖这一点）：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOTOOLCHAIN=local go build ./...
```

MySQL 集成测试需 `-tags=integration` 与真实数据库。

## 6. 未决项

已知的缺口，按优先级排列。前两项是**安全问题**，生产使用前应处理。

### 安全

| 项 | 问题 | 建议 |
|---|---|---|
| 供应商签名校验 | 只比对 URL 中的静态 token，**没有校验供应商签名**。拿到 URL 即可向任意号码注入任意验证码 | 接入各供应商的签名机制 |
| 每账号独立密钥 | 所有调用方共享同一个 `WEBHOOK_SECRET`，无法单独吊销 | 按调用方分配密钥 |
| `/api/v1/otp` 无鉴权限流 | 知道 `HMAC(号码, secret)` 即可读取该号码验证码 | 增加调用方认证与限流 |
| 短信原文留存 | `body` 含验证码明文且**长期保存、无清理策略** | 缩短保留期或只存必要字段 |
| 密钥强度 | `HMAC_SECRET` 若为低熵人工字符串，可被离线字典攻击反推手机号 | 用高熵随机值（`openssl rand -hex 32`） |

### 运维

| 项 | 问题 |
|---|---|
| 无健康检查端点 | 未提供 `/healthz`，容器编排只能靠端口探测；distroless 镜像内也无 shell |
| 写库失败无重试与告警 | 入库失败仅记日志，无重试、无失败计数、无告警，数据静默丢失 |
| 无指标与追踪 | 未暴露 Prometheus 指标或分布式追踪，只有文本日志 |
| 缓存 TTL 与 DB 记录不一致 | 验证码缓存 TTL 到期即失效，数据库记录永久保留，注意合规 |

### 已知行为陷阱

写库失败时（如 MySQL 中断），**验证码仍在存储中可用**，`/api/v1/otp` 依然返回 `success`，但数据库里没有该条记录。"验证码可用"与"记录已留存"是两件独立的事，排障时别混淆（见 LOGGING.md 第 8 节）。

## 7. 下一步建议

按投入产出排序，都是独立可交付的小改动：

1. **把 `cloudflare` 分支合回 `main`** —— 见 2.1 节，这是当前最高优先级：`main` 上没有 SQLite 后端与 `store` 抽象，本分支缺上游的 smsforward 特性，**两边都不完整**。合并需处理 5 个冲突文件（3.8 已列出精确冲突点）
2. **补全 `sqlite` 模式的短信落库** —— 给仓储层接纯 Go 的 GORM SQLite 驱动，消除 3.3 的能力缺口
3. **健康检查端点** —— 加 `GET /healthz`，改动小、能立刻改善容器编排体验
4. **写库失败可观测** —— 至少加失败计数与告警日志，目前是静默丢失
5. **短信原文保留策略** —— 加定期清理或只存必要字段，同时解决合规问题（第 6 节）
6. **存储抽象上抛错误** —— 按 3.2 的说明做独立的破坏性重构，让 `service` 能感知持久化失败

不建议的方向：升级 Go 版本（会动摇 3.5 的路由假设）、替换 SQLite 驱动（3.6 已说明约束）。

## 8. 目录速查

| 路径 | 用途 |
|---|---|
| `main.go` | 入口；`newOTPStore` 是存储接线的唯一分支点 |
| `store/store.go` | 存储抽象，**改接口前先读包注释** |
| `store/sqlite/` | SQLite 实现（纯 Go 驱动，毫秒精度） |
| `store/store_test.go` | 契约用例；新实现在 `implementations` 登记即可复用 |
| `cache/` | 内存实现（`DB_DRIVER=mysql` 时的后端） |
| `service/` | 验证码提取、在途追踪、清理协程 |
| `handler/` | 路由注册（**前缀路由**）、请求校验 |
| `repository/` | MySQL 仓储，短信原文落库 |
| `cloudflare/` | Workers + D1 形态，独立 module，自包含 |
| `docs/LOGGING.md` | 日志字段与级别规范 |
| `.github/workflows/` | CI / Cloudflare 门禁 / 发布 |
