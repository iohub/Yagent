# Yagent Agent 开发知识库

> **本文件是 Agent 理解仓库和进行开发的标准指南。** 只收两类句子：**会改变跨模块决策的判据**，
> 和**告诉读者去哪儿查的指针**。包内"这么做会静默坏在哪里"写在 `internal/**/AGENTS.md`（17 份，
> 导览在 `internal/AGENTS.md`），模块结构与文件计数由 `internal/blueprint` 每轮派生——
> 抄进任何文档就是第二份会漂走的事实。
> **编号是契约**：`§5.x`、`§6.x`、`§8.3`、`§9.3` 与 `§11` 的 46 条坑编号被 13 份嵌套文档共 25 处
> 指回，**重排或小节改号会让那些指针全部失效**。要瘦身就在小节内部删，不要动编号。
>
> 当前状态（核对 2026-10-24）：蓝图 R1–R6、结构化并发 C1–C6、停止/继续三层 A+B+C、配置连根拔 +
> 引导向导、MCP+技能接入 均已落地。**仍待办**：`[blueprint] export_path` 没有消费者（R8 导出层与
> `/blueprint` 入口未做，别写进 `docs/config-guide.md`）。`docs/` 下只有 `config-guide.md`、
> `skills-and-mcp-*` 与本文件的设计稿是活的，`ARCHITECTURE*.md` 已知过期；旧设计稿大多已删除，
> **引用前先确认文件存在**。文件末尾由 codeseek 自动维护的注入区块除外。

---

## 目录

1 概述 · 2 技术栈 · 3 知识分层与加载 · 4 Agent 角色 · 5 工具与执行面 · 6 系统架构 ·
7 配置 · 8 开发与构建 · 9 规范与不变量 · 10 Director 行为准则 · 11 坑索引

---

## 1. 项目概述

**Yagent** 是 Go 编写的多智能体 AI 编程助手，**Hub-and-Spoke** 架构：**Director 是唯一与用户
交互的 Agent**，负责分析、委派、评审；读写由专用子 Agent 完成。接口三套（TUI / HTTP+WS /
`--prompt`），代码引擎是 `codeseek`（Rust，git submodule，经 MCP stdio 调用，二进制 `go:embed` 分发）。

五条设计原则：**单一交互点**、**职责分离**（每个子 Agent 有明确权限：只读 / 可写 / 仅 Shell）、
**强制委派**（改文件必走 Coding，探索仓库必走 Repo，Director 自己只用只读工具）、
**验证优先**（用 `go build`/测试验证，不起长驻进程）、**可追溯与可恢复**（任务记忆 JSONL +
同步 rollout 双写，`/resume` 断点续跑）。三档上下文压缩与 Pub-Sub 事件总线见 §6。

## 2. 技术栈

### 2.1 依赖

Go **1.25.8**，模块名 `yagent`，导入路径 `yagent/internal/...`（已无 `pkg/`）。
`spf13/cobra` · `openai/openai-go/v3` + 自研 Anthropic Messages 引擎 · `gin` + `olahol/melody` ·
`charm.land/bubbletea/v2`（**v2**）、`bubbles`、`lipgloss`、`glamour` · `pkoukk/tiktoken-go`
（cl100k_base，`internal/tokenutil`）· `BurntSushi/toml` · `aymanbagabas/go-udiff` ·
`google/uuid`、`go-colorful`、`uniseg`。**`go-rod` 与 `fsnotify` 只在 `go.mod` 里活着，Go 代码零引用**
（前者是 Browser-Agent 重构的候选依赖，后者意味着词典与配置都没有热加载）。

### 2.2 外部依赖

`ripgrep`（**git 树里没有**，`dist/bin` 只跟踪占位文件，运行时回落系统 PATH）、`fzf`、`codeseek`、
`browser-control`（后两者随 release 构建内嵌，启动时解到 `~/.yagent/bin`）。`browser-control` 与
Chrome 缺一则 Browser-Agent 不可用，`delegate_browser` 的描述会自动降级并说明修复方式。
查找顺序与全部浏览器约束见 `internal/browserctl/AGENTS.md`。

### 2.3 LLM 接入

两种 `api_format`（openai / anthropic），provider 只是 base_url + api_key 的组合，**没有 Bedrock
专用客户端**（`api_base_url` + `api_key` 对每个 provider 一视同仁必填）；密钥可来自 `api_key_env`，
在 `validate()` 里一次性展开进同一个字段。`reasoning_effort` 在两侧是两件事（anthropic 映射
`thinking.budget_tokens`，认不出的取值一律当作没开思考）。provider 的 `max_tokens`/`temperature`
是**引擎级默认**，优先级 `单次 CallOptions > provider 配置 > 不发这个字段`，且 `0` 一律按"没配"处理。
逐条判据在 `internal/llm/AGENTS.md` 硬约束 1-3 / 9-10 与 `internal/config/AGENTS.md` 硬约束 10。

## 3. 知识分层与加载

| 层 | 内容 | 谁读它 |
|----|------|--------|
| 根 `AGENTS.md`（本文件） | 跨模块决策面 + 路由 + 坑索引 | yagent 运行时注入每次 Director 会话；外部 agent |
| `internal/**/AGENTS.md`（17 份） | 包内不可派生约束：边界 / 硬约束 N / 入口 / 闸门 | 外部 agent；yagent 侧随委派注入（§3.2） |
| `internal/blueprint` | 模块集合、文件计数、依赖边、入口与 manifest | 运行时每轮派生；`read_blueprint` |
| `docs/` | 只有 `config-guide.md` 与 `skills-and-mcp-*` 是活的 | 人 |

### 3.1 嵌套文档的写法约定

进任何目录动手之前先看它有没有 `AGENTS.md`；**没有局部文档 = 这个目录没有不可派生的知识**，直接读代码。
"哪些目录有局部文档"的表格只在 `internal/AGENTS.md:8-28` 一处（根不复制）。局部文档只写机器派生不出的：
边界（尤其**不负责**什么）、硬约束、历史坑、入口文件、钉住不变量的**测试函数名**；每条带
`internal/<包>/<文件>.go:行`，让读者能跳过去核实。**同一句话不要同时写在两处。**

### 3.2 加载契约

`internal/agents/director/project_context.go:42-72` 在工作区根依次试 `YAGENT.md`、`CLAUDE.md`、
`AGENTS.md`，**读到的全部逐字拼接**（不是"第一个命中就停"）后按会话缓存一次，**没有任何字节上限**。
这就是本文件必须保持精简的全部理由：这里的每一段散文都是每次委派前的固定 token 成本。

`internal/agents/module_docs.go` 补上另一半：每次委派前缀带上**任务点到的目录及其祖先**的
`AGENTS.md`（判据与 `ProseForTask` 同源——分母是磁盘上真实存在的文档，不猜路径）。最深优先、
最多 3 份 / 合计 12 KB，超预算在末尾写一行可见的省略账；工作区根那一份**永不重复注入**。
接入点 `director.go:packDelegateTask`，在蓝图散文之后、与 `YAGENT_BLACKBOARD` 无关。
**没有配置键、没有环境变量急停**：它的坏表现形态是"多花一点 token"，而多一个 kill-switch 就是多一处
"三处共用判据"要维护（§11 坑 13 那一类）。

