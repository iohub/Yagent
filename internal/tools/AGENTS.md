# internal/tools — 工具执行层与工作区守卫

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §5。
> "谁能用什么"不在这里（画像在 `internal/toolspec`）；LLM-工具循环在 `agents/executor.go`。

## 边界

把注册表里的 Spec 装配成一次运行的工具面（`Surface`），并提供文件/Shell/检索/调用图/浏览器/
认知类工具的执行器，加上工作区守卫与授权回路。`internal/diff`（16 行）只是 `search_replace_in_file`
的回显装饰：内容相同返回空串（`internal/diff/diff.go:10-12`），三个调用点都只当展示文本、不判错，
所以"空串"不会被误读成失败——10MB/50KB 那些限制不在它那儿。

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

**5. `agent_exit` 的参数在本层只回显、不解析**（`flow_control.go:48-57`）。加一个参数必须同时改
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
`adapter.go:118-159`）。入参不可用**不报错**，回落成单参数 `{"input": 原始串}`，并留一条
`slog.Warn`（`:126-137`；判据是 `llm.UsableToolArguments`，所以字面量 `null` 也算不可用）——
非 JSON 入参的工具靠这一步活着；结果一律 `json.Marshal` 回序列化（`:152-159`），
执行器返回不可序列化的值是错误而不是空结果。

**10. 工具声明上 provider 前必须按名字排序。** `SortToolDefs`（`adapter.go:171-175`）的三个消费点
都调它（`surface.go:247`、`internal/agents/director/planner_llm.go:34`、
`internal/agents/director.go:255` 的 hash 日志）；`ComputeToolDefsHash` 自排序副本后取
sha256 前 8 字节（`adapter.go:180-190`）。顺序漂走的症状不是崩溃，是 provider 端前缀缓存
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

**14. 文件大小限制的数值口径住在这两个文件。** read_file 的单次预算是
`maxReadLines`/`maxReadContentBytes`（`file_operations.go:112-113`，先触发的生效）、
绝对拒绝线 `maxFileSizeForRead`（`:107`）；三个都写成 **var** 当测试 seam，
否则"到顶之后"的分支在用例里要么触发不了、要么得造几百 MB 真文件。编辑侧 old/new 双向 10MB
（`file_edit.go:87,122`）；run_bash 的输出封顶是**另一个数字**（见第 16 条）。
不在 `internal/diff`（见边界），也不在压缩层。
预算这么定是因为 read_file 的结果是 `TruncFirst`（prio-0，第一档压缩最先砍它）：
多读可重取，而多跑一步要把整个上下文重发一遍。

**15. 仓库里可能根本没有 rg，搜索行为依赖 PATH。** `rgPath` 优先取内嵌产物，失败返回裸串
`"rg"` 交给系统解析（`search_operations.go:16-23`）；git 树的 `dist/bin` 只有占位文件，
真 rg 只在 release 构建进目录。改 `dist/` 下产物必须 `git add -f dist/...`
（`dist/` 整体被 gitignore，漏加的表现是 `go:embed` 编不过或永远分发旧数据）。

**16. 前台 `run_bash` 的等待必须有界，且四类结束方式各有回执。** `CombinedOutput` 的等待条件里
含"管道 EOF"，而 EOF 由**子孙进程**决定：`cmd &` / `nohup … &` 时直接子进程打完 `echo` 就退了，
Go 的 ctx 看门狗从此不再触发 `Cancel`（它只管"进程还活着时 ctx 结束"），于是工具超时、`activity`
的 idle/total、用户取消**全部打在空气上**——2026-10-26 那次事故把整条委派卡到手工 kill 为止。
唯一解开这道等待的是 `cmd.WaitDelay`（`system_operations.go:91`，seam 是 `runBashWaitDelay:22`）。
同一分支不许直接回吐 `runErr.Error()`：超时时它长这样 `"signal: killed"`，模型看不出"该用
is_background"和"命令自己失败了"的区别，所以四类出口在 `:113-133` 各写各的
（成功 / `ErrWaitDelay` + ProcessState 里的真 exit code + `note:139` / 超时或取消 / 非零退出）。
输出封顶是 `maxShellOutputBytes`（`system_operations.go:28`，自带 10MB 字面量；它历史上复用
read_file 的整读常量，两个数字如今表达两件不同的事，共用了改一个就动另一个），writer 到顶后**继续排空**——
返回错误会让孩子收 SIGPIPE，把"输出太大"变成"命令失败"（`internal/util/cappedbuffer.go:5-7`）。
**根 AGENTS.md §11 坑 46**。后台分支不受影响：它根本不接管道。

**17. 前台调用拥有整个进程组，后台调用把寿命交给用户。** 两条分支都 `Setpgid`
（前台 `system_operations.go:97`、后台 `:58`）。前台必须杀组（`Cancel: kill(-pgid)`，`:98`）：
exec 默认的取消只打得到直接子进程 bash，`make`/`node` 这些子孙会留下占端口的孤儿
（2026-10-26 那次事故留下的就是一个还在 LISTEN 的 proxy）。代价是**终端信号再也传不到子进程**
—— 于是取消只有 ctx 这一条通道，那条链在 `internal/shutdown`，两半必须一起看。
`liveGroups`（`:143-180`）只登记**在飞的前台**调用（`:104` 登记、`:106` 注销），唯一消费者是
第二次信号的硬退出：`main.go` 把 `KillLiveProcessGroups` 传给 `shutdown.Config.OnHardExit`，
因为 `os.Exit` 跳过所有 defer。`is_background=true` 刻意不进表 —— 那条分支的意义就是"服务留给用户"。
反向的一半同样是契约而不是运气：exec 只在"进程还活着时 ctx 结束"才调 `Cancel`，所以调用**返回之后**
`internal/agents/executor.go:154-155` 那次取消不会杀掉刚被放行的 daemon（`nohup … &` 因此真能留下服务）。

