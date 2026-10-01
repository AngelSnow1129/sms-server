# SMSServer Pages —— 静态文档站

面向使用者的在线说明与工具页：**纯静态、零依赖、无构建步骤**，通过 GitHub Pages 发布（见「部署」），站点地址固定为 `https://angelsnow1129.github.io/sms-server/`。

## 页面清单

| 页面 | 用途 |
|---|---|
| [`index.html`](index.html) | 产品介绍、一键部署入口、**取码助手**（浏览器本地计算 HMAC token 并生成 curl 命令） |
| [`token.html`](token.html) | **token 计算页**：输入收件人号码 + HMAC_SECRET 本地算 token（含日志前缀对账、取码命令），并解释「哪些值要计算、哪些是静态设置」 |
| [`docs.html`](docs.html) | 接口文档：token 算法、两个核心接口的请求/响应、部署形态配置对照、FAQ |
| [`smsforward.html`](smsforward.html) | **SmsForwarder 对接教程**：安卓备用机转发配置、占位符对照表、多通道鉴权、常见问题 |
| [`robots.txt`](robots.txt) | 爬取规则：全站公开、允许全部爬虫，并指向本站 `sitemap.xml` |
| [`sitemap.xml`](sitemap.xml) | 站点地图：与四个页面的 canonical 一一对应 |
| `assets/css/style.css` | 设计令牌与全部样式（`:root` 集中变量；浅色/暗色双主题靠令牌切换，选择器区无颜色字面量） |
| `assets/js/tool.js` | 取码助手与配置生成器脚本（Web Crypto，无任何第三方依赖） |

> `robots.txt` 与 `sitemap.xml` 的站点地址**单一事实源是各页 `canonical`**，两者须与之一致，已由 `scripts/verify-pages.sh` 门禁校验。

## 本地预览

```bash
# 任意静态服务器指向本目录即可，例如：
python3 -m http.server 8080 --directory pages
# 浏览器打开 http://localhost:8080
```

> 取码助手使用 Web Crypto，需要 **HTTPS 或 localhost** 环境；直接双击以 `file://` 打开时 HMAC 计算会不可用（页面会给出提示）。

## 部署

发布通道固定为 **GitHub Pages**：push 到 `main` 且 `pages/`、门禁脚本或发布 workflow 有变更时，由 `.github/workflows/pages.yml` 自动发布。首次启用需在仓库设置 → Pages → Source 选 **GitHub Actions**（一次性手工配置）。发布前会先跑 `scripts/verify-pages.sh` 门禁，不过则不发布。

不绑定自定义域名、不使用其他托管服务；若仓库改名或转移账号，需同步修改站点地址（见 `docs/PAGES-PLAN.md` §3 的六处位置清单）。

## 隐私说明

- 取码助手输入的 `HMAC_SECRET`、手机号**只参与当前页面内存中的计算**，`assets/js/tool.js` 不发起任何网络请求（可审计：全文件无 `fetch`/`XMLHttpRequest`）。
- 页面本身不加载任何第三方资源（无 CDN 字体、无统计脚本），可离线工作。

## 维护约定

- 文档内容与接口行为保持同步：改了根 README 的接口/配置说明时，检查 `docs.html` 与 `smsforward.html` 是否需要跟着改。
- `smsforward.html` 的占位符对照表以 [SmsForwarder 源码](https://github.com/pppscn/SmsForwarder/blob/master/app/src/main/kotlin/cn/ppps/forwarder/utils/Constants.kt) 的 `TAG_LIST` 为准，升级 App 大版本后建议复核一次。
- 「通道模式」依赖上游 PR #1（smsforward 通道），未合入前页面已有醒目提示；合入后移除提示并复核 `SMSFORWARD_CHANNELS_JSON` 示例。