## 4. Agent 角色定义

所有 Agent 实现 `Agent` 接口（`internal/agents/types.go:39`），由 `internal/app/app.go` 统一构造，
System Prompt 是同目录的 `<name>.prompt.md`——**改行为要同时改 prompt 与代码**。

| Agent | 定位 | 权限 | MaxSteps 默认 |
|-------|------|------|----------------|
| **Director** | 项目经理/技术主管，唯一对话入口 | 只读 + 委派 | 100 |
| **Coding** | 高级开发工程师 | 完整读写 + Shell | 150 |
| **Repo** | 架构师/审计员 | 只读 + 可委派 DevOps | 50 |
| **Chat** | 通用问答 | 无文件系统访问 | 50 |
| **DevOps** | 系统管理员 | 只读 + Shell | 50 |
| **Browser** | 经外部 `browser-control` 驱动真实 Chrome | 浏览器动作 + 会话工作区产物 | 200 |
| **Meta** | 自定义 Agent 设计器 | 无工具，单次 LLM 出 JSON | —（重试 5） |
| **Custom** | Meta 设计后运行时注册 | 由 LLM 选的工具子集 | 15 |

**"谁能用什么"只有一份**：能力画像 `internal/toolspec/profiles.go` + 装配点
`internal/agents/director_surface.go`，基线是 `internal/agents/testdata/tool_surfaces.golden`。
本文件**不列工具清单**（那是 golden 的副本）；Director 的 `Deny: EffectFSWrite | EffectShell` 是
"Director 绝不直接写文件"的机械保证。改工具面必须以显式 `-update` 重写 golden 的方式被评审。

Meta / 自定义 Agent：`delegate_meta` → 单次 LLM 出 JSON → 校验必填 → 按名字建
`delegate_<snake_name>` 追加进 Director 工具表（子集从 `ProfileDirector.Palette` 选）→
立即以 `MaxSteps=15` 执行一次 → 本任务内永久可用。落点在 `internal/agents/director/meta_handler.go`
与 `director_surface.go`，装配层不许复刻。

## 5. 工具与执行面

### 5.1 Adapter 模式与并发

`internal/tools/adapter.go`：`Adapter.Call()` 的顺序是 JSON 解析 → 工作区边界检查 → 危险操作授权 →
执行；`ToToolDef()` 产出 `llm.ToolDef`，声明上 provider 前**必须按名字排序**（顺序漂走的症状不是
崩溃，是 provider 端前缀缓存逐条失效）。

**两道独立的并发闸门，判据各只有一个函数**：

1. **只读工具并发**：`Adapter.IsReadOnly()`（由 `Spec` 的 `!Mutates && !Serial` 派生）为真、且不在
   `toolspec.IsSerial` 名单里 → 由 `toolbatch` 分批并行（默认 4，上限 8）。冷工具（schema 还没取回）
   也算可并行——它在这一批里只收到 teach 回执、不执行副作用。
2. **委派并发**：`delegate_*` 在子 Agent 侧一律 `Serial`；Director 侧只读委派可并发，跑几个由
   `[agent] max_parallel_delegations` 决定，而**池子建在这一轮 Run 的 toolRunner 上**
   （`newDirectorToolRunner` 是唯一构造方式），不是进程级——嵌套委派用的是自己那一轮的池，
   "父占一格、子在同一格排队"的死锁形状从结构上不存在。

两个循环共用同一个判据 `agents.toolCallIsParallelizable`。**新增可并发工具必须进
`signedReadOnlyAdditions` 或 `newTools` 集合并写明理由**（`TestDerivedMetadataMatchesLegacyTables`）；
并发准入测试必须拿**真实画像**问判据——自己拼一个工具列表来测等于重犯 §11 坑 33。

### 5.2 声明面

`internal/toolspec/schemas.json` 当前 **50 条**，改动必须显式改基线数字
（`registry_test.go:TestSchemasLoadAndDescribeEveryEntry`）。每个工具的完整契约 = schemas.json 的
description/parameters + `Spec` 的能力标签；并发/授权/截断/展示一律**从能力标签派生**，
不再往任何名字表加条目。**不存在**名为 `flow_control` 的工具（流控制由 `agent_exit` 与
`ask_user_for_help` 承担）。

### 5.3 MCP、外部工具与技能

外部工具**不进全局注册表**（`toolspec` 仍是 `init()` 期写入的只读事实）：由
`tools.BuildExternalSpecs` 在运行期构造 Spec，经 `Deps.ExternalSpecs` 进 `Surface.rebuild()`，
名字 `mcp__<server>__<tool>`，一律 Cold（走既有 deferred 目录 + `load_tool`）；
**不计入** §5.2 的基线。副作用判定的安全侧默认：**没有 annotations 一律按写处理**
（`Mutates`+`Serial`+`NeedsAuth`），写类默认不注册，除非 `allow_write = true`。
codeseek 是第一个内建 server，6 个手写桥经 curated 名单保留、不重复注册。
**启动不等 MCP**：`Manager.Start` 只同步 spawn 子进程，`Client("codeseek")` 当场可取。
技能：目录进 system prompt 的稳定前缀，正文由 `use_skill` 按需取回；合并语义、账目与
"`allowed-tools` 只是咨询不强制"见 `internal/AGENTS.md` 的 skills 一节。

### 5.4 安全机制

| 机制 | 实际行为 |
|------|----------|
| WorkspaceGuard | 危险工具（create/edit/delete/rename/run_bash）路径必须落在工作区内；只有"问用户"这一种强制，**没有硬拒绝路径**；放行状态三层（会话 / 项目级 `~/.yagent/settings.json` / YOLO） |
| UserConfirmManager | 走事件总线：发 `user_help_needed` 等 `user_help_response`，按 `request_id` 配对；这两个事件类型在 critical 旁路队列，永不丢弃 |
| `run_bash` 纵深防御 | **无黑名单**；信任 LLM 的 `is_dangerous` + 任何指向工作区外的绝对路径 token 都要授权（`safeSystemPaths` 例外，根目录恒危险） |
| 大小限制 | 编辑双向 10MB；整读 10MB、软警告 2MB、单次 1000 行（数值口径住 `internal/tools`） |
| YOLO | `-y` = **一切授权全绕过**（含工作区外路径）；`-Y` 隐含 `-y`，额外只做两件事：把 `ask_user_for_help` 摘出工具面 + 注入自主决策指令 |
| 分层守卫 | `director/` 子包禁止 import `internal/agents`；子 Agent 依赖经 `agents/ports.go` 窄接口注入 |

## 6. 系统架构

### 6.1 消息流

```
用户 → TUI / HTTP / --prompt → Yagent(app) → DirectorAgent(planner)
                                              ├ delegate_repo  → RepoAgent   ┐
                                              ├ delegate_coding→ CodingAgent  ├ 各自跑 RunAgentLoop
                                              ├ delegate_browser→ BrowserAgent│  共享同一事件总线
                                              ├ delegate_chat/devops/meta    ┘
                                              └ agent_exit
```

