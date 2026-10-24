# internal/compression — 三档上下文压缩

> 范围：本目录内不可派生的约束。全局摘要见仓库根 `AGENTS.md` §6.5。
> （原 `docs/compression-trigger-design.md` 已不存在，规格由本文件持有。）

## 边界

把"是否超阈值"和"超了怎么削"的三档算法收在一处。**不负责**决定何时调用自己（两个循环各两处调用点，
共四处）、不持有会话、不写 rollout。

## 硬约束

**1. 判定只有一处实现：`OverThreshold`，严格大于（等于不触发）**（`truncate.go:99-107`）。
不要再抄 `EstimateMessagesTokens(messages) > threshold`——抄开的表现是"子 Agent 与 Director 的
触发时机不一致"。四处调用点漏改一处的情况由 `internal/agents/executor_prefix_trigger_test.go`
跨包盯着（本包单测在那种情况下是全绿的）。

**2. `ResolveThreshold` 两个来源都是上限、取小**（`threshold.go:49-59`）：目录回答"这个模型装得下多少"，
配置回答"我愿意为此付多少"。**provider 的 `context_window` 完全不参与**（它只影响 TUI 进度条）。
`YAGENT_COMPRESS_THRESHOLD` 是第 0 级、只给调试用，且**每次现读不缓存**（`:21-22,60-62`）——
生产配置不要用它，它绕过一切、会让压缩早于模型真实容量触发。

**3. ⚠️ 同一个函数有两种调用语义。** 压缩触发传配置值；**重放预算传 `0`**（要纯模型容量，不参与取小），
见 `:56-59`。把取小套到重放预算上，能让本来可重放的委派**全部**降级成交接报告（不报错，只是恢复不了）。

**4. 计量口径 `Prefix` 只在可用时使用**（`truncate.go:73-99`）：`Refresh` 三条规则（失败保留 /
无 usage 作废 / 成功以 sentLen 为界）两个循环共用（`:108-121`）。作废后逐字回落到全量本地估算，
那才是改动前的行为。**偏差一律偏大，所以这个数只用于判定，`stats` 一律保持本地口径**（`:88-99`、
`truncate_results.go:40-46`）——否则 TUI ⧉ 面板的百分比是两个基数混在一起。

**5. token 估算包含 `Reasoning` 与工具调用参数**（`truncate.go:47-58`）。漏算不是"少几个 token"，
是压缩触发时机系统性偏晚。

**6. 第一档必须增量记账**（`truncate_results.go:42-46`）：达标判定每截一条只重算那一条。
旧写法对整份消息跑 tiktoken，200 条工具结果的会话要 9.3s。

**7. 依赖方向**：本包**不得 import `internal/agents`**（`compressor.go:8-10`），agents 侧的清理
经回调注入；对 thinklink 只走窄接口（`:20-38`）。

**8. 临时压低阈值做验收只有两条路，语义不同。** 首选写小的
`[enhanced_commander] context_compression_threshold`——与目录取 min、目录语义保留
（`threshold.go:70-79`）；`YAGENT_COMPRESS_THRESHOLD` 是压过目录**与配置键**的旁路
（`:52-55`），非正数或解析失败不生效、逐字回落到前两条路（`:32-39`，闸门
`TestResolveThreshold_EnvOverrideInvalid`，`threshold_test.go:98`）。另一个消费者的口径
相反且独立：TUI 进度条 `contextWindowFromCfg`（`internal/tui/tui_model.go:510-522`）是
provider `context_window` 优先、未设置才查目录——给目录已收录的模型手写 `context_window`
只改进度条，不改压缩时机。

**9. 压缩只有两个开关，不许加第三道布尔闸门。** 第一、二档只看
`enable_context_compression`（`internal/agents/executor.go:486`、
`internal/agents/director/planner_compress.go:23`），第三档在此之上再要
`enable_ultimate_compression` **与**非空 journal（`executor.go:578`、
`planner_compress.go:99`；子 Agent 侧两条件见三档速记第 3 条，
`TestRunAgentLoop_UltimateCompressionNeedsFlagAndStore`，
`internal/agents/executor_compression_test.go:155` 钉"缺一不可"）。旧的
`[enhanced_commander] enable` 已从 schema 删除、残留键由 config 的 `removedKeyHints`
点名（`internal/config/config.go:261`）；第三档"缺省为开、显式 false 真关"由 config 的
键存在性判定（`internal/config/config.go:486,703-705`），消费方只读布尔字段，
**不许**在调用点复刻存在性判定。

## 三档速记

1. **Rule** `TruncateToolResultsToBudget`：按优先级先截低价值工具结果（`deepthinking`/`design`
   受保护），保留最近 `tool_result_keep_tokens`（默认 200）。
2. **Emergency**：把 T&P 块抽进 thinklink，最近 3 条保留原文，其余 LLM 摘要，记忆整体重写为
   `[system, one user]`。
3. **Ultimate**：用 thinklink 日志重建上下文，保留最近 N 个 T&P（`ultimate_compression_keep_plans`，
   0 = 全部），最后硬截断到预算。子 Agent 侧需要 `EnableUltimateCompression` **与** 非空
   `UltimateThinkLink` 同时成立。

三档都会发 `context_*_compressed` 事件并写 `compacted` rollout 记录（`compacted` 会替换被压过的前缀，
这正是第 2 条与"指纹由读侧算"要处理的东西）。

## 入口

`truncate.go`（`Prefix` + 判定）→ `threshold.go` → `truncate_results.go`（第一档）→
`compressor.go` / `emergency.go`（二、三档）。

## 闸门

`TestOverThresholdIsStrictlyGreater`（`compressor_test.go:413`，改成 `>=` 或另开一份判定即红）、
`TestResolveThreshold_ReplayBudgetUnaffected`（`threshold_test.go:64`，把取小套到重放预算 →
786432 掉成 120000）、`TestResolveThreshold_EnvOverrideReadEveryCall`（`:115`，缓存取值即红）、
`TestPrefixTotalUsesMeasurement`（`prefix_test.go:32`）、`TestTruncateGateUsesMeasurement`（`:154`）、
`TestTruncateIncrementalMatchesFullRecompute`（`context_compressor_test.go:452`，增量与全量在任一支
路径不同形即红）、`TestTruncateCostScalesLinearly`（`:544`，**这是刻意保留的一条计时用例**，
用来钉住"每截一条全量重算"不许回来）。

**夹具大小**：本包历史上 28s 全花在用 `strings.Repeat("x", 50000)` 造工具结果的截断测试上
（tiktoken 反复编几百 KB）。payload 缩到 1-2k 并等比缩预算后整包 0.17s，断言语义不变
（根 AGENTS.md §8.3）。
