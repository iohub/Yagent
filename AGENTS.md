# Yagent Agent 开发知识库

> **本文件是 Agent 理解仓库和进行开发的标准指南。** 所有 Agent 在操作此仓库前必须先阅读本文件。
> 本文件描述的是当前代码的真实状态（核对时间 2026-10-19：**仓库蓝图 R1–R6 全部落地**
> （`docs/repo-blueprint-design.md` §21 实施记录）—— `internal/blueprint` 从零 LLM 扫描器一路做到
> 派生快照（`atomic.Pointer` 唯一写者 + 150ms 成本自适应）、形状②常驻注入（token 硬顶 + 四态必显）、
> `read_blueprint` 读通道、`agent_exit.blueprint[]` 写通道、五道校验 + 幂等 fold + 乐观 CAS +
> `revisions/` 归档 + 孤儿子节老化；**验收断言就是"文件出现"**（`e2e_blueprint_test.go`：真实 Repo
> 循环 + 真实扫描器 + 真实 guard，脚本化一轮 → `~/.yagent/data/blueprint/{projectID}/blueprint.md`）。
> R2 的 1965 行曾经只活在两条 `agent/coding-*` 委派分支上（便签里"已落地并全绿"是真的，只是不在
> 任何主战分支），本轮接回。仍待办：R8 导出层与
> `/blueprint`（`[blueprint] export_path` 至今没有消费者，别写进配置指南）。
> 2026-10-23：**外部能力接入落地**（`docs/skills-and-mcp-design.md` + 实施文档）——
> `internal/mcp` 从"只对着 codeseek 说话的单例"升级成多 server `Manager`（`tools/list` 分页、`instructions`、
> annotations、重连退避、四态状态账）；codeseek 成为第一个内建 server，6 个手写桥经 curated 名单保留、
> 漏掉的 3 个工具走通用通道；外部工具以 `mcp__<server>__<tool>` 动态进 `Deps.ExternalSpecs`（一律 Cold，
> 走既有 deferred 目录；**不进**全局注册表、golden、Meta 工具池），没 annotations 一律按写处理；
> `internal/skills` 重写成"目录式 + front-matter + 合并语义 + 逐条账"，目录进 system prompt 稳定前缀、
> 正文由新工具 `use_skill` 按需取回（声明面 49 → 50）；`[mcp]` / `[[mcp.servers]]` / `[skills]` 三段配置；
> 启动期第一次真的跑 `toolspec.Validate()`；TUI 新增 `/skills`、`/mcp` 面板与类型化斜杠补全（手打
> `/name args` 直接执行技能）。
> 上一轮 2026-10-22：**R7 以删除收场——知识整理机制整体下架，仓库级持久知识只剩蓝图一个 sink**
> （`internal/agents/consolidation_worker.go`、`repo_memory.go`、`knowledge_hook.go`、`internal/knowledge/`、
> `internal/memory/{shared,local}.go`、`internal/mcp/knowledge.go`、`internal/tools/{knowledge,specs_knowledge}.go`
> 全部删除；`consolidate_knowledge`/`prune_history` 两个工具随之从 `schemas.json` 摘掉，声明面 51 → 49，
> 画像不再 Grant `EffectKnowledge*`；`knowledge_injected` 事件再无发布方，TUI 的消费分支一并删除）。
> 删除的判据是"写入端活着、读取端硬关闭"——注入器 `Inject` 一直是恒真 if 提前返回，
> 于是知识库里的东西从来进不了任何一次上下文；蓝图 + 便签已是完整闭环。
> 自扫 golden 因此从 45 片/144 条边变成 **44 片/134 条边**（逐条归因写在 `scan_golden_test.go` 头上）。
> 上一轮 2026-10-18：**结构化并发 C1–C6 全部落地**
> （`docs/structured-concurrency-design.md`）—— 新增叶子包 `internal/supervise`（全仓 goroutine 的
> 唯一落脚点：具名 + recover + 可等待），裸 `go` 由 `check_no_bare_go.sh` 硬禁；rollout 改流入队 +
> 停点 drain；并行委派的死锁根因（executor 的并发谓词扫一份生产恒空的 `Adapters`）连根拔掉，
> 判据收成 `toolCallIsParallelizable` 一个函数；每次委派新建 Agent 实例（`SpawnForDelegation`）
> 并取消单槽交接；`ConversationMemory` 封装成"字段不导出、只走方法"；事件信封带上
> `Seq` + `delegation_id` 两个坐标，TUI 流式槽按委派分。C6 才真的放量：`[llm] max_in_flight_requests`
> 进程级在飞闸门、`[agent] max_parallel_delegations` 每轮 Run 一条委派池、
> `YAGENT_CONCURRENCY=0` 一键回串行，外加六处"锁内 IO / 静默失效"的清理（MCP 请求 deadline、
> 熔断 half-open 单探针、TaskManager 投递移出锁 + 终态过期、consolidation `Submit` 不再
> send-on-closed、`FlushAll` 锁外刷、TUI 覆盖 cancel 前先释放旧值）。
> 上一轮 2026-10-16：**配置面连根拔 + 首次使用引导**
> （`docs/config-trim-onboarding-design.md`）—— 删掉 13 组"解析了但没人读"的键并留下
> `removedKeyHints` 逐条点名；把 6 处"文档与代码不符"的键补成真语义（第三档真能关、压缩只剩
> 一个开关、`step_retries` 下发到六个子 Agent、Coding 侧 `keep_plans` 取配置、补
> `[agents.llm.browser]`、TUI 键位从"追加别名"改成"替换+释放"）；新增注释安全的 provider
> 写回层与 `api_key_env`；`yagent setup` / TUI `/setup` / 首启自动进入的引导向导；配置坏了
> 不再静默 `os.Exit(1)` 而是打 stderr 人话。
> 上一轮 2026-10-15 落地"停止/继续"三层（`docs/pause-resume-design.md`）—— P1 活暂停
> （`internal/pause` 闸门、`activity` 计时冻结、两个循环的 step 边界接入点、`paused` 停止原因、
> TUI `/pause` `/continue` 与 HTTP pause/continue）；P2 检查点重放 B 层（`delegation_id` 串父子
> rollout、`agent_checkpoint` 记录、`ReadRolloutForReplay`、executor 的 `ResumeState` 接缝、
> planner 的 `PendingResumption`、八条降级判据、`[resume]` 配置）；P3 交接报告 C 层
> （不叫 LLM 的降级报告 + `status=degraded` 便签）；再上一轮 2026-10-14 补齐核心系统单测基线，
> 并修掉三处"错了不报错"的静默失效 —— Bedrock 配置归一化、Anthropic thinking budget、
> 流式 tool_use 装配）。
> 文件末尾由 codeseek 自动维护的注入区块除外。

---

## 目录

