# 文档索引

> 状态：v2（2026-10-01）。本页是 `docs/` 知识库的唯一导航入口，**也是权威源**：新增文档在此登记，`scripts/sync-wiki.sh` 会把它与同目录文档一起镜像到 GitHub Wiki。
> 镜像模式下 **GitHub Wiki 由脚本自动生成，请勿在 Wiki 直接编辑**；改动一律提 PR 到本目录（架构见 `WIKI-ARCHITECTURE.md`）。

## 维护者 / 贡献者文档

| 文档 | 类型 | 读者 | 一句话 | 状态 |
|---|---|---|---|---|
| [WIKI-ARCHITECTURE.md](WIKI-ARCHITECTURE.md) | 方案 | 维护者 | 知识库架构：为何用镜像、页面映射、同步机制、链接规则、启用步骤 | v5 |
| [PAGES-PLAN.md](PAGES-PLAN.md) | 方案 | 维护者 | 文档站（GitHub Pages）方案：定位、信息架构、站点地址、发布 workflow、质量门禁、双主题 | v6 |
| [HANDOVER.md](HANDOVER.md) | 解释 | 新接手维护者 | 双部署形态与两条存储链路的设计取舍 | stable |
| [LOGGING.md](LOGGING.md) | 参考 | 维护者、排障者 | 日志脱敏、模块前缀、字段/级别/点位完整规范 | stable |
| [WIKI_SMSFORWARD_TEMPLATES.md](WIKI_SMSFORWARD_TEMPLATES.md) | 规格 | 维护者 | 未合入 PR #1 的模板化提取与 SMSForward 多通道目标状态 | 待合入 |
| [BRANCH-VERIFICATION.md](BRANCH-VERIFICATION.md) | 记录 | 审阅者 | cloudflare 分支交付清单的复现证据 | 已冻结 |
| [TEAM-TOOL-INCIDENT.md](TEAM-TOOL-INCIDENT.md) | 记录 | 维护者、平台方 | RustCode `team` 工具参数缺陷事件复盘 | 已冻结 |

## 使用者文档（不在本目录）

| 入口 | 用途 |
|---|---|
| 根 [README.md](../README.md) | 部署速查与使用入口 |
| [`pages/`](../pages/README.md) | 使用者文档站：接口文档、token 计算、SmsForwarder 教程 |
| [INTEGRATION.md](INTEGRATION.md) | 来源方/取码方完整对接流程与 token 计算示例 |
| [CLAUDE.md](../CLAUDE.md) | AI 协作约定与「改动前必须知道的硬约束」浓缩入口 |
