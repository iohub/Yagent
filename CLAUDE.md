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

- **Language**: Go 1.23+, module `codeactor`
- **LLM**: `github.com/tmc/langchaingo` (multi-provider: OpenAI-compatible, Bedrock)
- **HTTP/WS**: `gin-gonic/gin` + `olahol/melody`
- **TUI**: Bubble Tea
- **External**: `codeactor-codebase` (Rust, `127.0.0.1:12800`) — semantic search, repo investigation, code skeleton/snippet
- **System deps**: `ripgrep` (rg), `fzf`

## Project Structure

```
codeactor-agent/
├── main.go                      # Entry point, CLI parsing, start codebase service
├── tui.go                       # Bubble Tea terminal UI
├── i18n.go                      # i18n (Chinese/English)
├── config/config.toml           # LLM providers, HTTP port, agent step limits
├── docs/                        # Architecture, testing, prompt guides
├── internal/
│   ├── assistant/               # Core orchestration
│   │   ├── assistant.go         # CodingAssistant entry point
│   │   ├── llm.go               # Multi-provider LLM client
│   │   ├── data_manager.go      # Task memory persistence
│   │   ├── integration.go       # Messaging integration
│   │   ├── agents/              # Agent implementations (conductor/coding/repo/chat/meta)
│   │   └── tools/               # 14 tools (Adapter pattern wrapping langchaingo Tool)
│   ├── http/                    # REST API + WebSocket server (server, task_executor, task_manager)
│   ├── memory/                  # ConversationMemory (system/human/assistant/tool)
│   ├── config/                  # TOML config parsing
│   ├── globalctx/               # Global context (project path, language, prompt formatting)
│   └── util/                    # Error handling with call stacks
└── pkg/messaging/               # Pub-Sub message bus (MessageEvent → Dispatcher → Consumers)
```

## Core Architecture

1. **Hub-and-Spoke**: ConductorAgent is the sole user-facing agent, delegating to sub-agents via `delegate_repo` / `delegate_coding` / `delegate_chat` / `delegate_meta` tools. MetaAgent can dynamically create custom agents registered at runtime.
2. **Pub-Sub messaging**: Agent publishes `MessageEvent` → `MessageDispatcher` broadcasts → `TUIConsumer` / `WebSocketConsumer`
3. **Adapter pattern**: `ToolFunc` wrapped via `Adapter` into langchaingo's `Tool` interface for LLM Function Calling
4. **Config priority**: `$HOME/.codeactor/config/config.toml` → panics if not found
5. **Agent disable**: Use `--disable-agents=repo,coding,...` at startup to conditionally exclude delegate tools. Disabled agents are still constructed but their delegate tools are not registered in the Conductor's Adapters.

## Code Conventions

- Import path: `codeactor/internal/...`, `codeactor/pkg/...`
- Agent interface: `Name() string` + `Run(ctx, input) (string, error)`
- LLM calls: `GenerateContent` with Streaming + Function Calling support
- Memory: `ConversationMemory` with system/human/assistant/tool message types
- Task persistence: `~/.codeactor/tasks/{taskID}.json`
- LLM logs: `~/.codeactor/logs/llm-{date}.log`
