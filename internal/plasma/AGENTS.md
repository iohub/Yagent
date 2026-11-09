# internal/plasma — 生成式 Agent 的编译器与 IR

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md`；设计稿 `docs/plasma-architecture.md`。
> 模块结构与文件计数由 `internal/blueprint` 每轮派生，不要往这里抄。

## 边界

判定"一份 bundle 能不能被信任地装配成 Agent"，并把最小 spec 扩充成完整 IR。
**不负责**：执行 Agent（`agents.RunAgentLoop`）、调 LLM（`llm`）、装配工具面（`tools.Surface`）、
读配置（`config`）、子进程 spawn（`agents`/`app` 侧）、落盘存储与加载（P4 才进本包）。

它是**裁决者**：裁决者认识被裁决者的执行细节，就没有独立判据可言。所以执行能力全部经窄接口注入。

## 硬约束

**1. 叶子包**，由 `scripts/check_plasma_leaf.sh` 守：只准标准库 + `toolspec` + `util` +
`projectid` + `tokenutil` + `thinklink`。需要的外部能力一律作为值或窄接口传进来 ——
`Compiler.Palette`（生产注入 `PlasmaPalette`）、`Defaults`（配置值）、
`FixtureRunner`（P6，夹具执行）、`Spawner`（P8，trampoline）。
**不要 import agents / tools / llm / config / messaging。**
`tokenutil` 与 `thinklink` 进白名单是为了**复用判据而不是重写**：预算必须与压缩阈值同一个计数器，
`<thought>` 契约必须走 `ThoughtPlan.Validate`。两者都已实测是叶子包。

**2. `Palette` 刻意不给默认值。** 零值 palette 会拒掉一切工具（`TestCompile_ZeroPaletteRejectsEverything`）
—— "忘配 palette"必须是全拒而不是全放行。生产由装配层注入 `PlasmaPalette`（`palette.go`）。
`EscalationApproved` 是用户已批准的额外位，effective = `Palette | EscalationApproved`；
**批准必须按 bundle hash 注入**，按 name 记会让一次批准被后续所有变异继承。

**2.1 `PlasmaPalette` 不等于 `ProfileDirector.Palette`。** 后者含 FSWrite|Shell（`profiles.go:33`），
生成式 Agent 继承它就是权限升级。前者只读族 + flow + tool_load，且**第三方能力一位不给**
（ExternalRead/ExternalWrite/Skill）——`internal/toolspec/AGENTS.md` 硬约束 6.1 明写
"Palette 一个位都不加"，而第三方 MCP 输出本身就是注入向量。含 `EffectToolLoad` 是**修 bug**：
Director 的 Palette 缺它，于是今天 Custom Agent 的 Cold 工具永远取不回。
**广告侧与准入侧必须同用这一个常量**：`agents.renderCustomAgentToolPool`（`deps.go`）与
`agents.runCustomAgent`（`director_surface.go`）都改了，只收窄一边等于没收窄
（闸门 `TestMetaPromptToolPoolMatchesPalette` 同时断言两侧）。

**2.2 升级账目必须覆盖被拒的工具。** `escalation()` 合并 selected **和 rejected** 的能力位：
漏掉 rejected 的话，"需要升级"这条 warning 就永远不会出现在真正越权的那份 bundle 上，
批准门也无从问起（P1 实施时实测踩到，`TestEscalation_UnapprovedIsRejectedButAccounted` 钉住）。
PLS-210 是 **warning 不是 error**：它是账目，阻断由 PLS-201 负责。已批准也照样发 PLS-210
（坑 43：防线生效过必须有人看得见），note 文案区分已批准/未批准。

**3. 工具名解析与 `agent_exit` 自动追加只有一份判据：`toolspec.MatchNames`**
（`internal/toolspec/profile.go:174`，`:196` 自动追加）。本包不重写这两条规则。
`MatchNames` 忽略 deps（`:175` 的 `_ = deps`），所以传 nil 是安全的。

**4. 能力名 ↔ Effect 位的转换只有一张表**：`toolspec.Effect.Names()` / `EffectFromNames()`
（`internal/toolspec/effect.go`）。`policy.deny` 用能力名，认不出的名字必须报 `PLS-207`
而不是静默丢掉 —— 拼错一个名字会让 deny 少一项且毫无症状。

**5. 诊断按发射顺序渲染，不排序**（`diag.go` 的 `Report.Text`）：排序会抹掉"先解析错还是先越权"
这条因果线索，而生成侧靠它决定先修哪一处。发射路径上**一律不遍历 map**，否则顺序不确定、golden
每次启动都抖。`Path` 必须指向**原始** `tools` 的下标（`indexOfTool`），不能拿 `MatchNames` 去重/
追加后的结果下标 —— 那会让诊断指向生成侧没写过的位置。

**6. 一次跑完全部阶段，不遇错即停。** 生成侧要一轮拿到所有问题，否则每修一处就是一次 trampoline 往返
（~300ms）。`TestCorpusCoversEveryCode` 的 `multiple-errors-in-one-pass` 钉这条。

**7. 编译失败必须清空 `Report.Grade`**（`compile.go`）：留着 `provisional` 会让调用方以为可以装配；更高的档必须有夹具证据。

**8. `Defaults` 的每个值都必须真的被用到**（根 `AGENTS.md` §11 坑 35）。
`TestElaborate_DefaultsAreActuallyUsed` 逐个字段对账，并且**覆盖值必须生效** ——
否则"注入 Defaults"这条通道是装饰。兜底常数只在 `DefaultDefaults()` 一处。

**9. `TaskForAgent` 与 `thinking` 不进落盘 IR**（`ir.go` 的 `json:"-"`）。前者是"这次要干什么"，
属于本次执行；后者是设计过程。`TestElaborate_TaskIsNotPersisted` 钉住。

**10. 扩充必须幂等**：`Elaborate(Marshal(Elaborate(x)))` 与 `Elaborate(x)` 逐字节相同
（`TestElaborate_Idempotent`）。不幂等的话每次读写 store 都产出一整片 diff，而 bundle 的主键是 hash。

**11. `SnakeCase` 单源在 `internal/util/snakecase.go`。** 它原先是 `agents.toSnakeCase`（只有一个
生产调用点），P0 移到 util 供两侧共用；各写一份就是"两份同形实现各自漂走"。空结果回落 `custom_agent`。

**12. 阶段 4 全是 warning，不阻断**（`consistency.go`）。它指出"会静默坏掉"的形状，
但不足以断定 bundle 不可用；阻断由阶段 1-3 的 error 负责。warning 经 `Report.Warnings()`
交给 Emit（P4）写进 `quality.lint`，作为进化适应度的一项。

**13. 按形状识别的规则必须放过同形正常散文**（根 `AGENTS.md` §11 坑 42）。
PLS-302 的判据刻意收窄到"同一行里反引号工具名 + 反引号 token，且那个 token **是别的注册工具的
参数名**"——最后这一条把普通散文里的反引号词挡在外面。
`TestConsistency_ParamMismatchSparesNormalProse` 用 `` `注入风险` `` 作同形反例，删掉那个
"是别的工具参数"的条件它就红（参数名实测：`read_file`/`create_file` 用 `target_file`，
`search_replace_in_file` 用 `file_path` —— **不是** `path`）。

**14. token 预算必须说出降级状态。** `tokenutil.IsFallback()`（P2）报告本进程是否已退回 `len/4`。
降级是一次性粘滞的、对中文约 4 倍低估，所以 PLS-304 的 note 必须把这件事说出来，否则读诊断的人
会以为那是精确 token 数。`PromptTokenBudget=8000` 留余量也是为它。

**15. store 的不可变/可变必须分文件**（`store.go`）。`bundle.json` 与 `fixtures/` 一经落盘即不可变
（主键 = sha256，`Put` 遇到已存在的 `{name,hash}` 直接返回、不覆盖）；`report.json` 与 `meta.json`
是可变状态（grade 升降、`escalation_approved`、promoted 计数）。把可变状态写进 bundle 会改 hash。
**改夹具 ⇒ 新 hash ⇒ 新目录 ⇒ grade 从 provisional 重来**，不继承 —— 夹具不可变 + grade 不继承的机械保证。

**16. `Runtime.LLMTimeout` 是 `json:"-"`，每次从磁盘读回都必须 `normalizeRuntime()`。**
落盘用可读的 `LLMTimeoutText`，内存里用 `time.Duration`。P4 实测踩到：漏还原时 `Load` 得到 0s，
阶段 3 判 `PLS-204 越界`，所有已落地的 Agent 全部加载失败。`Put` 也会先把 duration 写成文本
（否则落盘文件不自描述）。

**17. 出题谱系分离在 store 层强制**（`checkFixtureProvenance`）：`fixtures.provenance.lineage` 为空
→ 拒收（无法证明分离）；等于 `lineage.mutation` → 拒收（自己出题自己考）。Goodhart 的第一道防线。

**18. 加载期必须对活注册表重跑阶段 1-4**（`Link`）：hash 只保证"文件没被改"，不保证"依赖的工具还在"。
`LoadAll` 按 name **字典序**加载（前缀缓存），预算截断按 grade→score→name 选留、但**呈现顺序仍回字典序**；
超预算返回 `omitted` 条数。`Skipped` 绝不静默：Agent 从工具面消失而没人知道原因，与它从没存在过无法区分（坑 43）。

**19. 遥测写入点是 `directorToolRunner.Call` 的 outcome 汇聚处**，不是 `applyEnhancedCommander`
（那只在 builtin 分支跑）。本包只提供 `AppendTelemetry`（一行 JSONL，失败返回 error 但调用方
不得因此阻断委派）；`agents` 侧用 `SetDelegationMetricsHook` 暴露接缝，app 在 P9 接线。

**20. 结构夹具**自动生成**只在为空时**（`ensureStructuralFixtures`，`Compile` 末尾）：作者提供的
夹具一条都不覆盖（ADR-5 夹具不可变）。生成时必须同时补 `fixtures.provenance`
（`plasma-compiler-auto` / `compiler`），否则"有夹具而无谱系"会被 `store.Put` 按「自己出题自己考」拒收。

**21. `Outcome` 里不许出现 `llm.*`**（叶子约束的推论）：脚本用 IR 自己的 `ScriptStep` 表达，
结果只留 `FixtureID/Passed/Reason/Steps/TokensIn/TokensOut/StopKind`。`Reason` 在
`Passed=false` 时**必须**非空且可读：生成侧靠它改下一轮，一句 "failed" 等于没跑。

**22. `FixtureRunner` 由 `agents` 实现，plasma 只声明**（`fixture.go`）：静态断言经 `surfaceFor`
装配真实 Surface，行为夹具经 `scripted` + `RunAgentLoop` 在生产路径上回放。plasma 侧不得
为了省事 import tools/llm；`LinkPlan.Profile` 必须带 `Names`（含自动追加的 `agent_exit`）
与 `Palette`（`effective()`），否则夹具与 typecheck 会各算一套能力数学。`Spawner`（P8，
`workdir.go`）是同一取向：agents 侧 `spawner.go` 实现 `os/exec`，plasma 只声明接口。

**23. exit-contract 必须比对交付**内容**。** `deliverableText` 倒序找最后一条非空 assistant
消息，找不到就回落 `exec.Text`（那段 `{"finished":true,…}` JSON）—— 只断言"非空"永远为真。
脚本形状只能是**一条响应里同时带正文与 `agent_exit`**：循环在没有 `tool_calls` 时直接按
纯文本收尾，"先文本、后退出"的两步脚本走不到第二步（5a 实施时实测踩到）。

**24. 5xx 每个码一条 corpus 负样本**（`PLS-501`–`505`）：5xx 只在验证阶段产生，`corpusCase.verify`
是唯一通道，`TestCorpusCoversEveryCode` 同时扫编译与验证两侧。步数判据用 `stepsDone` 而不是
`exec.Steps`：预算耗尽那条出口的 result 是零值，直接读它会让"步数超限"永远为假 —— 又一条永真测试。

**25. `Manifest` 是 Python 侧唯一的只读能力/码表来源，不许另写第二份码表。** 工具集合的判据与
`agents.renderCustomAgentToolPool` 同一句 `Effect.Has(Palette)`；`Codes` 来自 `DiagCatalog()`
（唯一来源，`TestDiagCatalogMatchesCorpus` 双向对账 corpus）；`CapabilityLock` 是整份 manifest 的 sha256。

**26. workdir 文件名与退出码是跨语言契约**（`workdir.go`）。文件名与 `exit 0/1/2/3/4` 改一处
必须同时改 Python zipapp，而两侧不同步的表现是"子进程 exit 0 但 Go 找不到产物"。`llm_req`/
`llm_resp` 按**调用下标**对齐（`%03d`，从 000 起），不靠时间戳、不扫目录；`exit 4` 是"我需要
一次采样"的信号，`Spawner` 实现必须返回 `(4, nil)` —— 当失败处理会把正常往返判成一次错误。

## 测试必须与生产同形（坑 39）

`toolspec` 的注册表由**实现侧的 `init()`** populate：只 import `tools` 得到 37 条，
再加 `agents` 才是生产的 50 条。所以测试用**外部测试包** `package plasma_test` 并 blank-import
两者（`main_test.go`），`TestRegistryIsProductionShaped` 钉住"50 条 + `Validate()` 干净"。

少 import 一个的后果不是编译错误，而是 did-you-mean 与能力派生对着一个生产不存在的注册表算。
`go list -deps` 不含测试 import，所以这不会打穿叶子守卫（已实测）。
外部测试包可以 import 依赖 plasma 的包，因此 P9 之后 `agents → plasma` 也不成环。

## 入口

`ir.go`（Bundle 与三档 Grade）、`diag.go`（码表 + Report）、`elaborate.go`（两种形态 → 完整 IR）、
`compile.go`（阶段 1-3）、`fixture.go`（5a 夹具 + `VerifyStructural` + 棘轮）、`manifest.go`（能力/码表）、
`workdir.go`（P8 的 workdir 契约 + `Spawner` + 孤儿 GC）、`suggest.go`（did-you-mean）。

## 闸门

- `TestCompile_ReportsEachRule`（`compile_test.go`）：13 个码各一条负样本，**且断言精确码集**
  —— 后半句防止规则互相误响。删任一判定即红（已实测 6 条变异全红）。
- `TestCompile_ZeroPaletteRejectsEverything`：忘配 palette 变成全放行即红。
- `TestCompile_FailureClearsGrade` / `_CleanBundleIsProvisional`：grade 语义。
- `TestCompile_ExitIsAutoAppended`：有人在本包重写自动追加规则即红。
- `TestCompile_UnknownToolSuggestion`：建议必须指向真实工具，且两次渲染逐字相同。
- `TestCompile_DiagnosticPointsAtOriginalIndex`：下标改用 MatchNames 结果即红（夹具有重复项）。
- `TestElaborate_DefaultsAreActuallyUsed`：兜底常数没生效、或注入值被忽略，即红。
- `TestElaborate_RejectsBadShapes`：四种坏输入各自必须报出**结构化** `*DiagError`。
- `TestElaborate_Idempotent` / `_TaskIsNotPersisted`：见硬约束 9/10。
- `TestDiagnosticsGolden`（`golden_test.go`）：`testdata/diagnostics.golden` 钉住 17 个 corpus
  的精确诊断文本。改码表/文案必须显式 `-update`，那个 diff 就是评审物。
- `TestCorpusCoversEveryCode`：corpus 没触发某个码即红（防止 golden 看着很长、实际有规则从未被渲染）。
- `TestRegistryIsProductionShaped`（`main_test.go`）：注册表不是 50 条即红。
- `fixture_test.go` 十条：四条夹具的 ID/形状/步数/幂等、作者夹具不被覆盖、改夹具换 hash 且
  store 接受自动谱系、`Ratchet` 四臂 + `CanPromote` 边界、5 个 5xx 码各一条、以及"缺 runner /
  缺 plan / runner 报错 / 失败不给 Reason"四种必须点名的失败（`Reason` 非空是硬约束 21）。
- agents 侧 `TestPlasmaFixtureRunner_*`：四夹具**各一绿一红**（负样本证明判据会失败，见硬约束 23）、
  5b 占位不白送通过、`stop_kind` 词表对账、端到端 `VerifyStructural` + `Ratchet` 停在 provisional。
- `palette_test.go` 七条：`TestPlasmaPaletteIsReadOnly`（11 项禁用能力逐个断言 + flow/tool_load 必须在）、
  `_UnapprovedIsRejectedButAccounted`（PLS-201 与 PLS-210 必须同时在场）、
  `_ApprovedPassesButStillAccounted`（批准后仍留账目）、`_ApprovalIsPerBit`（只批 fs_write 时 shell 仍被拒）、
  `_ReadOnlyBundleHasNoEscalation`（只读 bundle 不该带噪声 warning）、
  `_DeclaredIntentIsAccounted`（光声明意图也要账目）、`TestBeyondBase`。
- agents 侧配套：`TestMetaPromptToolPoolMatchesPalette`（`registry_gate_test.go`）同时断言
  **广告与准入**两侧都用 PlasmaPalette，且 run_bash/create_file 两侧都拒、load_tool 必须在。
- `workdir_test.go` + agents 侧 `trampoline_test.go`（P8）：契约文件名与 `%03d` 序号、diagnostics.json
  小写键、0700/唯一目录、`Cleanup` 幂等、GC 只删过期 `plasma-*`；trampoline 五条 = exit 4 序号对齐 /
  `diagnostics → --fix` / kill 后超时+workdir 清理 / `max_llm_calls` 上限 / `ai_chunk` 来自 Go 侧。

脚本守卫：`check_plasma_leaf.sh`（硬约束 1）。
`testdata/` 落地后要跑 `gofmt -l internal/plasma/testdata`（根 `AGENTS.md` §11 坑 41：
`go build/vet/test` 都不看它）。

## 尚未落地（别当成已有能力引用）

5a 已落地：`StructuralFixtures` + `VerifyStructural` + `Ratchet`；`promoted` 只由 `CanPromote` 判"够不够"，
写回 store 归 P9。**5b 仍是占位**（`RunBehavioral` 恒返回"未执行"，属 P11）。**P8 的 trampoline 驱动器 +
`Spawner` 已落地但未接线**（`agents/trampoline.go`/`spawner.go`，接进 `runMetaDesign` 是 P9）；CLI 已落地（P7）。
升级批准的用户回路仍等 P9 接线。
