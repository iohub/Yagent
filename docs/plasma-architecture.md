# Plasma 架构设计 — Go 机制层 / Python 策略层

> **状态**：设计定稿待实施（2026-11-09）。
>
> **读者**：架构师。§15 是引用索引，全部行号已逐条核实（改前坐标）。
>
> **范围**：新增 Agent 编译验证、持久化存储、自进化通道。
> **不改**：50 条静态工具基线、`schemas.json`、`tool_surfaces.golden`、7 个内建 Agent 的 `.prompt.md`。

---

## 0. 摘要

系统分成**在线路径**与**离线路径**，而不是"Go 层 / Python 层"：

- **在线路径（全 Go）**：用户通过 `delegate_meta` 请求新 Agent → MetaAgent 生成（流式 → TUI）→
  Plasma 编译器扩充 + 验证（进程内）→ 失败则诊断反馈重试 → 注册为 `provisional` → 执行（流式 → TUI）。
  **Python 不在这条路径上。** 流式、TUI 交互、事件总线全部复用既有机制，零退步。
  **在线只到 `provisional`**——行为验证（5b）是分钟级的离线职责，不在交互路径上跑。
- **离线路径（Python 策略 + Go 机制）**：定时/手动触发 → Python 进化/出题/评审（一次性子进程，
  批处理，无需流式）→ Go 编译器验证 → Go 行为夹具（5b）→ grade 升到 `verified`/`promoted`。
  `yagent plasma sample` 只在这条路径上使用（完整 JSON 即可，无人看 TUI）。
- **编译器即扩充器**：LLM 只产出 5 字段最小 spec（name / prompt / tools / task / thinking），
  编译器填充 policy / runtime / contract / fixtures 默认值并验证。LLM 不需要知道 IR 全貌。
- **安全底线**：生成式 Agent 走独立窄 `PlasmaPalette`（默认无写、无 shell），升级经用户授权。
- **两项前置重构**（不是"复用既有"，是新增生产代码，见 ADR-12 / ADR-13）：
  脚本引擎从测试文件提升为生产包；`ExecutorResult`/`AgentResult` 扩展出 steps/usage/stop_kind 指标。

一句话：**在线全 Go 保流式，离线 Python 做策略，编译器是两条路径共用的裁决者。**

---

## 1. 问题陈述

### 1.1 目标

| # | 目标 | 可测判据 |
|---|---|---|
| G1 | 核心能力留在 Go，不因策略迭代而重编译 | 改 Python 策略代码后 `go build` 产物字节不变 |
| G2 | 自进化在 Python 侧实现 | 变异/出题/评审/选择四个动作的代码 100% 在 `plasma/` |
| G3 | 在线生成保留流式与 TUI 交互 | `delegate_meta` 期间 TUI 看到 `ai_chunk` 流式输出（与今天一致） |
| G4 | Agent 生成经过类编译器的静态检测 | 每个诊断码有一条能触发它的负样本夹具；诊断文本进 golden |
| G5 | 生成质量可保证 | `verified` 必须通过行为夹具（5b，真 LLM）；只有 verified 以上可自动委派；在线产出止于 `provisional` |
| G6 | 动态层不可能绕过内核权限 | 越权 bundle 编译失败并点名；写/shell 升级必须走用户授权 |
| G7 | 两层交互高效简约 | 离线：无常驻进程、无协议；单次 = 一次 spawn + 一份 JSON |

### 1.2 非目标

- 不实现第二个 Agent 执行循环（复用 `RunAgentLoop`，`executor.go:184`）。
- 不让 Python 直连 provider、持密钥、碰工作区、起 shell。
- 不引入第三方 Python 依赖。
- 不做 bundle 热重载。
- 不自动改写内建 Agent（只出提案）。
- 不追求"用静态分析预测 LLM 行为"。
- **不把 Python 塞进在线生成路径**（那会打断流式与 TUI 交互）。

### 1.3 成功判据

1. 含幻觉工具名的 bundle → 编译失败，诊断指出 `tools[i]` 与最近候选名，**反馈给 MetaAgent 重试**。
2. 请求 `run_bash` 的 bundle → grade 钉 `provisional`，除非升级且用户批准。
3. 编译通过但行为夹具红 → grade 停 `provisional`，不可自动委派。
4. 篡改 store 一字节 → 加载期 hash 校验失败并拒载。
5. 删掉 Python 解释器 → 在线生成不受影响（MetaAgent 是 Go）；离线进化降级并点名。
6. 进化器改已 verified 版本的 fixtures → store 拒绝，新 hash grade 不继承。
7. `delegate_meta` 期间 TUI 看到流式输出（`ai_chunk` 事件），与今天 MetaAgent 行为一致。
8. `yagent plasma test --fixture s1` 在**生产二进制**里能跑结构夹具（脚本引擎不再是 test-only）。
9. 一次委派结束后 `telemetry.jsonl` 多一行，且 `steps`/`tokens_in`/`stop_reason` **非零值**
   （证明指标真的从执行循环传出来了，而不是写了个空壳账本）。

---

## 2. 代码现状分析

### 2.1 在线生成路径（今天已经完整工作）

```
delegate_meta → runMetaDesign (director_surface.go:182)
  → MetaAgent.Run (meta.go:44)
      opts.StreamHandler → Publisher.Publish("ai_chunk")     ← 流式到 TUI
      Publisher.Publish("ai_stream_start")                   ← TUI 建条目
      LLM.GenerateContent(ctx, messages, nil, opts)          ← 单次 LLM 调用
      Publisher.PublishWithMetadata("ai_stream_end")         ← TUI 定稿
  → ParseMetaAgentOutput (meta_handler.go:107)               ← 解析 5 字段 JSON
  → registerCustomAgent (director_surface.go:284)            ← Surface.Add
  → runCustomAgent (director_surface.go:261)
      MatchNames(tools, ProfileDirector.Palette, deps)       ← 校验工具名
      surfaceFor(Profile{Names}, deps)                       ← 装配工具面
  → executeCustomAgent (director.go:336)
      RunAgentLoop(ctx, cfg)                                 ← 流式执行，TUI 可见
```

**两个流式阶段**：生成（MetaAgent）+ 执行（RunAgentLoop），都通过 `Publisher.Publish("ai_chunk")`
到事件总线，TUI 实时渲染（`tui_update_tasks.go:380/406/469`）。

**今天缺什么**：`ParseMetaAgentOutput` 只查 JSON 格式与两个必填字段（`agent_name`、`agent_design`），
不查工具名是否存在、不查权限、不查 prompt 质量。retry 只说"你的 JSON 无效"
（`director_surface.go:207`），不给结构化诊断。

### 2.2 MetaAgent 的最小输出（编译器的输入）

MetaAgent prompt（`meta.prompt.md`）要求 LLM 产出 5 字段 JSON：

```json
{ "thinking": "…", "agent_name": "Security Auditor",
  "agent_design": "<完整 system prompt>",
  "tools_used": ["read_file","search_by_regex","agent_exit"],
  "task_for_agent": "…" }
```

这就是编译器的**源语言**——LLM 只负责创意核心（名字、prompt、工具选择），
结构化配置（policy / runtime / contract / fixtures）由编译器填充默认值。

### 2.3 工具池广告与权限现状

`renderCustomAgentToolPool`（`deps.go:97`）从 `ProfileDirector.Palette` 派生工具池表格。
**Palette 含 `EffectFSWrite | EffectShell`**（`profiles.go:33`），所以 MetaAgent 今天被广告了
写文件与 shell 工具。Director 自己 Deny 这两项（`:32`），却把它们放进给子 Agent 的 Palette。

`MatchNames`（`profile.go:174`）已挡幻觉工具名（返回 `unknown`）与越权工具名（返回 `rejected`），
且**自动追加 `agent_exit`**（`:196`）——LLM 不需要显式包含它。

### 2.4 执行路径的硬编码

`executeCustomAgent`（`director.go:336-366`）写死 `MaxSteps=15`、`StopOnFinish=true`。
其余来自 `DefaultExecutorConfig()`（`executor.go:59`）与 Director 自身配置。
Plasma 之后这些值来自 bundle 的 `runtime` 段。

### 2.5 事件总线与 TUI 的进程边界

`MessageDispatcher` 是**每任务**创建的（`app.go:486` `IntegrateMessaging`），TUI 消费它。
子进程（Python / `plasma sample`）没有父进程的 dispatcher——即使装配了 messaging，
创建的 dispatcher 也没有消费者。**这是"Python 不能流式到 TUI"的根本原因。**

### 2.6 可复用的既有接缝