### 6.2 执行循环与"停止 / 继续"三层

- **子 Agent**：`RunAgentLoop(ctx, ExecutorConfig)`（`internal/agents/executor.go`）。每步：LLM 重试
  （指数退避 ≤30s）→ 三档压缩检查 → 带 `llmTimeout` 的调用 → 抽 `<thought>` → 纯文本回复则注入提醒或
  结束 → 工具调用交给 `toolbatch` 分批并行 → `agent_exit` 立即返回；步数耗尽报错，panic 被 recover 并写
  `turn_aborted`。**`Surface` 是唯一工具来源**（`Adapters` 已删）。`ExecutorConfig` 的按-Agent 标志
  （`StopOnFinish` / `StepRetries` / `NoActionRetryLimit` / `MaxParallelReadOnlyTools`）**没有中心默认**，
  漏一个就是那条路径静默少一套行为（§11 坑 16）。每次委派都经 `SpawnForDelegation` 新建实例。
- **Director**：自己的 `director.Planner`（`director/planner_run.go`），额外有熔断器、未委派提醒（配额 3，
  耗尽后以 `plain_text` 收尾而不是报错）、`activity.WithIdleTimeout` 包裹的委派超时。

**A 活暂停**（`internal/pause`）：两个循环的 step 边界各有一行 `pause.From(ctx).Wait(ctx)`；闸门挂在任务
ctx 上（`http.NewTaskContext` 建、`Task.Gate()` 读回），嵌套委派自动继承。**闸门不取消 ctx**，所以恢复是
"从阻塞点原地继续"而不是"一次失败的委派"；暂停期间 `activity` 的 idle **和** total 都必须显式冻结。

**B 检查点重放**：两个循环各自只在**终止路径**上落一条 `agent_checkpoint`（不是每步一条），坐标里只存
派生不出来的东西（step 游标、指纹、模型/档位、工作区事实、`delegation_id`）——消息正文、已取回的冷工具、
T&P 块**刻意不存**，因为存了就会与代码漂移。`memory.WithDelegationID` 把那次 `delegate_*` 的
`tool_call_id` 挂到委派 ctx，子 rollout 文件名带 `_{agent}` 后缀，父子就此串起来；
`ReadRolloutForReplay` 重建后经 `WithResumeState(ctx, …)` 交回 `RunAgentLoop`。**恢复不经过模型**：
`Director.Run` 在 `planner.Run` **之前**处理 `PendingResumption`，结果作为同一个 `call_id` 的第二条
tool output（last-wins）交回，零 token、确定性。

**C 降级交接报告**：`assessReplay` 的判据全部"不需要真跑一次就能判"（判据清单见
`internal/agents/AGENTS.md` 硬约束 13），任一命中就改用**程序生成的**报告交回 Director 并写一张
`status=degraded` 的便签。**降级不是 error**——写成 error 的后果是 Director 把已做完的工作重做一遍。

### 6.3 路由（auto-routing）

`internal/llm/router.go` 的 **RoutingEngine 包裹每个 Agent 的引擎**，按任务难度路由到
fast / balanced / high；`Classify` 用 fast 模型输出 `{"tier":…}`（30s 上限，失败回落该角色 baseline）。
`[routing] mode = "auto" | "fixed"`。**优先级：TUI 运行时覆盖 > 路由档位 > `[agents.llm.*].use_provider`。**
运行中换模型不需要任务空闲（`Alt+M` / `:model`），代价是几个引擎指针会被并发替换，读写必须走各自的
getter/setter。**任何重建 Agent 引擎的地方都必须保留 `*RoutingEngine` 包装**，否则 auto-routing 静默失效
（闸门 `internal/app/assembly_test.go:TestAgentEngineAlwaysWrapsWithRoutingEngine`）。

### 6.4 上下文与知识

四条通道各管一件事，**互不替代**；每条的包内判据在对应 `AGENTS.md`：

- **Blackboard 便签**：每次委派 = 一张便签，**正文是交付物全文**，front-matter 是 `Handoff`。索引是
  **派生态且瞬态**（每轮重扫磁盘，只进这一次 LLM 调用的 messages，绝不进 `ConversationMemory`/rollout
  ——这是它扛住压缩的唯一原因）。board 作用域 = session；`context_refs` 是**强调**不是准入；`status` 词表
  刻意没有 `paused`。索引成本 = `index_max_notes × index_line_chars`，**每轮**注入。→ `internal/blackboard`
- **Artifact**：超预算（默认 500 token）的委派全文落盘，上下文只留摘要 + ID，`read_artifact` 分页取回。→ 导览 artifact 节
- **Blueprint**：**唯一的仓库级持久知识 sink**（KV 仓库记忆与知识注入已于 2026-10-22 整体删除）。
  **派生的不落盘，落盘的不派生**（模块级边由 `go/parser` 验过才写进派生层，所以模型没有任何途径写坏它）；持久层 `~/.yagent/data/blueprint/{projectID}/blueprint.md` + `revisions/`。
  新鲜度三判据全部机械（结构指纹 → 标 stale 但**保留散文**；锚点三态；孤儿 90 天老化），**stale 是粘滞的**。
  读通道 `read_blueprint`；写通道是 `agent_exit` 的 `blueprint[]`，落盘只发生在 **Repo 完成接缝**；
  fold 的账目经 `AgentResult.BlueprintAccount` **两处出口、不进 conclusion**。→ `internal/blueprint`
- **Skills / Thinklink**：→ 导览的 skills 与 thinklink 两节。

### 6.5 三档上下文压缩（`internal/compression`）

**阈值** = `min(模型目录 limit.context × 0.75, [enhanced_commander] context_compression_threshold)`，
两个来源都是上限；都没值才是 120000。**provider 的 `context_window` 不参与**（只影响 TUI 进度条）。
判定口径 = 上一步 provider 报的 `Usage.TotalInputTokens` 打底 + 此后新增消息的本地估算；计量缺失、还没跑过
一步、或列表被第二/三档整段重写时必须作废。判定只有一处实现 `compression.OverThreshold`，两个循环各两处
调用点共四处——**不要再抄 `EstimateMessagesTokens(messages) > threshold`**。总开关只有
`enable_context_compression` 一个键；第三档还要 `enable_ultimate_compression` **与**非空 thinklink store。

1. **Rule**：按优先级截低价值工具结果（`deepthinking`/`design` 受保护），保留最近 `tool_result_keep_tokens`。
2. **Emergency**：T&P 块抽进 thinklink，最近 3 条保留原文，其余 LLM 摘要，记忆重写为 `[system, one user]`。
3. **Ultimate**：用 thinklink 日志重建（`ultimate_compression_keep_plans`，0 = 全部），最后硬截断。

三档都发 `context_*_compressed` 事件并写 `compacted` rollout 记录（`compacted` 会替换被压过的前缀，
这正是"指纹由读侧算"要处理的东西）。

### 6.6 事件总线（`internal/messaging`）

