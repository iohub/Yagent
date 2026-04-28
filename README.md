# CodeActor Agent

An AI-powered autonomous coding assistant built with a **Hub-and-Spoke multi-agent architecture** in Go.

CodeActor Agent orchestrates multiple specialized agents — Conductor, Repo-Analyst, Coding-Engineer, and Chat-Assistant — to autonomously analyze, plan, and execute complex software engineering tasks with self-correction capabilities.

## Features

- **Multi-Agent Architecture** — Central Conductor delegates tasks to specialized sub-agents (Repo analysis, Code editing, General chat)
- **Rich Tool System** — 14 built-in tools for file operations, code search, semantic analysis, shell execution, and cognitive self-reflection
- **Self-Correction** — `thinking` tool enables agents to analyze errors and recover without blind retries
- **Dual Interaction Modes** — Terminal UI (TUI) for local use; HTTP + WebSocket server for IDE/Web integration
- **Multi-Provider LLM Support** — Xiaomi MiMo, Alibaba Qwen, DeepSeek, Mistral, AWS Bedrock, and more via OpenAI-compatible API
- **Streaming Output** — Real-time streaming of AI responses, tool calls, and results
- **Conversation Memory** — Full conversation context with tool-call history, persisted across sessions
- **Repository Analysis** — Automatic codebase investigation with directory trees, call graphs, and semantic search

## Quick Start

### Prerequisites

- Go 1.23+
- `ripgrep` (`rg`) — for full-text regex search
- `fzf` — for fuzzy file search (optional)
- A running `codeactor-codebase` service (or set `CODEBASE_URL`)

### Installation

```bash
git clone https://github.com/your-org/codeactor-agent.git
cd codeactor-agent
go build -o codeactor .
```

### Configuration

Create `$HOME/.codeactor/config/config.toml`:

```toml
[http]
server_port = 9080

[llm]
use_provider = "siliconflow"

[llm.providers.siliconflow]
model = "deepseek-ai/DeepSeek-V3.2"
temperature = 0.0
max_tokens = 23000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-api-key-here"

[app]
enable_streaming = true

[agent]
conductor_max_steps = 30
coding_max_steps = 50
repo_max_steps = 30
lang = "Chinese"
```

### Running

**TUI Mode** (terminal interface):
```bash
./codeactor tui
# Or with a task file:
./codeactor tui --taskfile TASK.md
```

**HTTP Server Mode** (API + WebSocket):
```bash
./codeactor http
# Server starts at http://localhost:9080
```

## Architecture