| 接缝 | 位置 | Plasma 怎么用 |
|---|---|---|
| 纯数据 Agent 描述 | `director/types.go:22` | 编译产物的目标形状 |
| 按名挑选 + Palette 校验 | `toolspec/profile.go:174` | Resolve 直接调 `MatchNames` |
| 三级校验器 | `toolspec/spec.go:108 / :168 / :225` | 能力不变量的唯一来源 |
| 运行期工具注册 | `tools/surface.go:413` | 加载期 `Surface.Add` |
| 共享执行循环 | `agents/executor.go:184` | 夹具执行与生产执行同一条 |
| 结果定型 | `agents/result.go:197 / :58` | 行为夹具判据取交付全文 |
| 委派完成接缝 | `director.go:454 / :462` | 遥测账本写入点 |
| 原子写唯一内核 | `util/atomic.go:41` | store 落盘 |
| 单一 token 计数器 | `tokenutil/tokenutil.go:30` | 预算判据，与压缩同源 |
| 路由基线 | `llm/router.go:52 / :139 / :176` | `runtime.tier` 的落点 |
| 不装配 Agent 的子命令 | `main.go:79` | `yagent plasma` 的取向 |
| MetaAgent 流式 | `meta.go:63-68 / :95 / :117` | 在线生成路径**原样保留** |
| retry 循环 | `director_surface.go:199-253` | 插入编译诊断反馈 |
| 委派统一汇聚点 | `director.go:698`（`directorToolRunner.Call`） | 遥测账本的**正确**写入点（见 §2.7） |

### 2.7 两处"看似能复用、实际不能"的地方

这两条是设计成立的前置条件，必须新增生产代码，**不是复用既有**：

**(a) 脚本引擎只存在于测试文件。** `agentScriptedEngine` / `scriptStep` 只出现在 9 个 `_test.go`
（`e2e_engine_test.go`、`e2e_lifecycle_test.go`、`e2e_blueprint_test.go`、`e2e_pause_test.go`、
`e2e_replay_test.go`、`e2e_handoff_test.go`、`e2e_resume_test.go`、`e2e_mcp_test.go`、
`resume_degrade_test.go`）。生产代码里 `llm.Engine`（`llm/engine.go:229`）**没有任何可编程实现**。
而 5a 结构夹具必须在生产路径上跑（在线编译、`yagent plasma test` CLI）⇒ 必须把它提升为生产包（ADR-12）。

提升时还要修一处脆弱性：`agentIdentity`（`e2e_engine_test.go:41-58`）靠**各家 System Prompt 的首行
措辞**认人，且只硬编码了四个内建（director/repo/coding/chat），default 返回 `"unknown"`。
plasma Agent 的 prompt 是 LLM 生成的，首行不可预测 ⇒ 嗅探必然落到 `"unknown"`。
5a 是**单 Agent** 夹具，根本不需要身份分派，所以提升后的引擎要支持显式身份注入（ADR-12）。

**(b) 执行指标不在结果结构体里。** `ExecutorResult` 只有 `Text` + `History`（`executor.go:66-69`）；
`AgentResult`（`types.go:14`）里 `Steps|Tokens|Usage|StopReason|StopKind` **grep 零命中**。
token usage 只进事件总线 metadata（`executor.go:570-577`）与 rollout，不回结果结构体。
而遥测账本要记 `steps`/`tokens_in`/`tokens_out`/`stop_reason` ⇒ 必须扩展这两个结构体（ADR-13）。

**遥测写入点也必须改**：文档早期版本写的 `applyEnhancedCommander`（`director.go:462`）/
`recordDelegationOutcome`（`:454`）**不是三条分支的共同汇聚点**。实测 `delegateTo`
（`director_surface.go:137-176`）有三个分支：`meta` 在 `:144` 提前 return、custom agent 在 `:151`
提前 return，**只有 builtin 分支**才走到 `recordDelegationOutcome` + `writeDelegateNote` +
`applyEnhancedCommander`（`:170-175`）。plasma Agent 走的正是提前 return 的 custom 分支。
真正的汇聚点是 `directorToolRunner.Call`（`director.go:698`）：`:727` 建 outcome 载体、
`:744` 调工具、`:748-755` 已有 `outcome.has` 判断与 `RecordDelegation`——账本写在这里。

---

## 3. 架构总览

### 3.1 在线路径（全 Go，流式，TUI 可见）

```
用户 → Director → delegate_meta → runMetaDesign
  │
  ├─ MetaAgent.Run(ctx, task)                    ← Go, 流式 ai_chunk → TUI
  │    产出 5 字段最小 JSON
  │
  ├─ plasma.Elaborate(json) → Bundle             ← Go, 进程内
  │    填充 policy/runtime/contract/fixtures 默认值
  │
  ├─ plasma.Compile(ctx, bundle) → Report        ← Go, 进程内, 约 5–15ms（§9）
  │    阶段 1-4: Parse → Resolve → Typecheck → Consistency
  │    阶段 5a: 自动生成结构夹具 + 脚本引擎验证
  │
  ├─ 失败？→ 诊断反馈 MetaAgent 重试              ← 流式 ai_chunk → TUI
  │    "[COMPILATION ERRORS] PLS-101: unknown tool…"
  │    （替代今天的 "your JSON was invalid"）
  │
  ├─ 通过？→ registerPlasmaAgent                 ← Surface.Add, grade=provisional
  │
  └─ executeCustomAgent(ctx, bundle, task)       ← Go, RunAgentLoop, 流式 → TUI
```

**在线止于 `provisional`。** 行为验证（5b）需要真 LLM（10–60s/夹具）而出题在离线，
在线路径上**无题可跑**；且 5b 的耗时会撞 `delegate_idle_timeout`（默认 60s）。
所以在线只跑 5a（确定性、毫秒级），产出立即可用但需人工点名委派的 `provisional` Agent。
升 `verified` 是离线职责（或用户手动 `yagent plasma test --behavioral`）。

**关键**：MetaAgent.Run 的流式机制（`StreamHandler` → `ai_chunk`）**原样保留**。
编译器是进程内函数调用，不引入子进程，不打断流式。retry 反馈从"JSON 无效"升级为结构化诊断。

### 3.2 离线路径（Python 策略 + Go 机制，批处理）

```
定时/手动触发
  │
  ├─ [Py] evolve                                 ← 一次性子进程, 无流式需求
  │    读 telemetry.jsonl → 评分 → 变异
  │    调 yagent plasma sample 借引擎（完整 JSON）
  │    产出候选 bundle 文件
  │
  ├─ [Go] plasma.Compile(候选)                   ← 进程内, 阶段 1-4 + 5a
  │    失败 → 诊断进 lineage.rejected[]
  │
  ├─ [Py] author-fixtures                        ← 一次性子进程, 独立谱系
  │    给候选出行为夹具
  │
  ├─ [Go] 5b 行为夹具                            ← 真 LLM, 后台运行
  │    + [Py] judge 评审                         ← 一次性子进程
  │
  ├─ [Go] emit → grade → index 更新
  │
  └─ [Py] propose                                ← 内建 Agent 提案, 永不自动 apply
```

**关键**：离线操作是批处理（generate 秒级、evolve 分钟级），无人看 TUI，
`plasma sample` 返回完整 JSON 即可。子进程模型（ADR-2）在这条路径上没有流式矛盾。

### 3.3 职责矩阵

| 关注点 | 在线（Go） | 离线 Go | 离线 Python |
|---|:--:|:--:|:--:|
| Agent 生成（LLM 调用） | ● MetaAgent | — | — |
| 编译六阶段 | ● | ● | — |
| 流式 → TUI | ● | — | — |
| 工具注册表 / Effect | ● | ● | — |
| token 计数 | ● | ● | — |
| 结构夹具（5a）自动生成 + 执行 | ● | ● | — |
| 行为夹具（5b）执行 | — | ● | — |
| 行为夹具出题 | — | — | ● |
| 行为评审（judge） | — | — | ● |
| 进化（变异/评分/选择） | — | — | ● |
| LLM 调用（离线） | — | ● sample CLI | 借 |
| 存储 / 版本 / hash | ● | ● | — |
| 遥测写入 | ● | ● | — |
| 遥测读取与评分 | — | — | ● |
| 权限裁决 / 授权 | ● | ● | — |
| 内建 Agent 提案 | — | — | ● |

### 3.4 为什么在线路径不用 Python

| 维度 | 在线用 Python（子进程） | 在线全 Go |
|---|---|---|
| 流式到 TUI | **不可能**：子进程无父 dispatcher | ● MetaAgent 既有机制 |
| retry 反馈 | 跨进程传诊断，复杂 | 进程内字符串拼接，简单 |
| 延迟 | +200-500ms spawn | 0（进程内） |
| 代码改动 | 重写 runMetaDesign 为子进程驱动 | 在既有 retry 循环里插入编译步骤 |
| 降级 | Python 缺失 → 在线生成不可用 | Python 缺失 → 在线不受影响 |

