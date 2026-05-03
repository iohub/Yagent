# CodeActor Agent Project Reference

## 1. Project Overview (项目概览)

CodeActor Agent 是一个基于 **Hub-and-Spoke (中枢-辐条)** 多智能体架构的 AI 编程助手。它通过协调多个专用智能体（Agents）来完成复杂的编程任务，支持代码检索、规划、编写、测试和自我修正。

## 2. Core Features (核心功能)

### 2.1 Multi-Agent Architecture (多智能体架构)

系统由以下核心 Agent 组成：

*   **Conductor Agent (指挥家)**:
    *   **职责**: 项目经理与技术主管。负责与用户交互，解析需求，制定计划，委派任务给 Sub-Agents，并评审结果。
    *   **能力**: 上下文管理，任务拆解，决策控制。
    *   **Workflow**: Analyze -> Plan -> Delegate (Repo/Coding) -> Review -> Iterate.
*   **Coding Agent (编码工程师)**:
    *   **职责**: 执行具体的代码编写和修改任务。
    *   **能力**: 文件读写，执行 Shell 命令（测试、Lint），自我修正（Thinking）。
    *   **Workflow**: Analyze -> Explore -> Plan -> Implement -> Verify.
*   **Repo Agent (仓库分析员)**:
    *   **职责**: 代码库分析与检索。
    *   **能力**: 全局搜索，引用查找，代码结构分析。

### 2.2 Tool System (工具系统)

Agent 配备了丰富的工具集：

*   **File Operations**: `read_file`, `write_file` (via `create_file`), `list_dir`, `print_dir_tree`, `delete_file`, `rename_file`.
*   **Code Editing**: `search_replace_in_file` (Precise search and replace).
*   **Search**: `search_by_regex` (grep), `semantic_search` (Semantic Search), `query_code_skeleton`, `query_code_snippet`.
*   **System Execution**: `run_bash` (Execute Shell commands).
*   **Cognitive**: `thinking` (Error analysis and self-correction chain-of-thought).

### 2.3 Interaction Modes (交互模式)

*   **HTTP Server**: 提供 REST API 和 WebSocket 接口，适用于 Web 前端或 IDE 插件集成。
*   **TUI (Terminal UI)**: 命令行交互界面，用于本地直接使用。

---

## 3. API Documentation (API 接口文档)

### 3.1 HTTP REST API

**Base URL**: `http://localhost:9080` (Default port, configurable in `config.toml`)

#### 1. Start Task (启动任务)
*   **Endpoint**: `POST /api/start_task`
*   **Description**: Starts a new coding task or resumes an existing one if `task_id` is provided.
*   **Request Body**:
    ```json
    {
        "project_dir": "/absolute/path/to/project", // Required
        "task_desc": "Refactor the login logic to use JWT", // Required
        "task_id": "optional-uuid-string" // Optional: Resume specific task
    }
    ```
*   **Success Response (200 OK)**:
    ```json
    {
        "task_id": "550e8400-e29b-41d4-a716-446655440000",
        "error": "" 
    }
    ```
*   **Error Response (409 Conflict)**: Task already running.
*   **Error Response (400 Bad Request)**: Missing required fields.

#### 2. Get Task Status (查询状态)
*   **Endpoint**: `GET /api/task_status`
*   **Query Params**: `task_id` (Required)
*   **Response (200 OK)**:
    ```json
    {
        "task_id": "...",
        "status": "running|finished|failed|cancelled",
        "result": "Final result text or null",
        "error": "Error message or null",
        "progress": "Current progress description",
        "created_at": "2023-10-27T10:00:00Z",
        "updated_at": "2023-10-27T10:05:00Z",
        "memory": {
            "messages": [...], // Array of ChatMessage
            "size": 15,
            "max_size": 300
        }
    }
    ```

#### 3. Cancel Task (取消任务)
*   **Endpoint**: `POST /api/cancel_task`
*   **Request Body**:
    ```json
    {
        "task_id": "..."
    }
    ```
*   **Response (200 OK)**: `{"task_id": "...", "message": "Task cancelled successfully"}`
*   **Response (404 Not Found)**: Task not found or not running.

#### 4. List History (历史记录)
*   **Endpoint**: `GET /api/history`
*   **Response (200 OK)**:
    ```json
    [
        {
            "id": "...",
            "status": "finished",
            "project_dir": "...",
            "created_at": "...",
            "summary": "..."
        }
    ]
    ```

#### 5. Load Task (加载/恢复任务)
*   **Endpoint**: `POST /api/load_task`
*   **Description**: Restores task context from persistence.
*   **Request Body**:
    ```json
    {
        "task_id": "...",
        "project_dir": "..."
    }
    ```
*   **Response (200 OK)**: `{"task_id": "...", "message": "Task loaded successfully"}`

#### 6. Memory Management (记忆管理)
*   **Get All Memory**: `GET /api/memory?task_id=...`
*   **Get Memory by Type**: `GET /api/memory/:type?task_id=...`
    *   Types: `system`, `human`, `assistant`, `tool`
*   **Clear Memory**: `DELETE /api/memory?task_id=...`

---

### 3.2 WebSocket API

