# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

SMSServer 是短信验证码中继服务：接收短信供应商/转发器的 webhook 回调，从短信原文提取验证码并缓存，业务调用方凭 `HMAC-SHA256(收件人号码)` 的 token 轮询取码，无需接入各短信平台 API。

仓库包含**两种协议一致、独立构建的部署形态**：

| 形态 | 位置 | 运行时 | 验证码存储 |
|---|---|---|---|
| 自托管 | 仓库根（`main.go`，module `smsserver`） | 常驻 Go 进程 / Docker | memory / SQLite / MySQL 三选一 |
| Cloudflare | `cloudflare/`（独立 module `smsserver/cloudflare`） | Workers（WASM，`GOOS=js GOARCH=wasm`） | D1（唯一事实源） |

仓库所有文档与代码注释均为中文，沿用此约定。

## 常用命令

### 自托管（仓库根）

```bash
gofmt -l .                          # 格式检查，CI 失败于任何未格式化文件
go vet ./...
go build ./...
go test ./...
go test -race -shuffle=on ./...     # 竞态检测 + 随机用例顺序
go test -run 'TestName' -v ./pkg/   # 跑单个测试/用例（子测试用 TestName/Sub）
CGO_ENABLED=0 go build ./...        # 验证全静态构建（distroless 镜像依赖此点）

# MySQL 集成测试（需真实库，未设 DSN 时自动跳过）
TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:3306)/smsdb_test?parseTime=true&loc=Local&charset=utf8mb4' \
  go test -tags=integration -race ./...
```

集成测试会 `DropTable`，`TEST_MYSQL_DSN` 必须指向**专用测试库**，切勿指向开发/生产库。

### Cloudflare（`cd cloudflare`，独立 module）

```bash
npm install
npm run db:migrations:apply:local   # 首次必须手动建本地 D1 表（wrangler 4 的 dev 不会自动 apply）
npm run dev                        # wrangler dev
npm run build                      # workers-assets-gen + GOOS=js GOARCH=wasm go build -o ./build/app.wasm
npm run deploy                     # wrangler d1 migrations apply --remote + wrangler deploy

GOOS=js GOARCH=wasm go vet ./...   # 静态检查 / WASM 编译
go test ./...                      # 测 otp 包（不依赖 Workers 运行时的纯逻辑）

cd d1sqltest && go test -race ./...  # SQL 语义实证：嵌套独立 module，不会被 ../... 递归到，必须显式跑
```

需要 Go 1.21.4 与 Node 20+。`./scripts/verify-deliverables.sh` 可一键核对交付清单与本地门禁。

## 架构

### 分层与解耦

```
main → handler → service → {store, repository} → model
```

- **`store.Store` 是核心抽象**（`store/store.go`）：只定义验证码存储能力（`Set/Get/Delete/GetAndDelete/Cleanup/Len`），三个实现 `cache.OTPCache`（内存）、`store/sqlite`、`store/mysql` 均满足该接口（各有编译期断言 `var _ store.Store`）。
- `store/store_test.go` 是**同一组契约用例**：新存储实现只需在测试的 `implementations` 登记即复用全部用例，无需另写。
- **换存储只改 `main.go` 的 `newOTPStore` 这一个接线分支点**，`service` 不感知具体实现。
- `handler` 与 `service` 各自通过未导出接口（`otpService` / `smsRepository`）解耦，测试用 fake 注入，**不需要数据库**。
- `repository`（GORM）负责短信原文归档（表 `sms_records`），与验证码存储是两条独立链路。

### 凭据模型（两种形态不同，接入/改动前先分清）

**自托管（仓库根）：两个密钥 + HMAC**

- 取码 token / 缓存 key **不是手机号本身**，而是 `HMAC-SHA256(收件人号码, HMAC_SECRET)` 的十六进制（`model.HMACPhoneNumber`）。调用方必须持有同一 `HMAC_SECRET` 才能算 key，故每个号码一个取码位。
- webhook 鉴权用另一个密钥 `WEBHOOK_SECRET`，作为 URL 路径末段、`subtle.ConstantTimeCompare` 常量时间比较。两密钥独立、均必填（为空启动即退出）。

**Cloudflare（`cloudflare/`）：单个 TOKEN，无 HMAC**

- 发送（webhook 路径末段）与读取（`/api/v1/otp` 的 body `token`）用同一个 `TOKEN`、常量时间比较；读取零计算。TOKEN 只鉴权、不落库。
- 全 Worker 只有一个取码位，D1 行以固定非敏感常量 `otp.SlotKey`（`"singleton"`）为主键，多号码互相覆盖（单人单卡）。`/api/v1/otp` 因此也要鉴权、错误返回 **401**（自托管的错误 HMAC 只得到 pending，不是 401）。

两版共同点：取码为**阅后即焚 / 原子抢占**，同一验证码在并发下只会被一个调用方取走。

### 两条独立的后端选择轴

- **`DB_DRIVER`**（`mysql` 默认 / `sqlite`）决定**短信原文**落在哪里、是否连接 MySQL。
- **`OTP_STORE`**（`auto` 默认 / `memory` / `sqlite` / `mysql`）决定**验证码**存哪里。二者可任意组合（如短信落 MySQL、验证码落本地 SQLite）。
- 默认值刻意保持向后兼容：不设置时行为与引入这两个变量前完全一致。非法值 / 不可能组合（`OTP_STORE=mysql` 但 `DB_DRIVER=sqlite`）**启动期明确报错，不静默降级**。推导逻辑见 `config.Config.ResolveOTPStore`。

### Cloudflare 形态特点