**18. 行尾是契约，不是渲染细节。** `read_file` 只按 `\n` 切、把 `\r` 留在行内容里
（`readFileInRange`），交回的正文与磁盘字节一致；因此 `search_replace_in_file` 允许**一次**回退：
一致 CRLF 的文件在精确匹配失败后去掉 `\r` 再比一次，唯一命中才替换、随后整体还原 CRLF
（`applyReplace`，`file_edit.go:176`）。两个方向都不许过头：
① 混合行尾**不回退**，只点名原因——静默把整个文件重写成一种行尾比一次编辑失败更糟
（`isUniformCRLF` 要求每个 `\n` 前是 `\r` 且没有游离 `\r`）；
② 精确命中也都要转 `new_string` 的行尾——单行 old_string 必然精确命中，
而多行 LF 的 new_string 直接插进 CRLF 文件就当场造出混合行尾。
任何一种转换都要在回执里写 `line_endings`（`crlf-normalized` / `crlf-applied`），
否则"兜住了"这件事只有读代码的人知道（根 AGENTS.md §11 坑 43 同族）。

## 入口

`surface.go`（装配与降级）、`adapter.go`（`Call()` 的顺序：JSON 解析 → 边界检查 → 授权 → 执行）、
`workspace_guard.go`、`specs_file.go`（授权回路另看 `user_confirm.go`）。

## 闸门

- `TestSurface_DeferredDisabledIsLegacyBehavior`（`surface_test.go:111`）、
  `_BudgetDemotesWarmOnly`（`:164`）、`_FullYoloRemovesAskUser`（`:243`，与 `specs_flow.go:35-41` 成对）、
  `TestSurfaceFromAdapters_ReportsWhatTheLoopAsks`（`:343`）。
- `TestWorkspaceGuard_BrowserEvalAuthByRisk`（`browser_guard_test.go:12`）：良性 JS 也弹授权。
- `TestBrowserSpecsMatchRegistry`（`browser_operations_test.go:91`）与
  `TestBrowserToolsAreNeverReadOnly`（`:121`）：argv 表与 Spec 两侧不对账即红。
- 授权回路整链在 `internal/agents/e2e_authorization_test.go:270,326,361,391,415,480`
  （批准执行 / 拒绝回灌给模型 / 会话级免问 / 工作区内不问 / 忽略错配 request_id / 取消解开阻塞的工具），
  用的是**真实** dispatcher 与真实 `WorkspaceGuard`。
- 第 16 条的四条闸门在 `system_operations_hang_test.go:51,110,129,152`：去掉 `cmd.WaitDelay`、
  把裸 `err.Error()` 交回模型、把超时与取消合并成一句文案、截断不记账——**都做过变异，各自当场红**。
  计时走 `runBashWaitDelay` 与 `maxShellOutputBytes` 两个 seam，没有"等真实超时"的 sleep；
  三处 `time.Sleep` 全在有界轮询里：两处只在负向断言的窗口（≤2s，见第 17 条那一对），一处是杀组用例等孩子写出 pid 清单（5ms 间隔、5s 上限）。
- 第 17 条是**方向相反的一对**加一本登记账：`_TimeoutKillsWholeProcessGroup`
  （`system_operations_hang_test.go:195`，去掉 `Setpgid` 即红）、
  `_ReturnedCallSurvivesLaterCancel`（`:221`，把杀组挪到返回之后即红）、
  `TestKillLiveProcessGroupsEndsInFlightCall`（`:290`，不登记 / 不注销 / 不杀组 各红一次）。
  只留一条的话，"杀干净"与"别误杀"任意一边漂走都是绿的。
  它的同步点是"孩子写出 `pids` 文件"（`waitForPidsWritten`，`:265`），**不是**"进程组已登记"：登记紧跟 `cmd.Start()`（`system_operations.go:102-106`），那一刻 bash 还没 fork 任何内层命令，等登记就杀组会让 `pids` 根本来不及出现——曾经"整包并发跑稳定红、单跑全绿"就是这个形状，是用例的同步点错了，不是环境噪声。
- `TestExecuteRunBash`（`system_operations_test.go:11`）是这条改动前的行为基线：
  `foreground success`/`foreground failure` 断言的是**精确**输出，改回执形状时它会先叫。
- 第 9 条的兜底形状与"不可用入参必须留痕"在 `adapter_call_test.go`：半截 JSON、字面量 `null`、
  空串各一条（都断言**不报错**且得到 `{"input": 原始串}`），外加"合法对象不被兜底吃掉"和一条
  `slog.Warn` 的字段核对。`flow_control_test.go` 钉 agent_exit 的两条可区分消息与真实工具名
  （`missing` vs `must be a string, got float64`），并把 `null` 入参整链跑通。
- 第 14 条的预算阶梯在 `file_operations_read_test.go`（23 条，`ExecuteReadFile` 在此之前
  **零直接单测**）：每条夹具都带"必须真的超上限"的前置 `t.Fatalf`，否则上限断言恒真；
  12 条变异逐条验过各自变红（`docs/read-file-redesign.md` §9 记了串号与结果）。
  `BenchmarkReadFile_*`（同一目录的 bench 文件）是同一条探针的 before/after 落点，只做记录不做门禁。
- 第 18 条的七条闸门在 `file_edit_crlf_test.go`：回退无条件生效、混合行尾也转换、
  精确命中不转 `new_string`、回退不查唯一性、还原时忘记转回 CRLF——**各做过一次变异，都当场红**。