MetaAgent 今天只有 ~150 行 Go（`meta.go`），做一次 LLM 调用 + 解析 JSON。
把它搬到 Python 增加复杂度（子进程、无流式、降级）而收益为零。
**Python 的价值在离线策略迭代，不在在线生成。**

### 3.5 三条通道（离线专用）

| 通道 | 方向 | 载体 |
|---|---|---|
| 能力清单 | Go→Python | spawn 前写 `manifest.json`，路径经 argv |
| 候选与产物 | 双向 | 文件（bundle / 诊断 / 评分 / 提案） |
| 遥测 | Go→Python | `~/.yagent/agents/telemetry.jsonl` |

在线路径不需要这些通道——编译器在进程内直接读活注册表。

---

## 4. 编译器即扩充器

### 4.1 源语言：MetaAgent 的 5 字段 JSON

```json
{ "thinking": "…", "agent_name": "Security Auditor",
  "agent_design": "<完整 system prompt>",
  "tools_used": ["read_file","search_by_regex"],
  "task_for_agent": "…" }
```

LLM 只负责创意核心。`agent_exit` 不需要显式包含——`MatchNames` 自动追加（`profile.go:196`）。

### 4.2 扩充规则（Elaborate）

| 源字段 | IR 字段 | 扩充规则 |
|---|---|---|
| `agent_name` | `name` | `toSnakeCase`（既有函数，`director_surface.go:225`） |
| `agent_name` | `display_name` | 原样 |
| `agent_design` | `prompt.system` | 原样 |
| — | `prompt.lang` | 从 `[agent] lang` 配置读 |
| `tools_used` | `tools` | 原样 + `agent_exit`（MatchNames 自动追加） |
| `tools` 的 Effect 位 | `policy.effects_required` | 从注册表派生（`Spec.Effect.Names()`） |
| — | `policy.deny` | 默认 `["fs_write","shell"]`（PlasmaPalette 之外） |
| — | `runtime.max_steps` | 默认 15（与今天 `executeCustomAgent` 一致） |
| — | `runtime.stop_on_finish` | 默认 true |
| — | `runtime.llm_timeout` | 从 `[llm] timeout` 配置读 |
| — | `runtime.tier` | 默认 `"balanced"` |
| — | `runtime.compression` | 从 `[enhanced_commander]` 配置读 |
| — | `contract.must_exit_with` | 默认 `["reason"]`——`agent_exit` schema 的 `required` 只有 `reason`（实测：top-level props = `reason`/`handoff`/`blueprint`）。`handoff.conclusion` 是**可选但强烈推荐**，写进 lint 规则（PLS-301）而不是 required |
| — | `contract.thought_format` | 默认 true |
| — | `fixtures.structural` | **自动生成**（§4.3） |
| — | `fixtures.behavioral` | 空（离线 Python 出题后填充；**在线路径不跑 5b**） |
| — | `quality.grade` | `"provisional"`（初始） |
| `thinking` | — | 记日志，不进 IR |
| `task_for_agent` | — | 传给执行路径，不进 IR |

### 4.3 自动生成的结构夹具（5a）

编译器从 IR 自动生成，不需要 Python，不需要真 LLM。**依赖 ADR-12 的生产级脚本引擎**
（`internal/llm/scripted`）——今天的脚本引擎只在测试文件里，生产路径跑不了（§2.7a）。

| 夹具 | 脚本 | 断言 |
|---|---|---|
| **exit-contract** | ① 一条 assistant 文本消息（`"审计结论：a.go:42 存在拼接注入"`）② `{tool:"agent_exit", args:{reason:"done", handoff:{conclusion:"…",status:"ok"}}}` | 退出成功；`deliverableText` 返回的是**那条 assistant 文本**而不是 `{"finished":true,…}` JSON；handoff 形状合规 |
| **step-budget** | 重复 `{tool:"thinking", args:{}}` `max_steps+1` 次 | 循环在 `max_steps` 停止；`stop_kind == "max_steps"` |
| **tool-containment** | 无脚本（静态断言） | Surface 只含声明的工具 + `agent_exit`；无 `PlasmaPalette` 之外的工具 |
| **palette-containment** | 无脚本（静态断言） | 所有工具的 `Effect ⊆ PlasmaPalette`（或有 `escalation_approved`） |

**exit-contract 的脚本为什么必须含 assistant 文本**：`deliverableText`（`result.go:58-70`）在
`isAgentExitResult` 为真时**倒序找最后一条非空 assistant 消息**，找不到就回落 `exec.Text`
——也就是 `ExecuteAgentExit` 返回的那段 `{"finished":true,"reason":…}` JSON。
纯脚本只发 `agent_exit` 调用时，"deliverableText 非空"这条断言**恒真**（回落值也非空），
是个永远不会失败的测试。所以脚本必须带一条 assistant 文本，断言必须比对**内容**而不是非空。

**身份分派**：5a 是单 Agent 夹具，用**显式固定身份**构造脚本引擎，不走 `agentIdentity` 的
prompt 首行嗅探（那套只认四个内建，plasma Agent 必然落到 `"unknown"`，§2.7a）。

前两个走 `RunAgentLoop` + 脚本引擎（确定性）；后两个是静态断言。

### 4.4 完整 IR（扩充后）

```jsonc
{ "schema_version": 1,
  "name": "security_auditor", "display_name": "Security Auditor",
  "version": "0.1.0",
  "capability_lock": "sha256:…",              // 溯源元数据
  "lineage": { "parent_hash": null, "mutation": "initial-generation" },
  "prompt": { "system": "…", "lang": "Chinese" },
  "tools": ["read_file","search_by_regex","agent_exit"],
  "policy": { "effects_required": ["fs_read","search_regex","flow"],
              "deny": ["fs_write","shell"], "workspace_scoped": true },
  "runtime": { "max_steps": 15, "stop_on_finish": true, "llm_timeout": "120s",
               "tier": "balanced", "compression": {"enable":true},
               "max_parallel_readonly_tools": 4 },
  "contract": { "must_exit_with": ["reason"], "thought_format": true },
  "fixtures": {
    "provenance": { "author": "plasma-compiler-auto", "lineage": "compiler" },
    "structural": [ /* §4.3 自动生成 */ ],
    "behavioral": []
  },
  "quality": { "grade": "provisional", "score": 0, "runs": 0, "lint": [] } }
```

离线进化产出的候选 bundle 可以是完整 IR（Python 填充所有字段），
也可以是最小 spec（编译器扩充）。编译器对两种输入一视同仁。

---

## 5. 关键设计决策（ADR）

### ADR-1：编译器归 Go，在线路径全 Go

- **问题**：六阶段中五阶段是对活注册表的确定性查询；在线生成需要流式到 TUI。
- **决定**：编译器在 Go 进程内；在线生成路径（MetaAgent → 编译 → 注册 → 执行）全 Go。
- **理由**：① 跨进程造出注册表漂移失败类；② token 计数必须用 `tokenutil`（与压缩同源）；
  ③ 编译器在 Go 才能被 `check_all.sh`/golden/变异测试覆盖；
  ④ MetaAgent 的流式机制（`StreamHandler` → `ai_chunk` → TUI）原样保留，零 UX 退步；
  ⑤ 子进程无父 dispatcher，流式不可能（`app.go:486`）。
- **后果**：Python 只在离线路径；`capability_lock` 是溯源元数据而非安全门。

### ADR-2：离线 Python 是一次性子进程

- **问题**：离线策略层的真实负载是批处理。
- **决定**：一次性子进程（argv + JSON + exit code），无常驻。
- **理由**：批处理不需要长连接；常驻换来生命周期/重连/readLoop 死锁五类问题而收益为零。
  离线无人看 TUI，`plasma sample` 返回完整 JSON 即可。
- **补偿控制**：`cmd.WaitDelay`（坑 46）；超时经 `supervise`。

### ADR-3：双层夹具

- **问题**：脚本引擎的 `Model()` 返回 `"scripted-mock"`（`e2e_engine_test.go:96`），
  生成的 prompt 根本没被执行。只用脚本测的是接线，不是质量。
- **决定**：5a 结构夹具（脚本，确定性，**编译器自动生成**）+ 5b 行为夹具（真 LLM + rubric）。

