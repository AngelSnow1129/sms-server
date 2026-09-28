# Webhook 对接指南

面向两类对接方：**短信来源方**（把短信转发给本服务的一方，如短信转发器类 App、短信网关）与**取码方**（轮询获取验证码的业务程序）。

协议在自托管版与 Cloudflare Workers 版之间完全一致，仅个别行为有差异，文中以「自托管 / Workers」标注。本文只讲对接，部署与配置全集见 [README](../README.md) 与 [cloudflare/README](../cloudflare/README.md)。

## 1. 角色与数据流

```
短信来源方                                取码方
   │ POST /api/v1/webhook/sms/{token}       │ POST /api/v1/otp {"token":"<HMAC hex>"}
   ▼                                        ▼
┌───────────────────────── SMSServer ─────────────────────────┐
│  校验 webhook token → 提取验证码 → 按 HMAC(收件人号) 存储      │
│                                        ← 命中即返回并失效     │
└──────────────────────────────────────────────────────────────┘
```

- **短信来源方**只需知道 webhook URL（含 `WEBHOOK_SECRET` 作为路径 token）。
- **取码方**只需持有 `HMAC_SECRET`，用它对收件人号码计算 HMAC 得到取码 token。两方不需要互通信息，服务端是中间人。

## 2. 短信来源方对接（Webhook）

### 接口

```
POST /api/v1/webhook/sms/{WEBHOOK_SECRET}
```

`{WEBHOOK_SECRET}` 就是部署时配置的 webhook 密钥本身，作为 URL 最后一段传递。路径含多余层级（如 `.../sms/密钥/extra`）按鉴权失败处理。

### 请求体

JSON，`Content-Type` 不强制校验，上限 64 KiB：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `provider` | string | 否 | 来源标识，随意填写（如 `smsforwarder`），仅入库归档用 |
| `sender` | string | 否 | 发送方号码 |
| `recipient` | string | **是** | 接收方号码。**取码方要用同一字符串计算 HMAC**，请原样传递（保留国家码前缀、不加空格） |
| `body` | string | **是** | 短信原文，服务端从中提取第一个 4-8 位数字作为验证码 |

示例：

```bash
curl -s -X POST "https://你的域名/api/v1/webhook/sms/$WEBHOOK_SECRET" \
  -H 'Content-Type: application/json' \
  -d '{"provider":"smsforwarder","sender":"+8613800000000","recipient":"+8613900000000","body":"【某某】您的验证码是123456，5分钟内有效。"}'
```

### 响应

| 状态码 | 响应体 | 场景 |
|---|---|---|
| 200 | `{"status":"ok"}` | 校验通过，已受理 |
| 401 | `unauthorized`（纯文本） | token 为空、不匹配，或路径多余层级 |
| 400 | `invalid json`（纯文本） | 请求体不是合法 JSON 或超 64 KiB |
| 405 | `method not allowed`（纯文本） | 非 POST |
| 500 | `internal error`（纯文本） | **仅 Workers 版**：写 D1 失败 |

### 两条来源方必须知道的行为差异

| 行为 | 自托管版 | Workers 版 |
|---|---|---|
| 200 的含义 | **受理即返回**，短信异步处理；写库失败只记日志，调用方无感知 | **同步处理完再返回**；写 D1 失败会返回 500 |
| 200 是否等于「验证码已可取」 | 不等于（异步窗口内取码可能得到 pending，稍后重试即可） | 基本等于（同步写入成功后才返回 200） |

> 短信转发器类 App 的对接方式：在其「Webhook 转发」配置中填入上面的 URL（token 拼在路径里），请求体用 App 的模板变量映射到 `provider`/`sender`/`recipient`/`body` 四个字段。不同 App 的变量名不同，以所用 App 的文档为准；关键是 **`recipient` 必须是收件人号码原文**，否则取码方算出的 HMAC 对不上。

### 提取规则

