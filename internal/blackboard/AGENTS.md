# internal/blackboard — 跨 Agent 便签

> 范围：本目录内不可派生的约束。完整设计见 `docs/blackboard-handoff-design.md`，
> 全局摘要见仓库根 `AGENTS.md` §6.4。

## 边界

每次委派的结果写成一张便签（front-matter = 结构化 `Handoff`，正文 = 交付物**全文**），
外加一份每轮重扫磁盘得到的派生索引。不负责决定"何时写、写什么"（在 `internal/agents` 的委派收尾处），
也不给 artifact 提供存储（在 `internal/artifact`）。

## 硬约束

**1. 索引是派生态，而且是瞬态的。** 每次调用都重扫磁盘，只进**这一次 LLM 调用**的入参，
绝不写进 `ConversationMemory`、绝不写进 rollout（`index.go:10-20`）。这是它能扛住上下文压缩的唯一
原因——压缩动的是会话历史里的 tool 消息，`note:` / `artifact:` 引用以前就是这么蒸发掉的。
注入方靠 `IndexHeaderPrefix` 识别并剔除上一轮插过的索引，保证同一步重复注入幂等（`:16-20`）。

**2. 三个阀门靠"键的存在性"判定，不是零值。** `index_max_notes = 0` = 关索引、
`index_line_chars = 0` = 不裁剪行、`ttl = "0"` = 不清理；"没写这一段"解析出来同样是 0（`index.go:33-51`）。
配置的 `index_line_chars=0` 必须经 `IndexOptionsFromConfig` 换算成 `MaxRenderRunes`，
不许直接把 0 传进来（`handoff.go:40-42`）。整体关闭不设 `enabled` 键，kill-switch 是
`YAGENT_BLACKBOARD=0`，`blackboard.Enabled()` 是写/读/注入/清理四处共用的唯一判据。

**3. `handoff.status` 词表刻意没有 `paused`。** `error` 由委派错误判定、`degraded` 由重放侧判定，
让模型自报"我这份是降级的"等于把判断交给看不到判据的一方（`handoff.go:54-68`）。
被闸门 abandon 的委派**什么都不写**（`internal/agents/director.go:566-572`）：写了就是一张空正文便签，
还会占住每轮重扫的索引位去冒充一条结论。

**4. `ParseHandoff` 永不返回 error、也永不返回 nil**，三档回落最坏 `Degraded=absent`
（`handoff.go:76-108`）。`Render` 预算不足时**逐条丢 Fact、绝不截 Conclusion**（`:192-196`）。

**5. artifact 线索以系统侧为准，覆盖 LLM 自报**（`note.go:93-94`）。
v1 存量便签只读时从 `Summary` 合成并标 `Degraded=v1`，**不迁移不重写**（`:279-288`）。

**6. 本包不 import artifact**：探测能力经 `ArtifactInfoFn` 注入（`resolve.go:114-116`）。
原子写只转调 `util.AtomicWrite`，tmp 后缀 `.md.tmp` **故意不以 `.md` 结尾**，否则索引与 GC
会把中间态当便签（`board.go:106-117`、`index.go:129`、`gc.go:131`）。

**7. GC 显式 `Lstat`、绝不跟随 symlink**；未过期 board 不得 `RemoveAll`；条数排序键是**文件名**
不是路径（`gc.go:48-59,91-115`）。

**8. `agent:repo` 与 `repo_agent` 两种写法必须归一，取不到必须计入 `unresolved` 并在结果头显示**
（`resolve.go:179-199,144-148`）。`context_refs` 的语义是**强调**（额外内联全文）而不是准入：
不传也能从索引发现。

## 入口

`note.go`（存储与 front-matter）、`index.go`（派生索引）、`resolve.go`（`ContextPack`）、`board.go`（路径与原子写）。

## 闸门

`TestBuildIndexEmptyAndValve`（`index_test.go:136`，把 0 当默认即红）、
`TestFormatIndexLineTinyBudgetStillHasRef`（`:190`，裁掉取回句柄即红）、
`TestRenderDropsFactsNotConclusion`（`handoff_test.go:146`）、
`TestWriteNoteArtifactIDsWinOverHandoffArtifacts`（`note_v2_test.go:60`）、
`TestReadV1NoteSynthesizesHandoff`（`:113`）、
`TestResolveRefsMaxLimit`（`resolve_test.go:64`，**夹具必须互不相同**——用 20 条相同 ref 测上限 10
会让去重把数量吃掉、断言恒真）、`TestResolveRefsAgentRefMissingReportsUnresolved`（`:152`）、
`TestContextPackBudgetTruncationKeepsExcerpt`（`:187`）、
`TestGCSkipsSymlinkInRoot`（`gc_test.go:185`）、`TestGCCountCapKeepsBoardDirs`（`:87`）、
`TestGCCountCapSortsAcrossAgentDirs`（`:127`）、
`TestConcurrentWriteNotesNeverCollide`（`note_stem_test.go:13`，有人把 stem 改成按任务文本命名即红）、
`TestBoardWriterNoteCarriesFullDeliverable`（`internal/agents/board_writer_test.go:67`，正文退回摘要即红）。
瞬态那条不变量的闸门在 `internal/agents/director/board_index_test.go`（见该目录 `AGENTS.md`）。
