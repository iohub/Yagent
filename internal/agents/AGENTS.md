# internal/agents — 子 Agent 与共享执行循环

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md`；编排内核见
> `director/AGENTS.md`。模块结构与文件计数由 `internal/blueprint` 每轮派生，不要往这里抄。

## 边界

六个子 Agent 的实现 + 它们共享的 LLM-工具循环 `RunAgentLoop` + 委派结果的分级与采集
（handoff / blueprint / artifact）。**不负责**：装配（`internal/app`）、工具实现与"谁能用什么"
（`internal/tools` + `internal/toolspec`）、Director 的编排逻辑（在 `director/` 子包，
`agents/director.go` 只是门面）。

## 硬约束

**1. `ExecutorConfig.Surface` 是唯一工具来源。** 不许再把 `Adapters` 快照加回来
（`executor.go:29-34`）：并行谓词曾经扫那份列表，而生产只填 Surface，于是**测试全绿、并行一次
都没发生**（根 AGENTS.md §11 坑 33）。并发准入判据只有一份 `toolCallIsParallelizable`，
两个循环共用（`executor.go:164-173`）——两份同形实现必有一边漂走。

**2. 以 `agent_exit` 收尾的 Agent 必须自己置 `StopOnFinish`。**
`repo.go:202`、`coding.go:393`、`chat.go:81`、`devops.go:90`、`browser_agent.go:124`，
消费点在 `executor.go:854`。漏设不报错：`agent_exit` 只是"又执行了一次工具"，循环继续烧到
MaxSteps，而 `deliverableText` 取的是最后一条 assistant 消息——便签正文与 artifact 全文存的
都不是那份交付（`repo.go:199-202` 把这段写成了注释）。

**3. 交付全文只经 `deliverableText()` 取**（`result.go:52-60`）。判据是返回 JSON 的**形态**
（`finished:true` + `reason` 键），不是 Agent 名字。`AgentResult.Text` 的既有语义没动。

**4. 每次委派新建实例，且 nil 判定必须在装箱之前。**
`SpawnForDelegation` 对 nil 接收者返回 nil 而不是 panic（`spawn.go:29-35`）；
`delegateTargets` 每个 spawn 必须先判**具体类型**的 nil 再返回 interface（`director_surface.go:36-43`）——
装着 nil 的 interface `!= nil`，`delegateTo` 那条"not wired"守卫于是永远不成立。

**5. 暂停闸门在 step 边界，且只有一处。** `executor.go:434-439`（`stepNumber++` 之前）：
ctx 不取消，恢复后原地往下走。不要把它挪进批次中间——会留下"副作用做了一半"的状态。

**6. 冷工具执行器恒绑。** `depsFor` 把蓝图视图写进签名参数（`deps.go:16-24`）：六个调用点少绑一个
是编译不过，而少记一处判据是"那个 Agent 的模型以为这个能力不存在"。

**7. 蓝图 fold 不许影响主流程。** 全程只 warn、不起 goroutine、不向 Repo 的返回值传播错误
（`blueprint_commit.go:24-30`；接缝 `repo.go:246-251`）。落盘只发生在这里——Director 侧不需要知道
`agent_exit.blueprint[]` 这个参数存在。

**8. 纯文本收尾的提醒有两条文案。** 通用那条催"去 explore/edit/run/verify"（执行型 Agent），
`cfg.NoActionReminder` 覆盖版给 Repo 的收尾契约（`executor_noaction_test.go:55,104` 钉住
"给 Repo 改文案不许顺手改掉所有子 Agent"）。对以报告为交付物的分析型 Agent，催它再执行一轮
等于把已做完的分析重做一遍（根 AGENTS.md §11 坑 41）。

## 子目录

**`activity/`** — 活动感知的 idle/total 监视器。**心跳只能续 idle，续不了 total**
（`totalElapsed` 取自创建时固定的 `startTime`，`internal/agents/activity/activity.go:249-251`），所以暂停必须显式 `Pause`；
`Resume` 必须把空闲基准推到恢复时刻（`:267-272`），否则第一个 tick 算出 `idleFor = 暂停时长`，
刚恢复的任务立刻被杀。`Pause`/`Resume` 必须沿 `parent` 链逐层（`:256-264`）——只冻最内层的话，
暂停超过外层阈值会杀掉整条委派链。`Resume` 在未暂停时是 no-op、不续 idle（`:274`，否则重复
Resume 成免费心跳）。监视 goroutine 挂到**父** ctx 的域上，不是本函数派生的那个
（`:175-177`，它会自己取消）。

**`toolbatch/`** — 纯调度器，不认工具名、不知道业务。**叶子包，不要往里加业务**。四条带牙齿的：
① `exec` 必须收到**传入的** ctx 而不是 supervise 域的 ctx（`internal/agents/toolbatch/run.go:63-66`）——域 ctx 是创建时刻
抓的祖先，看不见这里再派生的超时与取消，症状不是崩溃而是"用户取消了、工具还在跑"；
② 这一批的 WaitGroup 不许交给 `supervise.Drain`（`internal/agents/toolbatch/run.go:58-61`）——Drain 有预算，预算用尽就把
零值结果 commit 进对话，等于把超时伪装成"工具返回空"；③ 从未取得信号量的 goroutine 绝不做
`<-sem` 接收（`internal/agents/toolbatch/run.go:72-78` 的 `acquired` 标志），否则与别人的释放互相吃掉、死锁在 WaitGroup 上；
④ 取消不短路：已取消 ctx 的元素照样执行、照样 commit——不变量是"每个 tool_call 必有一条 tool
结果"，少一条就是 provider 400（`internal/agents/toolbatch/run.go:24-25`）。`onCommit` 在 join 之后、父线程单线程、按原序。

## 入口

`executor.go`（循环 + 两个判据）、`result.go`（交付分级，`FormatForDirector` 是账目渲染单源）、
`ports.go`（窄接口，**新依赖往这里加而不是往结构体加字段**）、`spawn.go` + `director_surface.go`。

## 闸门

- `TestRunAgentLoop_ReadOnlyBatch_ParallelEvidence`（`executor_parallel_test.go:197`）+ 对照臂
  `_SerialKillSwitch`（`:695`）：证明"峰值 ≥2"不是计量器自证。把谓词改回恒 false 立刻红。
- `TestParallelAdmission_*`（`parallel_admission_test.go:17,42,59`）：刻意不自己拼工具列表，直接拿
  **真实画像** `ProfileRepo/ProfileCoding/ProfileChat` 问判据；画像或能力标签漂走即红。
- e2e 五链路：`e2e_authorization_test.go:270`、`e2e_handoff_test.go:95`、
  `e2e_lifecycle_test.go:70`、`e2e_resume_test.go:29`、`e2e_replay_test.go:74`、
  `e2e_blueprint_test.go:127`（验收断言就是"文件出现"）。harness 的接线顺序照抄 `internal/app/app.go:115-287`。
- `TestSpawnForDelegation_CodingGetsItsOwnThinkLink`（`spawn_test.go:21`）：把 store 挂回单例即红。
- `TestStopKindClassifiersAgree`（`resume_test.go:175`）：本包 `classifyStop` 与
  `director.StopKindOf` 因分层守卫只能各写一份，靠这条对齐。
- `TestRunAgentLoopStampsDelegationOnEveryEvent`（`event_delegation_test.go:40`）：漏一个发布点就红。
- `TestAgentSurfacesGolden`（`registry_gate_test.go:48`）：工具面变化必须以显式 `-update` 重写 golden。
- `TestNoSubmissionNote*` + `TestE2EPlainTextRepoRunIsNudgedIntoSubmitting`：模型不调 `agent_exit`
  时必须留下一句可区分的话（根 AGENTS.md §11 坑 40）。

脚本守卫：`check_no_agents_import.sh` 管的是 `director/` 子包；本包受
`check_no_bare_go.sh` 与 `check_no_manual_lookup.sh`（FORBIDDEN 名单必须为零）约束。
测试侧：`TestMain` 用 `os.Setenv`（不是 `t.Setenv`）把 `$HOME` 隔离到临时目录，因为本包有
7 个 `t.Parallel()` 用例与 `t.Setenv` 互斥（`main_test.go:6-18`）。
