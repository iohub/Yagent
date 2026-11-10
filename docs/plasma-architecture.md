# Plasma 架构设计 — Go 机制层 / Python 策略层

> **状态**：设计定稿待实施（2026-11-09）。
>
> **读者**：架构师。§15 是引用索引，全部行号已逐条核实（改前坐标）。
>
> **范围**：新增 Agent 生成/编译/验证/存储/进化通道，**并把 Go 侧 Meta-Agent 的生成职责
> 整体移交给 Python**。
> **不改**：50 条静态工具基线、`schemas.json`、`tool_surfaces.golden`、
> 其余 6 个内建 Agent 的 `.prompt.md`。

---

## 0. 摘要

按**机制 / 策略**切分，而不是按"静态 / 动态"切分：

- **Go 拥有全部机制**：工具注册表与执行器、守卫、引擎栈、记忆与压缩、事件总线、UI、
  **Plasma 编译器**、**LLM 调用本身**。机制是确定性的、安全承重的、必须与被检查的注册表同进程。
- **Python 拥有全部策略**：**Agent 生成策略**（多轮精炼、few-shot 检索、工具预选）、
  夹具出题、行为评审、进化变异与选择、内建 Agent 提案。策略是启发式的、需要快速迭代
  而不应触发 Go 重编译。
- **在线路径用 trampoline 连接两层**：Python 跑生成策略，需要 LLM 时写请求文件并 `exit 4`；
  Go 读请求、**带 `StreamHandler` 调 LLM（流式 → TUI）**、写响应文件、重新拉起 Python `--resume`。
  于是"生成策略在 Python"与"流式到 TUI"两个要求同时成立。
- **离线路径用一次性子进程**：批处理（进化/出题/评审），无人看 TUI，`plasma sample` 返回完整 JSON。
- **编译器即扩充器**：Python 可产出最小 spec 或完整 IR，编译器填充默认值并验证。
- **无 Go 侧生成器兜底**：照 `browserAvailable` 先例，Python 缺失 ⇒ `delegate_meta` 从工具面消失
  并说明原因，**不保留第二个生成器**（那正是坑 33 的"两份同形实现"）。

一句话：**Python 提议，Go 裁决，LLM 调用与流式始终在 Go。**

### 0.1 与四条原始要求的对照

| 要求 | 本设计如何满足 |
|---|---|
| 核心静态能力放 Go | 内核全部 + 编译器 + LLM 调用 + 流式投递（§3.3 职责矩阵） |
| 动态 Agent 能力（自定义 agent、自进化）放 Python | **生成策略** + 出题 + 评审 + 进化 + 提案，全在 `plasma/`（§3.1/§3.2） |
| 两层交互高效简约 | 无协议、无常驻连接：文件 + exit code；在线 trampoline，离线一次性（§3.5） |
| Plasma 编译保证质量 | 六阶段编译 + 双层夹具 + 三档 grade + 夹具不可变 + 诊断码 golden（§5/§6） |

---

## 1. 问题陈述

### 1.1 目标

| # | 目标 | 可测判据 |
|---|---|---|
| G1 | 核心能力留在 Go，不因策略迭代而重编译 | 改 Python 策略代码后 `go build` 产物字节不变 |
| G2 | **自定义 Agent 生成**与自进化在 Python 侧实现 | 生成/变异/出题/评审/选择五个动作的代码 100% 在 `plasma/`；Go 侧无生成器 |
| G3 | 在线生成保留流式与 TUI 交互 | `delegate_meta` 期间 TUI 看到 `ai_chunk`（Go 侧 LLM 调用投递） |
| G4 | Agent 生成经过类编译器的静态检测 | 每个诊断码有一条能触发它的负样本夹具；诊断文本进 golden |
| G5 | 生成质量可保证 | `verified` 必须通过行为夹具（5b，真 LLM）；只有 verified 以上可自动委派；在线产出止于 `provisional` |
| G6 | 动态层不可能绕过内核权限 | 越权 bundle 编译失败并点名；写/shell 升级必须走用户授权 |
| G7 | 两层交互高效简约 | 无自定义协议、无常驻进程；在线 = 文件 + exit code 的 trampoline |

### 1.2 非目标

- 不实现第二个 Agent 执行循环（复用 `RunAgentLoop`，`executor.go:184`）。
- **不保留第二个生成器**（Go Meta-Agent 整体移除，不做 fallback）。
- 不让 Python 直连 provider、持密钥、碰工作区、起 shell。
- 不引入第三方 Python 依赖。
- 不做 bundle 热重载。
- 不自动改写内建 Agent（只出提案）。
- 不追求"用静态分析预测 LLM 行为"。
- 不用常驻连接做在线生成（trampoline 已够，见 §3.5）。

### 1.3 成功判据（验收时会真的跑）

1. 含幻觉工具名的 bundle → 编译失败，诊断反馈给 Python `--resume --fix` 重生成。
2. 请求 `run_bash` 的 bundle → grade 钉 `provisional`，除非升级且用户批准。
3. 编译通过但行为夹具红 → grade 停 `provisional`，不可自动委派。
4. 篡改 store 一字节 → 加载期 hash 校验失败并拒载。
5. **删掉 Python 解释器 → `delegate_meta` 从工具面消失，`Skipped()` 里点名原因**
   （照 `browserAvailable` 的形状，不是静默 Debug）。
6. 进化器改已 verified 版本的 fixtures → store 拒绝，新 hash grade 不继承。
7. `delegate_meta` 期间 TUI 看到 `ai_chunk`，且**流式来自 Go 侧 LLM 调用**
   （Python 全程不持有 provider 密钥）。
8. `yagent plasma test --fixture s1` 在**生产二进制**里能跑结构夹具。
9. 一次委派结束后 `telemetry.jsonl` 多一行，`steps`/`tokens_in`/`stop_reason` **非零值**。
10. trampoline 中途 kill Python 进程 → Go 侧超时生效、workdir 被清理、委派以错误收尾
    （不留孤儿进程与孤儿目录）。

---

## 2. 代码现状分析

### 2.1 在线生成路径（今天的样子，将被替换）

```
delegate_meta → runMetaDesign (director_surface.go:182)
  → MetaAgent.Run (meta.go:44)                  ← Go, 单次 LLM 调用, 流式 ai_chunk
  → ParseMetaAgentOutput (meta_handler.go:107)  ← 只查 JSON 格式 + 两个必填字段
  → registerCustomAgent (director_surface.go:284)
  → runCustomAgent (:261) → executeCustomAgent (director.go:336) → RunAgentLoop
```

retry 循环（`director_surface.go:199-253`）只在 JSON 解析失败时重试，反馈是一句
"your output was NOT valid JSON"（`:207`），**没有工具名/权限/prompt 质量的任何校验**。

### 2.2 Meta-Agent 的完整足迹（移交 Python 时要动的地方）

实测 61 处非测试引用，分布在 10 个文件：

| 落点 | 现状 | 处置 |
|---|---|---|
| `internal/agents/meta.go`（180 行） | `MetaAgent` + 单次 LLM 调用 + 流式 | **删除**（生成职责移 Python；流式由 trampoline 的 Go 侧承接） |
| `internal/agents/meta.prompt.md`（108 行） | 提示工程原则 + 5 字段输出契约 + `{{TOOL_POOL_TABLE}}` | **迁入 `plasma/`** 当生成策略种子（内容有价值，不浪费） |
| `app.go:389 / :470` | `NewMetaAgent` 装配 + `MetaAgent` 字段 | 删除，换成 plasma trampoline 装配 |
| `director.go` | `MetaAgent` 字段、`metaRetryCount` | 字段删除；**`metaRetryCount` 保留**（改为驱动 trampoline 的重生成上限） |
| `director/meta_handler.go:107` | `ParseMetaAgentOutput` / `ExtractJSONObject` | **删除**（Python 直接产 bundle，不再需要解析 LLM 裸输出）；`MetaAgentHandler` 注册表**保留** |
| `director/types.go` | `MetaAgentResult`（5 字段 JSON 形状） | 删除或降级为 `Elaborate` 的最小 spec 形状 |
| `deps.go:97` `renderCustomAgentToolPool` | 从 `ProfileDirector.Palette` 派生工具池表格喂 Meta prompt | **改用途**：喂能力清单（manifest），并用 `PlasmaPalette` |
| `specs.go:74` `delegate_meta` Spec | `{Mutates:false, Serial:true, Audience:["director"]}` | **保留不动**；`delegateAvailable("meta")` 改判 plasma 可用性 |
| `testdata/tool_surfaces.golden:12` | `delegate_meta  tier=hot  serial` | **不变**（工具还在，只换实现） |
| `config/defaults.go:24` / `config.go:91,649` | `MetaRetry: 5` / `meta_retry_count` | 保留，语义改为"生成重试上限" |
| `toolbatch/plan.go:20` | 仅注释提到 `delegate_meta` | 无需改动 |

**内建 Agent 从 7 个变 6 个**（Meta 不再是 Go Agent）。根 `AGENTS.md` §4 的表要同步；
`delegate_meta` 这个**工具名保留**，所以 50 条基线与 golden 都不动。

### 2.3 可复用的既有接缝

| 接缝 | 位置 | Plasma 怎么用 |
|---|---|---|
| 纯数据 Agent 描述 | `director/types.go:22` | 编译产物的目标形状 |
| 按名挑选 + Palette 校验 | `toolspec/profile.go:174` | Resolve 直接调 `MatchNames`（`:196` 自动追加 `agent_exit`） |
| 三级校验器 | `toolspec/spec.go:108 / :168 / :225` | 能力不变量的唯一来源 |
| 运行期工具注册 | `tools/surface.go:413` | 加载期 `Surface.Add` |
| 共享执行循环 | `agents/executor.go:184` | 夹具执行与生产执行同一条 |
| 结果定型 | `agents/result.go:58 / :197` | 行为夹具判据取交付全文 |
| **委派统一汇聚点** | `director.go:698`（`directorToolRunner.Call`） | 遥测账本写入点（§2.4） |
| 原子写唯一内核 | `util/atomic.go:41` | store 与 workdir 落盘 |
| 单一 token 计数器 | `tokenutil/tokenutil.go:30` | 预算判据，与压缩同源 |
| 路由基线 | `llm/router.go:52 / :139 / :176` | `runtime.tier` 的落点 |
| 不装配 Agent 的子命令 | `main.go:79` | `yagent plasma` 的取向 |
| **流式投递** | `llm/engine.go:219 / :226` + `executor.go:266` | trampoline 的 Go 侧 LLM 调用照此发 `ai_chunk` |
| **可恢复状态的序列化先例** | `memory/rollout_types.go:322-336`（`AgentCheckpoint`）、`agents/resume_state.go:25-43`（`ResumeState`） | trampoline 的 `state.json` 是同一模式的跨语言版（ADR-14） |
| **缺依赖即降级的先例** | `tools/specs_browser.go:54-59`（`browserAvailable` 返回 `(false, 原因)`） | 无 Go 兜底的降级形状（ADR-15） |

### 2.4 两处"看似能复用、实际不能"的地方

**(a) 脚本引擎只存在于测试文件。** `agentScriptedEngine` / `scriptStep` 只出现在 9 个 `_test.go`
（e2e_engine / e2e_lifecycle / e2e_blueprint / e2e_pause / e2e_replay / e2e_handoff /
e2e_resume / e2e_mcp / resume_degrade）。生产代码里 `llm.Engine`（`llm/engine.go:229`）
**没有任何可编程实现**。5a 结构夹具必须在生产路径上跑 ⇒ 必须提升为生产包（ADR-12）。

提升时还要修一处脆弱性：`agentIdentity`（`e2e_engine_test.go:41-58`）靠**各家 System Prompt 的
首行措辞**认人，只硬编码四个内建（director/repo/coding/chat），default 返回 `"unknown"`。
plasma Agent 的 prompt 是生成出来的，首行不可预测 ⇒ 嗅探必然落 `"unknown"`。
5a 是**单 Agent** 夹具，不需要身份分派 ⇒ 提升后的引擎支持显式身份注入（ADR-12）。

**(b) 执行指标不在结果结构体里。** `ExecutorResult` 只有 `Text` + `History`（`executor.go:66-69`）；
`AgentResult`（`types.go:14`）里 `Steps|Tokens|Usage|StopReason|StopKind` **grep 零命中**。
usage 只进事件总线 metadata（`executor.go:570-577`）与 rollout ⇒ 遥测账本要记这些就必须扩展
两个结构体（ADR-13）。

**遥测写入点**：`applyEnhancedCommander`（`director.go:462`）/ `recordDelegationOutcome`（`:454`）
**不是三条分支的共同汇聚点**。实测 `delegateTo`（`director_surface.go:137-176`）：
`meta` 在 `:144` 提前 return、custom agent 在 `:151` 提前 return，**只有 builtin 分支**才走到
`:170-175` 的收尾三件套。plasma Agent 走的正是 custom 分支。真正的汇聚点是
`directorToolRunner.Call`（`director.go:698`）：`:727` 建 outcome 载体、`:744` 调工具、
`:748-755` 已有 `outcome.has` 判断与 `RecordDelegation`——账本写在这里。

### 2.5 权限现状（安全设计的出发点）

`ProfileDirector.Palette`（`profiles.go:33-34`）**含 `EffectFSWrite | EffectShell`**，
Director 自己 Deny 这两项（`:32`）却把它们放进给子 Agent 的 Palette。且**不含 `EffectToolLoad`**
⇒ 今天 Meta 设计的 Custom Agent 拿不到 `load_tool`，被降到 Cold 的工具永远取不回。

---

## 3. 架构总览

### 3.1 在线路径：trampoline（生成策略在 Python，LLM 与流式在 Go）

```
用户 → Director → delegate_meta → runMetaDesign（trampoline 驱动器）
  │
  ├─ Go 建 workdir W，写 req.json + manifest.json（能力清单，用 PlasmaPalette）
  │
  ├─ spawn python3 plasma.pyz generate --workdir W
  │    Python 跑生成策略：多轮 draft→critique→refine / few-shot 检索 / 工具预选
  │    （策略种子来自迁入的 meta.prompt.md）
  │
  ├─ Python 需要 LLM → 写 W/llm_req_NNN.json + W/state.json，exit 4
  │    │
  │    └─ Go 读请求 → LLM.GenerateContent(ctx, …, opts.StreamHandler)
  │         → Publish("ai_stream_start" / "ai_chunk" / "ai_stream_end")  ← TUI 流式
  │         → 写 W/llm_resp_NNN.json
  │         → respawn python3 plasma.pyz generate --resume --workdir W
  │              Python 从 state.json 恢复，继续策略
  │    （循环，直到 exit 0）
  │
  ├─ Python exit 0 + W/bundle.json
  │
  ├─ plasma.Elaborate + Compile（Go 进程内，六阶段，§6.1）
  │    ├─ 失败 → 写 W/diagnostics.json → respawn generate --resume --fix
  │    │         Python 读诊断重生成（回到上面的 LLM 循环），上限 meta_retry_count
  │    └─ 通过 → registerPlasmaAgent（Surface.Add，grade=provisional）+ store 落盘
  │
  └─ executeCustomAgent（Go, RunAgentLoop, 流式 → TUI）→ 遥测一行 → 清理 W
```