`MessageDispatcher` 每 consumer 一条队列（容量只有一处定义 `NewDefaultMessageDispatcher()`）；普通事件
满载即丢并限流告警，`user_help_needed`/`user_help_response` 走 critical 旁路（永不丢弃，满则丢最旧）。
丢弃必须**可被知道**：`Dropped()` + 收尾 `WarnDropped(scope)`。

**并行必需的两个坐标**：`Seq` 由 dispatcher 在 `Publish` 里盖；`DelegationID` 由 `RunAgentLoop` 入口
**一次**绑定（`Publisher.ForDelegation`），不是在十个发布点各传一遍——漏一处不报错，只留下一句没人能
解释的现象。**`Source` 在并行委派下不再唯一**。现状：生产消费者全是旧版广播，拿到的是只搬 5 个字段的
裁剪副本，因此这两个坐标**到不了 HTTP/TUI 的投递端**（缺口与两种修法见 `internal/messaging` 硬约束 10）。

总线上的类型（SSOT 名义上是 `protocol/agent-events.yaml`，**它已落后于代码**——`model_routed`、
`context_*_compressed`、`thinklink_entry` 都没登记，所以清单以代码为准）：
`ai_stream_start`/`ai_chunk`/`ai_stream_end`/`ai_response`、`thinking`、`llm_call_start`/`llm_call_end`/
`model_info`/`model_routed`、`tool_call_start`/`tool_call_result`、`user_help_needed`/`user_help_response`、
`context_loaded`/`commit_context_loaded`/`context_compressed`/`context_emergency_compressed`/
`context_ultimate_compressed`、`thinklink_entry`、`conversation_error`/`conversation_result`、
`task_paused`/`task_resumed`、`sub_agent_activity`。**生成物不能整体重跑**：
`internal/protocol/agent_events.go` 头部写着 DO NOT EDIT，却含生成器不产出的手写类型与投递形状，重跑会
打断授权对话框——可行做法见 `internal/AGENTS.md` 的 protocol 一节。

### 6.7 任务生命周期与持久化

```
main.go → Yagent.Init(engine, repoPath) → 装配 MCP/工具引擎/守卫/子 Agent/Director（sync.Once）
        → TUI 建 Task(uuid + ConversationMemory)，每轮新建 Dispatcher + tuiEventConsumer
        → NewTaskRequest(...).WithProjectDir/WithUserMessage/WithMemory/WithResumed
        → ProcessCodingTaskWithCallback / ProcessConversation → Director.Run（多轮复用同一 Memory）
```

| 数据 | 路径 | 写入方式 |
|------|------|----------|
| 任务记忆 | `~/.yagent/tasks/{taskID}.jsonl` | 5s 防抖（**不是** /resume 的事实来源） |
| **Rollout**（断点续跑的事实来源） | `~/.yagent/data/rollout/{projectID}/{ts}_{session}[_{agent}].jsonl` | 入队即返回、攒批落盘（32 条 / 20ms，`Close` 会 join）；类型 `session_meta/turn_context/response_item/event_msg/compacted/agent_checkpoint`；`session_meta` 带 git 身份 + `delegation_id` + `parent_session_id`，终止事件带 `stop_reason`（词表 `memory/rollout_types.go`：`paused`/`cancelled`/`idle_timeout`/`total_timeout`/`max_steps`/`llm_error`/`circuit_breaker_open`/`panic`/`plain_text`/`agent_exit`/`unaccounted`）。**读回自己写过的文件之前必须排干** |
| Blackboard 便签 | `~/.yagent/notes/{boardID}/{agent}/*.md` | 原子写，`YAGENT_NOTES_ROOT` 可覆盖 |
| Artifact | `~/.yagent/data/artifacts/{id}` | 原子写，ID `^\d{8}-\d{6}-[0-9a-f]{8}$` |
| **仓库蓝图** | `~/.yagent/data/blueprint/{projectID}/blueprint.md` + `revisions/*.md` | fold 前整档进 `revisions/`（滚动 5）；`util.AtomicWrite`；多进程乐观 CAS 重放；根护栏拒 `$HOME`/`/`/无 manifest 且非 git |
| 授权状态 | `~/.yagent/settings.json` → `authorized_projects` | 持久 |
| 浏览器工作区 | `~/.yagent/data/browser/{delegation}/.browser-control/` | 由 `cmd.Dir` 决定落点；`workspace_ttl` 清理，**没有后台任务会删** |
| 日志 | `~/.yagent/logs/`（`app-`/`llm-`/`llm-error-`/`tool-`/`delegate-`） | 追加 |

**`/resume` 两层落点，共用同一份账本**：**会话级**（重建 Memory + 恢复 thinklink + 下一轮 `resumed=true`
强制重注入项目上下文；配对按 last-wins 修复，与 LLM 侧 `repairToolCallPairs` 同语义；
`ScanResumeCandidates`（在 `internal/agents/resume.go`，不在 memory）**默认拒绝**，只有 `paused` 与
`cancelled` 可重放）；**委派级**（`app.ResumableDelegation` 反问"这次会话是不是停在一次 `delegate_*` 上"，
能才给恢复对话框第三个选项；TUI 只交出 rollout 路径，落点由 app 侧算）。

### 6.8 TUI

Bubble Tea **v2**（`internal/tui`）。渲染与输入契约全部在 `internal/tui/AGENTS.md`：一次性事件监听 cmd
（**没有重放队列**）、弹窗守卫白名单（`taskCompleteMsg` 必须透传）、布局高度契约（侧栏四段抢同一份
`height-2`，让位序 token > Activity > ⧗≈⧉）、token 面板 / ⧉ 压缩 / ⧗ 耗时的单一渲染器、列表/表单共用原语、键位是**替换**语义、引导向导既能独立跑也能嵌进主 TUI。
**测试约定**：断言精确单元格宽度而非子串；宽度矩阵 {30..240} × 终端尺寸 {20..240}。

### 6.9 HTTP

`POST /api/{start_task,load_task,cancel_task,pause_task,continue_task}`、`GET /api/task_status`、
`GET|DELETE /api/memory`、`GET /api/memory/:type`、`GET /api/history`、`GET /ws`。任务状态四档之外
**另有 `paused`**（闸门不产生 task_complete）。WS 用 melody，每任务一个 Dispatcher + `WebSocketConsumer`，
向所有连接广播 `realtime` envelope（**无按会话过滤**）。`Task.Gate()` 只从 ctx 读回、不在结构体里另存一份；
锁不跨 IO，见 `internal/http/AGENTS.md`。

## 7. 配置系统

**解析顺序**：`--config` 指定路径（原样使用，**绝不自动创建**）→ `~/.yagent/config/config.toml`
（不存在则用内嵌模板生成，0600）。`./config/config.toml` **不是第二级查找**（只有 `UserHomeDir()`
失败才走 CWD 相对路径），仓库里那份是注释参考。`EnsureConfigExists` 返回的 `created` 是首启判据之一。

**三级 Provider 覆盖**：`ResolveProvider(agent, tool)` = 工具指定 → `tools.llm.use_provider` →
Agent 指定 → `agents.llm.use_provider` → `global.llm.use_provider`。Agent 名七个：
`director / coding / repo / chat / devops / browser / meta`。

