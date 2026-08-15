
---

Codex Rollout JSONL 格式规范

1. 文件格式

- 格式：JSON Lines（每行一个独立 JSON 对象，`\n` 分隔）
- 编码：UTF-8
- 扩展名：`.jsonl`

2. 顶层 Envelope

每行 JSON 的顶层结构统一为：

```json
{
  "timestamp": "2026-01-01T00:00:00.000Z",
  "type": "<record_type>",
  "payload": { ... }
}
```

字段	类型	必填	说明	
`timestamp`	ISO 8601 字符串	是	记录生成时间	
`type`	字符串	是	记录类型，见下表	
`payload`	对象	是	类型相关的载荷	

顶层 `type` 取值：

类型	说明	
`session_meta`	会话元数据（文件首条）	
`turn_context`	每轮对话的上下文配置	
`response_item`	模型 I/O（消息、推理、工具调用）	
`event_msg`	运行时事件（token 用量、任务状态等）	
`compacted`	上下文压缩后的历史快照	
`world_state`	世界状态（较少见）	
`inter_agent_communication_metadata`	多 Agent 通信元数据	

---

3. 详细 Payload 规范

3.1 `session_meta` — 会话元数据

通常作为文件的第一条记录。

```json
{
  "timestamp": "2026-01-01T00:00:00.000Z",
  "type": "session_meta",
  "payload": {
    "id": "sess-xxx",
    "session_id": "sess-xxx",
    "cwd": "/path/to/project",
    "cli_version": "0.118.0",
    "originator": "codex-tui",
    "model_provider": "openai",
    "source": "codex-cli",
    "base_instructions": "You are Codex...",
    "context_window": 200000,
    "history_mode": "full",
    "git": {
      "sha": "abc123",
      "branch": "main",
      "origin_url": "https://github.com/..."
    }
  }
}
```

常用字段：

字段	类型	说明	
`id` / `session_id`	字符串	会话唯一标识	
`cwd`	字符串	工作目录	
`cli_version`	字符串	Codex CLI 版本	
`originator`	字符串	发起方，如 `codex-tui`、`codex-desktop`	
`model_provider`	字符串	模型提供商，如 `openai`	
`base_instructions`	字符串	系统提示词	
`context_window`	整数	上下文窗口大小	
`git`	对象	Git 信息（sha、branch、origin_url）	

---

3.2 `turn_context` — 回合上下文

每轮用户输入前/后记录。

```json
{
  "timestamp": "2026-01-01T00:00:01.000Z",
  "type": "turn_context",
  "payload": {
    "turn_id": "turn-xxx",
    "cwd": "/path/to/project",
    "model": "o4-mini",
    "effort": "medium",
    "approval_policy": "suggest",
    "sandbox_policy": { "mode": "danger-full-access" },
    "summary": "auto",
    "workspace_roots": ["/path/to/project"],
    "collaboration_mode": "single"
  }
}
```

常用字段：

字段	类型	说明	
`turn_id`	字符串	回合唯一标识	
`model`	字符串	当前使用的模型	
`effort`	字符串	推理力度：`low` / `medium` / `high`	
`approval_policy`	字符串	审批策略：`suggest` / `auto-edit` / `never`	
`sandbox_policy`	对象	沙箱配置	
`workspace_roots`	字符串数组	工作空间根目录	
`collaboration_mode`	字符串	协作模式	

---

3.3 `response_item` — 模型响应项

最核心、最丰富的记录类型。`payload.type` 决定子类型。

3.3.1 `message` — 文本消息

```json
{
  "timestamp": "2026-01-01T00:00:02.000Z",
  "type": "response_item",
  "payload": {
    "type": "message",
    "role": "user",
    "id": "msg-xxx",
    "content": [
      { "type": "input_text", "text": "Hello" }
    ]
  }
}
```

User 消息 `role: "user"`，content 类型：
- `input_text` — 纯文本输入
- `input_image` — 图片输入（可能包含 base64）

Assistant 消息 `role: "assistant"`，content 类型：
- `output_text` — 模型输出的文本（现代格式）
- `text` — 旧格式的文本输出

```json
{
  "type": "response_item",
  "payload": {
    "type": "message",
    "role": "assistant",
    "id": "msg-yyy",
    "content": [
      { "type": "output_text", "text": "I'll help you with that." }
    ]
  }
}
```

字段说明：

字段	类型	说明	
`role`	字符串	`user` / `assistant`	
`id`	字符串	消息唯一标识	
`content`	数组	内容块数组	
`phase`	字符串	可选，消息阶段	

---

3.3.2 `reasoning` — 推理过程

