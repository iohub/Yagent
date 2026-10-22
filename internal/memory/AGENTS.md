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