| 层 | 引擎 | 测什么 | 何时跑 | 门禁 |
|---|---|---|---|---|
| 5a | 脚本（`internal/llm/scripted`，ADR-12） | 接线：工具解析、exit 契约、步数预算、palette 包含 | 每次编译（在线 + 离线） | 阻断 provisional |
| 5b | 真 LLM | prompt 质量：真模型在沙箱里是否产出预期交付物 | **只在离线**升 verified 前 / 手动 CLI | 阻断 verified |

- **5a 自动生成**（§4.3）：在线路径不依赖 Python 就能产出 provisional Agent。
- **5b 出题**：离线 Python `author-fixtures`（独立谱系）。
- **在线不跑 5b**：在线路径上 `fixtures.behavioral` 为空（无题可跑），且 5b 的 10–60s 会撞
  `delegate_idle_timeout`（默认 60s）。在线止于 provisional，升 verified 是离线/手动职责。
- **补偿控制**：5b 有 token 与墙钟双预算；超预算 ⇒ 停 provisional 并点名，绝不静默通过。

### ADR-4：PlasmaPalette + 升级门

- **问题**：`ProfileDirector.Palette` 含 `EffectFSWrite | EffectShell`（`profiles.go:33`）。
  自动委派的生成式 Agent 继承它 = 权限升级漏洞。
- **决定**：独立窄 palette + 显式升级门。

```go
PlasmaPalette = EffectFSRead | EffectSearchRegex | EffectCodeSemantic | EffectCodeGraph |
                EffectContext | EffectFlow | EffectToolLoad |
                EffectCogReflect | EffectCogDeep | EffectSkill | EffectExternalRead
// 不含：FSWrite · Shell · Browser* · Checkpoint · ExternalWrite · Delegate
```

- **升级路径**：请求窄 palette 之外的能力 → `PLS-210`，grade 上限 provisional →
  首次委派经 `UserConfirmManager` 明示批准 → 批准后该 hash 记 `escalation_approved=true`。
- **批准按 hash 记**：进化出新版本 ⇒ 批准失效，要重新批。
- **不进 `BuiltinProfiles()`**：走 `Profile{Names}` 按名挑选通道，golden 不动。
- **`EffectToolLoad` 是一处有意的行为变更**：实测今天的 Palette（`profiles.go:33-34`）
  **不含** `EffectToolLoad` ⇒ 今天 Meta 设计的 Custom Agent 拿不到 `load_tool`，
  任何被降到 Cold 档的工具**永远取不回来**。`PlasmaPalette` 加上它是**修这个 bug**，
  不是"只读族默认"。落地时要在 P1 的变更说明里点名，并配一条夹具
  （"Cold 工具经 load_tool 可取回"）。
- **工具池广告同步收窄**：`renderCustomAgentToolPool`（`deps.go:97`）改用 `PlasmaPalette`，
  MetaAgent 不再被广告写/shell 工具。注意这**改的是 `{{TOOL_POOL_TABLE}}` 的注入内容**
  （`meta.go:45` 做替换），`meta.prompt.md` **文件本身不动**——但 MetaAgent 运行时看到的
  prompt 实质变了，评审时要按"prompt 变更"对待。
- **迁移影响**：今天 Meta 设计的 Agent 能拿 `run_bash`；之后默认拿不到，需升级批准。
  有意的安全收窄。

### ADR-5：内容寻址存储 + 夹具不可变

- 存储键 = `sha256(归一化 bundle)`；回滚 = 指向旧 hash。
- `fixtures[]` 与 `contract` 一经落盘即不可变；改夹具 ⇒ 新 hash，grade 不继承。
- 出题谱系分离：同 lineage 的变异体不得由自己的出题动作验证。

### ADR-6：遥测独立账本，写入点在 `directorToolRunner.Call`

- **问题**：rollout 是重放账本，格式服务于 `/resume`；拿它做分析会把进化器耦合到重放格式变更上。
  而 `delegationOutcome`（`director.go:442-460`）只是 ctx 内的当次载体，没有账本。
- **决定**：写一行紧凑 outcome 到 `~/.yagent/agents/telemetry.jsonl`：

```jsonc
{ "ts":"…", "agent":"security_auditor", "bundle_hash":"sha256:…", "grade":"verified",
  "session":"…", "delegation_id":"…", "stop_reason":"agent_exit",
  "steps":7, "tokens_in":18400, "tokens_out":2100,
  "degraded":false, "escalated":false, "note_ref":"…", "fixture_run":false }
```

- **写入点 = `directorToolRunner.Call`（`director.go:698`）的 `:748` 附近**，
  即 `ad.Call` 返回后、已有的 `outcome.has` 判断与 `RecordDelegation` 旁边。
  **不是** `applyEnhancedCommander` / `recordDelegationOutcome`——那两个只在 builtin 分支上跑，
  meta 与 custom 分支在 `delegateTo` 里提前 return（`director_surface.go:144 / :151`），
  而 plasma Agent 走的正是 custom 分支（§2.7）。
  `directorToolRunner.Call` 是三个分支唯一的共同汇聚点，且此处已经握有
  `outcome.result`（AgentResult）、`err`、`name`（工具名）、`callID`（delegation_id）。
- **字段来源需要 ADR-13**：`steps`/`tokens_in`/`tokens_out`/`stop_reason` 今天**不在**任何结果
  结构体里（§2.7b），必须先扩展 `ExecutorResult` 与 `AgentResult`。
- **理由**：① 一行 vs 整个会话，解析成本差两个数量级；② 重放格式变更不再打断进化器；
  ③ `fixture_run` 标记让 5a/5b 的运行**不污染生产统计**。
- **补偿控制**：账本写入失败**不得阻断委派**（照 artifact `Save` 失败的取向：降级、不返回 error）；
  但必须留一条可见账目（坑 43）。验收判据见 §1.3 第 9 条：字段必须是**非零值**，
  否则就是写了个空壳账本。

### ADR-7：离线 LLM 经 `yagent plasma sample` 借引擎

- **只用于离线路径**。在线 LLM 调用走 MetaAgent / RunAgentLoop（既有引擎栈，流式）。
- 离线 `plasma sample` 返回完整 JSON，无流式需求（无人看 TUI）。
- 引擎栈（路由/回退/熔断/在飞闸门/token 账）只有一份；Python 不持密钥。

### ADR-8：stdlib-only zipapp

- Python 纯标准库，单文件 `plasma.pyz`，`go:embed` 进 `dist/bin`。
- **仍需 PATH 上有 python3（≥3.9）**。zipapp 消除依赖，不消除解释器。
- 解释器缺失 ⇒ **在线生成不受影响**（MetaAgent 是 Go）；离线进化降级并点名。
- 降级必须在工具描述里可见（不许只 `slog.Debug`，`app.go:167-168` 的坑不重犯）。

### ADR-9：三档 grade 棘轮

| 档 | 准入 | 能力 |
|---|---|---|
| `provisional` | 阶段 1–4 + 5a（自动生成）通过 | 可人工点名委派；不可自动委派 |
| `verified` | 上述 + 5b 通过 + 无未批准升级 | 可自动委派 |
| `promoted` | verified + N 次生产成功（默认 5） | 列表优先呈现 |

掉档自动；回滚 = 载旧 hash。

### ADR-10：编译器即扩充器

- **问题**：LLM 擅长创意（prompt 设计、工具选择），不擅长填结构化配置。
- **决定**：LLM 产出 5 字段最小 spec（与今天 MetaAgent 输出格式一致），
  编译器扩充为完整 IR（§4.2）。离线进化可直接产出完整 IR，编译器对两种输入一视同仁。
- **理由**：① 不改 MetaAgent prompt 的输出格式（只收窄工具池）；
  ② 默认值从配置与注册表派生，单一来源；
  ③ LLM 不需要知道 policy/runtime/contract/fixtures 的存在。

### ADR-11：编译诊断反馈 retry

- **问题**：今天 retry 只说"你的 JSON 无效"（`director_surface.go:207`），不给结构化诊断。
- **决定**：编译失败时，诊断（PLS-xxx 码 + JSON pointer + note）拼进 retry 上下文，
  MetaAgent 下次生成时看到具体错误。
- **理由**：幻觉工具名、越权请求、参数名错配都有精确诊断，比"JSON 无效"有用得多。
- **实现**：在 `runMetaDesign` 的 retry 循环里，`ParseMetaAgentOutput` 之后插入
  `plasma.Elaborate` + `plasma.Compile`；失败则 `retryTask = task + diagnostics`。

### ADR-12：脚本引擎提升为生产代码（`internal/llm/scripted`）

- **问题**：5a 结构夹具必须在生产路径上跑（在线编译、`plasma test` CLI），但脚本引擎只存在于
  9 个 `_test.go`（§2.7a）。生产代码里 `llm.Engine` 无任何可编程实现。
