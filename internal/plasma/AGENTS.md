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
`projectid` + `tokenutil`。需要的外部能力一律作为值或窄接口传进来 ——
`Compiler.Palette`（P1 起是 `PlasmaPalette`）、`Defaults`（配置值）、
`FixtureRunner`（P6，夹具执行）、`Spawner`（P8，trampoline）。
**不要 import agents / tools / llm / config / messaging。**

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

**5. 诊断按发射顺序渲染，不排序**（`diag.go` 的 `Report.Text`）。排序会抹掉"先解析错还是先越权"
这条因果线索，而生成侧靠它决定先修哪一处。发射路径上**一律不遍历 map**，否则顺序不确定、
golden 每次启动都抖。`Path` 必须指向**原始** `tools` 的下标（`indexOfTool`），
不能拿 `MatchNames` 去重/追加后的结果下标 —— 那会让诊断指向生成侧没写过的位置。

**6. 一次跑完全部阶段，不遇错即停。** 生成侧需要一轮拿到所有问题，否则每修一处就要一次
trampoline 往返（~300ms）。`TestCorpusCoversEveryCode` 的 `multiple-errors-in-one-pass` 钉这条。

**7. 编译失败必须清空 `Report.Grade`**（`compile.go`）。留着 `provisional` 会让调用方以为可以装配。
P0 只可能产出 `provisional`（5a/5b 尚未接入），更高的档必须有夹具证据。

**8. `Defaults` 的每个值都必须真的被用到**（根 `AGENTS.md` §11 坑 35）。
`TestElaborate_DefaultsAreActuallyUsed` 逐个字段对账，并且**覆盖值必须生效** ——
否则"注入 Defaults"这条通道是装饰。兜底常数只在 `DefaultDefaults()` 一处。

**9. `TaskForAgent` 与 `thinking` 不进落盘 IR**（`ir.go` 的 `json:"-"`）。前者是"这次要干什么"，
属于本次执行；后者是设计过程。`TestElaborate_TaskIsNotPersisted` 钉住。

**10. 扩充必须幂等**：`Elaborate(Marshal(Elaborate(x)))` 与 `Elaborate(x)` 逐字节相同
（`TestElaborate_Idempotent`）。不幂等的话每次读写 store 都产出一整片 diff，
而 bundle 的主键是它的 hash —— diff 就是换了一个 Agent。

**11. `SnakeCase` 单源在 `internal/util/snakecase.go`。** 它原先是 `agents.toSnakeCase`
（只有一个生产调用点），P0 把它移到 util 供两侧共用；各写一份就是"两份同形实现各自漂走"。
空结果回落 `custom_agent`，保证调用方永远拿到合法标识符。

## 测试必须与生产同形（坑 39）

`toolspec` 的注册表由**实现侧的 `init()`** populate：只 import `tools` 得到 37 条，
再加 `agents` 才是生产的 50 条。所以测试用**外部测试包** `package plasma_test` 并 blank-import
两者（`main_test.go`），`TestRegistryIsProductionShaped` 钉住"50 条 + `Validate()` 干净"。

少 import 一个的后果不是编译错误，而是 did-you-mean 与能力派生对着一个生产不存在的注册表算。
`go list -deps` 不含测试 import，所以这不会打穿叶子守卫（已实测）。
外部测试包可以 import 依赖 plasma 的包，因此 P9 之后 `agents → plasma` 也不成环。

## 入口

`ir.go`（Bundle 与三档 Grade）、`diag.go`（码表 + Report）、`elaborate.go`（两种形态 → 完整 IR）、
`compile.go`（阶段 1-3）、`suggest.go`（did-you-mean）。

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
- `palette_test.go` 七条：`TestPlasmaPaletteIsReadOnly`（11 项禁用能力逐个断言 + flow/tool_load 必须在）、
  `_UnapprovedIsRejectedButAccounted`（PLS-201 与 PLS-210 必须同时在场）、
  `_ApprovedPassesButStillAccounted`（批准后仍留账目）、`_ApprovalIsPerBit`（只批 fs_write 时 shell 仍被拒）、
  `_ReadOnlyBundleHasNoEscalation`（只读 bundle 不该带噪声 warning）、
  `_DeclaredIntentIsAccounted`（光声明意图也要账目）、`TestBeyondBase`。
- agents 侧配套：`TestMetaPromptToolPoolMatchesPalette`（`registry_gate_test.go`）同时断言
  **广告与准入**两侧都用 PlasmaPalette，且 run_bash/create_file 两侧都拒、load_tool 必须在。

脚本守卫：`check_plasma_leaf.sh`（硬约束 1）。
`testdata/` 落地后要跑 `gofmt -l internal/plasma/testdata`（根 `AGENTS.md` §11 坑 41：
`go build/vet/test` 都不看它）。

## 尚未落地（别当成已有能力引用）

阶段 4 Consistency（P2）、store 与加载期（P4）、`FixtureRunner` 与 5a 自动生成（P6）、
trampoline 驱动器与 `Spawner`（P8）。
`ir.go` 里的 `Fixtures`/`Quality` 只承载形状，**没有任何执行或落盘语义**。
`Report.EscalationRequired` 已置位但**还没有消费者**：grade 上限的真正执行要等 P6 的棘轮，
升级批准的用户回路要等 P9 接线。
