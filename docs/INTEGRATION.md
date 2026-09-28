# Webhook 对接指南

面向两类对接方：**短信来源方**（把短信转发给本服务的一方，如短信转发器类 App、短信网关）与**取码方**（轮询获取验证码的业务程序）。

> **重要：两种部署形态的取码凭据现在不同，接入前先确认你用哪种。**
>
> | 形态 | 密钥数量 | webhook 路径 token | 取码方发送的 token |
> |---|---|---|---|
> | **Cloudflare Workers** | **一个 `TOKEN`** | `TOKEN` | 直接发 `TOKEN`（无需计算） |
> | **自托管 / Docker** | 两个：`WEBHOOK_SECRET`、`HMAC_SECRET` | `WEBHOOK_SECRET` | `HMAC-SHA256(收件人号码, HMAC_SECRET)` |
>
> 部署与配置全集见 [README](../README.md)（自托管）与 [cloudflare/README](../cloudflare/README.md)。本文只讲对接。

## 1. 角色与数据流

```
短信来源方                                 取码方
   │ POST /api/v1/webhook/sms/<路径token>      │ POST /api/v1/otp {"token":"<取码凭据>"}
   ▼                                          ▼
┌────────────────────────── SMSServer ────────────────────────┐
│  校验路径 token → 提取验证码 → 写入取码位                      │
│                                        ← 命中即返回并核销      │
└──────────────────────────────────────────────────────────────┘
```

- **短信来源方**只需知道 webhook URL（路径末段是密钥）。
- **取码方**只需持有取码凭据：Workers 版就是 `TOKEN` 本身；自托管版用 `HMAC_SECRET` 对收件人号码算出。

## 2. 短信来源方对接（Webhook）

### 接口

```
POST /api/v1/webhook/sms/{路径token}
```

- Workers 版 `{路径token}` = `TOKEN`；自托管版 = `WEBHOOK_SECRET`。
- 路径含多余层级（如 `.../sms/密钥/extra`）按鉴权失败处理。

### 请求体

JSON，`Content-Type` 不强制校验，上限 64 KiB：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `provider` | string | 否 | 来源标识（如 `smsforwarder`），仅归档用 |
| `sender` | string | 否 | 发送方号码 |
| `recipient` | string | 自托管必填 | 接收方号码。**自托管取码凭据按它计算，必须是号码原文**（保留 `+86` 前缀、不加空格）。Workers 单取码位下不参与定位，可传但非必需 |
| `body` | string | **是** | 短信原文，服务端从中提取验证码 |

示例（Workers 版把 `$TOKEN` 换成本地 TOKEN；自托管换成 `$WEBHOOK_SECRET`）：

```bash
curl -s -X POST "https://你的域名/api/v1/webhook/sms/$TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"smsforwarder","sender":"+8613800000000","recipient":"+8613900000000","body":"【某某】您的验证码是123456，5分钟内有效。"}'
```

### 响应

| 状态码 | 响应体 | 场景 |
|---|---|---|
| 200 | `{"status":"ok"}` | 校验通过，已受理 |
| 401 | `unauthorized`（纯文本） | 路径 token 为空 / 不匹配，或路径多余层级 |
| 400 | `invalid json`（纯文本） | 请求体不是合法 JSON 或超 64 KiB |
| 405 | `method not allowed`（纯文本） | 非 POST |
| 500 | `internal error`（纯文本） | **仅 Workers 版**：写 D1 失败 |

### 两条来源方必须知道的行为差异

| 行为 | 自托管版 | Workers 版 |
|---|---|---|
| 200 的含义 | **受理即返回**，短信异步处理；写库失败只记日志，调用方无感知 | **同步处理完再返回**；写 D1 失败返回 500 |
| 200 是否等于「验证码已可取」 | 不等于（异步窗口内取码可能得到 pending，稍后重试即可） | 基本等于（同步写入成功后才返回 200） |

> 短信转发器类 App：在「Webhook 转发」配置里填上面的 URL，请求体用 App 模板变量映射到 `provider`/`sender`/`recipient`/`body`。自托管版关键是 **`recipient` 必须是收件人号码原文**，否则取码方算出的 HMAC 对不上；Workers 单 token 版无此约束。

