# CodeActor Agent 事件协议

## 概述

本目录定义了 CodeActor Agent 系统与 WebUI 之间通过 WebSocket 传输的所有事件消息的完整协议。
协议以 `agent-events.yaml` 作为**单一真相源（Single Source of Truth）**，通过 `protoc-gen-codeactor` 代码生成器自动生成 Go 类型、TypeScript 类型、JSON Schema 和渲染映射表。

## 架构

```
                    ┌──────────────────────────┐
                    │  agent-events.yaml        │ ◄── 单一真相源
                    │  (协议定义)               │
                    └──────────┬───────────────┘
                               │
                    ┌──────────▼───────────────┐
                    │  protoc-gen-codeactor     │ ◄── 代码生成器
                    │  (Go 程序)               │
                    │  scripts/protoc-gen-     │
                    │  codeactor/main.go       │
                    └──────┬──────┬──────┬─────┘
                           │      │      │
              ┌────────────┘      │      └────────────┐
              ▼                   ▼                   ▼
    ┌─────────────────┐ ┌──────────────┐ ┌──────────────────┐
    │ protocol/go/     │ │ protocol/ts/ │ │ JSON Schema      │
    │ agent-events.go  │ │ agent-events │ │ (agent-events.   │
    │ (Go 运行时类型)  │ │ .ts          │ │  schema.json)    │
    └────────┬─────────┘ │ + render-    │ └──────────────────┘
             │           │ mapping.ts   │
             ▼           └──────┬───────┘
    ┌─────────────────┐         │
    │ Go 后端          │         ▼
    │ (internal/http/  │ ┌──────────────────┐
    │  messaging/)     │ │ 前端 WebUI       │
    │ 使用生成类型替代  │ │ (ChatMessages.tsx)│
    │ interface{}      │ │ 使用类型守卫 +   │
    └─────────────────┘ │ 渲染映射表        │
                        └──────────────────┘

VSCode 插件:
  - 通过 JSON Schema (agent-events.schema.json) 感知协议
  - 在 VSCode 设置中配置 json.schemas 指向 schema 文件
  - 通过导入生成的 TypeScript 类型获得类型安全
```

## 文件说明

| 文件 | 说明 |
|------|------|
| `agent-events.yaml` | **权威协议定义**，所有消息类型的完整 schema |
| `go/agent-events.go` | 生成的 Go 类型定义 + 注册表 + 解析函数 |
| `ts/agent-events.ts` | 生成的 TypeScript 类型定义 + 类型守卫 |
| `ts/render-mapping.ts` | 生成的渲染映射表（JSON 格式），指导前端渲染 |
| `agent-events.schema.json` | 生成的 JSON Schema（含 `x-render-hint` 扩展） |
| `protoc-gen-codeactor` | 编译好的 codegen 可执行文件（Go 二进制） |

## 事件类型列表

协议定义了以下 **20 种** 事件类型，分为 6 个类别：

### Agent 核心事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `model_info` | LLM 模型信息（model, agent, provider） | — |
| `llm_call_start` | LLM 调用开始 | — |
| `llm_call_end` | LLM 调用结束（含 duration, error） | — |
| `ai_response` | AI 响应内容（含 token usage） | `StreamingText` |
| `tool_call_start` | 工具调用开始（tool_name, arguments, tool_call_id） | `ToolCallCard` |
| `tool_call_result` | 工具调用结果（tool_name, result, tool_call_id） | `ToolResultCard` |
| `tool_call_error` | 工具调用错误 | `ToolCallErrorCard` |

### 上下文管理事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `context_loaded` | 项目上下文加载完成 | `StatusPill` |
| `context_compressed` | 上下文压缩完成 | `StatusPill` |
| `commit_context_loaded` | Commit 学习器上下文加载 | `StatusPill` |

### 流式事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `ai_stream_start` | AI 流式响应开始 | `StreamingText` |
| `ai_chunk` | AI 流式响应数据块 | `StreamingText` |
| `ai_stream_end` | AI 流式响应结束 | `StreamingText` |

### 用户交互事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `user_help_needed` | 需要用户帮助/确认 | `UserConfirmCard` |
| `user_help_response` | 用户回复了帮助请求 | `StatusPill` |

### 任务生命周期事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `conversation_error` | 对话处理错误 | `ErrorCard` |
| `conversation_result` | 对话任务完成 | `ResultCard` |
| `task_complete` | 任务完成通知 | `TaskCompleteCard` |

### 状态更新事件

| 事件名称 | 说明 | 前端组件 |
|----------|------|----------|
| `status_update` | 通用状态更新 | `StatusPill` |
| `thinking` | Agent 思考过程（推理过程） | `ThinkingCard` |

## 使用方法

### 1. 修改协议

