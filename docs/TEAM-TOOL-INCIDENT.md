# RustCode `team` 工具缺陷事件报告

## 摘要

本仓库的「cloudflare 分支 + SQLite 适配 + README 按钮」工作要求使用 `team` 工具并行派发。
2026-09-29 会话中对该工具发起 **12 次调用，0 次成功**，全部失败于参数反序列化层。
经 5 类参数形态穷尽探测，定性为 **RustCode harness 的参数序列化缺陷**（与工具实现无关）；
全部并行工作已改用 `task` 工具等价完成，交付未受阻。本文档供平台侧定位与修复。

## 错误分类学（两种互斥的失败模式）

| 模式 | 报错原文 | 发生条件 | 含义 |
|---|---|---|---|
| A | `invalid team args: invalid type: string "[{\"description\": …}]", expected a sequence` | delegate 调用携带长参数 | **参数到达了工具端**，但 `tasks` 数组被 harness 序列化成了字符串，反序列化器要求 sequence 必然失败 |
| B | `invalid team args: missing field \`action\` at line 1 column 2` | 其余所有调用（含携带标量参数的探测） | **参数整体坍缩为空对象 `{}`**（报错列号 2 = `{}` 恰好两字符），字段从未到达工具端 |

模式 A 的报错是决定性证据：字符串化发生在 harness 出参一侧，任何我方参数格式都无法使
字符串重新成为数组。

## 探测矩阵（11 次，全部失败）

| # | 形态 | 参数要点 | 结果 |
|---|---|---|---|
| 1-2 | delegate·多任务/单任务·中文长文本 | 完整 description/role/scope/prompt | 模式 A |
| 3-5 | delegate·单任务 | 中文/英数混合 | 模式 A |
| 6 | 空调用 | 无参数 | 模式 B（预期：空输入本就缺 action） |
| 7-8 | 标量探测 | `action:"status"` 等完整对象 | 模式 B（**标量字段也从未到达**，证明坍缩先于工具端） |
| 9 | role 变体 | explore/explore 与 worker 两种 | 模式 A/B |
| 10 | delegate·纯 ASCII·极短 prompt | 单任务，最小负载 | 模式 B |
| 11 | 同轮重复调用 | 去重护栏拦截 | — |
| 12 | delegate·`tasks: []`（空数组） | 结构合法的最小数组 | 模式 B（**合法 JSON 数组同样被丢弃**，证明坍缩与内容无关、先于工具端分发） |
| 13 | 标量探测·数小时间隔重试 | `action:"status"` 等完整对象 | 模式 B（**持久性确认**：排除瞬态故障自愈，缺陷跨时段稳定复现） |

## 根因推断

同一会话中 `task` 工具以**同形参数**（tasks 数组、role、scope）多次成功执行
（3 worker + 1 reviewer + 2 补漏 + 1 独立核验，全部返回 completed），
排除参数内容、长度、编码、工具 schema 本身的问题。缺陷位于 harness 对 `team`
工具出参的序列化环节：大负载被字符串化（模式 A）、小负载被丢弃（模式 B）。

## 平台侧复现

任何会话中调用 `team`：

1. `{"action":"delegate","tasks":[{"description":"t","prompt":"p","role":"implementer","scope":["**"]}]}`
   → 预期复现模式 A（tasks 为字符串）
2. `{"action":"status","run_id":"x"}` → 预期复现模式 B（报 missing field action，即参数被丢弃）

## 影响与处置

- 影响：仅 `team` 工具不可用；异步长任务编排能力暂缺。
- 处置：`task`（同步批任务）已等价覆盖本轮全部并行需求；缺陷与替代方案已记录于
  `docs/BRANCH-VERIFICATION.md` 与本报告。