**运行时写回只有一个内核**：`config/persist.go` 的 `SetKeyInSection`（文本级、保注释、**永不整文件
重编码**——那会蒸发用户注释），`Persist*` 是薄封装；provider 块写/删在 `config/setup.go`，
**被任何 `use_provider` / `fallback_providers` 引用的 provider 拒绝删除**（留下一份"加载即失败"的
文件比拒绝更糟）。

**首次使用引导**：`yagent setup` 子命令（不装配 Agent/MCP，配置坏了也能跑）、TUI `/setup`
（任务运行中或弹窗打开时拒绝；**写完要重启才对新引擎生效**）、`main.go` 在 `config.NeedsSetup` 判出
`SetupFreshInstall / SetupPlaceholderKey / SetupBroken` 时自动进入。判据全部从配置本身推导，
**没有"看过一次就不再问"的标记文件**。配置坏了不再静默 `os.Exit(1)`，而是 stderr 三行人话 + 退出码 2。
界面契约在 `internal/tui/AGENTS.md`，写回与失效键点名在 `internal/config/AGENTS.md`。

```toml
[global.llm]
use_provider = "bailian"
[global.llm.providers.bailian]
model = "qwen3-coder"           # 必填；生效 provider 的 api_base_url + api_key 一视同仁必填
api_base_url = "https://..."
api_key_env = "BAILIAN_KEY"     # 或走环境变量；变量没设 → 生效 provider 启动即失败并点名
context_window = 262144         # 只影响 TUI 进度条；压缩阈值走模型目录（§11 坑 3）
temperature = 0.3               # 0 一律按"没配"处理
max_tokens = 28000
reasoning_effort = "high"       # openai 透传(+thinking)；anthropic 映射 1024/8192/24576/32000
api_format = "anthropic"        # 默认 openai
fallback_providers = ["deepseek", { provider = "mimo", weight = 2 }]

[agents.llm]  coding.use_provider = "deepseek"
[tools.llm]   micro_agent.use_provider = "local"    # 另有 thinking / deepthinking / design

[agent]
director_max_steps = 100  coding_max_steps = 150  repo_max_steps = 50
chat_max_steps = 50  devops_max_steps = 50  browser_max_steps = 200
delegate_idle_timeout = "60s"  delegate_total_timeout = "20m"
max_parallel_readonly_tools = 4   # 1 = 严格串行，<=0 = 默认 4，>8 钳到 8
max_parallel_delegations = 2      # 同一套取值约定（1 = 串行）
lang = "Chinese"                  # LLM 写字段用的语言，与 TUI 界面语言无关

[llm]  timeout / max_retries(5) / step_retries / enable_fallback / fallback_max_retries(2)
  max_in_flight_requests = 8     # 进程级在飞闸门；**这里的 0 有语义（不限流）**
  circuit_breaker_threshold = 0  # 0 = 不启用（模板与 defaults 都是 0）；>0 才建熔断器

[routing] mode = "auto"   [routing.baselines]  coding = "high"  repo = "balanced"
[enhanced_commander] enable_context_compression   # 唯一压缩开关
  context_compression_threshold / tool_result_keep_tokens(200) / enable_ultimate_compression /
  ultimate_compression_keep_plans

[blackboard]
index_max_notes = 12       # **0 = 关掉索引注入**；负数/没写 = 默认
index_line_chars = 160     # **显式 0 = 不裁剪行**；负数/没写 = 默认
ttl = "168h"               # **"0" 或 0 = 不清理**
summary_max_runes = 1200   # <=0 = 默认（没有"关"的语义）
max_notes_per_board = 200  # 0/负数 = 默认
# 不设 enabled 键：整体关闭是环境变量 YAGENT_BLACKBOARD=0

[resume]
enable = true                    # false = 不写检查点、不重放（只能从 Director 继续）
replay_token_budget = 0          # 0 = 用模型目录阈值；超预算**降级成交接报告**而不是报错
max_replay_messages = 2000       # **显式 0 = 不限**；负数/没写 = 默认
degrade_on_workspace_change = "warn"   # warn | block；认不出的值必 warn 后回落
checkpoint_workspace = true      # 超大仓库可关
# 整体退出是 YAGENT_RESUME=0；关掉是"连写都不写"，留半套开关会制造新的无人能答的问题

[blueprint]   enable / inject_into / index_max_tokens / prose_budget_tokens / export_path
[browser] [git_checkpoint] [mcp] [[mcp.servers]] [skills] [keywords] [tui.keybindings]
            # 完整键表与默认值在 docs/config-guide.md
```

> **`[blueprint] export_path` 至今没有消费者**（R8 未做）。其余键各有真实消费点；默认值与
> `internal/blueprint` 的渲染兜底常数由 `internal/config/blueprint_defaults_test.go` 钉成同源。
> **两套取值约定别混**：并发旋钮用 1 = 串行、≤0 = 默认；而 `max_in_flight_requests`、
> `[resume] max_replay_messages`、`[blackboard]` 三个阀门的 **0 本身有语义**，所以必须靠 **TOML 键的
> 存在性**（`md.IsDefined` / `sectionHasDecodedKey`）区分"没写"与"写了 0"（§11 坑 26）。
> 环境变量急停：`YAGENT_CONCURRENCY=0`、`YAGENT_BLUEPRINT=0`、`YAGENT_BLACKBOARD=0`、`YAGENT_RESUME=0`、
> `YAGENT_MCP=0`、`YAGENT_SKILLS=0`、`YAGENT_ARTIFACT_DISABLE=1`、`YAGENT_NOTES_ROOT`、
> `YAGENT_COMPRESS_THRESHOLD`（只给调试：压过目录与配置，生产别用）。判据各只有一个函数，
> 且写在 `Normalize*` **内部**而不是调用点。**已删除的键**残留时由 `removedKeyHints` 启动逐条点名并
> 给替代写法（登记表与测试夹具互为镜像）。

## 8. 开发与构建

### 8.1 构建

```bash
./build.sh                    # Rust(codeseek release) + Go 一体 → dist/bin/codeseek 与 ./yagent
SKIP_RUST=true ./build.sh     # 只构建 Go（复用已存在的 dist/bin）；BUILD_TYPE=debug；./build.sh clean|release|help
go build -o yagent .          # 快速验证 Go 侧（dist/bin 已有文件时）
```

### 8.2 运行

```bash
./yagent                      # 默认 TUI；./yagent -p "问题"（非交互单轮）；./yagent http --port 9080（--port 0 = 系统分配）
# 通用 flag: -d/--disable-agents  -c/--config  -y/--yolo  -Y/--full-yolo  -Q/--force-quit
```

工作目录即项目根（`os.Getwd()`），没有 `--cwd` 类参数。版本号经 ldflags 注入
`internal/globalctx.{Version,BuildTime,GitCommit}`——**这三个字段不存在**，注入的是空气（见导览的
globalctx 一节）。**没有 CI 质量闸门**：`release.yml` 只做跨平台构建 + 发布，不跑测试/lint。

### 8.3 测试