**为什么这样两个要求能同时成立**：生成**策略**在 Python（可快速迭代、不重编译 Go），
LLM **调用**在 Go（引擎栈只有一份、密钥不出边界、`StreamHandler` 能投递到父进程的 dispatcher）。
`exit 4` 只是"我需要一次采样"的信号，不是协议。

**在线止于 `provisional`**：行为验证（5b）需真 LLM（10–60s/夹具）且出题在离线，
在线无题可跑；5b 的耗时也会撞 `delegate_idle_timeout`（默认 60s）。

### 3.2 离线路径：一次性子进程（批处理）

```
定时/手动触发
  ├─ [Py] evolve：读 telemetry.jsonl → 评分 → 变异 → 候选 bundle
  │    （需要 LLM 时调 yagent plasma sample，完整 JSON 即可，无人看 TUI）
  ├─ [Go] Compile 候选（阶段 1-4 + 5a）；失败 → 诊断进 lineage.rejected[]
  ├─ [Py] author-fixtures：给候选出行为夹具（独立谱系）
  ├─ [Go] 5b 行为夹具（真 LLM）+ [Py] judge 评审
  ├─ [Go] emit → grade=verified → index 指向新 hash
  └─ [Py] propose：内建 Agent 的 diff 提案，永不自动 apply
```

离线路径**不需要 trampoline**：它是批处理，`plasma sample` 作为孙子进程返回完整 JSON 就够了
（没有 TUI 在等流式，所以"子进程拿不到父 dispatcher"这件事在这里不构成问题）。

### 3.3 职责矩阵

| 关注点 | Go | Python |
|---|:--:|:--:|
| 工具注册表 / Effect 位数学 | ● | — |
| 编译六阶段 | ● | — |
| token 计数 | ● | — |
| **LLM 调用本身**（在线与离线） | ● | 借 |
| **流式投递到 TUI** | ● | — |
| **Agent 生成策略**（多轮/few-shot/工具预选） | — | ● |
| 夹具**执行**（5a/5b） | ● | — |
| 夹具**出题** | — | ● |
| 行为**评审**（rubric/judge） | — | ● |
| **进化**（变异/评分/选择） | — | ● |
| 存储 / 版本 / hash | ● | — |
| 遥测**写入** | ● | — |
| 遥测**读取与评分** | — | ● |
| 权限裁决 / 授权回路 | ● | — |
| 内建 Agent 提案 | — | ● |
| trampoline 驱动器（spawn/exit 分发/超时/清理） | ● | — |

### 3.4 通道

| 通道 | 方向 | 载体 |
|---|---|---|
| 能力清单 | Go→Python | `W/manifest.json`（在线）/ spawn 前写盘（离线） |
| LLM 采样 | Python→Go→Python | `W/llm_req_NNN.json` + `exit 4` + `W/llm_resp_NNN.json`（在线）；`plasma sample` CLI（离线） |
| 候选与产物 | 双向 | 文件（bundle / 诊断 / 评分 / 提案） |
| 生成状态 | Python↔Python | `W/state.json`（跨 respawn 保留，ADR-14） |
| 遥测 | Go→Python | `~/.yagent/agents/telemetry.jsonl` |

`capability_lock` 是**溯源元数据**（记录 bundle 对着哪版清单生成，便于解释诊断），
不是安全门——裁决在 Go 进程内对着活注册表做。

### 3.5 为什么在线用 trampoline 而不是常驻连接

| 维度 | 常驻连接（MCP/NDJSON/gRPC） | trampoline |
|---|---|---|
| 流式到 TUI | 可以（但要实现 server→client 请求分发） | **可以**：LLM 调用本来就在 Go 侧 |
| 进程生命周期 | Manager / 重连 / 状态五档 / 启动顺序 | 无：spawn → wait → 分发 → respawn |
| 失败类 | 挂住、断连、readLoop 死锁、僵尸 | exit code + 超时 |
| `internal/mcp` | 要改 readLoop（已有两条死锁纪律） | **完全不涉及** |
| 代价 | 协议面 + 长驻进程 | 每次 LLM 调用一次进程往返（~300ms） |

trampoline 的唯一代价是进程往返，而它落在**秒级的 LLM 调用**之间（§9.2），可忽略。
换来的好处是：`internal/mcp` 一行不改、无常驻进程、无重连逻辑、无协议版本协商。

**状态序列化不是新发明**：`AgentCheckpoint`（`rollout_types.go:322-336`）只存
Step/Digest/Model/Tier/Workspace 这些派生不出来的东西，`ResumeState`（`resume_state.go:25-43`）
存 Messages/StartStep/Materialized/Digest——本仓已经在做"序列化可恢复状态 → 从游标续跑"。
Python 的 `state.json` 是同一模式的跨语言版，而且生成循环的状态比 `ResumeState` 小得多。

---

## 4. 编译器即扩充器

### 4.1 源语言

Python 的 `generate` 可产出**最小 spec**（沿用今天 Meta 的 5 字段形状，迁移成本低）：

```json
{ "thinking": "…", "agent_name": "Security Auditor",
  "agent_design": "<完整 system prompt>",
  "tools_used": ["read_file","search_by_regex"],
  "task_for_agent": "…" }
```

也可产出**完整 IR**（进化路径通常如此）。编译器对两种输入一视同仁：
`Elaborate` 检测形态，最小 spec 走扩充，完整 IR 走归一化。

### 4.2 扩充规则

| 源字段 | IR 字段 | 扩充规则 |
|---|---|---|
| `agent_name` | `name` | `toSnakeCase`（既有函数，`director.go:302`） |
| `agent_name` | `display_name` | 原样 |
| `agent_design` | `prompt.system` | 原样 |
| — | `prompt.lang` | 从 `[agent] lang` 读 |
| `tools_used` | `tools` | 原样 + `agent_exit`（`MatchNames:196` 自动追加） |
| `tools` 的 Effect | `policy.effects_required` | 从注册表派生（`Spec.Effect.Names()`） |
| — | `policy.deny` | 默认 `["fs_write","shell"]` |
| — | `runtime.max_steps` | 默认 15（与今天 `executeCustomAgent` 一致） |
| — | `runtime.stop_on_finish` | 默认 true |
| — | `runtime.llm_timeout` | 从 `[llm] timeout` 读 |
| — | `runtime.tier` | 默认 `"balanced"` |
| — | `runtime.compression` | 从 `[enhanced_commander]` 读 |
| — | `contract.must_exit_with` | 默认 `["reason"]`——`agent_exit` schema 的 `required` 只有 `reason`（top-level props = `reason`/`handoff`/`blueprint`）。`handoff.conclusion` 是**可选但强烈推荐**，走 lint（PLS-301）而非 required |
| — | `contract.thought_format` | 默认 true |
| — | `fixtures.structural` | **自动生成**（§4.3） |
| — | `fixtures.behavioral` | 空（离线 Python 出题后填充；在线不跑 5b） |
| — | `quality.grade` | `"provisional"` |
| `thinking` / `task_for_agent` | — | 前者记日志，后者传给执行路径，都不进 IR |

### 4.3 自动生成的结构夹具（5a）

编译器从 IR 自动生成，不需要 Python、不需要真 LLM。**依赖 ADR-12 的生产级脚本引擎**。

| 夹具 | 脚本 | 断言 |
|---|---|---|
| **exit-contract** | ① 一条 assistant 文本消息 ② `{tool:"agent_exit", args:{reason:"done", handoff:{conclusion:"…",status:"ok"}}}` | 退出成功；`deliverableText` 返回的是**那条 assistant 文本**而非 `{"finished":true,…}` JSON；handoff 形状合规 |
| **step-budget** | 重复 `{tool:"thinking"}` `max_steps+1` 次 | 循环在 `max_steps` 停止；`stop_kind == "max_steps"` |
| **tool-containment** | 静态断言 | Surface 只含声明工具 + `agent_exit` |
| **palette-containment** | 静态断言 | 所有工具 `Effect ⊆ PlasmaPalette`（或有 `escalation_approved`） |

**exit-contract 为什么必须含 assistant 文本**：`deliverableText`（`result.go:58-70`）在
`isAgentExitResult` 为真时**倒序找最后一条非空 assistant 消息**，找不到就回落 `exec.Text`
（那段 `{"finished":true,…}` JSON）。纯脚本只发 `agent_exit` 时，"deliverableText 非空"
**恒真**（回落值也非空）——是个永远不会失败的测试。所以断言必须比对**内容**。

**身份分派**：5a 是单 Agent 夹具，用**显式固定身份**，不走 prompt 首行嗅探（§2.4a）。

### 4.4 完整 IR

```jsonc
{ "schema_version": 1,
  "name": "security_auditor", "display_name": "Security Auditor",
  "version": "0.1.0",
  "capability_lock": "sha256:…",
  "lineage": { "parent_hash": null, "mutation": "initial-generation",
               "rejected": [{ "mutation":"tools:+run_bash", "diag":"PLS-210" }] },
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

---

## 5. 关键设计决策（ADR）

### ADR-1：编译器归 Go，生成策略归 Python

- **问题**：编译六阶段中五阶段是对活注册表的确定性查询；而生成策略需要快速迭代。
- **决定**：编译器在 Go 进程内（`internal/plasma`）；生成策略在 Python（`plasma/generate`）。
- **理由**：① 编译器跨进程就要发布清单并处理漂移，搬进进程内这个失败类整体消失；
  ② token 计数必须用 `tokenutil`（与压缩同源，且有粘滞降级语义），第二份实现会漂；
  ③ 编译器在 Go 才能被 `check_all.sh`/golden/变异测试覆盖；
  ④ 生成策略是启发式的，改它不该触发 Go 重编译。
- **后果**：Go 侧 `MetaAgent` 整体移除（§2.2）；`capability_lock` 降为溯源元数据；
  **编译器成为动态层与静态层之间真正的信任边界**（它检查的是 Python 的产出，不是 Go 自己的）。

### ADR-2：离线 Python 是一次性子进程

- **决定**：argv + JSON + exit code，无常驻。批处理不需要长连接。
- **补偿控制**：`cmd.WaitDelay`（坑 46：`CombinedOutput` 的等待条件含管道 EOF，
  子孙进程会让 ctx 看门狗失效）；超时经 `supervise`。

### ADR-3：双层夹具

| 层 | 引擎 | 测什么 | 何时跑 | 门禁 |
|---|---|---|---|---|
| 5a | 脚本（`internal/llm/scripted`，ADR-12） | 接线：工具解析、exit 契约、步数预算、palette 包含 | 每次编译（在线 + 离线） | 阻断 provisional |
| 5b | 真 LLM | prompt 质量：真模型在沙箱里是否产出预期交付物 | **只在离线**升 verified 前 / 手动 CLI | 阻断 verified |

- **问题**：脚本引擎的 `Model()` 返回 `"scripted-mock"`（`e2e_engine_test.go:96`），
  生成的 prompt 根本没被执行——只用脚本测的是接线，不是质量。只用真 LLM 则贵且非确定。
- **在线不跑 5b**：在线 `fixtures.behavioral` 为空（无题可跑），且 10–60s 会撞
  `delegate_idle_timeout`（默认 60s）。在线止于 provisional。
- **补偿控制**：5b 有 token 与墙钟双预算；超预算 ⇒ 停 provisional 并点名，绝不静默通过。

### ADR-4：PlasmaPalette + 升级门

- **问题**：`ProfileDirector.Palette` 含 `EffectFSWrite | EffectShell`（`profiles.go:33`）。
  自动委派的生成式 Agent 继承它 = 权限升级漏洞。
- **决定**：独立窄 palette + 显式升级门。

```go
PlasmaPalette = EffectFSRead | EffectSearchRegex | EffectCodeSemantic | EffectCodeGraph |
                EffectContext | EffectFlow | EffectToolLoad |
                EffectCogReflect | EffectCogDeep