- **选项**：(a) 提升为生产包，测试与生产共用；(b) 5a 改为只做静态断言 + 一次真 LLM 冒烟；
  (c) 在 plasma 里再写一个脚本引擎。
- **决定**：**(a)**，新增 `internal/llm/scripted`（实现 `llm.Engine`）。
- **理由**：(b) 让 5a 变成"要花钱、不确定、进不了 CI"的东西，等于取消 5a；
  (c) 直接违反坑 33 的纪律（**测试与生产必须共用同一个载体**——两份实现各自漂走，
  八条用例全绿而生产一次都没跑过）。(a) 是唯一同时满足"确定性"与"单一载体"的选项。
- **落点选 `internal/llm/scripted` 而不是 `internal/plasma/scripted`**：它要实现 `llm.Engine`
  就必须 import `llm`，而 `internal/plasma` 是叶子包（不许 import llm，§7.1）。
  放 llm 子包后，`agents` 侧的 `FixtureRunner` 实现 import 它，plasma 只通过窄接口间接使用。
- **提升时必须改的两处**：
  1. **身份注入**：`agentIdentity`（`e2e_engine_test.go:41-58`）靠 prompt 首行嗅探且只认四个内建，
     plasma Agent 必然落 `"unknown"`。提升后改为：构造时可注入显式身份解析器
     （5a 用固定身份；e2e 测试保留嗅探解析器以兼容既有用例）。
  2. **`CallOptions` 加 `AgentName`**：`llm.CallOptions`（`llm/engine.go:216`）今天只有
     MaxTokens/Temperature/StreamHandler/ReasoningEffort 四个字段，`GenerateContent` 的参数里
     没有 Agent 身份。加一个 `AgentName string`，由 executor 在构造 opts 时填 `cfg.AgentName`
     （`executor.go:263`）——这同时让嗅探成为**兜底**而不是唯一手段。
- **后果**：9 个 e2e 测试文件要改用新包（删除各自的本地实现）。这是**真实的改动量**，
  不是"复用既有"。P5 的工期要按此估（它是整个计划里最大的单个改动项）。
- **补偿控制**：新包必须有一条测试钉住"按身份分派而非全局序号"（既有纪律：全局序号会被
  循环里的补偿动作打断）；`GenerateContent` **必须先看 ctx**（忽略 ctx 的 mock 会掩盖
  "取消没传播"，`e2e_engine_test.go:69-72` 的注释已写明）；步骤必须在锁外跑
  （`sync.Mutex` 不可重入，锁内回调就是自锁死，`:96-98`）。

### ADR-13：扩展 `ExecutorResult` / `AgentResult` 携带执行指标

- **问题**：遥测账本要记 `steps`/`tokens_in`/`tokens_out`/`stop_reason`，但这四个值今天
  **不在任何结果结构体里**（§2.7b）：`ExecutorResult` 只有 `Text`+`History`（`executor.go:66-69`），
  usage 只进事件总线 metadata（`:570-577`）与 rollout。
- **选项**：(a) 扩展两个结果结构体；(b) 账本只记能拿到的字段（agent/hash/grade/err/note_ref），
  指标留空；(c) 从 rollout 反查。
- **决定**：**(a)**。
- **理由**：(b) 会让适应度函数退化成"只看成功/失败"，进化器失去最有信号量的两个维度
  （步数与 token 成本）——而 §6.6 的评分函数正是围绕它们设计的；
  (c) 直接违反 ADR-6（不耦合重放账本）。
  另外这些指标本来就该在结果里：今天任何程序化消费者（不只是 plasma）都拿不到"这次委派花了多少步"。
- **改动面（实测）**：`ExecutorResult` 的构造点共 9 处，其中 7 处是错误路径的零值返回
  （`executor.go:279/379/400/622/632/839`、`meta.go:176`），**只有 2 处带数据**
  （`executor.go:736` 纯文本收尾、`:787` agent_exit 收尾）——只需在这两处填新字段。
  `AgentResult` 的透传改 `FinalizeResultFull`（`result.go:197`）一处。
- **`stop_kind` 的词表已有单一来源**：`memory/rollout_types.go:298-303`
  （`StopKindMaxSteps`/`StopKindPlainText`/`StopKindAgentExit`/…）。
  **必须复用该词表**，且注意坑 18：`agents.classifyStop` 与 `director.StopKindOf` 是两份同形实现，
  加字段时要先给聚合测试喂新输入，再让两边实现去追它。
- **补偿控制**：一条测试断言"跑完一次带 `agent_exit` 的委派后，`AgentResult.Steps > 0` 且
  `Usage.TotalTokens > 0` 且 `StopKind == StopKindAgentExit`"——**三个都非零值**，
  否则就是加了字段没人填（§1.3 第 9 条）。

---

## 6. 详细设计

### 6.1 编译器六阶段

| # | 阶段 | 判据来源 | 挡住什么 | 码族 |
|---|---|---|---|---|
| 1 | **Parse** | IR schema + 归一化 | 结构错、必填缺失 | PLS-0xx |
| 2 | **Resolve** | 活注册表 `MatchNames` | 幻觉工具名（did-you-mean） | PLS-1xx |
| 3 | **Typecheck** | PlasmaPalette + `ValidateDynamic`/`ValidateProfile` 同源规则 + knob 范围 | 权限越界、能力不一致、无 agent_exit | PLS-2xx |
| 4 | **Consistency** | prompt↔schema 参数名对账 + `tokenutil` 预算 + `ThoughtPlan.Validate` | 参数名错配（坑 38）、dead selection、提示词吃压缩阈值、`<thought>` 静默丢失 | PLS-3xx |
| 5 | **Verify** | 5a 自动生成结构夹具 + `internal/llm/scripted`（ADR-12）；5b 真 LLM（**只离线**） | 接线错 / prompt 无效 | PLS-5xx |
| 6 | **Emit** | `util.AtomicWrite` + sha256 + grade + 夹具不可变 | 落盘失败、grade 越权继承 | PLS-6xx |

**加载期**：store → 重算 hash → 活注册表复核阶段 2–3 → `LinkPlan` → `Surface.Add`。
hash 不符或注册表漂移 ⇒ 拒载（PLS-7xx）。加载期**不跑阶段 4**（不含 token 计数），
所以启动加载比编译快一个量级（§9）。

### 6.2 在线生成流程（runMetaDesign 改造）

```go
// director_surface.go:182 — 改造后的 runMetaDesign
for attempt := 0; attempt < maxRetries; attempt++ {
    retryTask := task
    if attempt > 0 {
        retryTask = task + "\n\n[COMPILATION ERRORS]\n" + lastDiagnostics
    }
    metaResult, err := a.MetaAgent.Run(ctx, retryTask)   // 流式 → TUI（不变）
    // ... error handling ...

    bundle, err := plasma.Elaborate(metaResult.Text)      // 5 字段 → 完整 IR
    if err != nil { lastDiagnostics = err.Error(); continue }

    report, err := a.plasmaCompiler.Compile(ctx, bundle)  // 阶段 1-4 + 5a
    if report.HasErrors() {
        lastDiagnostics = report.DiagnosticsText()        // 结构化诊断反馈
        continue
    }

    ca := bundle.ToCustomAgent()                          // → director.CustomAgent
    a.registerPlasmaAgent(ca, bundle, report)             // Surface.Add + store, grade=provisional
    // ... execute + return（与今天一致，但 runtime 来自 bundle）...
}
```

**改动量**：在既有 retry 循环里插入 3 行（Elaborate / Compile / 诊断反馈）。
MetaAgent.Run 的流式、registerCustomAgent 的 Surface.Add、executeCustomAgent 的 RunAgentLoop
**全部不变**。但 `executeCustomAgent`（`director.go:336-366`）的 `MaxSteps=15` /
`StopOnFinish=true` 两处硬编码要改为读 `bundle.runtime`（落地顺序 **P8** 含此项）。
**在线不跑 5b**（ADR-3）。

### 6.3 诊断格式

```
bundle.tools[3]: error[PLS-101]: unknown tool "read_filez"
                 note: did you mean "read_file"?
bundle.policy.effects_required: error[PLS-210]: "shell" is outside PlasmaPalette
                 note: grade capped at provisional; user approval required
bundle.prompt.system: error[PLS-302]: mentions parameter "path" for "search_replace_in_file"
                 note: schema declares "file_path"
```

码表单一来源在 Go（`internal/plasma/diag.go`）。warning 进 `quality.lint[]`，作为进化适应度项。

### 6.4 存储与加载

