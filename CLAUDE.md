# CLAUDE.md — CodeActor Agent

> Detailed architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
> Testing methodology: [docs/TESTING.md](docs/TESTING.md)

## Project Overview

Go-based multi-agent AI coding assistant using **Hub-and-Spoke** architecture. The central **ConductorAgent** orchestrates specialized sub-agents (RepoAgent / CodingAgent / ChatAgent / MetaAgent) via delegate tools to perform code analysis, planning, writing, and self-correction.

## Build & Run

```bash
go build -o codeactor .

# TUI mode
./codeactor tui [--taskfile TASK.md] [--disable-agents=repo,coding,chat,meta]

# HTTP server mode (default port 9080)
./codeactor http [--disable-agents=repo,coding,chat,meta]
```

`--disable-agents` accepts a comma-separated list of agent names to exclude from the Conductor's tool list. Useful for isolating a specific sub-agent during debugging (e.g. `--disable-agents=repo,coding,chat` to test only MetaAgent).

### CLI Client (Node.js)

```bash
cd clients/nodejs-cli && npm install
node index.js run <project-dir> "task description"  # create & stream task
node index.js chat <task-id> <project-dir>           # continue conversation
node index.js status <task-id>                       # query status
node index.js memory <task-id>                       # view conversation history
```

Server defaults to `localhost:9080`. Override via `--host`/`--port` or `CODECACTOR_HOST=host:port`.

## Tech Stack

