# Plasma 架构设计 — Go 机制层 / Python 策略层

> **状态**：设计定稿待实施（2026-11-09）。
>
> **读者**：架构师。本文按"问题 → 约束 → 总览 → 决策记录（ADR）→ 详细设计 → 契约 →
> 威胁模型 → 测试 → 落地"组织。§14 是引用索引，**全部行号已于 2026-11-09 逐条核实**（改前坐标）。
>
> **范围**：新增一条"外部生成 Agent"的编译、加载、验证与进化通道。
> **不改**：50 条静态工具基线、`schemas.json`、`internal/agents/testdata/tool_surfaces.golden`、
> 7 个内建 Agent 的 `.prompt.md`。

---

## 0. 摘要

把系统切成**机制（mechanism）**与**策略（policy）**两半：

- **Go 拥有全部机制**：工具注册表与执行器、守卫、引擎栈、记忆与压缩、事件总线、UI，
  **以及 Plasma 编译器本身**。编译器是确定性的、安全承重的、必须与被检查的注册表同进程的东西。
- **Python 拥有全部策略**：Agent 生成、夹具出题、行为评审、进化变异与选择、内建 Agent 提案。
  这些是启发式的、LLM 驱动的、需要快速迭代而不应触发 Go 重编译的东西。
- **边界形态**：Python 是**按需拉起的一次性进程**（argv + JSON + exit code），**不是常驻 sidecar**。
  没有协议、没有握手、没有重连、没有生命周期管理。
- **质量保障**：六阶段编译（全在 Go）+ 双层夹具（结构层用脚本引擎、行为层用真 LLM）+
  三档 grade 棘轮 + 内容寻址存储 + 夹具不可变。
- **安全底线**：生成式 Agent 走**独立的窄 palette（默认无写、无 shell、无浏览器动作）**，
  升级必须经既有用户授权回路。

一句话：**Python 提议，Go 裁决。** 裁决者从不采信提议者的自我评估。

---

## 1. 问题陈述

### 1.1 目标

| # | 目标 | 可测判据 |
|---|---|---|
| G1 | 核心能力留在 Go，不因动态层迭代而重编译 | 改 Python 策略代码后 `go build` 产物字节不变 |
| G2 | 自定义 Agent 与自进化在 Python 侧实现 | 生成/变异/出题/评审/选择五个动作的代码 100% 在 `plasma/` |
| G3 | 两层交互高效简约 | 无常驻进程、无自定义协议；单次交互 = 一次 spawn + 一份 JSON |
| G4 | Agent 生成经过类编译器的静态检测 | 每个诊断码都有一条**能触发它**的负样本夹具；诊断文本进 golden |
| G5 | 生成质量可保证 | `verified` 必须同时通过结构夹具与行为夹具；只有 verified 以上可自动委派 |
| G6 | 动态层不可能绕过内核权限 | 越权 bundle 一律编译失败并点名；写/shell 升级必须走用户授权 |

### 1.2 非目标

- 不实现第二个 Agent 执行循环（复用 `RunAgentLoop`，`internal/agents/executor.go:184`）。
- 不让 Python 直连 provider、持密钥、碰工作区、起 shell。
- 不引入第三方 Python 依赖。
- 不做 bundle 热重载（`fsnotify` 仍只活在 `go.mod`）。
- 不自动改写内建 Agent（只出提案）。
- 不追求"用静态分析预测 LLM 行为"——自然语言提示词构不出调用图，编译器只做可判定的对账。

### 1.3 成功判据（验收时会真的跑）

1. 一份含幻觉工具名的 bundle → 编译失败，诊断指出 `tools[i]` 与最近候选名。
2. 一份请求 `run_bash` 的 bundle → grade 钉在 `provisional`（窄 palette），除非显式升级且用户批准。
3. 一份编译通过但行为夹具红的 bundle → grade 停在 `provisional`，**不可自动委派**。
4. 篡改 store 里的 bundle 一个字节 → 加载期 hash 校验失败并拒载。
5. 删掉 Python 解释器 → `delegate_meta` 自动降级到 Go 侧设计路径，描述里点名修复方式。
6. 进化器试图修改已 verified 版本的 `fixtures[]` → store 拒绝，要求新 hash 且 grade 不继承。

---

## 2. 现状与约束

### 2.1 可复用的既有接缝

| 接缝 | 位置 | Plasma 怎么用 |
|---|---|---|
| 纯数据 Agent 描述 | `director/types.go:22` | 编译产物的目标形状（`CustomAgent` 五字段，无闭包） |
| 按名挑选 + Palette 校验 | `toolspec/profile.go:174` | Resolve/Typecheck 直接调 `MatchNames`，**不重写** |
| 三级校验器 | `toolspec/spec.go:108 / :168 / :225` | 能力不变量的**唯一来源**，编译器引用而非复刻 |
| 运行期工具注册 | `tools/surface.go:413`（`Add` 拒重复） | 加载期把 `delegate_<name>` 挂进 Director 工具面 |
| 共享执行循环 | `agents/executor.go:184` + `ExecutorConfig`（`:29`） | 夹具执行与生产执行**同一条** |
| 结果定型 | `agents/result.go:197`（`FinalizeResultFull`）、`:58`（`deliverableText`） | 行为夹具的判据取交付全文，不取 `Text` |
| 委派完成接缝 | `director.go:462`（`applyEnhancedCommander`）、`:454`（`recordDelegationOutcome`） | 遥测账本的写入点 |
| 不装配 Agent 的子命令 | `main.go:79`（`setupCmd`） | `yagent plasma` 的取向 |
| 原子写唯一内核 | `util/atomic.go:41` | store 落盘 |
| 单一 token 计数器 | `tokenutil/tokenutil.go:30` | Consistency 阶段的预算判据，**与压缩同源** |
| 路由基线 | `llm/router.go:52 / :139 / :176` | `bundle.runtime.tier` 的落点（按 Agent 名小写查） |
| 唯一替换点 | `director_surface.go:182`（`runMetaDesign`） | `delegate_meta` 的实现从"单次 LLM 出 JSON"换成"调铸坊" |

`Profile.Names`（`toolspec/profile.go:23-27`）的注释明写：这条通道专为"LLM 运行期产出的工具名清单"
而开，调用方必须先用 Palette 校验。`MatchNames` 返回 `(selected, unknown, rejected)` 三档——
幻觉工具名今天就已经被挡下并记账。Plasma 的 Resolve 阶段直接复用这个函数。

### 2.2 必须遵守的既有不变量

1. **动态规格不进全局注册表**（坑 44）：plasma Agent 只经 `Surface.Add`，绝不 `toolspec.MustRegister`。
   否则同时打穿 golden、Custom Agent 的 `Palette`、`Validate()` 的双向 1:1。
2. **一个 tool_call 必有一条 tool 结果**：夹具执行走真实循环即自动满足。
3. **不制造第二份同形实现**（坑 18/33）：编译器收归 Go、token 计数用 `tokenutil`、
   夹具执行走 `RunAgentLoop`——全是为了不出现"两份实现各自漂走"。
4. **goroutine 只经 `internal/supervise`**：Python 子进程的等待与超时走 `supervise.From(ctx).Go`。
5. **锁不跨 IO**：store 的读写与 Surface 重建不得持同一把锁做磁盘 IO。
6. **每个新配置键必须有"能因改值而失败"的测试**（坑 25/35）。
7. **校验规则本身必须能被触发**：每个诊断码配一条负样本夹具（`scripts/check_test_hygiene.sh` 的取向）。

