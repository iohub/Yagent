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
`repo.go:160`、`coding.go:367`、`chat.go:81`、`devops.go:90`、`browser_agent.go:124`，
消费点在 `executor.go:868`。漏设不报错：`agent_exit` 只是"又执行了一次工具"，循环继续烧到
MaxSteps，而 `deliverableText` 取的是最后一条 assistant 消息——便签正文与 artifact 全文存的
都不是那份交付（`repo.go:157-160` 把这段写成了注释）。

**3. 交付全文只经 `deliverableText()` 取**（`result.go:52-60`）。判据是返回 JSON 的**形态**
（`finished:true` + `reason` 键），不是 Agent 名字。`AgentResult.Text` 的既有语义没动。

**4. 每次委派新建实例，且 nil 判定必须在装箱之前。**
`SpawnForDelegation` 对 nil 接收者返回 nil 而不是 panic（`spawn.go:29-35`）；
`delegateTargets` 每个 spawn 必须先判**具体类型**的 nil 再返回 interface（`director_surface.go:36-43`）——
装着 nil 的 interface `!= nil`，`delegateTo` 那条"not wired"守卫于是永远不成立。

**5. 暂停闸门在 step 边界，且只有一处。** `executor.go:434-439`（`stepNumber++` 之前）：
ctx 不取消，恢复后原地往下走。不要把它挪进批次中间——会留下"副作用做了一半"的状态。

**6. 冷工具执行器恒绑。** `depsFor` 把蓝图视图写进签名参数（`deps.go:22`）：六个调用点少绑一个
是编译不过，而少记一处判据是"那个 Agent 的模型以为这个能力不存在"。

**7. 蓝图 fold 不许影响主流程。** 全程只 warn、不起 goroutine、不向 Repo 的返回值传播错误
（`blueprint_commit.go:24-30`；接缝 `repo.go:193-197`）。落盘只发生在这里——Director 侧不需要知道
`agent_exit.blueprint[]` 这个参数存在。

**8. 纯文本收尾的提醒有两条文案。** 通用那条催"去 explore/edit/run/verify"（执行型 Agent），
`cfg.NoActionReminder` 覆盖版给 Repo 的收尾契约（`executor_noaction_test.go:55,104` 钉住
"给 Repo 改文案不许顺手改掉所有子 Agent"）。对以报告为交付物的分析型 Agent，催它再执行一轮
等于把已做完的分析重做一遍（根 AGENTS.md §11 坑 40）。

**9. 委派那一层的顺序是硬契约：先取格子，再建 ctx 链，最后才套看门狗。**
`acquireDelegateSlot` 在 `context.WithCancel`（`director.go:828`）**之前**（`:821-826`）——放错顺序的
后果不是崩溃，而是两件很难查的事：排队时间被算进 `llm.timeout`（一次正常排队在真正发出去之前
就被判成"provider 慢"）、被算进 `activity` 的 idle 计时（一次"前面有人"的等待把子 Agent 的看门狗
顶成超时）。取不到格子只可能是 ctx 已经结束：交回 `ctx.Err()`，planner 把它写成一条 tool 结果，
"每个 tool_call 必有一条结果"照守；**从未取得的一格绝不做 `<-pool` 接收**（`concurrency.go:54-58`，
与别人的释放互相吃掉就是把整批卡在 WaitGroup 上）。委派分支**不**套 `context.WithTimeout`，
套的是 `activity.WithIdleTimeout`（`:840-843`，`[agent] delegate_idle_timeout` / `delegate_total_timeout`），
超时因此以 `IdleTimeoutError`/`TotalTimeoutError` 挂在错误链上，`classifyStop` 才分得出"超时"与
"被取消"（`resume.go:72-88`）。

**10. 委派池建在"这一轮 Run"的 toolRunner 上，不是进程级。** `newDirectorToolRunner` 是唯一构造方式
（`director.go:776-783`），漏了池子等于漏了 `[agent] max_parallel_delegations`。分成 per-Run 的理由
是嵌套委派（Repo→`delegate_devops`）用的是自己那一轮的池，"父占一格、子在同一格里排队"的死锁
形状从结构上不存在（`concurrency.go:14-16`）。取值约定 `NormalizeMaxParallelDelegations`
（`concurrency.go:29-43`：1 = 严格串行、≤0 = 默认 2、>8 钳到 8），急停 `YAGENT_CONCURRENCY=0` 的判据
写在**函数内部**而不是调用点——写两处的后果是一处忘，而漏掉那处的表现只是"还是并发"。

