# Yagent Agent 开发知识库

> **本文件是 Agent 理解仓库和进行开发的标准指南。** 所有 Agent 在操作此仓库前必须先阅读本文件。
> 本文件描述的是当前代码的真实状态（核对时间 2026-10-15：落地"停止/继续"三层
> （`docs/pause-resume-design.md`）—— P1 活暂停（`internal/pause` 闸门、`activity` 计时冻结、
> 两个循环的 step 边界接入点、`paused` 停止原因、TUI `/pause` `/continue` 与 HTTP pause/continue）；
> P2 检查点重放 B 层（`delegation_id` 串父子 rollout、`agent_checkpoint` 记录、`ReadRolloutForReplay`、
> executor 的 `ResumeState` 接缝、planner 的 `PendingResumption`、八条降级判据、`[resume]` 配置）；
> P3 交接报告 C 层（不叫 LLM 的降级报告 + `status=degraded` 便签）；
> 上一轮 2026-10-14 补齐核心系统单测基线，并修掉三处"错了不报错"的静默失效 ——
> Bedrock 配置归一化、Anthropic thinking budget、流式 tool_use 装配）。
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

`reasoning_effort` 在两侧含义不同：**openai** 侧原样透传 `reasoning_effort` 并额外发
`thinking={type:enabled}`（DeepSeek 思考模式要求两者都在）；**anthropic** 侧映射成 `thinking.budget_tokens`
—— `low|medium|high|max` = 1024/8192/24576/32000，同时把 `max_tokens` 顶到 `budget+4096`、
`temperature` 强制 1.0（provider 在思考模式下拒绝其它值）。**认不出的取值一律当作"没开思考"**：
以前会把 `budget_tokens: 0` 发出去，而 provider 要求 ≥1024，一个拼错的 effort 就让此后每次调用都 400。

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
│   │                           #   rollout_replay.go(重放+指纹) / rollout_checkpoint.go(坐标+工作区) / rollout_ctx.go(delegation_id)
│   ├── messaging/              # 事件总线（Dispatcher / Publisher / consumers/）
│   ├── models/                 # 模型目录（dist/data/models.json）：context window + benchmark
│   ├── protocol/               # 由 protocol/agent-events.yaml 生成的事件类型
│   ├── pause/                  # 暂停闸门（A 层）：Gate + ctx value + AbandonedError，只依赖 activity
│   ├── recovery/               # LLM 熔断器
│   ├── skills/                 # SKILL.md 注册表（仅 TUI 斜杠命令消费）
│   ├── thinklink/              # 结构化 <thought> 解析 + 思维日志（驱动 ultimate 压缩）
│   ├── tokenutil/              # tiktoken 封装（EstimateTokens）
│   ├── tools/                  # 工具 Adapter 实现 + workspace_guard + user_confirm
│   ├── tui/                    # Bubble Tea v2 界面（78 个文件）
│   │   ├── anim/ common/ components/ layout/
│   └── util/                   # 崩溃上报等
├── protocol/                   # agent-events.yaml（SSOT）+ 生成的 go/ 与 ts/ 副本
├── scripts/                    # build-centos7 / build-extension / protoc-gen-yagent
│                               #   + 五把守卫（check_*.sh），其中 check_all.sh 是一条命令的本地闸门
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
- **Knowledge**（`internal/knowledge`）：向 Director/Coding/Repo 注入历史知识（写入走 `consolidate_knowledge`，
  自动固化在 `knowledge_hook.go`）。当前**注入器被临时硬关闭**（`injector.go:67-71`）。
- **RepoMemory / ConsolidationWorker**：仓库级长期记忆存在 SharedMemory 的 KV 中，由后台 worker 周期性用 LLM + codeseek 重写。
- **Thinklink**（`internal/thinklink`）：append-only 的思维日志（用户输入 + T&P 块），是 ultimate 压缩重建上下文的原料。

### 6.5 三档上下文压缩（`internal/compression`）

阈值 = 模型目录收录的 `limit.context × 0.75`（目录未命中则回退 `[enhanced_commander] context_compression_threshold`，默认 120000），
每步检查，`enhanced_commander.enable_context_compression` 总开关。
调试例外：`YAGENT_COMPRESS_THRESHOLD` 一旦设了就**直接当阈值**，压过目录与配置 ——
目录收录的模型动辄 1M 窗口（阈值 750k），想在真机上肉眼验收压缩行为只能靠它；生产不要用。

