# Yagent Agent 开发知识库

> **本文件是 Agent 理解仓库和进行开发的标准指南。** 所有 Agent 在操作此仓库前必须先阅读本文件。
> 本文件描述的是当前代码的真实状态（核对时间 2026-10-13）。文件末尾由 codeseek 自动维护的注入区块除外。

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
| 配置 | `BurntSushi/toml` + `fsnotify` | 配置解析、热加载（词典） |
| Diff | `aymanbagabas/go-udiff` | `search_replace_in_file` 返回 unified diff |
| 其他 | `google/uuid`、`lucasb-eyer/go-colorful`、`rivo/uniseg` | ID、配色、字宽 |

### 2.2 外部依赖

`ripgrep`（已内嵌，见 `dist/bin`）、`fzf`（模糊搜索）、`codeseek` 二进制（默认从 `dist/bin` 解出到 `~/.yagent/bin`）、
`browser-control`（Rust CLI，**刻意不内嵌**：按 `[browser] binary_path` → `~/.yagent/bin/browser-control` → `$PATH` 依次查找）
与 Chrome/Chromium —— 两者缺一，Browser-Agent 即不可用（`delegate_browser` 描述会自动降级）。

### 2.3 LLM 接入

两种 `api_format`：**openai**（默认，OpenAI-compatible）与 **anthropic**（原生 Messages，支持 Extended Thinking）。
Provider 只是 base_url + api_key 的组合，因此小米 MiMo、百炼、硅基流动、DeepSeek、Moonshot、Mistral、Z.ai、OpenRouter、Bedrock、Anthropic 官方均可用。

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
│   │   ├── activity/           # 活动感知 idle/total timeout（Heartbeat / WithIdleTimeout）
│   │   ├── director/           # Planner、Meta 注册表、项目上下文加载、熔断状态、指标
│   │   ├── toolbatch/          # 只读工具并行分批调度（无业务依赖的叶子包）
│   │   ├── git_checkpoint.go   # 每个编码子任务的 git 快照/回滚
│   │   ├── repo_memory.go consolidation_worker.go knowledge_hook.go
│   │   └── *.prompt.md         # 各 Agent 的 System Prompt
│   ├── app/app.go              # Yagent：一次性装配 MCP/工具引擎/守卫/子 Agent/Director
│   ├── browserctl/             # 浏览器执行层：外部 browser-control CLI 的进程/工作区/URL 策略/渲染
│   ├── blackboard/             # 跨 Agent 便签：note(存储) / handoff(结构化交接) / index(派生索引)
│   │                           #   resolve(ContextPack) / gc(生命周期) / board(路径与原子写)
│   ├── artifact/               # 超预算委派结果的落盘存储 + 摘要
│   ├── compression/            # 三档上下文压缩（rule / emergency / ultimate）
│   ├── config/                 # TOML 加载 + 三级 provider 解析 + 原子写回
│   ├── datamanager/            # 任务记忆 JSONL（5s 防抖）+ 索引
│   ├── dict/                   # 补全词典（fsnotify 热加载）
│   ├── diff/                   # unified diff
│   ├── embedbin/               # 从 embed FS 解出二进制（MD5 校验，版本化）
│   ├── globalctx/              # 全局上下文 + Version/BuildTime/GitCommit（ldflags 注入）
│   ├── http/                   # gin REST + melody WebSocket + TaskManager
│   ├── knowledge/              # 知识检索注入 + 固化（走 codeseek MCP）
│   ├── llm/                    # Engine 接口、OpenAI/Anthropic 引擎、Fallback、Router
│   ├── logging/                # slog 全局日志 + 按任务分目录
│   ├── mcp/                    # 自研 stdio JSON-RPC MCP 客户端 + codeseek/knowledge 桥
│   ├── memory/                 # ConversationMemory、rollout 读写（断点续跑的数据源）
│   ├── messaging/              # 事件总线（Dispatcher / Publisher / consumers/）
│   ├── models/                 # 模型目录（dist/data/models.json）：context window + benchmark
│   ├── protocol/               # 由 protocol/agent-events.yaml 生成的事件类型
│   ├── recovery/               # LLM 熔断器
│   ├── skills/                 # SKILL.md 注册表（仅 TUI 斜杠命令消费）
│   ├── thinklink/              # 结构化 <thought> 解析 + 思维日志（驱动 ultimate 压缩）
│   ├── tokenutil/              # tiktoken 封装（EstimateTokens）
│   ├── tools/                  # 工具 Adapter 实现 + workspace_guard + user_confirm
│   ├── tui/                    # Bubble Tea v2 界面（75 个文件）
│   │   ├── anim/ common/ components/ layout/
│   └── util/                   # 崩溃上报等
├── protocol/                   # agent-events.yaml（SSOT）+ 生成的 go/ 与 ts/ 副本
├── scripts/                    # build-centos7 / build-extension / 架构守卫 / protoc-gen-yagent
├── patches/                    # CI 用的 vendored-openssl 补丁
├── dist/                       # 构建产物 + 被跟踪的 models.json（见 §11 坑 2）
├── codeseek/                   # git submodule → iohub/codeseek（Rust 代码智能引擎）
└── docs/                       # ARCHITECTURE.md / config-guide.md / 设计稿
```

> 旧的 `pkg/messaging/` 已迁到 `internal/messaging/`；`internal/compact/` 已重构为 `internal/compression/`。

---

## 4. Agent 角色定义

所有 Agent 实现 `Agent` 接口（`internal/agents/types.go:20`），由 `internal/app/app.go:349-533` 统一构造。
每个 Agent 的 System Prompt 是同目录的 `<name>.prompt.md`，改动行为请同时改 prompt 与代码。

| Agent | 文件 | 定位 | 权限 | Max Steps 默认 |
|-------|------|------|------|----------------|
| **Director** | `director.go` + `director/` | 项目经理/技术主管，唯一对话入口 | 只读 + 委派 | 100 |
| **Coding** | `coding.go` | 高级开发工程师 | 完整读写 + Shell | 150 |
| **Repo** | `repo.go` | 架构师/审计员 | 只读 + 可委派 DevOps | 50 |
| **Chat** | `chat.go` | 通用问答 | 无文件系统访问 | 50 |
| **DevOps** | `devops.go` | 系统管理员 | 只读 + Shell | 50 |
| **Browser** | `browser_agent.go` | 网页操作：经外部 `browser-control` CLI 驱动真实 Chrome | 浏览器动作 + 会话工作区产物（无 Shell、无文件写工具） | 200 |
| **Meta** | `meta.go` | 自定义 Agent 设计器 | 无工具，单次 LLM 出 JSON | 50（重试 5） |
| **Custom** | `director/types.go:60` | Meta 设计后运行时注册 | 由 LLM 选择的子集 | 15 |

**Director 工具**：`delegate_*` + `agent_exit`、`read_artifact`、`read_note`、`list_notes`、`deepthinking`、`ask_user_for_help`，
以及 `director.go:236-287` 里显式列出的只读分析工具（`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`）。

**Coding 工具**：`create_file`、`search_replace_in_file`、`delete_file`、`rename_file`、`list_dir`、`print_dir_tree`、
`read_file`、`search_by_regex`、`run_bash`、`thinking`/`micro_agent`/`deepthinking`/`design`、
`semantic_search`/`query_code_skeleton`/`query_code_snippet`/`find_function_callee`/`find_function_caller`、
`consolidate_knowledge`/`prune_history`、`git_checkpoint_*`（仓库+配置开启时）、`delegate_browser`、`agent_exit`、`ask_user_for_help`。

**Repo 工具**（只读，含调用图）：`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`、
`semantic_search`、`query_code_skeleton`、`query_code_snippet`、`find_function_callee`、`find_function_caller`、
`query_call_graph`、`deepthinking`、`delegate_devops`、知识检索、`agent_exit`。

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
delegate_meta → MetaAgent 单次 LLM 出 JSON → ParseMetaAgentOutput (director/meta_handler.go:100)
  → 校验必填字段 → 按名字建 delegate_<snake_name> Adapter 并追加到 Director 工具表 (director.go:626-689)
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
闸门是 `TestDerivedMetadataMatchesLegacyTables`：新增可并发工具必须进 `signedReadOnlyAdditions` 或 `newTools`
集合并写明理由，否则测试直接红 —— 那两处集合就是"公开承认我扩大了并发面"的地方。

### 5.2 工具总览（schema 见 `internal/toolspec/schemas.json`，50 条）

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
| `agent_exit` `ask_user_for_help` | P | 结束任务（可带 `handoff{conclusion,facts[],open_questions,status}`，见 §6.4）/ 反问用户（confirm、select、input 三种交互） |
| `consolidate_knowledge` `prune_history` | K | 长期知识固化与历史裁剪（codeseek knowledge） |
| `git_checkpoint_create` `git_checkpoint_list` `git_checkpoint_rollback` | G | 编码子任务快照 |
| `read_note` `read_artifact` `list_notes` | R | 读跨 Agent 便签（handoff 块 + 分页正文）/ 超预算产物全文 / 枚举整块板上有哪些结论（boardID 存在时自动注入，`list_notes` 刻意不含正文） |
| `browser_status` `browser_open` `browser_inspect` `browser_snapshot` `browser_click` `browser_fill` `browser_type` `browser_press` `browser_scroll` `browser_select` `browser_tabs` `browser_wait` `browser_screenshot` `browser_eval` | 浏览器 | 经外部 `browser-control` CLI 驱动真实 Chrome；Browser-Agent 持有（详见 `docs/browser-agent-design.md`） |
| `delegate_repo/coding/chat/devops/browser/meta` | 委派 | Director 持有；`delegate_browser` 也在 Coding 上（能力不可用时描述自动降级） |

> **不存在** 名为 `flow_control` 的工具（旧文档有误）；流控制由 `agent_exit` 与 `ask_user_for_help` 承担。

### 5.3 MCP 与 codeseek

- `internal/mcp` 是一个**自研 stdio JSON-RPC 2.0 客户端**（只发 `tools/call`，不调 `tools/list`），
  因此 MCP 侧工具**不会自动暴露**，全部靠 Go 手写桥接并换成 yagent 的工具名：
  `semantic_search→codeseek_search`、`query_code_skeleton→codeseek_skeleton`、`query_code_snippet→codeseek_snippet`、
  `find_function_callee→codeseek_callees`、`find_function_caller→codeseek_callers`、`query_call_graph→codeseek_callgraph`。
- 二进制解析顺序：`[codeseek] binary_path` → `embedbin` 解出的 `~/.yagent/bin/codeseek`；都为空则 MCP 关闭，
  Agent 退化为纯文件工具模式。启动参数默认 `["serve","--mcp"]`；索引在 codeseek 内部异步构建，
  期间 `CallTool` 返回 `ErrIndexBuilding` 并附带"请改用文件工具"的提示。
- 只有一个 MCP server（codeseek）。没有通用多 server MCP 配置。

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

- **子 Agent**：`RunAgentLoop(ctx, ExecutorConfig)`（`agents/executor.go:136`）。
  每步：LLM 重试（指数退避 ≤30s）→ 三档压缩检查 → 带 `llmTimeout` 的 LLM 调用 → 抽取 `<thought>` →
  纯文本回复则注入提醒或结束 → 工具调用交给 `toolbatch` 分批并行 → `agent_exit` 立即返回；
  步数耗尽报错，panic 被 recover 并写 `turn_aborted` rollout。
  `ExecutorConfig` 关键字段：`SystemPrompt / UserInput / Adapters / LLM / MaxSteps / Publisher / StopOnFinish /
  LLMTimeout / StepRetries / SystemAsHuman / NoActionRetryLimit / MaxParallelReadOnlyTools / OnAgentStart / OnAgentExit / OnStepEnd`。
- **Director**：自己的 `director.Planner`（`director/planner_run.go`），额外有熔断器、
  未委派提醒（最多 3 次强制委派 nudges）、`activity.WithIdleTimeout` 包裹的委派超时
  （`[agent] delegate_idle_timeout` / `delegate_total_timeout`）。

### 6.3 路由（auto-routing）

`internal/llm/router.go` 的 **RoutingEngine** 包裹每个 Agent 的引擎，按任务难度把调用路由到
**fast / balanced / high** 三档模型之一：`Client.BuildRouter` 用模型目录里的 benchmark 分数为每个 provider
建三档引擎；`Classify` 用 fast 模型输出 `{"tier": ...}`（30s 上限，失败回落到该角色的 baseline）；
档位通过 context 传递，在 `RunAgentLoop` 里由 `ResolveTierEngine` 解析。
`[routing] mode = "auto" | "fixed"`，`baselines` 可为 director/coding/repo/chat/devops/browser/meta 指定默认档。

> 坑：任何重建 Agent 引擎的地方都必须保留 `*RoutingEngine` 包装，否则 auto-routing 静默失效（commit 0c87c8a）。

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
  - 预算与 GC 在 `[blackboard]`。三个阀门靠**键的存在性**判定（不是零值）：`index_max_notes = 0` 关索引、
    `index_line_chars = 0` 不裁剪行、`ttl = "0"` 不清理；`summary_max_runes` 与 `max_notes_per_board` 的 0/负数一律归默认
    （它们没有"关"的语义，放宽就写大数字）。整体关闭仍是 `YAGENT_BLACKBOARD=0`（`blackboard.Enabled()` 是唯一判据，
    写/读/注入/清理四处共用）。
- **Artifact**（`internal/artifact`）：超过预算（默认 500 token）的委派全文落盘 `~/.yagent/data/artifacts`，
  上下文里只留摘要 + ID（摘要优先来自 handoff，缺 handoff 时逐字回落到正文前缀），Director 用 `read_artifact` 分页取回。`YAGENT_ARTIFACT_DISABLE=1` 关闭。
- **Knowledge**（`internal/knowledge`）：向 Director/Coding/Repo 注入历史知识（写入走 `consolidate_knowledge`，
  自动固化在 `knowledge_hook.go`）。当前**注入器被临时硬关闭**（`injector.go:67-71`）。
- **RepoMemory / ConsolidationWorker**：仓库级长期记忆存在 SharedMemory 的 KV 中，由后台 worker 周期性用 LLM + codeseek 重写。
- **Thinklink**（`internal/thinklink`）：append-only 的思维日志（用户输入 + T&P 块），是 ultimate 压缩重建上下文的原料。

### 6.5 三档上下文压缩（`internal/compression`）

阈值 = 模型目录收录的 `limit.context × 0.75`（目录未命中则回退 `[enhanced_commander] context_compression_threshold`，默认 120000），
每步检查，`enhanced_commander.enable_context_compression` 总开关：

1. **Rule**：`TruncateToolResultsToBudget` — 按优先级先截低价值工具结果（`deepthinking`/`design` 结果受保护），保留最近 `tool_result_keep_tokens`（默认 200）。
2. **Emergency**：抽取 T&P 块进 thinklink，最近 3 条保留原文，其余 LLM 摘要，记忆整体重写为 `[system, one user]`。
3. **Ultimate**：仍超阈值时，用 thinklink 日志重建上下文（保留最近 N 个 T&P，N 由 `ultimate_compression_keep_plans` 决定，0=全部），最后硬截断到预算。

三档都会发 `context_*_compressed` 事件并写 `compacted` rollout 记录。

> **子 Agent 的第三档需要两个条件同时成立**：`ExecutorConfig.EnableUltimateCompression`（来自
> `enable_ultimate_compression`）与非空的 `UltimateThinkLink`，与 Director Planner 同语义
> （`executor.go` 的 T3 分支）。Browser/Repo/DevOps 的 store 在 `Run()` 内按委派创建——
> `RebuildPrompt` 会保留全部用户输入，放构造期会让历次委派的任务串进同一次重建。
> token 估算（`EstimateMessagesTokens`）**包含 `Reasoning`**：thinking/reasoning_content 会被回灌给
> provider，漏算会让压缩触发时机系统性偏晚。

### 6.6 事件总线（`internal/messaging`）

`Event{ID,Type,Source,Target,Content,Priority,Timestamp,Deadline,TraceID,Metadata}` →
`MessageDispatcher`（每个 consumer 一条 1000 容量队列 + 独立 goroutine）。
普通事件队列满则**丢弃并限流告警**；`user_help_needed` / `user_help_response` 走 **critical 旁路队列**（永不丢弃，FIFO，满则丢最旧）。
Consumer 有两种：旧版 `MessageConsumer`（广播）与新版 `Consumer{Types()}`（过滤订阅）。

当前总线上的事件类型（约 21 个，SSOT 是 `protocol/agent-events.yaml`，由 `scripts/protoc-gen-yagent` 生成 Go/TS/JSON-Schema）：

`ai_stream_start` · `ai_chunk` · `ai_stream_end` · `ai_response` · `thinking` ·
`llm_call_start` · `llm_call_end` · `model_info` · `model_routed` ·
`tool_call_start` · `tool_call_result` · `user_help_needed` · `user_help_response` ·
`context_loaded` · `commit_context_loaded` · `context_compressed` · `context_emergency_compressed` ·
`context_ultimate_compressed` · `knowledge_injected` · `thinklink_entry` · `conversation_error` / `conversation_result`

> **新增事件类型要改 `protocol/agent-events.yaml` 再重新生成**，不要手改 `internal/protocol/agent_events.go`（文件头写着 DO NOT EDIT）。
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
| **Rollout（断点续跑的事实来源）** | `~/.yagent/data/rollout/{projectID}/{ts}_{session}[_{agent}].jsonl` | **同步写**，类型含 `session_meta/turn_context/response_item/event_msg/compacted/world_state` |
| Blackboard 便签 | `~/.yagent/notes/{boardID}/{agent}/*.md` | 原子写，`YAGENT_NOTES_ROOT` 可覆盖 |
| Artifact | `~/.yagent/data/artifacts/{id}` | 原子写，ID `^\d{8}-\d{6}-[0-9a-f]{8}$` |
| 共享/仓库记忆 | `~/.yagent/data/shared_memory/{projectID}/shared_memory.json` | 5 秒 ticker + dirty 标记，退出前 flush |
| 授权状态 | `~/.yagent/settings.json` → `authorized_projects` | 持久 |
| 浏览器工作区 | `~/.yagent/data/browser/{delegation}/.browser-control/` | 由 `cmd.Dir` 决定落点（上游状态路径是 CWD 相对）；含 current-tab、chrome.pid、traces、截图；`[browser] workspace_ttl` 清理 |
| 日志 | `~/.yagent/logs/`（`app-`、`llm-`、`llm-error-`、`tool-`、`delegate-`、`knowledge-*`，按任务分子目录） | 追加 |

**`/resume`** 列出 rollout 会话 → `ReadRolloutMemory` 重建 Memory（修复工具调用配对）→ 恢复 thinklink 条目，
被打断的任务标记为失败并提示；下一轮以 `resumed=true` 强制重注入项目上下文。

### 6.8 TUI（`internal/tui`，Bubble Tea **v2**）

- **状态**：`tui_model.go`（logEntry + 渲染缓存 + keymap + `eventCh(1000)`）；更新逻辑按 `tui_update_*.go` 拆分；
  渲染按 `tui_view.go` / `tui_render.go` / `render.go` / `tui_chrome.go` / `tui_tabs.go` / `tui_dashboard.go`。
- **叠加模式**：timeline 全屏、thinklink 全屏、history、resume、dialog 栈、主页面——按此优先级渲染。
  每层都必须正常消费 `taskEventMsg` 并重新武装 `listenForEvents`（该 cmd 是一次性的，事件没有重放队列）。
- **thinklink 全屏**：条目来自 `thinklink_entry` 事件（payload 的 `agent` 字段 = 发布方 Agent 名），
  `f` 在 All ↔ 各 Agent 间过滤（`RebuildPrompt` 之外，一次委派里 Director/Coding/Browser 的块会混排，
  Browser 一轮可贡献上百条）。`/resume` 恢复的旧 rollout 没有 Agent 标记，只在 All 视图出现。
- **Dialog**：`components.DialogStack`（Push/Pop/ReplaceTop/Overlay），10 个对话框；
  弹窗打开时全局守卫只放行 KeyMsg/taskEventMsg/tickMsg，新增消息类型必须显式放行（`tea.PasteMsg` 的坑已踩过）。
- **模态编辑**：Esc→NORMAL、`i`→INSERT、`:` 命令行、`/` 真搜索、textarea 用 `SetVirtualCursor(true)`、支持 `10j` 数字前缀。
- **快捷键提示**只出现在 tab 行右侧与各面板底边框，**状态栏不含任何键帽**。
- **设计语言**：圆角手绘边框（`tui_chrome.go` 的 `╭╮╰╯─▌`），配色令牌集中在
  `common/styles.go`（`Styles`/`Theme`，深浅双主题）、`styles.go`（工具背景 16 色哈希、Agent 前景色）、
  `common/grad.go`（渐变）。强调色：`141`（lavender，活动 tab / thinklink / LLM 调用）、`62`（INSERT、滚动条、面板边框）、
  `214`（命令）、`39`（搜索）、`76`（运行中）、`235/250`（键帽）。
- **i18n**：`i18n.go` 一张 ~120 字段翻译表，zh/en，默认英文，仅通过 TUI 内切换（无配置项）。
- **测试约定**：断言**精确单元格宽度**而非子串；宽度矩阵 `{30..240}`、终端尺寸矩阵 `{20..240}`；
  共享 fixture `newTestModel()` / `chromeTestModel(t,w,h)`；主要闸门是 `tui_chrome_test.go`、
  `tui_paste_test.go`、`components/user_help_dialog_render_test.go`、`tui_resume_restore_test.go`。

### 6.9 HTTP

`POST /api/start_task`、`GET /api/task_status`、`GET|DELETE /api/memory`、`GET /api/memory/:type`、
`GET /api/history`、`POST /api/load_task`、`POST /api/cancel_task`、`GET /ws`。
WebSocket 用 melody，每个任务一个 Dispatcher + `WebSocketConsumer`，向所有连接广播 `realtime` envelope（无按会话过滤）。

---

## 7. 配置系统

**解析顺序**：`--config` 指定路径（不自动创建）→ `~/.yagent/config/config.toml`（不存在则用内嵌模板生成）→ `./config/config.toml`。
仓库里的 `config/config.toml` 是注释模板；运行时写回（改 provider/超时/路由模式）走 `config/persist.go` 的原子替换。

**顶层表**：`[global.llm]`、`[agents.llm]`、`[tools.llm]`、`[app]`、`[agent]`、`[llm]`、`[routing]`、
`[keywords]`、`[git_checkpoint]`、`[codeseek]`（含 `[codeseek.knowledge]`）、`[browser]`、`[blackboard]`、`[enhanced_commander]`、`[tui]`，
以及顶层 `task_timeout`。

**三级 Provider 覆盖（仍存在）**：`ResolveProvider(agent, tool)` = 工具指定 → `tools.llm.use_provider` →
Agent 指定 → `agents.llm.use_provider` → `global.llm.use_provider`。

```toml
[global.llm]
use_provider = "bailian"
[global.llm.providers.bailian]
model = "qwen3-coder"          # 必填
api_base_url = "https://..."    # 与 api_key 一起必填（bedrock 用 aws_region）
context_window = 262144         # 只影响 TUI 上下文进度条；压缩阈值走模型目录（见 §11 坑 3）
temperature = 0.3
max_tokens = 28000
reasoning_effort = "high"       # anthropic 时映射为 thinking budget
api_format = "anthropic"        # 默认 openai
fallback_providers = ["deepseek", { provider = "mimo", weight = 2 }]

[agents.llm]  coding.use_provider = "deepseek"
[tools.llm]   micro_agent.use_provider = "local"   # 另有 thinking / deepthinking / design

[agent]
director_max_steps = 100  coding_max_steps = 150  repo_max_steps = 50
chat_max_steps = 50  devops_max_steps = 50  browser_max_steps = 200  meta_max_steps = 50
delegate_idle_timeout = "60s"  delegate_total_timeout = "20m"
max_parallel_readonly_tools = 4
lang = "Chinese"

[llm]   timeout / max_retries(5) / step_retries / circuit_breaker_threshold / enable_fallback / fallback_max_retries(2)
[routing] mode = "auto"  [routing.baselines]  coding = "high"  repo = "balanced"
[enhanced_commander] enable / max_delegation_depth(3) / enable_context_compression
  context_compression_threshold / tool_result_keep_tokens(200) / enable_ultimate_compression / ultimate_compression_keep_plans
[browser] enabled / binary_path / chrome_path / mode(headless|headful|attach) / cdp_url / cdp_port_base(9333)
  cdp_timeout(65s) / command_timeout(75s) / idle_ttl(15m) / max_chrome(1) / workspace_root / workspace_ttl(72h)
  max_output_bytes(24000) / dialog_policy(accept) / allowed_domains / denied_domains / profile_dir / enable_skills
```

> `[browser]` **整表缺省**（老配置没有这一段，或只剩旧浏览器栈的失效键 `headless` / `browser_path`）时，
> 按 `config.DefaultBrowserConfig()` 填默认值。判据是段内是否存在**至少一个被新 schema 认识的键**
> （`config.go` 的 `browserSectionConfigured`），不能用 `enabled` 的零值判断——它缺省就是 false，
> 会把新能力静默关掉；失效键会被 `slog.Warn` 点名提示改写。

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
go test ./... -count=1 -timeout 600s                  # 全仓 21 个测试包，约 80s
go test ./internal/tui/... ./internal/messaging/... -count=1   # 快循环，秒级
go test ./internal/agents/... -count=1 -timeout 300s  # 最慢的一档，约 40s
go vet ./...                                          # 必须干净
```

- 共 82 个测试文件、21 个有测试的包。慢的只有两个：`internal/agents`（~39s）与 `internal/compression`（~28s）。
  历史上 `TestRunAgentLoop_ParentCancel_AllCallsAccounted` 会卡在 `toolbatch.Run`：它把 `cancel()`
  放在批次内的第一个工具里，而 `toolbatch` **不保证组内启动顺序**，负责取消的工具一旦排在信号量队列里，
  在飞调用就无人可取消，只能等 `defaultToolTimeout`(180s) 逐波解开。现在改成从批次外部取消
  （`time.AfterFunc`），写并发工具测试时不要重现这个假设。
- **没有 CI 质量闸门**：`.github/workflows/release.yml` 只做跨平台构建 + 发布，不跑测试/lint；也没有 Makefile/golangci。
  验证靠本地 `go build` + `go vet` + 相关包测试。
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
bash scripts/check_file_sizes.sh          # internal/agents/director/ 下非测试文件 ≤300 行
bash scripts/check_no_agents_import.sh    # director/ 子包不得 import yagent/internal/agents（防循环依赖）
```

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
3. **模型目录与配置里的 `context_window` 会打架，且两个消费者取法相反**：
   - 压缩阈值（`compression/threshold.go:19`）= **目录命中优先** → 未命中才用 `context_compression_threshold`（默认 120000）；
     **provider 的 `context_window` 完全不参与**。
   - TUI 上下文进度条（`tui/tui_model.go:600`）= **provider 的 `context_window` 优先** → 未设置才查目录。
   所以给目录里已收录的模型手写一个不同的 `context_window`，会出现"压缩时机与进度条口径不一致"。
4. **`default_config.toml` 里有一段 `[context]` 表**（`enable_auto_compact` 等）`Config` 根本不解析；真正的开关在 `[enhanced_commander]`。别照着模板那段改配置。
5. **声明 ≠ 可用（已消解）**：`Registry.Validate()` 双向对账 schemas.json 与 Spec，"声明了没人实现"
   是启动错误；工具面由画像派生，golden 基线（`internal/agents/testdata/tool_surfaces.golden`）钉住
   每个 Agent 实际拿到什么。加工具若只改 schemas.json 不改 Spec，`check_no_manual_lookup.sh` 与
   `TestRegistryMatchesSchemas` 会立刻报。
   另：`mcp` 客户端的 `sendJSON` 已加写锁 —— 并发只读工具（codeseek 桥接那批现在可并发了）依赖它。
6. **TUI 事件通道无重放**：`listenForEvents` 是一次性 cmd，谁消费了事件不重新武装监听器就会永久断流；
   弹窗守卫只放行少数消息类型（`tea.PasteMsg` 不是 KeyMsg）。任何新增叠加模式/守卫都必须显式放行需要的消息类型并 re-arm。
7. **布局高度必须自洽**：`computeFooterHeight` 与 `View()` 的 footer 组装顺序要完全一致，差一个空行整屏抖动；
   `viewport.SetHighlights` 在我们的重着色日志上会算错偏移，改用 `StyleLineFunc`。
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
