# internal/memory — rollout 账本与重放读侧

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.7。

## 边界

rollout JSONL 的**唯一写入者与唯一事实来源**：`/resume`、检查点、重放都只读它。
只管"会话账本"：不管压缩策略、不管委派编排、不判断业务对错。
仓库级持久知识不在这里（那是 `internal/blueprint`）——顺带住在这里的 KV 存储
（`SharedMemory` / `LocalMemory`）已于 2026-10-22 随知识整理机制一并删除。
任务记忆 `~/.yagent/tasks/*.jsonl` 在 `internal/datamanager`，**不是**断点续跑的事实来源。

## 硬约束

**1. 检查点指纹只能由读侧计算**（`rollout_checkpoint.go:93-104`）。写侧必须调用与重放端同一个
`FinalizeCheckpoint`，对**整份文件重建出的**消息序列算 sha256。按内存里那份算的后果：任何触发过
压缩的会话（=每一个长任务）都会在自己的检查点上对不上号，B 层恰好死在它最该起作用的地方。
`Reasoning` 必须算进指纹（`:35-37`，thinking 要回灌给 provider，漏算不是"少几个 token"而是
"整次重放被判成不可信"）；`IsAnchored` / `TruncationMarker` 刻意不在（`:38-40`）。

**2. 读自己写过的文件之前必须先排干。** 落盘是攒批的（32 条 / 20ms，`rollout_writer.go:45-46`）。
自己拼 `Flush + FinalizeCheckpoint(path)` 的调用点一定会拿到"少几条"的指纹——它不报错，
只是把能重放的会话判成损坏（`rollout_checkpoint.go:124-143`、`rollout_writer.go:286-291`）。

**3. `ConversationMemory.messages` 不导出，读写只走方法**：`Snapshot()` 拿拷贝、
`Append`/`Add*` 写、`Replace` 整体装载（不触发 MaxSize 截断）、`LastMessage()` 返回**拷贝**
（`memory.go:62-70`）。注释给出的三条理由：并发注入跑在工作线程上（planner 在主线程写同一份）；
留一个导出字段就等于承诺"锁保护一切"，而改之前它有 31 处包外直接读写、`datamanager` 甚至会 append；
旧版 `GetLastMessage` 返回指向 slice 内部的 `*ChatMessage`，拿着指针就能绕过锁改写（`:241-245`）。
`GetMessages` 只是 `Snapshot` 的旧名（`:210-212`），别当第二份读法。
JSON 线上形态 `{"messages":[…],"max_size":n}` 由 `MarshalJSON/UnmarshalJSON` 原样保住——**封装不许改协议**（`:77-83`）。

**4. 重放读侧不得复用 `ReadRolloutMemory`**（`rollout_replay.go:5-13`）：它会丢 system、
压 `Reasoning`、截工具输出，而重放的契约是"与暂停前逐条同形"。

**5. 本包不得 import `internal/thinklink`**（会成环，`rollout_replay.go:40`）。
`memory` 是 `agents` 与 `agents/director` 唯一共同依赖的包，所以 `delegation_id` 的 ctx 载体放这里
（`rollout_ctx.go:5-8`）——父子两个写入包不能互相 import，任何"共用实现"只能落在 memory。

**6. `Found=false` ≠ 不能继续**（`rollout_stop.go:15-17`）：终止事实必须显示成"未知"，
不许静默滤掉。终止事实只从尾部读（`:10-12`）。

**7. 本包的测试**没有** `TestMain` 隔离 `$HOME`**（与 `internal/agents` 不同）。
"往 `~/.yagent` 写夹具再扫"的用例必须每个 run 生成唯一 projectID，或自己 `t.Setenv("HOME", t.TempDir())`——
否则 `-count=4 -parallel 8` 下候选会累加，断言 `>=1` 恒真，门禁当场失效（根 AGENTS.md §11 坑 23，
`rollout_replay_test.go:19-22`、`rollout_stop_test.go:5-6`）。

**8. `Reasoning` 的写侧持久化与还原必须成对偶。** 写侧为带 `Reasoning` 的 assistant 消息先发一条
`reasoning` 记录（`rollout_convert.go:28-34`），重放侧再把它附着回下一条 assistant
（`rollout_replay.go:252-264`）。两侧规则在读侧头部明写为一一对偶（`:14-15`）——改一边不改另一边的
症状是"恢复后模型推理风格突变 / thinking 从它看到的世界里消失"，而没有任何地方报错。
指纹把 `Reasoning` 算进去是第 1 条的事，两件事各修各的洞。

**9. 停点判定只信文件尾部，且最后一条检查点胜出。** 写入端只在终止路径上写终止事件与
`agent_checkpoint`，所以列举会话不需要重建整份消息序列（`rollout_stop.go:11-15`）；一次会话
暂停-继续多轮、每轮各写一条检查点时，重建取最后一条（`rollout_replay.go:198-204`）；
有检查点记录但没有终止事件的文件（硬杀）仍算"有停点的会话"（`rollout_stop.go:131-133`）。

