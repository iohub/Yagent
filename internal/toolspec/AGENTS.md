# internal/toolspec — 工具元数据的唯一声明面

> 范围：本目录内不可派生的约束。（原 `docs/tool-registry-design.md` 已不存在，
> 注册表与画像的规格由本文件持有。）全局摘要见仓库根 `AGENTS.md` §5.1 与 §9.3。

## 边界

每个工具的 `Spec`（能力标签、频率档、授权要求、执行器构造）、`schemas.json` 的 JSON Schema、
以及 Agent 侧的**能力画像**（`profiles.go`）。不执行工具、不认识任何 Agent 实现。

## 硬约束

**1. 叶子包：只准标准库**，由 `scripts/check_toolspec_leaf.sh:9,29-32` 守。
新能力必须做成 `Deps` 的窄接口字段、由装配层注入——本包重新声明接口而不 import `agents` 的理由
写在 `deps.go:8-11`。

**2. 声明写在该工具实现同一个文件的 `init()`**（`spec.go:13`）。重名 `Register` 会被拒绝且**保留先
注册者**（`:87-93`）。`schemas.json` ↔ `Spec` 必须双向 1:1（`:163-166,216-221`）；
条数基线**硬写 50**，不许换成 `len(...)` 自比较（`registry_test.go` 的 `TestSchemasLoadAndDescribeEveryEntry`）——
自比较的断言永远不会失败，加工具漏改数字必须红。

**3. 每条校验规则都必须有能触发它的夹具。** 曾经那条"Serial 与 `IsReadOnly` 冲突"在数学上不可能成立
（`IsReadOnly()` 的定义含 `!Serial`），它既不会响、又与 `Serial` 字段的文档承诺矛盾，已删除
（`spec.go:208-213`，根 AGENTS.md §11 坑 15）。删规则不会有人发现，所以
`TestValidate_ReportsEachRule` 是这条纪律本身。

**4. 并发判据只有一条**：`IsReadOnly = !Mutates && !Serial`（`spec.go:68`），未注册名一律 fail-safe
（`queries.go:1-16`）。`Audience` 是注册表里**唯一**允许出现 Agent 名的地方（`spec.go:52-55`）。

**5. `ResultPaths` 必须逐工具声明。** `list_dir` / `print_dir_tree` 返回的裸文件名要显式标 `bare`
（`spec.go:264-266` + `internal/tools/specs_file.go:54,73,165`）——否则蓝图的证据集会收到假路径，
而后果是"入参侧 ∪ 出参侧"的判定误伤一整片模块（根 AGENTS.md §11 坑 42 同族）。

**5.1 静态注册表只读，动态规格走 `Deps.ExternalSpecs`。** `registry` 是 `init()` 期写入的只读事实；
运行期发现的 MCP 工具经 `Surface.rebuild()` 追加，名字 `mcp__<server>__<tool>`，用 `Spec.Inline`
自带 schema。把它们塞进全局注册表会打穿三处——(a) golden 走 `ProbeDeps()`，探针不知道有哪些
server，钉的是一条生产不存在的路（第 6 条同族）；(b) `renderCustomAgentToolPool`
（`internal/agents/deps.go:91-97`）遍历 `Specs()`，Custom Agent 于是拿到第三方能力，而 `Palette`
的全部意义就是"拿不到 Director 没有的能力"；(c) `Validate()` 的双向 1:1 会对每个外部工具报
"schemas.json 里没有对应声明"——修法不是放宽校验，是让它根本不进校验对象
（根 AGENTS.md §11 坑 44，闸门 `TestStaticRegistryNeverCarriesExternalTools`，
`internal/agents/registry_gate_test.go:350`）。外部工具**不计入**第 2 条的 schemas.json 条数基线。
`ValidateDynamic` 是这些规格自己的九条校验（名字前缀/字符集/长度、Inline 必填、写族必须
Mutates+NeedsAuth、不得与静态同名；调用点 `internal/tools/mcp_specs.go:131`）。

**6. 探针 `ProbeDeps()` 必须与生产 `depsFor` 同形。** 生产恒绑 `Blueprint` 执行器
（`internal/agents/deps.go:18-26`），而探针少绑一次的结果是 `Available` 为假 → 六个画像都"没有"这个工具 →
**golden 一动不动**（`profile.go:58-61`，根 AGENTS.md §11 坑 39）。修法是把能力加进 `testDeps()`，
而不是在测试里另搭一套夹具。

**6.1 画像矩阵决定谁能看见外部能力**：director/repo = external_read + skill；coding 额外拿
external_write；chat 只有 skill；devops 只有 external_read；browser 全无。`Palette` 一个位都不加
（Custom Agent 拿不到第三方能力）。改这张表必然让 `testdata/tool_surfaces.golden` 变红 —— 那是评审物。

