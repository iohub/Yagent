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