```
~/.yagent/agents/
  {name}/{hash}/bundle.json      # 归一化 IR（不可变；主键 = hash）
  {name}/{hash}/report.json      # 诊断 + grade + 各阶段结果（可重写：grade 会变）
  {name}/{hash}/fixtures/        # 沙箱工作区快照（不可变）
  {name}/{hash}/meta.json        # 可变状态：semver / lineage / fixture_provenance /
                                 #   escalation_approved / 加载计数 / promoted 计数
  index.json                     # name → 当前 hash（唯一可变指针，原子写）
  telemetry.jsonl                # ADR-6 账本（追加）
  proposals/                     # 内建 Agent 提案（等人批）
```

**不可变 vs 可变的分界**：`bundle.json` 与 `fixtures/` 一经落盘即不可变（改一个字节 ⇒ hash 变 ⇒
新目录）。`report.json` 与 `meta.json` 是**可变状态**——grade 会升降、`escalation_approved`
会被用户批准、promoted 计数会累加。把可变状态放进 `bundle.json` 会改 hash，所以必须分文件。

- **加载时机**：Director 构造期（`metaHandler` 今天在 `director.go:172` 建为空）。
- **字典序**加载与呈现（`customAgentPromptSection` 拼在 blueprint/skills/mcp 之前，
  `director.go:816`，顺序抖动会 invalidate 前缀缓存）。
- **预算**：`max_loaded_agents=12` + `catalog_line_chars=120`，超预算截断 + 省略账。
- **运行中不热替换**；版本变更在下一轮 Surface 重建时生效。
- **GC**：`store_ttl=720h`，清理未被 index 指向且超 TTL 的 hash 目录。

### 6.5 权限模型

```
PlasmaPalette（只读族） ⊂ ProfileDirector.Palette（含 FSWrite|Shell）

请求超出 → PLS-210, grade 上限 provisional
         → 首次委派经 UserConfirmManager 批准
         → 该 hash 记 escalation_approved=true
```

- 批准**按 hash 记**，落点是 `{name}/{hash}/meta.json` 的 `escalation_approved`（**不进 bundle.json**，
  那会改 hash）。进化出新版本 ⇒ 新 hash ⇒ 批准自然失效，要重新批。
- 写类工具 `NeedsAuth` 逐次授权照旧（由 `Effect` 的 `authEffects` 派生）；升级门管的是
  "这个 Agent 能不能拿到这类工具"，不是"这次调用要不要问"。两层不互相替代。
- YOLO（`-y`）照旧绕过一切授权；账本记 `escalated=true`。

### 6.6 遥测与评分

**写入点**：`directorToolRunner.Call`（`director.go:698`）的 `:748` 附近——`ad.Call` 返回后、
已有的 `outcome.has` 判断与 `RecordDelegation` 旁边。这是 meta / custom / builtin 三条分支
唯一的共同汇聚点（ADR-6、§2.7）。

**字段来源**：`agent`/`bundle_hash`/`grade`/`delegation_id`/`note_ref`/`err` 当场可得；
`steps`/`tokens_in`/`tokens_out`/`stop_reason` 需要 ADR-13 扩展 `ExecutorResult`/`AgentResult`
后从 `outcome.result` 取。`fixture_run` 由调用方标记（5a/5b 的运行置 true）。

```jsonc
{ "ts":"…", "agent":"…", "bundle_hash":"…", "grade":"…",
  "stop_reason":"agent_exit", "steps":7, "tokens_in":18400, "tokens_out":2100,
  "degraded":false, "escalated":false, "fixture_run":false }
```

评分（Python 侧）：成功率 + 1/步数 + 1/token + 批准率 - 降级率 - lint 警告。
只统计 `fixture_run=false`；样本 < `promote_min_runs`（默认 5）不升 promoted。

### 6.7 进化闭环

```
telemetry → [Py] score → [Py] mutate → 候选
  → [Go] compile 1-4 + 5a（失败 → rejected[]）
  → [Py] author-fixtures（独立谱系）
  → [Go] 5b + [Py] judge
  → [Go] emit → verified → 生产 → promoted / 掉档
```

Goodhart 三防线：夹具不可变 + 出题谱系分离 + 适应度含生产项。
进化默认关闭（`evolve_enable=false`）。

### 6.8 内建 Agent 提案

`plasma.pyz propose` 产出 diff 到 `proposals/`，永不自动 apply。TUI `/proposals` 审批。

---

## 7. 接口契约

### 7.1 Go API（`internal/plasma`，叶子包）

```go
// 允许 import：stdlib + toolspec + util + projectid + tokenutil
// 禁止：agents（循环）、llm、mcp、messaging、config

func Elaborate(metaJSON string) (*Bundle, error)           // 5 字段 → 完整 IR
func (c *Compiler) Compile(ctx, *Bundle) (*Report, error)  // 六阶段

type FixtureRunner interface {                              // 破循环依赖
    RunStructural(ctx, *LinkPlan, Fixture) (Outcome, error)
    RunBehavioral(ctx, *LinkPlan, Fixture) (Outcome, error)
}

type LinkPlan struct {
    Agent   CustomAgentView       // 纯数据，agents 侧转 director.CustomAgent
    Profile toolspec.Profile
    Config  ExecutorConfigView    // 纯数据，agents 侧转 ExecutorConfig
    Hash    string
    Grade   Grade
}
```

**分工边界**：`CustomAgentView` / `ExecutorConfigView` 是 plasma 侧的纯数据，由 `agents` 侧转换成
`director.CustomAgent` 与 `agents.ExecutorConfig`。**plasma 不 import agents / director / tools**，
所以它只产出 `toolspec.Profile`——真正的 `tools.NewSurface`（既有封装 `surfaceFor`，`deps.go:50`）
由 `agents` 侧的 `FixtureRunner` 实现与加载路径负责。
即：**plasma 决定"要哪些工具"，agents 决定"怎么装配成工具面"。**

### 7.2 CLI

| 命令 | 语义 | 装配 |
|---|---|---|
| `yagent plasma compile --bundle <path> [--behavioral]` | 六阶段 | 阶段5 需要时 |
| `yagent plasma sample --messages <path> --tier <t>` | 借引擎（**离线专用**） | 引擎 |
| `yagent plasma test --bundle <path> --fixture <id>` | 单跑夹具 | 是 |
| `yagent plasma list / promote / manifest` | store 管理 | 否 |

### 7.3 Python 进程契约（离线专用）

```
argv:    python3 plasma.pyz <command> --manifest <path> --in <path> --out <path>
stdout:  仅一份 JSON；日志走 stderr
exit:    0 成功 · 1 业务失败 · 2 用法错 · 3 超时
```

六个命令：`generate` / `author-fixtures` / `judge` / `evolve` / `explain` / `propose`。
全部无状态、一次性。

### 7.4 配置

```toml
[plasma]
enable = true
python = ""                        # 空 = PATH python3
store = "~/.yagent/agents"
max_loaded_agents = 12
catalog_line_chars = 120
compile_timeout = "60s"
structural_fixture_timeout = "60s"
behavioral_fixture_timeout = "10m"
behavioral_token_budget = 60000
sample_timeout = "10m"
auto_delegate_min_grade = "verified"
promote_min_runs = 5
store_ttl = "720h"
evolve_enable = false
evolve_interval = "30m"
proposal_dir = "~/.yagent/agents/proposals"
```

急停 `YAGENT_PLASMA=0`。每个键有能因改值而失败的测试。

---

## 8. 安全与信任模型

| 威胁 | 控制 | 测试 |
|---|---|---|
| T1 越权 bundle | 阶段 2–3 + PlasmaPalette | 每个 PLS-1xx/2xx 一条负样本 |
| T2 提示注入 → 写/shell | 默认无写无 shell + NeedsAuth 逐次授权 | "run_bash 未批准 ⇒ provisional"；变异：加回 FSWrite 必须红 |
| T3 Goodhart | 夹具不可变 + 出题谱系分离 + 生产项 | "改 fixtures ⇒ grade 不继承" |
| T4 磁盘篡改 | 加载期 sha256 校验 | "改一字节 ⇒ PLS-7xx" |
| T5 Python 被攻陷 | 无工作区/密钥/网络；最坏产出坏 bundle 被编译拒 | 子进程契约测试 |
| T6 前缀缓存 | 字典序 + 预算 + 省略账 | 宽度/条数矩阵 |
| T7 升级批准继承 | 按 hash 记 | "新 hash ⇒ 重置" |

不防：用户手动 promote 坏 bundle（显式人类操作）；语义级注入检测（主防线是能力面够窄）。

---

## 9. 性能与成本

### 9.1 实测基线（token 计数是本设计的性能主导项）

`tokenutil.EstimateTokens` 本机实测（cl100k_base，2026-11-09）：