- **Language**: Go 1.24+, module `codeactor`
- **LLM**: `github.com/openai/openai-go/v3` (multi-provider: OpenAI-compatible, Bedrock)
- **HTTP/WS**: `gin-gonic/gin` + `olahol/melody`
- **TUI**: Bubble Tea
- **External**: `codeactor-codexray` (Rust, `127.0.0.1:12800`) — semantic search, repo investigation, call graph, code skeleton/snippet. See [Codexray Component](#codexray-component) below.
- **System deps**: `ripgrep` (rg), `fzf`

## Project Structure

```
codeactor-agent/
├── main.go                      # Entry point, CLI parsing, start codexray service
├── internal/
│   ├── app/
│   │   └── app.go               # CodeActor: agent orchestration & init
│   ├── agents/                  # Agent implementations (flat files)
│   │   ├── conductor.go         # ConductorAgent: hub coordinator
│   │   ├── conductor.prompt.md  # Conductor system prompt
│   │   ├── coding.go            # CodingAgent: code writer/editor
│   │   ├── coding.prompt.md     # Coding system prompt
│   │   ├── repo.go              # RepoAgent: codebase analyst
│   │   ├── repo.prompt.md       # Repo system prompt
│   │   ├── chat.go              # ChatAgent: general Q&A
│   │   ├── chat.prompt.md       # Chat system prompt
│   │   ├── devops.go            # DevOpsAgent: shell/system ops
│   │   ├── devops.prompt.md     # DevOps system prompt
│   │   ├── meta.go              # MetaAgent: custom agent designer
│   │   ├── meta.prompt.md       # Meta system prompt
│   │   ├── executor.go          # Generic agent execution loop (RunAgentLoop)
│   │   ├── tools.go             # Tool registration helpers
│   │   ├── tools.json           # Tool definitions
│   │   └── types.go             # BaseAgent, shared types
│   ├── llm/
│   │   ├── engine.go            # Engine interface + LoggingEngine wrapper
│   │   ├── engine_openai.go     # OpenAI-compatible engine (openai-go/v3)
│   │   └── llm.go               # LLM client, provider registry (13+ providers)
│   ├── tools/                   # 17 tools (Adapter pattern)
│   │   ├── adapter.go           # Adapter: ToolFunc → langchaingo Tool interface
│   │   ├── file_operations.go   # read_file, create_file, delete_file, rename_file, list_dir, print_dir_tree
│   │   ├── file_edit.go         # search_replace_in_file (unified diff, 10MB limit)
│   │   ├── search_operations.go # search_by_regex (ripgrep), file_search (fzf)
│   │   ├── repo_operations.go   # semantic_search, query_code_skeleton, query_code_snippet
│   │   ├── system_operations.go # run_bash (foreground/background)
│   │   ├── cognitive.go         # thinking (error analysis & reflection)
│   │   ├── micro_agent.go       # micro_agent (sub-LLM reasoning)
│   │   ├── deepthinking.go      # deepthinking (deep system analysis)
│   │   ├── flow_control.go      # agent_exit, ask_user_for_help
│   │   ├── workspace_guard.go   # Workspace boundary enforcement
│   │   └── user_confirm.go      # User confirmation pipeline (Pub-Sub)
│   ├── compact/                 # Context compression engine
│   │   ├── engine.go            # Compression engine (conservative/balanced/aggressive)
│   │   ├── compressor.go        # Rule-based compressor
│   │   ├── summarizer.go        # LLM-based summarizer
│   │   ├── tokenizer.go         # Token counter
│   │   ├── priority.go          # Message priority calculator
│   │   └── compact_config.go    # Compression configuration
│   ├── http/                    # REST API + WebSocket server
│   │   ├── server.go            # Gin server, route registration
│   │   ├── task_executor.go     # Task execution orchestration
│   │   ├── task_manager.go      # Task lifecycle management
│   │   ├── types.go             # Request/Response types
│   │   └── websocket.go         # WebSocket handler (melody)
│   ├── tui/                     # Bubble Tea terminal UI (重构中，参见 TUI_REFACTOR.md)
│   │   ├── model.go             # UI struct, 顶层 Model
│   │   ├── root_update.go       # 状态机 Update 分发
│   │   ├── root_view.go         # 基于布局的 View 渲染管道
│   │   ├── layout.go            # 布局引擎 (UILayout, Rect, computeLayout)
│   │   ├── state.go             # 状态枚举 + 状态机 (UIState, UIFocusState)
│   │   ├── keys.go              # 集中化快捷键 (KeyMap)
│   │   ├── chat/                # 消息项渲染器 (Chat, MessageItem 接口)
│   │   ├── list/                # 通用虚拟化列表
│   │   │   └── list_test.go     # 11 项测试
│   │   ├── common/              # 通用 UI 组件 + 样式
│   │   │   ├── styles.go        # Styles 主题系统 (Dark/Light)
│   │   │   └── elements.go      # Header, StatusBar, DialogTitle, Button
│   │   ├── dialog/              # 弹窗系统
│   │   ├── components/          # 弹窗底层实现 (DialogStack, 7 种 Dialog)
│   │   ├── sidebar/             # 侧边栏 (Token 统计, Skills, Model info)
│   │   ├── input/               # 输入处理 (命令解析, 补全)
│   │   ├── anim/                # 动画系统 (可见性感知帧管理)
│   │   ├── diffview/            # Diff 查看器
│   │   ├── layout/              # 布局引擎 (骨架, layout.go 为实际使用)
│   │   ├── completion.go        # 补全系统
│   │   ├── tui_model.go         # 旧 model struct (逐步迁移到新架构)
│   │   ├── tui_update.go        # 旧 Update 管道 (~2013行)
│   │   ├── tui_view.go          # 旧 View 管道 (~594行)
│   │   ├── tui_render.go        # 渲染辅助
│   │   ├── tui_helpers.go       # 辅助函数
│   │   ├── tui_history.go       # 历史模式
│   │   ├── tui_tasks.go         # 任务管理
│   │   ├── tui_fzf.go           # FZF 集成
│   │   ├── types.go             # 基础类型
│   │   └── i18n.go              # 多语言
│   ├── memory/                  # ConversationMemory (system/human/assistant/tool)
│   ├── config/                  # Three-tier TOML config (tools > agents > global)
│   ├── diff/                    # Unified diff computation
│   ├── embedbin/                # Embed Rust codexray binary
│   ├── datamanager/             # Task persistence (~/.codeactor/tasks/)
│   ├── globalctx/               # Global context (CodexrayURL, tool references)
│   └── util/                    # Error handling, crash recovery
├── pkg/messaging/               # Pub-Sub message bus
│   ├── message_event.go         # MessageEvent definition
│   ├── message_publisher.go     # Agent → Dispatcher
│   ├── message_dispatcher.go    # Central dispatcher (fan-out)
│   ├── message_consumer.go      # Consumer interface
│   └── consumers/
│       ├── tui.go               # TUI event consumer
│       └── websock.go           # WebSocket event consumer
├── config/config.toml           # Config template
├── docs/                        # Architecture, testing, prompt guides
└── benchmark/                   # Benchmark tasks (Python/Rust/non-code)
```

## Core Architecture

1. **Hub-and-Spoke**: ConductorAgent is the sole user-facing agent, delegating to sub-agents via `delegate_repo` / `delegate_coding` / `delegate_chat` / `delegate_meta` tools. MetaAgent can dynamically create custom agents registered at runtime.
2. **Pub-Sub messaging**: Agent publishes `MessageEvent` → `MessageDispatcher` broadcasts → `TUIConsumer` / `WebSocketConsumer`
3. **Adapter pattern**: `ToolFunc` wrapped via `Adapter` into LLM's `Tool` interface for LLM Function Calling
4. **Config priority**: `$HOME/.codeactor/config/config.toml` → panics if not found
5. **Agent disable**: Use `--disable-agents=repo,coding,...` at startup to conditionally exclude delegate tools. Disabled agents are still constructed but their delegate tools are not registered in the Conductor's Adapters.
6. **Context compression**: Multi-strategy (conservative/balanced/aggressive) context compression with priority-based message selection and LLM summarization to handle long conversations.
7. **WorkspaceGuard**: All file operations and bash commands are validated against workspace boundaries. Dangerous operations require user confirmation via Pub-Sub confirmation pipeline.

## Codexray Component

`codeactor-codexray` is a standalone **Rust** service that provides deep code analysis capabilities. It runs as a background HTTP server on `127.0.0.1:12800` (configurable via `config.toml` `[http] codexray_port`).

### Build & Run

```bash
cd codexray && cargo build --release

# Start with target repo
./target/release/codeactor-codexray server --repo-path /path/to/project

# Custom address
./target/release/codeactor-codexray server --repo-path /path/to/project --address 0.0.0.0:8080
```

The Go binary automatically launches `~/.codeactor/bin/codeactor-codexray` as a background process on startup (`main.go:startCodexrayServer()`). Logs go to `~/.codeactor/logs/codeactor-codexray/{date}.log`.

### Architecture (Rust side)

```
codexray/src/
├── main.rs              # CLI entry: server / vectorize subcommands
├── config.rs            # Config loading from ~/.codeactor/config/config.toml
├── codegraph/           # AST parsing + graph data structures
│   ├── graph.rs         # Flat CodeGraph (HashMap-based)
│   ├── types.rs         # PetCodeGraph (petgraph DiGraph<FunctionInfo, CallRelation>)
│   ├── parser.rs        # CodeParser: tree-sitter multi-language parsing (Rust/Python/JS/TS/Java/C++/Go)
│   └── treesitter/      # Language parsers (Rust/Python/JS/TS/Java/C++/Go)
├── services/
│   ├── analyzer.rs      # CodeAnalyzer: call chains, cycles, complexity, reports
│   ├── embedding_service.rs  # LanceDB vector embeddings + SQLite cache + semantic search
│   └── snippet_service.rs    # Code snippet extraction + caching
├── storage/             # Graph persistence (JSON/binary), file watching (notify, 20s debounce)
└── http/                # Axum HTTP server (handlers, models, server)
```

Core design: **single repo per process** — binds to one repo at startup via `--repo-path`. All API endpoints use the bound repo.

### HTTP API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Health check |
| GET | `/status` | Repo status (functions, files, embedding state) |
| POST | `/investigate_repo` | Top-15 functions by out-degree, directory tree, file skeletons |
| POST | `/semantic_search` | Vector-based semantic code search (text + `limit`) |
| POST | `/query_code_skeleton` | Batch skeleton extraction from file paths |
| POST | `/query_code_snippet` | Extract code snippet by `filepath` + `function_name` |
| POST | `/query_call_graph` | Query call graph by file/function name |
| POST | `/query_hierarchical_graph` | Hierarchical call tree with depth limit |
| POST | `/query_indexing_status` | Embedding indexing status |
| GET | `/draw_call_graph` | ECharts call graph visualization |

### Embedding & Vector Search

The codexray service supports semantic code search via vector embeddings:
- **Embedding model**: Configurable (default: `text-embedding-3-small`, 1536 dimensions)
- **Vector store**: LanceDB for vector indexing
- **Cache**: SQLite for embedding cache (avoids re-embedding unchanged code)
- **Background indexing**: Triggers automatically after initial analysis
- **Status**: Query via `POST /query_indexing_status`

### Integration in Go

| Layer | File | Usage |
|-------|------|-------|
| Startup | `main.go:216-257` | `startCodexrayServer()` launches the Rust binary as a background process |
| Global state | `internal/globalctx/global_context.go:20,31` | `CodexrayURL` field + `RepoOps *RepoOperationsTool` |
| Initialization | `internal/app/app.go:62,73` | Sets `CodexrayURL=http://127.0.0.1:12800`, creates `RepoOperationsTool` |
| Tool wrapper | `internal/tools/repo_operations.go` | `RepoOperationsTool` with 3 methods: `ExecuteSemanticSearch`, `ExecuteQueryCodeSkeleton`, `ExecuteQueryCodeSnippet` |
| RepoAgent | `internal/agents/repo.go:105-139` | `doPreInvestigate()` calls `POST /investigate_repo` before each Run |
| Tool routing | `internal/agents/conductor.go:298-303`, `coding.go:59-63`, `repo.go:75-79` | Routes `semantic_search`/`query_code_skeleton`/`query_code_snippet` to `RepoOps` |

### Config sections (in project `config/config.toml`, deployed to `~/.codeactor/config/config.toml`)

```toml
[http]
codexray_port = 12800

[codexray]
enable_embedding = true
# 数据目录自动生成在 $HOME/.codeactor/data/
# embedding/ — 全局共享索引（BM25 + LanceDB 向量库）
# graph/     — 项目隔离数据（按 project_id 分目录）

[codexray.embedding]
model = "text-embedding-3-small"
api_token = "sk-..."
api_base_url = "https://api.openai.com/v1"
dimensions = 1536
```

## Code Conventions

- Import path: `codeactor/internal/...`, `codeactor/pkg/...`
- Agent interface: `Name() string` + `Run(ctx, input) (string, error)`
- LLM calls: `GenerateContent` with Streaming + Function Calling support
- Memory: `ConversationMemory` with system/human/assistant/tool message types
- Task persistence: `~/.codeactor/tasks/{taskID}.json`
- LLM logs: `~/.codeactor/logs/llm-{date}.log`
- Config: Three-tier LLM provider selection (tools.llm > agents.llm > global.llm)

## Testing Methodology

### Unit Tests (mock LLM, fast)

```bash
# Run all agent tests (mock LLM, no real API calls)
go test ./internal/agents/... -v -count=1

# Run a specific test
go test ./internal/agents/... -v -run TestDelegateMeta_DynamicRegistration
```

Agent tests use `mockLLM` in `conductor_test.go` — it returns pre-defined responses, so tests are deterministic and fast. Key test categories:
- `TestExtractJSONObject*` — JSON extraction from MetaAgent output
- `TestParseMetaAgentOutput*` — design JSON validation
- `TestRegisterCustomAgent*` — custom agent registration and delegate tool creation
- `TestCustomAgentDelegateTool*` — custom agent execution (LLM-tool loop)
- `TestDelegateMeta*` — full delegate_meta flow (design → register → execute)

### Full-Stack E2E (real LLM)

1. **Build** the updated binary: `go build -o codeactor . && go vet ./...`

2. **Start HTTP server** (background):
   ```bash
   pkill -f "codeactor http" 2>/dev/null
   nohup ./codeactor http --port=9800 > /tmp/codeactor.log 2>&1 &
   ```

3. **Run a task** via CLI client that triggers MetaAgent:
   ```bash
   node clients/nodejs-cli/index.js --port 9800 run /path/to/project \
     "使用delegate_meta设计一个代码统计agent，统计internal/assistant/agents/executor.go文件"
   ```

4. **Inspect task memory** to verify the full agent flow:
   ```bash
   curl -s "http://localhost:9800/api/memory?task_id=<task-id>" | python3 -m json.tool
   ```
   Look for: `delegate_meta` tool call → `Custom agent registered` → `Conductor executing newly designed agent` → execution result.

5. **Check LLM logs** for detailed request/response traces:
   ```bash
   tail -200 ~/.codeactor/logs/llm-$(date +%Y-%m-%d).log
   ```

### What to Verify in E2E Logs

- **Agent registration**: `INFO Custom agent registered delegate_name=delegate_<name>`
- **Execution dispatch**: `INFO Conductor executing newly designed agent delegate=delegate_<name>`
- **Result format**: `[Meta-Agent: Agent Designed and Executed]` wrapper in the tool result
- **Error fallback**: When custom agent fails, Conductor falls back to CodingAgent/ChatAgent
- **`task_for_agent` field**: Clean task (no meta-design instructions) passed to custom agent

<!-- CODERAY_INJECTION -->
# Code exploration: use codexray MCP tools first

Before any Grep/Glob/Bash for code search, try codexray tools first.
They give you AST-verified definitions with signatures and line numbers.

Tool priority (use in this order):
1. codexray_explore("how does X work?") — FIRST for architecture questions
2. codexray_search("what does Y do?")  — FIRST for finding code by behavior
3. codexray_find("Z")                  — FIRST for finding code by name
4. codexray_callers("fn")             — REQUIRED before modifying any function
5. codexray_callees("fn")             — to understand internal dependencies
6. Grep — ONLY for exact strings (error messages, UUIDs, log formats)
7. Glob — ONLY when you already know the exact filename pattern
<!-- /CODERAY_INJECTION -->