编辑 `agent-events.yaml`，然后重新运行 codegen：

```bash
# 从项目根目录执行
./protocol/protoc-gen-codeactor \
  -input protocol/agent-events.yaml \
  -output protocol
```

或使用 Go generate 方式：

```bash
go generate ./scripts/protoc-gen-codeactor/...
```

### 2. 在 Go 后端中使用

```go
import "codeactor/protocol/go" // 导入生成的协议包

// 发送事件时使用生成的数据类型
func sendToolCallResult(session *melody.Session, taskID string) {
    // 构造事件数据
    data := protocol.ToolCallResultData{
        ToolName:   "delegate_repo",
        Result:     "分析完成...",
        ToolCallId: "call_123",
    }
    dataRaw, _ := json.Marshal(data)

    // 构造传输信封
    envelope := protocol.AgentEventEnvelope{
        Type:   "realtime",
        Event:  string(protocol.EventTypeToolCallResult),
        Data:   dataRaw,
        From:   "Coding-Agent",
        TaskId: taskID,
    }

    msgBytes, _ := json.Marshal(envelope)
    session.Write(msgBytes)
}

// 接收事件时使用注册表解析
func handleMessage(msg []byte) {
    var envelope protocol.AgentEventEnvelope
    json.Unmarshal(msg, &envelope)

    eventData, err := protocol.ParseAgentEvent(envelope)
    if err != nil {
        // 未知事件类型
        return
    }

    switch d := eventData.(type) {
    case *protocol.ToolCallStartData:
        fmt.Printf("工具调用: %s\n", d.ToolName)
    case *protocol.ToolCallResultData:
        fmt.Printf("工具结果: %s\n", d.Result)
    case *protocol.ToolCallErrorData:
        fmt.Printf("工具错误: %s\n", d.Error)
    }
}
```

### 3. 在前端 WebUI 中使用

```typescript
import {
  AgentEvent, WebSocketMessage,
  isToolCallStart, isAiResponse, isThinking,
  EventTypes
} from '../protocol/ts/agent-events';
import renderMapping from '../protocol/ts/render-mapping';

// 使用类型守卫处理消息
function handleEvent(msg: WebSocketMessage) {
  if (isToolCallStart(msg)) {
    // msg 被自动推导为 ToolCallStartEvent 类型
    renderToolCallCard(msg.tool_name, msg.arguments);
  } else if (isAiResponse(msg)) {
    renderAiResponse(msg.content, msg.usage);
  } else if (isThinking(msg)) {
    renderThinkingCard(msg.content);
  }
}

// 使用渲染映射表指导 UI 渲染
function getRenderComponent(eventType: string) {
  const entry = renderMapping.events.find(e => e.event === eventType);
  return entry?.component || 'DefaultCard';
}
```

### 4. VSCode 插件感知协议

在 VSCode 插件的 `package.json` 中配置 JSON Schema 关联：

```json
{
  "contributes": {
    "jsonValidation": [
      {
        "fileMatch": "**/codeactor-protocol.json",
        "url": "./protocol/agent-events.schema.json"
      }
    ]
  }
}
```

或者直接在 VSCode 工作区设置中注册：

```json
{
  "json.schemas": [
    {
      "fileMatch": ["**/codeactor-websocket-message.json"],
      "url": "./protocol/agent-events.schema.json"
    }
  ]
}
```

插件代码中可以直接导入生成的 TypeScript 类型：

```typescript
import { AgentEvent, EventTypes } from './protocol/ts/agent-events';

// 插件可以预先知道所有事件类型
console.log(`支持的事件类型: ${Object.values(EventTypes).join(', ')}`);
```

## WebSocket 消息格式

所有消息通过统一的信封格式传输：

```json
{
  "type": "realtime",
  "event": "tool_call_start",
  "data": {
    "tool_name": "delegate_coding",
    "arguments": "{\"task\": \"...\"}",
    "tool_call_id": "call_123"
  },
  "from": "Conductor-Agent",
  "task_id": "task_456",
  "message": ""
}
```

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `type` | string | 是 | 消息类型标识（如 `realtime`, `connection`, `error`） |
| `event` | string | 是 | 事件名称（对应 `event_types` 中的 name） |
| `data` | object | 是 | 事件数据（具体结构由 event 决定） |
| `from` | string | 否 | 消息来源（Agent 名称或 `System`） |
| `task_id` | string | 否 | 关联的任务 ID |
| `message` | string | 否 | 简短文本消息（用于错误通知等） |

## 数据类型映射

| YAML 类型 | Go 类型 | TypeScript 类型 | JSON Schema 类型 |
|-----------|---------|-----------------|------------------|
| `string` | `string` | `string` | `"string"` |
| `number` | `float64` | `number` | `"number"` |
| `boolean` | `bool` | `boolean` | `"boolean"` |
| `object` | `json.RawMessage` / 结构体 | `Record<string, any>` / 接口 | `"object"` |
| `array` | `[]json.RawMessage` / `[]Type` | `any[]` / `Type[]` | `"array"` |