- **单 token / 单取码位**：不做 HMAC，webhook 路径与 `/api/v1/otp` 都用 `TOKEN`；取码位主键是常量 `otp.SlotKey`（migration `0003_single_token.sql` 把 `token_hash` 改名为 `slot_key`），TOKEN 因此不进 D1。
- Workers 实例间内存不共享，D1 是唯一事实源；无常驻进程，处理由自托管版的**异步 + WaitGroup 改为同步**。
- 取码用单条 `UPDATE ... RETURNING`（`status: pending→read`）保证原子性；SQL 语句以导出常量集中在 `cloudflare/otp/sql.go`。
- 提取规则为「关键词紧邻取 4-10 位字母/数字（支持 Gcfx 这类纯字符串码）→ 兜底 4-8 位数字」，实现在 `cloudflare/otp/extract.go`。
- `cloudflare/otp` 包刻意只依赖标准库（regexp/strings/database/sql），使不依赖 Workers 运行时的纯逻辑能在 linux/amd64 下用普通 `go test` 覆盖；`main.go` 带 `//go:build js && wasm`。
- `cloudflare/` 通过 `replace` 指向**必须提交的内嵌依赖** `cfvendor/workers-go`——Deploy to Cloudflare 按钮只克隆该子目录且云端构建不保证能访问外部 module 代理，删掉会导致一键部署失败。
- `d1sqltest/` 是又一个嵌套 module（`replace ../`），用真实 SQLite 方言 + 生产 SQL 语句原文实证原子取码/upsert/清理，其依赖因此不污染主 module 的 WASM 构建。

**CF 版留存策略（需求驱动，与自托管版的关键差异，改动前必读）**：`raw_content` 完整保留短信原文（严禁清洗截断）；提取失败落 `failed` 标记行（`code='Failures'`）、永不被清理、同号新短信 upsert 覆盖；`pending` 未读码**严禁自动删除或过期失效**（`expires_at` 仅为信息字段，取码与清理均不以它为准）；`read` 已消费记录允许累积，仅由每月 1 日 03:00 UTC 的 Cron 物理清理超出 30 天窗口者。

## 改动前必须知道的硬约束

这些多为实测踩坑、且单看某个文件不易察觉：

1. **Go 锁定 1.21.4，不能升 `go.mod` 的 go 指令。** Go 1.21 的 `http.ServeMux` 不支持 `{token}` 通配（1.22 才引入），故 webhook 路由按**前缀**注册（`handler.WebhookPathPrefix`），由处理器手动从路径取 token。CI 设 `GOTOOLCHAIN=local`，版本不匹配会明确报错而非静默下载新工具链。升到 1.22+ 后才可改用 `r.PathValue`，那是一次独立改动。
2. **跑 `go mod tidy` 必须带 `GOTOOLCHAIN=local`。** 否则会把 `modernc.org/sqlite` 升到 v1.59+、连带把 go 指令抬到 1.25，直接违反上一条并挂 CI。`modernc.org/sqlite` 固定在 v1.29.0。
3. **SQLite 驱动全进程统一用 `github.com/glebarez/go-sqlite`，不要直引 `modernc.org/sqlite`。** modernc 与 GORM 用的 `glebarez/sqlite` 都会在 init 注册名为 `sqlite` 的驱动，混用导致启动 panic（`Register called twice`）。
4. **不要手写 `CREATE INDEX`。** MySQL 不支持 `CREATE INDEX IF NOT EXISTS`，手写启动报 Error 1064 且错误被吞、索引静默缺失。一律在 model 上用 GORM `index` 标签声明、由 `AutoMigrate` 幂等创建。
5. **SQLite 模式不能直接 `AutoMigrate(model.SMSRecord)`**：其标签含 MySQL 专有的 `default:CURRENT_TIMESTAMP(3)`，SQLite 建表报语法错误。走 `repository/sqlite_repo.go` 的影子模型（列名逐列一致、仅 DDL 方言不同），DML 仍直接读写 `model.SMSRecord`。
6. **时间精度因库而异**：`store/sqlite` 存 Unix **毫秒**（契约用例有 30ms/40ms 级 TTL，秒精度会写入即过期），D1 与 `store/mysql` 存 Unix **秒**。两库不互通，若将来打通必须先统一精度。
7. **MySQL 取码没有 `UPDATE ... RETURNING`**，实现为「条件 UPDATE 抢占 → 判 `RowsAffected()==1` → SELECT 取回」；命中判定走 COUNT，不要退回按 error 判断——`Raw().Scan` 零行时不返回 `ErrRecordNotFound`，会把未命中误当成「命中空码」毁掉阅后即焚。

## 日志约定

- **脱敏是硬要求**：禁止记录验证码明文、完整 token、手机号明文、短信正文、两套密钥。日志中只记 `recipient_hash`（HMAC 前 8 位，常量 `tokenLogPrefixLen`），足以关联又不足以反推。
- 模块前缀固定为 `[服务] [配置错误] [数据库] [短信] [清理] [网关] [查询]`，新增日志复用其中之一，不自造。
- 鉴权失败（401）、请求体非法（400）必须写日志（含 path/remote），使密钥爆破在日志中可见。
- **GORM 的 SQL 日志走 stdout，应用日志走 stderr**，容器采集须同时收集合流。
- 字段/级别/点位的完整规范见 `docs/LOGGING.md`。

## 其他目录

- `pages/`：纯静态、零构建的文档站（取码助手 / token 计算页 / 接口文档 / SmsForwarder 教程）。改了接口或配置需同步检查 `docs.html`、`smsforward.html`。
- `docs/INTEGRATION.md`：来源方/取码方完整对接流程与 token 计算示例；`docs/HANDOVER.md`：维护视角的设计取舍；`docs/BRANCH-VERIFICATION.md`：分支门禁证据。
