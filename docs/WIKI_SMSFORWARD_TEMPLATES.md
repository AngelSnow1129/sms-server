# Wiki：验证码模板扩展与 SMSForward 通道配置

## 目标

- 支持中英文关键词与字母数字混合验证码提取。
- 支持 SMSForward 多通道接收、独立鉴权与按通道隔离。
- 保持 `/api/v1/otp` 查询接口兼容。

## 新增配置

### 1) OTP_TEMPLATES_JSON

用于定义模板化验证码提取策略（模板优先，最后回退数字正则）。

字段：

- `id`: 模板 ID
- `keywords`: 关键词数组（如 `验证码`、`verification code`、`otp`）
- `code_type`: `numeric` / `alpha` / `alnum`
- `min_length` / `max_length`: 长度范围
- `providers`（可选）: 限定 provider
- `channels`（可选）: 限定 channel_id

示例：

```json
[
  {
    "id": "en_alnum",
    "keywords": ["verification code", "otp", "code"],
    "code_type": "alnum",
    "min_length": 4,
    "max_length": 10,
    "providers": ["smsforward"],
    "channels": ["android-main"]
  }
]
```

> 为空或非法 JSON 时自动回退到内置默认模板。

### 2) SMSFORWARD_CHANNELS_JSON

用于定义 SMSForward 接收通道。

字段：

- `channel_id`: 通道标识
- `webhook_secret`: 通道独立鉴权密钥
- `enabled`: 是否启用
- `provider`: 该通道默认 provider（请求未传 provider 时使用）
- `template_ids`: 预留的模板策略绑定字段
- `source_whitelist`: 可选来源白名单（如设备名、应用名）

示例：

```json
[
  {
    "channel_id": "android-main",
    "webhook_secret": "replace-me",
    "enabled": true,
    "provider": "smsforward",
    "template_ids": ["en_alnum"],
    "source_whitelist": ["pixel-8"]
  }
]
```

## 新增接口

### SMSForward 回调

`POST /api/v1/webhook/smsforward/{channel_id}/{token}`

请求体：

```json
{
  "provider": "smsforward",
  "sender": "+8613800000000",
  "recipient": "+8613900000000",
  "body": "Your verification code is A1B2C3",
  "source": "pixel-8"
}
```

校验规则：

- `channel_id` 必须存在且 `enabled=true`
- `token` 必须与该通道 `webhook_secret` 匹配
- 若配置了 `source_whitelist`，则 `source` 必须命中白名单

## 提取策略（实现）

1. **强匹配**：模板 + 关键词邻近窗口（防止误提订单号）
2. **弱匹配**：模板匹配失败后在全短信文本尝试
3. **回退**：最后使用原有数字正则 `\b\d{4,8}\b`

提取结果会落库并记录：

- `channel_id`
- `template_id`
- `extraction_status`（如 `template_strong_extracted` / `template_conflict` / `fallback_extracted` / `not_found`）
- `extraction_confidence`（`strong` / `weak` / `fallback` / `none`）

## 兼容性说明

- 原有 `POST /api/v1/webhook/sms/{token}` 保持可用。
- 原有 `POST /api/v1/otp` 查询行为不变（阅后即焚）。
