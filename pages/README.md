# SMSServer Pages —— 静态文档站

面向使用者的在线说明与工具页：**纯静态、零依赖、无构建步骤**，任何静态托管（GitHub Pages、Cloudflare Pages、Nginx）直接指向本目录即可部署。

## 页面清单

| 页面 | 用途 |
|---|---|
| [`index.html`](index.html) | 产品介绍、一键部署入口、**取码助手**（浏览器本地计算 HMAC token 并生成 curl 命令） |
| [`token.html`](token.html) | **token 计算页**：输入收件人号码 + HMAC_SECRET 本地算 token（含日志前缀对账、取码命令），并解释「哪些值要计算、哪些是静态设置」 |
| [`docs.html`](docs.html) | 接口文档：token 算法、两个核心接口的请求/响应、部署形态配置对照、FAQ |
| [`smsforward.html`](smsforward.html) | **SmsForwarder 对接教程**：安卓备用机转发配置、占位符对照表、多通道鉴权、常见问题 |
| `assets/css/style.css` | 设计令牌与全部样式（主题变量集中在 `:root`） |
| `assets/js/tool.js` | 取码助手与配置生成器脚本（Web Crypto，无任何第三方依赖） |

## 本地预览

```bash
# 任意静态服务器指向本目录即可，例如：
python3 -m http.server 8080 --directory pages
# 浏览器打开 http://localhost:8080
```

> 取码助手使用 Web Crypto，需要 **HTTPS 或 localhost** 环境；直接双击以 `file://` 打开时 HMAC 计算会不可用（页面会给出提示）。

## 部署

### GitHub Pages

仓库设置 → Pages → Source 选 GitHub Actions，或直接选择 `main` 分支 `/pages` 目录。

### Cloudflare Pages

构建命令留空，输出目录填 `pages`。

## 隐私说明

- 取码助手输入的 `HMAC_SECRET`、手机号**只参与当前页面内存中的计算**，`assets/js/tool.js` 不发起任何网络请求（可审计：全文件无 `fetch`/`XMLHttpRequest`）。
- 页面本身不加载任何第三方资源（无 CDN 字体、无统计脚本），可离线工作。

## 维护约定

- 文档内容与接口行为保持同步：改了根 README 的接口/配置说明时，检查 `docs.html` 与 `smsforward.html` 是否需要跟着改。
- `smsforward.html` 的占位符对照表以 [SmsForwarder 源码](https://github.com/pppscn/SmsForwarder/blob/master/app/src/main/kotlin/cn/ppps/forwarder/utils/Constants.kt) 的 `TAG_LIST` 为准，升级 App 大版本后建议复核一次。
- 「通道模式」依赖上游 PR #1（smsforward 通道），未合入前页面已有醒目提示；合入后移除提示并复核 `SMSFORWARD_CHANNELS_JSON` 示例。