**7. 新增工具的正确步骤**（对照根 AGENTS.md §9.3，这里是它会红的地方）：实现旁边 `MustRegister`
→ `schemas.json` 加条目 → 让画像匹配（只有委派类工具才写 `Audience`）→
`go test ./internal/agents -run TestAgentSurfacesGolden`，**工具面变化必须以显式重写 golden 的方式被评审**。

**8. 派生查询是唯一读取面，查询→Spec 字段一一对应**（`queries.go:11-111`，
`:3-8` 列着它替代的 8 张旧名字表）：`IsReadOnly`→`Spec.IsReadOnly()`、`IsSerial`→`Serial`、
`IsInteractive`→`BlocksOnUser`（阻塞等用户 → 豁免工具级超时）、`NeedsAuth`、`PathParams`、
`EffectOf`、`TruncationPriority`→`Trunc.ToPriority()`（未注册=1 常规）、
`IsCompactDisplay`→`Display==DisplayCompact`、`ModelSlot`（空串=沿用所属 Agent 引擎）、`Brief`。
**未注册名一律 fail-safe**——再往消费方抄一张按名字查的表，就是回到"八处各有一份默认值"。

**9. 频率档决定 schema 怎么到达模型**（`effect.go:118-127`）：Hot 每步全量声明；
Warm 默认全量、超每步预算可降成 Cold；Cold 只在目录里露 name+Brief，模型 `load_tool` 物化后
才拿 parameters schema（`queries.go:124-148` 的 `DeferredSpecs`/`AdvertiseSpecs` 按画像的
`resolveTier` 分这两堆）。校验器钉死的四条：Cold 必须带 `Brief`（≤40 rune，`spec.go:18,180-181`）、
`BlocksOnUser` 要求 Serial+Hot（`:203-204`）、流程控制/元工具必须 Hot（`:207-208`）、
`TruncProtected` 与 `Mutates` 冲突（`:211-212`）。**降级决策在 `internal/tools` 的 Surface**
（该文档硬约束 4"只动 Warm 绝不动 Hot"），本包只声明档位。

**10. `Validate()` 的启动期消费者在装配层，失败逐条打日志而不是退出。**
`internal/app/app.go:129-134` 的 `checkRegistry(toolspec.Validate, ...)` 之前全仓只有测试调它——
"注册表坏了"与"配置坏了"共用一个退出码只会混淆症状，而构建期问题已被 `registry_gate_test` 拦住。
动态规格自检另走 `ValidateDynamic`（`spec.go:236-289`，九类规则各有夹具），静态↔动态的
Cold-Brief 规则两侧各写一遍（`:180,276`），加校验规则时两边都要加。

## 入口

`spec.go`（`Spec` / `Registry` / `Validate`）、`deps.go`（窄接口）、`profiles.go`（七个画像）、
`queries.go`（派生查询）、`schemas.json`。

## 闸门

- `TestSchemasLoadAndDescribeEveryEntry`（`registry_test.go:417`）：改条数或漏 description。
- `TestValidate_ReportsEachRule`（`:180`）：删任一规则或让它不可响应；
  `_CleanSpecReportsNothingForItself`（`:305`）：规则全体误响、把死规则加回来。
- `_ReportsSchemaWithoutSpec`（`:345`）：只加 schema 不注册。
- `TestValidateDynamic_ReportsEachRule`（`:549`）与 `_CleanSpecReportsNothing`（`:680`）：
  钉第 5.1/10 条——动态规格的九类规则删一条、或让它不可响应即红。
- `TestStaticRegistryNeverCarriesExternalTools`（`internal/agents/registry_gate_test.go:350`）：
  有人把 MCP 工具塞回全局注册表即红。
- `TestProbeDepsIsCapabilityComplete`（`profile_test.go:420`）、
  `TestProbeDepsPassesEveryAvailabilityHook`（`:455`）：探针少填字段 → golden 静默少一类工具。
- agents 侧的配套闸门：`TestDerivedMetadataMatchesLegacyTables`（`internal/agents/registry_gate_test.go:110`，
  新增可并发/需授权/豁免超时的工具未进签字名单即红）、`TestAgentSurfacesGolden`（`:48`）、
  `TestCustomPaletteStaysInsideDirector`（`:256`，Custom Agent 拿到委派能力会成环）。

**已知不一致**：`profile.go:28-30` 的注释称 `RequiresExit` "关掉它的唯一合理场景是 Chat"，
而 `profiles.go:65-70` 的 `ProfileChat` 写的是 `RequiresExit: true`。引用这条约束时读代码，别读注释。