```bash
bash scripts/check_all.sh                 # 本地闸门：build + vet + 十一把守卫 + 全量单测
RACE=true bash scripts/check_all.sh       # 再对并发包跑 -race（慢 3-8 倍，改到并发时手动跑）
bash scripts/check_all.sh --fast          # 只跑 ./internal/... 的单测
go test ./... -count=1 -timeout 600s      # 26 个测试包，墙钟约 12s
go test ./internal/agents/... -count=1    # 最慢的一档，约 10.5s
```

**测试纪律（比测试数量重要）**：

- **不要写"永远不会失败"的测试**：断言不能在异步完成之前；夹具必须真的触发被测分支（自带 `t.Fatalf`
  前置条件）；去重不能让上限恒真；**校验规则本身也必须能被触发**——新写一条校验就同时写一条"它必须报"
  的夹具用例。闸门 `scripts/check_test_hygiene.sh`：它按**名字**认断言（`t.Error*`/`t.Fatal*`，或
  `assert*/check*/verify*/expect*/require*` 辅助），所以自定义等待辅助（如 messaging 的 `waitFor`）**不算**
  断言——"等异步"的用例仍必须补一条直接的 `t.Errorf`；唯一豁免是 `hygiene:panic-guard` 标记（去掉某条
  nil 守卫后测试会 panic 而失败的那类）。另两条：函数体第一层不得出现裸 `t.Skip*`，同包只准一个
  `string→string` 的 ANSI 剥离辅助。
- **测试与生产必须共用同一个载体**（§11 坑 33）：新增一个"生产只填 A、测试却填 B"的字段对，
  就等于埋下一个永远不会红的测试。
- **链路级 E2E 只 mock 模型，不 mock 被测组件**：`internal/agents/e2e_*_test.go` 钉五条链路
  （授权回路、跨委派交接、委派生命周期、`/resume`、委派级重放），用真实 dispatcher、真实
  `WorkspaceGuard`、真实便签盘、真实 rollout 文件；harness 的接线顺序照抄 `internal/app/app.go`
  ——换顺序会测到一条生产不存在的路。脚本引擎**按 Agent 身份分派而不是全局调用序号**（序号会被
  循环里的补偿动作打断），且**必须先看 ctx**（忽略 ctx 的 mock 会掩盖"取消没传播"）。
  协议层用 `httptest` 假服务端钉字节形状；MCP 的假服务器 = 重新执行测试二进制。
- **耗时必须由测试自己负责**：计时走 seam（`datamanager.taskFlushDebounce`、
  `agents.stepRetryBackoffBase`、`tools.defaultToolTimeout`），不要 `time.Sleep`；LLM 退避
  （baseDelay 10s）不要跑完整重试序列——用短 `cfg.Timeout` 钳住预算，断言"服务端只收到 1 次请求"。
  **"扫目录再计数"的用例必须拥有那个目录**（唯一 projectID 或 `t.Setenv("HOME", …)`），否则
  `-count=4 -parallel 8` 下候选累加、`>=1` 恒真，门禁当场失效。
- 覆盖率基线（2026-10-14 `-cover`）：recovery 97.6 / toolspec 87.3 / config 84.2 / messaging 80.4 /
  datamanager 65.2 / memory 64.3 / llm 61.6 / mcp 62.1 / agents 59.5 / app 16.9。低覆盖的共性是
  "错了不报错，只是静默换模型/静默丢工具"，所以补的是**契约**而不是行数。
- 各包的计时 seam、`$HOME` 隔离方式、真实画像夹具与全部闸门测试名：见对应 `AGENTS.md` 的"闸门"节。
  TUI 改动必须跑渲染/尺寸矩阵测试。

## 9. 编码规范与架构不变量

### 9.1 规范

错误用 `fmt.Errorf("…: %w", err)` 包装，不吞错误；长任务必须响应 `ctx` 取消；注释默认不写，只写
"为什么"且不超过一行；`gofmt` 为准，但**存量文件有对齐漂移**（`internal/tui` 若干），改到被
`gofmt -l` 报出的文件时先 `gofmt -d` 区分"我写的"与"历史遗留"，不要顺手全量重排。
`scan`/树遍历类的 fixture 落地后顺手跑一次 `gofmt -l <目录>`——`go build/vet/test` **都不看 `testdata/`**
（§11 坑 41）。

### 9.2 守卫与全局不变量

**十一把守卫**（`bash scripts/check_all.sh` 一条命令跑完；每条都对应一次"曾经静默漂移"）：

```
check_no_manual_lookup        不许重新出现按名字查工具（FORBIDDEN 名单必须为零）
check_toolspec_leaf           toolspec 不得反向依赖 agents/tools
check_no_agents_import        director/ 子包不得 import internal/agents（扫传递闭包，防循环）
check_blueprint_leaf          internal/blueprint 只准标准库 + util + projectid
check_supervise_leaf          internal/supervise 只准标准库
check_shutdown_leaf           internal/shutdown 只准标准库 + supervise
check_no_bare_go              裸 go 全仓硬禁，**无 PENDING 名单**（唯一例外：supervise 自己）
check_no_inline_atomic_write  tmp+rename 只准 internal/util/atomic.go 一处
check_file_sizes              director/ 非测试文件 ≤300 行，存量按 PENDING 冻结
check_agents_md_size          根 + 17 份 AGENTS.md 的体积棘轮（防本文件再次膨胀）
check_test_hygiene            每个 Test* 必须能失败；裸 t.Skip 要有条件；同包只准一个 ANSI 剥离辅助
```

> **棘轮的纪律**：超限即红、**长回去即红、改小了必须同步下调 PENDING 的数字**否则脚本主动报失步。
> 一条常红的守卫等于没有守卫——所以 `check_no_bare_go.sh` 刻意不留名单（带名单的棘轮就是第二份会漂走的事实）。

**跨模块不变量**（包内的写在各包文档）：

- **goroutine 只能经 `internal/supervise` 起**（`supervise.From(ctx).Go("名字", fn)`：具名 + recover +
  可等待；`Drain(timeout)` 返回的是"哪几条还没退"的名字清单，不是一句超时）。两条纪律：
  ① **需要"等这一批"的地方仍用本地 WaitGroup**（`toolbatch`、`tui_resume_stop`）——Drain 等的是整条域，
  把批次当成域会把不相关的后台工作算进来，甚至等自己；② **跑任务的那条 goroutine 不进任务自己的域**
  （收尾时 Drain 自己 = 死锁）。
- **锁不许跨过 IO；投递一律"锁内装配、锁外写"**（`internal/http` 六个状态点、`DataManager.FlushAll`；
  且**首错不得提前 return**——退出时"第一条刷失败"不该让后面 N-1 条任务记忆蒸发）。
- **排在闸门上的调用要替 ctx 让路，而且照样产出结果**：在飞闸门与委派池都 `select` 上 `ctx.Done()`，
  取不到格子返回错误而不是硬等；`toolbatch` 的纪律是"已取消的元素照样启动、照样 commit"。
  不变量：**每个 tool_call 必有一条 tool 结果**，少一条就是 provider 400。从未取得的一格绝不去做
  `<-sem` 接收（会与其他释放方的接收互相吃掉）。