### 2.3 既有闸门

`bash scripts/check_all.sh` = build + vet + 11 把守卫 + 全量单测。**没有 CI 质量闸门**
（`release.yml` 只构建发布）。因此 Plasma 的所有契约必须由本地闸门钉住，不能指望 CI。

### 2.4 权限现状（安全设计的出发点）

`ProfileDirector.Palette`（`internal/toolspec/profiles.go:33`）**含 `EffectFSWrite | EffectShell`**。
Director 自己 Deny 这两项（`:32`），却把它们放进给子 Agent 的 Palette。今天 Meta 设计的 Custom Agent
经 `runCustomAgent`（`director_surface.go:261`）用的正是这个 Palette——人在回路时可接受，
但**自动委派的自进化 Agent 继承它就是权限升级漏洞**。Plasma 必须用独立的窄 palette（ADR-4）。

---

## 3. 架构总览

### 3.1 分层

```
┌── Go 机制层（唯一副作用执行者 / 唯一裁决者）───────────────────────────────┐
│                                                                            │
│  内核：toolspec(50 Spec + Effect) · tools(执行器/守卫/Surface)              │
│        llm(引擎/路由/回退/熔断/在飞闸门) · memory · compression             │
│        blackboard · artifact · blueprint · messaging · tui/http · config    │
│                                                                            │
│  ┌── internal/plasma（新增叶子包）──────────────────────────────────┐     │
│  │  编译器：Parse → Resolve → Typecheck → Consistency → Verify → Emit │     │
│  │  存储：内容寻址 + hash 校验 + 版本/血统 + 夹具不可变                │     │
│  │  加载：store → LinkPlan → CustomAgent/Profile/ExecutorConfig        │     │
│  │  遥测：委派完成接缝写一行 outcome                                  │     │
│  │  子进程驱动：spawn python3 plasma.pyz <cmd> --json（超时/降级）     │     │
│  └───────────────────────────────────────────────────────────────────┘     │
│         ↑ FixtureRunner 窄接口注入（破循环依赖）                            │
│  internal/agents：RunAgentLoop 实现 FixtureRunner；runMetaDesign 调铸坊     │
└──────────────────────────────┬─────────────────────────────────────────────┘
                               │ 一次性子进程：argv + JSON + exit code
                               │ （无常驻、无协议、无握手）
┌──────────────────────────────┴── Python 策略层（plasma.pyz, stdlib-only）──┐
│  generate        需求 → 候选 bundle（经 yagent plasma sample 借引擎）       │
│  author-fixtures 给 bundle 出题（**谱系与答题者分离**）                     │
│  judge           行为转录 → rubric 评分（经 sample 借引擎）                 │
│  evolve          遥测 → 变异 → 候选（**不编译、不改夹具**）                 │
│  explain         诊断 → 人话（TUI 面板）                                    │
│  propose         内建 Agent 的 prompt/工具面 diff 提案（永不自动 apply）    │
└────────────────────────────────────────────────────────────────────────────┘
```

### 3.2 职责矩阵

| 关注点 | Go | Python | 理由 |
|---|:--:|:--:|---|
| 工具注册表 / Effect 位数学 | ● | — | 唯一真相，必须同进程 |
| 编译六阶段 | ● | — | 确定性 + 安全承重（ADR-1） |
| token 计数 | ● | — | 必须与压缩阈值同一个计数器（`tokenutil`） |
| 夹具**执行** | ● | — | 只能有一个 Agent 循环 |
| 夹具**出题** | — | ● | 启发式、需要 eval 生态 |
| 行为**评审**（rubric/judge） | — | ● | 策略，迭代快 |
| Agent **生成** | — | ● | LLM 驱动的编排 |
| **进化**（变异/评分/选择） | — | ● | 策略层的全部价值所在 |
| LLM 调用 | ● | 借 | 引擎栈只有一份（ADR-7） |
| 存储 / 版本 / hash | ● | — | 安全承重 |
| 遥测**写入** | ● | — | 在委派完成接缝 |
| 遥测**读取与评分** | — | ● | 分析策略 |
| 权限裁决 / 授权回路 | ● | — | 内核不变量 |

### 3.3 三条通道

| 通道 | 方向 | 载体 | 漂移的后果 |
|---|---|---|---|
| **能力清单** | Go→Python | spawn 前写 `manifest.json`，路径经 argv 传入 | 仅是一次编译失败（生成器参考资料过期），不是安全问题——裁决在 Go 进程内对着活注册表做 |
| **候选与产物** | 双向 | 文件（bundle / 诊断 / 评分 / 提案），线缆只走路径 | 无 |
| **遥测** | Go→Python | `~/.yagent/agents/telemetry.jsonl`（独立账本，不 tail rollout） | 无 |

`capability_lock` 是**溯源元数据**（记录这份 bundle 是对着哪版清单生成的，便于解释诊断），
不是安全门——因为编译器在 Go 进程内对着活注册表做裁决，不存在跨语言 ABI 漂移。

### 3.4 为什么不是常驻 sidecar

| 维度 | 常驻 MCP sidecar | 一次性子进程 |
|---|---|---|
| 进程生命周期 | 需 Manager、重连、状态五档、`/mcp` 面板 | 无：spawn → wait → 收 JSON |
| 启动顺序约束 | `app.Init` 必须先 spawn 才可用 | 无：用到才拉起 |
| 失败类 | 挂住、断连、readLoop 死锁、僵尸 | 只有"退出码非 0 / 超时" |
| 泄漏面 | 长驻进程 + 子进程组 | 每次调用结束即回收（`WaitDelay` 兜管道 EOF，坑 46） |
| 延迟 | 首次 spawn 后 ~ms | 每次 ~200–500ms（Python 启动） |
| 适配的实际负载 | 高频请求 | **批处理**：generate 秒级、evolve 分钟级 |

延迟那一栏是唯一代价，而它落在秒级/分钟级的动作上，**可忽略**。策略层的真实负载是批处理
（读遥测→变异→出题→评审），不是长连接服务。常驻进程换来的是生命周期、重连、readLoop 死锁、
启动顺序、状态面板五类问题，而收益为零。若将来 profile 证明生成路径需要高频内循环，
再引入常驻（届时 ADR-2 可被推翻，但要先有测量）。

---

## 4. 关键设计决策（ADR）

### ADR-1：编译器归 Go

- **问题**：六阶段中五阶段是对活注册表的确定性查询。放 Python 就要发布能力清单并处理
  "Python 看过的清单 vs Go 拥有的注册表"的漂移，然后再发明一个链接闸去补它。
- **选项**：(a) 全在 Python + Go 链接闸；(b) 全在 Go，Python 只提议；(c) 分阶段混放。
- **决定**：**(b)**。
- **理由**：① 跨进程凭空造出漂移失败类，搬进进程内它整体消失，typecheck 从"近似"变"精确"；
  ② token 预算必须用 `tokenutil.EstimateTokens`（`tokenutil/tokenutil.go:30`），它有 cl100k_base +
  一次性粘滞降级语义，Python 侧再写一个就是第二份实现且降级行为不同（坑 18/33）；
  ③ 编译器在 Go 才能被 `check_all.sh`、golden、变异测试覆盖——放 Python 就**脱离了本仓全部闸门**。