服务端用正则 `\b(\d{4,8})\b` 取短信原文中**第一个** 4-8 位数字。这是通用启发式，没有语义识别：若短信中先出现订单号、日期片段等数字，会被误当验证码。常见输入的提取结果见 [README 提取规则](../README.md#验证码提取规则)。

## 3. 取码方对接

### 取码 token 的计算

```
token = HMAC-SHA256(key = HMAC_SECRET, message = 收件人号码).hex（小写）
```

- `message` 必须与 webhook 请求体里的 `recipient` **逐字节一致**（含 `+86` 前缀）。
- 输出为十六进制小写字符串，无需再编码。

多语言示例（均以 `recipient = +8613900000000` 为例）：

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

请求体：`{"token": "<HMAC hex>"}`，首尾空白会被裁剪。该接口不需要任何密钥头——token 本身就是凭据。

### 响应

| 状态码 | 响应体 | 说明 |
|---|---|---|
| 200 | `{"status":"success","code":"123456"}` | 命中，`code` 为该号码最近一次提取到的验证码 |
| 200 | `{"status":"pending"}` | 未命中：未收到短信 / 已过期 / 已被取走 / token 算错 |
| 400 | `missing token` / `invalid json` | 请求体问题 |
| 405 | `method not allowed` | 非 POST |

注意 4xx 是纯文本、200 才是 JSON。

### 取码语义（取码方必须处理）

1. **阅后即焚**：`success` 一次后该码立即失效，第二次查询必为 `pending`。不要对同一次业务重试取码。
2. **覆盖**：同号来新短信，新码覆盖旧码，旧码立即不可取。
3. **过期**：有效期后取不到（自托管默认 5 分钟 `OTP_CACHE_TTL_MINUTES`；Workers 默认 300 秒 `OTP_TTL_SECONDS`）。已读记录在服务端还会软保留 10 分钟用于对账，但对取码方永远返回 `pending`，无需关心。
4. **一次业务只需取一个码**：命中即停，不要把 `pending` 当失败重试到 TTL 结束。

## 4. 轮询建议

- **间隔 3~5 秒**，从触发短信后开始，最迟查询到 `TTL` 截止。
- 服务端**没有内置限流**。取码走主键索引，单次成本极低，但死循环高频轮询在 Workers 免费额度下会打满 D1 每日额度（超限后查询直接报错到 UTC 午夜重置）。
- 合理模式：`间隔 3s × 持续 5min × 单号码` 远低于任何限额。

## 5. Token 与密钥的环境配置

两个密钥都是**环境自定义**的，且三 种部署形态都支持，不落任何配置文件：

| 密钥 | 用途 | 使用方 |
|---|---|---|
| `WEBHOOK_SECRET` | webhook 路径鉴权 token | 短信来源方 |
| `HMAC_SECRET` | 取码 token 的 HMAC 密钥 | 取码方 |

**生成**（两端都要用高熵随机值，低熵值可被离线字典攻击反推手机号）：

```bash
openssl rand -hex 32
```

### 各形态的配置方式

| 形态 | `WEBHOOK_SECRET` / `HMAC_SECRET` | 说明 |
|---|---|---|
| 自托管进程 | `export WEBHOOK_SECRET=...`、`export HMAC_SECRET=...` | **必填**，为空进程立即退出（fail-closed） |
| Docker | `docker run -e WEBHOOK_SECRET=... -e HMAC_SECRET=...` | 同上，见 README 快速开始 |
| Cloudflare Workers | 部署向导中填写，或 `npx wrangler secret put WEBHOOK_SECRET`；本地开发填 `cloudflare/.dev.vars` | 以 **Secret** 形式注入（不进 `wrangler.jsonc`、不进代码库）；为空时 webhook 一律 401、取码必 `pending` |

### 自检：密钥是否真的生效

| 形态 | 方法 |
|---|---|
| 自托管 | 启动日志；或故意不设密钥，进程应拒绝启动 |
| Workers | 浏览器访问 `https://sms-server.<子域>.workers.dev/`，返回 `hmac_secret_set` / `webhook_set` 两个布尔值（不含明文） |

### 轮换（换密钥）的影响

- 换 `WEBHOOK_SECRET`：旧 URL 立即 401，**短信来源方必须同步改 URL**。
- 换 `HMAC_SECRET`：**所有已下发未取的验证码立即对取码方不可见**（取码 token 是用它算的），且取码方必须换新密钥重算。建议在无在途验证码的窗口期操作。
- Workers 版换 Secret 用 `wrangler secret put`，立即生效、无需重新部署。

## 6. 联调自检清单

按顺序执行，全部通过即对接完成：

1. 密钥已配置并自检通过（见上节）。
2. 用 `curl` 发一条模拟短信，期望 200 `{"status":"ok"}`（Workers 版若 D1 写失败会得到 500，先查部署）。
3. 用取码方语言算出 HMAC token，POST `/api/v1/otp`，期望 `{"status":"success","code":"<模拟短信里的码>"}`。
4. 立即再查一次，期望 `{"status":"pending"}`（阅后即焚生效）。
5. 等超过 TTL 后再查，期望仍 `pending`（过期生效）。
6. 换一个错误 token 查询，期望 `pending`（token 校验生效，不会串号）。

## 7. 安全注意事项

- 全链路 HTTPS：webhook token 在 URL 路径里，可能出现在中间设备的访问日志中。
- 不要把两个密钥写进代码、前端或客户端 App；它们只应存在于服务端配置与取码方服务端。
- 当前所有来源方共享同一个 `WEBHOOK_SECRET`，无法单独吊销某一方；如需按来源分权，见 README「安全建议」中的演进方向。
