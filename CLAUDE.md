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

## Testing with the CLI Client

Start the server in one terminal:

```bash
./codeactor http
```

Use the Node.js CLI client in another terminal:

```bash
cd clients/nodejs-cli
npm install

# Create a task and stream output in real-time
node index.js run <project-dir> "Your task description"

# Continue a conversation
node index.js chat <task-id> <project-dir>

# Query status
node index.js status <task-id>

# List task history
node index.js history

# View conversation memory
node index.js memory <task-id>
```

Server defaults to `localhost:9080`. Override via `--host`/`--port` or `CODECACTOR_HOST=host:port`.

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

## Functional Testing Methodology

When implementing or modifying agent behavior (especially Meta-Agent / ConductorAgent logic), follow this multi-layered testing approach:

### Layer 1: Unit Tests (`go test`)

Test individual functions with mock LLM and temp directories. This covers pure logic without real LLM calls:

- **Parsing functions**: `parseMetaAgentOutput`, `fallbackParseMetaAgentOutput`, `extractAgentName`, `extractToolsUsed`, `toSnakeCase`
- **Tool lookup**: `getToolFunc` — verify all 14 known tools return non-nil, unknown returns nil
- **Registration logic**: `registerCustomAgent` — verify adapter is added, duplicate is skipped, unknown tools are handled
- **Custom agent execution**: `executeCustomAgent` — verify mock LLM response is returned, `finish` terminates loop
- **System prompt construction**: verify `<custom_agents>` block appears/disappears correctly

Create a `mockLLM` struct implementing the `llms.Model` interface (`GenerateContent` + `Call`) to control LLM output deterministically.

### Layer 2: Delegate-Meta Integration Tests

Test the `delegate_meta` tool handler end-to-end with a mock MetaAgent:

1. Create `MetaAgent` with a `mockLLM` that returns pre-defined output (structured or malformed)
2. Create `ConductorAgent` with the MetaAgent
3. Find the `delegate_meta` adapter from `conductor.Adapters`
4. Call `delegateMeta.Call(ctx, input)` directly
5. Verify: custom agent registered / not registered, result message format

Key scenarios to cover:
- **Structured output** → agent registered, `[New Agent Registered]` in result
- **Malformed output** → heuristic extraction attempted, `[Meta-Agent Raw Output]` returned
- **Empty agent name** → no registration
- **No agent_design block** → no registration
- **Duplicate delegation** → no duplicate registration

### Layer 3: Server-Level Functional Tests

Test the full stack with a running HTTP server:

```bash
# 1. Build and start server in background, capture all logs
go build -o codeactor .
kill $(lsof -ti :9080) 2>/dev/null
nohup ./codeactor http > /tmp/codeactor-server.log 2>&1 &
sleep 3

# 2. Run tasks via CLI client
node clients/nodejs-cli/index.js run . "task description"

# 3. Monitor server logs for key events
grep "Conductor delegating to Meta-Agent\|Custom agent registered\|Strict Meta-Agent parse failed\|Heuristic extraction" /tmp/codeactor-server.log

# 4. Inspect task memory for tool call traces
node clients/nodejs-cli/index.js memory <task-id> | grep "tool_call\|delegate_"

# 5. Check task files on disk for detailed analysis
python3 -c "
import json
with open('$HOME/.codeactor/tasks/<task-id>.json') as f:
    d = json.load(f)
for m in d.get('messages', []):
    print(f'[{m[\"type\"]}] {str(m.get(\"content\",\"\"))[:200]}')
"
```

### Layer 4: Log Level Configuration

**Important**: The default `slog` handler in `main.go` `init()` is set to `LevelError`. The HTTP mode override in `main()` uses `LevelInfo`. Key slog calls and their levels:

| Level | Calls |
|-------|-------|
| `Info` | `Conductor delegating to Meta-Agent`, `Custom agent registered`, `Custom agent registered via heuristic fallback` |
| `Warn` | `Strict Meta-Agent parse failed, trying heuristic extraction`, `Heuristic extraction insufficient, returning raw output` |
| `Error` | `Meta-Agent execution failed`, `CustomAgent LLM error` |

When debugging Meta-Agent issues, ensure the log level is `Info` or lower to see registration events.

### Layer 5: LLM Output Diagnosis

When Meta-Agent produces unexpected output:

```bash
# Search LLM input/output logs for Meta-Agent interactions
grep -c "MetaAgent\|Meta-Agent" ~/.codeactor/logs/llm-$(date +%Y-%m-%d).log

# Check task memory for delegate_meta tool results (empty = LLM returned nothing)
python3 -c "
import json
with open('$HOME/.codeactor/tasks/<task-id>.json') as f:
    d = json.load(f)
msgs = d.get('messages', [])
for i, m in enumerate(msgs):
    if m['type'] == 'tool':
        c = str(m.get('content',''))
        if len(c) < 20:
            print(f'[{i}] EMPTY or short tool result: {repr(c)}')  
"
```

### Common Pitfalls

1. **LLM returns empty content**: Local/quantized models may not follow the Meta-Agent prompt format. The heuristic fallback parser mitigates this.
2. **Log level too high**: `LevelError` hides all `slog.Info` messages. Always use `LevelInfo` or lower for functional testing.
3. **Conductor prefers existing agents**: The LLM naturally delegates to Repo/Coding/Chat agents first. Meta-Agent is only triggered when the LLM judges those insufficient. Test with tasks explicitly matching Meta-Agent use cases.
4. **Adapter JSON encoding**: `adapter.Call()` JSON-encodes return values. When reading tool results from memory, unmarshal with `json.Unmarshal` to get the raw string.
