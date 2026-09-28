# SMSServer Cloudflare 版（Workers + D1）

[![Cloudflare 门禁](https://github.com/AngelSnow1129/WebHookServer/actions/workflows/cloudflare.yml/badge.svg)](https://github.com/AngelSnow1129/WebHookServer/actions/workflows/cloudflare.yml)

自托管版的无服务器移植：跑在 Cloudflare Workers 上，验证码存 D1，**免费额度内可长期 $0/月 运行，无需任何服务器与数据库运维**。

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/AngelSnow1129/WebHookServer/tree/main/cloudflare)

> 点击按钮 → 登录 Cloudflare → 向导中填写两个密钥 → Deploy。
> Cloudflare 会自动：克隆仓库 → 创建 D1 数据库 → 运行 migration 建表 → 构建并部署 Worker。

## 一键部署会做什么

| 步骤 | 说明 |
|---|---|
| 克隆仓库 | 仅使用 `cloudflare/` 子目录（自包含：内嵌依赖，无需外部 module 代理） |
| 创建 D1 | 由 `wrangler.jsonc` 中的 `d1_databases` 声明自动开通，`database_id` 自动回填 |
| 建表 | `deploy` 脚本先执行 `wrangler d1 migrations apply DB --remote` |
| 构建编译 | 先由 `workers-assets-gen` 生成 Workers 运行时资源，再以 `GOOS=js GOARCH=wasm` 编译为 WASM |
| 配置密钥 | 向导中按 `package.json` 的 `cloudflare.bindings` 说明填写 `HMAC_SECRET` / `WEBHOOK_SECRET` |

## 部署时需要填写的配置

| 变量 | 必填 | 说明 |
|---|---|---|
| `HMAC_SECRET` | 是 | 手机号 HMAC-SHA256 密钥，**必须与调用方一致**。生成：`openssl rand -hex 32` |
| `WEBHOOK_SECRET` | 是 | webhook 路径鉴权密钥，即 `/api/v1/webhook/sms/{token}` 中的 `{token}`。生成：`openssl rand -hex 32` |
| `OTP_TTL_SECONDS` | 否 | 验证码有效期（秒），默认 `300` |

> 密钥以 Worker Secrets 形式注入，不会写入代码仓库。部署后可用 `npx wrangler secret put HMAC_SECRET` 随时更换。

## 部署后验证

```bash
# 根路径返回配置摘要（不含密钥明文），确认两个密钥均已生效
curl https://sms-server.<你的子域>.workers.dev/

# 发送一条模拟短信（WEBHOOK_SECRET 换成你的）
curl -s -X POST "https://sms-server.<你的子域>.workers.dev/api/v1/webhook/sms/$WEBHOOK_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"twilio","sender":"+8613800000000","recipient":"+8613900000000","body":"【某某】您的验证码是123456，5分钟内有效。"}'

# 取码（TOKEN = HMAC-SHA256(收件人号码, HMAC_SECRET) 的十六进制，算法与自托管版一致）
curl -s -X POST "https://sms-server.<你的子域>.workers.dev/api/v1/otp" -d "{\"token\":\"$TOKEN\"}"
# → {"status":"success","code":"123456"}

# 再查一次 → {"status":"pending"}（阅后即焚）
```

## 接口（与自托管版协议一致）

| 接口 | 行为 |
|---|---|
| `POST /api/v1/webhook/sms/{WEBHOOK_SECRET}` | 提取验证码写入 D1；同号新码覆盖旧码；未提取到验证码不写入 |
| `POST /api/v1/otp` `{"token":"<HMAC hex>"}` | 命中即原子标记已读并返回 `success`，否则 `pending` |

**读取原子性**：取码是单条 `UPDATE ... RETURNING code`（`status='pending'` → `'read'` 并写 `read_at`），并发下不可能两个请求同时把同一个码从 `pending` 翻成 `read`。

**删除策略**（解决纯阅后即焚无法对账的问题）：

- 读取成功 → 立即失效（第二次查询必为 `pending`），记录**软删除**（`status='read'` + `read_at`）保留 10 分钟供排查「调用方说没收到码」
- Cron 每 30 分钟物理清理：已读超 10 分钟的记录 + 未读但已过期的记录
- 数据因此不无限积累，D1 存储（免费 5 GB）与写额度（免费 10 万行/天）远够用

## 免费额度参考（Workers Free，每日 UTC 重置）

| 项 | 免费额度 | 本项目典型消耗 |
|---|---:|---|
| Worker 请求 | 10 万/天 | webhook + 轮询 |
| D1 行读 | 500 万/天 | 取码走主键索引，每次 1~2 行 |
| D1 行写 | 10 万/天 | 每条短信约 3 行（写入 + 标记已读 + 清理删除） |
| D1 存储 | 5 GB | 记录滚动清理，不积累 |

> 注意：免费额度超出后 D1 查询会直接报错（直到 UTC 午夜重置）。调用方请勿死循环轮询；如遇额度问题可在 Cloudflare 控制台 D1 Metrics 中核对行数消耗。

## 本地开发

```bash
cd cloudflare
cp .dev.vars.example .dev.vars   # 填入两个密钥
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
| 运行时 | 常驻进程（Docker/二进制） | Cloudflare Workers（WASM） |
| 存储 | 内存缓存 + MySQL 归档 | D1 唯一事实源 |
| 短信原文留存 | 落库 | **不留存**（PII 最小化） |
| 处理方式 | 异步 + WaitGroup | 同步（Workers 无常驻进程） |
| 过期清理 | 内存 TTL + 进程内协程 | D1 字段 + Cron Trigger |
| 健康检查 | 无（见根 README 已知限制） | `GET /` 返回配置摘要 |

其余协议（路由、鉴权、HMAC token、响应格式）与自托管版一致，调用方无需区分部署形态。