**11. `pause.AbandonedError` 的两半判定都要保住，方向不许互换。** 取消侧靠
`errors.Is(..., context.Canceled)`：`Unwrap` 把原因留在链上（`internal/pause/gate.go:56-57`），
本包的 cancelled 兜底（`resume.go:85-86`）才认得出它——换成孤立新错误的表现是"取消了但没人认为
是取消"，Director 把它当成一次失败的委派继续往下跑。停住侧靠 `errors.As`：`classifyStop` 归
`paused`（`resume.go:81-84`）、`writeDelegateNote` 对 abandoned **什么都不写**（`director.go:572-574`，
为什么不写见 `internal/blackboard/AGENTS.md` 硬约束 3）。两侧判据落在"停在哪"这个事实上：闸门
只在 step 边界生效，所以那一轮的工具批次已全部提交完，恢复侧据此决定能不能原样继续。

**12. 检查点只在循环的 deferred 终止块里写一条。** `writeExecutorCheckpoint`
（`executor.go:403`，实现 `checkpoint.go:23`）挂在终止分支上、仅在有终止错误时调用，不是每步一条：
每步一条会把 150 步的会话写成 150 条坐标，而重放永远只看最后一个停点。写与读共用
`memory.WriteCheckpoint`——父子两个写入包不能互相 import，抄一份必然分叉（Director 侧同规矩，
见 `director/AGENTS.md` 硬约束 6）。想加"每步检查点"之前先想清楚恢复侧怎么选。

**13. 降级交回的是"报告 + 理由清单"，不是 error。** `resumeDelegation` 的签名就是
`(string, []string)`（`resume_delegation.go:209`），`assessReplay`（`:105`）命中判据时走
`degradedHandoff`；把它写成 error 的表现是 Director 以为"这次委派失败了"，于是把已经做完的工作
重做一遍。理由必须逐条显示出来——`DelegationStop.Reasons`（`:44-47`）存在的理由就是"不能继续
而不说原因是另一种静默"。重放成功那一侧交回的是**同一个 `call_id` 的第二条 tool output**
（last-wins，见 `director/AGENTS.md` 硬约束 4），模型看到的是"那次委派成功返回了"。
重放接缝只有三个回填点，少任何一个都不报错：`ScanDelegationStops`（本包 `resume_delegation.go:53`，
**不在 `memory`**）按 `delegation_id` 反查父子停点、`ResumeState.StartStep`（`resume_state.go:29-30`，
消费点 `executor.go:286-290`）是循环起点（漏接 = 游标算错一步，重复或漏掉一次副作用）、
`Surface.RestoreMaterialized`（`executor.go:291-293` → `internal/tools/surface.go:468`）回填已取回的
冷工具（漏接 = 模型在恢复后的会话里重新 `load_tool` 一遍）。

**14. 把交付押在 `agent_exit` 的可选参数上，就必须同时回答"模型不调它会发生什么"。**
`handoff` 与 `blueprint[]` 都只在那一刻被采集，三件配套缺一不可：`repo.prompt.md:14-16` 的 Output
Contract（答案写完、同一轮调 `agent_exit`）、循环补的一次收尾提醒（`repo.go:171` 置
`NoActionRetryLimit = 1` + 覆盖文案，消费点 `executor.go:794`）、零提交留下一句可区分的账
（`blueprint_commit.go:55-58` 的 `noSubmissionNote` 与 `:61-66` 的"本次 run 不是 agent_exit 收尾"）。
缺任何一件的症状都是"目录建了、文件没有、不报错"，与"蓝图从来没写过"逐字相同，差别只在日志里
那两行 WARN。变异：`NoActionRetryLimit` 调回 0 → 蓝图当场不落盘。

**15. 蓝图的账目必须从返回值离开本包，两处出口、不进 conclusion。** `repo.go:202` 把 `Commit`
返回的那行账目写进 `AgentResult.BlueprintAccount`（`types.go:30-35`）而**不是** `Handoff`；两处出口
= `FormatForDirector`（`result.go:220,230` → `appendBlueprintAccount:234-243`，落在交付正文之外）与
便签正文的 `> [blueprint]` 尾注（`director.go:590-593`）。只修一边，另一边就是新的静默：工具输出
会被上下文压缩卷走，而便签正文不进每轮重扫的索引。混进 `conclusion` 等于替模型下结论。
`blueprintAccount`（`blueprint_commit.go:126-131`）只搬 fold 已做出的判据、不重复任何校验。