**Endpoint**: `ws://localhost:9080/ws`

The WebSocket API uses a JSON-based event system. Every message follows the `SocketMessage` structure.

#### Common Message Structure
```json
{
    "type": "string",       // Message category (e.g., "realtime", "chat_message")
    "event": "string",      // Specific event name (e.g., "status_update")
    "data": { ... },        // Payload
    "from": "string",       // Sender (e.g., "System", "CodingAgent")
    "task_id": "string"     // Associated Task ID
}
```

#### 1. Connection Lifecycle
*   **On Connect**: Server sends `connected` event.
    ```json
    {
        "type": "connection",
        "event": "connected",
        "data": { "message": "Connected to AI Coding Assistant" },
        "from": "System"
    }
    ```

#### 2. Client Commands (Client -> Server)

*   **Start Task**:
    ```json
    {
        "event": "start_task",
        "data": {
            "project_dir": "/path/to/repo",
            "task_desc": "Fix bug in auth.go"
        }
    }
    ```

*   **Send Chat Message**:
    ```json
    {
        "event": "chat_message",
        "data": {
            "task_id": "...",
            "message": "Please also update the tests."
        }
    }
    ```

*   **Memory Operations**:
    *   Get: `{"event": "get_memory", "data": {"task_id": "..."}}`
    *   Clear: `{"event": "clear_memory", "data": {"task_id": "..."}}`

#### 3. Real-time Events (Server -> Client)

These events are streamed during task execution with `type: "realtime"`.



*   **AI Response Chunk** (Streaming text):
    ```json
    {
        "type": "realtime",
        "event": "ai_response",
        "data": {
            "task_id": "...",
            "content": "I have found the issue. It seems...",
            "timestamp": 1698765433
        }
    }
    ```

*   **Tool Call Start** (Agent invoking a tool):
    ```json
    {
        "type": "realtime",
        "event": "tool_call_start",
        "data": {
            "task_id": "...",
            "content": "",
            "metadata": {
                "tool_name": "read_file",
                "arguments": "{\"target_file\": \"main.go\"}",
                "tool_call_id": "call_123"
            }
        }
    }
    ```

*   **Tool Call Result** (Tool execution output):
    ```json
    {
        "type": "realtime",
        "event": "tool_call_result",
        "data": {
            "task_id": "...",
            "metadata": {
                "tool_name": "read_file",
                "result": "package main\n...",
                "tool_call_id": "call_123"
            }
        }
    }
    ```

*   **Conversation Result** (Turn completion):
    ```json
    {
        "type": "realtime",
        "event": "conversation_result",
        "data": {
            "task_id": "...",
            "content": "Final full response text...",
            "metadata": { "result": "..." }
        }
    }
    ```

---

## 4. Data Structures (数据结构)

### Task Object
```go
type Task struct {
    ID         string
    Status     string      // "running", "finished", "failed", "cancelled"
    Result     string
    Error      string
    Progress   string
    ProjectDir string
    CreatedAt  time.Time
    UpdatedAt  time.Time
}
```

### Socket Message
```go
type SocketMessage struct {
    Type    string      `json:"type"`    // e.g., "realtime"
    Event   string      `json:"event"`   // e.g., "status_update"
    Data    interface{} `json:"data"`    // Payload
    From    string      `json:"from"`    // Agent Name
    TaskID  string      `json:"task_id"`
    Message string      `json:"message"`
}
```

---

## 5. Project Structure (项目结构)

```text
codeactor-agent/
├── config/              # Configuration files
│   └── config.toml      # Main configuration (LLM providers, server port)
├── docs/                # Documentation
├── internal/            # Private application code
│   ├── assistant/       # Core Agent Logic
│   │   ├── agents/      # Agent implementations (Conductor, Coding, Repo)
│   │   │   ├── *.prompt.md  # System prompts for agents
│   │   │   └── tools.json   # Tool definitions schema
│   │   ├── tools/       # Tool implementations (FileOps, SearchOps, etc.)
│   │   └── memory/      # Memory management (Conversation history)
│   ├── config/          # Configuration loading logic
│   ├── globalctx/       # Global context (Shared state, Tool providers)
│   ├── http/            # HTTP Server & WebSocket handling
│   └── util/            # Utilities
├── pkg/                 # Public library code
│   └── messaging/       # Message bus/Event system
├── main.go              # Entry point
└── go.mod               # Go module definition
```

---

## 6. Configuration Guide (配置指南)

Configuration is managed in `config/config.toml`.

### Key Sections:

*   **[http]**: Server settings.
    *   `server_port`: Port for the HTTP/WebSocket server (default: 9080).

*   **[llm]**: LLM Provider settings.
    *   `use_provider`: Select the active provider (e.g., "siliconflow", "aliyun", "xiaomi").

*   **[llm.providers.<provider_name>]**: Specific settings for each provider.
    *   `model`: Model name (e.g., "deepseek-ai/DeepSeek-V3.2", "qwen3-coder-plus").
    *   `api_base_url`: API endpoint.
    *   `api_key`: Secret API key.
    *   `temperature`: Sampling temperature (0.0 recommended for coding).
    *   `max_tokens`: Max output tokens.

---
