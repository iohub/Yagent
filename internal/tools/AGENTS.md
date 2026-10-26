# internal/tools — 工具执行层与工作区守卫

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §5。
> "谁能用什么"不在这里（画像在 `internal/toolspec`）；LLM-工具循环在 `agents/executor.go`。

## 边界

把注册表里的 Spec 装配成一次运行的工具面（`Surface`），并提供文件/Shell/检索/调用图/浏览器/
认知类工具的执行器，加上工作区守卫与授权回路。`internal/diff`（16 行）只是 `search_replace_in_file`
的回显装饰：内容相同返回空串（`internal/diff/diff.go:10-12`），三个调用点都只当展示文本、不判错，
所以"空串"不会被误读成失败——10MB/1000 行那些限制不在它那儿。

## 硬约束

**1. `Surface` 是唯一工具来源。** `NewSurfaceFromAdapters`（`surface.go:75-99`）存在的理由是
**不让测试自带第二个载体**——历史上并发谓词扫 `Adapters`、测试也填 `Adapters`，于是八条用例全绿
而生产一次都没并行（根 AGENTS.md §11 坑 33）。不要再给 `ExecutorConfig` 塞快照。
`scripts/check_no_manual_lookup.sh:19-31` 的 FORBIDDEN 十条（按名字查工具的旧符号与名字表）必须为零，
PENDING 名单已清空（`:34-37`），往 `internal/agents` 里重新写按工具名 `case` 分派直接红。

**2. 授权判据来自注册表，分派按 Effect 不按名字**（`workspace_guard.go:44-50,83-93`）。
guard 由 `rebuild` 统一挂（`surface.go:122-125`），集中构造消除了"某个 appender 忘记 SetGuard"。

**3. 工作区目录本身算越界。** `isInWorkspace` 对 `rel == "."` 返回 false
（`workspace_guard.go:235-243`）。`run_bash` **没有黑名单**：纵深防御 = 信任 LLM 的 `is_dangerous`
+ 任何指向工作区外的绝对路径 token 都要授权，`safeSystemPaths` 白名单只此一处且根目录恒危险
（`:210-228,248-296`）。

**4. deferred 开关归 Surface。** 调用方或探针预填 `LoadTool`/`DeferSchemas` 不算开启
（`surface.go:60-70`）。预算降级**只动 Warm，绝不能动 Hot**（`surface.go:128-144`）——
流程控制与元工具降级会让模型失去收尾与取 schema 的能力。

**5. `agent_exit` 的参数在本层只回显、不解析**（`flow_control.go:26-34`）。加一个参数必须同时改
两处：schema、以及这里的回显。少回显一个键，提交就在离开循环的那一刻**静默消失**、不报错、不丢日志
（`handoff` 与 `blueprint` 都挂在这条路上，根 AGENTS.md §11 坑 38）。

**6. 14 个 `browser_*` 全部显式 `Serial`**：它们共享一条 CDP 与磁盘上的 current-tab
（`specs_browser.go:11-13`；执行侧 `internal/browserctl/session.go:77-88`）。截图路径由 Go 侧硬收敛到会话
工作区，因为上游会原样写绝对路径（`browser_operations.go:447-448`）。

**7. 浏览器子进程不经 shell，argv 全由调用方构造**（`internal/browserctl/spawn.go:41-43`）——这是
"LLM 提供的字符串不可能被解释成 shell 元字符"的前提，新增调用点不要自己拼路径或走 `sh -c`。

**8. 外部工具走 `Deps.ExternalSpecs`，与静态规格在 `rebuild()` 汇合。**
追加时按画像的 Grant/Deny 过滤（矩阵见 `toolspec/profiles.go`）、同名时静态优先并记进 `Skipped()`；
guard 仍在循环之后统一挂。`ExternalSpecs` 一律 Cold，靠既有 deferred 目录与 `load_tool`。
`catalogBrief` 的取值顺序是 **`Adapter.Brief()` > `toolspec.Brief(name)` > 描述首句** ——
写反的话动态工具的 `Spec.Brief` 形同虚设（目录里会是一行截断的英文描述）。
`use_skill` 的执行器只认 `toolspec.SkillSource`，本包不认识 `internal/skills`。

**9. `Adapter.Call` 内部的四步顺序是契约**（JSON 解析 → 工作区边界 → 危险操作授权 → 执行，
`adapter.go:99-127`）。JSON 解析失败**不报错**，回落成单参数 `{"input": 原始串}`（`:100-104`）——
非 JSON 入参的工具靠这一步活着；结果一律 `json.Marshal` 回序列化（`:119-126`），
执行器返回不可序列化的值是错误而不是空结果。

**10. 工具声明上 provider 前必须按名字排序。** `SortToolDefs`（`adapter.go:138-142`）的三个消费点
都调它（`surface.go:247`、`internal/agents/director/planner_run.go:124`、
`internal/agents/director.go:255` 的 hash 日志）；`ComputeToolDefsHash` 自排序副本后取
sha256 前 8 字节（`adapter.go:147-159`）。顺序漂走的症状不是崩溃，是 provider 端前缀缓存
逐条失效——要到 token 账单对不上才被发现有两条路：排序漏一处，或 hash 比对基线被整体重算。

**11. 授权只有"问用户"这一种强制，没有硬拒绝路径；放行状态分三层，持久化只有一处。**
`Check` 对越界/危险只返回 needsAuth，`RequestAuth` 阻塞等用户（`workspace_guard.go:53-93,112-120`）；
会话级 per-tool / 全局跳过（`:23-24`）、项目级 `authorized_projects` 持久化到
`~/.yagent/settings.json`（`:298-345`）、YOLO（`:38-41`）。三层任意一层命中，此后所有检查直接跳过。