**16. 证据集 = 本次 run 真打开过的文件，入参侧 ∪ 出参侧，判不了就少收。**
`evidenceFiles`（`blueprint_evidence.go:81-105`）先收工具调用参数里的路径，再按
`toolspec.ResultPathsOf` 分派收工具结果里的路径（`:93-102`）——出参侧的形态问注册表、不 switch
工具名，`list_dir`/`print_dir_tree` 的裸文件名必须排除（收进来会映射到错误的片：宁可少一块证据，
不要一条假证据）。`pathsFromJSON` 解析失败返回 nil，容错方向永远是"少收"（`:109-113`）。
只有以 `agent_exit` 收尾的 run 才有交付物可作证据（`blueprint_commit.go:61-66`）。

**17. `sub_agent_activity` 必须在每个有工具调用的 step 落 rollout。** `executor.go:917-919` 是读侧
推 `EffectiveSteps` 的唯一凭据；它曾挂在已删除的 `OnStepEnd` 钩子里，于是**只有"Coding + git
checkpoint"那一条路径**写步数事实，其余子 Agent 的 `/resume` 步数只能靠 assistant 轮次猜。
`ExecutorConfig` 里没有 `OnStepEnd`，不要加回来（那个钩子剩下的唯一作用是每步打一条 deprecation
警告）。

**18. `ExecutorConfig` 的按-Agent 标志没有中心默认，漏一个就是那条路径静默少一套行为。**
`StepRetries` 由各子 Agent 自己从 `[llm] step_retries` 带进来（`coding.go:367`、`repo.go:158`、
`chat.go:82`、`devops.go:91`、`browser_agent.go:125`；Meta 走自己的字段，`meta.go:34,74`），漏设的
那个 Agent 恒 0 步级重试且不报错。`NoActionRetryLimit` 同理（历史上只有 Coding 设 2，
`coding.go:372`；Repo 现在设 1）。同类先例见硬约束 2（`StopOnFinish`）。

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

**19. 局部文档注入的判据是磁盘，不是路径形状**（`module_docs.go`）。`LocalDocsForTask` 只认
"这个目录真的有一份 `AGENTS.md`"，祖先链最深优先、最多 3 份 / 12 KB，超预算必须留下可见的省略行
（静默少一份文档 = 子 Agent 以为自己没见过约束，与 §11 坑 43 同族）。三条不许改：工作区根那一份
**永不**注入（已在系统上下文里，重复是纯浪费）；正文每次现读、不缓存（缓存它就得再发明失效判据，
同 `blueprint_view.go:123-125`）；输出顺序按 (深度 desc, 字典序) 稳定（前缀缓存靠它）。
软链出工作区的文档由 `EvalSymlinks` + 前缀包含判据挡掉，这层守卫在**读取侧**而不是 token 侧——
token 侧的 `..`/绝对路径拒绝只是第二层。闸门：`module_docs_test.go` 七条
（`_MatchesClosedSet` / `_NearestWinsAndNeverCarriesRoot` / `_BudgetTruncationIsVisible` /
`_RejectsPathEscape` / `_SkipsSymlinkOutsideWorkspace` / `_NeverCachesBody` /
`TestPackDelegateTask_CarriesModuleDocs`）——删掉 `packDelegateTask` 里那一行调用、把预算判定改成
恒真、排序改成浅先、或去掉软链包含检查，各自立刻红。

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
  `e2e_blueprint_test.go:127`（验收断言就是"文件出现"）。harness 的接线顺序照抄 `internal/app/app.go:112-292`。
- 那五条链路共用的模型替身是 `agentScriptedEngine`（`e2e_engine_test.go:23`）：脚本按 **Agent 身份**
  分派（`agentIdentity`，`:41`，靠各家 System Prompt 的首行措辞认人）而不是全局调用序号——序号会被
  循环里的补偿动作打断；`GenerateContent` **必须先看 ctx**（`:66-67`），忽略 ctx 的 mock 会把
  "取消没传播"这类缺陷直接掩盖掉。
