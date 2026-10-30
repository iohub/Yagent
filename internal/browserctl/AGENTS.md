# internal/browserctl — browser-control CLI 执行层

> 范围：本目录内不可派生的约束。全局摘要见仓库根 `AGENTS.md` §5.4。
> （原 `docs/browser-agent-design.md` 已不存在，浏览器侧的规格由本文件与 `internal/tools/AGENTS.md` 持有。）

## 边界

外部 `browser-control`（Rust CLI）的执行层：进程/工作区/CDP 生命周期/URL 策略/输出整形。
**不是 Agent**，不决定何时该用浏览器（在 `internal/agents` 与 `internal/tools`），不含工具 schema。
工具层的 14 个 `browser_*` 全部 `Serial`，理由见 `internal/tools/AGENTS.md`。

## 硬约束

**1. `cmd.Dir` 是纪律，不是建议。** 上游把状态写在 **CWD 相对**的 `.browser-control/`，没有任何
环境变量能改这个根（`session.go:64-66`、`manager.go:34-35`）。所有调用必须带 `Dir`
（经 `mgr.spawnOpts` 注入，`spawn.go:53-54` → `session.go:95`）；漏设就会**在用户仓库里长出该目录**。
`WorkspaceDir` 是唯一入口（`manager.go:149-152`），新增调用点不要自己拼路径。

**2. 必须显式注入 `BROWSER_CONTROL_CDP_URL`**（`manager.go:524-531`）：没有它上游会硬扫
9222/9223，于是连到用户自己的调试浏览器或另一个任务。自启端口从 `CDPPortBase`(9333) 起并**跳过**
9222/9223（`:560-566`）。上游内部 CDP 超时默认仅 15s，本包按最长 wait 把它抬到 ≥65s，
下限与 `config.MinBrowserCDPTimeout` 共用一个常量（`:542-549`）——**别在别处重复注入这个变量**。
外层 `commandTimeout` 必须大于内层，否则判定权被抢（`:551-558`）。

**3. 同一 Session 内串行**：current-tab 与快照 ref 是磁盘上的共享状态（`session.go:77-88`）。

**4. 子进程不经 shell，argv 全由调用方构造**（`spawn.go:41-43`）——这是"LLM 提供的字符串不可能被
解释成 shell 元字符"的前提。

**5. attach 模式 / 非我们启动的浏览器绝不由我们 stop**（`manager.go:40,426-429`）；
崩溃回收靠上游的归属校验（`:449-451` + `internal/app/app.go:858-859`）。

**6. 非零退出不是 error**：trace 摘要回带给模型自诊（`session.go:18-19,125-127,197-221`）。
把它做成 Go error 的后果与根 AGENTS.md §11 坑 22 同族——上层会以为"这次委派失败了"。

**7. `profile_dir` 为空不等于"不共享"**：`profileDir`（`manager.go:170-182`）会回落到默认共享目录
`<home>/.yagent/data/browser-profile`（**必须在 workspaceRoot 之外**，否则登录态会被 TTL 清理扫走）；
显式值必须是绝对路径，相对值直接报错而不是静默忽略。`delegationID` 必须过
`SanitizeDelegationID`（防路径穿越，`context.go:32-51`）。

**8. URL 只允许 http/https。** `file://` 会让浏览器变成绕过 `WorkspaceGuard` 读本地文件的通道
（`policy.go:24-39`）；只拦参数不够，还要在会话建立时装阻断策略（`:12-15`）。

**9. 上游的行为与 README 不符，以代码为准。** `reload` 实际等于 `stop`（刷新页面要用
`browser_eval 'location.reload()'`）；上游**没有** `hover` / `back`（用 `eval` 派发 `mouseover` /
`history.back()`）。二进制查找顺序 `[browser] binary_path` → `~/.yagent/bin/browser-control` → `$PATH`，
第一级由 `app` 无条件从嵌入层解出（见 `internal/app/AGENTS.md`）。
上游源码不在本仓（只有 `dist/bin` 里的二进制），所以这三条只能按实测行为记，**不许编造 Rust 行号**。

**10. 工作区目录就是隔离边界，清理只在装配期跑一次。** 每次委派独占
`<workspaceRoot>/<SanitizeDelegationID(id)>`，默认 `~/.yagent/data/browser/{delegation}`
（`WorkspaceDir` `manager.go:150-152`、根路径 `:154-162`）；里面是上游写的 `.browser-control/`
（current-tab、`chrome.pid`、`traces/<毫秒时间戳>/error.txt`）与截图产物。上游把 `--user-data-dir`
硬编码成 `.browser-control/profile` **且没有覆盖 flag**，登录态只能靠 symlink 把它接过去
（调用点 `manager.go:214-218`、`linkProfileLocked:314-360`）。默认也共享：旧工作区留下的**真实** profile
目录在首次 `Acquire` 时被 rename 迁进共享目录，迁不动（共享目录非空 = 别的委派正在用）就告警并保留本地副本、
**绝不删用户数据、绝不阻断启动**（`takeOverWorkspaceProfile:368-384`；macOS 的 `rename` 连空目录都不允许覆盖，
所以写法是先 `Remove` 空目录再 `Rename`）。trace 目录名按字典序即时间序（`session.go:197-199`）。
两条回收只此一处调用（`internal/app/app.go:389-391`）：`ReapStale`（死 pid 直接删记录、活 pid 才
spawn `stop`，`manager.go:452-494`）与 `CleanupExpiredWorkspaces`（按目录 mtime 比 `[browser]
workspace_ttl`，`<=0` 一律不清理，`:497-521`）。**没有任何后台任务会删工作区**——别假设"过一会儿自己没了"。

## 入口

`manager.go`（生命周期与端口）、`session.go`（一次调用做什么）、`spawn.go`（进程边界）、`policy.go`（URL 策略）。

## 闸门

`session_test.go:11 TestSessionRun_InjectsDirEnvAndArgv`——**一条用例同时钉** `cmd.Dir`、env 里有
CDP URL、每次调用带 timeout（`:28-36`）；`manager_test.go:66 TestAcquire_AttachModeInjectsEnvAndNeverLaunches`
（`:88-93` 断言 65s 抬高）、`:188 TestFindFreePort_SkipsDebugPorts`、`:102`（启一次后复用）、
`:231`（attach 不动用户浏览器）、`:282`（相对 profile 拒绝）、`:419,452`（端点就绪竞态，真实 E2E 复现过）、
**登录态三条钉死第 7/10 条**：`:305`（不设 profile_dir 也 symlink 到默认共享目录、且该目录在 workspaceRoot 之外）、
`:339`（工作区已有真实 profile 目录 → rename 迁移，cookie 不丢）、`:375`（共享目录非空 → 保留本地副本、不报错）。
`policy_test.go:8,74`（file scheme）、`render_test.go:9,39,89`（丢坐标+去重、预算提示语、UTF-8 不切坏）、
`manager_test.go:475 TestReapStale_RemovesDeadPidRecord`、`:495 TestCleanupExpiredWorkspaces`
（钉第 10 条：TTL 与死 pid 两条回收；`<=0` 被改成"照样清"即红）、`:524`（清理不得顺着 profile symlink 清空共享登录态）。