- **把指针装进 interface 的那一瞬间就不再等于 `nil`**：凡"具体类型指针 → interface"的返回必须在
  具体类型上先判 nil，否则"not wired"那条守卫永远不成立。
- **"配置项存在"不等于"配置项生效"**：判据只有一个——改这个值，有没有一条用户可观察的行为会变；
  新增配置项必须同时新增一个**能因为改值而失败**的测试。**"默认值"必须有人真的用它**：文档写 1000
  而六处手写 100，那个 1000 从来没生效过（§11 坑 35）。
- **"两份同形实现"必须各自有输入进聚合测试**：分层守卫逼出来的两份实现（`agents.classifyStop` 与
  `director.StopKindOf`）靠聚合测试对齐；加词表值时的正确顺序是**先给聚合测试喂一个新输入**，
  再让两边实现去追它——否则只有一边漂走时测试是绿的（§11 坑 18）。
- Director 是**门面 + 子包**；`toolbatch` 是无业务依赖的叶子包，不要往里加业务。

### 9.3 新增一个工具

① `internal/tools/` 实现执行器 → ② `internal/toolspec/schemas.json` 加 name/description/JSON Schema →
③ 实现旁边 `toolspec.MustRegister(Spec{Effect, Frequency, Mutates, Serial, NeedsAuth, PathParams, Trunc,
Display, Brief, Build})`（并发/授权/截断/展示**全部派生**）→ ④ 让它进某个画像的工具面（优先靠能力标签
自动匹配；只有委派类工具才写 `Audience`），**先看清 diff** 再 `-update` golden → ⑤ 需要 LLM 的工具用
`client.GetToolEngine("<name>")` + `[tools.llm.<name>]` → ⑥ `internal/tools/*_test.go`，涉及循环则补
`agents` 的执行循环测试。**MCP 外部工具走另一条通道**（运行期构造 Spec、不进 schemas.json / 全局注册表 /
golden），判据见 §5.3 与 §11 坑 44。

## 10. Director 行为准则

1. **禁止幻觉** — 不编造文件名/函数名/代码；不确定就委派 Repo。
2. **编码分离** — 不向用户输出代码块，所有修改经 `delegate_coding`。
3. **逐步执行** — 一次委派一个子任务，验证后再继续。
4. **强制委派** — 仓库探索走 `delegate_repo`；文件写走 Coding；Director 自己只用只读工具。
5. **禁止长驻进程** — 用 `go build` / 测试验证，不起 dev server。
6. **并行执行** — 只读工具与独立只读委派可以并行（由 `toolbatch` 调度）。
7. **思考先于行动** — 每次工具调用前给出 `<thought>` 块。

`<thought>` 格式契约（`internal/thinklink/thoughtplan.go`）：定界符字面成对、每轮恰好一个块、置于
工具调用之前；`goal`/`step`/`reason`/`plan` 是固定小写 key 各占一行（`current goal`/`reasoning` 等别名
会被归一）；`Validate()` 要求 `goal` 与 `reason` 非空且**至多一条** `[>]`；状态标记 `[x]` / `[>]` /
`[ ]`（解析器也容忍 `X`、`-`、`1.`）；字段**值**用 `[agent] lang` 的语言书写，key、标记、标签保持英文。
该块同时是 TUI thinklink 面板的内容与 ultimate 压缩的重建原料；**解析失败不报错、只静默丢失**，
所以格式必须严格遵守。目前 Director、Coding、Browser 的 prompt 强制该格式；Repo/Chat/DevOps/Meta 未要求。

## 11. 已知坑索引（46 条，编号稳定）

每条 = 一句判据 + 去处。去处是**会红的那条测试/那条硬约束**，不是"某个地方写着"。
四条跨模块的通用教训（33 / 25 / 39 / 18）正文在 §8.3 与 §9.2。