### 提取规则

服务端先在「验证码 / 动态码 / 校验码 / 动态密码」或英文「verification code / code / otp / passcode」等关键词**紧邻位置**取一个 4-10 位的字母/数字串，取不到再用兜底正则 `\b(\d{4,8})\b` 取第一个 4-8 位数字。因此支持：

| 短信内容 | 提取结果 |
|---|---|
| `您的验证码是123456` | `123456` |
| `您的验证码为Gcfx，该验证码只能使用一次，请勿泄露于他人。` | `Gcfx` |
| `验证码：Ab12Cd` | `Ab12Cd` |
| `订单号20240921123456，验证码为9876` | `9876` |
| `您的话费余额不足，请及时充值。` | 无 |

启发式没有完整语义识别，极少数排版仍可能误提；关键词紧邻设计已尽量避免把正文单词/订单号当成码。

## 3. 取码方对接

### 取码凭据

**Workers 版：直接用 TOKEN，零计算。**

```
请求体 {"token":"<TOKEN>"}
```

**自托管版：对收件人号码做 HMAC-SHA256。**

```
token = HMAC-SHA256(key = HMAC_SECRET, message = 收件人号码).hex（小写）
```

- `message` 必须与 webhook 请求体里的 `recipient` **逐字节一致**（含 `+86` 前缀）。
- 多语言示例（`recipient = +8613900000000`）：

```python
# Python
import hmac, hashlib
token = hmac.new(HMAC_SECRET.encode(), RECIPIENT.encode(), hashlib.sha256).hexdigest()
```

```go
// Go
mac := hmac.New(sha256.New, []byte(hmacSecret))
mac.Write([]byte(recipient))
token := hex.EncodeToString(mac.Sum(nil))
```

```javascript
// Node.js
const token = crypto.createHmac('sha256', hmacSecret).update(recipient).digest('hex');
```

### 接口

```
POST /api/v1/otp
```

首尾空白会被裁剪。鉴权与未命中的表现两种形态不同，见下表。

### 响应

**Workers 版**（token 错误直接 401）：

| 状态码 | 响应体 | 说明 |
|---|---|---|
| 200 | `{"status":"success","code":"123456","raw_content":"<短信原文>"}` | 命中；返回码与完整原文 |
| 200 | `{"status":"failures","code":"Failures","raw_content":"<原文>","reason":"..."}` | 最近一条短信未提到验证码 |
| 200 | `{"status":"pending"}` | 未命中：从未写入 / 已被消费 |
| 401 | `unauthorized` | body token 与 TOKEN 不匹配 |
| 400 / 405 | `missing token` / `invalid json` / `method not allowed` | 请求体或方法问题 |

**自托管版**（无独立鉴权；HMAC 本身是凭据，算错只会得到 pending，不是 401）：

| 状态码 | 响应体 | 说明 |
|---|---|---|
| 200 | `{"status":"success","code":"123456"}` | 命中 |
| 200 | `{"status":"pending"}` | 未收到 / 已过期 / 已被取走 / HMAC 算错 |
| 400 / 405 | 同上文本 | 请求体或方法问题 |

> 4xx 统一纯文本，200 才是 JSON。

### 取码语义（取码方必须处理）

1. **阅后即焚（两版相同）**：`success` 一次后该码立即核销，第二次查询必为非 success。不要对同一次业务重试取码。
2. **覆盖（两版相同）**：来新短信，新码覆盖旧码，旧码立即不可取。
3. **过期（两版不同）**：
   - 自托管：未读码在 `OTP_CACHE_TTL_MINUTES`（默认 5 分钟）后失效；持久化实现里已读记录再软保留 10 分钟供对账。
   - **Workers：未读码永不过期**，`OTP_TTL_SECONDS` 仅作建议信息字段；失效只有「被消费」或「被新短信覆盖」两条途径。
4. **一次业务只需取一个码**：命中即停，不要把 pending 当失败无限重试。

## 4. 轮询建议

