# internal/blackboard — 跨 Agent 便签

> 范围：本目录内不可派生的约束。（原 `docs/blackboard-handoff-design.md` 已不存在，
> 便签与派生索引的规格由本文件持有。）全局摘要见仓库根 `AGENTS.md` §6.4。

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
不许直接把 0 传进来（换算在 `index.go:44-51`，哨兵常数在 `handoff.go:40-42`）。整体关闭不设 `enabled` 键，kill-switch 是
`YAGENT_BLACKBOARD=0`，`blackboard.Enabled()` 是写/读/注入/清理四处共用的唯一判据。

**3. `handoff.status` 的词表是 `ok | error | degraded`（+ LLM 可自报的 `partial`），刻意没有 `paused`。**
分工钉死：`ok`/`partial` 由模型自报，`error` 由委派错误判定（`internal/agents/director.go:576-578`），
`degraded` 由重放侧按降级判据判定（`internal/agents/resume_delegation.go:276-278`）；
**只有 `ok`/`partial`/`error` 出现在 `agent_exit` 的枚举里**（`internal/toolspec/schemas.json:472-478`），
所以 `toolspec` golden 没动。把 `degraded` 放进枚举等于把降级判定交给看不到判据的一方
（`handoff.go:54-68`）。词表里**没有 `paused`**：被闸门 abandon 的委派**什么都不写**
（`internal/agents/director.go:569-575`）——写了就是一张空正文便签，
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
不传也能从索引发现。归一的唯一出口是 `ListNotesByAgent`（`:200-206`）——`resolveAgentRef` 与
`list_notes` 都走它，别在工具侧再抄一份别名表。

**9. 路径形状与"拿不到 home"的落点。** `NotesRootDir` = `YAGENT_NOTES_ROOT` → `~/.yagent/notes`，
`os.UserHomeDir()` 失败时**退回 CWD 相对的 `notes/`**（`board.go:35-45`）——与浏览器状态目录同一类
失效（在用户仓库里长出目录），不报错。板内形状 `notes/{boardID}/{agent}/{stem}.md`
（`BoardDir`/`AgentDir`，`board.go:79-87`），ref 的线上形态就是 `{agent}/{stem}`（`note.go:130`）；
boardID 净化后截 128、agent 名截 64 且空串变 `unknown`（`board.go:51-63,89-98`）。

**10. board 作用域 = session，而"两块板"的判据在调用方。** `SetBoardScope(sessionID, taskID)`
（`internal/agents/director.go:446-459`）sessionID 优先、空则 taskID；**两者都空时不许把空串交给
`SanitizeBoardID`**——它给空串编一个 `adhoc_<unix_ts>_<rand4>`（`board.go:51-77`），于是每轮一块新板、
索引永远为空且不报错。`/resume` 靠 `session_meta.board_id` 重连旧板
（`internal/memory/rollout_types.go:28`、`internal/tui/tui_resume.go:559`），存量 rollout 回落 taskID。
boardID 由 ctx 传给三个读回工具（`WithBoardID`/`BoardIDFrom`，`context.go:7-16`）。

**11. 正文是交付物全文，索引是它的一行目录，两者的预算各算各的。** 正文为空才回落成
`handoff.Render(0)`（`note.go:95-97`）；"取哪一段算全文"（以 `agent_exit` 收尾时取最后一条
assistant 消息、不是那句 reason）在 `internal/agents` 的 `deliverableText`，本包不认识。
索引一行的成本上限 = `index_max_notes × index_line_chars`（默认 12/160，`index.go:22-26`），
而且是**每轮**注入：Director 每次 LLM 调用（`internal/agents/director/board_index.go:27-28`）
加每次委派前缀（`internal/agents/director.go:679`）。`BuildIndex` 只 decode 最新的 maxNotes 个
（文件名字典序 == 时间序，`:78-81`），读不出来的记 `skipped` 并写进尾部指示行（`:112-119`）——
"板是空的"与"有几条读不出来"必须可区分。

**12. `NoteMeta` 刻意没有 `Body` 字段**（`note.go:36-49`，只有 `BodySize`）：`list_notes` 与索引行
都从它渲染，枚举因此便宜到每轮可用；正文取回只有一条路（`ReadNote`，分页在工具层）。

**13. 便签文件名的唯一性来自计数器，不来自随机段。** 形状是
`20060102T150405Z_<rand4>-<pid>-<seq6>.md`（`board.go:124-142`）。随机段只有 2 字节：同进程
20 个并发写同一块板撞名概率约 0.3%，200 个约 78%，而撞上的表现是 `AtomicWrite` 的 rename
把先写那张**静默吃掉**——没有错误、也没有第二条日志，正是根 AGENTS.md §11 坑 43 那一族。
所以进程内唯一交给 `noteSeq`（单调 + 定宽零填充，因此字典序仍然 == 时间序），pid 兜跨进程，
随机段只兜"pid 复用 + 同秒"。用例把 `noteRandom`（`:136`）换成常量来证明机制在、不靠运气。

## 入口

`note.go`（存储与 front-matter）、`index.go`（派生索引）、`resolve.go`（`ContextPack`）、`board.go`（路径与原子写）、
`context.go`（boardID 的 ctx 载体，13 行的契约文件）。

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
`TestConcurrentWriteNotesNeverCollide`（`note_stem_test.go:52`，200 个并发写少一张即红）与
`TestGenerateNoteFilenameSurvivesConstantRandomness`（`:18`，把 `noteRandom` 钉成常量：
删掉计数器或让它不递增，第一条名字就重复，当场红）、
`TestBoardWriterNoteCarriesFullDeliverable`（`internal/agents/board_writer_test.go:67`，正文退回摘要即红）。
瞬态那条不变量的闸门在 `internal/agents/director/board_index_test.go`（见该目录 `AGENTS.md`）。

路径与作用域那三条（钉第 9/10/12 条）：`TestNotesRootDir`（`board_test.go:12`）、
`TestSanitizeBoardID`（`:19`）、`TestGenerateNoteFilename` + `TestParseNoteRef`（`:75`、`:340`，
形状互为镜像）、`TestWithBoardID`（`:92`）、`TestListNotes`（`:183`，`NoteMeta` 加进 `Body` 就应当
让枚举侧的用例变红）；工具侧的对应闸门在另一个包：
`TestListNotesLineFields`（`internal/agents/list_notes_tool_test.go:77`，只给一行结论不给正文）、
`TestListNotesAgentFilterUsesAlias`（`:100`，钉第 8 条的唯一出口）、
`TestReadNoteRendersHandoff` / `TestReadNoteMarksV1Note`（`:145`、`:187`，钉第 5 条的两半）。

**已知覆盖缺口**：`TestNotesRootDir` 只钉 `YAGENT_NOTES_ROOT` 一级，**没有**用例钉"`UserHomeDir`
失败 → 退回 CWD 相对 `notes/`"（第 9 条）；要改这段查找顺序，先补一条夹具用例再改。