1. 本文件之外的旧资料普遍过期：`docs/ARCHITECTURE*.md`、`README.md` 的模块图与工具数落后于代码 → 以本文件与源码为准。
2. `dist/` 被 gitignore，但 `dist/data/models.json` 与 `dist/bin` 里的文件被跟踪：改完 `git add -f dist/...` → `internal/tools` 硬约束 15。
3. 模型目录与 `context_window` 是两个消费者的不同口径（压缩阈值 vs TUI 进度条）→ `internal/compression` 硬约束 2/8。
4. 压缩历史上写过两个假开关（一个 AND 门 + "见 false 就写回 true"）→ `internal/compression` 硬约束 9、`internal/config` 硬约束 2。
5. "声明 ≠ 可用"已消解：`Validate()` 双向对账 + golden 钉住每个 Agent 实拿的工具面 → `internal/toolspec` 硬约束 2/3。
6. TUI 事件通道无重放：一次性 cmd，消费了不重新武装就永久断流 → `internal/tui` 硬约束 1。
7. 布局高度必须自洽：`computeFooterHeight` 与 `View()` 顺序完全一致；`Height(h)` 含边框 → `internal/tui` 硬约束 3。
8. `browser-control` 的状态目录是**进程 CWD 相对路径**，所有调用必须带 `cmd.Dir` → `internal/browserctl` 硬约束 1/10。
9. 上游 `reload` 实际等于 `stop`，且没有 `hover`/`back`；上游源码不在本仓，**别编造 Rust 行号** → `internal/browserctl` 硬约束 9。
10. 无 CDP 环境变量时上游硬扫 9222/9223：必须显式注入，自启端口从 9333 递增并跳过 → `internal/browserctl` 硬约束 2。
11. 便签索引必须是派生态且**瞬态**：只进这次 LLM 调用的 messages，返回值绝不写回外层 → `internal/blackboard` 硬约束 1、`director` 硬约束 3。
12. `agent_exit` 收尾的 Agent，交付物不在 `Text` 里：全文只经 `deliverableText()` 取（判据是 JSON 形态，不是 Agent 名）→ `internal/agents` 硬约束 3。
13. `[blackboard]` 三个阀门靠**键的存在性**判定，不是零值 → `internal/config` 硬约束 11、`internal/blackboard` 硬约束 2。
14. Bedrock 豁免已整体删除（没有 Bedrock 专用客户端）；**往 `validate()` 加提前 return 之前，先确认下面的归一化段还会被执行** → `internal/config` 硬约束 1/10。
15. `toolspec.Validate()` 的每条规则都必须有能触发它的夹具（曾有"Serial 与 IsReadOnly 冲突"数学上不可能成立）→ `internal/toolspec` 硬约束 3。
16. 子 Agent 的 `agent_exit` 会不会真收尾，取决于它自己设的 `StopOnFinish` → `internal/agents` 硬约束 2。
17. **暂停不是取消**：闸门不取消 ctx；idle **和** total 都要冻结；`Resume` 必须推空闲基准；暂停态不走换 ctx 分支 → `internal/agents` 硬约束 5/11、`internal/tui` 硬约束 6、导览 pause 节。
18. "两份同形实现"必须各自有输入进聚合测试（先喂新输入，再让两边实现追它）→ §9.2、`director` 硬约束 9。
19. 检查点指纹只能由**读侧**计算（两侧规则同源，含 `compacted` 替换前缀）→ `internal/memory` 硬约束 1。
20. `Reasoning` 真的进 rollout：写侧发 `reasoning` item、重放侧还原，`DigestMessages` 算它 → `internal/memory` 硬约束 8、`internal/compression` 硬约束 5。
21. 检查点写在**终止路径**，不是每步一条（`TestNoPerStepCheckpointWritten`）→ `internal/memory` 硬约束 9、`internal/agents` 硬约束 12、`director` 硬约束 6。
22. **降级不是错误**：命中判据返回"报告 + 理由清单"，重放成功交回的是同一个 `call_id` 的第二条 output → `internal/agents` 硬约束 13、`internal/memory` 硬约束 11。
23. "扫目录再计数"的测试必须拥有那个目录（唯一 projectID 或 `t.Setenv("HOME", …)`）→ `internal/memory` 硬约束 7。
24. `pause.AbandonedError` 两半都要保住：取消侧 `errors.Is` 到 `context.Canceled`，停住侧 `errors.As` 且便签**什么都不写** → `internal/agents` 硬约束 11、`internal/blackboard` 硬约束 3。（旧文本点名的 `looksLikeCancelledContext` 与"toolbatch 取消短路"都已不存在。）
25. "配置项存在"不等于"配置项生效"：判据是"改值有没有一条可观察行为会变"，且必须有能因此失败的测试 → §9.2、`internal/config` 硬约束 4。
26. 布尔键的"缺省为真"必须靠键的存在性实现（Go 零值是 false）→ `internal/config` 硬约束 2/7.1。
27. 写 config.toml 只能文本级替换；值一律转义；删 provider 前先查引用 → `internal/config` 硬约束 3/5。
28. 向导能独立跑也能嵌进主 TUI，所以不碰 Yagent/Dispatcher/MCP；`applyOps` **不是事务**，失败必须带 note 与"已写入 N/M" → `internal/tui` 硬约束 9/14。
29. TUI 键位是**替换**不是追加别名：两个方向都要写，`keyUnbound` 不能长得像真按键 → `internal/tui` 硬约束 10。
30. TUI 行数预算必须按**渲染后**的行数算（`fitLines` / `wrapPlain` 硬切 / `capStatus` 三道闸门缺一个就复现）→ `internal/tui` 硬约束 11。
31. 表单屏的模态键必须带修饰符（`ctrl+p`/`ctrl+o`/`f2`）；键帽写的每个单键都必须真的能改状态 → `internal/tui` 硬约束 12。
32. 向导的 `done` ≠ 退出：独立跑要翻成 `tea.Quit`，嵌在主 TUI 必须不翻 → `internal/tui` 硬约束 13。
33. **测试与生产必须共用同一个载体**：并发谓词扫 `cfg.Adapters`、测试也填 `Adapters` → 八条用例全绿而生产一次都没并行。修法是把那个字段对**连根删掉**，不是补一个分支 → `internal/agents` 硬约束 1、§8.3。
34. 流式槽的键（`agent#delegationID`）≠ 显示名（裸 Agent 名）；没有坐标时不许变新 → `internal/tui` 硬约束 4。
35. **"默认值"必须有人真的用它**：文档写 1000、六处手写 `NewMessageDispatcher(100)` → `internal/messaging` 硬约束 4/5、§9.2。
36. 熔断器的"查询"本身就是那次试探（`Allow()` 带 open→half-open 迁移，half-open 只放一个探针）：加查询点前先数有几个调用点在读它 → 导览 recovery 节、`director` 硬约束 11（**这条配对目前没有自动化闸门**）。
37. 排队要发生在 `llm.timeout` **之前**（取格子在建 ctx 链 / 套超时之前），且 `select` 上 `ctx.Done()`：放错顺序会把"前面有人"算成"provider 慢"、把看门狗顶成超时 → `internal/llm` 硬约束 4、`internal/agents` 硬约束 9。
38. 工具回显少一个键，提交就在离开循环的那一刻静默消失：任何"工具层只回显、Agent 层才解读"的参数加字段必须同改两处 → `internal/tools` 硬约束 5。
39. 探针 `ProbeDeps()` 必须与生产 `depsFor` 同形，否则 golden 钉的是生产不存在的路（两边不对称本身就是证据）→ `internal/toolspec` 硬约束 6。
40. 写通道挂在 `agent_exit` 上，就必须有人保证它真的被调用：prompt 的 Output Contract + 一次收尾提醒 + 零提交留下一句可区分的账，三件缺一不可。症状是"目录建了、文件没有、不报错" → `internal/agents` 硬约束 14。
41. `testdata/` 是 Go 工具链的盲区：`go build/vet/test` 都不看它，只有 `gofmt -l <目录>` 会报 → §9.1、`internal/blueprint` 闸门注。
42. "像路径"不等于"是路径断言"：形状判定误伤中文散文里的斜杠列表与写短的前缀，一条拒丢整条 claim，全丢 ⇒ 文件永不出现。**任何"按形状识别 + 违规即丢弃"的校验，夹具里必须放一条与违规串同形的正常散文** → `internal/blueprint` 硬约束 6。
43. 防线生效过但没人知道它生效过，和静默丢失只差一步：**任何"可选写通道 + 终止型工具"的组合，都要问"被拦下来的那一次，谁看得见"**。账目经两处出口（工具输出会被压缩卷走、便签正文不进索引），不进 conclusion → `internal/blueprint` 硬约束 11、`internal/agents` 硬约束 15。
44. 动态规格不进全局注册表（会同时打穿 golden、Custom Agent 的 `Palette`、`Validate()` 的双向 1:1）；**没 annotations 一律按写处理**是同一条纪律的安全侧 → `internal/toolspec` 硬约束 5.1、`internal/mcp` 硬约束 3。
45. "连接落定前收到的通知"必须补一次（`listChangedPending`），且通知回调不能在 readLoop 里同步调用（读循环等自己 = 重发现永远超时）→ `internal/mcp` 硬约束 6。
46. `CombinedOutput` 的等待条件里含**管道 EOF**，而 EOF 由子孙进程决定：直接子进程退出后 exec 的 ctx 看门狗再也不触发 `Cancel`，于是工具超时与用户取消都打在空气上（`WaitDelay` 是唯一解）→ `internal/tools` 硬约束 16/17、`internal/shutdown`。

<!-- CODESEEK_INJECTION -->
# Code exploration: use CodeSeek MCP tools first

Before any Grep/Glob/Bash for code search, try CodeSeek tools first.
They give you AST-verified definitions with signatures and line numbers.

Tool priority (use in this order):
1. codeseek_search("query")      — FIRST for finding code by name or behavior
2. codeseek_callers("fn")        — REQUIRED before modifying any function
3. codeseek_callees("fn")        — to understand internal dependencies
4. codeseek_callgraph("fn", 2)   — to explore full calling context with depth
5. codeseek_skeleton("filepaths") — to explore file structure before reading full files
6. codeseek_snippet("fn", "filepath") — to read exact function implementation
7. Grep — ONLY for exact strings (error messages, UUIDs, log formats)
8. Glob — ONLY when you already know the exact filename pattern
<!-- /CODESEEK_INJECTION -->