- **间隔 3~5 秒**，从触发短信后开始。自托管最迟查到 TTL 截止；Workers 未读不过期，可按业务需要设定总等待时长。
- 服务端**没有内置限流**。取码成本极低，但死循环高频轮询在 Workers 免费额度下会打满 D1 每日额度（超限后查询直接报错到 UTC 午夜重置）。
- 合理模式：`间隔 3s × 持续 5min` 远低于任何限额。

## 5. Token 与密钥的环境配置

密钥均为**环境自定义**、不落任何配置文件。

| 形态 | 配置项 | 必填失败行为 |
|---|---|---|
| Cloudflare Workers | 单个 `TOKEN`（+ 可选 `OTP_TTL_SECONDS`） | 未配置时 webhook 一律 401、取码 401 |
| 自托管 / Docker | `WEBHOOK_SECRET` + `HMAC_SECRET` 两个 | 为空进程立即退出（fail-closed） |

**生成高熵随机值**（低熵值可被离线字典攻击反推手机号）：

```bash
openssl rand -hex 32
```

### 各形态的配置方式

| 形态 | 命令 | 注入方式 |
|---|---|---|
| 自托管进程 | `export WEBHOOK_SECRET=...`、`export HMAC_SECRET=...` | 环境变量 |
| Docker | `docker run -e WEBHOOK_SECRET=... -e HMAC_SECRET=...` | 环境变量 |
| Cloudflare Workers | 部署向导填 `TOKEN`，或 `npx wrangler secret put TOKEN`；本地开发填 `cloudflare/.dev.vars` | 以 **Secret** 注入（不进 `wrangler.jsonc`、不进代码库） |

### 自检：密钥是否真的生效

| 形态 | 方法 |
|---|---|
| 自托管 | 启动日志；或故意不设密钥，进程应拒绝启动 |
| Workers | 浏览器访问 `https://sms-server.<子域>.workers.dev/`，返回 `token_set` 布尔值（不含明文） |

### 轮换（换密钥）的影响

- **Workers（单个 TOKEN）**：来源方 URL 与取码方 body 都要同步换；`wrangler secret put TOKEN` 立即生效、无需重新部署。已在取码位但未取的码仍在，只是凭据变了。
- 自托管换 `WEBHOOK_SECRET`：旧 URL 立即 401，来源方必须同步改 URL。
- 自托管换 `HMAC_SECRET`：所有未取验证码立即对取码方不可见（取码 token 用它算），必须重算。建议在无在途验证码时操作。

## 6. 联调自检清单

### Cloudflare Workers 版

1. `TOKEN` 已配置，访问根路径 `token_set=true`。
2. `curl` 发模拟短信到 `/webhook/sms/$TOKEN`，期望 200 `{"status":"ok"}`（D1 写失败会 500）。
3. POST `/api/v1/otp` body `{"token":"$TOKEN"}`，期望 `{"status":"success","code":"<短信里的码>"}`。
4. 立即再查，期望 `{"status":"pending"}`（阅后即焚）。
5. 用错误 TOKEN 查询，期望 401 `unauthorized`。

### 自托管版

1. 两个密钥已配置（启动日志可见）。
2. `curl` 发模拟短信，期望 200 `{"status":"ok"}`。
3. 用取码方语言算出 HMAC token，POST `/api/v1/otp`，期望 `{"status":"success","code":"<码>"}`。
4. 立即再查，期望 `{"status":"pending"}`。
5. 超过 TTL 后再查，期望 `pending`（过期生效）；用错误 HMAC 查询也得 `pending`（不串号）。

## 7. 安全注意事项

- 全链路 HTTPS：路径 token 可能出现在中间设备的访问日志中。
- 不要把 TOKEN / 两套密钥写进代码、前端或客户端 App。
- Workers 版单个 TOKEN 同时授权「发短信」和「取码」，持有它即同时拥有两种能力——适合单人单卡；需要把两类权限分给不同方时用自托管版的双密钥形态。
- 自托管所有来源方共享同一个 `WEBHOOK_SECRET`，无法单独吊销某一方；如需按来源分权，见 README「安全建议」的演进方向。