## 协议扩展指南

### 添加新事件类型

1. 在 `agent-events.yaml` 的 `event_types` 列表中添加新条目
2. 定义字段、类型、是否必填
3. 可选添加 `render_hint` 指定前端如何渲染
4. 运行 codegen 生成所有代码
5. 前端实现对应的渲染组件

示例：

```yaml
- name: file_change_applied
  description: "文件变更已应用"
  render_hint:
    component: FileChangeCard
    heading: "文件变更"
    show_header: true
    collapse: true
  fields:
    - name: file_path
      type: string
      description: "文件路径"
      required: true
    - name: change_type
      type: string
      enum: ["added", "modified", "deleted"]
      description: "变更类型"
      required: true
    - name: diff
      type: string
      description: "变更的 diff 内容"
      required: false
```

### 修改现有事件

1. 修改 `agent-events.yaml` 中的字段定义
2. **注意**：移除或重命名字段是破坏性变更，需要同步更新所有生产者和消费者
3. 运行 codegen

### 渲染提示（render_hint）字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `component` | string | 前端渲染组件名称（如 `StreamingText`, `ToolCallCard`） |
| `heading` | string | 卡片/组件的标题文本 |
| `show_header` | boolean | 是否显示头部 |
| `collapse` | boolean | 是否可折叠（默认展开） |
| `stream_mode` | boolean | 是否为流式模式 |
| `merge_consecutive` | boolean | 是否合并连续的同类型消息 |
| `max_preview_lines` | number | 预览最大行数（超出折叠） |

## 消息流架构

Agent 后端通过 `MessagePublisher` 发布事件，经 `MessageDispatcher` 分发到所有注册的消费者（TUI、WebSocket 等）：

```
┌──────────────────────────────────────────────────────────┐
│  Agent 内部（conductor.go / executor.go）                 │
│                                                          │
│  Publish("event_type", content, from)                    │
│       │                                                  │
│       ▼                                                  │
│  ┌─────────────────────────────────────┐                 │
│  │ MessagePublisher.Publish()          │                 │
│  │  → 创建 MessageEvent {Type, Content,│                 │
│  │     From, Timestamp, Metadata}      │                 │
│  └────────────┬────────────────────────┘                 │
│               │                                          │
│               ▼                                          │
│  ┌─────────────────────────────────────┐                 │
│  │ MessageDispatcher.Publish()         │                 │
│  │  → 广播到所有注册的 Consumer         │                 │
│  └──────┬──────────────┬───────────────┘                 │
│         │              │                                 │
│         ▼              ▼                                 │
│  ┌────────────┐ ┌──────────────┐                         │
│  │ TUIConsumer│ │WebSocket     │                         │
│  │ (终端显示) │ │Consumer      │                         │
│  └────────────┘ │(HTTP/WS 推)  │                         │
│                 └──────────────┘                         │
└──────────────────────────────────────────────────────────┘
```

## 设计原则

1. **单一真相源**：`agent-events.yaml` 是唯一权威定义，所有生成产物均源自此文件
2. **向前兼容**：添加新字段时设为 `required: false`，避免破坏现有消费者
3. **渲染与数据分离**：渲染提示放在 `render_hint` 中，不污染业务数据字段
4. **类型安全**：通过 codegen 确保 Go 和 TypeScript 类型完全一致，消除运行时类型错误
5. **VSCode 原生支持**：JSON Schema 是 VSCode 原生支持的协议感知方式，编辑器可直接提供代码补全和校验
6. **零手工同步**：修改协议后只需运行 codegen，所有语言绑定自动更新

## 相关代码位置

| 组件 | 位置 |
|------|------|
| 协议定义 (YAML) | `protocol/agent-events.yaml` |
| Codegen 源码 | `scripts/protoc-gen-codeactor/` |
| Codegen 二进制 | `protocol/protoc-gen-codeactor` |
| Go 生成类型 | `protocol/go/agent-events.go` |
| TS 生成类型 | `protocol/ts/agent-events.ts` |
| 渲染映射表 | `protocol/ts/render-mapping.ts` |
| JSON Schema | `protocol/agent-events.schema.json` |
| Go 消息发布器 | `internal/messaging/message_publisher.go` |
| Go 消息分发器 | `internal/messaging/message_dispatcher.go` |
| TUI 消费者 | `internal/messaging/consumers/tui.go` |
| WebSocket 消费者 | `internal/http/websocket.go` |
| Agent 发布调用 | `internal/agents/conductor.go`, `internal/agents/executor.go` |