**10. 续跑资格按默认拒绝分类，每一档都带人话原因。** 只有 `paused` / `cancelled` 获得
`ResumeReplayable`（`rollout_stop.go:187-189`），default 分支必须"不可原样重放"（`:170-173`）——
反过来写，将来新增的任何停止原因都自动获得重放资格，那正是把失败当成可继续、重复执行副作用的路径。
存量文件没有 `stop_reason` 时按最保守的 cancelled 归类（`:181-186`）。`StopKind*` 词表住本包是因为
两个写入端分属两个包且 director 不得 import agents（`rollout_types.go:339-345`）——写成裸字符串的
表现是恢复侧少认一类停点原因，而没有任何地方会报错。

**11. 降级不是吞掉事实：超预算同时返回 error 与已重建的内容。** `ErrReplayTooLarge` 的含义是
"重放会立刻触发压缩、失去重放意义"，不是读失败（`rollout_replay.go:34-37`）；超预算时必须
连同重建出的 `Replay` 一起返回（`:127-128,242-244`）——调用方拿同一份事实生成降级交接报告，
只返回 error 就得再读一遍文件，而两遍之间文件可能变。任何"没能完整还原"（无法解析的行、消息数
触顶、`compacted` 重建、无检查点）逐条记进 `Degraded`（`:102-105,155,196,208,231`），恢复侧与 UI 都读它。

**12. 同一 `call_id` 的多条输出：后者原位胜出（last-wins）。** 恢复路径就会为一次委派写出第二条
同 id 输出（取消时一条 `Error:`、重放成功后一条真实结果），`normalizeToolPairing` 的原位覆盖语义
必须与 `llm.repairToolCallPairs` 的 map 语义一致（`rollout_reader.go:764-768`，覆盖计入
`SupersededOutputs` 的字段账 `:124-127`、`:820-823`）。两边不一致的表现是"进程内喂给模型的和
`/resume` 后喂给模型的不是同一份对话"。

**13. 委派的双向坐标走 ctx，文件名后缀是认亲凭据的镜像。** `WithDelegationID` 携带那次委派的
tool_call_id、子 Agent 写 `session_meta` 时取用（`rollout_ctx.go:19-23`）；`WithRolloutSessionID`
让子 Agent 把父会话记成自己的 `ParentSessionID`——做成双向的理由：一个 session 里同一角色可被委派
多次，单向的 delegation_id 从 Director 那次 call 找不到该读哪份子文件（`:36-41`）。两个取回的
**空串都必须当成"没有坐标，不能原样重放"**，而不是"它就是根会话"（`:25-27`）。writer 文件名形状
`<date>_<time>_<sessionID>[_<agentName>]`（`rollout_writer.go:122-125`）与读侧
`parseRolloutFilename`（`rollout_sessions.go:321-339`）互为镜像，改命名规则必须连镜像一起改。

## 入口

`rollout_types.go`（记录形状 + `StopKind*` 词表，`:347-379`）→ `rollout_checkpoint.go` →
`rollout_replay.go` → `memory.go`。

## 闸门

- `TestWriteCheckpointDigestMatchesReplay`（`rollout_replay_test.go:379`）：两侧规则分成两份实现即红。
- `TestReplayDigestDetectsTruncation`（`:157`）：把指纹变成常量即红。
- `TestNoPerStepCheckpointWritten`（`:429`）：有人加"每步一条检查点"即红（重放只需要最后一个停点）。
- `TestCheckpointDigestSeesQueuedRecords`（`rollout_writer_queue_test.go:130`）：删掉写检查点前的排干即红。
- `TestConversationMemoryConcurrentAppendKeepsEveryMessage`（`:384`）：去掉锁在 `-race` 下报竞态。
- `TestReadRolloutStopFacts_NoTerminalRecordIsNotFoundNotError`（`rollout_stop_test.go:112`）、
  `TestStopFactsState_EveryKindIsClaimed`（`:176`）：新增停点词表没人认领即红。
- `TestReadRolloutForReplayRoundTripsMessages`（`rollout_replay_test.go:68`）：写出去的消息序列
  必须一字不差读回来（含 `Reasoning`）；两侧转换不成对偶即红（钉第 8 条）。
- `TestReadRolloutStopFacts_LastCheckpointWins`（`rollout_stop_test.go:80`）：读取侧改成"取第一条
  检查点"即红（钉第 9 条）。
- `TestStopFactsState_UnknownKindDoesNotBecomeReplayable`（`rollout_stop_test.go:230`）、
  `TestStopFactsState_NoDeclaredKindFallsThrough`（`:211`）、
  `TestStopFactsState_LegacyAbortedWithoutReasonIsReplayable`（`:163`）：default 分支写成可重放即红
  （钉第 10 条）。
- `TestReplayTokenBudgetRejectsOversizedContext`（`rollout_replay_test.go:284`）：超预算只返回 error、
  不带回已重建 `Replay` 即红（钉第 11 条）。
- `TestReadRolloutMemory_Table` 的 `duplicate_output_keep_last` 用例（`rollout_reader_test.go:335`）：
  配对修复退回 first-wins 即红；跨包一致性由 `TestToolPairingNormalizersAgreeOnDuplicateOutput`
  （`internal/agents/tool_pairing_test.go:61`）钉（钉第 12 条）。
- `TestParseRolloutFilename`（`rollout_sessions_test.go:208`）：writer 命名规则与读侧镜像解析
  分叉即红（钉第 13 条）。
