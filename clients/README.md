# Clients

Client libraries and tools for interacting with the CodeActor Agent HTTP/WebSocket API.

## Node.js CLI

A command-line client for creating tasks and receiving real-time streaming output.

### Setup

```bash
cd clients/nodejs-cli
npm install
```

### Usage

```bash
# Direct
node index.js <command> [args]

# Or link globally
npm link
codeactor <command> [args]
```

### Commands

| Command | Description |
|---------|-------------|
| `run <dir> <desc>` | Create a task and stream output via WebSocket |
| `chat <id> <dir>` | Interactive chat with an existing task |
| `status <id>` | Query task status |
| `cancel <id>` | Cancel a running task |
| `history` | List recent tasks |
| `memory <id>` | Show conversation memory |
| `load <id> <dir>` | Load a historical task from disk |

### Server connection

```bash
# Default: localhost:9080
codeactor run ./myproject "Add a login endpoint"

# Custom host/port
codeactor --host dev-server --port 9080 status <task-id>

# Or via environment variable
export CODECACTOR_HOST=dev-server:9080
```

### API overview

The CLI communicates with the CodeActor server through two channels:

- **HTTP REST** (`/api/*`) — task CRUD, status, history, memory
- **WebSocket** (`/ws`) — real-time task streaming and chat

See the [architecture docs](../../docs/ARCHITECTURE.md) for the full API specification.