// 不含：FSWrite · Shell · Browser* · Checkpoint · Delegate
//       ExternalRead · ExternalWrite · Skill（见下条既有不变量）
```

- **第三方能力一律不给**：`internal/toolspec/AGENTS.md` 硬约束 6.1 明写
  "**`Palette` 一个位都不加**（Custom Agent 拿不到第三方能力）"。`PlasmaPalette` 遵守它——
  不含 `EffectExternalRead`/`EffectExternalWrite`/`EffectSkill`。理由与 T2 同源：
  第三方 MCP 输出是提示注入向量，而生成式 Agent 的 prompt 本身就是生成出来的，
  两者叠加等于把注入面交给进化器。要给必须走升级门（逐个 Agent、用户批准）。

- **升级路径**：请求窄 palette 之外的能力 → `PLS-210`，grade 上限 provisional →
  首次委派经 `UserConfirmManager` 明示批准 → 该 hash 记 `escalation_approved=true`。
- **批准按 hash 记**，落 `{name}/{hash}/meta.json`（**不进 bundle.json**，那会改 hash）。
  进化出新版本 ⇒ 新 hash ⇒ 批准自然失效。
- **`EffectToolLoad` 是有意的行为变更**：今天 Palette 不含它（`profiles.go:33-34` 实测）
  ⇒ Custom Agent 的 Cold 工具永远取不回。加上它是**修 bug**，P1 要点名并配夹具。
- **不进 `BuiltinProfiles()`**：走 `Profile{Names}` 按名挑选，golden 不动。
- **工具池广告**：`renderCustomAgentToolPool`（`deps.go:97`）改用 `PlasmaPalette`，
  产物从"喂 Meta prompt"改为"喂 manifest"。
- **迁移影响**：今天 Meta 设计的 Agent 能拿 `run_bash`；之后默认拿不到，需升级批准。有意的安全收窄。

### ADR-5：内容寻址存储 + 夹具不可变

- 存储键 = `sha256(归一化 bundle)`；回滚 = 指向旧 hash。
- `fixtures[]` 与 `contract` 一经落盘即不可变；改夹具 ⇒ 新 hash，grade 不继承。
- 出题谱系分离：同 lineage 的变异体不得由自己的出题动作验证（防 Goodhart）。

### ADR-6：遥测独立账本，写入点在 `directorToolRunner.Call`

- **决定**：写一行到 `~/.yagent/agents/telemetry.jsonl`，写入点 = `director.go:748` 附近
  （`ad.Call` 返回后、已有 `outcome.has` 与 `RecordDelegation` 旁边）。
- **为什么不是 `applyEnhancedCommander`**：那只在 builtin 分支跑，meta/custom 在
  `delegateTo:144/:151` 提前 return，而 plasma Agent 走 custom 分支（§2.4）。
- **字段来源**：`steps`/`tokens_in`/`tokens_out`/`stop_reason` 需要 ADR-13 扩展结果结构体。
- **补偿控制**：写入失败不阻断委派，但留可见账目（坑 43）；验收要求字段**非零值**（§1.3 第 9 条）。

### ADR-7：LLM 调用始终在 Go

- **在线**：trampoline 的 Go 侧承接（带 `StreamHandler` → 流式到 TUI）。
- **离线**：`yagent plasma sample` CLI（完整 JSON，无流式需求）。
- **理由**：引擎栈已有路由（`RoutingEngine` 漏一层则 auto-routing 静默失效）、回退、
  熔断（`Allow()` 查询即试探）、进程级在飞闸门、token 账、`llm-*` 日志。
  Python 自带客户端 = 这些全部第二份实现且密钥出边界。
- **补偿控制**：`sample_timeout` 兜上界；采样同样受在飞闸门约束。

### ADR-8：stdlib-only zipapp

- Python 纯标准库，单文件 `plasma.pyz`，`go:embed` 进 `dist/bin`，启动解到 `~/.yagent/bin`。
- **仍需 PATH 上有 python3（≥3.9）**：zipapp 消除依赖，不消除解释器。`[plasma] python` 可显式指定。
- 降级见 ADR-15。
- 代价：不能用 pydantic/numpy，IR 与统计手写。有意的取舍。

### ADR-9：三档 grade 棘轮

| 档 | 准入 | 能力 |
|---|---|---|
| `provisional` | 阶段 1–4 + 5a 通过 | 可人工点名委派；不可自动委派 |
| `verified` | 上述 + 5b 通过 + 无未批准升级 | 可自动委派 |
| `promoted` | verified + N 次生产成功（默认 5，`fixture_run=false` 才计数） | 列表优先呈现 |

掉档自动（生产失败累计到阈值）；回滚 = 载旧 hash。

### ADR-10：编译器即扩充器

- **问题**：LLM 擅长创意（prompt 设计、工具选择），不擅长填结构化配置。
- **决定**：接受最小 spec 或完整 IR；最小 spec 由编译器扩充默认值（§4.2）。
- **理由**：默认值从配置与注册表派生，单一来源；生成策略不必知道 IR 全貌；
  迁移时可沿用今天 Meta 的 5 字段形状，降低 Python 侧首版成本。

### ADR-11：编译诊断反馈重生成

- **问题**：今天 retry 只说"你的 JSON 无效"（`director_surface.go:207`）。
- **决定**：编译失败 → 诊断（PLS-xxx 码 + JSON pointer + note）写 `W/diagnostics.json` →
  respawn Python `generate --resume --fix`，Python 读诊断重生成。
- **上限**：`meta_retry_count`（默认 5，`config/defaults.go:24`），语义从"JSON 解析重试"
  改为"编译失败重生成"。

### ADR-12：脚本引擎提升为生产代码（`internal/llm/scripted`）

- **问题**：5a 必须在生产路径跑，但脚本引擎只在 9 个 `_test.go`（§2.4a）。
- **选项**：(a) 提升为生产包，测试与生产共用；(b) 5a 只做静态断言 + 真 LLM 冒烟；(c) plasma 里再写一个。
- **决定**：**(a)**。
- **理由**：(b) 让 5a 变成要花钱、不确定、进不了 CI 的东西，等于取消 5a；
  (c) 直接违反坑 33（测试与生产必须共用同一载体）。
- **落点选 `internal/llm/scripted`**：它要实现 `llm.Engine` 就必须 import `llm`，
  而 `internal/plasma` 是叶子包（不许 import llm）。放 llm 子包后由 `agents` 侧的
  `FixtureRunner` 实现 import 它，plasma 只通过窄接口间接使用。
- **提升时必须改的两处**：① 身份注入改为可注入解析器（5a 用固定身份，e2e 保留嗅探以兼容）；
  ② `llm.CallOptions`（`engine.go:216`，今天只有 MaxTokens/Temperature/StreamHandler/ReasoningEffort）
  加 `AgentName string`，由 executor 在 `:263` 构造 opts 时填 `cfg.AgentName`。
- **后果**：9 个 e2e 测试文件改用新包。**这是真实的改动量，不是"复用既有"**。
- **补偿控制**：钉住"按身份分派而非全局序号"；`GenerateContent` 先看 ctx
  （`e2e_engine_test.go:69-72` 的纪律）；步骤在锁外跑（`:96-98`，`sync.Mutex` 不可重入）。

### ADR-13：扩展 `ExecutorResult` / `AgentResult` 携带执行指标

- **问题**：`steps`/`tokens`/`stop_reason` 不在任何结果结构体（§2.4b）。
- **选项**：(a) 扩展结构体；(b) 账本只记可得字段；(c) 从 rollout 反查。
- **决定**：**(a)**。
- **理由**：(b) 让适应度退化成只看成功/失败，失去步数与 token 成本这两个最有信号量的维度；
  (c) 违反 ADR-6。且这些指标本来就该在结果里——今天任何程序化消费者都拿不到"这次委派花了多少步"。
- **改动面（实测）**：`ExecutorResult` 构造点 9 处，7 处是错误路径零值返回
  （`executor.go:279/379/400/622/632/839`、`meta.go:176`——后者随 Meta 删除消失），
  **只有 2 处带数据**（`executor.go:736` 纯文本收尾、`:787` agent_exit 收尾）。
  `AgentResult` 透传改 `FinalizeResultFull`（`result.go:197`）一处。
- **`stop_kind` 复用既有词表**：`memory/rollout_types.go:298-303`。注意坑 18：
  `agents.classifyStop` 与 `director.StopKindOf` 是两份同形实现，**先给聚合测试喂新输入**再让两边追。
- **补偿控制**：一条测试断言 `Steps > 0` **且** `Usage.TotalTokens > 0` **且**
  `StopKind == StopKindAgentExit`——三个都非零值，否则就是加了字段没人填。

### ADR-14：在线生成用 trampoline

- **问题**：生成策略要在 Python（G2），流式要到 TUI（G3），而子进程拿不到父进程的 dispatcher
  （`app.go:486` 每任务创建）。三者看似互斥。
- **选项**：(a) 生成留 Go（放弃 G2）；(b) 常驻连接 + server→client 请求分发；
  (c) trampoline：Python 写请求 + `exit 4`，Go 调 LLM（流式）+ 写响应 + respawn。
- **决定**：**(c)**。
- **理由**：(a) 直接违背"自定义 agent 放 Python"的原始要求，且让编译器变成"Go 检查 Go 自己的产出"，
  信任边界名存实亡；(b) 要动 `internal/mcp` 的 readLoop（已有两条死锁纪律），并引入长驻进程与重连；
  (c) 把 LLM 调用留在 Go（流式天然成立），Python 只保留策略，且**无协议、无常驻**。
- **状态序列化有先例**：`AgentCheckpoint`（`rollout_types.go:322-336`）与 `ResumeState`
  （`resume_state.go:25-43`）已经是"只存派生不出来的东西 → 从游标续跑"。
- **代价**：每次 LLM 调用一次进程往返（~300ms spawn + 状态反序列化）；
  Go 侧新增 trampoline 驱动器（约 200–300 行）。
- **补偿控制**：workdir 有 TTL 与收尾清理（防孤儿目录）；respawn 次数有上限
  （`max_llm_calls`，防 Python 逻辑死循环）；子进程 `WaitDelay` + `supervise` 超时；
  一条测试钉住"kill Python 后 Go 超时生效且 workdir 被清理"（§1.3 第 10 条）。

### ADR-15：不保留 Go 侧生成器兜底

- **问题**：Python 缺失时 `delegate_meta` 怎么办？
- **选项**：(a) 照 browser 先例，工具从工具面消失并说明原因；(b) 保留 Go MetaAgent 作 fallback。
- **决定**：**(a)**。
- **理由**：(b) 会留下**两个生成器**，正是坑 33 的形状（两份同形实现各自漂走，
  测试绿而生产走另一条）。而 (a) 有明确先例：`browserAvailable`（`specs_browser.go:54-59`）
  缺依赖就返回 `(false, "浏览器执行层未装配")`，工具直接不进工具面，**没有 Go 兜底**。
- **后果**：无 python3 ⇒ 自定义 Agent 生成不可用（相对今天是能力回退）。
  但 zipapp 已内嵌，缺的只是解释器，修复方式一句话。
- **可见性要求**：降级原因必须进 `Surface.Skipped()` 并在 `delegate_meta` 的描述里可见，
  **不许只 `slog.Debug`**（`app.go:167-168` 的 embedbin 坑不重犯）。
- **补偿控制**：一条测试断言"plasma 不可用 ⇒ `delegate_meta` 不在 `Defs()` 里，
  且 `Skipped()` 含可读原因"。

---

## 6. 详细设计

### 6.1 编译器六阶段

| # | 阶段 | 判据来源 | 挡住什么 | 码族 |
|---|---|---|---|---|
| 1 | **Parse** | IR schema + 归一化 | 结构错、必填缺失 | PLS-0xx |
| 2 | **Resolve** | 活注册表 `MatchNames` | 幻觉工具名（did-you-mean） | PLS-1xx |
| 3 | **Typecheck** | PlasmaPalette + `ValidateDynamic`/`ValidateProfile` 同源规则 + knob 范围 | 权限越界、能力不一致、无 agent_exit | PLS-2xx |
| 4 | **Consistency** | prompt↔schema 参数名对账 + `tokenutil` 预算 + `ThoughtPlan.Validate` | 参数名错配（坑 38）、dead selection、提示词吃压缩阈值、`<thought>` 静默丢失 | PLS-3xx |
| 5 | **Verify** | 5a 自动生成 + `internal/llm/scripted`；5b 真 LLM（**只离线**） | 接线错 / prompt 无效 | PLS-5xx |
| 6 | **Emit** | `util.AtomicWrite` + sha256 + grade + 夹具不可变 | 落盘失败、grade 越权继承 | PLS-6xx |

**加载期**（非编译阶段）：store → 重算 hash → 活注册表复核阶段 2–3 → `LinkPlan` → `Surface.Add`。
hash 不符或注册表漂移 ⇒ 拒载（PLS-7xx）。加载期**不跑阶段 4**（不含 token 计数）。

阶段 4 的 `<thought>` 规则对齐 `ThoughtPlan.Validate()`（`thinklink/thoughtplan.go:195`）：
定界符成对、每轮恰一块、`goal`/`reason` 非空、至多一条 `[>]`。**为什么必须编译期查**：
该块解析失败"不报错、只静默丢失"，Agent 看起来在跑而 thinklink 面板与终极压缩原料全空。

阶段 4 的 token 预算用 `tokenutil.EstimateTokens`（`tokenutil/tokenutil.go:30`）——与压缩阈值
同一个计数器。它的降级是一次性粘滞的（首次离线失败后全进程走 `len/4`，对中文约 4 倍低估），
所以预算要留余量，诊断里要说明当前是否处于降级估算。

### 6.2 trampoline 驱动器（`runMetaDesign` 的新实现）

```go
// director_surface.go — runMetaDesign 改为 trampoline 驱动器
func (a *DirectorAgent) runMetaDesign(ctx context.Context, task string) (interface{}, error) {
    W := newWorkdir()                    // 唯一目录；defer 清理（含异常路径）
    writeJSON(W/"req.json", task)
    writeJSON(W/"manifest.json", a.plasma.Manifest())   // 用 PlasmaPalette

    for attempt := 0; attempt < a.metaRetryCount; attempt++ {
        mode := []string{"generate"}                    // 首轮
        if attempt > 0 { mode = []string{"generate", "--resume", "--fix"} }

        for calls := 0; calls < maxLLMCalls; calls++ {
            code := a.plasma.Spawn(ctx, W, mode)        // supervise + WaitDelay + 超时
            switch code {
            case 4:                                     // 需要 LLM 采样
                req := readJSON(W / fmt.Sprintf("llm_req_%03d.json", calls))
                a.publishStreamStart("meta")            // ai_stream_start → TUI
                resp := a.LLM.GenerateContent(ctx, req.Messages, nil,
                           &llm.CallOptions{StreamHandler: a.chunkPublisher("meta")})
                a.publishStreamEnd("meta", resp.Usage)  // ai_stream_end → TUI
                writeJSON(W / fmt.Sprintf("llm_resp_%03d.json", calls), resp)
                mode = []string{"generate", "--resume"}
            case 0:
                goto compiled                           // bundle.json 已写
            default:
                return nil, plasmaExitError(code, W)
            }
        }
        return nil, fmt.Errorf("plasma: LLM 调用次数超上限")

    compiled:
        bundle, err := plasma.Elaborate(readFile(W/"bundle.json"))
        report := a.plasmaCompiler.Compile(ctx, bundle)  // 六阶段（在线不含 5b）
        if report.HasErrors() {
            writeJSON(W/"diagnostics.json", report)
            continue                                     // 下一轮 --fix
        }
        a.registerPlasmaAgent(bundle, report)            // Surface.Add + store, provisional
        return a.executeCustomAgent(ctx, bundle, task)   // RunAgentLoop, 流式 → TUI
    }
    return nil, fmt.Errorf("plasma: 编译在 %d 次重生成后仍失败", a.metaRetryCount)
}
```

**要点**：`StreamHandler` 在 **Go 侧**构造（`a.chunkPublisher`），所以 `ai_chunk` 进的是
**父进程的 dispatcher**，TUI 正常消费（`tui_update_tasks.go:406`）。Python 全程不接触 provider。

### 6.3 workdir 契约

```
W/                              # 每次 delegate_meta 一个唯一目录，收尾清理
  req.json          Go 写       用户需求 + 上下文（只读）
  manifest.json     Go 写       能力清单（PlasmaPalette 派生，只读）
  state.json        Python 写   生成状态，跨 respawn 保留（Python 私有）
  llm_req_NNN.json  Python 写   LLM 请求：messages / tier / opts
  llm_resp_NNN.json Go 写       LLM 完整响应（含 usage）
  diagnostics.json  Go 写       编译诊断（--fix 时 Python 读）
  bundle.json       Python 写   最终产物（最小 spec 或完整 IR）
```

**exit code**：`0` 完成 · `1` 业务失败 · `2` 用法错 · `3` 超时被杀 · **`4` 需要 LLM 采样**。
`NNN` 从 000 递增，Go 与 Python 各自按序号对齐（不靠时间戳、不靠扫描目录顺序）。

**清理**：`defer` 覆盖正常与异常路径；另有一条启动期 GC 清理超过 `workdir_ttl`（默认 1h）
的孤儿目录（进程被 kill -9 时 defer 不跑）。

### 6.4 诊断格式

```
bundle.tools[3]: error[PLS-101]: unknown tool "read_filez"
                 note: did you mean "read_file"?