- **后果**：`capability_lock` 是溯源元数据而非安全门；无独立"链接"阶段（合并进加载期）；
  Python 净表面积缩到约 1200 行策略代码（见 OD-1）。
- **补偿控制**：`internal/plasma/testdata/diagnostics.golden` 钉住诊断文本；每个码一条负样本夹具。

### ADR-2：无常驻进程，策略层按需 spawn

- **问题**：策略层的真实负载是批处理，不是长连接服务。
- **选项**：(a) MCP stdio 常驻；(b) 自建 NDJSON 常驻；(c) 一次性子进程；(d) localhost gRPC。
- **决定**：**(c)**。
- **理由**：见 §3.4 对照表。(a)/(b)/(d) 都在为一个不存在的"高频长连接"需求付分布式系统的税。
- **后果**：`internal/mcp` **一行不改**；不新增 MCP server 配置；`YAGENT_PLASMA=0` 就是"不 spawn"。
- **补偿控制**：子进程必须带 `cmd.WaitDelay`（坑 46：`CombinedOutput` 的等待条件含管道 EOF，
  子孙进程会让 ctx 看门狗失效）；超时经 `supervise` 起 goroutine 等待。

### ADR-3：双层夹具——结构层与行为层必须分开

- **问题**：脚本引擎按 Agent 身份回放预设响应，`Model()` 返回 `"scripted-mock"`
  （`e2e_engine_test.go:96`）——**生成的 system prompt 根本没被执行过**。
  只用脚本夹具测的是 Go 循环的接线，不是 Agent 的质量。只用真 LLM 则贵且非确定，进不了 CI。
- **选项**：(a) 只脚本；(b) 只真 LLM；(c) 双层。
- **决定**：**(c)**。

| 层 | 引擎 | 测什么 | 确定性 | 何时跑 | 门禁 |
|---|---|---|---|---|---|
| **5a 结构夹具** | 脚本（按 Agent 身份分派） | 接线：工具集真能解析、`agent_exit` 产出声明的契约形状、守卫/授权路径、步数预算 | 是 | 每次编译 + `check_all.sh` | 阻断 `provisional` |
| **5b 行为夹具** | **真 LLM** | prompt 质量：真模型在沙箱工作区里是否产出预期交付物 | 否 | 升 `verified` 前 / 手动 / 预算内 | 阻断 `verified` |

- **5b 的判据**：硬断言（`must_call` / `must_not_call` / `deliverable_contains` / `max_steps`）
  **加上** rubric 评审（`plasma.pyz judge`，LLM-as-judge，经 `sample` 借引擎）。硬断言防 judge 放水，
  rubric 防硬断言过拟合。
- **后果**：`verified` 的成本是"十秒到分钟 + 真金白银的 token"。因此 5b **默认只在
  升级 grade 时跑**，不在每次编译时跑（OD-3）。
- **补偿控制**：5b 有 token 与墙钟双预算；超预算 ⇒ grade 停在 `provisional` 并点名"未验证"，
  **绝不静默当作通过**。

### ADR-4：plasma 独立窄 palette + 能力升级门

- **问题**：`ProfileDirector.Palette` 含 `EffectFSWrite | EffectShell`（`profiles.go:33`）。
  自动委派的生成式 Agent 继承它 = 权限升级漏洞：生成器一旦被任务描述里的注入内容污染，
  产出的 prompt 就带着写/shell 能力进内核。
- **选项**：(a) 沿用 Director Palette；(b) 新窄 palette，写/shell 一律拒；(c) 新窄 palette + 显式升级门。
- **决定**：**(c)**。

```go
// internal/plasma/palette.go —— 不进 BuiltinProfiles()，因此不动 golden
PlasmaPalette = EffectFSRead | EffectSearchRegex | EffectCodeSemantic | EffectCodeGraph |
                EffectContext | EffectFlow | EffectToolLoad |
                EffectCogReflect | EffectCogDeep | EffectSkill | EffectExternalRead
// 刻意不含：FSWrite · Shell · BrowserObserve/Act/Eval/Artifact · Checkpoint · ExternalWrite · Delegate
```

- **升级路径**：bundle 的 `policy.effects_required` 若请求窄 palette 之外的能力，编译器不直接拒，
  而是产出 `PLS-210 escalation-required` 并把该 bundle 的 grade **上限钉在 `provisional`**；
  首次委派时经既有 `UserConfirmManager` 回路（`user_help_needed` / `user_help_response`，
  critical 旁路永不丢弃）向用户明示"这个生成式 Agent 要求写文件/shell"，批准后才解锁。
- **为什么不直接拒**：直接拒会让"生成一个能改代码的 Agent"这个合理需求无路可走；
  为什么不走 config 开关：开关是全局的，而风险是**逐个 Agent** 的。
- **为什么不加进 `BuiltinProfiles()`**：那会改 golden（`profiles.go:6-8` 明写每个画像的解析结果由
  golden 钉死）。plasma Agent 走 `Profile{Agent, Names, RequiresExit:true}` 的**按名挑选**通道，
  窄 palette 只作为 `MatchNames` 的第二个参数——**golden 一个字节都不动**。
- **迁移影响**：今天 Meta 设计的 Agent 能拿 `run_bash`；走 plasma 通道的同类 Agent 默认拿不到，
  需升级批准。这是**有意的能力收窄**，属安全修复而非回归。
- **补偿控制**：一条夹具断言"请求 `run_bash` 且未批准 ⇒ grade == provisional 且不可自动委派"；
  一条变异测试：把 `PlasmaPalette` 里的 `EffectFSWrite` 加回去，该夹具必须红。

### ADR-5：内容寻址存储 + 夹具不可变

- **问题**：进化需要回滚，且必须防 Goodhart——若进化器既能改 Agent 又能改它的夹具，
  就是自己出题自己考，适应度必然虚高。
- **决定**：
  - 存储键 = `sha256(归一化 bundle)`，目录 `~/.yagent/agents/{name}/{hash}/`；semver 与 lineage 是
    **索引元数据**，不是主键。回滚 = 指向旧 hash，无"撤销"逻辑。
  - **`fixtures[]` 与 `contract` 一经某个 hash 落盘即不可变**。进化器改夹具 ⇒ 必须产生新 hash，
    且新 hash 的 grade 从 `provisional` 重新开始（**不能继承旧 grade**）。
  - **出题谱系分离**：`author-fixtures` 的调用必须记录出题者标识；同一 lineage 的变异体
    **不得**由该 lineage 自己的出题动作产生的夹具来验证。store 记录 `fixture_provenance`，
    加载期校验。
- **补偿控制**：store 层一条测试"改 fixtures 后 grade 不继承"；一条"同谱系出题 ⇒ 拒载"。

### ADR-6：遥测独立账本，不 tail rollout

- **问题**：rollout 是**重放账本**，格式服务于 `/resume`；拿它做分析会把进化器耦合到重放格式变更上，
  且要反复解析大文件。`delegationOutcome`（`director.go:442-460`）只是 ctx 内的当次载体，没有账本。
- **决定**：在**委派完成接缝**写一行紧凑 outcome 到 `~/.yagent/agents/telemetry.jsonl`：

```jsonc
{ "ts":"…", "agent":"security_auditor", "bundle_hash":"sha256:…", "grade":"verified",
  "session":"…", "delegation_id":"…", "stop_reason":"agent_exit",
  "steps":7, "tokens_in":18400, "tokens_out":2100,
  "degraded":false, "escalated":false, "note_ref":"…", "fixture_run":false }
```