| 输入 | tokens | 耗时 |
|---|---|---|
| 首次调用（含编码器加载） | 501 | **51.4ms** |
| 8000 token 英文 prompt | 8001 | **5.0ms** |
| 17000 token 中文 | 17000 | 4.9ms |
| 20000 token 英文 | 20001 | 14.1ms |

**结论**：阶段 4 的单次 token 计数在典型 prompt 长度下**就是 5ms 量级**，不是可以忽略的常数。
首次编译另付 ~50ms 编码器加载（`tokenutil` 的 `sync.Once`，进程内只付一次）。

### 9.2 延迟预算（含实测依据）

| 动作 | 预算 | 依据 |
|---|---|---|
| Elaborate | < 1ms | 纯字段映射，无 IO |
| 阶段 1–3 | < 2ms | 约 40 行 JSON 解析 + 注册表 map 查询 |
| 阶段 4 | **5–15ms** | 一次 token 计数（§9.1 实测）+ 正则对账 |
| 阶段 5a | < 50ms | ≤8 步脚本循环，无 LLM；**待实测**（依赖 ADR-12 落地） |
| 阶段 6 | < 5ms | 一次原子写 |
| **在线一次编译合计** | **约 10–25ms**（首次 +50ms） | 相对 MetaAgent 的 LLM 调用（秒级）可忽略 |
| 5b（离线） | 10–60s / 夹具 | 真 LLM，1–3 次调用；受双预算约束 |
| Python spawn（离线） | 200–500ms | zipapp + stdlib import |
| 启动加载 N 个 | N × <2ms，N ≤ 12 ⇒ <25ms | **只复核阶段 2–3，不跑阶段 4** ⇒ 不含 token 计数 |

在线生成的额外延迟 = 一次编译（约 10–25ms）+ 首次的编码器加载（~50ms，进程内一次）。
**不是零**，但相对 LLM 调用可忽略。**启动加载不得阻塞 TUI 首帧**：若实测超预算，
改为 Director 首轮惰性加载（但必须保持字典序稳定）。

### 9.3 token 成本

- 每个加载的 plasma Agent ≈ 清单行 80–150 token + `delegate_<name>` 工具声明 100–200 token。
  12 个 ⇒ **约 2–4k token 的每轮固定前缀**。这与 blackboard 的
  `index_max_notes × index_line_chars` 是同一类成本，必须同样有预算与省略账。
- 能力清单约 15KB，**只在离线 spawn 前写一次文件**，不进 LLM 上下文。
- 5b 是主要变动成本，由 `behavioral_token_budget` 兜住。

### 9.4 缓存与增量

- bundle hash 未变 ⇒ 跳过阶段 1–4，直接复用 `report.json`（同 Go build cache 取向）。
  这也让"首次编译付 50ms 编码器加载"只发生一次。
- 5b 结果按 hash 缓存：同一 hash 不重复烧 token。
- 清单 hash 未变 ⇒ 复用上次 `manifest.json`。

---

## 10. 可观测性与降级

| 情况 | 行为 |
|---|---|
| Python 缺失 | **在线不受影响**；离线降级 + 描述点名 |
| 编译失败 | 结构化诊断 → retry 反馈（在线）/ report.json（离线） |
| 5b 超预算 | 停 provisional，点名"未验证" |
| hash 不符 | 拒载 + PLS-7xx |
| 遥测写入失败 | 不阻断委派 + 可见账目 |
| 加载超预算 | 截断 + 省略账 |

新增 `plasma-` 日志类别。TUI `/plasma` 面板（list / explain / promote / proposals）。

---

## 11. 测试策略

- **闸门**：`check_plasma_leaf.sh`（stdlib + toolspec + util + projectid + tokenutil），
  纳入 `check_all.sh`，守卫 11→12。`internal/llm/scripted` 另需一条"只准 import llm + stdlib"的约束。
- **每个诊断码一条负样本**；`diagnostics.golden` 钉住文本。
- **变异测试**：去掉不变量确认测试变红（如把 `EffectFSWrite` 加回 `PlasmaPalette`、
  把 `escalation_approved` 改成按 name 记）。变异用 `/tmp` 最小副本，绝不 `git checkout` 还原。
- **ADR-13 的指标断言**：跑完一次带 `agent_exit` 的委派后，`AgentResult.Steps > 0` **且**
  `Usage.TotalTokens > 0` **且** `StopKind == StopKindAgentExit`——三个都必须非零值，
  否则就是"加了字段没人填"。`stop_kind` 的新输入要**先喂聚合测试**再让
  `agents.classifyStop` 与 `director.StopKindOf` 去追（坑 18）。
- **ADR-12 的脚本引擎断言**：按身份分派而非全局序号；`GenerateContent` 先看 ctx
  （取消必须传播）；步骤在锁外跑（`sync.Mutex` 不可重入）。9 个 e2e 文件改用新包后必须全绿。
- **5a 夹具自身的卫生**：exit-contract 夹具必须断言 `deliverableText` 的**内容**而不是非空
  （纯脚本只发 agent_exit 时回落值也非空，那条断言恒真——§4.3）。
- **E2E**（`e2e_plasma_test.go`）：真实 dispatcher/guard/store/rollout，只 mock 模型。
  钉四条链路：① 生成→编译→retry→注册（provisional）→执行→遥测；② 越权→升级→批准→verified；
  ③ 进化→编译失败→`lineage.rejected[]`；④ 篡改 store→加载期拒载。
- **在线流式**：断言 `delegate_meta` 期间有 `ai_chunk` 事件（与今天 MetaAgent 一致）。
- **性能回归**：一条基准钉住"一次编译 < 30ms（8k token prompt、编码器已热）"，
  防止阶段 4 被塞进更多全量 token 计数。
- 计时走 seam；扫目录用例拥有自己的目录；Python 测试不进 `check_all.sh`。

---

## 12. 落地顺序

全程硬闸门：每个 P 完成后 `check_all.sh` 绿 + golden / 50 条基线不变。
**含两项前置重构**（ADR-12 脚本引擎提升、ADR-13 指标扩展）——它们不是"复用既有"，
是新增/改动生产代码，工期要按此估。

三条可并行的轨道在 P8 汇合：

```
轨道 A（编译器）  P0 → P1 → P2 ─────────────────┐
轨道 B（遥测）    P3 → P4 ──────────────────────┼→ P8 在线接线 → P9 Python → P10 进化 → P11 TUI
轨道 C（夹具）    P5 → P6 → P7 ─────────────────┘
```

| P | 内容 | 依赖 | 备注 |
|---|---|---|---|
| **P0** | `internal/plasma` 骨架：IR + `Elaborate` + diag 码表 + 阶段 1–3（Parse/Resolve/Typecheck）+ golden 诊断 + `check_plasma_leaf.sh` | — | 纯 Go，无外部依赖 |
| **P1** | `PlasmaPalette` + 升级门 + `renderCustomAgentToolPool` 改用窄 palette + 负样本/变异测试 | P0 | **含 `EffectToolLoad` 修复**（ADR-4）：今天 Custom Agent 拿不到 `load_tool`，Cold 工具永远取不回 |
| **P2** | 阶段 4 Consistency：token 预算（`tokenutil`）+ prompt↔schema 参数名对账 + `ThoughtPlan.Validate` 契约 | P0 | 性能主导项在此（§9.1） |
| **P3** | **ADR-13**：`ExecutorResult`/`AgentResult` 加 `Steps`/`Usage`/`StopKind` + `FinalizeResultFull` 透传 | — | 只需改 2 处带数据的构造点（`executor.go:736/787`）；`stop_kind` 复用 `rollout_types.go:298-303` 词表；**先给聚合测试喂新输入**再让 `classifyStop`/`StopKindOf` 追（坑 18） |
| **P4** | store（内容寻址 / hash / 夹具不可变 / GC）+ 加载期（字典序 / 预算 / 省略账）+ 遥测写入点 | P0,P3 | 写入点在 `directorToolRunner.Call:748`（**不是** `applyEnhancedCommander`，§2.7） |
| **P5** | **ADR-12**：`internal/llm/scripted` 生产包 + `CallOptions.AgentName` + 9 个 e2e 测试改用它 + 显式身份注入 | — | 改动量最大的一项：触碰 9 个测试文件；必须保留"先看 ctx""按身份分派""步骤锁外跑"三条纪律 |
| **P6** | `FixtureRunner` 窄接口 + `agents` 侧实现（构建 Surface）+ 5a 四夹具自动生成 + grade 棘轮 | P4,P5 | 5a 的 exit-contract 脚本必须含 assistant 文本（§4.3） |
| **P7** | `yagent plasma` CLI：compile / sample / test / manifest / list / promote | P6 | `sample` 只服务离线路径 |
| **P8** | **在线接线**：`runMetaDesign` 插入 Elaborate/Compile/诊断 retry；`executeCustomAgent` 改读 `bundle.runtime`（去掉 `MaxSteps=15`/`StopOnFinish=true` 硬编码） | P2,P6 | 在线路径唯一改动点；**在线止于 provisional**（不跑 5b） |
| **P9** | Python zipapp：generate / author-fixtures / explain + embedbin 接入 + 降级路径 | P7 | 解释器缺失 ⇒ 在线不受影响，离线降级并点名 |
| **P10** | 5b 行为夹具 + `judge` + `evolve` + Goodhart 三防线 | P6,P9 | `evolve_enable` 默认关 |
| **P11** | 内建提案通道 + TUI `/plasma` `/proposals` 面板 | P10 | 需要前面全部的可信度 |

