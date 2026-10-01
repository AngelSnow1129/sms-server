# 知识库（Wiki）架构设计

> 状态：v4（2026-10-01）。v1 曾结论「仓库内 `docs/` 优先、不用 GitHub Wiki」；v2 修订为「GitHub Wiki 为权威源」；**v3 定稿为镜像模式**：`docs/` 仍是唯一事实源，GitHub Wiki 由 `scripts/sync-wiki.sh` 自动同步生成、**请勿在 Wiki 直接编辑**。两者均用仓库自带功能，不引入第三方托管、wiki 引擎与自定义域名。
> 相关文档：`PAGES-PLAN.md`（文档站）、`HANDOVER.md`（设计取舍）、`pages/README.md`。

## 1. 总体格局：两个 GitHub 原生载体

| 受众 | 载体 | 地址 |
|---|---|---|
| 使用者（来源方 / 取码方） | GitHub Pages（仓库 `pages/` 目录） | `https://angelsnow1129.github.io/WebHookServer/` |
| 维护者 / 贡献者 | GitHub Wiki | `https://github.com/AngelSnow1129/WebHookServer/wiki` |
| AI 协作 | 仓库内 `CLAUDE.md`（必须在 checkout 内才可被读取） | 仓库根 |

单一事实源原则不变：每个事实只在一处权威定义，其余位置链接引用。Wiki 与 Pages 的分工是**受众分工**（维护者 vs 使用者），不是内容备份关系。

## 2. 为什么是镜像：手工维护 Wiki 的两个硬伤

最初设想「把 `docs/` 迁进 Wiki，Wiki 成为权威源」，实现前发现两个无法绕开的硬伤：

1. **PR 流程管不到**：改代码的 PR 看不到 Wiki diff，reviewer 无从审查文档改动，同步只能靠人工自觉。
2. **无 CI 可校验**：Wiki 不在 checkout 内，门禁脚本（`scripts/verify-pages.sh` 同族的检查）跑不到它。

因此改为**镜像模式**：`docs/*.md` 仍是权威源，仓库 PR 审查与 CI 门禁全部作用其上；Wiki 由 `scripts/sync-wiki.sh` 单向生成，永远等于 `main` 上的 `docs/`。收益：Wiki 自带的渲染与全文搜索、对外可分享的页面 URL 全部保留，同时消掉上述两个硬伤。

镜像页的保护措施：每页顶部注入横幅——「本页由仓库自动同步生成，请勿在 Wiki 直接编辑」，并给出对应的 `docs/<源文件>` 链接。

## 2.1 同步机制

| 组成 | 说明 |
|---|---|
| `scripts/sync-wiki.sh` | 生成 + 推送；支持 `--dry-run`；支持 `WIKI_REMOTE` 覆盖远端（自测用） |
| `.github/workflows/wiki.yml` | `docs/**`、`scripts/sync-wiki.sh`、`.github/workflows/wiki.yml` 有 push 到 `main` 时自动触发；`permissions: contents: write` |
| 页面映射 | 脚本内 `MANIFEST` 是唯一权威表（源文件 → 页面名 → 分组 → 一句话） |
| 链接改写 | `docs/` 内互链改写成 Wiki 页面名；`../` 开头改写成 GitHub 绝对链接（`../pages/...` 同样落到 blob 链接，**不做**文档站跳转） |
| 陈旧页清理 | 只删除「带镜像横幅但已不在 `MANIFEST` 中」的旧镜像页；用户在 Wiki 上自建的无横幅页面一律保留，`_Footer.md` 跳过 |
| 权限 | 实测 `GITHUB_TOKEN` + `contents: write` 即可推送 `.wiki.git`，**无需 PAT 或 deploy key** |

**安全与鲁棒性（实测踩坑后加固）**：令牌经 `git credential.helper` 内联片段从环境变量读取，**不进入远端 URL 与 `argv`**（避免 `ps` 与 git 报错回显泄露凭据）；所有 git 调用的输出都被捕获并分类，不再用 `2>/dev/null` 吞掉（否则会把「推送失败」伪装成「已是最新」）；预检区分「Wiki 未初始化」（已用主仓库可达性二次确认）与「认证 / 网络故障」（后者直接 `FAIL` 而非 `SKIP`）；`push` 遇 non-fast-forward 会 `rebase` 重试一次。

**一次性前置条件**：Wiki 的 git 后端在「创建第一个页面」之前不存在。未初始化时同步脚本会打印指引并以 `0` 退出（输出 `SKIP`），不让流水线长时间飘红。初始化后每次 `docs/` 变更即自动覆盖同步。

## 3. 页面架构

`Home`（索引页）+ `_Sidebar.md`（分组导航）+ 四类页面（类型沿用 Diátaxis 适配：参考 / 规格 / 解释与方案 / 记录）。首版页面映射：

| Wiki 页面 | 来源 | 类型 | 状态 |
|---|---|---|---|
| `Home` | 新建 | 索引 | — |
| `LOGGING` | `docs/LOGGING.md` | 参考 | stable |
| `INTEGRATION` | `docs/INTEGRATION.md` | 参考 | stable |
| `SMSFORWARD-TEMPLATES` | `docs/WIKI_SMSFORWARD_TEMPLATES.md` | 规格 | 待合入 |
| `HANDOVER` | `docs/HANDOVER.md` | 解释 | stable |
| `PAGES-PLAN` | `docs/PAGES-PLAN.md` | 方案 | v4 |
| `WIKI-ARCHITECTURE` | 本篇 | 方案 | v4 |
| `BRANCH-VERIFICATION` | `docs/BRANCH-VERIFICATION.md` | 记录 | 已冻结 |
| `INCIDENT-TEAM-TOOL` | `docs/TEAM-TOOL-INCIDENT.md` | 记录 | 已冻结 |

