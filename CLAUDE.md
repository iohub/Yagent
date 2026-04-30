# CLAUDE.md — CodeActor Agent

> See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed system architecture, module design, data flow, and API protocols.
> Read that document first before starting any task to understand the full project picture.

## Project Overview

CodeActor Agent is a Go-based multi-agent AI coding assistant using a **Hub-and-Spoke** architecture. The central **ConductorAgent** orchestrates specialized sub-agents (RepoAgent / CodingAgent / ChatAgent) to perform code analysis, planning, writing, and self-correction.

## Build & Run

```bash
# Build
go build -o codeactor .

# TUI terminal mode
./codeactor tui [--taskfile TASK.md]

# HTTP server mode (default port 9080)
./codeactor http
```

## Tech Stack

- **Language**: Go 1.23+
- **Module name**: `codeactor`
- **LLM abstraction**: `github.com/tmc/langchaingo`
- **HTTP/WS framework**: `gin-gonic/gin` + `olahol/melody`
- **TUI**: Bubble Tea
- **External service**: `codeactor-codebase` (Rust, listening on `127.0.0.1:12800`), providing semantic search, repo investigation, code skeleton/snippet queries
- **System dependencies**: `ripgrep` (rg) for full-text search, `fzf` for file search

## Project Structure

```
codeactor-agent/
├── main.go                      # Entry point: parse commands, start codebase service, dispatch TUI/HTTP mode
├── tui.go                       # Bubble Tea terminal UI
├── i18n.go                      # i18n (Chinese/English)
├── config/config.toml           # LLM providers, HTTP port, Agent parameters
├── docs/                        # Architecture docs, API reference, Prompt guide
├── internal/
│   ├── assistant/               # Core orchestration layer
│   │   ├── assistant.go         # CodingAssistant entry point
│   │   ├── llm.go               # Multi-provider LLM client
│   │   ├── data_manager.go      # Task memory persistence
│   │   ├── integration.go       # Messaging system integration
│   │   ├── agents/              # Agent implementations (conductor/coding/repo/chat)
│   │   └── tools/               # 14 tool implementations (Adapter pattern wrapping langchaingo Tool)
│   ├── http/                    # REST API + WebSocket server
│   ├── memory/                  # Conversation history management (ConversationMemory)
│   ├── config/                  # TOML config parsing
│   ├── globalctx/               # Global context (project path, language, prompt formatting)
│   └── util/                    # Error handling with call stacks
└── pkg/messaging/               # Pub-Sub message bus (MessageEvent → Dispatcher → Consumers)
```

## Core Architecture

1. **Hub-and-Spoke**: ConductorAgent is the sole user-facing agent, delegating tasks to sub-agents via `delegate_repo` / `delegate_coding` / `delegate_chat` tools
2. **Pub-Sub messaging**: Agent publishes `MessageEvent` → `MessageDispatcher` broadcasts → `TUIConsumer` / `WebSocketConsumer` consume and render
3. **Adapter pattern**: `ToolFunc` wrapped via `Adapter` into langchaingo's `Tool` interface, enabling LLM Function Calling
4. **RepoAgent pre-investigation**: Automatically calls `POST /investigate_repo` before each run to obtain directory tree, core function call graph, and file skeletons
5. **Config priority**: `$HOME/.codeactor/config/config.toml` → `config/config.toml`, panics if neither found

## Code Conventions

- Go module path is `codeactor`, internal imports use the `codeactor/` prefix
- All agents implement the `Agent` interface: `Name() string` + `Run(ctx, input) (string, error)`
- All LLM calls use langchaingo's `GenerateContent` method, supporting Streaming and Function Calling
- Memory management uses `ConversationMemory`, supporting four message types: system/human/assistant/tool
- Task data persisted to `~/.codeactor/tasks/{taskID}.json`
- LLM logs written to `~/.codeactor/logs/llm-{date}.log`