bundle.policy.effects_required: error[PLS-210]: "shell" is outside PlasmaPalette
                 note: grade capped at provisional; user approval required
bundle.prompt.system: warning[PLS-302]: 提示把参数 "target_file" 与工具 "search_replace_in_file" 写在同一行，但该工具的 schema 里没有它
                 note: "target_file" 是 create_file/read_file 的参数；调用会静默丢掉这个键
```

码表单一来源在 Go（`internal/plasma/diag.go`），经 manifest 发布给 Python 只读 ⇒ 不可能两侧漂移。
warning 进 `quality.lint[]`，作为进化适应度的一项。
码族：`0xx` parse · `1xx` resolve · `2xx` typecheck/权限 · `3xx` consistency/lint ·
`5xx` verify · `6xx` emit/store · `7xx` load。

### 6.5 存储与加载

```
~/.yagent/agents/
  {name}/{hash}/bundle.json      # 归一化 IR（不可变；主键 = hash）
  {name}/{hash}/report.json      # 诊断 + grade（可变）
  {name}/{hash}/fixtures/        # 沙箱工作区快照（不可变）
  {name}/{hash}/meta.json        # 可变状态：semver / lineage / fixture_provenance /
                                 #   escalation_approved / promoted 计数
  index.json                     # name → 当前 hash（唯一可变指针，原子写）
  telemetry.jsonl                # ADR-6 账本（追加）
  proposals/                     # 内建 Agent 提案（等人批）
```

**不可变 vs 可变的分界**：`bundle.json` 与 `fixtures/` 一经落盘即不可变（改一字节 ⇒ hash 变 ⇒ 新目录）。
`report.json` 与 `meta.json` 是可变状态（grade 升降、批准、计数）。把可变状态放进 `bundle.json`
会改 hash，所以必须分文件。

- **加载时机**：Director 构造期（`metaHandler` 今天在 `director.go:172` 建为空）。
- **字典序**加载与呈现：`customAgentPromptSection()` 拼在 blueprint/skills/mcp **之前**
  （`director.go:816-821`），顺序抖动会 invalidate 后面全部前缀缓存。
  `metaHandler.List()` 今天按插入序（`meta_handler.go:71`），跨进程不确定 ⇒ 必须改字典序。
- **预算**：`max_loaded_agents=12` + `catalog_line_chars=120`，超预算按 grade→score→name 截断
  + 一行可见省略账。
- **运行中不热替换**：一次委派保持加载时的 hash；版本变更在下一轮 Surface 重建生效。
- **GC**：`store_ttl=720h` 清理未被 index 指向且超 TTL 的 hash 目录（`artifact` 无 GC 的坑不重犯）。

### 6.6 权限模型

```
PlasmaPalette（只读族） ⊂ ProfileDirector.Palette（含 FSWrite|Shell）

请求超出 → PLS-210, grade 上限 provisional
         → 首次委派经 UserConfirmManager 批准
         → {name}/{hash}/meta.json 记 escalation_approved=true
```

写类工具 `NeedsAuth` 逐次授权照旧（由 `authEffects` 派生）；升级门管"能不能拿到这类工具"，
不是"这次调用要不要问"。两层不互相替代。YOLO（`-y`）照旧绕过，账本记 `escalated=true`。

### 6.7 遥测与评分

写入点 `directorToolRunner.Call`（`director.go:748` 附近，§2.4）。
`agent`/`bundle_hash`/`grade`/`delegation_id`/`note_ref`/`err` 当场可得；
`steps`/`tokens_in`/`tokens_out`/`stop_reason` 经 ADR-13 从 `outcome.result` 取；
`fixture_run` 由调用方标记（5a/5b 置 true）。

```jsonc
{ "ts":"…", "agent":"…", "bundle_hash":"…", "grade":"…",
  "stop_reason":"agent_exit", "steps":7, "tokens_in":18400, "tokens_out":2100,
  "degraded":false, "escalated":false, "fixture_run":false }
```

评分（Python）：成功率 + 1/步数 + 1/token + 批准率 - 降级率 - lint 警告。
只统计 `fixture_run=false`；样本 < `promote_min_runs`（默认 5）不升 promoted。

### 6.8 进化闭环

```
telemetry → [Py] score → [Py] mutate → 候选
  → [Go] compile 1-4 + 5a（失败 → lineage.rejected[]）
  → [Py] author-fixtures（独立谱系）
  → [Go] 5b + [Py] judge
  → [Go] emit → verified → 生产 → promoted / 掉档
```

Goodhart 三防线：夹具不可变 + 出题谱系分离 + 适应度含生产项。
进化默认关闭（`evolve_enable=false`）——它是唯一会在后台改变系统行为的开关。

### 6.9 内建 Agent 提案

`plasma.pyz propose` 产出 diff 到 `proposals/`，**永不自动 apply**：内建 Agent 的 prompt 与
Go 代码是"改行为要同时改两处"的耦合（根 `AGENTS.md` §4）。TUI `/proposals` 审批。

---

## 7. 接口契约

### 7.1 Go API（`internal/plasma`，叶子包）

```go
// 允许 import：stdlib + toolspec + util + projectid + tokenutil
// 禁止：agents（循环）、llm、mcp、messaging、config、tools

func Elaborate(raw []byte) (*Bundle, error)                // 最小 spec 或完整 IR
func (c *Compiler) Compile(ctx, *Bundle) (*Report, error)   // 六阶段
func (c *Compiler) Manifest() *Manifest                     // PlasmaPalette 派生

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

// trampoline 驱动器所需的子进程能力：由 agents/app 侧实现（那边才 import os/exec），
// plasma 只声明接口 ⇒ 叶子包约束不被打破
type Spawner interface {
    Spawn(ctx context.Context, workdir string, args []string) (exitCode int, err error)
}
```

**分工边界**：plasma 不 import `agents`/`director`/`tools`/`llm`，所以它只产出 `toolspec.Profile`；
真正的 `tools.NewSurface`（既有封装 `surfaceFor`，`deps.go:50`）与 LLM 调用由 `agents` 侧负责。
即：**plasma 决定"要哪些工具、编译是否通过"，agents 决定"怎么装配、怎么调 LLM"**。

### 7.2 CLI

| 命令 | 语义 | 装配 |
|---|---|---|
| `yagent plasma compile --bundle <path> [--behavioral]` | 六阶段 | 阶段5 需要时 |
| `yagent plasma sample --messages <path> --tier <t>` | 借引擎（**离线专用**） | 引擎 |
| `yagent plasma test --bundle <path> --fixture <id>` | 单跑夹具 | 是 |
| `yagent plasma list / promote / manifest` | store 管理 | 否 |

### 7.3 Python 命令与进程契约

| 命令 | 路径 | 契约 |
|---|---|---|
| `generate [--resume] [--fix] --workdir W` | **在线** | trampoline：exit 4 请求采样，exit 0 产出 bundle（§6.3） |
| `evolve` / `author-fixtures` / `judge` / `explain` / `propose` | 离线 | 一次性：`--in` / `--out`，stdout 仅一份 JSON，日志走 stderr |

离线 exit code：`0` 成功 · `1` 业务失败 · `2` 用法错 · `3` 超时。
在线另有 `4`（需要 LLM 采样）。**全部无状态跨调用**（在线状态在 `W/state.json`）。

### 7.4 配置

```toml
[plasma]
enable = true
python = ""                        # 空 = PATH python3；显式路径 = 指定解释器
store = "~/.yagent/agents"
max_loaded_agents = 12             # <=0 = 默认
catalog_line_chars = 120           # 显式 0 = 不裁剪
max_llm_calls = 12                 # trampoline 单次生成的采样上限（防 Python 死循环）
workdir_ttl = "1h"                 # 孤儿 workdir 的启动期 GC
compile_timeout = "60s"
structural_fixture_timeout = "60s"
behavioral_fixture_timeout = "10m"
behavioral_token_budget = 60000
sample_timeout = "10m"
auto_delegate_min_grade = "verified"
promote_min_runs = 5
store_ttl = "720h"                 # "0" = 不清理
evolve_enable = false
evolve_interval = "30m"
proposal_dir = "~/.yagent/agents/proposals"