- [1. 项目概述](#1-项目概述)
- [2. 技术栈](#2-技术栈)
- [3. 模块地图](#3-模块地图)
- [4. Agent 角色定义](#4-agent-角色定义)
- [5. 核心工具](#5-核心工具)
- [6. 系统架构](#6-系统架构)
- [7. 配置系统](#7-配置系统)
- [8. 开发与构建](#8-开发与构建)
- [9. 编码规范与架构不变量](#9-编码规范与架构不变量)
- [10. Director 行为准则](#10-director-行为准则)
- [11. 已知坑](#11-已知坑)

---

## 1. 项目概述

**Yagent** 是 Go 语言编写的多智能体 AI 编程助手，采用 **Hub-and-Spoke（中枢-辐条）** 架构：
**Director Agent** 是唯一与用户交互的 Agent，负责分析、委派、评审；具体读写由专用子 Agent 完成。

| 维度 | 描述 |
|------|------|
| **系统类型** | 多智能体 AI 编程助手 |
| **架构模式** | Hub-and-Spoke + 三档上下文压缩 + Pub-Sub 事件总线 |
| **核心原则** | 职责分离、强制委派、验证优先、可恢复 |
| **用户接口** | TUI (Bubble Tea v2) / HTTP+WebSocket (gin+melody) / 非交互 `--prompt` |
| **代码引擎** | `codeseek`（Rust，git submodule，经 MCP stdio 调用，二进制随 Go 程序 `go:embed` 分发） |

### 设计原则

1. **单一交互点** — Director 是唯一直接面向用户的 Agent。
2. **职责分离** — 每个子 Agent 有明确职责边界与权限（只读 / 可写 / 仅 Shell）。
3. **强制委派** — 文件与代码修改必须经 Coding-Agent，仓库探索必须经 Repo-Agent。
4. **验证优先** — 用 `go build` / 测试验证，不启动长驻进程。
5. **可追溯与可恢复** — 任务记忆（JSONL）+ 同步 rollout 日志双写，支持 `/resume` 断点续跑。

---

## 2. 技术栈

### 2.1 主要依赖（`go.mod`，Go **1.25.8**，模块名 `yagent`）

| 类别 | 技术 | 用途 |
|------|------|------|
| CLI | `spf13/cobra` | 子命令与全局 flag |
| LLM 抽象 | `openai/openai-go/v3` | OpenAI-compatible 协议；另有自研 Anthropic Messages 引擎 |
| HTTP | `gin-gonic/gin` + `olahol/melody` | REST + WebSocket |
| TUI | `charm.land/bubbletea/v2`、`bubbles/v2`、`lipgloss/v2`、`glamour/v2` | 终端界面（注意是 **v2**） |
| 浏览器 | `go-rod/rod`（go.mod 仍声明，代码已无引用） | Browser-Agent 重构时的候选依赖 |
| Token | `pkoukk/tiktoken-go` | cl100k_base 计数（`internal/tokenutil`） |
| 配置 | `BurntSushi/toml` | 配置解析。**`fsnotify` 只活在 `go.mod`，Go 代码里零引用**：词典与配置都没有热加载（见 `internal/AGENTS.md` 的 dict 一节） |
| Diff | `aymanbagabas/go-udiff` | `search_replace_in_file` 返回 unified diff |
| 其他 | `google/uuid`、`lucasb-eyer/go-colorful`、`rivo/uniseg` | ID、配色、字宽 |

### 2.2 外部依赖

`ripgrep`（**git 树里没有内嵌**：`dist/bin` 只跟踪一个占位文件，`internal/tools/search_operations.go:16-23`
回落系统 PATH；release 构建才把 rg 放进 `dist/bin`）、`fzf`（模糊搜索）、`codeseek` 二进制（默认从 `dist/bin` 解出到 `~/.yagent/bin`）、
`browser-control`（Rust CLI，随 release 构建内嵌 `dist/bin`、启动时解出到 `~/.yagent/bin`；查找顺序 `[browser] binary_path` → `~/.yagent/bin/browser-control` → `$PATH` 不变）
与 Chrome/Chromium —— 两者缺一，Browser-Agent 即不可用（`delegate_browser` 描述会自动降级）。

### 2.3 LLM 接入

两种 `api_format`：**openai**（默认，OpenAI-compatible）与 **anthropic**（原生 Messages，支持 Extended Thinking）。
Provider 只是 base_url + api_key 的组合，因此小米 MiMo、百炼、硅基流动、DeepSeek、Moonshot、Mistral、Z.ai、OpenRouter、Anthropic 官方均可用。**没有 Bedrock 专用客户端**：`api_base_url` + `api_key` 对每个 provider 一视同仁地必填，所谓 Bedrock 配置也得指向一个 OpenAI-compatible 网关（以前按 provider 名字前缀豁免那两项，只会把失败推迟到第一次调用，见 §11 坑 14）。密钥也可以来自环境变量：`api_key_env = "DASHSCOPE_API_KEY"` 在 `validate()` 里一次性展开进 `APIKey`，之后所有消费方读的都是同一个字段。

`reasoning_effort` 在两侧含义不同：**openai** 侧原样透传 `reasoning_effort` 并额外发
`thinking={type:enabled}`（DeepSeek 思考模式要求两者都在）；**anthropic** 侧映射成 `thinking.budget_tokens`
—— `low|medium|high|max` = 1024/8192/24576/32000，同时把 `max_tokens` 顶到 `budget+4096`、
`temperature` 强制 1.0（provider 在思考模式下拒绝其它值）。**认不出的取值一律当作"没开思考"**：
以前会把 `budget_tokens: 0` 发出去，而 provider 要求 ≥1024，一个拼错的 effort 就让此后每次调用都 400。

provider 的 `max_tokens` / `temperature` 是**引擎级默认**，优先级为
`单次 CallOptions > provider 配置 > 不发这个字段`（anthropic 侧未配时兜底 8192，因为 Messages API 必填）。
两个引擎的构造函数收的是 `*config.ProviderConfig` 而不是散参数——散参数时每往 `ProviderConfig`
加一个键就多一处"配置写了但没人读"，而 `max_tokens` 曾经正是这样失效的（请求体里根本没有它，
thinking 模型因此能一口气生成 39k token）。**`0` 一律按"没配"处理**：`ProviderConfig.Temperature`
是裸 `float64`，分不清"写了 0.0"和"没写"，而显式发 `temperature: 0` 会把服务端默认采样顶成贪心解码。
钉住这套形状的是 `engine_openai_test.go:TestOpenAIEngine_ProviderDefaultsReachWire` 与
`engine_anthropic_test.go:TestAnthropicBuildRequest_ProviderDefaults`。

---

## 3. 模块地图

```
yagent/
├── main.go                     # cobra 入口：tui(默认) / http；--prompt -f -p -d -c -y -Y -Q
├── embed.go                    # //go:embed dist/bin/*  +  dist/data/models.json
├── build.sh                    # Rust(codeseek) + Go 一体构建（release 默认）
├── internal/
│   ├── agents/                 # Agent 实现（根包 = 具体 Agent + 通用循环）
│   │   ├── director.go         # Director（门面；编排逻辑在 director/ 子包）
│   │   ├── coding.go repo.go chat.go devops.go browser_agent.go meta.go
│   │   ├── executor.go         # RunAgentLoop：所有子 Agent 共享的 LLM-工具循环
│   │   ├── specs.go            # Agent 侧工具的注册表声明（checkpoint/delegate_*/read_*）
│   │   ├── ports.go            # 窄接口：切断 agents ↔ 子系统的循环依赖
│   │   ├── result.go           # 子 Agent 结果 finalize / FormatForDirector
│   │   ├── activity/           # 活动感知 idle/total timeout（Heartbeat / WithIdleTimeout / Pause / Resume）
│   │   ├── director/           # Planner、Meta 注册表、项目上下文加载、熔断状态、指标
│   │   ├── toolbatch/          # 只读工具并行分批调度（无业务依赖的叶子包）
│   │   ├── git_checkpoint.go   # 每个编码子任务的 git 快照/回滚
│   │   ├── resume_state.go     # 重放接缝（B 层）：WithResumeState(ctx) → RunAgentLoop 从第 N 步续跑
│   │   ├── checkpoint.go       # 子 Agent 停止时落 agent_checkpoint 的薄封装
│   │   ├── resume_delegation.go # 恢复侧编排（B+C）：delegation_id 扫描、八条判据、降级交接报告
│   │   └── *.prompt.md         # 各 Agent 的 System Prompt
│   ├── app/app.go              # Yagent：一次性装配 MCP/工具引擎/守卫/子 Agent/Director
│   ├── browserctl/             # 浏览器执行层：外部 browser-control CLI 的进程/工作区/URL 策略/渲染
│   ├── blackboard/             # 跨 Agent 便签：note(存储) / handoff(结构化交接) / index(派生索引)
│   │                           #   resolve(ContextPack) / gc(生命周期) / board(路径与原子写)
│   ├── blueprint/              # 仓库蓝图（R1–R6 已落地，docs/repo-blueprint-design.md v4 + §21 实施记录）：
│   │                           #   scan*.go 零 LLM 扫描器（43 片/137 条边的自扫 golden）；derive.go 的
│   │                           #   派生快照（atomic.Pointer 唯一写者 + 成本自适应）；index.go 形状②渲染
│   │                           #   （token 硬顶 + stale/unannotated/TRUNC/oversize 四态）；prose.go 按需视图；
│   │                           #   fold.go/merge.go/commit.go 五道校验 + 幂等 fold + 乐观 CAS + revisions/；
│   │                           #   freshness.go 结构指纹与三态锚点；gc.go 孤儿子节老化。
│   │                           #   叶子包（只用标准库 + util + projectid，check_blueprint_leaf.sh 守：
│   │                           #   gitinfo/tokenutil 由装配层做成参数传进来，不 import）；
│   │                           #   启用判据是 config.BlueprintEnabled()（本包不读环境变量）
│   ├── projectid/              # 项目绝对路径 → 文件系统安全的目录名（rollout / artifact / 蓝图共用一把主键；
│   │                           #   director.ComputeProjectID 现在只是转发，TestComputeProjectID 仍钉着具体串）
│   ├── artifact/               # 超预算委派结果的落盘存储 + 摘要
│   ├── compression/            # 三档上下文压缩（rule / emergency / ultimate）
│   ├── config/                 # TOML 加载 + 三级 provider 解析；persist.go 的 SetKeyInSection 是
│   │                           #   唯一写回内核（文本级保注释），setup.go 是 provider 写/删 +
│   │                           #   首启判据（NeedsSetup / IsPlaceholderKey / api_key_env 展开）
│   ├── datamanager/            # 任务记忆 JSONL（5s 防抖）+ 索引
│   ├── dict/                   # 补全词典（只在 TUI 构造时加载一次，无热加载）
│   ├── diff/                   # unified diff
│   ├── embedbin/               # 从 embed FS 解出二进制（MD5 校验，版本化）
│   ├── globalctx/              # 全局上下文（EnvView / RepoContextStore 两个并发视图）。
│   │                           #   没有 Version/BuildTime/GitCommit 三个字段——build.sh 的 -X 注入的是空气
│   ├── http/                   # gin REST + melody WebSocket + TaskManager
│   ├── llm/                    # Engine 接口、OpenAI/Anthropic 引擎、Fallback、Router
│   ├── logging/                # slog 全局日志 + 按任务分目录
│   ├── mcp/                    # stdio JSON-RPC 客户端 + 多 server Manager（tools/list 发现、重连、状态账）
│   ├── memory/                 # ConversationMemory、rollout 读写（断点续跑的数据源）
│   │                           #   rollout_replay.go(重放+指纹) / rollout_checkpoint.go(坐标+工作区) / rollout_ctx.go(delegation_id)
│   ├── messaging/              # 事件总线（Dispatcher / Publisher / consumers/）
│   ├── models/                 # 模型目录（dist/data/models.json）：context window + benchmark
│   ├── protocol/               # 由 protocol/agent-events.yaml 生成的事件类型
│   ├── pause/                  # 暂停闸门（A 层）：Gate + ctx value + AbandonedError，只依赖 activity
│   ├── recovery/               # LLM 熔断器
│   ├── skills/                 # 技能注册表：目录式/扁平 SKILL.md、front-matter、合并语义、use_skill 读通道
│   ├── supervise/              # 全仓 goroutine 的唯一落脚点：具名 + recover + 有人等（C1，
│   │                           #   docs/structured-concurrency-design.md）。叶子包（只依赖标准库，
│   │                           #   check_supervise_leaf.sh 守）；裸 `go` 由 check_no_bare_go.sh 禁掉
│   ├── thinklink/              # 结构化 <thought> 解析 + 思维日志（驱动 ultimate 压缩）
│   ├── tokenutil/              # tiktoken 封装（EstimateTokens）
│   ├── tools/                  # 工具 Adapter 实现 + workspace_guard + user_confirm
│   ├── tui/                    # Bubble Tea v2 界面（82 个文件）
│   │   ├── setup*.go           #   引导向导：状态机+落盘队列 / 按键与表单行规格 / 外壳 / 七屏正文 /
│   │   │                         #   端点预设 / 嵌入主 model（docs/setup-wizard-ui-design.md）
│   │   ├── tui_keymap.go       #   [tui.keybindings] → 按键翻译表（替换语义，见 §11 坑 29）
│   │   ├── anim/ common/ components/ layout/
│   └── util/                   # 崩溃上报等；atomic.go 是全仓唯一的 tmp+rename 落盘内核（I21）
├── protocol/                   # agent-events.yaml（SSOT）+ 生成的 go/ 与 ts/ 副本
├── scripts/                    # build-centos7 / build-extension / protoc-gen-yagent
│                               #   + 九把守卫（check_*.sh），其中 check_all.sh 是一条命令的本地闸门
├── patches/                    # CI 用的 vendored-openssl 补丁
├── dist/                       # 构建产物 + 被跟踪的 models.json（见 §11 坑 2）
├── codeseek/                   # git submodule → iohub/codeseek（Rust 代码智能引擎）
└── docs/                       # ARCHITECTURE.md / config-guide.md / 设计稿
```

### 3.1 模块知识文档（嵌套 `AGENTS.md`）

`internal/` 下 15 个模块目录各有一份**局部** `AGENTS.md`，加上导览 `internal/AGENTS.md` 共 16 份。
局部文档写的是"在这个目录里改代码之前必须知道、而又派生不出来"的东西：职责边界（尤其不负责什么）、
硬约束、历史坑、入口文件、钉住不变量的测试函数名。

新增这类文档会让蓝图扫描器把它们计入所在目录的 md 文件数（**只数不读**，`agents.md` 出现在
`internal/blueprint/freshness.go:47` 只是"这个 token 像路径"的词表，没有特殊处理）。

| 约定 | 内容 |
|------|------|
| **什么时候读** | 进任何目录动手之前先看它有没有 `AGENTS.md`；没有局部文档 = 该目录没有不可派生的知识，直接读代码。导览表在 `internal/AGENTS.md` |
| **写什么** | 只写机器派生不出的：边界、禁令、坑、入口、闸门 |
| **不写什么** | 模块列表 / 文件计数 / 依赖边 / 每个模块有哪些文件——这些由 `internal/blueprint` 每轮派生，抄进文档就是第二份会漂走的事实 |
| **与根文件的分工** | 根 `AGENTS.md`（本文件）讲跨模块的架构、流程与全局不变量；局部 `AGENTS.md` 讲"在这个包里怎么做会静默坏掉"。**同一句话不要同时写在两处** |
| **引用格式** | 每条约束带 `internal/<包>/<文件>.go:行` 或测试函数名，让读者能跳过去核实；仓库根文件（`main.go`、`embed.go`、`build.sh`）按根路径写 |
| **谁加载它** | yagent 运行时的 `internal/agents/director/project_context.go:53` 只读工作区根的 `YAGENT.md`/`CLAUDE.md`/`AGENTS.md`，**不递归**——所以嵌套文档今天只服务于读代码的人与 coding agent，运行时子 Agent 读不到 |

> 2026-10-21 随这批文档一起做了一次事实核对，顺手改掉了根文件里五处与代码不符的描述：
> `fsnotify` 热加载（Go 代码零引用）、`globalctx` 的三个版本字段（不存在，但 build.sh 还在 `-X` 注入）、
> "ripgrep 已内嵌"（git 树里只有占位文件）、注入器硬关闭的位置（当时是 `internal/knowledge/injector.go:74-78`，
> 不是 `:67-71`；整个包已于 2026-10-22 随知识机制删除）、
> 行数棘轮的 PENDING 上限（`planner_run.go` 是 **572 行**，不是 658）。

> 旧的 `pkg/messaging/` 已迁到 `internal/messaging/`；`internal/compact/` 已重构为 `internal/compression/`。

---

## 4. Agent 角色定义

所有 Agent 实现 `Agent` 接口（`internal/agents/types.go:39`），由 `internal/app/app.go:398-522` 统一构造。
每个 Agent 的 System Prompt 是同目录的 `<name>.prompt.md`，改动行为请同时改 prompt 与代码。

| Agent | 文件 | 定位 | 权限 | Max Steps 默认 |
|-------|------|------|------|----------------|
| **Director** | `director.go` + `director/` | 项目经理/技术主管，唯一对话入口 | 只读 + 委派 | 100 |
| **Coding** | `coding.go` | 高级开发工程师 | 完整读写 + Shell | 150 |
| **Repo** | `repo.go` | 架构师/审计员 | 只读 + 可委派 DevOps | 50 |
| **Chat** | `chat.go` | 通用问答 | 无文件系统访问 | 50 |
| **DevOps** | `devops.go` | 系统管理员 | 只读 + Shell | 50 |
| **Browser** | `browser_agent.go` | 网页操作：经外部 `browser-control` CLI 驱动真实 Chrome | 浏览器动作 + 会话工作区产物（无 Shell、无文件写工具） | 200 |
| **Meta** | `meta.go` | 自定义 Agent 设计器 | 无工具，单次 LLM 出 JSON | —（无 step 循环；重试 5） |
| **Custom** | `internal/agents/director/types.go:60` | Meta 设计后运行时注册 | 由 LLM 选择的子集 | 15 |

**Director 工具**：由画像 `toolspec.ProfileDirector` 派生（`internal/toolspec/profiles.go:24-39`，
装配点 `internal/agents/director_surface.go:97-98`）——`delegate_*` + `agent_exit`、`read_artifact`、`read_note`、`list_notes`、`deepthinking`、`ask_user_for_help`，
以及只读分析工具（`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`）。
`Deny: EffectFSWrite | EffectShell` 是"Director 绝不直接写文件"的机械保证，不再依赖一份手写的工具清单。

**Coding 工具**：`create_file`、`search_replace_in_file`、`delete_file`、`rename_file`、`list_dir`、`print_dir_tree`、
`read_file`、`search_by_regex`、`run_bash`、`thinking`/`micro_agent`/`deepthinking`/`design`、
`semantic_search`/`query_code_skeleton`/`query_code_snippet`/`find_function_callee`/`find_function_caller`、
`git_checkpoint_*`（仓库+配置开启时）、`delegate_browser`、`agent_exit`、`ask_user_for_help`。

**Repo 工具**（只读，含调用图）：`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`、
`semantic_search`、`query_code_skeleton`、`query_code_snippet`、`find_function_callee`、`find_function_caller`、
`query_call_graph`、`deepthinking`、`delegate_devops`、`agent_exit`。

**Browser 工具**（14 个，能力标签在注册表、argv/render 在 `tools/browser_operations.go` 的 spec 表，schema 在 schemas.json）：
`browser_status`、`browser_open`、`browser_inspect`、`browser_snapshot`、`browser_click`、`browser_fill`、
`browser_type`、`browser_press`、`browser_scroll`、`browser_select`、`browser_tabs`、`browser_wait`、
`browser_screenshot`、`browser_eval`，外加 `agent_exit` / `ask_user_for_help`。
`browser_eval` 仅对敏感 JS（cookie/存储/网络请求/提交表单/动态执行）要求授权；截图路径由工具层收敛到会话工作区。
能力不可用时 `delegate_browser` 的描述会降级为"不可用 + 原因 + 修复方式"（见 `browserDelegateDescription`）。
完整设计见 `docs/browser-agent-design.md`。

> **工具注册已收归注册表**（`internal/toolspec`，见 `docs/tool-registry-design.md`）：每个工具在实现旁边
> `MustRegister(Spec{...})` 声明能力标签/频率档/授权要求/执行器，Agent 侧只写一份**能力画像**
> （`toolspec/profiles.go`），装配统一走 `tools.NewSurface(profile, deps)`。
> 七个 Agent（Director + 六个子 Agent）**全部**走这条路，`lookupToolFunc`/`getToolFunc` 已删除；
> `scripts/check_no_manual_lookup.sh` 是棘轮守卫（PENDING 名单已清空，往里加条目必须评审）。

### Meta / 自定义 Agent 注册流程

```
delegate_meta → MetaAgent 单次 LLM 出 JSON → ParseMetaAgentOutput (internal/agents/director/meta_handler.go:100)
  → 校验必填字段 → 按名字建 delegate_<snake_name>（registerCustomAgent，internal/agents/director_surface.go:284；
    工具子集由 ProfileDirector.Palette 选，同文件 :263）并追加到 Director 工具表
  → 立即以 MaxSteps=15 执行一次 → 新 Agent 在本任务内永久可用（写进 system prompt 的清单）
```

---

## 5. 核心工具

### 5.1 Adapter 模式

```go
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

type Adapter struct { name, description string; fn ToolFunc
                      schema map[string]interface{}; guard *WorkspaceGuard; readOnly bool }
```

`internal/tools/adapter.go`：`NewAdapter` + `WithSchema` / `WithReadOnly` / `WithReadOnlyIfKnown`；
`Call()` 依次做 JSON 解析 → 工作区边界检查 → 危险操作授权 → 执行；`ToToolDef()` 产出 `llm.ToolDef`。
`SortToolDefs` / `ComputeToolDefsHash` 保证工具顺序稳定（利于 provider 端 prompt 缓存）。

**并行性判定**：`Adapter.IsReadOnly()`（由 `Spec` 的 `!Mutates && !Serial` 派生）为真、且不在
`toolspec.IsSerial` 名单里的工具，
可由 `toolbatch` 分批并行执行（默认并发 4，上限 8，`[agent] max_parallel_readonly_tools`）。
`read_note` / `read_artifact` / `list_notes` 与认知类、调用图工具都走这条派生路径。
**委派是另一道闸门**：`delegate_*` 在子 Agent 侧一律 `Serial`（所以一次只跑一个），
Director 侧的只读委派可以并发，但同时跑几个由 `[agent] max_parallel_delegations` 决定 ——
池子建在**这一轮 Run 的 `directorToolRunner`** 上（`newDirectorToolRunner` 是唯一构造方式），
不是进程级：嵌套委派（Repo→`delegate_devops`）用的是自己那一轮的池，
于是"父占一格、子在同一格里排队"的死锁形状从结构上不存在。
判据是**一个函数**：`agents.toolCallIsParallelizable(surface, name)`（`executor.go`），
Director 的 `isToolParallelizable` 与子 Agent 循环都调它 —— 两份同形判据的历史代价见 §11 坑 33。
冷工具（还没取回 schema）也算可并行：它在这一批里只收到 teach 回执、不执行副作用。
闸门是 `TestDerivedMetadataMatchesLegacyTables`：新增可并发工具必须进 `signedReadOnlyAdditions` 或 `newTools`
集合并写明理由，否则测试直接红 —— 那两处集合就是"公开承认我扩大了并发面"的地方。
另外 `TestParallelAdmission_*`（`parallel_admission_test.go`）是拿**真实画像**
（`ProfileRepo` / `ProfileCoding` / `ProfileChat`）问这个判据的，所以画像或能力标签漂走时会红；
自己拼一个工具列表来测等于重犯坑 33。

### 5.2 工具总览（schema 见 `internal/toolspec/schemas.json`，50 条 + 运行期动态外部工具）

> 每个工具的完整契约 = `schemas.json` 的 description/parameters + `Spec` 的能力标签。
> 派生查询：`IsReadOnly` / `IsSerial` / `NeedsAuth` / `PathParams` / `TruncationPriority` /
> `IsInteractive` / `IsCompactDisplay` / `ModelSlot`。

| 工具 | 类别 | 说明 |
|------|------|------|
| `read_file` `list_dir` `print_dir_tree` `search_by_regex` | R | 只读文件与 ripgrep 搜索 |
| `create_file` `delete_file` `rename_file` `search_replace_in_file` | W/D/M/E | 文件写；替换强制唯一匹配并返回 diff |
| `run_bash` | S | Shell；`is_background` / `is_dangerous` |
| `semantic_search` `query_code_skeleton` `query_code_snippet` | R | codeseek MCP 桥 |
| `find_function_callee` `find_function_caller` `query_call_graph` | R | 调用图；改函数前**必须**先查 callers |
| `thinking` `micro_agent` `deepthinking` `design` | C | 反思 / 隔离子 LLM 推理 / 多步深度思考 / 编码前设计闸门 |
| `agent_exit` `ask_user_for_help` | P | 结束任务（可带 `handoff{conclusion,facts[],open_questions,status}`，见 §6.4；另有 `blueprint[]` 写入通道，只有 Repo 的完成接缝会落盘，见 §6.4）/ 反问用户（confirm、select、input 三种交互） |
| `git_checkpoint_create` `git_checkpoint_list` `git_checkpoint_rollback` | G | 编码子任务快照 |
| `read_note` `read_artifact` `list_notes` | R | 读跨 Agent 便签（handoff 块 + 分页正文）/ 超预算产物全文 / 枚举整块板上有哪些结论（boardID 存在时自动注入，`list_notes` 刻意不含正文） |
| `use_skill` | R | 按名取回用户技能的完整正文（front-matter 事实 + 分页），skill 目录在 system prompt 里常驻。没有技能时回执自带"放哪儿"的修复指引，不从工具面消失 |
| `read_blueprint` | R | 读仓库蓝图：`section/module/field/state` 收窄 + `offset/limit` 分页。**六个画像**都拿得到（`EffectContext` 已 Grant），没有蓝图时回执自带原因与修复方式（不静默摘工具）。目录→目录归它，符号→符号归 codeseek |
| `browser_status` `browser_open` `browser_inspect` `browser_snapshot` `browser_click` `browser_fill` `browser_type` `browser_press` `browser_scroll` `browser_select` `browser_tabs` `browser_wait` `browser_screenshot` `browser_eval` | 浏览器 | 经外部 `browser-control` CLI 驱动真实 Chrome；Browser-Agent 持有（详见 `docs/browser-agent-design.md`） |
| `delegate_repo/coding/chat/devops/browser/meta` | 委派 | Director 持有；`delegate_browser` 也在 Coding 上（能力不可用时描述自动降级） |

> **不存在** 名为 `flow_control` 的工具（旧文档有误）；流控制由 `agent_exit` 与 `ask_user_for_help` 承担。

### 5.3 MCP、外部工具与 codeseek

- `internal/mcp` 是自研 stdio JSON-RPC 2.0 客户端 + **多 server `Manager`**：`tools/list`（跟随
  `nextCursor` 分页）、`initialize.instructions`、`tools/call`、重连退避、状态账四件事都在这一层。
  传输层（`client.go` 的 `sendJSON`/`doRequest`/`readLoop`/`IsAlive`）没有改。
- **codeseek 是第一个内建 server**。6 个手写桥（`semantic_search` 等）原样保留（结果整形 + `ErrIndexBuilding`
  降级文案），服务端名进 curated 名单，不再以 `mcp__codeseek__*` 重复注册；`codeseek_init/status/list`
  经通用 `tools/list` 成为动态工具。
- **外部工具不进全局注册表**（`toolspec` 仍是 `init()` 期写入的只读事实）：它们从
  `Deps.ExternalSpecs` 进 `Surface.rebuild()`，名字是 `mcp__<server>__<tool>`，一律 Cold（走既有
  deferred 目录 + `load_tool`）。三条理由见 §11 坑 44。
- 副作用判定：`readOnlyHint=true`（或配置 `read_only` 名单）⇒ 只读可并发；明确 false/destructive ⇒ 写；
  **没有 annotations 一律按写处理**（`Mutates`+`Serial`+`NeedsAuth`），写类默认不注册，除非 `allow_write = true`。
- 状态五档 starting / ready / degraded（进程活着但没拿到清单）/ offline / disabled，一段文本三处出口
  （启动日志、`/mcp` 面板、Director 的 system prompt），由 `mcp.RenderStatusLines` 单源渲染。
- **启动不等 MCP**：`Manager.Start` 只同步 spawn 子进程（`Client("codeseek")` 当场可取，
  手写桥捕获的就是这个指针），initialize 与 `tools/list` 在后台跑。initialize 的预算是 15 分钟
  而不是 `startup_timeout` —— 判死并重开等于让冷索引从头再建。

### 5.4 安全机制

| 机制 | 位置 | 实际行为 |
|------|------|----------|
| **WorkspaceGuard** | `tools/workspace_guard.go` | 危险工具（create/edit/delete/rename/run_bash）路径必须落在工作区内；会话/项目级放行记录在 `~/.yagent/settings.json` |
| **UserConfirmManager** | `tools/user_confirm.go` | 走事件总线的 pub-sub 授权：发 `user_help_needed`，等待 `user_help_response`（按 `request_id` 配对） |
| **run_bash 纵深防御** | 同上 | 无黑名单；信任 LLM 的 `is_dangerous` + 任何指向工作区外的绝对路径 token 都要求授权（`safeSystemPaths` 例外） |
| **大小限制** | `tools/file_*` | 编辑双向 10MB；整读 10MB、软警告 2MB、单次 1000 行 |
| **YOLO** | `-y` / `-Y` | `--yolo` 跳过工作区内授权；`--full-yolo` 连工作区外路径也放行，并移除 `ask_user_for_help` |
| **分层守卫** | `agents/ports.go` + `scripts/` | 子 Agent 需要的依赖经窄接口注入；`director/` 子包禁止 import `internal/agents`（防循环） |

---

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

### 6.2 执行循环

- **子 Agent**：`RunAgentLoop(ctx, ExecutorConfig)`（`internal/agents/executor.go:219`）。
  每步：LLM 重试（指数退避 ≤30s）→ 三档压缩检查 → 带 `llmTimeout` 的 LLM 调用 → 抽取 `<thought>` →
  纯文本回复则注入提醒或结束 → 工具调用交给 `toolbatch` 分批并行 → `agent_exit` 立即返回；
  步数耗尽报错，panic 被 recover 并写 `turn_aborted` rollout。
  `ExecutorConfig` 关键字段：`SystemPrompt / UserInput / Surface / LLM / MaxSteps / Publisher / StopOnFinish /
  LLMTimeout / StepRetries / SystemAsHuman / NoActionRetryLimit / MaxParallelReadOnlyTools / OnAgentStart / OnAgentExit`。
  `Surface` 是**唯一**的工具来源（`Adapters` 字段已删除，见 §11 坑 33）：工具声明、按名解析、并发准入都只问它。
  `StepRetries` 现在由各 Agent 从 `[llm] step_retries` 带进来（以前全仓无写入点，六个子 Agent 恒为 0）。
  纯文本收尾的提醒有**两条文案**：通用那条（"立刻调用工具去 explore/edit/run/verify"，Coding 那类
  执行型 Agent）与 `cfg.NoActionReminder` 覆盖版（Repo 用收尾契约"Call `agent_exit` now + 填 handoff +
  本轮打开过的目录补 blueprint[]"）。理由见 §11 坑 41：对以报告为交付物的分析型 Agent，
  催它"再去执行一轮"是把已经做完的分析重做一遍。`NoActionRetryLimit` 历史上只有 Coding 设（2），
  Repo 现在设 1。
  `OnStepEnd` 已删除：自动检查点的钩子早没了，它唯一的作用是在 `AutoCheckpoint=true` 时
  每步打一条 deprecation 警告。顺带修好一件事 —— `sub_agent_activity` 这条 rollout 记录以前
  挂在那个钩子里，于是**只有"Coding + git checkpoint"那一条路径**会写步数事实，
  其余子 Agent 的 `/resume` 步数只能靠 assistant 轮次猜；现在它在每个有工具调用的 step 都写。
- **Director**：自己的 `director.Planner`（`director/planner_run.go`），额外有熔断器、
  未委派提醒（最多 3 次强制委派 nudges）、`activity.WithIdleTimeout` 包裹的委派超时
  （`[agent] delegate_idle_timeout` / `delegate_total_timeout`）。
- **暂停闸门（A 层，`docs/pause-resume-design.md`）**：两个循环的 step 边界各有一行
  `pause.From(ctx).Wait(ctx)` —— 子 Agent 在 `executor.go` 循环体开头（`stepNumber++` 之前），
  Director 在 `planner_run.go` 循环体开头（`p.state.Step = i + 1` 之前）。闸门挂在任务 ctx 上
  （`internal/pause`，`http.NewTaskContext` 建、`http.Task.Gate()` 读回），嵌套委派自动继承。
  `Wait` 对 nil receiver 安全：没有闸门的调用点（测试、`--prompt`）一行都不用改。
  与取消的区别：闸门**不取消** ctx，所以恢复后是从阻塞点原地继续，而不是"一次失败的委派"
  （`AGENTS.md` §11 坑 17）。暂停期间 `activity` 的 idle 与 total 计时都被冻结
  （`Heartbeat` 只能续 idle —— total 的起点是创建时固定的）。
- **检查点（B 层，`memory/rollout_checkpoint.go` + `agents/checkpoint.go`）**：两个循环各自在
  **终止路径**上落一条 `agent_checkpoint`（子 Agent 在 `executor.go` 的 deferred 终止块里、
  Director 在 `director/planner_rollout.go` 的 `aborted`/`checkpoint`），**不是每步一条** ——
  每步一条会把 200 步的会话写成 200 条坐标，而重放只需要最后一个停点。
  坐标里只存派生不出来的东西（step 游标、指纹、模型/档位、工作区 sha+dirty、`delegation_id`）；
  消息正文、已取回的冷工具、thinklink T&P 块**刻意不存**，因为存了就会与代码漂移
  （见 `rollout_types.go` 的 `AgentCheckpoint` 注释）。写与读共用 `memory.WriteCheckpoint`，
  父子两个写入包不能互相 import，所以这里只留一份实现。
- **重放接缝（B 层，`agents/resume_state.go`）**：`memory.WithDelegationID` 把 Director 那次
  `delegate_*` 的 `tool_call_id` 挂到委派 ctx，子 Agent 的 rollout 文件名带 `_{agent}` 后缀、
  `session_meta` 带 `delegation_id` + `parent_session_id`，父子就此串起来（`ScanDelegationStops` 反查）。
  `ReadRolloutForReplay` 从子文件忠实重建（含 `Reasoning` 与工具全文，`compacted` 记录会替换被压过的前缀），
  再以 `WithResumeState(ctx, *ResumeState)` 交回 `RunAgentLoop`：起点是 `StartStep` 而不是 0，
  **不重写 system/user rollout**（续写会开新文件，靠 delegation_id 认亲），`Materialized` 回填
  `Surface.RestoreMaterialized`（模型不必再 `load_tool` 一次），thinklink 经 `Seed` 复原（否则第三档压缩
  只能拿原始任务输入重建）。走 ctx 而不是给 `ExecutorConfig` 加字段：六个子 Agent 一行都不用改，
  也就不存在"漏改一个 Agent、那条路径的恢复静默失效"。
- **恢复不经过模型（D7）**：`Director.Run` 在 `planner.Run` **之前**处理 `pendingResumption`，
  把结果作为**第二条 tool output（同一个 `call_id`，last-wins）**交回（`planner_run.go` 的
  `PendingResumption` 提交块），零 token、确定性。让模型决定怎么恢复的后果是"同样的停点有时重做、
  有时接着跑"，不可复现比不能恢复更难排查。
- **降级交接报告（C 层，`agents/resume_delegation.go`）**：`assessReplay` 是"不需要真跑一次就能判"的判据
  （无子 rollout / 重读失败 / 无检查点 / 指纹不符 / 消息数不符 / 重放即超预算 / 工作区移动 / browser 委派 /
  max_steps 耗尽 / 超时停的），任一命中就改用**程序生成的**降级报告（不叫 LLM）交回 Director，
  并写一张 `status=degraded` 的便签把降级原因留在板上。报告分三段来自账目本身：已完成、未完成的计划项、下一步建议。

### 6.3 路由（auto-routing）

`internal/llm/router.go` 的 **RoutingEngine** 包裹每个 Agent 的引擎，按任务难度把调用路由到
**fast / balanced / high** 三档模型之一：`Client.BuildRouter` 用模型目录里的 benchmark 分数为每个 provider
建三档引擎；`Classify` 用 fast 模型输出 `{"tier": ...}`（30s 上限，失败回落到该角色的 baseline）；
档位通过 context 传递，在 `RunAgentLoop` 里由 `ResolveTierEngine` 解析。
`[routing] mode = "auto" | "fixed"`，`baselines` 可为 director/coding/repo/chat/devops/browser/meta 指定默认档。

**优先级：TUI 运行时覆盖 > 路由档位 > 配置里的 `[agents.llm.*].use_provider`。** 手动选过一次模型是显式意图，
`RoutingEngine.resolve` 先查覆盖表再查档位；`routeCtx` 据此跳过 `Classify`（省一次 LLM 调用）也不发那条描述未使用档位的
`model_routed`。配置的静态 use_provider 仍让位给档位。
**运行中换模型不需要任务空闲**（`Alt+M` / `:model`）：`RunAgentLoop` 在入口就把 `RoutingEngine` 解成具体引擎，
所以子 Agent 在**下一次委派**换新引擎，Director 每次调用现场解析（下一个 step 即生效），正在飞的那次调用不变。
代价是几个引擎指针现在会被并发替换：`Client.Engine` 只能经 `DefaultEngine()` 读、`Yagent.engine` 只能经
`defaultEngine()/setEngine()` 读写、deepthinking/design 的引擎只能经 `SetEngine()` 换。

> 坑：任何重建 Agent 引擎的地方都必须保留 `*RoutingEngine` 包装，否则 auto-routing 静默失效（commit 0c87c8a）。
> 这条契约由 `internal/app/assembly_test.go:TestAgentEngineAlwaysWrapsWithRoutingEngine` 钉住 ——
> 它只看返回类型，不驱动 `resolve()`（后者要真实 provider，属于 `internal/llm` 的测试范围）。

### 6.4 上下文与知识

- **Blackboard**（`internal/blackboard`，完整设计见 `docs/blackboard-handoff-design.md`）：每次委派的结果写成一张便签
  `~/.yagent/notes/{boardID}/{agent}/{stem}.md`，**正文是交付物全文**（以 `agent_exit` 收尾的 Agent，全文取最后一条 assistant 消息，
  不是那句 reason）。front-matter 带 `Handoff`（`conclusion` / `facts[]{claim,where}` / `open_questions` / `status`），
  schema v2；读 v1 存量便签时自动从 `Summary` 合成并标 `degraded: v1`。
  - **索引是派生态**：`BuildIndex` 每轮从磁盘重扫，注入 Director 的**每一轮**上下文与每次委派的任务前缀；
    它**只进 LLM 调用的 messages**，绝不写进 `ConversationMemory` 或 rollout —— 这是它能扛住上下文压缩的唯一原因
    （压缩动的是会话历史里的 tool 消息，`note:`/`artifact:` 引用以前就是这么蒸发掉的）。
  - `context_refs` 的语义是**强调**（额外内联全文），不是准入：不传也能从索引发现。`agent:` 前缀认 `repo` 与 `repo_agent` 两种写法，
    取不到必须计入 `unresolved` 并在结果头显示。
  - **board 作用域 = session**（`SetBoardScope(sessionID, taskID)`）：同一 TUI tab / 同一 HTTP chat session 内多次任务共享一块板；
    `/resume` 靠 `session_meta.board_id` 重连旧板，存量 rollout 回落 taskID。
  - 三个读回工具：`read_note`（handoff 块 + 分页正文）、`read_artifact`（分页全文）、`list_notes`（整板目录，只给一行结论不含正文）。
  - `handoff.status` 词表 `ok | error | degraded`（+ LLM 可自报的 `partial`）。**只有 `ok`/`partial`/`error`
    出现在 `agent_exit` 的枚举里**：`error` 由委派错误判定、`degraded` 由重放侧按降级判据判定，
    让模型自报"我这份是降级的"等于把判断交给自己看不到判据的一方 —— 所以 `toolspec` golden 没动。
    词表里**刻意没有 `paused`**：被闸门 abandon 的委派零交付物，`writeDelegateNote` 在那条路径上
    **什么都不写**（写了就是一张空正文便签，还会占住每轮重扫的索引位去冒充一条结论），
    "停在第几步"由 rollout 的 `stop_reason` + `agent_checkpoint` 记账，恢复成功后才落便签。
  - 预算与 GC 在 `[blackboard]`。三个阀门靠**键的存在性**判定（不是零值）：`index_max_notes = 0` 关索引、
    `index_line_chars = 0` 不裁剪行、`ttl = "0"` 不清理；`summary_max_runes` 与 `max_notes_per_board` 的 0/负数一律归默认
    （它们没有"关"的语义，放宽就写大数字）。整体关闭仍是 `YAGENT_BLACKBOARD=0`（`blackboard.Enabled()` 是唯一判据，
    写/读/注入/清理四处共用）。
- **Artifact**（`internal/artifact`）：超过预算（默认 500 token）的委派全文落盘 `~/.yagent/data/artifacts`，
  上下文里只留摘要 + ID（摘要优先来自 handoff，缺 handoff 时逐字回落到正文前缀），Director 用 `read_artifact` 分页取回。`YAGENT_ARTIFACT_DISABLE=1` 关闭。
- **Blueprint**（`internal/blueprint`，设计稿 `docs/repo-blueprint-design.md` v4 + §21 实施记录）：仓库级导航知识的
  **唯一有机械新鲜度判据**的一套，也是**唯一**的仓库级持久知识 sink（2026-10-22 起：KV 仓库记忆与
  codeseek 知识注入/沉淀整体删除）。两层内容、一份文档：**派生的不落盘，落盘的不派生**。
  - 派生层（内存，每次现算，本仓实测 6.6ms）= 模块集合 / 文件计数 / `go/parser` 验过的模块级边 / 入口与
    manifest 事实 / 每片一个结构指纹。**结构层不落盘 ⇒ 模型没有任何途径写坏它**（§3.4）。
  - 持久层 = `~/.yagent/data/blueprint/{projectID}/blueprint.md`（front-matter 是 JSON，与便签同形）
    + `revisions/`（fold 前整档，滚动 5 份 —— "删错了"要有 Undo）。原子写走 `util.AtomicWrite`（I21）。
  - 三条新鲜度判据全部机械：**(a) 结构指纹**不符 → 该片整节标 `stale` 但**散文保留**（清空会把可用信息
    变成不可用）；**(b) 出处锚点**三态（`path` / `path:line` 越界 / `path:symbol` 词边界匹配 / 非路径形态
    = `unverifiable` 不谎称验过）；**(c) 孤儿**：可见集合 == 派生集合，改名/删除的旧片既渲染不出来也
    读不到，超过 90 天才从文件里摘除。
  - **stale 是粘滞的**：没有后台进程会补写，一片过期之后只有下一次 Repo 真的覆盖它才修 —— 所以索引行写成
    行动理由（`N modules annotated but stale (delegate_repo to refresh)`），把"值不值得为新鲜度花一次委派"
    留给有任务上下文的一方。
  - 读通道 `read_blueprint`；写通道是 `agent_exit` 的 **`blueprint[]`**（`section/module/field/claim/where/supersedes`，
    一次最多 8 条）。落盘只发生在 **Repo 完成接缝**（`repo.go` 里 `FinalizeResultFull` 之后的那一步）；
    **Director 侧不需要知道这个参数存在**，Coding prompt 明确写"你不写蓝图"。
    提交采集在 `result.go`（与 `collectHandoff` 并排，executor 不认识蓝图），落在 `AgentResult.Blueprint`
    而**不是** `Handoff` —— 同一些句子不该同时是便签头和蓝图正文（§2 R4 的病）。
    fold 的**账目**（提交几条 / 写入几条 / 拒收原因逐条归堆 / 证据覆盖几片）走 `AgentResult.BlueprintAccount`，
    由 `FormatForDirector` 与便签正文的 `> [blueprint]` 尾注各渲染一次（同一个字符串，两处出口），
    **不进 `conclusion`** —— 索引每轮重扫，程序的账混进去就是替模型下结论（坑 43）。
  - fold 是**确定性纯函数**：短 ID 由内容派生 ⇒ 幂等 + 乱序输入产出同一份字节；段内按 `(where, claim)` 稳定排序
    （每次 fold 只动那一行，否则人类 review 与 provider 前缀缓存同时失效）；同 `module+field` 非空才覆盖；
    `supersedes` 指认错 ID 时记账但不失败。**正文逐字没变就不算一次 fold**（`NoOp`），否则 I20 的"字节不变"
    与 I18 的"没更新 ≠ 更新失败"会打架。
  - 五道校验各有一条能读出来的拒收账（I18）；证据集 = 本次 run 真打开过的文件所属模块，
    **入参侧 ∪ 出参侧**（只认入参会误伤"检索到 8 个、读了 3 个"那一轮）。
    哪些工具的结果算"读过"是 `toolspec.Spec.ResultPaths`（`json`/`rg`/`bare`）—— `list_dir` 的裸文件名必须排除。
    I14 的散文路径归属只问"它到底指哪儿 / 仓库里有没有它"，**判不了就弃权不拒收**（一条拒收丢整条 claim，
    全丢就是 NoOp ⇒ 文件永不出现，见坑 42）；写短的前缀与裸文件名都算，存在性交给 `where` 锚点。
  - 注入只有**形状②**（模块 + 边，`index_max_tokens` 硬顶、截断写进 header），三处 system prompt 由
    `[blueprint] inject_into` 决定；`purpose` 这类不可验证的散文永不进常驻。委派前缀会带上目标模块的
    现有散文（判据是"派生集合里的片路径逐字出现在任务里"，不是猜路径 —— §6.3 否决的是后者）。
  - 启用判据只有 `config.BlueprintEnabled()`（I8，吞下 `enable` 与 `YAGENT_BLUEPRINT=0`）；
    `internal/blueprint` 自己不读环境变量，也不 import `config`/`gitinfo`/`tokenutil` ——
    仓库读数与 token 计数器由装配层做成参数传进来。
  - **仍然没做**：`export_path` 的导出层（R8）、`/blueprint` 的 TUI/CLI 入口、
    以及 §8 纪律 1 里"每种拒收再记一条 rollout `event_msg`"（钩子那里没有 writer）。
    （R7 已了结，但方式不是"收编"而是把 KV 仓库记忆整套删掉——见本文件开头的 2026-10-22 记录。）
- **Skills**（`internal/skills`）：用户写的可复用流程。磁盘约定两种都认 —— `<dir>/<name>/SKILL.md`
  （目录式，胜）与 `<dir>/<name>.md`（扁平，兼容）；front-matter **JSON 优先**，容错扁平
  `key: value` 子集，都没有时沿用"正文首行去 `#`"当描述。作用域是**合并**：user → project →
  `[skills] dirs`，后者覆盖前者，覆盖与拒收都进 `LoadAccount` 的账（启动日志与 `/skills` 面板同源）。
  目录（`## Skills`，一行一个 name: description，截断标记写进正文）进 system prompt 的稳定前缀；
  正文由 `use_skill` 按需取回（**Hot** 工具，没有技能时仍在场并解释"放哪儿"）。`allowed-tools`
  只是回吐给模型看的咨询信息，**不强制**。`Load` 永不返回 nil 注册表 —— 目录读不出只记一条 Rejected。
- **Thinklink**（`internal/thinklink`）：append-only 的思维日志（用户输入 + T&P 块），是 ultimate 压缩重建上下文的原料。

### 6.5 三档上下文压缩（`internal/compression`）

阈值 = `min(模型目录收录的 limit.context × 0.75, [enhanced_commander] context_compression_threshold)` ——
两个来源都是上限，配置没写（或写 0/负数）就纯按目录推导，都没值才是 120000；
**provider 的 `context_window` 不参与**（见 §11 坑 3）。每步检查，`enhanced_commander.enable_context_compression` 总开关。
调试例外：`YAGENT_COMPRESS_THRESHOLD` 一旦设了就**直接当阈值**，压过目录与配置。
判定口径是 `compression.Prefix`：上一步 provider 报的 `Usage.TotalInputTokens` 打底，加上此后新增消息的
本地估算（本地估算从不计工具声明，且每步扫两遍全量 tiktoken）。计量缺失、还没跑过一步、或列表被
第二/三档整段重写时必须作废 —— 作废后逐字回落到全量本地估算，就是改动前的行为。
判定只有一处实现：`compression.OverThreshold(messages, prefix, threshold)`，两个循环各两处调用点，
不要再抄 `EstimateMessagesTokens(messages) > threshold`。Director 侧的三档整段在
`agents/director/planner_compress.go`（从 `planner_run.go` 搬出，该文件行数上限同步下调）。
取小的代价是压缩会在更低的位置触发，所以第一档是**增量记账**：达标判定每截一条只重算那一条，
不再对整份消息跑 tiktoken（旧写法 200 条工具结果的会话要 9.3s）；`stats` 一律保持本地口径，
否则 TUI ⧉ 面板的百分比两个基数混在一起。完整规格与实测数据（含"为什么压缩不是延迟主因"）
见 `docs/compression-trigger-design.md`。

1. **Rule**：`TruncateToolResultsToBudget` — 按优先级先截低价值工具结果（`deepthinking`/`design` 结果受保护），保留最近 `tool_result_keep_tokens`（默认 200）。
2. **Emergency**：抽取 T&P 块进 thinklink，最近 3 条保留原文，其余 LLM 摘要，记忆整体重写为 `[system, one user]`。
3. **Ultimate**：仍超阈值时，用 thinklink 日志重建上下文（保留最近 N 个 T&P，N 由 `ultimate_compression_keep_plans` 决定，0=全部），最后硬截断到预算。

三档都会发 `context_*_compressed` 事件并写 `compacted` rollout 记录。

> `enable_ultimate_compression` **现在真的能关**：`validate()` 不再"见 false 就写回 true"。
> 因为它的缺省语义是"开"而 Go 零值是 false，区分靠的是 **TOML 键的存在性**
> （`config.ultimateCompressionKeyDefined`，由 `md.IsDefined` 得出）：没写 → 开；显式写 false → 真的关。
> 与 `[blackboard]` 三个阀门、`[git_checkpoint]` 段级判定是同一套办法（§11 坑 13）。
> 现状由 `internal/config/validate_test.go:TestLoadFromFile_UltimateCompressionRespectsFalse` 钉住。
>
> **压缩只有一个开关**：`enable_context_compression`。以前它还要与 `[enhanced_commander] enable`
> 同时为真才生效（5 个 AND 点：`coding.go` / `repo.go` / `devops.go` / `browser_agent.go` / `director.go`），
> 而模板把两个都写成 false —— 那个 `enable` 键已删除，旧配置里残留它会走 `removedKeyHints` 点名告警。
>
> **子 Agent 的第三档需要两个条件同时成立**：`ExecutorConfig.EnableUltimateCompression`（来自
> `enable_ultimate_compression`）与非空的 `UltimateThinkLink`，与 Director Planner 同语义
> （`executor.go` 的 T3 分支）。Browser/Repo/DevOps 的 store 在 `Run()` 内按委派创建——
> `RebuildPrompt` 会保留全部用户输入，放构造期会让历次委派的任务串进同一次重建。
> **Coding 曾是唯一一个把 store 挂在单例上的**（`coding.go` 构造期 `thinkLink: thinklink.NewStore(0)`），
> 现在由 `CodingAgent.SpawnForDelegation` 每次委派换一份新 store（`agents/spawn.go`，C4/D2）：
> 挂在单例上不仅会串味，委派一旦并行化就直接是数据竞态。
> 其余四家今天没有可变自身状态（五家 `Run()` 逐个核过：一处都不写 `a.<field>`），
> 克隆看起来是空动作，仍统一做，是为了把"委派从不碰共享实例"变成不需要记住的不变量——
> 以后谁往结构体上加可变字段，默认就是每委派一份。嵌套调用（Repo→`delegate_devops`、
> Coding→`delegate_browser`）也各自 `Spawn` 一次，否则拷来的仍是同一个共享单例。
> `ultimate_compression_keep_plans` 现在也真的到达 Coding（`coding.go` 曾硬编码 0，即"永远全部保留"）。
> token 估算（`EstimateMessagesTokens`）**包含 `Reasoning`**：thinking/reasoning_content 会被回灌给
> provider，漏算会让压缩触发时机系统性偏晚。

### 6.6 事件总线（`internal/messaging`）

`Event{ID,Type,Source,Target,Content,Priority,Timestamp,Deadline,TraceID,Metadata,Seq,DelegationID}` →
`MessageDispatcher`（每个 consumer 一条 1000 容量队列 + 独立 goroutine）。
普通事件队列满则**丢弃并限流告警**；`user_help_needed` / `user_help_response` 走 **critical 旁路队列**（永不丢弃，FIFO，满则丢最旧）。
Consumer 有两种：旧版 `MessageConsumer`（广播）与新版 `Consumer{Types()}`（过滤订阅）。

**两个并行必需的坐标**（C5）：
`Seq` 由 dispatcher 在 `Publish` 里盖（每个 dispatcher 一条单调序列），所以"界面少了字"第一次
变成可回答的问题——消费者看见 7→9 就知道少了一条。`DelegationID` 是产生这条事件的那一次委派
（`delegate_*` 的 tool_call_id）：**`Source` 在并行委派下不再唯一**，两个 Coding 同时输出时
只按名字分槽会把两股流粘成一段文字。
它由 `RunAgentLoop` 入口**一次**绑定（`Publisher.ForDelegation(memory.DelegationIDFrom(ctx))`），
不是在 10 个发布点各传一遍——漏一处的事件不报错，只会留下没人能解释的现象。
绑定用能力断言而非 `EventBus` 接口方法：没绑定的正确后果是少一个坐标，不该逼 5 个测试 mock 各写一个假方法。

满载丢弃必须**可被知道**：`Dropped()` 是总数，任务收尾调 `WarnDropped(scope)`
（HTTP 任务 / 两个 TUI 闭包 / WS 会话各自报告）。另外 6 个建调度器的地方原本都手写
`NewMessageDispatcher(100)`，把文档承诺的"默认 1000"压成了十分之一——现在统一走
`NewDefaultMessageDispatcher()`，容量只有一处定义。

Web 侧坐标放在 `realtime` 信封的 `Data` map 里（`seq` / `delegation_id`），**不动
`protocol.AgentEventEnvelope`**：那是生成物，AGENTS.md 下面那条"重跑生成器会冲掉手工修复"的
警告仍然成立，而加字段不需要经过它。

当前总线上的事件类型（约 20 个，SSOT 是 `protocol/agent-events.yaml`，由 `scripts/protoc-gen-yagent` 生成 Go/TS/JSON-Schema）：

`ai_stream_start` · `ai_chunk` · `ai_stream_end` · `ai_response` · `thinking` ·
`llm_call_start` · `llm_call_end` · `model_info` · `model_routed` ·
`tool_call_start` · `tool_call_result` · `user_help_needed` · `user_help_response` ·
`context_loaded` · `commit_context_loaded` · `context_compressed` · `context_emergency_compressed` ·
`context_ultimate_compressed` · `thinklink_entry` · `conversation_error` / `conversation_result` ·
`task_paused` / `task_resumed`（暂停闸门，由 `TaskManager.PauseTask/ContinueTask` 经 melody 广播）

> **新增事件类型要改 `protocol/agent-events.yaml` 再重新生成**，不要手改 `internal/protocol/agent_events.go`（文件头写着 DO NOT EDIT）。
> **但"重新生成"今天不能无脑跑**：`scripts/protoc-gen-yagent` 的输出与仓库里两份 Go 产物已经漂移
> （`protocol/go/agent-events.go` 的 `ParseEventPayload` 被手工修成走 `json.RawMessage`，生成器还在走
> `interface{}` 类型断言；`internal/protocol/agent_events.go` 与它差约 445 行）。整体重跑会把那个手工修复
> 冲掉。当前可行做法是：改 yaml → 只覆盖 `ts/agent-events.ts`、`ts/render-mapping.ts`、
> `agent-events.schema.json`（这三个与生成器输出逐字节相同）→ 把生成器**新增的那几块**逐字粘进两份 Go 产物。
> 想恢复"一条命令生成"，先把生成器追上现状。详见 `docs/pause-resume-design.md` §15.3 第 7 条。
> 生成器与 schema 目前落后于实际总线（`model_routed`、`context_*_compressed`、`thinklink_entry` 等未入 schema）。

### 6.7 任务生命周期与持久化

```
main.go → Yagent.Init(engine, repoPath) → 装配 MCP/工具引擎/守卫/子 Agent/Director（sync.Once）
        → TUI 建 Task(uuid + ConversationMemory) 并每轮新建 Dispatcher(100) + tuiEventConsumer
        → NewTaskRequest(...).WithProjectDir/WithUserMessage/WithMemory/WithResumed
        → ProcessCodingTaskWithCallback / ProcessConversation → Director.Run（多轮复用同一 Memory）
```

| 数据 | 路径 | 写入方式 |
|------|------|----------|
| 任务记忆 | `~/.yagent/tasks/{taskID}.jsonl` | **5 秒防抖**、增量追加（条数变少则全量重写） |
| **Rollout（断点续跑的事实来源）** | `~/.yagent/data/rollout/{projectID}/{ts}_{session}[_{agent}].jsonl` | **入队即返回、攒批落盘**（C2：每个 writer 一条写循环，32 条或 20ms 刷一次，`Close` 会 join），类型含 `session_meta/turn_context/response_item/event_msg/compacted/agent_checkpoint`（`world_state` 已于 2026-10-14 删除）；`session_meta` 带 git 身份 + `delegation_id` + `parent_session_id`，终止事件带 `stop_reason`（词表在 `memory/rollout_types.go`：`paused/cancelled/idle_timeout/total_timeout/max_steps/llm_error/circuit_breaker_open/panic/plain_text/agent_exit/unaccounted`）。子 Agent 的文件名后缀 `_{agent}` 就是"这次委派是哪一个"的凭据。**读回自己写过的文件之前必须排干**：`WriteCheckpoint` 走 `(*RolloutWriter).FinalizeCheckpoint`（先 Flush 再按整份文件算指纹，写完再 Flush 让坐标本身落地），自己拼 `Flush + FinalizeCheckpoint(path)` 的调用点一定会拿到"少几条"的指纹 —— 它不报错，只是把能重放的会话判成损坏 |
| Blackboard 便签 | `~/.yagent/notes/{boardID}/{agent}/*.md` | 原子写，`YAGENT_NOTES_ROOT` 可覆盖 |
| Artifact | `~/.yagent/data/artifacts/{id}` | 原子写，ID `^\d{8}-\d{6}-[0-9a-f]{8}$` |
| **仓库蓝图** | `~/.yagent/data/blueprint/{projectID}/blueprint.md` + `revisions/*.md` | **fold 前整档先落 `revisions/`（滚动 5）**；tmp+rename 走 `util.AtomicWrite`（0700/0600）；多进程用**乐观 CAS 重放**而不是锁文件；根护栏拒绝 `$HOME`/`/`/无 manifest 且非 git，存储根落进项目内也拒绝。**取代了原先的 `~/.yagent/data/shared_memory/`（2026-10-22 删除，老目录不迁移、不再写）** |
| 授权状态 | `~/.yagent/settings.json` → `authorized_projects` | 持久 |
| 浏览器工作区 | `~/.yagent/data/browser/{delegation}/.browser-control/` | 由 `cmd.Dir` 决定落点（上游状态路径是 CWD 相对）；含 current-tab、chrome.pid、traces、截图；`[browser] workspace_ttl` 清理 |
| 日志 | `~/.yagent/logs/`（`app-`、`llm-`、`llm-error-`、`tool-`、`delegate-`，按任务分子目录） | 追加 |

**`/resume` 有两层落点**，共用同一份账本：

- **会话级**（旧有的）：列出 rollout 会话 → `ReadRolloutMemory` 重建 Memory → 恢复 thinklink 条目，
  下一轮以 `resumed=true` 强制重注入项目上下文。工具调用配对按 **last-wins** 修复（同一 `call_id`
  出现第二条 output 时原位覆盖并计入 `SupersededOutputs`）—— 这与 LLM 侧 `llm.repairToolCallPairs`
  的语义一致，两边不一致的表现是"进程内喂给模型的和 /resume 后喂给模型的不是同一份"
  （`TestToolPairingNormalizersAgree` 钉住）。终止事件带 `stop_reason`，
  `ScanResumeCandidates` 据此判定能否原样继续：**默认拒绝**，只有 `paused` 与 `cancelled`
  可重放（前者必然停在 step 边界，后者可能停在工具中间）；不可重放的候选照样返回并说明原因，
  不静默过滤。
- **委派级**（B 层，`docs/pause-resume-design.md` §6）：会话恢复后 `app.ResumableDelegation(路径)`
  反问"这次会话是不是停在一次 `delegate_*` 上"，能就给恢复对话框加第三个选项
  "从 <agent> step <n> 继续"；TUI 只交出**那份 Director rollout 的路径**（`internal/tui` 不许依赖
  `internal/agents`，落点由 `app.resolveResumption` 在提交时算）。执行侧见 §6.2 的重放接缝与降级报告。

### 6.8 TUI（`internal/tui`，Bubble Tea **v2**）

- **状态**：`tui_model.go`（logEntry + 渲染缓存 + keymap + `eventCh(1000)`）；更新逻辑按 `tui_update_*.go` 拆分；
  渲染按 `tui_view.go` / `tui_render.go` / `render.go` / `tui_chrome.go` / `tui_tabs.go` / `tui_dashboard.go`。
  token 消耗展示是**单一渲染器**：`token_panel.go` 的 `buildTokenPanel` 同时产出侧栏 dashboard、底部面板与 `alt+t` 折叠行，
  所有口径只在 `(*model).tokenSnapshot()` 里算一次（输入量一律用 provider 归一化的 `TotalInputTokens` ——
  OpenAI 的 `prompt_tokens` 含缓存、Anthropic 的 `input_tokens` 不含，混用会让同一标签出两个数）。
  不要在任何表面里另取计数器直接格式化；完整规格见 `docs/token-dashboard-design.md`。
- **⧉ 压缩段**：`compress_panel.go` 的 `buildCompressBlock` 排在侧栏 Activity **之下**（底部面板只给 1 行摘要，折叠态不给行），
  数字只在 `(*model).compressSnapshot()` 里算一次；`pct = Σsaved / max(original_tokens)` 且**显示层封顶 100%**
  —— 三档串行嵌套，用 Σoriginal 当分母会重复计算同一批 token；而压缩是**每个 step 独立触发**的，
  多轮任务的 Σsaved 会合法地超过任何单次 original。没触发过压缩时区块**不消失**，而是退到常驻的
  2 行空闲形态（`⧉ COMPRESS 0 (0%) T0`）。完整规格见 `docs/token-compress-panel-design.md`。
- **叠加模式**：timeline 全屏、thinklink 全屏、history、resume、dialog 栈、主页面——按此优先级渲染。
  每层都必须正常消费 `taskEventMsg` 并重新武装 `listenForEvents`（该 cmd 是一次性的，事件没有重放队列）。
  叠加层还必须把 `taskCompleteMsg` 透传给 `handleTaskCompleteMsg`：它是 `taskRunning` 唯一的清零点，
  吞掉的表现是界面永久卡在"运行中"且最终答案不落地（dialog 守卫曾经就这么吞过，弹窗打开时任务一完成就锁死）。
- **thinklink 全屏**：条目来自 `thinklink_entry` 事件（payload 的 `agent` 字段 = 发布方 Agent 名），
  `f` 在 All ↔ 各 Agent 间过滤（`RebuildPrompt` 之外，一次委派里 Director/Coding/Browser 的块会混排，
  Browser 一轮可贡献上百条）。`/resume` 恢复的旧 rollout 没有 Agent 标记，只在 All 视图出现。
- **Dialog**：`components.DialogStack`（Push/Pop/ReplaceTop/Overlay），10 个对话框；
  弹窗打开时全局守卫只放行 KeyMsg/taskEventMsg/taskCompleteMsg/tickMsg，新增消息类型必须显式放行（`tea.PasteMsg` 的坑已踩过）。
- **模态编辑**：Esc→NORMAL、`i`→INSERT、`:` 命令行、`/` 真搜索、textarea 用 `SetVirtualCursor(true)`、支持 `10j` 数字前缀。
- **快捷键提示**只出现在 tab 行右侧与各面板底边框，**状态栏不含任何键帽**。
- **列表/表单表面的共用原语在 `common/list_metrics.go`**（`PanelWidths` / `ListBudget` /
  `WindowRange` / `MoreLine` / `AllocateColumns` / `Ellipsize` / `PadLeft`）：`components` 的十个对话框
  与引导向导共用同一份，两边都只留别名。**不要在任何一边再写一份** —— 同形两份实现只有一边会漂走
  （§11 坑 18）。输入框视觉令牌同理：`applyTextareaTokens`（`tui_model.go`）是主输入框与向导字段的唯一来源。
- **设计语言**：圆角手绘边框（`tui_chrome.go` 的 `╭╮╰╯─▌`），配色令牌集中在
  `common/styles.go`（`Styles` 与 `ColorTokens`，**只有一套深色**；终端背景只切 glamour
  markdown 渲染，不切这些令牌）、`styles.go`（工具背景 16 色哈希、Agent 前景色）、
  `common/grad.go`（渐变）。强调色：`141`（lavender，活动 tab / thinklink / LLM 调用）、`62`（INSERT、滚动条、面板边框）、
  `214`（命令）、`39`（搜索）、`76`（运行中）、`235/250`（键帽）。
- **i18n**：`i18n.go` 一张 ~120 字段翻译表，zh/en，默认英文，仅通过 TUI 内切换（无配置项）。
- **测试约定**：断言**精确单元格宽度**而非子串；宽度矩阵 `{30..240}`、终端尺寸矩阵 `{20..240}`；
  共享 fixture `newTestModel()` / `chromeTestModel(t,w,h)`；主要闸门是 `tui_chrome_test.go`、
  `tui_paste_test.go`、`components/user_help_dialog_render_test.go`、`tui_resume_restore_test.go`。

### 6.9 HTTP

`POST /api/start_task`、`GET /api/task_status`、`GET|DELETE /api/memory`、`GET /api/memory/:type`、
`GET /api/history`、`POST /api/load_task`、`POST /api/cancel_task`、
`POST /api/pause_task`、`POST /api/continue_task`、`GET /ws`。
任务状态四档不变，另加 **`paused`**（`TaskStatusPaused`）：暂停闸门不产生 task_complete，
所以旧四档里没有一个能表达"停住但可继续"。`http.Task.Gate()` 从 ctx 读回闸门，
不在结构体里另存一份（两份状态 = 一处忘了换 = `/pause` 停错对象且不报错）。
WebSocket 用 melody，每个任务一个 Dispatcher + `WebSocketConsumer`，向所有连接广播 `realtime` envelope（无按会话过滤）。

---

## 7. 配置系统

**解析顺序**：`--config` 指定路径（原样使用，**绝不自动创建**）→ `~/.yagent/config/config.toml`（不存在则用内嵌模板生成，权限 0600）。`./config/config.toml` **不是第二级查找**：只有 `os.UserHomeDir()` 失败时才会走到 CWD 相对路径，仓库里那份是注释参考。模板由 `config.EnsureConfigExists` 生成并返回 `created`，这个信号是首启判据之一（见下面的"引导"段）。

**顶层表**：`[global.llm]`、`[agents.llm]`、`[tools.llm]`（含 `[tools.deferred]`）、`[agent]`、`[llm]`、`[routing]`、
`[keywords]`、`[git_checkpoint]`、`[codeseek]`、`[browser]`、`[blackboard]`、`[resume]`、`[enhanced_commander]`、`[tui]`，
`[blueprint]`（仓库蓝图：`enable` / `inject_into` / `index_max_tokens` /
`prose_budget_tokens` / `export_path`；启用判据 `config.BlueprintEnabled()` 同时吞下环境变量
`YAGENT_BLUEPRINT=0` 与 `enable` 键。**五个键里四个已有真实消费者**（`enable`→`app.buildBlueprint`
一处读取、五处共问；`index_max_tokens`→形状②硬顶；`inject_into`→三处 system prompt；
`prose_budget_tokens`→`read_blueprint` 与委派前缀），**`export_path` 仍无消费者**（导出层是 R8，
`docs/config-guide.md` 里先不要写它）。默认值与 `internal/blueprint` 的渲染兜底常数由
`internal/config/blueprint_defaults_test.go` 钉成同源（同 §11 坑 18 那类"两份同形实现"），
以及顶层 `task_timeout`。**`[app]` 与 `[codeseek.knowledge]` 已整体删除**（前者唯一的键只进 `slog.Debug`，后者五个键全部不可达 —— `enabled` 会被写回 true，其余四个读点在注入器的 early return 之后；那套注入器连同 `consolidate_knowledge`/`prune_history` 已在 2026-10-22 整体删除）。

> 工具声明面的 schema 条数基线：`read_blueprint` 把它从 50 → 51，2026-10-22 随知识机制删除回到 49，
> 2026-10-23 技能读通道 `use_skill` 加到 **50**；改动必须显式改数字
> （`internal/toolspec/registry_test.go:TestSchemasLoadAndDescribeEveryEntry`）。
> MCP 外部工具**不计入**这个基线：它们走 `Deps.ExternalSpecs` 的动态通道。

**运行时写回**：`config/persist.go` 只有一个内核 `SetKeyInSection(path, section, key, renderedValue)`
（段缺失→末尾追加，键缺失→段内插入，存在→就地替换且保留行内注释与等号两侧空白；永不整文件重编码，
那会蒸发用户注释），另有 `RemoveKeyInSection` / `RemoveSection`。
`PersistUseProvider` / `PersistRoutingMode` / `PersistLLMTimeout` 是它的三个薄封装。
provider 块的写/删在 `config/setup.go`（`WriteProvider` / `DeleteProvider` / `ProviderRefs` / `WriteFirstRun`），
被任何 `use_provider`/`fallback_providers` 引用的 provider 拒绝删除 —— 留下一个"加载即失败"的文件比拒绝更糟。

**首次使用引导**（`docs/config-trim-onboarding-design.md`）：手写 config.toml 不友好，而模板自带
`api_key = "your-key"`、校验只查非空，于是全新机器"启动成功"、第一次任务才炸。现在：
- 入口三个 —— `yagent setup` 子命令（不装配 Agent/MCP，配置坏了也能跑）、TUI `/setup`
  （任务运行中或弹窗打开时拒绝，避免抢键盘；写完要重启才对新引擎生效）、以及 `main.go` 在
  `config.NeedsSetup` 判出 `SetupFreshInstall / SetupPlaceholderKey / SetupBroken` 时自动进入。
- 判据全部从配置本身推导，**没有"看过一次就不再问"的标记文件**（那是会漂移的第二份事实）。
- 向导内容是 provider 全生命周期 + 一次真实连通性探测 + 五个首选项（`lang` / yolo 档位 /
  `[routing] mode` / `[browser] enabled` / `[llm] timeout`）。排队到摘要屏才落盘，之前放弃 = 文件没动过。
- 表单**必填只有 4 格**（name / api_base_url / 密钥 / model），其余 4 格收在 `ctrl+o` 才展开的可选区；
  `ctrl+p` 是端点预设（**只填空字段**，`setup_presets.go` 那份手写的 URL 表不是权威，探测屏才是）；
  `f2` 把 `api_key` 与 `api_key_env` 合并成一行两态（`config.WriteProvider` 本来就删另一行，
  分成两格只会留下"两个都填了谁赢"这条只能靠 hint 说明的隐式规则）。
  `model` 命中内嵌目录时自动回填 `context_window`，但**只在该格为空或仍是上一次自动填的那个值**时回填——
  手输过的数不会被覆盖。校验集合与必填集合是同一份（`TestWizardRequiredGroupMatchesValidator` 双向钉），
  其中 `api_base_url` 是本轮补的：`WriteProvider` 只要求"URL 与密钥不全空"，而 `validate()` 要求每个
  provider 都有 URL —— 旧表单会放行出一份"下次启动直接加载失败"的文件。
- 进度是**四段阶段轨**（PROVIDER / DEFAULT / CHOICES / REVIEW），判据来自账本（op 队列 + 文件现状），
  **不是"走到第几屏"**：真实路径 `providers→form→probe→providers→default` 里游标会来回跳，
  而"已经配完了"这个事实不会倒退。旧的 `4/7` 编号因此是假信息（见 §11 坑 30）。
- 摘要屏是人话 diff（`+ 新增 / − 删除 / → 改默认 / ~ 改值 old → new / = 不变`），
  每条 op 的 `note` 原文作为次级 dim 行**保留**——`applyOps` 失败时报的就是它；写失败还会带上
  "已写入 N/M 条"（它不是事务，§11 坑 28）。
- 实现是 `internal/tui/setup*.go` 的状态机 `setupWizard`（`setup.go` 状态与落盘队列、
  `setup_keys.go` 按键与表单行规格、`setup_render.go` 外壳、`setup_screens.go` 七屏正文、
  `setup_presets.go` 端点预设、`setup_embed.go` 主 TUI 宿主），既能独立跑（`RunSetup`）也能当主 TUI 的全屏模式；
  View/Update 优先级最高，且必须把 `taskEventMsg` / `taskCompleteMsg` 透传给原 handler。
- **仍未兑现**：写完不会让运行中的进程生效（原方案 §6.2 要求复用 `SwitchProvider`/`SetLLMTimeout`/
  `SetRoutingMode`），所以 `/setup` 之后要重启 yagent。
- 非 TTY 的失败路径改成 stderr 三行人话（哪一项坏了 / 文件在哪 / `run yagent setup`）+ 退出码 2，
  不再 `slog.Error` 进日志文件后静默 `os.Exit(1)`。

**已删除的键**：残留它们的旧配置会命中 `config.go` 的 `removedKeyHints` / `removedProviderKeyHints`，
启动时 `slog.Warn` **逐条点名并给出替代写法**（`removed_keys_test.go` 要求登记表与夹具互为镜像，
所以这条承诺本身有测试）。

**三级 Provider 覆盖（仍存在）**：`ResolveProvider(agent, tool)` = 工具指定 → `tools.llm.use_provider` →
Agent 指定 → `agents.llm.use_provider` → `global.llm.use_provider`。Agent 名现在有**七个**：
`director / coding / repo / chat / devops / browser / meta`（`[agents.llm.browser]` 以前是 undecoded 键，
被静默忽略）。

```toml
[global.llm]
use_provider = "bailian"
[global.llm.providers.bailian]
model = "qwen3-coder"          # 必填
api_base_url = "https://..."    # 与 api_key 一样对每个 provider 必填（无 Bedrock 豁免，见 §11 坑 14）
api_key_env = "BAILIAN_KEY"     # 或者由环境变量供给；变量没设 → 生效 provider 启动即失败并点名
context_window = 262144         # 只影响 TUI 上下文进度条；压缩阈值走模型目录（见 §11 坑 3）
temperature = 0.3
max_tokens = 28000
reasoning_effort = "high"       # openai 原样透传(+thinking 扩展字段)；anthropic 映射为 budget 1024/8192/24576/32000
api_format = "anthropic"        # 默认 openai
fallback_providers = ["deepseek", { provider = "mimo", weight = 2 }]

[agents.llm]  coding.use_provider = "deepseek"
[tools.llm]   micro_agent.use_provider = "local"   # 另有 thinking / deepthinking / design

[agent]
director_max_steps = 100  coding_max_steps = 150  repo_max_steps = 50
chat_max_steps = 50  devops_max_steps = 50  browser_max_steps = 200
delegate_idle_timeout = "60s"  delegate_total_timeout = "20m"
max_parallel_readonly_tools = 4
max_parallel_delegations = 2         # 一次 Director 运行里同时跑几个子 Agent
lang = "Chinese"

[llm]   timeout / max_retries(5) / step_retries / enable_fallback / fallback_max_retries(2)
  max_in_flight_requests(8)          # 全进程同时在飞的 provider 调用上限
  step_retries 现在下发到**六个子 Agent**（以前 ExecutorConfig.StepRetries 全仓无写入点，只有 Director 和 Meta 生效）
  circuit_breaker_threshold = 0 意为**不启用**（仓库模板与 defaults.go 都是 0）；
  只有 > 0 才创建熔断器，此时 reset_timeout 缺省补 60s
[routing] mode = "auto"  [routing.baselines]  coding = "high"  repo = "balanced"
[enhanced_commander] enable_context_compression（唯一开关）
  context_compression_threshold / tool_result_keep_tokens(200) / enable_ultimate_compression / ultimate_compression_keep_plans
[browser] enabled / binary_path / chrome_path / mode(headless|headful|attach) / cdp_url / cdp_port_base(9333)
  cdp_timeout(65s) / command_timeout(75s) / idle_ttl(15m) / max_chrome(1) / workspace_root / workspace_ttl(72h)
  max_output_bytes(24000) / dialog_policy(accept) / allowed_domains / denied_domains / profile_dir
[git_checkpoint] enabled / agent_branch_prefix / checkpoint_tag_prefix / stash_dirty_worktree / squash_on_exit
  auto_merge_on_exit / cleanup_agent_branch / cleanup_checkpoint_tags / generate_commit_message
```

> `[browser]` **整表缺省**（老配置没有这一段，或只剩旧浏览器栈的失效键 `headless` / `browser_path`）时，
> 按 `config.DefaultBrowserConfig()` 填默认值。判据是段内是否存在**至少一个被新 schema 认识的键**
> （`config.go` 的 `browserSectionConfigured`），不能用 `enabled` 的零值判断——它缺省就是 false，
> 会把新能力静默关掉；失效键会被 `slog.Warn` 点名提示改写。

> **并发那三个旋钮的取值约定是两套，而且各有理由**：
> `max_parallel_readonly_tools` 与 `max_parallel_delegations` 用 **1 = 严格串行**、≤0 = 取默认（4 / 2）、>8 钳到 8；
> 而 `[llm] max_in_flight_requests` 的 **0 有语义（不限流）**，所以它必须靠**键的存在性**
> （`md.IsDefined("llm", "max_in_flight_requests")`）区分"没写 → 8"与"写了 0 → 不限"（坑 26）。
> 别把后者的"串行"写成 0 —— 那两个 0 含义不同，而邻居那套（1 = 串行）已经是既有约定。
> 急停开关是环境变量 `YAGENT_CONCURRENCY=0`，唯一判据 `config.ConcurrencyEnabled()`，
> **写在两个 `Normalize*` 函数内部**而不是调用点：两个循环都只经这两个函数取值，
> 判据写两处就会有一处忘，而漏掉那处的表现只是"还是并发"。只认精确的 `"0"`。
> 在飞闸门的排队发生在 `llm.timeout` **之前**（等一个格子不是"provider 慢"），
> 重试与流式在格子内完成（一次逻辑请求只占一格）。

```toml
[blackboard]
index_max_notes = 12          # 索引条数上限；**0 = 关掉索引注入**，负数/没写 = 默认 12
index_line_chars = 160        # 索引单行 rune 预算；**显式 0 = 不裁剪行**，负数/没写 = 默认
summary_max_runes = 1200      # Director 侧 handoff 摘要预算；<=0 = 默认
ttl = "168h"                  # board 目录多久没动就整板清理；"0" 或 0 = 不清理
max_notes_per_board = 200     # 每 board 保留的最新便签数；0/负数 = 默认（放宽请写大数字）
```

> 与 `[browser]` 同构的整表缺省判定在 `blackboardSectionDefined`；但本表有**三个键的 0 是有意义的**
> （关索引 / 不裁剪行 / 不清理），所以除段级判定外还要看**单个键的存在性**（`md.IsDefined`）——
> 只看零值会把用户的"关掉"当成"没写"，或反过来把"没写"当成"关掉"。
> 整体关闭不设 `enabled` 键：kill-switch 是环境变量 `YAGENT_BLACKBOARD=0`（`blackboard.Enabled()` 唯一判据）。
> 索引的 token 成本上限 = `index_max_notes × index_line_chars`，且它是**每轮**都注入（Director 自己 + 每次委派）。

```toml
[resume]
enable = true                            # 总开关；false = 不写检查点、不重放（停住的任务只能从 Director 继续）
replay_token_budget = 0                  # 重放上下文 token 上限；0 = 用模型目录阈值，超预算**降级成交接报告**而不是报错
max_replay_messages = 2000               # 重建消息条数上限；**显式 0 = 不限**，负数或没写 = 默认 2000
degrade_on_workspace_change = "warn"     # warn = 工作区 HEAD 变了仍重放并记一条降级；block = 拒绝重放走交接报告
checkpoint_workspace = true              # 停止时跑一次 git status 把工作区事实写进检查点（超大仓库可关）
```

> 与 `[blackboard]` 同一套存在性判定：整段缺省走 `sectionHasDecodedKey(md, "resume")`（结果记在
> `cfg.resumeSectionDefined`），单键走 `md.IsDefined("resume", …)`。`max_replay_messages`
> 的 **0 是有意义的**（不限），所以它不能被零值兜底成默认；认不出的 `degrade_on_workspace_change`
> 会 `slog.Warn` 后回落 `warn`（静默按另一种策略走 = 用户写了 `block` 却以为挡住了危险重放）。
> 缺省是 `warn` 而不是 `block`：换机器/pull 之后 HEAD 必然变，默认 block 会让 B 层在最主要的场景里失效。
> 整体退出这套机制是环境变量 `YAGENT_RESUME=0`（`config.ResumeEnabled()` 是写检查点、重放、UI 入口
> 三处共用的唯一判据）—— 关掉是**连写都不写**，留半套开关的后果是"这份 rollout 为什么没有检查点"
> 变成没人能回答的新问题。

---

## 8. 开发与构建

### 8.1 构建

```bash
./build.sh                    # Rust(codeseek release) + Go，产物 dist/bin/codeseek 与 ./yagent
./build.sh -j 8               # 调 cargo 并行度
./build.sh clean | release | help
SKIP_RUST=true ./build.sh     # 只构建 Go（复用已存在的 dist/bin）；dist/bin 为空时二进制里不嵌任何引擎
SKIP_GO=true ./build.sh
BUILD_TYPE=debug ./build.sh
```

只想快速验证 Go 侧改动：`go build -o yagent .`（`dist/bin` 已有文件时可用）。
版本号通过 ldflags 注入 `internal/globalctx.{Version,BuildTime,GitCommit}`。

### 8.2 运行

```bash
./yagent                      # 默认 TUI
./yagent tui --taskfile TASK.md
./yagent -p "问题"             # 非交互单轮
./yagent http --port 9080     # 注意：--port 0 表示系统分配端口
# 通用 flag: -d/--disable-agents  -c/--config  -y/--yolo  -Y/--full-yolo  -Q/--force-quit
```

工作目录即项目根（`os.Getwd()`），没有 `--cwd` 类参数。

### 8.3 测试

```bash
bash scripts/check_all.sh                 # 一条命令的本地闸门：build + vet + 九把守卫 + 全量单测
RACE=true bash scripts/check_all.sh       # 同上，再对并发包跑 -race（慢 3-8 倍，改到并发时手动跑）
bash scripts/check_all.sh --fast          # 只跑 ./internal/... 的全量单测，跳过 ./... 与守卫之外的部分
go test ./... -count=1 -timeout 600s      # 全仓 26 个测试包，墙钟约 12s（包间并行；2026-10-15 实测）
go test ./internal/tui/... ./internal/messaging/... -count=1   # 快循环，亚秒级
go test ./internal/agents/... -count=1 -timeout 300s  # 最慢的一档，约 10.5s
go test ./internal/agents/ -count=1 -skip 'TestPause...'       # 用 -skip 做耗时归因，别猜
bash scripts/check_test_hygiene.sh        # 单测卫生棘轮（见 §9.2）
go vet ./...                              # 必须干净
```

- 共 124 个测试文件、约 952 个 `Test*`（2026-10-14 两轮整理：先 966→805 清掉"永远不会失败"的
  用例并把全仓墙钟 80s→7s；同日补齐核心系统契约后 805→952。加了 147 个用例而墙钟没涨（并行下
  仍约 6-7s），因为新增的都自带计时 seam 或走假服务端，没有一个是靠 sleep 等出来的。
- **核心包覆盖率基线**（`go test -cover`，2026-10-14）：recovery 97.6%、toolspec 87.3%、
  config 84.2%、messaging 80.4%、datamanager 65.2%、memory 64.3%、llm 61.6%、mcp 62.1%、
  agents 59.5%、app 16.9%。`app`/`toolspec`/`mcp`/`llm` 原本分别只有 0%/8.5%/8.6%/18.6% ——
  低覆盖率的共性是"错了不报错，只是静默换模型/静默丢工具"，所以补的是**契约**而不是行数。
- **链路级 E2E 只 mock 模型，不 mock 被测组件**：`internal/agents/e2e_*_test.go` 钉五条
  核心链路 —— 授权回路（`e2e_authorization_test.go`）、跨委派交接（`e2e_handoff_test.go`：
  便签 + artifact + deferred schema 重试）、委派生命周期（`e2e_lifecycle_test.go`：idle/total
  超时与取消传播）、`/resume`（`e2e_resume_test.go`）、**委派级重放**（`e2e_replay_test.go`：
  恢复后喂给模型的消息必须与暂停前**逐条同形**、取消路径也要写出可用检查点、
  `delegation_id` 真的落进了子 rollout）。它们用的是**真实** dispatcher、真实
  `WorkspaceGuard`、真实便签盘（`TestMain` 已把 `$HOME` 隔离到临时目录）、真实 rollout 文件，
  只把 LLM 换成脚本。harness 的接线顺序照抄 `internal/app/app.go:112-292` —— 换顺序会测到一条生产不存在的路。
- **脚本引擎按 Agent 身份分派，不按全局调用序号**（`e2e_engine_test.go`）：序号会被循环里的
  补偿动作打断（纯文本触发 NoActionRetryLimit、Director 未委派触发强制委派提醒、超时后的重试），
  一个无关的 +1 就让整条脚本错位，失败看起来像产品 bug。身份靠各家 System Prompt 的首行措辞识别，
  prompt 改了会失败在"认不出是谁"。脚本引擎**必须先看 ctx**：忽略 ctx 的 mock 会把
  "取消没传播"这类缺陷直接掩盖掉。
- **协议层用 `httptest` 假服务端钉字节形状**：`internal/llm/engine_openai_test.go` 与
  `engine_anthropic_test.go` 断言的是发出去的 JSON（`system` 顶层字段、`tool_result` 块、
  `reasoning_content` 回灌、`stream_options.include_usage`）与读回来的账目
  （OpenAI 的 `prompt_tokens` 已含缓存、Anthropic 的 `input_tokens` 不含 —— 两边口径相反）。
- **MCP 客户端的假服务器 = 重新执行测试二进制**：`internal/mcp/client_process_test.go` 的
  `TestMain` 认 `YAGENT_TEST_FAKE_MCP_SERVER=<模式>` 环境变量，子进程直接跑假服务器而不是
  `m.Run()`。不要改成 `go run` 或外部脚本：那会把编译时间算进单测，也会让 `-race` 下起不了进程。
- **计时类 seam**：`datamanager.taskFlushDebounce`（5s 防抖窗口）与 `agents.stepRetryBackoffBase`
  （步级 LLM 重试退避基数）。凡是"等 timer / 等退避"的
  测试都必须走这两个变量而不是 `time.Sleep`，同 `tools.defaultToolTimeout` 一个道理。
  新增 timer 请先加 seam，再让测试自己负责耗时。
- **耗时必须由测试自己负责**：历史上 `internal/compression` 的 28s 全部来自五个用
  `strings.Repeat("x", 50000)` 造工具结果的截断测试（tiktoken 反复编几百 KB）——payload 缩到
  1-2k 并等比缩预算后整包 0.17s，断言语义不变（今天 0.55s：`TestTruncateCostScalesLinearly` 是故意留下的
  一条计时用例，用来钉住"每截一条全量重算"不许回来，见 `docs/compression-trigger-design.md` R2）。`internal/agents` 的 30s 来自
  `TestRunAgentLoop_MultipleToolCalls` 把工具阻塞到 **父 ctx 的 30s 超时**；现在统一用
  `restoreToolTimeout(t, …)`（改 `defaultToolTimeout` 这个已有 seam）+ `context.Background()`。
  LLM 引擎的退避（baseDelay 10s）**不要**试图跑完整重试序列：用短 `cfg.Timeout` 把退避钳制到
  剩余预算，然后断言"服务端只收到 1 次请求"——这条断言同时钉住 SDK 自带重试被
  `option.WithMaxRetries(0)` 关掉了。
- **不要写"永远不会失败"的测试**。整理前的基线里有 16 个用例没有任何断言能力
  （只有 `t.Logf`、整函数体只有一行 `t.Skip`、或恒真断言）。三类典型坑：
  - 断言在异步完成之前：`callConsolidationLLM` 失败要睡 1s+2s 真实退避，而测试只睡 2s，
    于是"LLM 失败后保留旧记忆"这条断言在重试跑完之前就检查了 —— 无论实现对错都通过。
    改法是等确定性信号（`Stop()` 会排干队列），不是等更久。
  - fixture 小到根本没触发被测分支：`TestEnforceTokenBudget_TruncatesAtSectionBoundary`
    的夹具只有 643 token < 上限 1500，从未截断过。现在每条这类测试都自带
    `t.Fatalf` 前置条件（"夹具必须超预算"）。
  - 去重先把数量吃掉：`TestResolveRefsMaxLimit` 用 20 条**相同** ref 去测上限 10，
    去重后必然剩 1 条，上限断言永远为真。
  - 反向的坑同样致命：**校验规则本身也可能不可触发**。`toolspec.Validate()` 里那条
    "Serial 与 IsReadOnly 冲突"永远为假（`IsReadOnly()` 的定义含 `!Serial`），
    而它与 `Serial` 字段"无副作用也必须串行"的文档直接矛盾 —— 已删除，由
    `registry_test.go` 的 `TestValidate_ReportsEachRule` 保证每条规则都有能响的夹具。
    新写校验时，请同时写一个"它必须报"的夹具用例。
- 历史上 `TestRunAgentLoop_ParentCancel_AllCallsAccounted` 会卡在 `toolbatch.Run`：它把 `cancel()`
  放在批次内的第一个工具里，而 `toolbatch` **不保证组内启动顺序**，负责取消的工具一旦排在信号量队列里，
  在飞调用就无人可取消，只能等 `defaultToolTimeout`(180s) 逐波解开。现在改成从批次外部取消
  （`time.AfterFunc`），写并发工具测试时不要重现这个假设。
- **没有 CI 质量闸门**：`.github/workflows/release.yml` 只做跨平台构建 + 发布，不跑测试/lint；也没有 Makefile/golangci。
  验证靠本地 `go build` + `go vet` + 相关包测试 + `scripts/` 下的守卫。
  `-race` 不在默认流程里；改动并发/事件总线相关测试时手动跑一次。
- TUI 改动必须跑渲染/尺寸矩阵测试（见 §6.8）。

---

## 9. 编码规范与架构不变量

### 9.1 规范

- 导入路径 `yagent/internal/...`（已无 `yagent/pkg/...`）。
- `gofmt` 为准，但**存量文件有对齐漂移**（`internal/tui` 若干）；改到被 `gofmt -l` 报出的文件时先用 `gofmt -d`
  区分"我写的"和"历史遗留"，不要顺手全量重排。新建/大改的文件必须干净。
- 错误用 `fmt.Errorf("context: %w", err)` 包装，不吞错误；长任务必须响应 `ctx` 取消。
- 注释默认不写；只写"为什么"，且不超过一行。

### 9.2 工具/规模守卫

```bash
bash scripts/check_all.sh                 # 上面九把守卫 + build + vet + 全量单测，一条命令跑完
bash scripts/check_file_sizes.sh          # internal/agents/director/ 下非测试文件 ≤300 行（存量按 PENDING 冻结）
bash scripts/check_no_agents_import.sh    # director/ 子包不得 import yagent/internal/agents（防循环依赖）
bash scripts/check_toolspec_leaf.sh       # toolspec 不得反向依赖 agents/tools
bash scripts/check_no_manual_lookup.sh    # 不许重新出现按名字查工具的 lookupToolFunc/getToolFunc
bash scripts/check_test_hygiene.sh        # 单测卫生棘轮，见下
bash scripts/check_blueprint_leaf.sh      # internal/blueprint 只准依赖标准库 + util + projectid
bash scripts/check_supervise_leaf.sh      # internal/supervise 只准依赖标准库
bash scripts/check_no_bare_go.sh          # 全仓不许出现裸 go（唯一例外：internal/supervise 自己）
bash scripts/check_no_inline_atomic_write.sh  # tmp+rename 只准有 internal/util/atomic.go 一处（I21）
```

- `check_file_sizes.sh` 现在是**棘轮而不是硬门**：存量超限项在脚本顶部的 `PENDING` 里
  点名并记下"允许的最大行数"作为冻结上限（今天只有 `director/planner_run.go`，上限 **572 行**——
  压缩段 96 行已抽到 `planner_compress.go`，657 → 572；剩下主循环 + 委派编排两段职责，拆分是独立一次改动）。规则：新文件超 300 行直接红；
  存量文件长回去也红；**改小了必须同步下调 PENDING 里的数字**，否则脚本会主动报失步。
  以前它无条件红灯，结果所有人对它视而不见 —— 一条常红的守卫等于没有守卫。
- `check_test_hygiene.sh` 守的是**测试自己**：① 每个 `Test*` 必须能失败（自身有 `t.Error*/t.Fatal*`，
  或调用 `assert*/check*/verify*/expect*/require*` 辅助，或写明 `hygiene:panic-guard` 豁免标记）；
  ② 函数体第一层不得出现裸 `t.Skip*`（按环境跳过必须带条件）；③ 同一个包只允许一个
  `string→string` 的 ANSI 剥离辅助函数。脚本自己也会报"只扫到 N 个函数"的失步异常，
  不会静默放行 —— 2026-10-14 整理前的基线是 882 个 `Test*`，其中 16 个没有任何失败能力
  （`t.Logf` dump、只有一行 `t.Skip` 的空壳、恒真断言如"发布 1000 条却断言 count>1000"）。
  唯一豁免途径是 `hygiene:panic-guard`：去掉某条 nil 守卫后测试会 panic 而失败的那类用例。
  注意它按**名字**认断言：自定义等待辅助（如 messaging 的 `waitFor`）不算断言，
  所以"等异步"的用例仍必须补一条直接的 `t.Errorf`。

- **goroutine 只能经 `internal/supervise` 起**（C1，`docs/structured-concurrency-design.md`）：
  `supervise.From(ctx).Go("名字", fn)` / `supervise.New(ctx, 域名)` 给出**名字 + recover + 可等待**，
  `Drain(timeout)` 返回的是"哪几条还没退"的名字清单，不是一句超时。裸 `go` 由
  `check_no_bare_go.sh` 硬禁（**没有 PENDING 名单** —— 一条常红的守卫等于没有守卫，
  而带名单的棘轮会变成第二个会漂走的事实）。两条纪律：
  ① **需要"等这一批"的地方仍用本地 WaitGroup**（`toolbatch`、`tui_resume_stop`）—— Drain 等的是
  整条域，把"批次"当成"域"会把不相关的后台工作一起算进来，甚至等自己（见 ②）。
  ② **跑任务的那条 goroutine 不进任务自己的域**：域由 `http.NewTaskContext` 挂在 ctx 上，
  任务收尾时 `Drain` 它；把执行体自己也算进去就是收尾时等自己，会死锁。
- **`memory.ConversationMemory` 的 `messages` 字段不导出，读写只走方法**（C4/D6）：
  `Snapshot()` 拿一份拷贝、`Append(...)`/`Add*` 写、`Replace()` 整体装载（从磁盘/rollout 重建时用，
  **不**触发 MaxSize 截断）、`Size()`、`LastMessage()` 返回**拷贝**。
  三条理由都要记住：① 并行委派的注入跑在工作线程上，planner 在主线程写同一份；
  ② `GetMessages()` 的注释早就写着 "thread-safe copy" 而它返回的是内部 slice 本身——
  注释与实现不符比没注释更糟，现在名副其实；③ 旧版 `GetLastMessage()` 返回
  `*ChatMessage` 指向 slice 内部，拿着指针的人绕过锁还能改写它。
  JSON 线上形态（`{"messages":[…],"max_size":n}`）由 `MarshalJSON/UnmarshalJSON` 原样保住，
  封装不许改协议。
- **把指针装进 interface 的那一瞬间就再也不等于 `nil`**（C4 顺手修的既有洞）：
  `func() builtinAgent { return a.RepoAgent }` 在 `a.RepoAgent == nil` 时返回的是
  "装着 nil 的 interface"，于是 `agent == nil` 那条"not wired"守卫永远不成立，
  未装配的子 Agent 会一路走到 nil 解引用。凡"具体类型指针 → interface"的返回，
  必须在**具体类型**上先判 nil（见 `director_surface.go` 的 `delegateTargets` 注释）。
- **锁不许跨过 IO；投递一律"锁内装配、锁外写"**（C6-d）：`internal/http` 的 6 个状态点
  （三个 setter + pause/continue/cancel）在 `tm.lock` 内只编字节，`taskAction.deliver` 在
  Unlock 之后做 `Socket.Write`/`Broadcast`；`DataManager.FlushAll` 先拿一份 writer 快照就放掉
  `dm.mu`，且**首错不再提前 return**（退出时"第一条刷失败"不该让后面 N-1 条任务记忆蒸发）。
  要测这类不变量得能把 IO 卡住，所以有 `writeSession` / `melodyBroadcast` / `flushHook` 三个
  seam —— 探针必须用**不需要投递**的路径，否则带 socket 的 setter 会停在它自己的慢客户端上，
  那不是"别人的锁压着"。
- **排在闸门上的调用要替 ctx 让路，而且照样产出结果**（C6-a/C6-c）：在飞闸门
  （`llm.acquireInFlight`）与委派池（`acquireDelegateSlot`）都是 `select` 上 `ctx.Done()`，
  取不到格子时返回错误而不是硬等；`toolbatch.Run` 的既有纪律是"已取消的元素照样启动、
  照样 commit"，为的都是同一条不变量：**每个 tool_call 必有一条 tool 结果**，少一条就是
  provider 400（§11 坑 5）。从未取得的一格绝不去做 `<-sem` 接收 —— 那会和其他释放方的
  接收互相吃掉，把整批卡在 WaitGroup 上。
- **MCP 客户端的 `IsAlive()` 只读两个原子位**（C6-b）：`cmd.ProcessState` 由 `cmd.Wait()` 写，
  而查活的线程可能在 Wait 落笔的同一瞬间读它（拿到"非空但还没填完"的指针不是不可能）。
  进程真的没了这件事由 readLoop 的 stdout EOF 兜进 `alive`，所以删掉那次读不丢信息。
  `doRequest` 给每条请求套 `context.WithTimeout(ctx, c.cfg.RequestTimeout)` —— **取调用方 ctx 的较小值**，
  兜底 30s 只在 `NewMCPClient` 一处，别再往上游抄一份默认值。
- Director 采用**门面 + 子包**结构：`internal/agents/director.go` 只做装配与 delegate 注册，
  编排逻辑进 `internal/agents/director/`，跨子系统依赖一律经 `internal/agents/ports.go` 的窄接口注入。
- `toolbatch` 是无业务依赖的叶子包，不要往里加业务。


### 9.3 新增一个工具的正确步骤

1. 在 `internal/tools/` 实现 `Execute*`（或独立 `*Tool` 结构）。
2. 在 `internal/toolspec/schemas.json` 加 name/description/JSON Schema。
3. 在实现旁边 `toolspec.MustRegister(Spec{Name, Effect, Frequency, Mutates, Serial, NeedsAuth,
   PathParams, Trunc, Display, Brief, Build})` —— **这一条取代了旧的第 3、4 步**：
   并发/授权/截断/展示全部从 `Effect` + 显式字段派生，不再往任何名字表里加条目。
4. 让该工具进入某个 Agent 的工具面：优先靠已有的能力标签（画像会自动匹配，无需改 Agent）；
   仅当它是委派类工具时才在 Spec 上写 `Audience`。
   ⤷ 改完必须跑 `go test ./internal/agents -run TestAgentSurfacesGolden -update` 之前先看清 diff，
     工具面变化必须以显式改写 golden 的方式被评审。
5. 用 `client.GetToolEngine("<name>")` + `[tools.llm.<name>]` 取工具级引擎；构造期依赖从 `globalctx` 拿。
6. 加 `internal/tools/*_test.go`；涉及循环则补 `agents` 的执行循环测试。

> **外部工具走另一条通道**：MCP 工具由 `tools.BuildExternalSpecs` 在运行期构造 Spec，经
> `Deps.ExternalSpecs` 进 `Surface.rebuild()`，**不进** `schemas.json`、不进全局注册表、不进 golden。
> 判据见 §5.3 与 §11 坑 44。

---

## 10. Director 行为准则

### 10.1 核心约束

1. **禁止幻觉** — 不编造文件名/函数名/代码；不确定就委派 Repo。
2. **编码分离** — 不向用户输出代码块，所有修改经 `delegate_coding`。
3. **逐步执行** — 一次委派一个子任务，验证后再继续。
4. **强制委派** — 仓库探索走 `delegate_repo`；文件写走 Coding；Director 自己只用只读工具。
5. **禁止长驻进程** — 用 `go build` / 测试验证，不起 dev server。
6. **并行执行** — 只读工具与独立只读委派可以并行（由 `toolbatch` 调度）。
7. **思考先于行动** — 每次工具调用前给出 `<thought>` 块。

### 10.2 `<thought>` 格式契约（`internal/thinklink/thoughtplan.go`）

```
<thought>
goal: [当前高层目标]
step: [上一步及结果]
reason: [为什么执行下一步]
plan:
- [x] [已完成]
- [>] [当前 — 即将委派]
- [ ] [待处理]
</thought>
```

- `<thought>`/`</thought>` 为字面定界符；每轮回复恰好一个块，置于工具调用之前。
- `goal`/`step`/`reason`/`plan` 为固定小写 key，各占一行（`current goal`/`reasoning`/`plan update` 等别名会被归一）。
- `Validate()` 要求 `goal` 与 `reason` 非空，且**至多一条** `[>]` 进行中项。
- 状态标记支持 `[x]` 已完成、`[>]` 进行中、`[ ]` 待处理（解析器也容忍 `X`、`-`、`1.` 等写法）。
- 字段**值**用配置语言（`[agent] lang`）书写；key、标记、标签保持英文。
- 该块同时是 TUI thinklink 面板的显示内容和 ultimate 压缩的重建原料；**解析失败不会报错，只会静默丢失**，所以格式必须严格遵守。
- 目前 Director、Coding、Browser 的 prompt 强制该格式（`browser.prompt.md` 的 Thought Block Contract）；Repo/Chat/DevOps/Meta 未要求——这三个子 Agent 虽然也建了 per-Run thinklink store，但没有 T&P 块，ultimate 重建只剩原始任务输入。

---

## 11. 已知坑

1. **本文档之外的旧资料普遍过期**：`docs/ARCHITECTURE.md` 与 `README.md` 里的部分描述（模块图、`pkg/`、工具数）落后于代码；以本文件和源码为准。
2. **`dist/` 被 `.gitignore` 忽略，但 `dist/data/models.json` 与 `dist/bin` 里的文件是被跟踪的**。改目录后 `git add -f dist/...`，否则 `go:embed` 编不过、或二进制继续用旧数据。
3. **模型目录与配置里的 `context_window` 是两个消费者的不同口径**：
   - 压缩阈值（`compression/threshold.go` 的 `ResolveThreshold`）= 目录命中值（`limit.context × 0.75`）与
     `[enhanced_commander] context_compression_threshold` **取小**（两者都是上限）；没写配置就纯按目录推导；
     **provider 的 `context_window` 完全不参与**。
   - TUI 上下文进度条（`tui/tui_model.go` 的 `contextWindowFromCfg`）= **provider 的 `context_window` 优先** → 未设置才查目录。
   所以给目录里已收录的模型手写一个不同的 `context_window`，只影响进度条，不影响压缩时机。
   **想临时压低阈值做验收**有两条路：写一个小的 `context_compression_threshold`（会被 min 采纳），
   或设 `YAGENT_COMPRESS_THRESHOLD`（跳过目录与配置、且不缓存取值，非法值原样回落）。
   生产配置不要用后者 —— 它绕过一切，会让压缩早于模型真实容量触发，白丢上下文。
4. **压缩的配置历史上写过两次假开关**：模板里曾经既有 `[enhanced_commander] enable`（必须与
   `enable_context_compression` 同时为真）又有 `enable_ultimate_compression`（见 false 就被写回 true）。
   两个坑在 2026-10-16 一并修掉：压缩现在只有一个键，第三档靠**键的存在性**决定默认开、显式 false 就真关。
   （更早那条"`default_config.toml` 里有一段 `[context]` 表 `Config` 不解析"已经名不副实 —— 那段没了。）
5. **声明 ≠ 可用（已消解）**：`Registry.Validate()` 双向对账 schemas.json 与 Spec，"声明了没人实现"
   是启动错误；工具面由画像派生，golden 基线（`internal/agents/testdata/tool_surfaces.golden`）钉住
   每个 Agent 实际拿到什么。加工具若只改 schemas.json 不改 Spec，`check_no_manual_lookup.sh` 与
   `TestRegistryMatchesSchemas` 会立刻报。
   另：`mcp` 客户端的 `sendJSON` 已加写锁 —— 并发只读工具（codeseek 桥接那批现在可并发了）依赖它。
6. **TUI 事件通道无重放**：`listenForEvents` 是一次性 cmd，谁消费了事件不重新武装监听器就会永久断流；
   弹窗守卫只放行少数消息类型（`tea.PasteMsg` 不是 KeyMsg）。任何新增叠加模式/守卫都必须显式放行需要的消息类型并 re-arm。
7. **布局高度必须自洽**：`computeFooterHeight` 与 `View()` 的 footer 组装顺序要完全一致，差一个空行整屏抖动；
   `viewport.SetHighlights` 在我们的重着色日志上会算错偏移，改用 `StyleLineFunc`。
   另：`lipgloss.Style.Height(h)` 是**含上下边框**的总高（实测 `Height(5)` 装 3 行内容 → 输出 5 行，内容多了就撑破），
   所以给一个面板里的若干段分配行预算时要按 `height-2` 算；按 `height` 算会多要 2 行，
   被 `renderDashboard` 末尾的"行数收敛 + 补边框"悄悄截掉，表现是最后一段莫名少两行而不报错。
8. **browser-control 的状态目录是进程 CWD 相对路径**（`.browser-control/`），上游没有任何环境变量可改这个根：
   所有调用必须带 `cmd.Dir`（指向 `~/.yagent/data/browser/{delegation}`）。漏设就会在用户仓库里长出该目录——
   `internal/browserctl` 已把它固化成唯一入口，新增调用点不要自己拼路径。
9. **上游 `reload` 实际等于 `stop`**（README 写 "minimal lifecycle reset"，但 `src/main.rs` 里
   `Cmd::Stop | Cmd::Reload => lifecycle::stop()`），刷新页面要用 `eval 'location.reload()'`；
   上游也**没有** `hover` / `back`，需要用 `browser_eval` 派发 `mouseover` / 调 `history.back()`。
10. **无 CDP 环境变量时上游会硬扫 9222/9223**：必须显式注入 `BROWSER_CONTROL_CDP_URL`，
    自启端口从 `cdp_port_base`(9333) 递增且跳过 9222/9223，否则会连到用户自己的调试浏览器或另一个任务。
    另外上游内部 CDP 超时默认仅 15s，`browserctl` 会按最长 wait 把它抬到 ≥65s，别在别处重复注入这个变量。
11. **便签索引必须是派生态、且瞬态**：`messagesForLLMCall` 只在**这次 LLM 调用**的消息上附加索引，
    返回值绝不写回外层 `messages`（`planner_run.go` 传的就是返回值）。
    一旦漏回，下一步的压缩预算会把目录算进正文、紧急压缩还会把它当成一条用户输入折叠进摘要 ——
    那正好是它要解决的病（`note:`/`artifact:` 引用消失在 tool 消息里）。
    `TestMessagesForLLMCallIsIdempotent` 与 `TestIndexSurvivesEmergencyCompressionShape` 钉住这条不变量。
12. **`agent_exit` 收尾的 Agent，交付物不在 `Text` 里**：`RunAgentLoop` 在这条路径上把 `Text` 设成
    `{"finished":true,"reason":...}`（`executor.go` 的 stop 分支），真实分析在最后一条 assistant 消息。
    便签正文与 artifact 全文一律经 `deliverableText()` 取，判据是"JSON 里有 `finished:true` + `reason` 键"
    这个**形态**而不是 Agent 名字；`AgentResult.Text` 的既有语义没动（超预算时仍是摘要），
    需要全文的下游读 `AgentResult.Deliverable`。
13. **`[blackboard]` 的三个阀门靠"键存在性"而非零值**（`md.IsDefined`）：`index_max_notes = 0` = 关索引、
    `index_line_chars = 0` = 不裁剪行、`ttl = "0"` = 不清理；"没写这一段"解析出来同样是 0，
    只看零值会把用户的关闭意图当成没写（或反之静默关掉新能力）。不设 `enabled` 键，
    整体关闭是 `YAGENT_BLACKBOARD=0`（`blackboard.Enabled()` 是写/读/注入/清理四处共用的唯一判据）。
14. **Bedrock 豁免已整体删除**：以前 `config.validate()` 用 `strings.HasPrefix(effectiveProvider, "bedrock")`
    跳过 `api_key`/`api_base_url` 并要求 `aws_region` —— 而本仓库没有 Bedrock 专用客户端，豁免只会把失败
    推迟到第一次调用（空 base_url），同时"名字不带 bedrock 的真 Bedrock"反而被要求填密钥。
    现在所有 provider 一视同仁，`aws_region/aws_profile/model_provider` 三个键连字段一起删了。
    **仍然成立的教训**：这段豁免和**整段缺省归一化**挤在同一个函数里，以前它写成 `return nil`，于是
    max steps、`[browser]` 缺省、TUI 键位、压缩参数全部停在零值（表现不是报错，是能力静默缺失）。
    往 `validate()` 里加提前 return 之前，先确认下面的归一化段还会被执行；
    回归由 `internal/config/validate_test.go:TestLoadFromFile_BedrockNoLongerExempted` 钉住
    （它同时断言"归一化照常发生"，就是因为那条历史教训）。
15. **`toolspec.Validate()` 的每条规则都必须有能触发它的夹具**：`IsReadOnly()` 的定义含 `!Serial`，
    所以曾经那条 "Serial 与 IsReadOnly 冲突" 在数学上不可能成立 —— 它既不会响，又与 `Serial`
    字段"无副作用也必须串行"的文档承诺矛盾，已删除。教训：只测"生产注册表没有 problems"等于
    没测，删掉任何一条规则都不会有人发现。见 `registry_test.go:TestValidate_ReportsEachRule`。
16. **子 Agent 的 `agent_exit` 会不会真的收尾，取决于 `cfg.StopOnFinish`**：`RunAgentLoop` 的判据是
    `cfg.StopOnFinish && tc.Function.Name == "agent_exit"`，而这个标志是各 Agent 自己在
    `ExecutorConfig` 上逐个设的。`repo.go` 曾漏掉它 —— Repo 的 prompt 明确指示"以 agent_exit 收尾"，
    于是收尾信号只是"又执行了一次工具"，循环继续烧到 MaxSteps，交付物变成后续那几轮的文本。
    连带后果在便签/artifact 上：`deliverableText()` 取的是最后一条 assistant 消息，
    所以便签正文与 artifact 全文存的都不是那份交付。现在由
    `internal/agents/e2e_{handoff,lifecycle}_test.go` 钉住（委派只花 1 次 LLM 调用），
    新增子 Agent 时漏设 StopOnFinish 会直接红。
17. **暂停不是取消，两者弄混会静默丢工作**：A 层的闸门（`internal/pause`）**不取消 ctx**，只在
    step 边界阻塞；取消会解开调用栈，并把那次委派的工具结果写成 `"Error: context canceled"`，
    Director 于是以为"委派失败了"并自行重做或收尾。三条必须记住的推论：
    ① 暂停期间必须显式冻结 `activity` 的 idle **和** total —— `Heartbeat` 只能续 idle
    （`totalElapsed` 取自创建时固定的 `startTime`），只处理 idle 的表现是"暂停 20m 后被总超时杀掉"；
    ② `activity.Resume` 必须把空闲基准推到恢复时刻，否则第一个 tick 就算出
    `idleFor = 暂停时长`，**刚恢复的任务立刻被杀**；
    ③ 暂停态绝不能走 `handleTaskCompleteMsg` 的换 ctx 分支（它会丢掉闸门所在的那棵 ctx）。
    另外：`/` 前缀在 TUI 里默认是**搜索**，新命令必须显式列进 `processCommand` 的斜杠分支，
    否则 `/pause` 是在搜字符串 "pause"，界面上什么都没发生。
18. **"两份同形实现"必须各自有输入进聚合测试**：`agents.classifyStop` 与
    `director.StopKindOf` 因分层守卫（`check_no_agents_import.sh`）只能各写一份，取值靠
    `memory.StopKind*` 对齐、行为靠 `TestStopKindClassifiersAgree` 对齐。P1 加 `paused` 时，
    值表先加了 abandoned 输入而聚合表没有 —— 结果**只有一边漂走**时聚合测试是绿的，
    补上输入才红得出来。加词表值时的正确顺序：先给聚合测试喂一个新输入，再让两边实现去追它。
19. **检查点指纹只能由读侧计算**：`agent_checkpoint.digest` 存的是写侧那一刻对**整个文件重建成**的
    消息序列算出的 sha256，但比较发生在 `ReadRolloutForReplay` 用**同一套规则**重算之后 ——
    而这套规则包括"用 `compacted` 记录替换被压过的前缀"。如果指纹是写侧按内存里那份消息算的，
    任何触发过压缩的会话（=每一个长任务）都会在自己的检查点上对不上号，B 层恰好死在它
    最该起作用的地方。`TestWriteCheckpointDigestMatchesReplay` 钉的就是两侧规则同源。
20. **`Reasoning` 从此才真的进 rollout**：`memory/rollout_convert.go` 现在会为 thinking /
    `reasoning_content` 发一条 `reasoning` item。此前它**从来没有被持久化**，
    于是"重放出的消息与暂停前逐条同形"这条承诺缺一大块（token 估算与 ultimate 压缩都算它）。
    改任何一边的转换都要记得 `DigestMessages` 把 `Reasoning` 算进指纹：漏算不是"少几个 token"，
    是"整次重放被判成不可信、静默降级成交接报告"。
21. **检查点写在终止路径，不写每步**：两个循环各只在终止/deadfer 块里落一条 `agent_checkpoint`
    （`executor.go` 的 `writeExecutorCheckpoint`、`director/planner_rollout.go` 的 `aborted`）。
    每步一条会让 150 步的会话多出 150 条记录，而重放永远只关心最后一个停点；
    `TestNoPerStepCheckpointWritten` 钉住这条。想加"每步检查点"前先想清楚恢复侧怎么选。
22. **降级不是错误**：`assessReplay` 命中判据时 `resumeDelegation` 返回的是一份**报告文本 + 理由列表**，
    不是 `error`。把它写成 error 的表现与 §11 坑 17 同类 —— Director 认为"这次委派失败了"，
    于是把已经做完的工作重做一遍。同理，重放成功后交回的是**同一个 `call_id` 的第二条 tool output**
    （last-wins 覆盖），模型看到的是"那次委派成功返回了"，而不是一条新消息。
23. **"扫目录再计数"的测试必须拥有那个目录**：`internal/memory` 的测试**不像 `internal/agents` 那样
    把 `$HOME` 隔离到临时目录**，所以 `TestScanResumeCandidates_ClassifiesEveryStopKind` 这类
    "往 `~/.yagent` 写夹具再扫"的用例在 `-count=4 -parallel 8` 下会把候选累加（24/36/48 条），
    断言 `>=1` 恒真 —— 门禁当场失效。每个 run 生成唯一 projectID（或 `t.Setenv("HOME", t.TempDir())`）。
24. **`pause` 的 `AbandonedError` 要 `errors.Is` 到 `context.Canceled`**：toolbatch 的取消短路、
    `delegateTo` 的错误分类、`looksLikeCancelledContext` 的文本匹配都认它。
    换成孤立新错误的表现是"取消了但没人认为是取消"，Director 把它当成一次失败的委派继续往下跑。
    反过来，便签侧要区分"停住"与"失败"用的是 `errors.As`（`writeDelegateNote` 对 abandoned
    **什么都不写**，词表里因此没有 `paused`），两件事都要保留。
25. **"配置项存在"不等于"配置项生效"**：本轮删掉的 13 组键里，大多数不是没人读，而是**读了也没用**
    （`enable_streaming` 只进 `slog.Debug`；`auto_checkpoint` 只打一条每步 deprecation 警告；
    `[codeseek.knowledge] enabled` 被 `validate()` 写回 true，因此连"关掉"这个动作都不成立）。
    判断标准只能是"改这个值，有没有一条用户可观察的行为会变"。新增配置项时请同时新增
    一个**能因为改值而失败**的测试；`removed_keys_test.go` 要求登记表与夹具互为镜像，
    就是为了不让"承诺会点名"变成第二句空话。
26. **布尔键的"缺省为真"必须靠键的存在性实现**（`md.IsDefined` / `sectionHasDecodedKey`）：
    Go 零值是 false，直接看值就分不清"没写"与"写了 false"。`enable_ultimate_compression` 之前
    用"见 false 就写回 true"绕过，结果那个键永远关不掉；`[git_checkpoint]` 之前用
    "前缀为空就整段换默认值"绕过，结果只写 `enabled = false` 会被顶回 true。
    正确形状见 §6.5 的第三档与 §11 坑 13。
27. **写 config.toml 只能文本级替换**：`persist.go` 的内核是 `SetKeyInSection`，
    用 `toml.NewEncoder` 整份重编码会把用户注释全部蒸发（模板里每个键都靠注释自我说明）。
    两个例外规则也要记得：值一律 `tomlBasicString` 转义，且"删一个 provider"前必须查引用
    （`ProviderRefs`），否则会写出一份"下次启动加载失败"的文件。
28. **向导既能独立跑也能嵌进主 TUI，所以它不碰 Yagent/Dispatcher/MCP**：`setupWizard` 只读
    配置文件文本（`config.DecodeRaw`，故意绕开 `LoadFromFile` —— 配置坏的时候正是最需要它的时候）、
    写文件、最多发一次最小 LLM 请求。`/setup` 在任务运行中直接拒绝，因为它独占键盘而
    `listenForEvents` 没有重放队列（坑 6）；`applyOps` 是逐条落盘、**不是事务**，
    所以失败信息必须带上"哪一条 note 失败了"。
29. **TUI 键位是"替换"不是"追加别名"**：翻译表把按下的键映射到**规范键名**（各 switch 分支的字面量），
    所以自定义键 → 规范键，而被换掉的默认键 → 空串 = 解绑，消费点经 `translateKey` 换成
    `keyUnbound` 哨兵落到 default 分支。旧实现只有 `editKeyMap[custom] = default` 这一半，
    于是"把 quit 挪走"之后 `ctrl+c` 仍然退程序，而且默认键永远释放不掉。
    两个附带约束：抢同一个键时**保留先声明的动作**并 `slog.Warn`；`keyUnbound` 绝不能长得像真按键
    （`TestUnboundSentinelMatchesNothing`）。表单屏还在 `tab/enter` 之后把字符交给 textarea ——
    字段自己得吃掉按键。
30. **TUI 的行数预算必须按"渲染后的行数"算**：`lipgloss.Style.Width()` 会把超宽的行**再折一次**，
    所以"我数过 body 有 13 行"和屏上真正的 16 行不是一个数，面板因此能超出终端高度 ——
    在 AltScreen 里多出来的行看不见也滚不回去，表现是"最后一屏莫名少几行"。
    三道闸门都在计数之前做，缺一个就会复现：① `fitLines` 先把越宽的行剪断（宽度用 `lipgloss.Width`，
    样式串里的 ANSI 不占列）；② `wrapPlain` 必须**硬切超长单词**（provider 错误原文里全是 URL、
    base64 密钥、JSON 片段，只按空白折行等于没折）；③ 状态条（错误原文）自己有预算 `capStatus`，
    否则"正文 = 高 - 2 - 状态行"会算出负数并夹到最小值，总高反而超出终端。
    同理，卡片/表格每一行的列宽要**正好加到容器内宽**（分隔符数量按真正拼出的列数算，
    徽章还要算上 `BadgeStyle` 的 `Padding(0,1)`）。闸门是 `TestWizardRenderWidthMatrix`
    （宽度 {40..240} × 高度 {18,24,40} × 七屏 × 折叠/预设子模式，断言行数 ≤ 终端高度）。
31. **表单屏的模态键必须带修饰符**：向导的表单每一格都是活的 textarea，裸字母要先送进输入框。
    曾经用裸 `p`（端点预设）和裸 `o`（展开可选区），于是填 `https://…` 会吃掉 `ps`、
    并莫名打开预设列表 —— 用户看到的是"我打的字少了两个字母"。正确形状：`ctrl+p` / `ctrl+o` / `f2`
    （与主 TUI 用 `alt+s` 提交同理），只有在**没有 textarea 活着**的屏（列表、摘要、预设选择中）
    才可以用裸键。`TestWizardHintsAreNotDecoration` 还反向钉住另一半：底栏键帽上写的每个单键
    都必须真的能改状态，因为键帽比裸串更容易被相信。
32. **向导的 `done` 不等于退出，退出是宿主的决定**：`setupWizard` 同一个状态机跑在两个宿主里，
    `q` / `ctrl+c` / 摘要落盘只置 `done=true`，而 bubbletea 只认 `tea.Quit`。独立跑（`RunSetup`）
    时必须把它翻成 `tea.Quit`（`w.standalone` + `Update` 里单点收口），否则进程停在最后一屏 ——
    用户看到的症状是"按 q 没反应"；嵌在主 TUI 时反过来必须**不**翻，那会把整个 yagent 退掉，
    交还键盘由 `updateSetupMode` 的 `Done()` 分支负责。闸门是 `TestRunSetupProgramActuallyExits`：
    它用 `tea.WithInput(strings.NewReader("q"))` 真跑一次程序断言 `Run` 返回，不是只查 cmd 形状。
33. **测试与生产必须共用同一个载体，否则测试会钉住一条不存在的路**：子 Agent 的并行曾经完全没生效 ——
    `executor.go` 的并发谓词扫 `cfg.Adapters`，而五个子 Agent 全部只填 `cfg.Surface`，于是谓词恒 false、
    `toolbatch` 被调用却一个 goroutine 都不开；同时 `executor_parallel_test.go` 的 8 个用例都往 `Adapters`
    里塞夹具，**所以测试全绿**。同一文件里 `resolveAdapter`/`defsForStep` 都正确"Surface 优先"，
    只有这个谓词漏了 —— 工具注册表迁移时没跟上，而两边不对称本身就是证据。
    修法不是补一个分支，是把 `ExecutorConfig.Adapters` 与 `DirectorAgent.Adapters` **连根删掉**
    （`resolveAdapter` 只认 Surface，判据收成 `toolCallIsParallelizable` 一个函数、两个循环共用），
    测试改用 `tools.NewSurfaceFromAdapters`；`parallel_admission_test.go` 更进一步直接拿真实画像
    （`ProfileRepo`/`ProfileCoding`）问判据。闸门是 `TestRunAgentLoop_ReadOnlyBatch_ParallelEvidence`
    （+ 一条 `max_parallel_readonly_tools=1` 的对照臂，证明"并发峰值 ≥2"不是计量器自己的毛病）：
    把谓词改回恒 false，它立刻红 `expected max concurrency >= 2, got 1`。
    **教训通用**：新增一个"生产只填 A、测试却填 B"的字段对，就等于埋下一个永远不会红的测试。
34. **流式槽的键不等于显示名**：TUI 的 `aiChunkBuffers` / `aiStreamActiveEntries` /
    `aiStreamCompletedEntries` 现在按 `streamSlotKey(event, agentName)`（`agent#delegationID`）分槽，
    而 `le.from` / `m.currentAgent` / token 归集**必须继续用裸 Agent 名** —— 把组合键当名字用的那一刻，
    界面标签就变成 `Coding#call_3`。两者在同一个 handler 里挨着写，是顺手会搞混的地方。
    坐标为空时键退回裸名字，于是 Director 自己的输出与 `/resume` 恢复的存量 rollout 行为逐字不变
    （`TestLegacyStreamsWithoutDelegationKeepOldBehavior` 钉的就是"没有坐标时不许变新"）。
    变异验证：让 `streamSlotKey` 忽略 `DelegationID` → `TestParallelStreamsSameAgentDoNotMerge` 红，
    症状正是当年的 `"AAABBBAAABBB"` 粘成一条。
35. **"默认值"必须有人真的用它的默认值**：调度器文档写着"每消费者 1000 容量"，而 6 个建
    dispatcher 的地方全都手写 `NewMessageDispatcher(100)` —— 那个 1000 从来没生效过，
    于是满载丢弃比设计早一个数量级发生，表现是"界面偶尔少字"而日志只有一条限频 warn。
    现在统一走 `NewDefaultMessageDispatcher()`；配套的可见性是 `Dropped()` / `WarnDropped(scope)`。
    判断一个默认值是不是活的，别看文档，看有没有调用点真的走它。
36. **熔断器的"查询"本身就是那次试探**（C6-d 改了这里的语义）：
    `RecoveryHandler.IsCircuitBreakerOpen() == !breaker.Allow()`，而 `Allow()` 带着
    open→half-open 的迁移副作用。half-open 现在只放**一个**探针（`probeAt` 记账），
    于是"先查一下再决定要不要调用"的代码会**消费掉**那次试探 —— 生产安全的前提是 Director
    每一步都在同一次迭代里"查询 → 调用 → 回报 Success/Failure"（`planner_run.go` 的
    198/319/324 三行）；探针万一没人回报（调用方死在半路），过 `resetTimeout` 后重新放行一次，
    所以最坏是多等一个周期而不是永久锁死。要加新的查询点之前先数一遍有几个调用点在读它，
    并把"第二个调用者在试探回报之前必须被拒绝"这条测试一起改掉 ——
    旧用例 `TestCircuitBreaker_HalfOpenAdmitsEveryRequest` 的注释就明确要求改写而不是删掉。
37. **`[llm] timeout` 与"排队"的先后是能看出来的**：在飞闸门（`llm.SetInFlightLimit`）
    与委派池都在**建这轮 ctx 链 / 套调用超时之前**取格子。放错顺序的后果不是崩溃，而是两件
    很难查的事：①排队时间被算进 `llm.timeout`，一次正常的排队在真正发出去之前就被判成
    "provider 慢"；②排队时间被算进 `activity` 的 idle 计时，一次"前面有人"的等待把子 Agent
    的看门狗顶成超时。同一条理由也解释了 `MCP doRequest` 的 deadline 为什么必须
    `WithTimeout(ctx, …)` 而不是 `WithTimeout(context.Background(), …)` —— 后者会整条丢掉
    调用方的取消链，表现就是坑 17 那一类"取消了但没人认为被取消"。
38. **工具回显少一个键，提交就在离开循环的那一刻静默消失**：`ExecuteAgentExit`
    （`tools/flow_control.go`）把 `handoff` 透传进返回值，Agent 侧的采集点读的就是**返回值**
    —— 而它当初没有透传 `blueprint`，于是蓝图永远不落盘、不报错、不丢日志（R6 落地时
    由 `TestE2ERepoRunWritesBlueprintFile` 抓出，变异 M2 把它改回去立刻红）。
    **通用教训**：任何"工具层只回显、Agent 层才解读"的参数，加参数时必须同时改两处；
    只改 schema 与解析侧，得到的是一条永远绿的路径。同类历史坑见 §11 坑 16（StopOnFinish）。
39. **探针 Deps 必须与生产同形，否则 golden 在钉一条不存在的路**：`read_blueprint` 注册后
    工具面 golden 一动不动 —— 因为 golden 走 `Profile.ProbeDeps()`，而生产的 `depsFor` 恒绑
    `Blueprint` 执行器；探针没绑 → `Available` 为假 → 六个画像都"没有"这个工具。
    这与 §11 坑 33（并发谓词扫 `Adapters`、测试自己填 `Adapters`）是同一类失效：
    **两边不对称本身就是证据**。修法是把能力加进 `testDeps()`（`toolspec/profile.go`），
    而不是在测试里另搭一套夹具；`TestProbeDepsIsCapabilityComplete` 是那道的兜底。
40. **写通道挂在 `agent_exit` 上，就必须有人保证它真的被调用**：蓝图的 `blueprint[]` 与便签的
    `handoff` 都只在 `agent_exit` 那一刻被采集，而 `repo.prompt.md` 开头三档 Mode Selection 全在教模型
    "用文本返回答案"，全文一度没有一句要求以 `agent_exit` 收尾。实测 10-17~10-19 的 10 份 Repo rollout：
    **7 份 `stop_reason=plain_text`、3 份 `agent_exit`** —— 前者 handoff 与蓝图一起消失，而且不报错
    （只有 `派生起跑` 一条日志，用户看到的是"目录建了、文件没有"）。两道修复：Repo 的 prompt 加
    Output Contract（答案写完，同一轮调 `agent_exit`）+ 循环补一次收尾提醒（`NoActionRetryLimit=1` +
    `NoActionReminder`，见 §6.2）。闸门是 `TestE2EPlainTextRepoRunIsNudgedIntoSubmitting` 与
    `TestNoSubmissionNote*`（"零提交"现在必须留下一句可区分的话）；变异：把 limit 调回 0，蓝图当场不落盘。
    **通用教训**：把交付押在一个可选工具参数上时，要同时回答"模型不调它会发生什么"，
    并且让那次"没发生"在日志里可读。
41. **`testdata/` 是 Go 工具链的盲区**：R2 的 8 个 fixture 写着 `const F08 = 08`（非法八进制），
    而 `go build ./...` / `go vet ./...` / `go test ./...` **全都不看** `testdata/` 里的文件 ——
    只有 `gofmt -l <目录>` 会报（它按目录走，不按包）。所以扫描器/树遍历类的 fixture
    落地后请顺手跑一次 `gofmt -l`；"编译过了"不等于"夹具是能编译的 Go"。
42. **"像路径"不等于"是路径断言"**：I14 的散文成员校验原本对任何含 `/` 的 token 要求归属，
    而中文技术散文里的斜杠多半是标识符列表（`Name/Brief/Effect/Frequency`、`Cold/Hot`），
    少写前缀的真路径（`toolspec/schemas.json`、裸 `schemas.json`）又因为 `ModuleOf` 只沿目录前缀上溯
    而永远落到根片 —— 三条提交全被判成 `path-not-in-module`，一条被拒就丢整条 claim，
    全丢 ⇒ fold `NoOp` ⇒ **蓝图文件从未出现**（`app-2026-10-19.log:179-182` 就是这个形状）。
    最坏的地方在于症状与"模型根本没提交"（坑 40）**逐字相同**：目录建了、文件没有，
    差别只在日志里那两行 WARN 有没有出现。修法见 `internal/blueprint/prosepath.go`：
    先问"它到底指哪儿"（含写短的前缀与裸文件名），再问"仓库里有没有这个东西"，
    两者都判不了就**弃权**（同名多处、凭空路径 —— 存在性由 `where` 锚点管，那是第 5 条的活）。
    **通用教训**：任何"按形状识别 + 违规即丢弃"的校验，夹具里必须放一条**与违规串同形的正常散文**，
    否则它只会在一堆永远不会出现在生产里的路径上绿灯（`TestProsePathMustBelongToModule` 原本三条
    全是空格分隔英文 + 从根写全的路径，假阳性形态一条都没触发过）。闸门：`fold_prose_test.go` 四形
    + `TestE2ERepoRunWritesBlueprintFile`（散文形状照生产那轮）；三条变异见设计稿 §21.5。
43. **防线生效过但没人知道它生效过，和静默丢失只差一步**：修完坑 42 再跑一轮，症状一模一样
    （目录建了、文件没有），账目却换成了 `outside-evidence-set` ×4 —— 那轮"快速架构讲解"一次文件
    都没读，模型照着注入的 45 片目录名编了 4 句 `purpose`，I17 拦它是**对的**。要修的是下一环：
    `agent_exit` 是终止点，fold 的拒收只进 `slog`，所以 Director 看到的仍是一次成功的委派，
    模型也不知道自己白交了，下一轮还会这么干。现在 `BlueprintCommit.Commit` 返回一行账目，
    经 `AgentResult.BlueprintAccount` 同时到 `FormatForDirector`（这次决策看的地方）与便签正文的
    `> [blueprint]` 尾注（下次委派与索引重扫看的地方），**不进 conclusion**（程序的账目混进结论
    = 替模型下了一句架构判断）。两处都得写：工具输出会被压缩卷走，便签正文不进索引，
    只修一边另一边就是新的静默。三条变异见设计稿 §21.6。
    **通用教训**：任何"可选写通道 + 终止型工具"的组合，都要问"被拦下来的那一次，谁看得见"。

44. **动态规格不进全局注册表**：`toolspec.registry` 写着"init() 期写入，之后只读"，把 MCP 工具塞进去会同时
    打穿三处 ——（a）`TestAgentSurfacesGolden` 走 `ProbeDeps()`，探针不知道有哪些 server，golden 会钉一条
    生产不存在的路径（坑 39 的变体）；（b）`renderCustomAgentToolPool` 遍历 `toolspec.Specs()`，Custom Agent
    于是拿到第三方能力，而 `Palette` 的全部意义就是"拿不到 Director 没有的能力"；（c）`Validate()` 的
    双向 1:1 会对每个外部工具报"schemas.json 里没有对应声明"。修法不是放宽校验，是让动态规格走
    `Deps.ExternalSpecs`，在 `Surface.rebuild()` 汇合（`TestStaticRegistryNeverCarriesExternalTools` 钉住）。
    **没 annotations 一律按写处理**是同一条纪律的安全侧：判错只是多问一次授权，判对则是第三方拿到一个
    不受守卫的写入口 —— 放宽必须写配置（`read_only` / `allow_write`），不能靠猜。
45. **"连接落定前收到的通知"必须补一次**：`tools/list_changed` 可能在 `initialize` 与 `markConnected` 之间到达，
    而这时 Manager 的 `entry.client` 还是 nil，重发现会被守卫丢掉。`MCPClient` 用
    `listChangedPending` 记住它、`markConnected` 事后补一次。另外通知回调**不能**在 readLoop 里同步调用
    （回调里会发 `tools/list`，要等 readLoop 读响应 —— 那就是"读循环等自己"的死锁，表现是重发现永远超时）。


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