1. **Rule**：`TruncateToolResultsToBudget` — 按优先级先截低价值工具结果（`deepthinking`/`design` 结果受保护），保留最近 `tool_result_keep_tokens`（默认 200）。
2. **Emergency**：抽取 T&P 块进 thinklink，最近 3 条保留原文，其余 LLM 摘要，记忆整体重写为 `[system, one user]`。
3. **Ultimate**：仍超阈值时，用 thinklink 日志重建上下文（保留最近 N 个 T&P，N 由 `ultimate_compression_keep_plans` 决定，0=全部），最后硬截断到预算。

三档都会发 `context_*_compressed` 事件并写 `compacted` rollout 记录。

> `enable_ultimate_compression` **今天关不掉**：`config.validate()` 见 false 就写回 true，所以这个键
> 只剩"默认开"的效果，写 false 不会有任何后果（两个 TOML 模板都把它当可配项在说，别被骗）。
> 现状由 `internal/config/validate_test.go:TestLoadFromFile_UltimateCompressionIsForcedOn` 钉住；
> 想改成尊重配置，就改写那条用例并同步本节。
>
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
`context_ultimate_compressed` · `knowledge_injected` · `thinklink_entry` · `conversation_error` / `conversation_result` ·
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
| **Rollout（断点续跑的事实来源）** | `~/.yagent/data/rollout/{projectID}/{ts}_{session}[_{agent}].jsonl` | **同步写**，类型含 `session_meta/turn_context/response_item/event_msg/compacted/agent_checkpoint`（`world_state` 已于 2026-10-14 删除）；`session_meta` 带 git 身份 + `delegation_id` + `parent_session_id`，终止事件带 `stop_reason`（词表在 `memory/rollout_types.go`：`paused/cancelled/idle_timeout/total_timeout/max_steps/llm_error/circuit_breaker_open/panic/plain_text/agent_exit/unaccounted`）。子 Agent 的文件名后缀 `_{agent}` 就是"这次委派是哪一个"的凭据 |
| Blackboard 便签 | `~/.yagent/notes/{boardID}/{agent}/*.md` | 原子写，`YAGENT_NOTES_ROOT` 可覆盖 |
| Artifact | `~/.yagent/data/artifacts/{id}` | 原子写，ID `^\d{8}-\d{6}-[0-9a-f]{8}$` |
| 共享/仓库记忆 | `~/.yagent/data/shared_memory/{projectID}/shared_memory.json` | 5 秒 ticker + dirty 标记，退出前 flush |
| 授权状态 | `~/.yagent/settings.json` → `authorized_projects` | 持久 |
| 浏览器工作区 | `~/.yagent/data/browser/{delegation}/.browser-control/` | 由 `cmd.Dir` 决定落点（上游状态路径是 CWD 相对）；含 current-tab、chrome.pid、traces、截图；`[browser] workspace_ttl` 清理 |
| 日志 | `~/.yagent/logs/`（`app-`、`llm-`、`llm-error-`、`tool-`、`delegate-`、`knowledge-*`，按任务分子目录） | 追加 |

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
`GET /api/history`、`POST /api/load_task`、`POST /api/cancel_task`、
`POST /api/pause_task`、`POST /api/continue_task`、`GET /ws`。
任务状态四档不变，另加 **`paused`**（`TaskStatusPaused`）：暂停闸门不产生 task_complete，
所以旧四档里没有一个能表达"停住但可继续"。`http.Task.Gate()` 从 ctx 读回闸门，
不在结构体里另存一份（两份状态 = 一处忘了换 = `/pause` 停错对象且不报错）。
WebSocket 用 melody，每个任务一个 Dispatcher + `WebSocketConsumer`，向所有连接广播 `realtime` envelope（无按会话过滤）。

---

## 7. 配置系统

**解析顺序**：`--config` 指定路径（不自动创建）→ `~/.yagent/config/config.toml`（不存在则用内嵌模板生成）→ `./config/config.toml`。
仓库里的 `config/config.toml` 是注释模板；运行时写回（改 provider/超时/路由模式）走 `config/persist.go` 的原子替换。

**顶层表**：`[global.llm]`、`[agents.llm]`、`[tools.llm]`、`[app]`、`[agent]`、`[llm]`、`[routing]`、
`[keywords]`、`[git_checkpoint]`、`[codeseek]`（含 `[codeseek.knowledge]`）、`[browser]`、`[blackboard]`、`[resume]`、`[enhanced_commander]`、`[tui]`，
以及顶层 `task_timeout`。

**三级 Provider 覆盖（仍存在）**：`ResolveProvider(agent, tool)` = 工具指定 → `tools.llm.use_provider` →
Agent 指定 → `agents.llm.use_provider` → `global.llm.use_provider`。