[agent]
meta_retry_count = 5               # 既有键，语义改为"编译失败重生成上限"（ADR-11）
```

急停 `YAGENT_PLASMA=0`，判据写在 `Normalize*` **内部**。
**两套取值约定别混**：`max_loaded_agents`/`max_llm_calls` 用 `<=0 = 默认`；
`catalog_line_chars`/`store_ttl`/`workdir_ttl` 的 **0 本身有语义**，靠 TOML 键存在性
（`md.IsDefined`）区分（坑 13/26）。每个键都要有能因改值而失败的测试（坑 25/35）。

---

## 8. 安全与信任模型

### 8.1 威胁与补偿控制

| 威胁 | 控制 | 钉住它的测试 |
|---|---|---|
| T1 生成器产出越权 bundle | 阶段 2–3 + PlasmaPalette | 每个 PLS-1xx/2xx 一条负样本 |
| T2 提示注入 → 写/shell | 默认无写无 shell + `NeedsAuth` 逐次授权 + 阶段 4 模式检查（纵深防御） | "run_bash 未批准 ⇒ provisional"；变异：加回 FSWrite 必须红 |
| T3 Goodhart | 夹具不可变 + 出题谱系分离 + 生产项 | "改 fixtures ⇒ grade 不继承"；"同谱系出题 ⇒ 拒载" |
| T4 store 磁盘篡改 | 加载期重算 sha256 | "改一字节 ⇒ PLS-7xx" |
| T5 Python 被攻陷 | 无工作区权限、无密钥、无网络（LLM 经 Go）；最坏产出坏 bundle 被编译拒 | 子进程契约：stdout 非 JSON / exit 3 ⇒ 降级不崩 |
| T6 前缀缓存 / token 膨胀 | 字典序 + 预算 + 省略账 | 宽度/条数矩阵 |
| T7 升级批准被变异继承 | 批准按 hash 记 | "新 hash ⇒ escalation_approved 重置" |
| **T8 workdir 被跨任务窥探/篡改** | 每次委派唯一目录、0700、收尾清理 + 启动 GC | "并发两次 delegate_meta 的 W 不同且互不可读" |
| **T9 Python 死循环刷 LLM 调用** | `max_llm_calls` 上限 + `meta_retry_count` 上限 | "脚本无限 exit 4 ⇒ 到上限报错而非无限采样" |

### 8.2 明确不防的

- 用户自己写坏 bundle 并手动 `plasma promote`（显式人类操作，与 `-y` 同性质）。
- 真 LLM 在 5b 里被沙箱内容注入（沙箱是夹具作者提供的可信输入；但 `fixture_run=true` 不污染生产统计）。
- 语义级提示注入检测（模式匹配只是纵深防御，主防线是**能力面够窄**）。

---

## 9. 性能与成本

### 9.1 实测基线

`tokenutil.EstimateTokens` 本机实测（cl100k_base，2026-11-09）：

| 输入 | tokens | 耗时 |
|---|---|---|
| 首次调用（含编码器加载） | 501 | **51.4ms** |
| 8000 token 英文 prompt | 8001 | **5.0ms** |
| 17000 token 中文 | 17000 | 4.9ms |
| 20000 token 英文 | 20001 | 14.1ms |

阶段 4 的单次 token 计数在典型 prompt 长度下**就是 5ms 量级**，不是可忽略的常数。

### 9.2 延迟预算

| 动作 | 预算 | 依据 |
|---|---|---|
| Elaborate | < 1ms | 纯字段映射 |
| 阶段 1–3 | < 2ms | JSON 解析 + 注册表 map 查询 |
| 阶段 4 | **5–15ms** | 一次 token 计数（§9.1）+ 正则对账 |
| 阶段 5a | < 50ms | ≤8 步脚本循环，无 LLM；**待实测**（依赖 ADR-12） |
| 阶段 6 | < 5ms | 一次原子写 |
| **trampoline 每次往返** | **~300ms** | Python spawn + `state.json` 反序列化 |
| 在线一次生成（3 次采样） | **~0.9s 额外** + LLM 本身秒级 | 3 × 300ms；相对 LLM 延迟可忽略 |
| 5b（离线） | 10–60s / 夹具 | 真 LLM，受双预算约束 |
| 启动加载 N 个 | N × <2ms，N ≤ 12 ⇒ <25ms | **只复核阶段 2–3，不含 token 计数** |

在线生成的额外延迟 = trampoline 往返（采样次数 × ~300ms）+ 一次编译（约 10–25ms，
首次 +50ms 编码器加载）。**不是零**，但相对 LLM 调用可忽略。
**启动加载不得阻塞 TUI 首帧**：若实测超预算，改为首轮惰性加载（保持字典序稳定）。

### 9.3 token 成本与缓存

- 每个加载的 plasma Agent ≈ 清单行 80–150 token + `delegate_<name>` 声明 100–200 token。
  12 个 ⇒ **约 2–4k token 每轮固定前缀**，与 blackboard 的 `index_max_notes × index_line_chars`
  同类，必须同样有预算与省略账。
- manifest 约 15KB，**只写文件不进 LLM 上下文**。
- bundle hash 未变 ⇒ 跳过阶段 1–4 复用 `report.json`；5b 结果按 hash 缓存（不重复烧 token）。

---

## 10. 可观测性与降级

| 情况 | 行为 | 可见性 |
|---|---|---|
| **Python 缺失 / zipapp 提取失败** | **`delegate_meta` 不进工具面**（ADR-15） | `Surface.Skipped()` 含可读原因；**不许只 `slog.Debug`** |
| Python exit 1/2/3 | 报错 + 诊断；不阻塞 Director（走委派 idle/total 超时） | 工具输出 + `plasma-*` 日志 |
| Python 被 kill / 挂住 | `supervise` 超时 + `WaitDelay` 生效；workdir 清理 | §1.3 第 10 条 |
| 采样次数超上限 | 报错"LLM 调用次数超上限"（T9） | 工具输出 |
| 编译失败 | 结构化诊断 → `W/diagnostics.json` → `--fix` 重生成 | `report.json` + TUI `plasma explain` |
| 5b 超预算 | 停 provisional，点名"未验证：预算耗尽" | 绝不静默通过 |
| hash 不符 / 注册表漂移 | 拒载 + PLS-7xx 点名哪个工具改名/删除 | 启动账目一行 |
| 遥测写入失败 | 不阻断委派 + 可见账目 | 坑 43 |
| 加载超预算 | 截断 + 省略账 | 照 `module_docs.go` 取向 |
| 孤儿 workdir | 启动期 GC（`workdir_ttl`） | kill -9 时 defer 不跑，靠 GC 兜 |

新增 `plasma-` 日志类别（照既有 `app-`/`llm-`/`tool-`/`delegate-`）。
TUI `/plasma` 面板（list / explain / promote / proposals）。
Python 的生成进度可选经 stderr NDJSON 由 Go 转发到既有 `sub_agent_activity` 事件族
（根 `AGENTS.md` §6.6 已登记该类型）——**LLM 输出的流式已由 Go 侧 `StreamHandler` 保证**，
这条只是补充"策略层在做什么"的可见性。

---

## 11. 测试策略

- **闸门**：`check_plasma_leaf.sh`（stdlib + toolspec + util + projectid + tokenutil），
  纳入 `check_all.sh`，守卫 11→12。`internal/llm/scripted` 另需"只准 import llm + stdlib"约束。
- **每个诊断码一条负样本**；`diagnostics.golden` 钉住文本（改码表必须显式 `-update`）。
- **变异测试**：去掉不变量确认测试变红（加回 `EffectFSWrite`、把批准改成按 name 记、
  去掉 `max_llm_calls` 上限）。变异用 `/tmp` 最小副本，绝不 `git checkout` 还原。
- **ADR-13 指标断言**：`Steps > 0` **且** `Usage.TotalTokens > 0` **且**
  `StopKind == StopKindAgentExit`，三个都非零值。`stop_kind` 新输入**先喂聚合测试**
  再让 `classifyStop`/`StopKindOf` 追（坑 18）。
- **ADR-12 脚本引擎断言**：按身份分派而非全局序号；先看 ctx；步骤锁外跑。9 个 e2e 改用新包后全绿。
- **ADR-14 trampoline 断言**：① exit 4 → Go 采样 → respawn 的往返次数与序号对齐；
  ② kill Python 后超时生效且 workdir 清理；③ 无限 exit 4 到 `max_llm_calls` 报错；
  ④ **流式事件来自 Go 侧**（断言 `ai_chunk` 的 Publisher 是父进程 dispatcher）。
- **ADR-15 降级断言**：plasma 不可用 ⇒ `delegate_meta` 不在 `Defs()`，`Skipped()` 含可读原因。
- **5a 夹具卫生**：exit-contract 断言 `deliverableText` 的**内容**而非非空（§4.3）。
- **E2E**（`e2e_plasma_test.go`）：真实 dispatcher/guard/store/rollout，只 mock 模型。
  钉四条链路：① 生成（trampoline）→编译→`--fix` 重生成→注册→执行→遥测；
  ② 越权→升级→批准→verified；③ 进化→编译失败→`rejected[]`；④ 篡改 store→拒载。
- **在线流式**：断言 `delegate_meta` 期间有 `ai_chunk`。
- **性能回归**：一条基准钉住"一次编译 < 30ms（8k token prompt、编码器已热）"。
- 计时走 seam；扫目录用例拥有自己的目录（唯一 store/workdir 或 `t.Setenv("HOME", …)`，坑 23）；
  Python 测试用 stdlib `unittest`，不进 `check_all.sh`，但 Go 侧要有"zipapp 可被解释器加载"的冒烟测试。
- `testdata/` 落地后跑 `gofmt -l`（坑 41：Go 工具链不看 testdata）。

---

## 12. 落地顺序

全程硬闸门：每个 P 完成后 `check_all.sh` 绿 + golden / 50 条基线不变。
**含三项前置重构**（ADR-12 脚本引擎、ADR-13 指标扩展、Meta-Agent 移除），
它们不是"复用既有"，工期要按此估。

```
轨道 A（编译器）  P0 → P1 → P2 ──────────────────────┐
轨道 B（遥测）    P3 → P4 ───────────────────────────┼→ P9 在线接线 → P10 Python → P11 进化 → P12 TUI
轨道 C（夹具）    P5 → P6 → P7 → P8 ─────────────────┘
```

| P | 内容 | 依赖 | 备注 |
|---|---|---|---|
| **P0** | `internal/plasma` 骨架：IR + `Elaborate` + diag 码表 + 阶段 1–3 + golden 诊断 + `check_plasma_leaf.sh` | — | 纯 Go |
| **P1** | `PlasmaPalette` + 升级门 + `renderCustomAgentToolPool` 改喂 manifest + 负样本/变异 | P0 | **含 `EffectToolLoad` 修复**（今天 Cold 工具取不回） |
| **P2** | 阶段 4 Consistency：token 预算 + prompt↔schema 对账 + thinklink 契约 | P0 | 性能主导项（§9.1） |
| **P3** | **ADR-13**：`ExecutorResult`/`AgentResult` 加 `Steps`/`Usage`/`StopKind` + `FinalizeResultFull` 透传 | — | 只改 2 处带数据构造点；先喂聚合测试（坑 18） |
| **P4** | store（内容寻址/hash/夹具不可变/GC）+ 加载期（字典序/预算/省略账）+ 遥测写入 | P0,P3 | 写入点 `directorToolRunner.Call:748` |
| **P5** | **ADR-12**：`internal/llm/scripted` + `CallOptions.AgentName` + 9 个 e2e 改用它 + 显式身份注入 | — | 单项最大改动；保留三条纪律 |
| **P6** | `FixtureRunner` 窄接口 + `agents` 侧实现（构建 Surface）+ 5a 四夹具 + grade 棘轮 | P4,P5 | exit-contract 脚本必须含 assistant 文本 |
| **P7** | `yagent plasma` CLI：compile / sample / test / manifest / list / promote | P6 | `sample` 只服务离线 |
| **P8** | **trampoline 驱动器** + workdir 契约（§6.3）+ `Spawner` + 超时/清理/孤儿 GC + `max_llm_calls` | P2,P6 | 先用**假 Python**（shell 脚本按契约 exit 4/0）打通，不依赖真 zipapp |
| **P9** | **在线接线 + Meta-Agent 移除**：`runMetaDesign` 换成驱动器；删 `meta.go`/`meta.prompt.md`（内容迁 Python）/`ParseMetaAgentOutput`/`MetaAgentResult`；`delegateAvailable("meta")` 改判 plasma；`executeCustomAgent` 改读 `bundle.runtime` | P8 | **golden 与 50 条基线不动**（`delegate_meta` 工具保留）；根 AGENTS.md §4 的 7→6 要同步 |
| **P10** | Python zipapp：`generate`（含 trampoline 契约）+ author-fixtures + explain；embedbin 接入 + 降级 | P7,P8 | `meta.prompt.md` 的 108 行原则作为策略种子 |
| **P11** | 5b + judge + evolve + Goodhart 三防线 | P6,P10 | `evolve_enable` 默认关 |
| **P12** | 内建提案 + TUI `/plasma` `/proposals` | P11 | 需要前面全部的可信度 |

**P8 先用假 Python 打通**是关键排序：trampoline 的契约（exit 4 / 序号对齐 / 超时 / 清理）
可以在没有任何 Python 代码的情况下被测死，这样 P10 的 Python 实现只是"填契约"，
不会把协议缺陷和策略缺陷混在一起调试。

---

## 13. 风险登记册

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| 无 python3 ⇒ 生成不可用 | 中 | 中 | ADR-15 有意取舍；zipapp 已内嵌，缺的只是解释器；降级原因可见 |
| trampoline 往返拖慢生成 | 低 | 低 | ~300ms × 采样次数，相对 LLM 秒级可忽略（§9.2） |
| Python 状态序列化出错 ⇒ 重生成错乱 | 中 | 中 | `state.json` 带 schema_version + 序号；不匹配则整轮重来而非硬跑（照 `ResumeState.Digest` 的取向） |
| 孤儿 workdir / 孤儿进程 | 中 | 中 | defer 清理 + 启动 GC（`workdir_ttl`）+ `WaitDelay` + `supervise` |
| Python 死循环刷采样 | 中 | 中 | `max_llm_calls` + `meta_retry_count` 双上限（T9） |
| P5 触碰 9 个 e2e 文件 | 确定 | 中 | 逐字保留三条纪律；先让 9 个文件全绿再动 plasma |
| P9 删 Meta-Agent 牵连 61 处引用 | 确定 | 中 | §2.2 的处置表逐行结算；golden 与基线不动作为闸门 |
| P3 改核心结果结构体 | 确定 | 中 | 只 2 处带数据构造点；复用既有 stop_kind 词表；聚合测试先喂新输入 |
| 5b 成本 | 中 | 中 | 双预算 + 只在升 grade 时跑 + hash 缓存 |
| Goodhart | 高 | 高 | 夹具不可变 + 出题分离 + 生产项 |
| 前缀缓存 / token 膨胀 | 中 | 中 | 字典序 + 预算 + 省略账 |
| store 膨胀 | 中 | 低 | TTL + GC |
| 编译器规则漂移 | 低 | 高 | 调用 `ValidateDynamic`/`ValidateProfile` 而非复刻 |

---

## 14. 开放决策

| ID | 问题 | 倾向 |
|---|---|---|
| OD-2 | `generate` 首版是否直接产出完整 IR，还是先沿用 5 字段最小 spec？ | **先最小 spec**（迁移成本低、`meta.prompt.md` 可直接当种子），完整 IR 留给进化路径 |
| OD-3 | Python 进度事件（stderr NDJSON → `sub_agent_activity`）是否首版就做？ | 后置到 P12：LLM 输出的流式已由 Go 侧保证，进度可见性是锦上添花 |

**OD-1 已拍板（2026-11-09）：一次全改。** P5 提升脚本引擎时，9 个 e2e 测试文件在同一个 commit 里
全部改用 `internal/llm/scripted`，不留新旧并存窗口（并存就是坑 33 的两份同形实现）。

已定不再开放：编译器归 Go（ADR-1）、trampoline（ADR-14）、无 Go 兜底（ADR-15）、
在线止于 provisional（ADR-3）、升级门钉 provisional + 问用户（ADR-4）、**9 个 e2e 一次全改（OD-1）**。

---

## 15. 引用索引（核实于 2026-11-09）

| 引用 | 内容 |
|---|---|
| `internal/agents/meta.go:44 / :63-68 / :95 / :117 / :176` | MetaAgent.Run / StreamHandler→ai_chunk / ai_stream_start / ai_stream_end / ExecutorResult 构造点（**P9 删除**） |
| `internal/agents/meta.prompt.md`（108 行） | 提示工程原则 + 5 字段契约 + `{{TOOL_POOL_TABLE}}`（**迁 Python**） |
| `internal/agents/deps.go:50 / :97` | surfaceFor / renderCustomAgentToolPool（**改喂 manifest**） |
| `internal/agents/specs.go:74` | `delegate_meta` Spec：Mutates=false / Serial=true / Audience=director（**保留**） |
| `internal/agents/testdata/tool_surfaces.golden:12` | `delegate_meta  tier=hot  serial`（**不变**） |
| `internal/agents/director_surface.go:137-176 / :144 / :151 / :170-175 / :182 / :199-253 / :207 / :261 / :284` | delegateTo 三分支 / meta 提前 return / custom 提前 return / builtin 收尾 / runMetaDesign / retry 循环 / "JSON 无效"文案 / runCustomAgent / registerCustomAgent |
| `internal/agents/director.go:172 / :302 / :336-366 / :442-460 / :454 / :462 / :698 / :727 / :744 / :748-755 / :816-821` | metaHandler 构造 / toSnakeCase / executeCustomAgent / delegationOutcome / recordDelegationOutcome / applyEnhancedCommander / **directorToolRunner.Call** / outcome 载体 / ad.Call / **遥测写入点** / prompt 拼装顺序 |
| `internal/agents/director/meta_handler.go:35 / :42 / :71 / :107` | 注册表（保留）/ 构造为空 / List 插入序 / ParseMetaAgentOutput（**P9 删除**） |
| `internal/agents/director/types.go:22` | CustomAgent 纯数据五字段 |
| `internal/agents/executor.go:29 / :59 / :66-69 / :184 / :263 / :266 / :570-577 / :736 / :787` | ExecutorConfig / DefaultExecutorConfig / **ExecutorResult 只有 Text+History** / RunAgentLoop / opts 构造点 / StreamHandler / usage 只进事件 metadata / 两处带数据的返回 |
| `internal/agents/result.go:52-70 / :58 / :197` | deliverableText 回落语义 / 函数 / FinalizeResultFull |
| `internal/agents/types.go:14` | AgentResult（**无 Steps/Tokens/Usage/StopReason/StopKind**，grep 零命中） |
| `internal/agents/resume_state.go:25-43` | ResumeState：Messages/StartStep/Materialized/ThinkLink/DelegationID/Digest（**trampoline 状态先例**） |
| `internal/agents/e2e_engine_test.go:23 / :41-58 / :60 / :69-72 / :96-98` | agentScriptedEngine / **agentIdentity 靠 prompt 首行嗅探只认四个内建** / newScripted / 先看 ctx 纪律 / 步骤锁外跑纪律 |
| `internal/memory/rollout_types.go:298-303 / :322-336` | StopKind 词表 / **AgentCheckpoint**（trampoline 状态先例） |
| `internal/toolspec/profile.go:11 / :23-27 / :174 / :196` | Profile / Names 通道 / MatchNames / 自动追加 agent_exit |
| `internal/toolspec/profiles.go:6-8 / :32 / :33-34` | golden 钉住 / Director Deny / **Palette 含 FSWrite\|Shell 且不含 EffectToolLoad** |
| `internal/toolspec/spec.go:108 / :168 / :225` | Validate / ValidateDynamic / ValidateProfile |
| `internal/toolspec/schemas.json`（50 条，`agent_exit`） | 基线 50 条含 `delegate_meta`；`agent_exit` required=`["reason"]`，props=`reason`/`handoff`/`blueprint`（**无 summary/deliverable**） |
| `internal/tools/surface.go:413` | Surface.Add 拒重复 |
| `internal/tools/specs_browser.go:54-59` | **browserAvailable**：缺依赖返回 `(false, 原因)`，无 Go 兜底（ADR-15 先例） |
| `internal/tokenutil/tokenutil.go:30` | EstimateTokens；**实测 8k=5.0ms、首次=51.4ms**（§9.1） |
| `internal/thinklink/thoughtplan.go:195` | ThoughtPlan.Validate() |
| `internal/llm/engine.go:216 / :219 / :226 / :229` | CallOptions 四字段（**无 AgentName**）/ StreamHandler 字段 / 类型 / Engine 接口 |
| `internal/llm/router.go:52 / :139 / :176` | 默认基线 / 按小写名查 / 合并配置 |
| `internal/util/atomic.go:41` | 唯一原子写内核 |
| `internal/app/app.go:167-168 / :389 / :470 / :486` | 提取失败只 Debug / **NewMetaAgent 装配（P9 删）** / MetaAgent 字段 / IntegrateMessaging（**dispatcher 每任务创建，子进程拿不到**） |
| `internal/tui/tui_update_tasks.go:380 / :406 / :469` | ai_stream_start / ai_chunk / ai_stream_end 消费 |
| `internal/config/config.go:91 / :260 / :649-650` | meta_retry_count / "Meta 是一次 LLM 调用"注释 / 归一化 |
| `internal/config/defaults.go:8 / :16 / :24` | Meta 不在 step 表 / MetaRetry 字段 / **默认 5** |
| `internal/agents/toolbatch/plan.go:20` | 仅注释提到 delegate_meta（无需改动） |
| `main.go:79` | setupCmd 先例 |
| Meta 足迹（实测） | 61 处非测试引用，分布 10 文件：app / config×2 / toolbatch / specs / director×3 / meta / director_surface |
| 脚本引擎分布（实测） | `agentScriptedEngine`/`scriptStep` **只在 9 个 `_test.go`**：e2e_engine / e2e_lifecycle / e2e_blueprint / e2e_pause / e2e_replay / e2e_handoff / e2e_resume / e2e_mcp / resume_degrade |

## 16. 实施记录

### P0 已落地（2026-11-09）

**新增** `internal/plasma`（叶子包，9 个文件）：`ir.go`（Bundle + 三档 Grade + Fixtures 形状）、
`diag.go`（13 个码 + `Report` 按发射顺序渲染）、`elaborate.go`（最小 spec / 完整 IR 两形态 +
注入式 `Defaults`）、`compile.go`（阶段 1–3）、`suggest.go`（Levenshtein did-you-mean）。
测试用外部包 `plasma_test`，blank-import `agents` + `tools` 以 populate **真实的 50 条注册表**。

**闸门**：`scripts/check_plasma_leaf.sh`（守卫 11→12，已接进 `check_all.sh`）；
`internal/plasma/AGENTS.md`（11 条硬约束 + 闸门清单）；`testdata/diagnostics.golden`（16 个 corpus，
13 个码全覆盖，`TestCorpusCoversEveryCode` 钉住"每个码都被渲染过"）。

**顺带的单源化**：`agents.toSnakeCase` → `util.SnakeCase`（原先只有一个生产调用点，
复制一份就是坑 18/33）；`toolspec.EffectFromNames` 新增（`effectNames` 表的逆运算，
`policy.deny` 需要它，认不出的名字报 PLS-207 而不是静默丢掉）。

**结算的既有基线**：`blueprint/scan_golden_test.go` 自扫 45 片/145 边 → **46 片/148 边**
（三条新边：plasma→toolspec、plasma→util、agents→util，逐条 `go list` 核实）；
根 `AGENTS.md` 的守卫数与文档数同步，**靠把 §9.2 的两行棘轮纪律压成一行保住 608 行冻结上限**；
`internal/AGENTS.md` 加一行目录表（199→200，正好达上限）；
`blueprint/AGENTS.md` 那条自扫数字原本就已过期（写着 44/143），一并修正。

**验证**：`bash scripts/check_all.sh` **exit 0**（12 把守卫全跑）；
`tool_surfaces.golden` 与 `schemas.json` **零改动**（`git status --porcelain` 为空）；
`TestSchemasLoadAndDescribeEveryEntry`（50 条）与 `TestAgentSurfacesGolden` 均绿。
**6 条变异在 `/tmp` 副本上实做，全部变红**：去掉 palette 判定 / 去掉 agent_exit 判定 /
snake_case 正则恒真 / 去掉 did-you-mean / 下标恒 0 / Elaborate 无视注入的 MaxSteps。

**与本文的偏差**：① `Elaborate` 签名多了 `Defaults` 参数（叶子包不能 import config，
配置值必须由装配层注入，§7.1 已按此写）；② `Compiler.Palette` 是字段而非 `Paletter` 函数，
且**刻意不给默认值**（零 palette 拒掉一切，"忘配"必须是全拒不是全放行）；
③ `PlasmaPalette` 未在 P0 定义（属 P1），P0 由测试注入只读族画像；
④ P0 复核时发现 ADR-4 原稿把 `EffectExternalRead|EffectSkill` 放进了 `PlasmaPalette`，
与 `toolspec/AGENTS.md` 硬约束 6.1"Palette 一个位都不加（Custom Agent 拿不到第三方能力）"冲突，
**已修文档去掉这两位**（第三方 MCP 输出是 T2 的注入向量，不该默认给生成式 Agent）。

**待办**：P1（PlasmaPalette + 升级门 + 工具池改喂 manifest）。

### P5 已落地（2026-11-09）

**代码在上一轮已落地，本轮不改一行**（ADR-12 选项 (a) 的结果）：`internal/llm/scripted`（253 行，
唯一实现，`New`/`NewWithResolver`/`NewForIdentity` 三个构造器）+ `llm.CallOptions.AgentName`
（`engine.go:223-226`）+ `agents.ExecutorConfig.AgentName` 填进 opts；9 个 e2e 文件经
`e2e_engine_test.go:18-22` 的类型别名全部指向 `scripted.Engine`，**无新旧并存**。

**本轮补齐的验收配套**（§11 与 §16 自己要求、上一轮漏掉的三件）：
① **新闸门** `scripts/check_llm_scripted.sh`：scripted 只准 import **stdlib + `yagent/internal/llm`**。
判据是**直接 import**（`go list -f '{{join .Imports}}…{{join .TestImports}}…{{join .XTestImports}}'`），
**刻意不用 `-deps`**——`internal/llm` 自己依赖 config/logging/models/util，传递闭包会把它们全误报成越界。
已接进 `check_all.sh`（守卫 12→13，排在 `check_plasma_leaf` 之后）。
② **`internal/llm/scripted/AGENTS.md`**（62 行，新建）：三条纪律各自绑定闸门名（
`TestEngine_DispatchesByIdentityNotCallOrder` / `TestEngine_HonoursContext` / `TestEngine_StepRunsOutsideLock`）、
三种身份注入分工、`AgentName` 只是进程内通道、耗尽回落为纯文本、`call_<tool>` ID 契约、import 约束、
`Model()` 只是接线替身（质量验证在 ADR-3 的离线侧）。
③ **两处 `agentIdentity` 过期引用**：该符号在代码里已不存在（实现是未导出的 `scripted.sniffIdentity`），
已改 `internal/agents/AGENTS.md` 的闸门节与 `e2e_pause_test.go` 的 `codingSystemPrompt` 注释
（后者标为**仅供 e2e 的嗅探兼容路径**，5a 走 `NewForIdentity` 不看首行措辞）。

**行数棘轮的结算**：根 `AGENTS.md` 仍 **608 行**（新守卫并进 `check_plasma_leaf` 那一行，不增行；
"根 + 18 份"→"根 + 19 份"）；`internal/agents/AGENTS.md` 仍 **212 行**（四行改写为四行）；
`internal/tui/AGENTS.md` 仍 **226 行**（"十一把守卫"→"十三把"）；`internal/llm/AGENTS.md` 140→142 行
（加一行子包指针，远低于 200 硬上限）。

**验证**：`go build ./...` / `go vet ./...` 干净；`go test ./internal/llm/scripted/ -count=1`（8 条）
与 `go test ./internal/agents/ -count=1`（9 个 e2e）全绿；`bash scripts/check_all.sh` **exit 0**
（13 把守卫 + 全量单测）。**闸门变异在 `/tmp` 副本上实做**：给 `scripted.go` 加
`_ "yagent/internal/config"` → 红（点名该 import）；再给 `scripted_test.go` 加
`_ "yagent/internal/toolspec"` → 也红（证明 `TestImports` 分支不是空的）；删掉两行即回绿。
`internal/agents/testdata/tool_surfaces.golden` 与 `internal/toolspec/schemas.json` **零改动**。

**与本文的偏差**：① §11 只写"另需一条『只准 import llm + stdlib』约束"，没写清它必须查**直接依赖**
而不是闭包——照 `check_plasma_leaf.sh` 的 `-deps` 形状写会立刻误报，已写进守卫注释与本节；
② §16 原先只有 P0 一节，P1–P4 未按节追加，本轮只补 P5、不回填历史；
③ `internal/llm/scripted` 的局部文档是**新建**，不是"同步既有文档"。

**待办**：P6（`FixtureRunner` 窄接口 + `agents` 侧实现 + 5a 四夹具 + grade 棘轮）。

### P6 已落地（2026-11-09）

**新增**：`internal/plasma/fixture.go`（叶子包）—— `Outcome`/`Outcomes`、`FixtureRunner`
窄接口、`StructuralFixtures` 四条自动夹具、`ensureStructuralFixtures`、`VerifyStructural`、
`Ratchet`/`CanPromote`；`diag.go` 补 5 个 5xx 码（`PLS-501`–`505`）。
`internal/agents/plasma_fixture.go` —— `PlasmaFixtureRunner`：静态夹具经 `surfaceFor` 装配真实
Surface，行为夹具经 `scripted.NewForIdentity` + `RunAgentLoop` 在**生产路径**上回放；
`evaluateExpect` 是 Expect 的唯一求值点。测试：`internal/plasma/fixture_test.go`（10 条，外部
测试包 `plasma_test`）、`internal/agents/plasma_fixture_test.go`（7 条，四夹具**各一绿一红**）。

**顺带修的真实缺陷**：`Link` 原先给 `Profile.Names` 的是一份原始 `b.Tools`（不含 `MatchNames`
自动追加的 `agent_exit`，也没有 `Palette`）。而 Surface 的按名通道（`toolspec.matchByNames`）
**只认清单、不补收尾工具** —— 拿这份 LinkPlan 装配出的 Agent 会无法收尾，只能烧到 `max_steps`。
`tool-containment` 夹具第一次跑就红在这条上；现已改为 `Names`=解析后名单（与 `Agent.Tools`
同源）、`Palette`/`Grant`=`effective()`，夹具判据与阶段 3 的 typecheck 因此同源。

**与本文的偏差**：
1. **exit-contract 的脚本是一条响应**（正文 + `agent_exit` 同一个 Choice），不是 §4.3 写的
   "先一条 assistant 文本、再一条工具调用"。执行器在"没有 tool_calls"的响应上直接按纯文本
   收尾，两步写法走不到第二步（实测表现：`stop_kind=plain_text`、`must_call agent_exit 未被调用`）。
   为此在 `internal/llm/scripted` 增加 `TextAndToolCall`（真实模型本来就会这样回）。**断言语义
   不变**：仍是"交付文本取自那条 assistant 消息，而不是退出 JSON"。
2. **`Expect.ExitShape` 是既有字段 `[]string`**（§7.1 的示意写成标量），实现按"必须命中的形状
   集合"解释（`agent_exit` / `handoff`），未改字段名或类型。
3. **静态夹具不是纯静态推导**：`tool-containment` 走真实 `surfaceFor` 装配（`ProbeDeps` + 真实
   `FlowControlTool`），因为它要验的正是"装配后模型看到什么"；`ProbeDeps` 的 Flow 是空实现，
   直接用它会让 exit-contract 永远拿不到可解析的退出 JSON（夹具自己装错，不是契约被违反）。
4. **`Outcome.Steps` 在预算耗尽口由 runner 补齐**：`executor` 的 max_steps 出口返回零值结果 +
   error（错误路径不伪造成功结果），所以步数判据用 runner 算出的 `stepsDone`；直接读
   `exec.Steps` 会让"步数超限"永远为假。
5. **自动夹具同时补齐 `fixtures.provenance`**（`plasma-compiler-auto`/`compiler`）：否则
   `store.Put` 的 `checkFixtureProvenance` 会按「自己出题自己考」拒收（编译能过、存不进去）。
6. 5xx 码按夹具 ID 分派（502 exit / 503 step / 504 tool / 505 palette），未登记的一类回落 501；
   §6.1 只写了"5xx verify"，没规定到码级。
7. `Ratchet` 把"没跑"与"通过"分开：空 `structural` 或空 `behavioral` 都**不**算通过（空集的
   `AllPassed()` 为真，照搬会白送 provisional/verified）。

**验证**：`go build ./...` / `go vet ./...` 干净；`go test ./internal/plasma/`（新 9 条）、
`go test ./internal/agents/`（9 个 e2e 回归 + 新 7 条）、`go test ./internal/blueprint/`
（自扫仍 **46 片 / 151 边** —— 新增的 `agents → llm/scripted` 生产边未改变片/边计数）全绿；
`check_plasma_leaf.sh` 与 `check_llm_scripted.sh` exit 0；`bash scripts/check_all.sh` **exit 0**
（13 把守卫 + 全量单测）。`internal/agents/testdata/tool_surfaces.golden` 与
`internal/toolspec/schemas.json` **零改动**；`diagnostics.golden` 为 **`25 insertions(+)`**，
即 5 段新增、无删除无重排（`git diff --stat` 可核）。

**行数结算**：根 `AGENTS.md` 仍 **608**；`internal/agents/AGENTS.md` 仍 **212**；
`internal/plasma/AGENTS.md` 167 → **196**（≤200 硬上限）；新增 `fixture.go` 293 行、
`plasma_fixture.go` 420 行（都不在 `check_file_sizes.sh` 的 `internal/agents/director` 范围）。
`check_agents_md_size.sh` exit 0。

**待办**：P7（`yagent plasma` CLI：compile / sample / test / manifest / list / promote）。
5b（真 LLM 行为夹具）仍在 P11，`RunBehavioral` 目前恒返回"未执行"。

### P7 已落地（2026-11-09）

**新增**：`internal/config/plasma.go` + `plasma_test.go` —— `[plasma]` 的 18 个键、`PlasmaPresence`
（Section / Enable / catalog_line_chars / workdir_ttl / store_ttl）、`NormalizePlasma`（**返回 error**）、
`SetGlobalPlasma`/`GlobalPlasma`/`PlasmaEnabled`/`PlasmaStoreDir`/`PlasmaProposalDir`；
`default_config.toml` 与 `config/config.toml` 各加一段 `[plasma]`。
`internal/plasma/manifest.go` + `manifest_test.go` —— `Manifest`/`ManifestTool`/`ManifestCode`/
`ManifestDefaults`、`(*Compiler).Manifest()`、`CapabilityLock`；`diag.go` 新增 `CodeInfo`/`DiagCatalog()`
（码表唯一来源）。`store.go` 新增 `SetGrade`。根包新增 `cmd_plasma.go`（命令树 / flag / 日志与退出码）、
`cmd_plasma_support.go`（配置派生默认值、升级批准、compile 前半）、`cmd_plasma_compile.go`
（compile / test 执行体）、`cmd_plasma_sample.go`、`cmd_plasma_promote.go`（manifest / list / promote）、
`cmd_plasma_render.go` + `cmd_plasma_render_store.go`（JSON 与人读两路渲染）、`cmd_plasma_test.go`。

**与本文的偏差**：
1. **`NormalizePlasma` 返回 error 而不是 warn 后回落**，与 `internal/config/AGENTS.md` 硬约束 12
   （认不出的字符串枚举 warn + 回落）刻意不同：`auto_delegate_min_grade` 是自动委派的安全边界，
   回落成任何一档都可能在放行未验证的 Agent；负时长静默归零会让超时 / 清理变成"没有"。
   判据在 `checkPlasmaDurations` / `checkPlasmaGrade`，由 `LoadFromFile` 归入
   `config validation failed: %w`。
2. **`PlasmaPresence` 多一个 `Enable`**：§7.4 没点名，但 `enable = true` 的缺省与 Go 零值相反，
   不看键的存在性就会把"没写"当成"用户关掉了"（坑 26，与 `[blueprint]` 同形）。
3. **`Manifest` 不调 `Spec.AvailableFor`**：`renderCustomAgentToolPool` 有这一步，但那是运行期装配
   属性（某个 MCP server 没起来），而 manifest 是**能力契约**。判据仍是同一句 `Effect.Has(Palette)`。
4. **`plasma compile` 只读、不落盘**：§7.2 的表格没有落盘语义，落盘属 P8 的 trampoline。因此
   `promote` 的输入来自别处写入的 store（P7 之前只有测试或未来的 generate 会写）。
5. **`compile` 的升级批准读取顺序**：`ensureStructuralFixtures` 在 `Compile` 内部补夹具 ⇒ hash 只有
   编译之后才算得准。顺序因此是 Compile → `HashBundle` → `store.Load` 读批准 → 重编一次
   （纯计算、幂等）。批准位用 `BeyondBase(声明集合, Palette)` 算，不重写能力数学。
6. **`plasma test` 的 `grade` 打印成 `n/a`**：单条夹具不参与 `Ratchet`（要四条都有才有意义），
   用 `(none)` 会看起来像"这份 bundle 掉档了"。
7. **`--tier` 的落点是 `Client.GetRouter()` + `Router.EngineFor/ModelFor`**：既有 `RoutingEngine`
   按 agent 基线选档，而 CLI 没有 agent 身份，所以直接取档位引擎；名字合法性复用
   `llm.ParseTier(s, unknownTier)` 的哨兵，不新写一份 tier 词表。路由未启用时明确写"改用固定
   provider"，不假装 tier 生效。
8. **`promote` 判据不过时不动 `index.json`**；已 promoted 的版本再 promote 只改指针（回滚语义）——
   后者若也走 `CanPromote` 会被 `grade == verified` 挡住，回滚就永远不可用。
9. **`--behavioral` 仍是占位**：`RunBehavioral` 返回"未执行 + 原因"，CLI **如实打印为未执行**，
   不谎报通过（5b 属 P11）。两种"未执行"分开措辞：没给 `--behavioral` 说"才跑"，给了但 bundle
   里没有 behavior fixtures 说"已给但没有夹具"；`--json` 用 `behavioral_requested` 让机读侧也能分
   （把两句混成一句 = 把"我请求了但它没跑"报成"我没请求"）。
10. **夹具 runner 的 `Deps` 留 nil**：`agents.PlasmaFixtureRunner.fixtureDeps` 在 `Deps == nil` 时
    会注入真实 `FlowControlTool`；显式传 `ProfileDirector.ProbeDeps()` 会让 exit-contract 永远
    拿不到可解析的退出 JSON（`ProbeDeps` 的 Flow 是空实现）。

**验证**：`go build ./...` / `go vet ./...` 干净；`go test ./internal/plasma/ ./internal/config/
./internal/agents/` 全绿；真二进制冒烟（`go build -o /tmp/yagent-p7 .`）→ `plasma manifest`
（19 工具 / 25 码 / lock）、`plasma list`（空 store **exit 0**）、`plasma compile --bundle` 一份最小
只读 spec（四条结构夹具全 PASS、`grade=provisional`、exit 0）、`plasma test --fixture
palette-containment`（exit 0）、`plasma test --fixture nope`（**exit 2**）、对幻觉工具名 compile
（输出含 `PLS-101`、**exit 1**）、`plasma compile --behavioral`（bundle 里没有 behavior fixtures →
文本写"已给但没有夹具"、`--json` 里 `behavioral_requested=true`，两种"未执行"可区分）。变异两条全部转红并还原：`CanPromote` 恒真 ⇒
`TestRunPlasmaPromoteBranches` 红；`compile` 去掉 `report.HasErrors()` 判断 ⇒
`TestRunPlasmaCompileRejectsUnknownTool` 红。`check_plasma_leaf.sh` / `check_llm_scripted.sh`
exit 0；`bash scripts/check_all.sh` **exit 0**。`tool_surfaces.golden` 与 `schemas.json` **零改动**。

**行数结算**：根 `AGENTS.md` 仍 **608**（§8.2 加一行命令，等量压缩同段散文）；`internal/agents/AGENTS.md`
仍 **212**；`internal/plasma/AGENTS.md` 196 → **200**（≤200 硬上限）；新增 `cmd_plasma.go` 210 行、
`cmd_plasma_support.go` 147 行、`cmd_plasma_compile.go` 143 行、`cmd_plasma_sample.go` 87 行、
`cmd_plasma_promote.go` 89 行、`cmd_plasma_render.go` 198 行、`cmd_plasma_render_store.go` 82 行、
`internal/config/plasma.go` 371 行、`internal/plasma/manifest.go` 156 行（都不在
`check_file_sizes.sh` 的 `internal/agents/director` 范围）。
`check_agents_md_size.sh` exit 0。

**一处形状变化（必须说明）**：`internal/blueprint/scan_golden_test.go` 的模块级边数
**151/151 → 153/153**。原因：CLI 落在根包，根包首次 import `internal/plasma` 与 `internal/toolspec`
（两条新边）。片数仍 46，叶子性不受影响（根包不是叶子）。

**待办**：P8（trampoline 驱动器 + `Spawner`）。P9（加载期接线、`auto_delegate_min_grade` 的自动委派
判据、升级批准的用户回路）。P10（进化器 —— `evolve_*` 与 `proposal_dir` 目前只有配置没有读者）。
P11（5b 真 LLM 行为夹具）。

### P8 已落地（2026-11-09）

**新增**：`internal/plasma/workdir.go` + `workdir_test.go` —— 退出码契约（`ExitOK/Failed/Usage/
Timeout/Sampling`）、契约文件名常数（`FileReq`/`FileManifest`/`FileState`/`FileDiag`/`FileBundle`）、
`Workdir`（`NewWorkdir` 0700 + 绝对路径、`LLMReq/LLMResp` 的 `%03d` 序号、`WriteReq`/`WriteManifest`/
`WriteDiagnostics`/`ReadDiagnostics`/`ReadBundleRaw`/`WriteBundleRaw`/`WriteLLMResp` 全部走
`util.AtomicWrite`、`Cleanup` 幂等）、`GCWorkdirs`、`Spawner` 窄接口、`SpawnError` + `NewSpawnError`
（stderr 尾部截断到 2KB）、`ExitCodeName`、`TruncateTail`（按 UTF-8 边界回退）。
`internal/agents/trampoline.go` + `trampoline_test.go` —— `PlasmaTrampoline`（`Generate` 的
spawn → `exit 4` 采样 → respawn → 编译 → `--fix` 循环；`validate` 逐个点名缺失依赖）、
`llmRequestFile`、`TrampolineResult`、Go 侧 `chunkPublisher` 与 `ai_stream_*` 事件。
`internal/agents/spawner.go` —— `ExecSpawner`（`exec.CommandContext` + `cmd.WaitDelay` +
有界 `tailBuffer`；`exit 4` 返回 `(4, nil)`，ctx 结束优先判成 `ExitTimeout`）。
`diag.go`：`Diagnostic`/`Report` 加跨语言 JSON tag（`code`/`severity`/`path`/`message`/`note`、
`diagnostics`/`grade`/`escalation_required`）。

**与本文的偏差**：
1. **`Spawner` 与退出码落在 `workdir.go`，不新开 `trampoline.go`**：契约（文件名 + 退出码 + 接口）
   必须待在同一文件才挡得住"改一半"；驱动器在 agents 侧（`trampoline.go`），因为循环要 llm/EventBus/os/exec。
2. **`llm_req` 刻意没有 `tier` 字段**：引擎本身绑定档位，Go 侧拿到一个 tier 字符串也无处施加
   （`CallOptions` 没有该字段）。多一个没人使用的键就是坑 35 的装饰性配置。
3. **`Elaborate` 失败也写成一份 `diagnostics.json`**：把 `*DiagError` 转成单条 `Report` 再落盘。
   直接终止的话 `--fix` 轮永远看不到"上一轮为什么被拒"，Python 只会重复生成同一份坏 JSON。
4. **`max_llm_calls` 按全局采样计数**（跨 `attempt` 累加，不每轮重置）：T9 要防的是整次生成的
   死循环；每轮重置会让 `meta_retry_count × max_llm_calls` 变成实际上限。
5. **启动期 GC 不在 `Generate` 内**：驱动器只导出一个可调用点（`plasma.GCWorkdirs(base, ttl, now)`），
   由装配层（P9）在建 Director 时调一次 —— 每次委派都扫一遍父目录是无谓的 IO，而且 TTL 判据要配置值。
6. **单次 spawn 的超时是"驱动器套 ctx + 实现设 `WaitDelay`"两件事**：只有 `context.WithTimeout`
   时，子进程的子孙持有 stdout 管道会让 `cmd.Wait` 一直等 EOF，看门狗形同虚设（根 AGENTS.md 坑 46）。

**验证**：`go build ./...` / `go vet ./...` 干净；`go test ./internal/plasma/ ./internal/agents/`
全绿（含 9 个 e2e 与 P6 夹具回归）；`go test -race ./internal/agents/ -run 'Trampoline|Spawn|Workdir'`
全绿；`check_plasma_leaf.sh`（plasma 仍只依赖 stdlib + toolspec/util/projectid/tokenutil/thinklink）与
`check_llm_scripted.sh` exit 0；`bash scripts/check_all.sh` **exit 0**；`tool_surfaces.golden` 与
`schemas.json` **零改动**。**假 Python 五条**（全部真 `/bin/sh` 脚本，PATH 无关）：`exit 4 → 采样 →
respawn` 且序号从 `llm_req_000.json` 起、`diagnostics.json`(含 `PLS-101`) → `--fix` 重生成、`sleep 30`
在 `SpawnTimeout=300ms` 下被杀且 workdir 已删、无限 `exit 4` 停在 `max_llm_calls=3`（用例自带 10s
死循环保护）、`ai_chunk` 由 Go 侧 `StreamHandler` 产生（事件顺序 start < chunk < end，end 带 usage）。
**变异四条全部转红并还原**：attempt>0 去掉 `--fix` ⇒ `TestTrampoline_CompileFailureWritesDiagnosticsAndFixes` 红；
序号改 `%d` ⇒ `TestWorkdir_ContractFileNames` 红；删 `defer w.Cleanup()` ⇒
`TestTrampoline_SamplingThenRespawnSequence` 的 workdir 泄漏断言红；`res.LLMCalls <= MaxLLMCalls` ⇒
`TestTrampoline_MaxLLMCallsStopsRunaway`（LLM 被调 4 次）红。

**行数结算**：`internal/plasma/AGENTS.md` 仍 **200**（新增硬约束 26 与 P8 闸门，等量压缩 12/13/15/17/18/20/24）；
`internal/agents/AGENTS.md` 仍 **212**（闸门段加一条 P8，等量压缩超时样例与步数事实两条）；
根 `AGENTS.md` 未动。新增 `internal/plasma/workdir.go` 329 行、`workdir_test.go` 373 行、
`internal/agents/trampoline.go` 334 行、`spawner.go` 201 行、`trampoline_test.go` 565 行（都不在
`check_file_sizes.sh` 的 `internal/agents/director` 范围）。`check_agents_md_size.sh` exit 0。

**待办**：P9（把 `PlasmaTrampoline.Generate` 接进 `runMetaDesign`、装配 `ExecSpawner`（`[plasma] python`
+ 内嵌 pyz 路径）、启动期 `GCWorkdirs`、加载期接线与升级批准回路）。P10（Python zipapp —— 本阶段
全部用例都用假 Python 脚本，真包一次都没跑）。P11（5b 真 LLM 行为夹具）。

（后续每个 P 完成后在此追加。方案落地后本文可能被删——届时不变量须已同步进
`internal/plasma/AGENTS.md`、`internal/llm/scripted` 的局部文档与根 `AGENTS.md` §4/§5.3/§9.2/§11。）

### P9a 已落地（2026-11-09）

**删除**（Go 侧不再有任何生成器，ADR-15）：`internal/agents/meta.go`（180 行：`MetaAgent` + 单次 LLM
调用 + 流式）与 `internal/agents/meta.prompt.md`（108 行）；`director/meta_handler.go` 的
`ParseMetaAgentOutput` / `ExtractJSONObject`（`MetaAgentHandler` 的 List/Register/Get 原样保留，
P9b 还要用）；`director/types.go` 的 `MetaAgentResult`；`agents/deps.go` 的
`renderCustomAgentToolPool` + `effectCategory`（第二套"广告源"）；`DirectorAgent.MetaAgent` 字段与
`DirectorAgentParams.MetaAgent`（`refreshSubAgentEngines` 的 meta 分支随之删除）。
`internal/config` 的 `MetaRetry` / `[agent] meta_retry_count` **保留不动** —— 语义已改为"编译失败后
`--fix` 的轮数"（ADR-11），没有再新加第二个旋钮。`delegate_meta` 的 Spec 与
`testdata/tool_surfaces.golden` **一个字节都没动**（降级发生在 `AvailableFor` 这一层，不是删 Spec）。

**迁入**：`meta.prompt.md` 的 108 行提示工程原则 + 5 字段输出契约 + `{{TOOL_POOL_TABLE}}` 占位符整体
搬到 `plasma/prompts/generate_seed.md`（118 行，头部注明来源与"Go 侧不再有生成器"）。新顶层目录
`plasma/` 目前只有这一个文件，Python 包在 P10 落地；行数/文档/扫描三类守卫都无需改动即通过。
Go 侧不再渲染工具池表格：`plasma.Manifest().Tools`（判据仍是 `Effect.Has(PlasmaPalette)`）由 Python 读、
表格由 Python 填 —— 两份表必然漂移，那正是当年"广告了实现不了的工具"的形状。闸门
`TestPlasmaToolPoolMatchesPalette`（由 `TestMetaPromptToolPoolMatchesPalette` 改写）同时钉
"manifest 与 PlasmaPalette 一致"与"种子里只留占位符、没有硬编码工具表"。

**驱动器**：`runMetaDesign` 整段换成 `internal/agents/plasma_meta.go`：
`Generate → Compile（归一化字节，先补结构夹具再算 hash）→ Link（对活注册表复核）→
VerifyStructural（5a，在线必跑）→ Ratchet（behavioral 传空、escalationApproved=false ⇒ 最高
provisional）→ Store.Put（report 里写这个 grade）→ registerCustomAgent → setExecutorConfig →
runCustomAgent → rollout/workdir 收尾`。错误文案改成"可执行的下一步"：Link 失败与 5a 失败都带
`report.Text()` 前 5 行 + `plasmaFixHint`，"your output was NOT valid JSON" 这类只描述格式的文案已删除。
`DirectorAgent` 新增窄接口 `plasmaGenerator`（`Generate(ctx, task, contextNote)`），`nil` = 降级；
`Publisher` 直接用 Director 的父进程 publisher（不新建 dispatcher，否则 TUI 会多出一条不归任何
Agent 的 chunk）；`Store`/`FixtureRunner` 可注入 —— 用例指到临时目录，绝不写用户 home 的产物仓。

**降级可见（ADR-15）**：`internal/agents/plasma_gate.go` 的 `plasmaAvailable() (bool, string)` 三段判据
（`[plasma] enable` → 解释器（显式路径存在，否则 PATH 上的 `python3`）→ zipapp 存在），每条原因都是
"缺什么 + 怎么修"。`toolspec` 只声明 `PlasmaResolver` 窄接口（叶子约束），生产实现是
`plasmaGate{gen, reason}`，由 `DirectorAgent.toolDeps()` 注入；`delegateAvailable("meta")` 改判它 ——
不可用时 `delegate_meta` **不进 `Defs()`**，原因进 `Surface.Skipped` 并由 `surfaceFor` warn 出来
（不是 `slog.Debug`）。装配层 `internal/app/plasma.go`：`buildPlasmaGenerator`（判据不过就连
trampoline 都不建，避免"每次委派 exit 1"）+ `gcPlasmaWorkdirs`（启动期一次 `plasma.GCWorkdirs`，
结果一行 `plasma-gc` 日志）。P10 之前 `plasma.pyz` 天然不存在 ⇒ 默认部署就是降级，这是**预期**状态。

**执行参数改读 `LinkPlan.Config`**：`executeCustomAgent` 不再硬编码 `MaxSteps=15 / StopOnFinish=true /
LLMTimeout=currentLLMTimeout()`，改由 `setExecutorConfig` 登记的 `plasma.ExecutorConfigView` 套用
（`MaxSteps`/`StopOnFinish`/`LLMTimeout`/`MaxParallelReadOnlyTools`/`KeepToolResultTokens`/
`CompressionEnable`；`Compression.Threshold` 等其余字段保持 `DefaultExecutorConfig` 的值）。
**没有 plan 的老路径整套回落历史值并 warn**：第一版把零值 view 套上去，`StopOnFinish` 的零值 false
让 `agent_exit` 不再终止循环，`TestCustomAgentDelegateTool_FinishTerminates` 从 1 次 LLM 调用变 2 次 ——
这条用例就是那个回落的闸门。

**验证**：`go build ./... && go vet ./...` 干净；`go test ./internal/agents/... ./internal/app/...
./internal/config/` 全绿（9 个 e2e 与 P6/P8 用例未动）；`bash scripts/check_all.sh` **exit 0**；
`git diff --stat internal/agents/testdata/tool_surfaces.golden internal/toolspec/schemas.json` **零输出**。
**降级断言两份**：`TestDelegateMeta_GatedByPlasmaAvailability`（不可用 ⇒ 不在 `Defs()`、`Skipped()`
含 `plasma.pyz` 与"修复"；同一条用例带正面对照"接上生成器就出现"，否则"被误删"也会让它绿）、
`TestPlasmaAvailableWith_ReasonsAreActionable`（三段原因各含关键词与"修复"）。
**变异两条全部转红并还原**：`plasmaAvailableWith` 改恒 `true` ⇒ 前者的三个子用例红；
`plasmaAvailableFor` 改恒 `true` ⇒ `TestDelegateMeta_GatedByPlasmaAvailability` 红。
**新驱动器用例 5 条**（假生成器 + 假夹具执行器）：注册+执行+落盘+参数登记、同名不覆盖、
生成失败文案、Link 拒越权工具、5a 红则不注册不落盘。

**行数结算**：根 `AGENTS.md` 仍 **608**（§4 删掉 Meta 行、把 Meta 段换成 plasma 驱动器三段式，
等量压缩补回一行）；`internal/agents/AGENTS.md` 仍 **212**（硬约束 18 去掉 `meta.go` 引用）；
`internal/plasma/AGENTS.md` 仍 **200**；`check_agents_md_size.sh` exit 0。新增
`internal/agents/plasma_meta.go` 241 行、`plasma_gate.go` 154 行、`plasma_defaults.go` 56 行、
`internal/app/plasma.go` 98 行、`plasma_meta_test.go` 345 行 —— 都不在 `check_file_sizes.sh` 的
`internal/agents/director` 范围。
**一处基线因"新增依赖边"上调**：`internal/blueprint` 自扫描 153/153 → **155/155**，来源逐条
`git grep` 对照 HEAD 确认：`internal/app` 首次 import plasma（`app/plasma.go`）、
`internal/agents` 首次 import embedbin（`plasma_gate.go` 让判定与 spawner 读同一个 zipapp 路径）。

**待办（P9b，本阶段明确未做）**：① 加载期 `store → Surface` 接线（`plasma.LoadAll` 与它的
`skipped` 账目进装配）；② 升级批准的用户回路（`escalation_approved` 现在恒 false，所以在线止于
`provisional`）；③ 遥测账本（`plasma/telemetry.go` 已有形状，还没有读者）。
另：`docs/ARCHITECTURE.md` / `ARCHITECTURE.en.md` 里的 `MetaAgent` 类图与表格仍是 P9a 之前的状态，
需在 P10 文档收口时一并改（本轮只同步了根 `AGENTS.md` §4 与本文）。

### P9b 已落地（2026-11-10）

**加载期（§6.5）——`plasma.LoadAll` 上生产。** `internal/agents/plasma_load.go` 是唯一装配点，
在 `NewDirectorAgent` 的 `rebuildSurface()` **之后**调用一次（`registerCustomAgent` 追加到
"当前那一份 Surface"，而 `rebuildSurface` 会把它整份换掉）。三件配套缺一不可：
① `MetaAgentHandler.List/Names` 从插入序改成**字典序**（`meta_handler.go`）—— 插入序只在
单进程内确定，而 `customAgentPromptSection` 拼在 blueprint/skills/mcp **之前**，顺序抖一次
后面全部前缀缓存失效；② `rebuildSurface` 新增 `mountRegisteredAgents()`：每次重建都从
`metaHandler` 把已注册的生成式 Agent 重新挂回去（P9a 期间它们活不过一个 Run 的根因就在这里）；
③ 单项拒载/预算省略/超预算各留一行可见账目，`Skipped` 里带 name + hash + 原因
（`PLS-701` 磁盘被改、`PLS-702` 活注册表复核失败）。
加载期只跑 `Compiler.LoadLink`（阶段 1–3，跳过阶段 4 的 token 计数与结构夹具补齐——后者会改
bundle 字节、进而改 hash）。**实测**：12 个 Agent（预算满编）总计 **1.57 ms**，预算是 §9.2 的
25 ms，因此**不**改首轮惰性加载；`plasmaLoadBudget` 常量与用例
`TestE2E_Plasma_LoadStaysWithinFrameBudget` 一起钉住这条结论。超预算按
`grade → score → name` 截断，被截断的条数写进一行 WARN（不是 Debug）。
提示区新增 `catalog_line_chars`（默认 120，**显式 0 = 不裁剪**）逐行截断 + 一行省略账
（"N of M descriptions truncated … full text: `yagent plasma list`"）。
运行中**不**热替换：`plasmaAgentInfo.Hash` 在加载时固定，版本变更只在下一轮加载（新进程/新 Run）
生效；同一进程内同名 Agent 不覆盖（bundle 不可变，执行器也不能被悄悄换掉）。

**升级批准回路（§6.6 / ADR-4）。** 越权 bundle（`bundle.tools` 里有 `PlasmaPalette` 之外的能力）
照旧在阶段 2 被拒，但 `Compiler.LoadLink` 增加一条**收窄的**容忍：当报告里的 error **全部**是
`PLS-201`（越权）时照样产出计划（`errorsOnlyEscalation`），于是 Agent 能注册、`grade` 钉
`provisional`、越权工具不在 `Tools` 里、`AutoDelegate=false` —— 这解掉了 ADR-4 的死锁
（不注册就没有首次委派，批准永远问不出口）。首次委派由 `ensureEscalationApproved`
（`plasma_load.go`）现场问一次：**复用** `tools.UserConfirmManager`（窄接口 `UserConfirmer`），
回答判据统一为 `tools.IsAffirmativeAnswer`（`user_confirm.go` 与 `WorkspaceGuard` 共用一份词表）。
批准**按 bundle hash** 落 `{name}/{hash}/meta.json` 的 `escalation_approved`（`Store.ApproveEscalation`），
**不写** `bundle.json`（会改 hash）；写入失败即不放行。批准后用批准位重新 `LoadLink` 一次并把
生效工具面换成复核结果 —— 只翻 bool 的话"已批准"是个空话（未批准时越权工具根本没进过 `Tools`）。
`YOLO(-y)` 照旧绕过询问，但**不写** `meta.json`（用户批准与策略放行不是一件事），账本记
`escalated=true`。拒绝批准是一次可读的收尾文案（"需要用户批准写/shell 能力"），不是崩溃。
自动委派判据只有一份：`plasma.CanAutoDelegate(grade, required, approved, minGrade)`
（`internal/plasma/grade.go`），`escalation_required && !approved` 一票否决，
`[plasma] auto_delegate_min_grade` 与 `grade` 比较（空 grade 永远不达标）。

**遥测账本（§6.7 / ADR-6）。** 写入点固定在 `directorToolRunner.Call` 的 `outcome.has` 分支
（`director.go:855-861`）—— meta / custom / builtin 三条路的唯一共同出口；
`applyEnhancedCommander` 只在 builtin 分支跑，选它会漏掉全部 plasma Agent（§2.4）。
执行者身份取新增的 `delegationOutcome.agent`（`executeCustomAgent` 填 `ca.Name`）：
按工具名后缀反推会把 `delegate_meta` 的首次执行记成"meta 跑了 N 步"（meta 没有 LLM 循环），
而评分正是按 agent 名字分组的。一行 JSON 追加到 `~/.yagent/agents/telemetry.jsonl`
（`plasma.AppendTelemetry`，O_APPEND，进程被 kill 只丢最后一行）；字段
`ts / agent / bundle_hash / grade / delegation_id / note_ref / err / stop_reason / steps /
tokens_in / tokens_out / degraded / escalated / fixture_run`。`fixture_run` 由
`PlasmaFixtureRunner` **强制**置真（另加 `Metrics` 出口），调用方不需要记得 —— 需要人记住的
标记迟早在某条新路径上漏掉，而漏掉的表现是夹具样本污染生产评分。写入失败不阻断委派，
但留一条 `slog.Warn`（坑 43：只 Debug 等于没有账目）。

**E2E 四条（`internal/agents/e2e_plasma_test.go`，只 mock 模型与子进程）。** 真 Store、真
trampoline（`/bin/sh` 假 python）、真 `RunAgentLoop`、真 `telemetry.jsonl`：
① 生成 → `--fix` → 注册 → 执行 → 遥测：首轮产出幻觉工具名（`PLS-101`）被拒，
`diagnostics.json` 落盘后驱动器带 `--fix` 重生成（`Attempts==2` 只可能来自这条路径），
注册后**当场可委派**，账本里 `steps/tokens_in/stop_reason` 三个字段全非零（§1.3 第 9 条）
且 `bundle_hash` 非空、`fixture_run=false`；
② 越权 → 升级门 → 按 hash 批准 → 新 hash 失效：`report.json` 里有 `PLS-210`、
grade 钉 `provisional`、越权工具不在工具面、未批准不可自动委派；无确认通道 / 用户拒绝都是
可读收尾，同意后 `meta.json` 的 `escalation_approved=true` 且工具面补回 `run_bash`
（档位不变）；**新实例**加载新 hash 时批准不继承（旧 hash 的批准记录仍在，只是不适用）；
③ 改一个字节（保持 JSON 合法）⇒ `LoadAll` 返回 `PLS-701` 拒载该 Agent，另一个照常加载，
被篡改的 `delegate_*` 不出现在 `Surface.Names()`；
④ 加载期预算：12 个 Agent 满编 < `plasmaLoadBudget`。

**变异三条全部转红并还原**（`/tmp` 副本，还原后回绿）：① 遥测 `steps` 恒写 0 ⇒ 链路①红
（"账本 steps = 0"）；② 批准改成"按 name 记"（`LoadAll` 扫该 name 任一 hash 的批准）⇒
链路②的新 hash 断言红；③ `Store.Load` 去掉 hash 重算 ⇒ 链路③红（`loaded/skipped = 2/0`）。

**7xx 码族不进 `diagCatalog`。** 那张表是发给 Python 侧的只读码表，而 Python 从不读 store；
`TestDiagCatalogMatchesCorpus` 要求"码表里的每个码都被 corpus 发射过"，而 corpus 走
`Elaborate→Compile→VerifyStructural`，`Load` 不在那条路上。于是 `PLS-701/702` 以常量 +
可读文本落在 Load 层（`diag.go` 的注释写清了理由），账目照样可 grep、可断言，
`schemas.json` 与 50 条基线零改动。

**行数结算**：根 `AGENTS.md` 仍 **608**；`internal/agents/AGENTS.md` 仍 **212**（新增硬约束 20
"遥测账本只有一个写入点"，等量压缩 7 处措辞补回）；`internal/plasma/AGENTS.md` 仍 **200**。
`check_all.sh` exit 0；`git diff --stat internal/agents/testdata/tool_surfaces.golden
internal/toolspec/schemas.json` **零输出**（`delegate_meta` Spec 一个字节都没动）。

**文档收口（P9a 待办第 2 条）**：`docs/ARCHITECTURE.md` / `docs/ARCHITECTURE.en.md` 里
`MetaAgent` 的类图节点、`DirectorAgent` 字段、子 Agent 表、"输出格式"段、接线顺序与文件表
全部改成 plasma 现状（`MetaAgentHandler` 保留，`MetaAgent` / `MetaAgentResult` /
`ParseMetaAgentOutput` 标注为已删除）。

**待办（P10）**：① Python 包 `plasma/py/` + `plasma.pyz` 打包与 `embedbin` 落地（当前
`plasmaAvailable()` 恒降级属预期）；② 进化器的 store 读写与 `store_ttl` GC 接线；
③ 5b 行为夹具的评分侧（只统计 `fixture_run=false` 的记录）与 `promote_min_runs` 棘轮；
④ `yagent plasma list` 的统计栏接 telemetry 聚合。