**12. YOLO 两档的真实差异不在路径边界。** `-y` 的 `SetYoloMode` = **一切授权全绕过**，
含工作区外路径（`:59-62` 短路 `Check`、`:117-120` 自动批准；装配点 `internal/app/app.go:324-328`）。
`-Y` 隐含 `-y`，额外只做两件事：把 `ask_user_for_help` **摘出**工具面
（`specs_flow.go:35-41`，与闸门 `_FullYoloRemovesAskUser` 成对）并注入自主决策指令
（`internal/globalctx/global_context.go:83-90`）。根 AGENTS.md §5.4 的"-y 只跳工作区内授权"
与代码不符——工作区外从来只是"要授权"，而授权被 `-y` 整体绕过。

**13. 授权回路按 `request_id` 配对，这两个事件类型在总线上永不丢。**
`RequestConfirmation`/`RequestUserHelp` 缺 ID 时自造 uuid、按 id 挂 channel 进 `pendingHelp`，
`Consume` 只认 `user_help_response`，错配 id 只 warn 不响应（`user_confirm.go:42-107,141-183`）。
`user_help_needed`/`user_help_response` 走 dispatcher 的 critical 旁路队列
（FIFO、满丢最旧，`internal/messaging/message_dispatcher.go:4,32`）——与普通事件"队列满即丢"相反；
队列满/无 pending 在回路两侧都只 warn（`user_confirm.go:150,209`），丢信号的兜底是 ctx 取消与超时。

**14. 文件大小限制的数值口径住在这两个文件。** 整读 10MB 硬限 `MaxFileSizeForEntireRead`
（`file_operations.go:108`）、2MB 软警告 `SoftFileSizeLimit`（`:111`）、单次 1000 行
（`MaxEntireFileLines`/`MaxLineRangeSize`，`:115,125`）；编辑侧 old/new 双向 10MB
（`file_edit.go:87,122`）。不在 `internal/diff`（见边界），也不在压缩层。

**15. 仓库里可能根本没有 rg，搜索行为依赖 PATH。** `rgPath` 优先取内嵌产物，失败返回裸串
`"rg"` 交给系统解析（`search_operations.go:16-23`）；git 树的 `dist/bin` 只有占位文件，
真 rg 只在 release 构建进目录。改 `dist/` 下产物必须 `git add -f dist/...`
（`dist/` 整体被 gitignore，漏加的表现是 `go:embed` 编不过或永远分发旧数据）。

**16. 前台 `run_bash` 的等待必须有界，且四类结束方式各有回执。** `CombinedOutput` 的等待条件里
含"管道 EOF"，而 EOF 由**子孙进程**决定：`cmd &` / `nohup … &` 时直接子进程打完 `echo` 就退了，
Go 的 ctx 看门狗从此不再触发 `Cancel`（它只管"进程还活着时 ctx 结束"），于是工具超时、`activity`
的 idle/total、用户取消**全部打在空气上**——2026-10-26 那次事故把整条委派卡到手工 kill 为止。
唯一解开这道等待的是 `cmd.WaitDelay`（`system_operations.go:82`，seam 是 `runBashWaitDelay:19`）。
同一分支不许直接回吐 `runErr.Error()`：超时时它长这样 `"signal: killed"`，模型看不出"该用
is_background"和"命令自己失败了"的区别，所以四类出口在 `:90-110` 各写各的
（成功 / `ErrWaitDelay` + ProcessState 里的真 exit code + `note:116` / 超时或取消 / 非零退出）。
输出封顶复用 `MaxFileSizeForEntireRead`（`maxShellOutputBytes:23`），writer 到顶后**继续排空**——
返回错误会让孩子收 SIGPIPE，把"输出太大"变成"命令失败"（`internal/util/cappedbuffer.go:5-7`）。
**根 AGENTS.md §11 坑 46**。后台分支不受影响：它根本不接管道。

## 入口

`surface.go`（装配与降级）、`adapter.go`（`Call()` 的顺序：JSON 解析 → 边界检查 → 授权 → 执行）、
`workspace_guard.go`、`specs_file.go`（授权回路另看 `user_confirm.go`）。

## 闸门

- `TestSurface_DeferredDisabledIsLegacyBehavior`（`surface_test.go:111`）、
  `_BudgetDemotesWarmOnly`（`:164`）、`_FullYoloRemovesAskUser`（`:243`，与 `specs_flow.go:35-41` 成对）、
  `TestSurfaceFromAdapters_ReportsWhatTheLoopAsks`（`:343`）。
- `TestWorkspaceGuard_BrowserEvalAuthByRisk`（`browser_guard_test.go:12`）：良性 JS 也弹授权。
- `TestBrowserSpecsMatchRegistry`（`:91`）与 `TestBrowserToolsAreNeverReadOnly`（`:121`）：
  argv 表与 Spec 两侧不对账即红。
- 授权回路整链在 `internal/agents/e2e_authorization_test.go:270,326,361,391,415,480`
  （批准执行 / 拒绝回灌给模型 / 会话级免问 / 工作区内不问 / 忽略错配 request_id / 取消解开阻塞的工具），
  用的是**真实** dispatcher 与真实 `WorkspaceGuard`。
- 第 16 条的四条闸门在 `system_operations_hang_test.go:48,107,126,149`：去掉 `cmd.WaitDelay`、
  把裸 `err.Error()` 交回模型、把超时与取消合并成一句文案、截断不记账——**都做过变异，各自当场红**。
  计时走 `runBashWaitDelay` 与 `maxShellOutputBytes` 两个 seam，用例里没有 `time.Sleep` 等真实超时。
- `TestExecuteRunBash`（`system_operations_test.go:11`）是这条改动前的行为基线：
  `foreground success`/`foreground failure` 断言的是**精确**输出，改回执形状时它会先叫。