约定：

- **页面名**用大写连字符英文（URL 稳定、可预测），页面首行 H1 用中文标题；`WIKI_` 前缀在 wiki 内冗余，去掉。
- wiki 内互链用 `[[页面名]]`（GitHub wikilink），必要处 `[[页面名|别名]]`。
- `_Sidebar.md` 按「参考 / 规格 / 方案 / 记录」四组排序，每行一页；**新增页面必须在 `Home` 与 `_Sidebar` 同时登记**。
- 记录类页面（INCIDENT / VERIFICATION）冻结不改写，勘误另起段落。

## 4. 仓库内保留物（明确不镜像 / 不迁移）

- `CLAUDE.md`：AI 工具只在仓库 checkout 内读取指令文件，硬约束必须留在 repo；它引用 `docs/` 路径而非 Wiki 链接。
- `docs/*.md`：**全部保留**，是唯一事实源（镜像模式下不存在「迁走」）。
- `pages/`：文档站，见 `PAGES-PLAN.md`。

## 5. 链接规则（跨仓库的关键细节）

| 方向 | 写法 |
|---|---|
| wiki 内部 | `[[页面名]]` 或相对 Markdown 链接 |
| 仓库 → wiki | 只能绝对 URL：`https://github.com/AngelSnow1129/WebHookServer/wiki/<页面名>`（相对链接不会指向 wiki） |
| wiki → 仓库 | 绝对 URL 指向文件：规范 / 方案类锁 `main` 分支（`blob/main/...`），记录类锁提交 hash，保证证据不被后续改动污染 |
| wiki / 仓库 → Pages | 绝对 URL：`https://angelsnow1129.github.io/WebHookServer/...` |
| `pages/` → wiki | 不链（受众不同）；确需引用时用绝对 URL |

## 6. 同步触发器（代码改动 → 必检 wiki 页）

| 改动 | 必检 |
|---|---|
| handler / 鉴权 / webhook 行为 | `LOGGING`（401/400 点位）、`docs/INTEGRATION.md`（留仓库） |
| config 新增环境变量 | `HANDOVER`（取舍）、`CLAUDE.md` 若构成新硬约束 |
| store / repository 存储语义 | `HANDOVER` + 对应契约测试说明 |
| 提取规则 / 模板（含 PR #1 合入） | `SMSFORWARD-TEMPLATES` 转正 + `pages/` 警示移除 |
| 部署形态 / CI / release | `PAGES-PLAN`、`.github/workflows/*` 注释 |
| 事故 / 重要验证 | 新增 `INCIDENT-*` / `*-VERIFICATION` 页 + `Home`、`_Sidebar` 登记 |

## 7. 生命周期与质量门禁

状态机：`draft → stable → (deprecated → 删除)`，头部保留 `> 状态：vN（日期）` 元数据块。

- 新增文档三要件：头部状态块、在 `scripts/sync-wiki.sh` 的 `MANIFEST` 登记（决定页面名与 Sidebar 分组）、至少一条交叉链接（防孤儿页）。
- 页面增删只改 `MANIFEST`：`_Sidebar.md` 与 `Home.md` 由脚本生成，不手工维护。
- `deprecated` 页顶部加横幅指向替代文档，保留一个阶段后从 `MANIFEST` 移除（Wiki git 历史即归档）。
- 质量门禁：文档随代码走 PR，由 CI 的 `pages` job（`scripts/verify-pages.sh`）与人工 review 共同把关；Wiki 侧无需单独校验——它是 `docs/` 的确定性镜像。

## 8. 启用步骤（一次性）

1. **初始化 Wiki**：打开 `https://github.com/AngelSnow1129/WebHookServer/wiki`，点「创建第一个页面」随便写一行保存。这一步只为让 GitHub 建出 `.wiki.git` git 后端（此前该仓库不存在，脚本会 SKIP 并打印指引）。
2. **触发首次同步**：合并包含 `scripts/sync-wiki.sh` 与 `docs/` 的 PR 到 `main`（`wiki.yml` 自动跑），或在 Actions 里手动 `workflow_dispatch` 该 workflow；本地执行 `./scripts/sync-wiki.sh` 等效。
3. **确认结果**：Wiki 上应出现 9 个页面 + `_Sidebar`，每页顶部是「请勿在 Wiki 编辑」横幅。

> 第 1 步必须在 GitHub 网页端完成，脚本无法代劳（远端 wiki 仓库在初始化前不存在）。第 2 步会向 `.wiki.git` 推送，属外部写操作。

## 9. 演进路线

1. **PR #1 合入时**：`SMSFORWARD-TEMPLATES` 从 `MANIFEST` 的「规格」移入「参考」分组，同步 `pages/` 与 `docs/INTEGRATION.md` 的警示移除。
2. **新增维护文档时**：在 `docs/` 写文件 + 在 `MANIFEST` 加一行，其余交给同步脚本。
3. **wiki 页面 > 20 时**：`_Sidebar` 仍保持单层分组，不引入层级页面名。
4. **不做**：第三方 wiki 引擎、自建搜索（GitHub wiki 自带）、在 Wiki 直接编辑（会破坏镜像一致性）、把使用者文档搬进 wiki（使用者内容归 Pages）。
