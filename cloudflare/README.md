# SMSServer Cloudflare 版（Workers + D1）

[![Cloudflare 门禁](https://github.com/AngelSnow1129/WebHookServer/actions/workflows/cloudflare.yml/badge.svg)](https://github.com/AngelSnow1129/WebHookServer/actions/workflows/cloudflare.yml)

自托管版的无服务器移植：跑在 Cloudflare Workers 上，验证码存 D1，**免费额度内可长期 $0/月 运行，无需任何服务器与数据库运维**。

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/AngelSnow1129/WebHookServer/tree/cloudflare/cloudflare)

> 点击按钮 → 登录 Cloudflare → 向导中填写一个 TOKEN → Deploy。
> Cloudflare 会自动：克隆仓库 → 创建 D1 数据库 → 运行 migration 建表 → 构建并部署 Worker。

## 一个 token，发送和读取都用它

本版只有**一个密钥 `TOKEN`**，不做 HMAC：

| 用途 | 用法 |
|---|---|
| 发送（鉴权短信来源） | `POST /api/v1/webhook/sms/{TOKEN}` |
| 读取（鉴权取码方） | `POST /api/v1/otp`，body `{"token":"TOKEN"}` |

读取方**无需任何计算**，直接发同一个 TOKEN；服务端用常量时间比较校验。TOKEN 只用于鉴权、不落库，D1 里那一个取码位以固定、非敏感的常量 `singleton` 为主键。

## 一键部署会做什么

| 步骤 | 说明 |
|---|---|
| 克隆仓库 | 仅使用 `cloudflare/` 子目录（自包含：内嵌依赖，无需外部 module 代理） |
| 创建 D1 | 由 `wrangler.jsonc` 中的 `d1_databases` 声明自动开通，`database_id` 自动回填 |
| 建表 | `deploy` 脚本先执行 `wrangler d1 migrations apply DB --remote` |
| 构建编译 | 先由 `workers-assets-gen` 生成 Workers 运行时资源，再以 `GOOS=js GOARCH=wasm` 编译为 WASM |
| 配置 TOKEN | 向导中按 `package.json` 的 `cloudflare.bindings` 说明填写 `TOKEN` |

## 部署时需要填写的配置

| 变量 | 必填 | 说明 |
|---|---|---|
| `TOKEN` | 是 | 唯一鉴权 token，webhook 路径与 `/api/v1/otp` 请求都用它。生成：`openssl rand -hex 32` |
| `OTP_TTL_SECONDS` | 否 | 验证码**建议**有效期（秒），默认 `300`；仅作信息字段，未读码不会因它自动失效 |

> TOKEN 以 Worker Secrets 形式注入，不会写入代码仓库。部署后可用 `npx wrangler secret put TOKEN` 随时更换。

## 部署后验证

```bash
TOKEN='你的TOKEN'

# 根路径返回配置摘要（不含密钥明文），确认 TOKEN 已生效
curl https://sms-server.<你的子域>.workers.dev/

# 发送一条模拟数字验证码
curl -s -X POST "https://sms-server.<你的子域>.workers.dev/api/v1/webhook/sms/$TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"twilio","sender":"+8613800000000","recipient":"+8613900000000","body":"【某某】您的验证码是123456，5分钟内有效。"}'

# 取码（直接发同一个 TOKEN，不用算 HMAC）
curl -s -X POST "https://sms-server.<你的子域>.workers.dev/api/v1/otp" -d "{\"token\":\"$TOKEN\"}"
# → {"status":"success","code":"123456","raw_content":"【某某】您的验证码是123456，5分钟内有效。"}

# 再查一次 → {"status":"pending"}（阅后即焚，只有被消费才失效）

# 字符串验证码同样支持，例如短信「您的验证码为Gcfx，该验证码只能使用一次，请勿泄露于他人。」
# → {"status":"success","code":"Gcfx","raw_content":"您的验证码为Gcfx，……"}

# 若最近一条短信提取不到验证码 → {"status":"failures","code":"Failures","raw_content":"<短信原文>","reason":"..."}
# （失败记录长期保留，新短信到达后自动覆盖）
```

## 接口

完整对接流程（轮询建议、联调自检清单、与自托管版差异）见 [docs/INTEGRATION.md](../docs/INTEGRATION.md)。

| 接口 | 行为 |
|---|---|
| `POST /api/v1/webhook/sms/{TOKEN}` | 提取验证码连同短信原文（`raw_content`，完整保留不截断）upsert 到取码位；新短信覆盖旧码；提取不到验证码时落失败标记（`status='failed'`、`code='Failures'`） |
| `POST /api/v1/otp` `{"token":"TOKEN"}` | TOKEN 鉴权；命中即原子标记已读，返回 `success` + `code` + `raw_content`；最近一条提取失败时返回 `failures` + 原文 + 原因；其余（从未写入 / 已被消费）返回 `pending`。TOKEN 错误返回 401 |

**读取原子性**：取码是单条 `UPDATE ... RETURNING code, raw_content`（`status='pending'` → `'read'` 并写 `read_at`），并发下不可能两个请求同时把同一个码从 `pending` 翻成 `read`。

**留存策略**（需求驱动，与自托管版的关键差异）：

- **原文完整保留**：`raw_content` 存短信原文（`req.Body` 原样，严禁清洗截断），取码成功与失败标记查询都会返回
- **失败可见**：提取不到验证码不静默丢弃，落 `failed` 标记行，用户查询即见 `status=failures` 与原文；该记录永不被清理删除，新短信按 upsert 覆盖
- **未读严禁删除**：`pending` 验证码属于关键有效数据，不自动删除、不自动过期失效（`expires_at` 降级为信息字段，仅记录建议有效期）；失效途径只有两条——被消费、或被新短信覆盖
- **已消费低频清理**：`read` 记录允许累积，每月 1 日 03:00（UTC）Cron 批量物理清理超出 30 天窗口的记录

## 免费额度参考（Workers Free，每日 UTC 重置）

| 项 | 免费额度 | 本项目典型消耗 |
|---|---:|---|
| Worker 请求 | 10 万/天 | webhook + 轮询 |
| D1 行读 | 500 万/天 | 取码每次 1~2 行 |
| D1 行写 | 10 万/天 | 每条短信约 3 行（写入 + 标记已读 + 清理删除） |
| D1 存储 | 5 GB | 记录滚动清理，不积累 |

> 注意：免费额度超出后 D1 查询会直接报错（直到 UTC 午夜重置）。调用方请勿死循环轮询；如遇额度问题可在 Cloudflare 控制台 D1 Metrics 中核对行数消耗。

## 本地开发

```bash
cd cloudflare
cp .dev.vars.example .dev.vars   # 填入 TOKEN
npm install
npm run db:migrations:apply:local # 建本地 D1 表（wrangler dev 不会自动建表，必须手动执行一次）
npm run dev                      # wrangler dev，本地 D1 + Worker
```

新增脚本 `db:migrations:apply:local`（`wrangler d1 migrations apply DB --local`）。
实测：wrangler 4.x 的 `wrangler dev` **不会**自动应用 migration（本地 D1 表为空，
首个请求会因 `no such table: otps` 返回 500），故本地首次启动前需手动执行一次。

需要 Go 1.21.4 与 Node.js（wrangler 4 要求 Node 20+）。

## 与自托管版的差异

| 项 | 自托管版（仓库根目录） | 本目录 |
|---|---|---|
| 密钥 | `WEBHOOK_SECRET` + `HMAC_SECRET` 两个 | **单个 `TOKEN`**，发送读取通用、无 HMAC |
| 运行时 | 常驻进程（Docker/二进制） | Cloudflare Workers（WASM） |
| 取码凭据 | `HMAC-SHA256(号码, HMAC_SECRET)` | 直接发 `TOKEN` |
| 取码位 | 每个号码一行 | 单个取码位（多号码互相覆盖） |
| 存储 | 内存缓存 + MySQL/SQLite 归档 | D1 唯一事实源 |
| 处理方式 | 异步 + WaitGroup | 同步（Workers 无常驻进程） |
| 过期清理 | 内存 TTL + 进程内协程 | D1 字段 + Cron Trigger |
| 健康检查 | 无（见根 README 已知限制） | `GET /` 返回配置摘要 |

取码位虽只有一个，但协议（路由形态、JSON 响应、阅后即焚）与自托管版一致，单人单卡场景无需关心差异。