- **写入点**：`applyEnhancedCommander`（`director.go:462`）/ `recordDelegationOutcome`（`:454`）
  这条既有接缝——它已经是"每次委派结束都必经"的地方，且并行委派下按 ctx 载体取值不串味。
- **理由**：① 一行 vs 整个会话，解析成本差两个数量级；② 重放格式变更不再打断进化器；
  ③ `fixture_run` 标记让 5b 的运行**不污染生产统计**。
- **补偿控制**：账本写入失败**不得阻断委派**（照 artifact `Save` 失败的取向：降级、不返回 error）；
  但必须留一条可见账目（坑 43：防线生效过却没人知道，与静默丢失只差一步）。

### ADR-7：LLM 经 `yagent plasma sample` 借 Go 引擎

- **问题**：Python 生成/变异/评审都要调 LLM。引擎栈已有路由（`RoutingEngine` 包装漏一层则
  auto-routing 静默失效）、回退、熔断（`recovery.Allow()` 查询即试探）、进程级在飞闸门、
  token 账、`llm-*` 日志。
- **选项**：(a) 子命令借引擎；(b) 扩展 MCP sampling；(c) Python 自带客户端。
- **决定**：**(a)**。
- **理由**：(c) 会让引擎栈全部出现第二份实现且密钥出 Go 边界；(b) 要动 `internal/mcp` 的 readLoop
  （ADR-2 已决定不碰它）。
- **后果**：每次采样约 100ms 进程启动开销；生成/变异本身是秒级，可忽略。
- **补偿控制**：`sample_timeout` 兜上界；采样调用**同样受在飞闸门约束**（不会因为来自子进程就绕过）。

### ADR-8：stdlib-only zipapp

- **决定**：Python 侧纯标准库，打成单文件 `plasma.pyz`，`go:embed` 进 `dist/bin`，
  启动解到 `~/.yagent/bin`（照 codeseek/browser-control 的既有机制）。
- **前提**：**仍需 PATH 上有 `python3`（≥3.9）**。zipapp 消除的是依赖与联网，不是解释器。
  `[plasma] python` 可显式指定解释器路径。
- **降级**：解释器缺失 / zipapp 提取失败 ⇒ `delegate_meta` 描述自动降级并点名修复方式，
  Meta 退回 Go 侧单次 LLM 设计路径（`runMetaDesign` 的既有实现保留为 fallback）。
- **不许重犯的坑**：`embedbin` 提取失败今天只 `slog.Debug`（`internal/app/app.go:167-168`），
  默认级别下不可见，症状是"退化成纯文件工具"。Plasma 的降级**必须在工具描述里可见**。
- **代价**：不能用 pydantic/numpy，IR 与统计手写。这是有意的取舍。

### ADR-9：三档 grade 棘轮

| 档 | 准入 | 能力 |
|---|---|---|
| `provisional` | 阶段 1–4 + **5a** 通过 | 可列出、可人工点名委派；**不可自动委派**；TUI 醒目标记 |
| `verified` | 上述 + **5b 通过**（真 LLM + rubric）+ 无未批准的升级请求 | 可被 Director 自动委派 |
| `promoted` | verified + 连续 N 次生产成功（默认 N=5，遥测驱动，`fixture_run=false` 才计数） | 同上，列表优先呈现 |

- **掉档自动**：生产失败（`stop_reason != agent_exit`、或降级交接）累计到阈值 ⇒ verified→provisional。
- **回滚 = 载旧 hash**。

---

## 5. 详细设计

### 5.1 Plasma IR

```jsonc
{ "schema_version": 1,
  "name": "security_auditor",                 // snake_case → delegate_<name>
  "display_name": "Security Auditor",
  "version": "0.3.1",                         // 索引元数据；主键是 hash
  "capability_lock": "sha256:…",              // 溯源元数据（非安全门，ADR-1）
  "lineage": { "parent_hash": "sha256:…", "mutation": "prompt:exit-contract",
               "rejected": [{ "mutation":"tools:+run_bash", "diag":"PLS-210" }] },
  "prompt": { "system": "…", "lang": "Chinese" },
  "tools": ["read_file","search_by_regex","find_function_caller","agent_exit"],
  "policy": { "effects_required": ["fs_read","search_regex","code_graph","flow"],
              "deny": ["fs_write","shell"],
              "workspace_scoped": true },
  "runtime": { "max_steps": 15, "stop_on_finish": true, "llm_timeout": "120s",
               "tier": "balanced",
               "compression": { "enable": true, "keep_tool_result_tokens": 200 },
               "max_parallel_readonly_tools": 4 },
  "contract": { "must_exit_with": ["summary","deliverable"], "thought_format": true },
  "fixtures": {
    "provenance": { "author": "plasma-author-fixtures", "lineage": "independent-2026-11-09" },
    "structural": [{ "id":"s1", "workspace":"fixtures/s1/", "input":"…",
                     "script": [{ "tool":"read_file", "args":{"path":"a.go"} },
                                { "tool":"agent_exit", "args":{"summary":"…","deliverable":"…"} }],
                     "expect": { "must_call":["read_file","agent_exit"],
                                 "must_not_call":["run_bash"], "max_steps":8,
                                 "exit_shape":["summary","deliverable"] } }],
    "behavioral": [{ "id":"b1", "workspace":"fixtures/b1/", "input":"审计 a.go 的注入风险",
                     "expect": { "must_call":["read_file","agent_exit"],
                                 "deliverable_contains":["injection"],
                                 "max_steps":12 },
                     "rubric": ["是否指出了具体行号","是否给了可执行的修复建议"] }]
  },
  "quality": { "grade":"verified", "score":0.87, "runs":12, "lint":[],
               "behavioral_verified_at":"…", "behavioral_tokens":18400 } }
```

**字段 → Go 归宿映射**（这张表是"IR 足够小"的证明）：

| bundle 字段 | Go 归宿 | 位置 |
|---|---|---|
| `name`/`display_name`/`prompt.system`/`tools` | `CustomAgent` 四字段 | `director/types.go:22` |
| `tools`（经 `PlasmaPalette` 校验） | `Profile{Agent, Names, RequiresExit:true}` | `toolspec/profile.go:11`；装配 `director_surface.go:261` |
| `runtime.*` | `ExecutorConfig` 同名字段 | `agents/executor.go:29`，基线 `:59` |
| `runtime.tier` | 该 Agent 名的 routing baseline | `llm/router.go:139`（按小写名查）、`:176`（合并配置） |
| `name` | 运行期 `Spec{delegate_<name>, EffectDelegate, Hot, Mutates:true, Audience:["director"]}` → `Surface.Add` | 形状见 `director_surface.go:284`；`Add` 在 `tools/surface.go:413` |
| `policy`/`contract`/`fixtures`/`quality`/`lineage` | **只在编译期与加载期活着**，不进运行时权限来源 | — |

最后一行是关键：运行时权限只认 `Effect` 位掩码 + `WorkspaceGuard` + 授权回路，
**`policy` 是编译器的输入，不是运行时的授权依据**。

今天 `executeCustomAgent`（`director.go:336-366`）把 `MaxSteps=15`、`StopOnFinish=true` 写死；
Plasma 之后来自 bundle，**其余接线逐字不动**。

