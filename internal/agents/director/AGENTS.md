# internal/agents/director — Director 编排内核

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.2。
> **本包禁止 import `yagent/internal/agents`**（`scripts/check_no_agents_import.sh`）。

## 边界

Director 的编排内核：Planner 主循环、熔断与恢复、指标、便签索引注入、项目上下文加载、Meta 注册表。
不负责子 Agent 实例与委派执行（经 `ToolRunner` 回调交给门面层）、不面向用户。

## 硬约束

**1. 依赖守卫扫的是整条传递依赖闭包，且必须整行匹配**（`scripts/check_no_agents_import.sh:15-23`）——
子包路径含父串，子串匹配会误伤。跨子系统的依赖一律经 `internal/agents/ports.go` 的窄接口注入。

**2. 非测试 `.go` ≤ 300 行，存量按 PENDING 冻结。** PENDING 条目的格式是
`<相对路径>:<允许的最大行数>`（`scripts/check_file_sizes.sh:25,33`），今天只有一项：
`planner_run.go` 的上限 **572 行**（三段职责——主循环 + 压缩 + 委派编排——里只搬走了压缩那段，
657 → 572）。规则：新文件超限直接红；**改小了必须同步下调 PENDING 里的数字**，否则脚本主动报
"棘轮失步"并红。一条常红的守卫等于没有守卫。

**3. 便签索引是派生态且瞬态。** `messagesForLLMCall` 返回**新切片**，外层 `messages` 必须保持无索引
（`board_index.go:11-22`）：一旦漏回，下一步的压缩预算会把目录算进正文，紧急压缩还会把它当成一条
用户输入折叠进摘要——那正是它要解决的病。同一步重试必须幂等（`:37`）。

**4. 恢复决策不许交给模型。** `PendingResumption` 在进 LLM 循环**之前**处理，结果作为**同一个
`call_id` 的第二条 tool output**（last-wins）交回（`planner_run.go:100-110`），零 token、确定性。
让模型决定怎么恢复的后果是"同样的停点有时重做、有时接着跑"。`DelegationID` 为空时必须整份忽略，
不许塞一条没配对的 tool 消息（`:105`，`planner_resumption_test.go:127`）。

**5. 暂停闸门在 `p.state.Step = i + 1` 之前**（`planner_run.go:188-196`）：这一步根本没跑过，
推进游标会让恢复侧多算一步。

**6. 检查点只在终止路径写**（`planner_rollout.go:10-12,49-59`），与子 Agent 共用
`memory.WriteCheckpoint`——父子两个写入包不能互相 import，抄一份必然分叉。

**7. 两个"看起来更方便"的签名决定是刻意的。** `EventPublisher` 刻意**不含** `ForDelegation`
（`planner.go:14-16`，Director 每任务只有一个，不会与自己混流）；`ToolRunner.Call` 的 `callID`
必须是显式参数而不是塞进 ctx（`:30-34`）——接口漂移要在编译期炸，ctx 里悄悄传的后果是恢复侧拿到空
`delegation_id`、把能救的会话判成不可重放。

**8. 熔断开不了就永远不建。** 阈值 0 = 不创建 breaker（`recovery.go:46-49`），"Director 绝不能因为
网络抖动停摆"。熔断器的"查询即试探"语义见 `internal/AGENTS.md` 的 recovery 一节。

**9. `StopKindOf` 的词表顺序是硬约束**（`stop_kind.go:42-49`）：`DeadlineExceeded` 与 `abandoned`
都必须排在 `cancelled` **之前**，因为 `AbandonedError` 能 `errors.Is` 到 `context.Canceled`。
本包与 `agents.classifyStop` 是两份同形实现（分层守卫逼出来的），靠
`TestStopKindClassifiersAgree` 对齐；加词表值时的正确顺序是**先给聚合测试喂新输入**，再让两边实现追它
（根 AGENTS.md §11 坑 18）。

**10. 强制委派提醒的次数上限必须导出**（`MaxNonDelegationPrompts`，`planner_run.go:19-21`）：
曾经 `agents` 侧有一份只有测试在读的同名常量。