```
                               ┌──────────────────────────────────────┐
                               │           INTERACTION LAYER          │
                               │                                      │
                               │  ┌─────────────┐  ┌──────────────┐  │
                               │  │     TUI     │  │  HTTP + WS   │  │
                               │  │ (bubbletea) │  │(gin+melody)  │  │
                               │  └──────┬──────┘  └──────┬───────┘  │
                               └─────────┼────────────────┼──────────┘
                                         │                │
                               ┌─────────┼────────────────┼──────────┐
                               │         ▼                ▼          │
                               │   ┌──────────────────────────────┐  │
                               │   │       CodingAssistant        │  │
                               │   │    task init & orchestration  │  │
                               │   └─────────────┬────────────────┘  │
                               │                 │                   │
                               │                 ▼                   │
                               │   ┌──────────────────────────────┐  │
                               │   │        ConductorAgent         │  │
                               │   │    plan → delegate → review   │  │
                               │   │                              │  │
                               │   │  ┌──────────┐ ┌───────────┐  │  │
                               │   │  │ delegate │ │ delegate  │  │  │
                               │   │  │   repo   │ │  coding   │  │  │
                               │   │  └────┬─────┘ └─────┬─────┘  │  │
                               │   └───────┼───────────────┼──────┘  │
                               │           │               │         │  AGENT LAYER
                               │   ┌───────┼───────────────┼──────┐  │
                               │   │       ▼               ▼      │  │
                               │   │ ┌───────────┐ ┌───────────┐  │  │
                               │   │ │ RepoAgent │ │CodingAgent│  │  │
                               │   │ │ (只读分析) │ │ (代码编辑) │  │  │
                               │   │ └─────┬─────┘ └─────┬─────┘  │  │
                               │   │       │             │        │  │
                               │   │       │  ┌──────────┘        │  │
                               │   │       │  │ ┌──────────────┐  │  │
                               │   │       │  │ │  ChatAgent   │  │  │
                               │   │       │  │ │  (通用对话)   │  │  │
                               │   │       │  │ └──────────────┘  │  │
                               │   └───────┼──┼───────────────────┘  │
                               └───────────┼──┼──────────────────────┘
                                           │  │
                               ┌───────────┼──┼──────────────────────┐
                               │           ▼  ▼                      │
                               │   ┌──────────────────────────────┐  │
                               │   │     MessageDispatcher         │  │
                               │   │      (Pub-Sub Event Bus)      │  │
                               │   └──────┬───────────┬───────────┘  │  MESSAGE BUS
                               │          │           │              │
                               │          ▼           ▼              │
                               │   ┌──────────┐ ┌───────────────┐   │
                               │   │   TUI    │ │   WebSocket   │   │
                               │   │ Consumer │ │   Consumer    │   │
                               │   └──────────┘ └───────────────┘   │
                               └─────────────────────────────────────┘
                                           │
                               ┌───────────┼──────────────────────────┐
                               │           ▼                          │
                               │   ┌──────────────────────────────┐  │
                               │   │        14 BUILT-IN TOOLS      │  │
                               │   │                              │  │
                               │   │  📁 File   │ 🔍 Search       │  │
                               │   │  read_file │ search_by_regex │  │
                               │   │  create    │ semantic_search │  │  TOOL LAYER
                               │   │  delete    │ code_skeleton   │  │
                               │   │  rename    │ code_snippet    │  │
                               │   │  list_dir  │ print_dir_tree  │  │
                               │   │  replace   │                 │  │
                               │   │                              │  │
                               │   │  ⚙️ System  │ 🧠 Cognitive    │  │
                               │   │  run_cmd   │ thinking        │  │
                               │   └──────────────┬───────────────┘  │
                               └──────────────────┼──────────────────┘
                                                  │
                               ┌──────────────────┼──────────────────┐
                               │                  ▼                  │
                               │   ┌──────────────────────────────┐  │
                               │   │       INFRASTRUCTURE          │  │
                               │   │                              │  │
                               │   │  ┌────────────────────────┐  │  │
                               │   │  │    LLM Providers        │  │  │
                               │   │  │  xiaomi / aliyun        │  │  │
                               │   │  │  deepseek / bedrock ... │  │  │  INFRA
                               │   │  │  (OpenAI-compatible)    │  │  │
                               │   │  └────────────────────────┘  │  │
                               │   │                              │  │
                               │   │  ┌────────────────────────┐  │  │
                               │   │  │  codeactor-codebase     │  │  │
                               │   │  │  Rust 代码分析服务       │  │  │
                               │   │  │  semantic_search        │  │  │
                               │   │  │  investigate_repo       │  │  │
                               │   │  └────────────────────────┘  │  │
                               │   └──────────────────────────────┘  │
                               └─────────────────────────────────────┘
```

## API Overview

### REST Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/start_task` | Start or resume a coding task |
| `GET` | `/api/task_status?task_id=` | Query task status and memory |
| `POST` | `/api/cancel_task` | Cancel a running task |
| `GET` | `/api/history` | List historical tasks |
| `POST` | `/api/load_task` | Restore a task from persistence |
| `GET` | `/api/memory?task_id=` | Get conversation memory |
| `DELETE` | `/api/memory?task_id=` | Clear conversation memory |

### WebSocket

Connect to `ws://localhost:9080/ws`

| Client Event | Description |
|-------------|-------------|
| `start_task` | Create and start a new coding task |
| `chat_message` | Send a follow-up message |
| `get_memory` | Retrieve conversation memory |
| `clear_memory` | Clear conversation memory |

See [docs/Agent_Reference.md](docs/Agent_Reference.md) for detailed API documentation.

## Supported LLM Providers

| Provider | Config Key | Example Model |
|----------|-----------|---------------|
| Xiaomi MiMo | `xiaomi` | `mimo-v2-flash` |
| Alibaba Bailian | `aliyun` | `qwen3-coder-plus` |
| SiliconFlow | `siliconflow` | `deepseek-ai/DeepSeek-V3.2` |
| DeepSeek | `deepseek` | `deepseek-ai/DeepSeek-V3` |
| Moonshot | `moonshot` | `moonshotai/Kimi-K2-Instruct` |
| Mistral | `mistral` | `mistralai/devstral-small` |
| Zhipu Z.ai | `zai` | `zai-org/GLM-4.5-Air` |
| OpenRouter | `openrouter` | `qwen3-coder-plus` |
| StreamLake | `streamlake` | Custom endpoints |
| AWS Bedrock | `bedrock` | `us.anthropic.claude-3-7-sonnet-*` |
| Local | `local` | Any OpenAI-compatible server |


## Documentation

- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — System architecture, modules, data flow, protocols
- [Agent_Reference.md](docs/Agent_Reference.md) — API reference and configuration guide
- [Agent_Design.md](docs/Agent_Design.md) — Multi-agent design rationale

## License

[Apache License 2.0](LICENSE)