### 5.2 编译器：六阶段（全在 Go 进程内）

| # | 阶段 | 判据来源 | 挡住什么 | 失败码族 |
|---|---|---|---|---|
| 1 | **Parse** | IR schema + 归一化 | 结构错、必填缺失、未知键 | PLS-0xx |
| 2 | **Resolve** | **活注册表** `toolspec.Get` / `MatchNames` | 幻觉工具名（附 did-you-mean，取向照 `models` 的三级：完整名→裸名→相似度） | PLS-1xx |
| 3 | **Typecheck** | `PlasmaPalette` 包含 + `policy.deny` + `ValidateDynamic`/`ValidateProfile` 的**同一批规则** + knob 范围 + `agent_exit` 存在 | 权限越界、能力不一致、无收尾通道（坑 40 形状） | PLS-2xx |
| 4 | **Consistency** | prompt ↔ schema 对账 + `tokenutil` 预算 + `ThoughtPlan.Validate` 契约 | 参数名错配（坑 38 形状）、dead selection、提示点名被 deny 的能力、提示词吃掉压缩阈值、`<thought>` 静默丢失 | PLS-3xx |
| 5 | **Verify** | 5a 结构夹具（脚本引擎）；5b 行为夹具（真 LLM + rubric，按 ADR-3 的时机） | 接线错 / prompt 无效 | PLS-5xx |
| 6 | **Emit** | `util.AtomicWrite` + sha256 + grade 判定 + 夹具不可变校验 | 落盘失败、grade 越权继承 | PLS-6xx |

**加载期**（不是编译阶段）：store → 重算 hash → 对着**活注册表**复核阶段 2–3 → `LinkPlan` →
`CustomAgent` + `Profile` + `ExecutorConfig` → `Surface.Add`。hash 不符或注册表漂移 ⇒ 拒载并点名
（PLS-7xx）。这一步防的是磁盘篡改与工具改名。

阶段 4 的 `<thought>` 规则对齐 `ThoughtPlan.Validate()`（`thinklink/thoughtplan.go:195`）：
定界符成对、每轮恰一块、`goal`/`reason` 非空、至多一条 `[>]`。**为什么必须编译期查**：
该块解析失败"不报错、只静默丢失"，于是 Agent 看起来在跑，thinklink 面板与终极压缩的重建原料却全空。

阶段 4 的 token 预算用 `tokenutil.EstimateTokens`（`tokenutil/tokenutil.go:30`）——
**与压缩阈值同一个计数器**。注意它的降级是一次性粘滞的（首次离线失败后全进程走 `len/4`，
对中文约 4 倍低估），所以预算判据要留余量，且诊断里要说明当前是否处于降级估算。

### 5.3 诊断：稳定契约

```
bundle.tools[3]: error[PLS-101]: unknown tool "read_filez"
                 note: did you mean "read_file"?
bundle.policy.effects_required: error[PLS-210]: "shell" is outside PlasmaPalette
                 note: grade capped at provisional; user approval required at first delegation
bundle.prompt.system: error[PLS-302]: mentions parameter "path" for tool "search_replace_in_file"
                 note: schema declares "file_path"; the call would silently lose the argument
bundle.prompt.system: warning[PLS-304]: system prompt is 11200 tokens (budget 8000)
                 note: counted with tokenutil (cl100k_base); eats into the compression threshold
bundle.fixtures.behavioral: error[PLS-502]: behavioral fixture b1 failed (2/3 hard assertions)
                 note: grade remains provisional; rubric score 0.41
```

**码表单一来源在 Go**（`internal/plasma/diag.go`），Python 只读它（经清单发布），
因此诊断码不可能两侧漂移。warning 不阻断，但进 `quality.lint[]` 并作为进化适应度的一项。

码族划分：`0xx` parse · `1xx` resolve · `2xx` typecheck/权限 · `3xx` consistency/lint ·
`5xx` verify · `6xx` emit/store · `7xx` load。

### 5.4 存储、版本与加载生命周期

```
~/.yagent/agents/
  {name}/{hash}/
    bundle.json          # 归一化后的 IR（主键 = hash）
    report.json          # 诊断 + grade + 各阶段结果
    fixtures/            # 沙箱工作区快照（不可变）
    meta.json            # semver / lineage / fixture_provenance / 加载计数
  index.json             # name → 当前指向的 hash（原子写；这是唯一的可变指针）
  telemetry.jsonl        # ADR-6 的账本（追加）
  proposals/             # 内建 Agent 提案（等人批）
```

- **加载时机**：Director 构造期（`metaHandler` 今天在 `director.go:172` 建为空）。
  枚举 `index.json` → 逐条 hash 校验 + 阶段 2–3 复核 → `Surface.Add`。
- **确定性顺序**：按 `name` **字典序**加载与呈现，不用磁盘扫描序、不用插入序。
  理由：`customAgentPromptSection()` 拼在 blueprint/skills/mcp 之前（`director.go:816-821`），
  顺序抖动会让后面全部前缀缓存失效；本仓已有"工具声明必须按名排序"的同源纪律。
  `metaHandler.List()` 今天按插入序返回（`director/meta_handler.go:71`），跨进程加载顺序不确定——
  Plasma 的加载必须改为字典序。
- **预算**：`max_loaded_agents`（默认 12）+ `catalog_line_chars`（默认 120）。
  超预算按 grade→score→name 排序截断，并**留一行可见的省略账**（照 `module_docs.go` 的取向）。
- **运行中不热替换**：一次委派开始后保持它加载时的 hash；版本变更在下一轮 Surface 重建时生效。
- **GC**：`store_ttl`（默认 720h）清理未被 index 指向且超过 TTL 的 hash 目录。
  **必须有 GC**——`artifact` 包今天无 TTL/GC、全文只增不删，这个坑不重犯。

### 5.5 权限模型

```
PlasmaPalette（默认，只读族）
   ⊂ ProfileDirector.Palette（今天含 FSWrite|Shell）

请求超出 PlasmaPalette
   → PLS-210，grade 上限 = provisional
   → 首次委派经 UserConfirmManager 明示批准
   → 批准后该 hash 记 escalation_approved=true，可升 verified
```

- 授权状态**按 hash 记**，不按 name 记：进化出新版本 ⇒ 升级批准**失效**，要重新批。
  否则"批准过一次写权限"会被后续所有变异继承。
- 写类工具的 `NeedsAuth` 仍由 `Effect` 派生（`authEffects`），**逐次调用照旧要授权**；
  升级门管的是"这个 Agent 能不能拿到这类工具"，不是"这次调用要不要问"。两层不互相替代。
- YOLO（`-y`）照旧绕过一切授权——这是既有语义，Plasma 不改它，但**诊断与账本要记 `escalated=true`**。

### 5.6 遥测与评分

评分函数（Python 侧，权重可迭代）：

```
score = w1·成功率 + w2·(1/解决步数) + w3·(1/token成本)
      + w4·用户批准率 + w5·(1-降级交接率) - w6·lint警告数
```

- 只统计 `fixture_run=false` 的记录（5b 的运行不污染生产统计）。
- 样本量不足（< N_min，默认 5）时**不升 promoted**，避免小样本噪声。
- 评分函数的权重属策略，**不进 Go**；Go 只保证账本字段齐全且写入不阻断委派。

### 5.7 进化闭环