**11. 熔断的"查询"就是那次试探，所以本包的查询点必须与回报成对。**
`IsCircuitBreakerOpen()` 的实现是 `return !r.breaker.Allow()`（`recovery.go:74-78`），而 `Allow()`
带着 open→half-open 迁移、half-open 只放**一个**探针。生产安全的前提是 planner 每一步都在同一次
迭代里 查询（`planner_run.go:198`）→ 调用 → 回报（`:318` `RecordLLMSuccess` / `:323` `RecordLLMFailure`）；
多加一处"只查不报"的调用点会**消费掉**那次试探。探针真的没人回报时，最坏是多等一个
`reset_timeout`（卡住的探针会过期）而不是永久锁死。**现状要说清楚**：这条配对没有自动化闸门——
本包用例只固化状态机语义（`TestCharacterizationRecoveryCircuitBreaker`、
`TestRecoveryHandler_BreakerDisabledAtThresholdZero`），加第二个查询点不会有任何一条测试变红，
所以改之前先数有几个调用点在读它（`internal/AGENTS.md` 的 recovery 一节）。

**12. 未委派提醒是"配额 3 + 两条文案"，耗尽后以 `plain_text` 收尾而不是报错。**
计数在每次 `Run` 开头归零（`planner_run.go:25`）——跨任务保留会让第二次任务一开口就连吃三次提醒；
第 1 次与第 2/3 次是两条不同文案（`:440-446`），注入的是**本步的 messages** 与 rollout
（`:447-449`），绝不写进 `ConversationMemory`（与硬约束 3 同一个理由）；配额用尽走
`writeComplete(StopKindPlainText)` 并正常返回文本（`:452-453`），把它写成 error 的表现是上层把一次
已经写完回答的收尾当成失败重做（`internal/agents/AGENTS.md` 硬约束 13 同族）。

## 入口

`planner.go`（全部窄接口契约）、`planner_run.go`（主循环，行数债所在）、`board_index.go`
（最短的不变量读法）、`stop_kind.go`（词表与顺序）。

## 闸门

- `TestMessagesForLLMCallIsIdempotent`（`board_index_test.go:70`）、
  `TestIndexSurvivesEmergencyCompressionShape`（`:106`）。
- `TestPlannerCommitsResumedDelegationWithoutCallingLLM`（`planner_resumption_test.go:22`）、
  `TestPlannerWithoutResumptionIsUnchanged`（`:112`，钉"没有落点时不许凭空塞 tool 消息"）、
  `TestPlannerIgnoresResumptionWithoutCallID`（`:130`）。
- `TestPlannerReadOnlyBatchParallelism` / `TestPlannerMixedBatchSegmentation` /
  `TestPlannerParallelKillSwitch`（`planner_parallel_test.go:210,245,266`）：混合批断言的是
  "写类元素执行窗口内没有任何只读元素在飞"。
- `TestPlannerRun_TriggerUsesProviderMeasurement` / `_MeasurementInvalidatedAfterRewrite`
  （`planner_prefix_trigger_test.go:52,116`）：第二/三档整段重写消息后 `measured` 必须作废，
  否则同一步下一档判定拿着不存在的规模做决定（`planner_compress.go:17-19`）。
- `TestComputeProjectID`（`project_context_test.go:103`）钉具体串；
  `TestProjectContextLoaderCacheHit`（`:59`）钉"同一实例只加载一次"。
  `project_context.go:53` 是"只读工作区根的 `YAGENT.md`/`CLAUDE.md`/`AGENTS.md`、不递归"这条
  加载契约的唯一实现。
- `TestPlannerRunForceDelegationReminder`（`planner_test.go:307`）：钉第 12 条的三件事——恰好 3 次、
  两条文案各一次、配额用尽后放行；`TestPlannerRunPlainTextExit`（`:174`）：委派过就不再注入提醒。
- `TestCharacterizationRecoveryCircuitBreaker`
  （`internal/agents/director_characterization_test.go:348`）与
  `TestRecoveryHandler_BreakerDisabledAtThresholdZero`（`recovery_test.go:61`）钉第 11 条的**状态机
  那一半**；"查询点与回报成对"这半边目前是缺口，见该条正文。