**P8 的改动量**：`runMetaDesign` 的 retry 循环里插入 3 行（Elaborate / Compile / 诊断反馈）
+ `executeCustomAgent` 两处硬编码改为读 bundle。MetaAgent.Run 的流式、registerCustomAgent 的
`Surface.Add`、`RunAgentLoop` **全部不变**。

---

## 13. 风险登记册

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 无 python3 | 中 | 低 | **在线不受影响**（MetaAgent 是 Go）；离线降级并点名 |
| 5b 成本 | 中 | 中 | 双预算 + 只在升 grade 时跑 + hash 缓存 |
| Goodhart | 高 | 高 | 夹具不可变 + 出题分离 + 生产项 |
| 前缀缓存 | 中 | 中 | 字典序 + 预算 + 省略账 |
| store 膨胀 | 中 | 低 | TTL + GC |
| Meta 能力回退 | 确定 | 中 | 有意收窄 + 升级门；同时修 `load_tool` 缺失（净收益） |
| 子进程 EOF | 中 | 高 | WaitDelay + supervise |
| 编译器规则漂移 | 低 | 高 | 调用 ValidateDynamic/ValidateProfile 而非复刻 |
| **P5 触碰 9 个 e2e 测试文件** | **确定** | **中** | 提升时逐字保留三条纪律（先看 ctx / 按身份分派 / 步骤锁外跑）；先让 9 个文件全绿再动 plasma |
| **P3 改核心结果结构体** | **确定** | **中** | 只有 2 处带数据的构造点要填；`stop_kind` 复用既有词表；聚合测试先喂新输入（坑 18） |
| token 计数拖慢编译 | 低 | 低 | 实测 5ms/8k token（§9.1）；hash 缓存让重复编译跳过阶段 4 |

---

## 14. 开放决策

| ID | 问题 | 倾向 |
|---|---|---|
| OD-1 | Python 净表面积（六个离线命令：generate / author-fixtures / judge / evolve / explain / propose，估算 800–1500 行，**未实测**）是否仍值得引入第二语言运行时？ | 保留（策略迭代快、eval 生态）；若不高频迭代进化策略则全 Go 严格更简单。**这是本设计唯一可能再被推翻的根基** |
| OD-2 | 升级门默认值 | 钉 provisional + 首次委派问用户（ADR-4 已按此写） |
| OD-3 | 5b 何时跑 | **只在离线升 verified 前 + 手动 CLI**；在线不跑（ADR-3 已定，不再是开放项） |
| OD-4 | P5 提升脚本引擎时，9 个 e2e 测试是**一次全改**还是**新旧并存过渡**？ | 一次全改（并存就是两份同形实现，坑 33）；但这会让 P5 成为整个计划里最大的单个 commit |

---

## 15. 引用索引（核实于 2026-11-09）

| 引用 | 内容 |
|---|---|
| `internal/agents/meta.go:44 / :63-68 / :95 / :117` | MetaAgent.Run / StreamHandler→ai_chunk / ai_stream_start / ai_stream_end |
| `internal/agents/meta.prompt.md` | 5 字段 JSON 输出格式 + 工具池模板 |
| `internal/agents/deps.go:50 / :97` | surfaceFor / renderCustomAgentToolPool（从 Palette 派生） |
| `internal/agents/director_surface.go:137-176 / :144 / :151 / :170-175 / :182 / :199-253 / :261 / :284` | delegateTo 三分支 / meta 提前 return / custom 提前 return / builtin 收尾 / runMetaDesign / retry 循环 / runCustomAgent / registerCustomAgent |
| `internal/agents/director.go:172 / :336-366 / :442-460 / :454 / :462 / :698 / :727 / :744 / :748-755 / :816` | metaHandler 构造 / executeCustomAgent / delegationOutcome / recordDelegationOutcome / applyEnhancedCommander / **directorToolRunner.Call** / outcome 载体 / ad.Call / **遥测写入点** / customAgentPromptSection 位置 |
| `internal/agents/director/meta_handler.go:35 / :42 / :71 / :107` | 注册表 / 构造为空 / List 插入序 / ParseMetaAgentOutput |
| `internal/agents/director/types.go:22` | CustomAgent 纯数据五字段 |
| `internal/agents/executor.go:29 / :59 / :66-69 / :184 / :263 / :266 / :570-577 / :736 / :787` | ExecutorConfig / DefaultExecutorConfig / **ExecutorResult 只有 Text+History** / RunAgentLoop / opts 构造点 / StreamHandler / usage 只进事件 metadata / 纯文本收尾返回 / agent_exit 收尾返回 |
| `internal/agents/result.go:52-70 / :58 / :197` | deliverableText 注释与回落语义 / 函数 / FinalizeResultFull |
| `internal/agents/types.go:14` | AgentResult（**无 Steps/Tokens/Usage/StopReason/StopKind**，grep 零命中） |
| `internal/agents/e2e_engine_test.go:23 / :41-58 / :60 / :69-72 / :96-98` | agentScriptedEngine / **agentIdentity 靠 prompt 首行嗅探，只认四个内建** / newScripted / 先看 ctx 的纪律 / 步骤锁外跑的纪律 |
| `internal/agents/deps.go:50 / :97` | surfaceFor / renderCustomAgentToolPool（从 Palette 派生） |
| `internal/agents/meta.go:45` | `{{TOOL_POOL_TABLE}}` 替换点 |
| `internal/toolspec/profile.go:11 / :23-27 / :174 / :196` | Profile / Names 通道 / MatchNames / 自动追加 agent_exit |
| `internal/toolspec/profiles.go:6-8 / :32 / :33-34` | golden 钉住 / Director Deny / **Palette 含 FSWrite\|Shell 且不含 EffectToolLoad** |
| `internal/toolspec/spec.go:108 / :168 / :225` | Validate / ValidateDynamic / ValidateProfile |
| `internal/toolspec/schemas.json`（`agent_exit`） | `required: ["reason"]`；top-level props = `reason`/`handoff`/`blueprint`（**无 summary/deliverable 字段**） |
| `internal/tools/surface.go:413` | Surface.Add 拒重复 |
| `internal/tokenutil/tokenutil.go:30` | EstimateTokens（cl100k_base + 粘滞降级）；**实测 8k token = 5.0ms、首次 = 51.4ms**（§9.1） |
| `internal/thinklink/thoughtplan.go:195` | ThoughtPlan.Validate() |
| `internal/llm/engine.go:216 / :226 / :229` | CallOptions 四字段（**无 AgentName**）/ StreamHandler / Engine 接口 |
| `internal/llm/router.go:52 / :139 / :176` | 默认基线 / 按小写名查 / 合并配置 |
| `internal/memory/rollout_types.go:298-303` | StopKind 词表（`stop_kind` 必须复用） |
| `internal/util/atomic.go:41` | 唯一原子写内核 |
| `internal/app/app.go:167-168 / :389 / :486` | 提取失败只 Debug / MetaAgent 装配 / IntegrateMessaging（**dispatcher 每任务创建，子进程拿不到**） |
| `internal/tui/tui_update_tasks.go:380 / :406 / :469` | ai_stream_start / ai_chunk / ai_stream_end 消费 |
| `internal/config/config.go:91 / :649` | MetaRetryCount / 默认值 |
| `main.go:79` | setupCmd 先例 |
| 脚本引擎分布（实测） | `agentScriptedEngine`/`scriptStep` **只出现在 9 个 `_test.go`**：e2e_engine / e2e_lifecycle / e2e_blueprint / e2e_pause / e2e_replay / e2e_handoff / e2e_resume / e2e_mcp / resume_degrade |

## 16. 实施记录

（待补。方案落地后本文可能被删——届时不变量须已同步进
`internal/plasma/AGENTS.md` 与根 `AGENTS.md`。）