```
telemetry.jsonl ─→ [Py] score ─→ [Py] mutate ─→ 候选 bundle
                                                   │
                     ┌─────────────────────────────┘
                     ↓
              [Go] compile 阶段1-4 + 5a
                     │ 失败 → 诊断进 lineage.rejected[]（免费负样本）
                     ↓ 通过
              [Py] author-fixtures（**独立谱系**）
                     ↓
              [Go] 5b 行为夹具（真 LLM + [Py] judge）
                     ↓ 通过
              [Go] emit → grade=verified → index 指向新 hash
                     ↓
              生产运行 → telemetry → promoted / 掉档
```

**Goodhart 三道防线**：① 夹具不可变（ADR-5）；② 出题谱系分离（ADR-5）；
③ 适应度含**生产遥测项**，不只是夹具通过率（§5.6）。

**进化默认关闭**（`evolve_enable=false`）：它是唯一会在后台改变系统行为的开关。

### 5.8 内建 Agent 提案通道

`plasma.pyz propose` 读遥测 + 内建 Agent 的 `.prompt.md`，产出 unified diff 到 `proposals/`，
**永不自动 apply**。理由：内建 Agent 的 prompt 与 Go 代码是"改行为要同时改两处"的耦合
（根 `AGENTS.md` §4），自动改写会打断这个不变量。TUI `/proposals` 面板供人审批（P8）。

---

## 6. 接口契约

### 6.1 Go API（`internal/plasma`，叶子包）

```go
// 允许的 import：stdlib + toolspec + util + projectid + tokenutil
// 禁止：agents（循环）、llm、mcp、messaging、config

type Compiler struct {
    Runner   FixtureRunner   // 由 agents 侧注入；nil ⇒ 跳过阶段5，grade 上限 provisional
    Paletter func() toolspec.Effect  // 默认返回 PlasmaPalette
    Store    *Store
}
func (c *Compiler) Compile(ctx context.Context, b *Bundle) (*Report, error)

// 破循环依赖的窄接口（形状照 agents/ports.go 的 BoardWriter）
type FixtureRunner interface {
    RunStructural(ctx context.Context, plan *LinkPlan, f Fixture) (Outcome, error)
    RunBehavioral(ctx context.Context, plan *LinkPlan, f Fixture) (Outcome, error)
}

type LinkPlan struct {   // 加载期产物，全部落进既有类型
    Agent   CustomAgentView   // name/display/prompt/tools（纯数据，不 import director）
    Profile toolspec.Profile
    Config  ExecutorConfigView // max_steps/stop_on_finish/timeout/compression（纯数据）
    Hash    string
    Grade   Grade
}
```

`CustomAgentView` / `ExecutorConfigView` 是 plasma 侧的纯数据结构，由 `agents` 侧转换成
`director.CustomAgent` 与 `agents.ExecutorConfig`。**plasma 不 import agents，也不 import director。**

循环依赖的成因：`agents` 要 import `plasma`（调编译器），而阶段 5 需要 `RunAgentLoop`（在 `agents` 里）。
`FixtureRunner` 窄接口由 `agents` 侧实现并注入，与 `agents/ports.go` 的 `BoardWriter`/`BlueprintSource`
同一形状。

### 6.2 CLI（`main.go` 新增 `plasmaCmd`，取向照 `setupCmd`）

| 命令 | 语义 | 是否装配 Agent/MCP |
|---|---|---|
| `yagent plasma compile --bundle <path> [--behavioral] [--json]` | 跑六阶段，出报告 | 否（阶段5 需要时才装配） |
| `yagent plasma sample --messages <path> --tier <t> --json` | 借 Go 引擎栈采样 | 是（引擎） |
| `yagent plasma test --bundle <path> --fixture <id> --json` | 单跑一个夹具（5a 或 5b） | 是 |
| `yagent plasma list [--json]` | 列 store | 否 |
| `yagent plasma promote --name <n> --hash <h>` | 手动改 index 指针 | 否 |
| `yagent plasma manifest --out <path>` | 导出能力清单（给生成器） | 否 |

### 6.3 Python 进程契约

```
argv:    python3 <plasma.pyz> <command> --manifest <path> --in <path> --out <path> [--budget …]
stdin:   不使用
stdout:  仅一份 JSON（结果或 {"error": …}）；日志一律走 stderr
exit:    0 成功 · 1 业务失败（诊断在 out） · 2 用法错 · 3 超时被杀
```

六个命令：`generate` / `author-fixtures` / `judge` / `evolve` / `explain` / `propose`。
**全部无状态、一次性**。Go 侧 `internal/plasma/pyproc.go` 负责 spawn、`WaitDelay`、超时、
退出码翻译、降级。

### 6.4 配置

```toml
[plasma]
enable = true                      # false = 不装配，delegate_meta 退回 Go 侧设计
python = ""                        # 空 = PATH 上的 python3；显式路径 = 指定解释器
store = "~/.yagent/agents"
max_loaded_agents = 12             # 加载预算；<=0 = 默认
catalog_line_chars = 120           # 每个 Agent 在 prompt 清单里的行宽；显式 0 = 不裁剪
compile_timeout = "60s"
structural_fixture_timeout = "60s"
behavioral_fixture_timeout = "10m"
behavioral_token_budget = 60000    # 5b 的 token 上界；超 ⇒ 停 provisional 并点名
sample_timeout = "10m"
auto_delegate_min_grade = "verified"
promote_min_runs = 5
store_ttl = "720h"                 # "0" = 不清理
evolve_enable = false              # 默认关
evolve_interval = "30m"
proposal_dir = "~/.yagent/agents/proposals"
```

急停 `YAGENT_PLASMA=0`（与 `YAGENT_MCP=0`/`YAGENT_BLUEPRINT=0` 同族），判据写在 `Normalize*` **内部**。

**两套取值约定别混**（根 `AGENTS.md` §7）：`max_loaded_agents` 用 `<=0 = 默认`；
`catalog_line_chars` 与 `store_ttl` 的 **0 本身有语义**，必须靠 TOML 键的存在性
（`md.IsDefined`）区分"没写"与"写了 0"（坑 13/26）。

每个键都要有一条**能因改值而失败**的测试（坑 25/35）。

---

## 7. 安全与信任模型

### 7.1 威胁模型

| ID | 威胁 | 攻击面 |
|---|---|---|
| T1 | 生成器产出越权 bundle（幻觉或策略错误） | 编译期 |
| T2 | **提示注入经生成器进入 Agent system prompt**（任务描述来自网页/文件/第三方 MCP 输出） | 编译期 + 运行期 |
| T3 | 进化 Goodhart：改夹具刷分 | 存储期 |
| T4 | store 磁盘篡改 | 加载期 |
| T5 | Python 进程被攻陷 | 策略层 |
| T6 | 前缀缓存投毒 / token 成本失控 | 每轮 |
| T7 | 升级批准被后续变异继承 | 加载期 |

### 7.2 补偿控制矩阵