- **取消不许从批次内部发起**：`toolbatch` 不保证组内启动顺序，负责取消的那个工具一旦排在信号量队列里，
  在飞调用就无人可取消，只能等 `defaultToolTimeout`(180s，`executor.go:90`) 逐波解开。历史用例
  `TestRunAgentLoop_ParentCancel_AllCallsAccounted`（`executor_parallel_test.go:514`）就卡在这里，
  改法是 `time.AfterFunc` 从批次外部取消（`:531`，理由写在 `:528-530` 的注释里）。
- 超时类用例走 `restoreToolTimeout(t, …)` 这个 seam（`executor_test.go:59-66`，临时改短
  `defaultToolTimeout`），`TestRunAgentLoop_MultipleToolCalls`（`executor_test.go:232`，用法 `:234`）
  是样例；不许用 `time.Sleep` 等真实超时。
- `TestSpawnForDelegation_CodingGetsItsOwnThinkLink`（`spawn_test.go:21`）：把 store 挂回单例即红。
- `TestStopKindClassifiersAgree`（`resume_test.go:175`）：本包 `classifyStop` 与
  `director.StopKindOf` 因分层守卫只能各写一份，靠这条对齐。
- `TestRunAgentLoopStampsDelegationOnEveryEvent`（`event_delegation_test.go:40`）：漏一个发布点就红。
- `TestAgentSurfacesGolden`（`registry_gate_test.go:48`）：工具面变化必须以显式 `-update` 重写 golden。
- `TestNoSubmissionNote*`（`blueprint_commit_test.go:34,52,69`）+
  `TestE2EPlainTextRepoRunIsNudgedIntoSubmitting`（`e2e_blueprint_test.go:332`）：模型不调 `agent_exit`
  时必须留下一句可区分的话（根 AGENTS.md §11 坑 40）。
- `TestDelegatePoolCapsConcurrentDelegations`（`delegate_concurrency_test.go:128`）：池子漏建即红。
  `TestKillSwitchReachesDirectorAgentPool`（`:222`）与
  `TestConcurrencyKillSwitchForcesSerialDelegations`（`:160`）：`YAGENT_CONCURRENCY=0` 没到达委派侧即红。
- `TestE2E_DelegateTotalTimeoutAlsoFires`（`e2e_lifecycle_test.go:184`）：`delegate_total_timeout`
  真的收口"每轮都成功但不肯收尾"的委派（idle 刻意设大以排除 idle 分支），并留下一张便签。
- `TestRunAgentLoop_AbandonedPauseReportsPaused`（`executor_stopkind_test.go:371`）：把
  `AbandonedError` Unwrap 成裸取消（记成 cancelled）即红。
  `TestAbandonedDelegationWritesNoNote`（`resume_degrade_test.go:444`）钉另一侧：停住不等于失败。
- `TestReplayFailureDegradesInsteadOfErroring`（`resume_degrade_test.go:282`）：降级写成 error 即红；
  `TestDegradedReportSectionsComeFromTheLedger`（`:255`）、
  `TestDegradedHandoffWritesDegradedNote`（`:301`）。
- `TestEvidenceSetIncludesSearchResults` / `_ReadsAllPathParams` /
  `_IgnoresNonPathToolOutput`（`blueprint_evidence_test.go:50,77,140`）：证据集口径；
  `TestEvidenceCollectedOnlyForRealSubmissions`（`:116`）：非 `agent_exit` 收尾还去采集证据即红。
- 步数事实的读侧闸门在**另一个包**：`TestReadRolloutMemory_StepAndTurnCount`
  （`internal/memory/rollout_reader_test.go:946`）。本包没有测试钉住"每个有工具调用的 step 都写
  `sub_agent_activity`"——这是当前覆盖缺口，改 `executor.go:917-919` 之前先补一条。

脚本守卫：`check_no_agents_import.sh` 管的是 `director/` 子包；本包受
`check_no_bare_go.sh` 与 `check_no_manual_lookup.sh`（FORBIDDEN 名单必须为零）约束。
测试侧：`TestMain` 用 `os.Setenv`（不是 `t.Setenv`）把 `$HOME` 隔离到临时目录，因为本包有
7 个 `t.Parallel()` 用例与 `t.Setenv` 互斥（`main_test.go:6-18`）。
