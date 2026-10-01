# 文档站（Pages）建设方案

> 状态：v6（2026-10-01）。按项目决策修订：**文档站 = GitHub Pages，仓库 `pages/` 目录发布，固定使用 GitHub Pages 默认域名**，不绑定自定义域名、不使用任何第三方托管（Cloudflare Pages / Nginx 等均不做）。v3 起质量门禁已落地（`scripts/verify-pages.sh` + CI `pages` job），v4 起门禁 fail-closed 并同时作为发布前置；v5 起样式全令牌化并支持**暗色主题**（`prefers-color-scheme` 自动切换，无 JS、无手动开关）；v6 起主仓库迁移为 `AngelSnow1129/sms-server`，站点域（`angelsnow1129.github.io/sms-server`）、部署按钮、GHCR 镜像名随之切换。知识库侧见 `WIKI-ARCHITECTURE.md`（GitHub Wiki）。
> 相关文档：`pages/README.md`（页面清单与维护约定）、`docs/INTEGRATION.md`（对接流程）。

## 1. 定位与现状

`pages/` 是面向**使用者**（来源方接入员、取码方调用员）的在线说明与工具站，与面向**维护者**的 GitHub Wiki 严格按受众分工：

| 资产 | 内容 | 现状 |
|---|---|---|
| `index.html` | 产品介绍、一键部署入口、取码助手（Web Crypto 本地算 HMAC） | 完整元数据 + JSON-LD |
| `token.html` | token 计算页、日志前缀对账排查法 | 完整元数据 |
| `docs.html` | 接口文档（token 算法、两个核心接口、部署形态对照、FAQ） | 完整元数据 |
| `smsforward.html` | SmsForwarder 对接教程、配置生成器 | 完整元数据 |
| `assets/css/style.css` | 设计令牌与全部样式（`:root` 集中变量） | 稳定 |
| `assets/js/tool.js` | 取码助手 / 配置生成器脚本，零第三方依赖 | 稳定 |
| `robots.txt` / `sitemap.xml` | 爬取规则与站点地图 | 已就位 |

既有原则（继续坚持）：**纯静态、零构建、零第三方资源**——无打包器、无 CDN 字体/统计脚本、`tool.js` 不发任何网络请求（可审计）。所有文档与页面文案为中文。

## 2. 信息架构

单层扁平结构，页面间靠顶部导航与页脚互链，不引入目录层级：

```
index.html ──┬── docs.html        （接口文档，锚点：#token-algorithm #webhook-sms #otp #smsforward #deploy #limits #faq）
             ├── token.html       （token 计算，锚点：#tool #static-or-compute）
             └── smsforward.html  （对接教程，锚点：#principle #config-generator #app-steps …）
```

约定：

- 新页面一律平铺在 `pages/` 根，命名用小写连字符（如 `faq.html`），并在 `sitemap.xml` 登记、`pages/README.md` 页面清单补一行。
- 页面内二级标题必须带 `id` 锚点，供跨页深链（现状已满足）。
- 通用工具脚本继续收敛在 `assets/js/tool.js`；单页私有逻辑内联在该页，避免过度抽象。

## 3. 站点地址（固定值，不是配置项）

站点 URL 恒为 GitHub Pages 默认域名，写死于以下位置，新增页面时必须保持一致：

| 位置 | 文件 |
|---|---|
| 各页 `canonical` / `og:url` | 四个 HTML 头部（`index.html` 头部有域名总注释） |
| `robots.txt` 的 `Sitemap:` 行 | `pages/robots.txt` |
| `sitemap.xml` 的 `<loc>` | `pages/sitemap.xml` |
| 首页 `ld+json` 的 `url` | `pages/index.html` |

**不做**自定义域名：不加 `CNAME` 文件、不配 DNS；域名唯一，无「换域名」流程。若仓库未来改名或转移账号，一次性全局搜索替换 `angelsnow1129.github.io/sms-server` 即可（六处，均在 `pages/` 内），属于仓库级事件而非本站日常维护项。

## 4. 发布方式：GitHub Actions 部署 Pages

唯一发布通道是 `.github/workflows/pages.yml`（本方案配套新增）：push 到 `main` 且 `pages/` 有变更时，把 `pages/` 目录整体作为 artifact 交给官方 Pages 部署动作发布。仓库设置中 Pages 的 Source 需选 **GitHub Actions**（一次性手工配置）。

- 无构建步骤：工作流只做「上传目录 + 部署」，不装 Node、不跑打包器。
- 不用「选分支目录」的 Pages 配置：以 workflow 为准可绑定 `pages/` 路径过滤，文档站发布与代码 CI 互不干扰。
- 文档站校验（§6）跑在 CI 的 `pages` job；但 CI job 只能拦 PR，拦不住 `pages.yml` 自行部署，故发布 workflow 在 `configure-pages` 之前也先跑一次 `./scripts/verify-pages.sh`，不过则不发布。门禁同时是 PR 门与发布前置，坏页面永远上不了线。`pages.yml` 的 `paths` 已加入 `scripts/verify-pages.sh`，脚本改动同样触发发布校验。
- 其他任何托管方式（Cloudflare Pages、自建 Nginx 等）均不采用；`pages/` 目录本身仍可离线本地预览（`python3 -m http.server --directory pages`），这与发布通道无关。