| 威胁 | 控制 | 钉住它的测试 |
|---|---|---|
| T1 | 阶段 2–3 对活注册表 + `PlasmaPalette` | 每个 PLS-1xx/2xx 码一条负样本夹具 |
| T2 | **默认无写/无 shell/无浏览器动作**（ADR-4）+ 写类工具 `NeedsAuth` 逐次授权 + 阶段 4 模式检查（纵深防御，不是主防线） | "请求 run_bash 未批准 ⇒ provisional 且不可自动委派"；变异：把 FSWrite 加回 Palette 必须红 |
| T3 | 夹具不可变 + 出题谱系分离 + 适应度含生产项 | "改 fixtures 后 grade 不继承"；"同谱系出题 ⇒ 拒载" |
| T4 | 加载期重算 sha256，不符拒载 | "改一字节 ⇒ PLS-7xx" |
| T5 | Python 无工作区权限、无密钥、无网络（除经 `sample` 借引擎）；最坏产出坏 bundle 被编译拒 | 子进程契约测试：stdout 非 JSON / 退出码 3 ⇒ 降级不崩 |
| T6 | 字典序稳定 + `max_loaded_agents` + `catalog_line_chars` + 省略账 | 宽度/条数矩阵测试（照 TUI 的取向） |
| T7 | 升级批准**按 hash 记** | "新 hash ⇒ escalation_approved 重置" |

### 7.3 明确不防的

- **不防用户自己写坏 bundle 并手动 `plasma promote`**：那是显式的人类操作，与 `-y` 同性质。
- **不防真 LLM 在 5b 里被沙箱内容注入**：沙箱工作区是夹具作者提供的，属可信输入；
  但 5b 的运行**不计入生产统计**（`fixture_run=true`），且其 grade 判定只影响该 bundle。
- **不做语义级提示注入检测**：模式匹配只是纵深防御。主防线是**能力面本身够窄**。

---

## 8. 性能与成本模型

### 8.1 延迟预算

| 动作 | 预算 | 依据 |
|---|---|---|
| 阶段 1–4 | < 5ms | 约 40 行 JSON + 注册表 map 查询 + 一次 token 计数 |
| 阶段 5a | < 50ms | ≤8 步脚本循环，无 LLM |
| 阶段 5b | 10–60s / 夹具 | 真 LLM，1–3 次调用；受双预算约束 |
| 阶段 6 | < 5ms | 一次原子写 |
| Python spawn | 200–500ms | zipapp + stdlib import |
| `plasma sample` | ~100ms + LLM 延迟 | Go 二进制启动 |
| 启动加载 N 个 | N × 5ms，N ≤ 12 ⇒ < 60ms | 阶段 2–3 复核，跳过 5 |

**启动加载不得阻塞 TUI 首帧**：若实测超预算，改为 Director 首轮惰性加载（但必须保持字典序稳定）。

### 8.2 token 成本

- 每个加载的 plasma Agent ≈ 清单行 80–150 token + `delegate_<name>` 工具声明 100–200 token。
  12 个 ⇒ **约 2–4k token 的每轮固定前缀**。这与 blackboard 的
  `index_max_notes × index_line_chars` 是同一类成本，必须同样有预算与省略账。
- 能力清单约 15KB，**只在 spawn 前写一次文件**，不进 LLM 上下文。
- 5b 是主要变动成本，由 `behavioral_token_budget` 兜住。

### 8.3 缓存与增量

- bundle hash 未变 ⇒ 跳过阶段 1–4，直接复用 `report.json`（同 Go build cache 取向）。
- 5b 结果按 hash 缓存：同一 hash 不重复烧 token。
- 清单 hash 未变 ⇒ 复用上次 `manifest.json`。

---

## 9. 可观测性与降级

| 情况 | 行为 | 可见性 |
|---|---|---|
| 解释器缺失 / zipapp 提取失败 | `delegate_meta` 描述自动降级 + 点名修复；Meta 退回 Go 侧路径 | **工具描述里可见**（不许只 `slog.Debug`，`app.go:167-168` 的坑不重犯） |
| Python 超时 / 退出码非 0 | 报错 + 诊断；不阻塞 Director（走既有委派 idle/total 超时） | 工具输出 + `plasma-*` 日志 |
| 编译失败 | 结构化诊断（码 + JSON pointer + note） | `report.json` + TUI `plasma explain` |
| 5b 超预算 | grade 停 `provisional`，点名"未验证：预算耗尽" | **绝不静默当作通过** |
| hash 不符 / 注册表漂移 | 拒载 + PLS-7xx 点名哪个工具改名/删除 | 启动账目一行 |
| 遥测写入失败 | 不阻断委派，但留一条可见账目 | 坑 43：防线生效过却没人知道 = 静默丢失 |
| 加载超预算 | 截断 + 一行省略账 | 照 `module_docs.go` 取向 |

新增日志类别 `plasma-`（照 `~/.yagent/logs/` 既有 `app-`/`llm-`/`tool-`/`delegate-` 的形状）。
TUI 侧新增 `/plasma` 面板（list / explain / promote / proposals），复用既有列表与表单原语。

---

## 10. 测试策略

### 10.1 闸门

- 新增 `scripts/check_plasma_leaf.sh`：`internal/plasma` 的 `go list -deps` 里只准出现
  stdlib + `toolspec` + `util` + `projectid` + `tokenutil`（照 `check_blueprint_leaf.sh` 的形状）。
- 纳入 `scripts/check_all.sh`，守卫数从 11 → 12，**根 `AGENTS.md` §9.2 的清单与
  `check_agents_md_size.sh` 的体积棘轮要同步**（该棘轮零余量，需逐行换置）。

### 10.2 单元与 golden

- **每个诊断码一条能触发它的负样本夹具**（本仓纪律：校验规则本身必须能被触发）。
- `internal/plasma/testdata/diagnostics.golden`：一组坏 bundle → 精确诊断文本。
  改码表必须显式 `-update`，那个 diff 就是评审物。
- **变异测试**：每条新不变量，去掉它并确认有测试变红（例如把 `EffectFSWrite` 加回 `PlasmaPalette`）。
  变异用 `/tmp` 最小副本，**绝不 `git checkout` 还原未提交文件**。
- `testdata/` 落地后跑 `gofmt -l internal/plasma/testdata`（坑 41：Go 工具链不看 testdata）。

### 10.3 链路级 E2E

`internal/agents/e2e_plasma_test.go`：真实 dispatcher、真实 `WorkspaceGuard`、真实 store、
真实 rollout；**只 mock 模型**（5a 用脚本引擎，5b 用 `httptest` 假服务端钉字节形状）。
接线顺序照抄 `internal/app/app.go`。钉三条链路：
1. 需求 → generate → compile → register → delegate → telemetry（全链）。
2. 越权 bundle → PLS-210 → provisional → 首次委派触发授权 → 批准后解锁。
3. 进化候选 → 编译失败 → 诊断进 `lineage.rejected[]` → 下一轮生成读到反例。

脚本引擎**按 Agent 身份分派**（不是全局调用序号），且**必须先看 ctx**（忽略 ctx 的 mock 会掩盖
"取消没传播"）。

### 10.4 计时与隔离

- 计时走 seam（`compileTimeout`、`fixtureTimeout`、`spawnDelay`），**不 `time.Sleep`**。
- "扫目录再计数"的用例必须拥有那个目录（唯一 store 路径或 `t.Setenv("HOME", …)`），
  否则 `-count=4 -parallel 8` 下候选累加、`>=1` 恒真（坑 23）。
- Python 侧测试用 stdlib `unittest`，**不进 `check_all.sh`**（保持 Go-only 闸门），
  但 `plasma/tests/` 必须能独立跑，且 Go 侧有一条"zipapp 可被解释器加载"的冒烟测试。

---

