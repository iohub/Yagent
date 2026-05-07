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
- **External**: `codeactor-codebase` (Rust, `127.0.0.1:12800`) — semantic search, repo investigation, call graph, code skeleton/snippet. See [Codebase Component](#codebase-component) below.
- **System deps**: `ripgrep` (rg), `fzf`

## Project Structure

```
codeactor-agent/
├── main.go                      # Entry point, CLI parsing, start codebase service
├── internal/
│   ├── app/
│   │   └── app.go               # CodingAssistant: agent orchestration & init
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
│   │   ├── impl_plan_agent.go   # ImplPlanAgent: read-only implementation planner
│   │   ├── impl_plan.prompt.md  # ImplPlan system prompt
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
│   │   ├── impl_plan.go         # impl_plan (stateful implementation plan)
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
│   ├── tui/                     # Bubble Tea terminal UI
│   ├── memory/                  # ConversationMemory (system/human/assistant/tool)
│   ├── config/                  # Three-tier TOML config (tools > agents > global)
│   ├── diff/                    # Unified diff computation
│   ├── embedbin/                # Embed Rust codebase binary
│   ├── datamanager/             # Task persistence (~/.codeactor/tasks/)
│   ├── globalctx/               # Global context (CodebaseURL, tool references)
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

## Codebase Component

`codeactor-codebase` is a standalone **Rust** service that provides deep code analysis capabilities. It runs as a background HTTP server on `127.0.0.1:12800` (configurable via `config.toml` `[http] codebase_port`).

### Build & Run

```bash
cd codebase && cargo build --release

# Start with target repo
./target/release/codeactor-codebase server --repo-path /path/to/project

# Custom address
./target/release/codeactor-codebase server --repo-path /path/to/project --address 0.0.0.0:8080
```

The Go binary automatically launches `~/.codeactor/bin/codeactor-codebase` as a background process on startup (`main.go:startCodebaseServer()`). Logs go to `~/.codeactor/logs/codeactor-codebase/{date}.log`.

### Architecture (Rust side)

```
codebase/src/
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

The codebase service supports semantic code search via vector embeddings:
- **Embedding model**: Configurable (default: `text-embedding-3-small`, 1536 dimensions)
- **Vector store**: LanceDB for vector indexing
- **Cache**: SQLite for embedding cache (avoids re-embedding unchanged code)
- **Background indexing**: Triggers automatically after initial analysis
- **Status**: Query via `POST /query_indexing_status`

### Integration in Go

| Layer | File | Usage |
|-------|------|-------|
| Startup | `main.go:216-257` | `startCodebaseServer()` launches the Rust binary as a background process |
| Global state | `internal/globalctx/global_context.go:20,31` | `CodebaseURL` field + `RepoOps *RepoOperationsTool` |
| Initialization | `internal/app/app.go:62,73` | Sets `CodebaseURL=http://127.0.0.1:12800`, creates `RepoOperationsTool` |
| Tool wrapper | `internal/tools/repo_operations.go` | `RepoOperationsTool` with 3 methods: `ExecuteSemanticSearch`, `ExecuteQueryCodeSkeleton`, `ExecuteQueryCodeSnippet` |
| RepoAgent | `internal/agents/repo.go:105-139` | `doPreInvestigate()` calls `POST /investigate_repo` before each Run |
| Tool routing | `internal/agents/conductor.go:298-303`, `coding.go:59-63`, `repo.go:75-79` | Routes `semantic_search`/`query_code_skeleton`/`query_code_snippet` to `RepoOps` |

### Config sections (in project `config/config.toml`, deployed to `~/.codeactor/config/config.toml`)

```toml
[http]
codebase_port = 12800

[codebase]
enable_embedding = true
embedding_db_uri = "~/.codeactor/data/lancedb"
graph_db_uri = "~/.codeactor/data/graph"

[codebase.embedding]
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