## 5. 元数据规范（已落地，新增页面照抄）

每页 `<head>` 必备，缺一不可（顺序按现文件即可）：

1. `charset` / `viewport`
2. `title`：`页面名 · SMSServer`（首页例外：`SMSServer · 短信验证码中继服务`）
3. `description`：一句话说清本页解决什么问题
4. `canonical`：绝对 URL，按 §3 固定域名
5. OG 三件套：`og:type=website`、`og:site_name=SMSServer`、`og:title` / `og:description` / `og:url` / `og:locale=zh_CN`
6. Twitter Card：`twitter:card=summary` + `twitter:title` / `twitter:description`（无图卡片，刻意不配 `twitter:image`，保持零二进制资源）
7. `theme-color` 双档（light `#F8FAFC` / dark `#0B1120`，跟随系统）、内联 SVG favicon（data URI，无 favicon.ico 文件）
8. 首页独有：`application/ld+json` 的 `SoftwareApplication` 结构化数据

## 6. 质量门禁（已落地）

门禁脚本 `scripts/verify-pages.sh` 已实现并接入 CI（`.github/workflows/ci.yml` 的 `pages` job），**任何 PR 都会执行**；`pages.yml` 发布前也会再跑一次（见 §4），本地提交前可先跑一次。检查项：

- **fail-closed**：`python3` 缺失或崩溃、页面目录为空、必需文件（`sitemap.xml` / `robots.txt`）缺失、页面非 UTF-8 均计 `FAIL`，**绝不假绿**。
- **元数据完整性**：每个 `*.html`（**递归发现**，子目录页面如规划中的 `en/` 一并纳入）必含 `canonical`、`og:title`、`og:url`、`twitter:card`、`description`、`viewport`、`theme-color`、favicon。
- **站点地址一致性**：页 `canonical` 与 `og:url` 逐页一致；`sitemap.xml` 的 `<loc>` 与各页 canonical 一一对应，`robots.txt` 的 `Sitemap:` 指向本站——单一事实源是页面 canonical，其余两处必须与之一致。
- **内链有效性**：抽取页内 `href="*.html#..."`，相对链接按所在目录解析，校验目标文件与锚点 id 存在。
- **隐私红线**：覆盖 `.html` / `.js` / `.css` 三类文件，全目录无 `fetch(` / `XMLHttpRequest`；并区分「用户可点击的 `<a href>` 外链」（允许）与「页面自动加载的子资源标签」（`script` / `link` / `img` / `iframe` / `source` / `video` / `audio` / `track` / `embed` / `object` / `use` 及 CSS `url()` / `@import`）指向站外（违规），同站域名放行。守住「零网络请求、零第三方资源」承诺。

实现说明：解析交给 python3 标准库（不引入 Node 工具链，符合 §4 的无构建原则），脚本以 `PASS/FAIL` 逐项输出并以失败数作为退出码。

## 7. 内容同步流程（既有约定，重申并细化）

改动触发器 → 必检页面：

| 仓库改动 | 必检 |
|---|---|
| 根 README 的接口/配置说明 | `docs.html`、`smsforward.html` |
| webhook / 取码接口行为（handler、service） | `docs.html`（含错误码表）、`docs/INTEGRATION.md` |
| 新增环境变量 | `docs.html#deploy` 部署形态表、`.env.example` |
| SmsForwarder 大版本升级 | `smsforward.html` 占位符对照表（对上游 `Constants.kt` 的 `TAG_LIST`） |
| 上游 PR #1（通道模式）合入 | `smsforward.html` / `docs.html#smsforward` 的「未合入」警示移除 + `SMSFORWARD_CHANNELS_JSON` 示例复核 |

## 8. 演进路线

按需、克制，红线是不引入构建步骤与第三方资源：

1. **搜索**：页面总量 ≤ 10 时不做；超过后优先「一个 `search.html` + 预生成 JSON 索引脚本」，不引入客户端搜索库。
2. **英文版**：如需 i18n，用 `en/` 子目录镜像结构 + `<link rel="alternate" hreflang>`，不做框架化。
3. **OG 图**：若分享卡片有诉求，补一张 1200×630 静态 PNG 并放开 `og:image` / `twitter:image:src`（当前刻意为无图 summary）。
4. **版本化文档**：接口出现不兼容变更时再加 `v1/` 路径快照，当前接口稳定不预做。
5. **不做**：自定义域名、CDN 加速层、评论区等第三方嵌入（违反零第三方资源原则）。