## 11. 落地顺序

全程硬闸门：每个 P 完成后 `bash scripts/check_all.sh` 绿，且
`internal/agents/testdata/tool_surfaces.golden` 与 `TestSchemasLoadAndDescribeEveryEntry` 的
50 条基线**一个字节都不许变**（变了就说明 plasma 漏进了静态注册表或 `BuiltinProfiles()`）。

| P | 内容 | 依赖 | 为什么这个顺序 |
|---|---|---|---|
| **P0** | `internal/plasma` 骨架：IR + diag 码表 + 阶段 1–4 + golden 诊断 + `check_plasma_leaf.sh` | — | **纯 Go、无 Python、无夹具**，先把裁决者立起来 |
| **P1** | `PlasmaPalette` + 升级门（ADR-4）+ 负样本/变异测试 | P0 | 安全硬伤最先修；它不依赖任何后续 |
| **P2** | store（内容寻址 + hash + 夹具不可变 + GC）+ 加载期（字典序 + 预算 + 省略账）+ 遥测账本写入点 | P0 | 加载与遥测是进化的前提 |
| **P3** | `yagent plasma` CLI：`compile` / `sample` / `manifest` / `list` / `promote` | P0,P2 | Python 的依赖前提，先用假 bundle 打通 |
| **P4** | `FixtureRunner` 窄接口 + `agents` 侧实现（5a）+ grade 棘轮 | P2,P3 | 破循环依赖，provisional 这一档开始有意义 |
| **P5** | Python zipapp 骨架：`generate` / `author-fixtures` / `explain`；embedbin 接入 + 降级路径 | P3 | 最短端到端通路 |
| **P6** | 接线 `runMetaDesign` → 铸坊（保留 Go 侧 fallback） | P4,P5 | 此时 `delegate_meta` 行为被替换 |
| **P7** | 5b 行为夹具 + `judge` + `evolve` + 评分/选择 + Goodhart 三防线 | P4,P6 | verified/promoted 与自进化 |
| **P8** | 内建提案通道 + TUI `/plasma` 与 `/proposals` 面板 | P7 | 最后做：需要前面全部的可信度 |

---

## 12. 风险登记册

| ID | 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|---|
| R1 | 用户机器无 python3 | 中 | 低 | 降级到 Go 侧 Meta 路径 + 描述点名修复（ADR-8） |
| R2 | 5b token 成本失控 | 中 | 中 | 双预算 + 只在升 grade 时跑 + 按 hash 缓存（OD-3） |
| R3 | Goodhart：进化刷夹具分 | **高** | **高** | 夹具不可变 + 出题谱系分离 + 生产遥测项（ADR-5） |
| R4 | 前缀缓存失效 / 固定 token 膨胀 | 中 | 中 | 字典序 + 加载预算 + 省略账 |
| R5 | store 无限膨胀 | 中 | 低 | `store_ttl` + GC（`artifact` 无 GC 的坑不重犯） |
| R6 | 窄 palette 让 Meta 路径能力回退 | **确定** | 中 | 迁移说明 + 升级门；这是有意的安全收窄（ADR-4） |
| R7 | 诊断码两侧漂移 | 低 | 中 | 码表单一来源在 Go，Python 只读（§5.3） |
| R8 | 子进程管道 EOF 让超时失效 | 中 | 高 | `cmd.WaitDelay`（坑 46）+ `supervise` 等待 |
| R9 | 编译器与注册表规则漂移 | 低 | 高 | 阶段 3 **调用** `ValidateDynamic`/`ValidateProfile` 的规则而非复刻；聚合测试 |

---

## 13. 开放决策（待拍板）

| ID | 问题 | 选项 | 倾向 |
|---|---|---|---|
| **OD-1** | 编译器归 Go 后，Python 净表面积只剩约 1200 行策略代码（generate/evolve/judge/author-fixtures/explain/propose）。**是否仍值得引入第二语言运行时？** | (a) 保留 Python（策略迭代快、eval 生态）；(b) 全 Go，零第二语言 | **(a)**，但若你不打算高频迭代进化策略，(b) 严格更简单 |
| **OD-2** | 升级门默认值 | (a) 默认钉 provisional + 首次委派问用户；(b) 默认直接拒写/shell；(c) 默认允许但需 promoted | **(a)**（ADR-4 已按此写） |
| **OD-3** | 5b 何时跑 | (a) 只在升 verified 前；(b) 每次编译；(c) nightly 批量 | **(a)**，(c) 作为补充 |

---

## 14. 引用索引（全部核实于 2026-11-09，改前坐标）

| 引用 | 内容 |
|---|---|
| `internal/agents/director/types.go:22` | `CustomAgent` 纯数据五字段 |
| `internal/agents/director/meta_handler.go:35 / :42 / :71` | 注册表类型 / 构造为空 / `List` 按插入序 |
| `internal/toolspec/profile.go:11 / :23-27 / :174` | `Profile` 结构 / `Names` 通道注释 / `MatchNames` |
| `internal/toolspec/profiles.go:6-8 / :32 / :33` | golden 钉住画像 / Director `Deny` / **`Palette` 含 FSWrite\|Shell** |
| `internal/toolspec/spec.go:108 / :166 / :168-223 / :225-252` | `Validate` / 动态名形态 / `ValidateDynamic` / `ValidateProfile` |
| `internal/tools/surface.go:413` | `Surface.Add` 拒重复注册 |
| `internal/agents/director_surface.go:182 / :261 / :284 / :338` | `runMetaDesign` / `runCustomAgent` / `registerCustomAgent` / `customAgentPromptSection` |
| `internal/agents/director.go:172 / :336-366 / :442-460 / :454 / :462 / :809-822 / :816` | metaHandler 构造 / `executeCustomAgent` / `delegationOutcome` / `recordDelegationOutcome` / `applyEnhancedCommander` / prompt 拼装顺序 / custom 段位置 |
| `internal/agents/executor.go:29 / :59 / :184` | `ExecutorConfig` / `DefaultExecutorConfig` / `RunAgentLoop` |
| `internal/agents/result.go:58 / :197 / :208` | `deliverableText` / `FinalizeResultFull` / `FormatForDirector` |
| `internal/agents/e2e_engine_test.go:23 / :60 / :96` | `agentScriptedEngine` / `newScripted` / `Model()=="scripted-mock"` |
| `internal/tokenutil/tokenutil.go:30` | `EstimateTokens`（cl100k_base + 粘滞降级） |
| `internal/thinklink/thoughtplan.go:195` | `ThoughtPlan.Validate()` |
| `internal/llm/router.go:52 / :139 / :176` | 默认基线 / 按小写名查 / 合并配置 |
| `internal/util/atomic.go:41` | 唯一 tmp+rename 内核 |
| `internal/memory/rollout_types.go:298-303` | `stop_reason` 词表 |
| `internal/embedbin/embedbin.go:153` | `isExecutableName` |
| `internal/app/app.go:167-168` | 提取失败只 Debug |
| `main.go:79` | `setupCmd`（不装配 Agent/MCP 的子命令先例） |

## 15. 实施记录

（待补：每个 P 完成后追加实际落点行号、与本文的偏差、以及"方案写错才发现"的条目。
按本仓惯例，方案落地后本文可能被删——届时不变量必须已同步进
`internal/plasma/AGENTS.md` 与根 `AGENTS.md` §5.3/§9.2/§11。）