```json
{
  "type": "response_item",
  "payload": {
    "type": "reasoning",
    "id": "reason-xxx",
    "summary": [
      { "type": "summary_text", "text": "Analyzing the codebase..." }
    ],
    "content": null,
    "encrypted_content": ""
  }
}
```

字段	类型	说明	
`summary`	数组	可读推理摘要（`summary_text` 块）	
`content`	数组/null	明文推理内容（开启时）	
`encrypted_content`	字符串	加密推理内容（空字符串表示无加密）	

> ⚠️ `encrypted_content` 非空时，该记录仅供本地展示，不应发送到 API 续接会话，否则会导致 `invalid_encrypted_content` 错误。

---

3.3.3 `function_call` — 工具调用请求

```json
{
  "type": "response_item",
  "payload": {
    "type": "function_call",
    "id": "fc-xxx",
    "call_id": "call_01",
    "name": "shell",
    "namespace": "default",
    "arguments": "{\"command\":[\"/bin/bash\",\"-lc\",\"ls\"],\"workdir\":\"/path\"}"
  }
}
```

字段	类型	说明	
`id`	字符串	调用项 ID	
`call_id`	字符串	配对标识，与 `function_call_output` 关联	
`name`	字符串	工具名称，如 `shell`、`apply_patch`、`view`	
`namespace`	字符串	命名空间，如 `default`	
`arguments`	字符串	JSON 字符串，工具参数	

常见工具名称：
- `shell` — 执行 shell 命令
- `apply_patch` — 应用代码补丁
- `view` — 查看文件
- `write_stdin` — 向进程写入 stdin
- `web_search` — 网页搜索

---

3.3.4 `function_call_output` — 工具执行结果

```json
{
  "type": "response_item",
  "payload": {
    "type": "function_call_output",
    "call_id": "call_01",
    "output": "{\"output\":\"file1.txt\\nfile2.txt\\n\",\"metadata\":{\"exit_code\":0,\"duration_seconds\":0.5}}"
  }
}
```

字段	类型	说明	
`call_id`	字符串	与对应的 `function_call` 的 `call_id` 匹配	
`output`	字符串	JSON 字符串，包含 `output` 和 `metadata`	

`output` 字段内部结构：

```json
{
  "output": "命令标准输出内容",
  "metadata": {
    "exit_code": 0,
    "duration_seconds": 1.2
  }
}
```

---

3.3.5 `custom_tool_call` / `custom_tool_call_output` — 自定义工具

与 `function_call` 类似，但使用 `input` 而非 `arguments`：

```json
{
  "type": "response_item",
  "payload": {
    "type": "custom_tool_call",
    "id": "ctc-xxx",
    "call_id": "call_02",
    "name": "my_tool",
    "input": { "param": "value" },
    "status": "in_progress"
  }
}
```

```json
{
  "type": "response_item",
  "payload": {
    "type": "custom_tool_call_output",
    "call_id": "call_02",
    "output": { "result": "done" }
  }
}
```

---

3.3.6 `tool_search_call` / `tool_search_output` — 工具搜索

```json
{
  "type": "response_item",
  "payload": {
    "type": "tool_search_call",
    "id": "tsc-xxx",
    "call_id": "call_03",
    "arguments": { "query": "find files" },
    "execution": {},
    "status": "in_progress"
  }
}
```

---

3.4 `event_msg` — 运行时事件

`payload.type` 决定事件子类型。

3.4.1 `token_count` — Token 用量

```json
{
  "type": "event_msg",
  "payload": {
    "type": "token_count",
    "info": {
      "total_token_usage": {
        "input_tokens": 1000,
        "cached_input_tokens": 500,
        "output_tokens": 200,
        "reasoning_output_tokens": 50,
        "total_tokens": 1250,
        "model_context_window": 200000
      },
      "last_token_usage": {
        "input_tokens": 100,
        "cached_input_tokens": 50,
        "output_tokens": 20,
        "reasoning_output_tokens": 5,
        "total_tokens": 125
      }
    }
  }
}
```

字段	说明	
`total_token_usage`	累计 token 用量	
`last_token_usage`	上一回合的 token 用量	
`input_tokens`	输入 token 数	
`cached_input_tokens`	缓存命中 token 数	
`output_tokens`	输出 token 数	
`reasoning_output_tokens`	推理 token 数	
`total_tokens`	总 token 数	
`model_context_window`	模型上下文窗口	

---

3.4.2 `agent_reasoning` — Agent 推理文本

```json
{
  "type": "event_msg",
  "payload": {
    "type": "agent_reasoning",
    "text": "Planning the next steps..."
  }
}
```

---

3.4.3 `agent_message` — Agent 消息

```json
{
  "type": "event_msg",
  "payload": {
    "type": "agent_message",
    "message": "Task completed successfully."
  }
}
```

---

3.4.4 其他事件类型

