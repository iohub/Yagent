# internal/browserctl — browser-control CLI 执行层

> 范围：本目录内不可派生的约束。完整设计见 `docs/browser-agent-design.md`，
> 全局摘要见仓库根 `AGENTS.md` §5.4。

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

**5. attach 模式 / 非我们启动的浏览器绝不由我们 stop**（`manager.go:40,368-371`）；
崩溃回收靠上游的归属校验（`:391-393` + `internal/app/app.go:912-913`）。

**6. 非零退出不是 error**：trace 摘要回带给模型自诊（`session.go:18-19,125-127,197-221`）。
把它做成 Go error 的后果与根 AGENTS.md §11 坑 22 同族——上层会以为"这次委派失败了"。

**7. `profile_dir` 必须绝对路径**（`manager.go:290-297`）；`delegationID` 必须过
`SanitizeDelegationID`（防路径穿越，`context.go:32-51`）。

**8. URL 只允许 http/https。** `file://` 会让浏览器变成绕过 `WorkspaceGuard` 读本地文件的通道
（`policy.go:24-39`）；只拦参数不够，还要在会话建立时装阻断策略（`:12-15`）。

**9. 上游的行为与 README 不符，以代码为准。** `reload` 实际等于 `stop`（刷新页面要用
`browser_eval 'location.reload()'`）；上游**没有** `hover` / `back`（用 `eval` 派发 `mouseover` /
`history.back()`）。二进制查找顺序 `[browser] binary_path` → `~/.yagent/bin/browser-control` → `$PATH`，
第一级由 `app` 无条件从嵌入层解出（见 `internal/app/AGENTS.md`）。

## 入口

`manager.go`（生命周期与端口）、`session.go`（一次调用做什么）、`spawn.go`（进程边界）、`policy.go`（URL 策略）。

## 闸门

`session_test.go:11 TestSessionRun_InjectsDirEnvAndArgv`——**一条用例同时钉** `cmd.Dir`、env 里有
CDP URL、每次调用带 timeout（`:28-36`）；`manager_test.go:66 TestAcquire_AttachModeInjectsEnvAndNeverLaunches`
（`:88-93` 断言 65s 抬高）、`:188 TestFindFreePort_SkipsDebugPorts`、`:102`（启一次后复用）、
`:231`（attach 不动用户浏览器）、`:282`（相对 profile 拒绝）、`:295,328`（端点就绪竞态，真实 E2E 复现过）、
`policy_test.go:8,74`（file scheme）、`render_test.go:9,39,89`（丢坐标+去重、预算提示语、UTF-8 不切坏）。