```toml
[global.llm]
use_provider = "bailian"
[global.llm.providers.bailian]
model = "qwen3-coder"          # 必填
api_base_url = "https://..."    # 与 api_key 一起必填；豁免看 §11 坑 14（按 provider 名字前缀，不看 api_format）
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
chat_max_steps = 50  devops_max_steps = 50  browser_max_steps = 200  meta_max_steps = 50
delegate_idle_timeout = "60s"  delegate_total_timeout = "20m"
max_parallel_readonly_tools = 4
lang = "Chinese"

[llm]   timeout / max_retries(5) / step_retries / enable_fallback / fallback_max_retries(2)
  circuit_breaker_threshold = 0 意为**不启用**（仓库模板与 defaults.go 都是 0）；
  只有 > 0 才创建熔断器，此时 reset_timeout 缺省补 60s
[routing] mode = "auto"  [routing.baselines]  coding = "high"  repo = "balanced"
[enhanced_commander] enable / max_delegation_depth(3) / enable_context_compression
  context_compression_threshold / tool_result_keep_tokens(200) / enable_ultimate_compression(写 false 也关不掉，见 §6.5) / ultimate_compression_keep_plans
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
bash scripts/check_all.sh                 # 一条命令的本地闸门：build + vet + 五把守卫 + 全量单测
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
  只把 LLM 换成脚本。harness 的接线顺序照抄 `app.go:115-287` —— 换顺序会测到一条生产不存在的路。
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
- **计时类 seam 只有一个**：`datamanager.taskFlushDebounce`（5s 防抖窗口）。凡是"等 timer"的
  测试都必须走这个变量而不是 `time.Sleep`，同 `tools.defaultToolTimeout` 一个道理。
  新增 timer 请先加 seam，再让测试自己负责耗时。
- **耗时必须由测试自己负责**：历史上 `internal/compression` 的 28s 全部来自五个用
  `strings.Repeat("x", 50000)` 造工具结果的截断测试（tiktoken 反复编几百 KB）——payload 缩到
  1-2k 并等比缩预算后整包 0.17s，断言语义不变。`internal/agents` 的 30s 来自
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
bash scripts/check_all.sh                 # 上面五把守卫 + build + vet + 全量单测，一条命令跑完
bash scripts/check_file_sizes.sh          # internal/agents/director/ 下非测试文件 ≤300 行（存量按 PENDING 冻结）
bash scripts/check_no_agents_import.sh    # director/ 子包不得 import yagent/internal/agents（防循环依赖）
bash scripts/check_toolspec_leaf.sh       # toolspec 不得反向依赖 agents/tools
bash scripts/check_no_manual_lookup.sh    # 不许重新出现按名字查工具的 lookupToolFunc/getToolFunc
bash scripts/check_test_hygiene.sh        # 单测卫生棘轮，见下
```

- `check_file_sizes.sh` 现在是**棘轮而不是硬门**：存量超限项在脚本顶部的 `PENDING` 里
  点名并记下"当前行数"作为冻结上限（今天只有 `director/planner_run.go:658`，主循环 +
  压缩 + 委派编排三段职责，拆分是独立一次改动）。规则：新文件超 300 行直接红；
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
   - 压缩阈值（`compression/threshold.go` 的 `ResolveThreshold`）= **目录命中优先** → 未命中才用 `context_compression_threshold`（默认 120000）；
     **provider 的 `context_window` 完全不参与**。
   - TUI 上下文进度条（`tui/tui_model.go` 的 `contextWindowFromCfg`）= **provider 的 `context_window` 优先** → 未设置才查目录。
   所以给目录里已收录的模型手写一个不同的 `context_window`，会出现"压缩时机与进度条口径不一致"。
   **想临时压低阈值做验收**只能靠 `YAGENT_COMPRESS_THRESHOLD`（目录命中时配置项是被跳过的，改配置不起作用）；
   它是纯调试入口：非法值原样回落，取值不缓存，生产配置不要用 —— 会让压缩早于模型真实容量触发，白丢上下文。
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
14. **Bedrock 的必填项豁免按 `use_provider` 名字前缀判定，不看 `api_format`**：`config.validate()` 里是
    `strings.HasPrefix(effectiveProvider, "bedrock")` —— 名字不带 bedrock 的真 Bedrock 配置照样会要求
    `api_key`/`api_base_url`，名字带 bedrock 的反而会跳过这两项。更要命的是这段豁免和**整段缺省归一化**
    挤在同一个函数里：以前它写成 `return nil`，于是 Bedrock 用户的 max steps、`[browser]` 缺省、
    TUI 键位、压缩参数全部停在零值（表现不是报错，是能力静默缺失）。现在只豁免那两项检查，
    归一化照常跑；回归由 `internal/config/validate_test.go:TestLoadFromFile_BedrockSkipsOnlyKeyChecks` 钉住。
    往 `validate()` 里加提前 return 之前，先确认下面的归一化段还会被执行。
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