类型	说明	
`user_message`	用户消息（去除了环境上下文注入的纯净版本）	
`task_started`	任务开始	
`task_complete`	任务完成	
`turn_aborted`	回合中止	
`context_compacted`	上下文已压缩	
`patch_apply_end`	补丁应用结束	
`web_search_end`	网页搜索结束	
`mcp_tool_call_end`	MCP 工具调用结束	
`sub_agent_activity`	子 Agent 活动	

---

3.5 `compacted` — 压缩快照

当上下文过长时，Codex 将历史记录压缩为一个快照：

```json
{
  "type": "compacted",
  "payload": {
    "first_window_id": "win-001",
    "previous_window_id": "win-002",
    "window_id": "win-003",
    "window_number": 3,
    "message": { ... },
    "replacement_history": [ ... ]
  }
}
```

> ⚠️ `compacted` 记录可能包含大量内联 base64 图片数据，体积可达数十 MB。

---

4. Content Block 格式

`message.content` 和 `reasoning.summary` 使用统一的内容块数组：

块类型	用途	字段	
`input_text`	用户文本输入	`text`	
`input_image`	用户图片输入	`image_url` / `data`	
`output_text`	助手文本输出	`text`	
`summary_text`	推理摘要	`text`	
`reasoning_text`	推理内容	`text`	

```json
// User message
{ "type": "input_text", "text": "Hello world" }

// Assistant message
{ "type": "output_text", "text": "Hello! How can I help?" }

// Reasoning summary
{ "type": "summary_text", "text": "Analyzing requirements..." }
```

---

5. 完整示例

以下是一个最小但完整的 rollout 文件示例：

```jsonl
{"timestamp":"2026-01-01T00:00:00.000Z","type":"session_meta","payload":{"id":"sess-001","session_id":"sess-001","cwd":"/home/user/project","cli_version":"0.135.0","originator":"codex-tui","model_provider":"openai"}}
{"timestamp":"2026-01-01T00:00:01.000Z","type":"turn_context","payload":{"turn_id":"turn-001","cwd":"/home/user/project","model":"o4-mini","effort":"medium","approval_policy":"suggest"}}
{"timestamp":"2026-01-01T00:00:02.000Z","type":"response_item","payload":{"type":"message","role":"user","id":"msg-001","content":[{"type":"input_text","text":"List all files"}]}}
{"timestamp":"2026-01-01T00:00:03.000Z","type":"response_item","payload":{"type":"reasoning","id":"reason-001","summary":[{"type":"summary_text","text":"User wants to list files"}],"content":null,"encrypted_content":""}}
{"timestamp":"2026-01-01T00:00:04.000Z","type":"response_item","payload":{"type":"function_call","id":"fc-001","call_id":"call_01","name":"shell","namespace":"default","arguments":"{\"command\":[\"ls\"],\"workdir\":\"/home/user/project\"}"}}
{"timestamp":"2026-01-01T00:00:05.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_01","output":"{\"output\":\"README.md\\nsrc\\npackage.json\\n\",\"metadata\":{\"exit_code\":0,\"duration_seconds\":0.1}}"}}
{"timestamp":"2026-01-01T00:00:06.000Z","type":"response_item","payload":{"type":"message","role":"assistant","id":"msg-002","content":[{"type":"output_text","text":"Here are the files in your project:\\n\\n- README.md\\n- src\\n- package.json"}]}}
{"timestamp":"2026-01-01T00:00:07.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":50,"cached_input_tokens":0,"output_tokens":25,"reasoning_output_tokens":10,"total_tokens":85,"model_context_window":200000},"last_token_usage":{"input_tokens":50,"cached_input_tokens":0,"output_tokens":25,"reasoning_output_tokens":10,"total_tokens":85,"model_context_window":200000}}}}
{"timestamp":"2026-01-01T00:00:08.000Z","type":"event_msg","payload":{"type":"task_complete"}}
```

---

6. 编写代码生成 Rollout 的要点

1. 严格 JSONL：每行一个有效 JSON，不要有多行格式化 JSON
2. 时间戳单调递增：`timestamp` 应按记录顺序非递减
3. `call_id` 配对：`function_call` 和 `function_call_output` 必须通过 `call_id` 一一对应
4. `arguments` / `output` 是 JSON 字符串：不是嵌套对象，需要 `JSON.stringify()`
5. Assistant 文本用 `output_text`：现代格式使用 `output_text` 内容块，旧格式用 `text`
6. `encrypted_content` 处理：如果无加密内容，设为空字符串 `""`，不要省略该字段
7. Token 用量：`token_count` 的 `info` 对象包含 `total_token_usage` 和 `last_token_usage`
8. 文件首条必须是 `session_meta`：Codex 恢复会话时依赖此记录
