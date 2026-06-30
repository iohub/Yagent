# CodeActor WebUI - 仓库背景知识

## 项目概述

CodeActor WebUI 是一个基于 React + TypeScript 构建的 Web 前端应用，作为 CodeActor VSCode 扩展的 Webview 界面。它允许用户在浏览器或 VSCode 扩展环境中与 AI 编程助手进行交互。

**项目名称**: `codeactor-webui`
**版本**: 1.0.0
**构建工具**: React Scripts (Create React App)
**UI框架**: Tailwind CSS + VSCode 风格主题

---

## 技术栈

| 类别 | 技术 |
|------|------|
| 核心框架 | React 18.2.0 |
| 类型系统 | TypeScript 4.9.5 |
| 样式 | Tailwind CSS 3.3.6 |
| UI 组件库 | lucide-react (图标) |
| Markdown 渲染 | react-markdown + remark-gfm |
| 包管理器 | npm |

---

## 目录结构

```
webui/
├── src/
│   ├── index.tsx              # React 入口文件
│   ├── App.tsx                # 根组件 - 应用状态管理和布局
│   ├── components/            # UI 组件目录
│   │   ├── ChatInput.tsx      # 消息输入框组件
│   │   ├── ChatMessages.tsx   # 聊天消息列表和渲染组件
│   │   ├── Header.tsx         # 顶部导航栏
│   │   ├── WelcomeMessage.tsx # 欢迎消息组件
│   │   ├── SettingsPageFull.tsx # 设置页面
│   │   ├── AddModelModal.tsx  # 添加自定义模型模态框
│   │   ├── TaskHistory.tsx    # 任务历史组件
│   │   ├── TaskHistoryModal.tsx # 任务历史模态框
│   │   └── index.ts           # 组件导出
│   ├── hooks/
│   │   └── conversation.ts    # 会话状态管理
│   ├── utils/
│   │   ├── vscode.ts          # VSCode API 封装 (单例模式)
│   │   ├── vscode-mock.ts     # VSCode API 浏览器模拟实现
│   │   ├── serverApi.ts       # WebSocket 服务器通信 API
│   │   ├── configManager.ts   # 配置管理 (单例模式)
│   │   ├── mockData.ts        # 模拟消息数据
│   │   └── mockData.ts
│   ├── types/
│   │   └── index.ts           # TypeScript 类型定义
│   └── styles/
│       └── globals.css        # 全局样式和 Tailwind 入口
├── public/
├── build/                     # 构建输出目录
├── package.json
├── tailwind.config.js         # Tailwind 配置 (VSCode 主题变量)
├── tsconfig.json
└── postcss.config.js
```

---

## 核心架构

### 1. 多环境适配架构

该项目支持三种运行环境，通过 `vscode.ts` 中的适配器模式统一接口：

```
┌─────────────────────────────────────────────────────────┐
│                     App.tsx                             │
│              useChat() → vscodeAPI.postMessage()        │
└─────────────────────────────────────────────────────────┘
                           │
           ┌───────────────┼───────────────┐
           ▼               ▼               ▼
    ┌────────────┐  ┌────────────┐  ┌────────────┐
    │  VSCode    │  │  Server    │  │   Mock     │
    │   Mode     │  │   Mode     │  │   Mode     │
    │(扩展环境)   │  │(WebSocket) │  │(浏览器预览)  │
    └────────────┘  └────────────┘  └────────────┘
```

**环境检测逻辑** (`vscode.ts:156-162`):
- **VSCode 模式**: `window.vscode` 存在 → 使用 `VSCodeAPI`
- **服务器模式**: `window.SERVER_URL` 或 URL 参数 `?server` → 使用 `ServerAPI`
- **Mock 模式**: 浏览器预览环境 → 使用 `VSCodeAPIMock`

### 2. API 适配器

#### VSCodeAPI (`vscode.ts`)
- 单例模式，通过 `postMessage` 与 VSCode 扩展通信
- 支持消息订阅/取消订阅 (`onMessage`)
- 提供状态管理和消息存储功能

#### ServerAPI (`serverApi.ts`)
- WebSocket 连接到远程 Claude Code 服务器
- 支持自动重连 (最多 5 次)
- HTTP REST API 创建会话，WebSocket 维持通信
- 消息类型转换: `ServerMessage` ↔ `ExtensionMessage`

#### VSCodeAPIMock (`vscode-mock.ts`)
- 浏览器环境下的完整模拟实现
- 自动生成模拟响应 (chat/agent/code 模式)
- 支持任务提交模拟 (`submitTask`)

### 3. 状态管理

#### useChat Hook (`useChat.ts`)
核心状态管理，提供:

```typescript
// Chat 状态
interface ChatState {
  messages: Message[];      // 消息列表
  isProcessing: boolean;   // 是否正在处理
  currentMode: 'chat' | 'agent';  // 当前模式
  theme: 'light' | 'dark';
}

// Task 状态
interface TaskState {
  isTaskRunning: boolean;
  currentTask: string;
  taskProgress: number;
  currentStep: string;
  taskStatus: 'running' | 'completed' | 'failed' | 'terminated';
  taskId?: string;
}
```

**关键功能**:
- `sendMessage(text)`: 发送聊天消息
- `submitTask(taskDesc)`: 提交任务
- `terminateTask()`: 终止正在运行的任务
- `clearChat()`: 清空聊天
- `handleMessageAction()`: 处理消息操作 (重试/继续)

### 4. 消息处理流程

```
用户输入 → useChat.sendMessage()
    │
    ▼
vscodeAPI.postMessage(WebviewMessage)
    │
    ├─► VSCode Mode: 发送到扩展端
    ├─► Server Mode: WebSocket 发送到服务器
    └─► Mock Mode: 模拟响应
    │
    ▼
vscodeAPI.onMessage(ExtensionMessage)
    │
    ▼
useChat.handleExtensionMessage()
    │
    ▼
addMessageFromExtension(Message)
    │
    ▼
setChatState → 更新 UI
```

### 5. 消息类型定义 (`types/index.ts`)

```typescript
// 核心消息结构
interface Message {
  id: string;
  text?: string;
  sender?: 'user' | 'assistant' | 'system' | 'Director' | 'Coding-Agent';
  timestamp: number;
  type?: 'text' | 'result' | 'thinking' | 'tool_call_start' | 'tool_call_result';
  data?: any;
  metadata?: {
    toolName?: string;
    toolCallId?: string;
    result?: any;
    taskStatus?: 'completed' | 'failed' | 'in_progress';
    // ... 其他元数据
  };
}

// Webview → 扩展的消息格式
interface WebviewMessage {
  type: 'chat' | 'agent' | 'code' | 'submitTask' | 'terminateTask' | 'clearChat';
  id: string;
  payload: { text?, taskDesc?, mode?, messages?, ... };
}

// 扩展 → Webview 的消息格式
interface ExtensionMessage {
  type: 'initialize' | 'error' | 'ai_response' | 'tool_call_start' |
        'tool_call_result' | 'task_complete' | 'conversation_end' | ...;
  id?: string;
  data?: any;
  error?: string;
}
```

---

## 组件架构

### 1. App.tsx (根组件)

**职责**:
- 环境检测和初始化
- 协调子组件布局
- 管理设置页面和任务历史模态框的显示

**状态**:
- `showSettings`: 是否显示设置页面
- `showTaskHistoryModal`: 是否显示任务历史
- `isServerConnected`: 服务器连接状态

### 2. ChatMessages.tsx (消息渲染)

**核心组件**:
- `MessageItem`: 单条消息渲染
- `MessageGroup`: 消息分组 (按 sender 聚合)
- `ToolCallState`: 工具调用状态管理

**特性**:
- 消息分组折叠/展开
- 工具调用状态追踪 (running/completed/error)
- Thinking 消息缓存和展开详情
- Markdown 渲染 (代码高亮、GFM 支持)

### 3. ChatInput.tsx (输入组件)

**功能**:
- 多行文本输入 (5-10 行自动调整)
- 模式切换 (Chat / Agent)
- 模型选择下拉菜单
- 角色切换 (Developer / Designer / PM / Tester / DevOps)
- 上下文插入 (@Files / @Folders / @Code / @Docs / @Web)
- 任务运行时显示 Abort 按钮

### 4. SettingsPageFull.tsx (设置页面)

**Tab 页**:
- **Models**: 模型列表 (默认 + 自定义)
- **Rules**: 规则配置 (预留)
- **Tools**: 工具配置 (预留)
- **Agents**: Agent 配置 (预留)
- **Indexing**: 代码索引设置

---

## 主题系统

### VSCode 主题变量 (`globals.css`)

```css
/* 暗色主题 */
--vscode-editor-background: #1e1e1e;
--vscode-sideBar-background: #252526;
--vscode-editorWidget-background: #2d2d30;
--vscode-editor-foreground: #d4d4d4;
--vscode-descriptionForeground: #9d9d9d;
--vscode-widgetBorder: #454545;
--vscode-button-background: #0e639c;
--vscode-button-hoverBackground: #1177bb;
```

### Tailwind 扩展 (`tailwind.config.js`)

```javascript
// 通过 CSS 变量引用 VSCode 主题色
colors: {
  vscode: {
    bg: {
      primary: 'var(--vscode-editor-background)',
      secondary: 'var(--vscode-sideBar-background)',
      tertiary: 'var(--vscode-editorWidget-background)',
    },
    text: {
      primary: 'var(--vscode-editor-foreground)',
      secondary: 'var(--vscode-descriptionForeground)',
    },
    border: 'var(--vscode-widgetBorder)',
    accent: 'var(--vscode-button-background)',
  }
}
```

---

## 通信协议

### WebSocket 消息格式 (Server Mode)

**客户端 → 服务器**:
```typescript
interface ClientMessage {
  type: 'prompt' | 'interrupt' | 'ping';
  content?: string;
}
```

**服务器 → 客户端**:
```typescript
interface ServerMessage {
  type: 'welcome' | 'message' | 'tool_use' | 'tool_result' |
        'error' | 'pong' | 'done' | 'thinking' | 'status';
  sessionId?: string;
  cwd?: string;
  data?: any;
}
```

### 会话创建流程

```
1. POST /api/sessions → { sessionId, cwd }
2. WebSocket /session/{sessionId}
3. 双向消息通信
```

---

## 配置管理 (`configManager.ts`)

单例模式，管理应用配置:

```typescript
interface CodeActorConfig {
  selectedModel: string;      // 当前选中模型
  selectedRole: string;      // 当前角色
  theme: 'light' | 'dark';
  language: string;
  enableAutoSave: boolean;
  enableNotifications: boolean;
  customModels: Array<{
    name: string;
    apiKey: string;
    endpoint: string;
    model: string;
  }>;
}
```

**配置变更监听**: 通过 `onConfigChange(listener)` 订阅配置变更。

---

## 构建和部署

### 构建命令

```bash
# 开发模式
npm start

# 生产构建
npm run build

# 服务器模式构建 (WebSocket 支持)
REACT_APP_SERVER_URL=ws://localhost:3000 npm run build:server

# 复制到扩展目录
npm run copy-to-extension
```

### 开发服务器模式

启动后访问: `http://localhost:3000?server&url=ws://localhost:3000`

---

## 扩展集成

作为 VSCode 扩展的 Webview 运行时，通过以下方式与扩展通信:

1. **初始化**: 扩展发送 `initialize` 消息
2. **配置**: 通过 `configResponse` 消息传递配置
3. **通信**: 双向消息通道 (`postMessage` / `onMessage`)
4. **状态同步**: `getState()` / `setState()`

---

## 依赖关系图

```
App.tsx
├── useChat (hook)
│   ├── vscodeAPI
│   │   ├── VSCodeAPI (单例)
│   │   ├── ServerAPI (单例)
│   │   └── VSCodeAPIMock (单例)
│   ├── configManager (单例)
│   └── mockMessageLoader
├── ChatMessages
│   └── react-markdown, remark-gfm
├── ChatInput
│   └── lucide-react (图标)
├── Header
└── SettingsPageFull
    └── configManager
```

---

## 关键设计模式

1. **单例模式**: VSCodeAPI, ServerAPI, ConfigManager, MockMessageLoader
2. **适配器模式**: 统一的 `vscodeAPI` 接口屏蔽底层实现差异
3. **发布-订阅模式**: 消息处理通过 `onMessage` 订阅机制
4. **Hooks 模式**: 状态逻辑封装在 `useChat` 中

---

## 消息卡片渲染详解

### 1. 渲染流程总览

```
useChat (消息来源)
    │
    ▼
ChatMessages (容器组件)
    │
    ├─► useEffect: 处理工具调用状态 [toolCallStates Map]
    │
    ├─► useEffect: 监听 messages 变化，自动滚动到底部
    │
    └─► useEffect: 监听主题变化 (MutationObserver)
    │
    ▼
groupedMessages (按 sender 分组)
    │
    ▼
MessageGroup[] (可折叠的分组)
    │
    ▼
MessageItem[] (单条消息)
    │
    ├─► 工具调用消息: renderToolCallContent()
    ├─► Thinking 消息: 特殊样式 + 可展开详情
    ├─► 普通文本消息: ReactMarkdown 渲染
    └─► 操作按钮: renderActions()
```

### 2. 消息分组逻辑

**分组策略** (`ChatMessages.tsx:671-683`):

```typescript
const groupedMessages = messages
  .filter(message => message.type !== 'finish' && message.type !== 'taskSubmitted')
  .reduce<{ sender: string; messages: Message[] }[]>((acc, message) => {
    const sender = message.sender || 'Agent';
    const lastGroup = acc[acc.length - 1];
    
    // 相同 sender 合并到同一组
    if (lastGroup && lastGroup.sender === sender) {
      lastGroup.messages.push(message);
    } else {
      acc.push({ sender, messages: [message] });
    }
    return acc;
  }, []);
```

**分组原则**:
- 过滤掉 `finish` 和 `taskSubmitted` 类型消息
- 按 `sender` (消息发送者) 分组
- 相同 sender 的连续消息合并为一个 `MessageGroup`
- 自动展开最后一组消息

### 3. 工具调用状态管理

**核心数据结构** (`ChatMessages.tsx:16-24`):

```typescript
interface ToolCallState {
  toolCallId: string;           // 工具调用唯一 ID
  toolName: string;             // 工具名称
  status: 'running' | 'completed' | 'error';  // 执行状态
  startTime: number;             // 开始时间戳
  endTime?: number;             // 结束时间戳
  result?: any;                 // 执行结果
  error?: string;               // 错误信息
}
```

**状态更新流程** (`ChatMessages.tsx:560-644`):

```typescript
useEffect(() => {
  const newToolCallStates = new Map<string, ToolCallState>();
  const newResultToStartIdMap = new Map<string, string>();
  const pendingTools = new Map<string, string>(); // toolName → toolCallId (running)

  messages.forEach(message => {
    // 1. tool_call_start: 创建 running 状态
    if (message.type === 'tool_call_start' && message.metadata?.toolCallId) {
      const toolCallId = message.metadata.toolCallId;
      newToolCallStates.set(toolCallId, {
        toolCallId,
        toolName: message.metadata.toolName || 'Unknown Tool',
        status: 'running',
        startTime: message.timestamp
      });
    }

    // 2. tool_call_result: 更新为 completed 状态
    if (message.type === 'tool_call_result' && message.metadata?.toolCallId) {
      const toolCallId = message.metadata.toolCallId;
      const existingState = newToolCallStates.get(toolCallId);
      if (existingState) {
        newToolCallStates.set(toolCallId, {
          ...existingState,
          status: 'completed',
          endTime: message.timestamp,
          result: message.metadata.result
        });
      }
    }

    // 3. tool_call_error: 更新为 error 状态
    if (message.type === 'tool_call_error' && message.metadata?.toolCallId) {
      // ... 类似处理
    }
  });

  setToolCallStates(newToolCallStates);
  setResultToStartIdMap(newResultToStartIdMap);
}, [messages]);
```

**关键机制**:
- `toolCallStates`: Map 结构，通过 toolCallId 快速查找状态
- `pendingTools`: 处理无 toolCallId 的情况，通过 toolName 匹配
- `resultToStartIdMap`: tool_call_result 消息 ID → tool_call_start toolCallId 的映射
- 执行时间统计: `endTime - startTime`

### 4. 工具调用消息合并显示

**合并逻辑** (`ChatMessages.tsx:337-355`):

```typescript
// 如果是工具调用消息且已经被合并处理，则不显示
let effectiveToolCallId = message.metadata?.toolCallId;
if (!effectiveToolCallId) {
  if (message.type === 'tool_call_start') {
    effectiveToolCallId = message.id;
  } else if (message.type === 'tool_call_result' && resultToStartIdMap) {
    effectiveToolCallId = resultToStartIdMap.get(message.id);
  }
}

// 只显示第一个工具调用消息（tool_call_start）
if ((message.type === 'tool_call_start' || message.type === 'tool_call_result') &&
    effectiveToolCallId && toolCallStates?.has(effectiveToolCallId)) {
  const toolCallState = toolCallStates.get(effectiveToolCallId);
  // 跳过非首个的 tool_call_start 和所有 tool_call_result
  if (message.type === 'tool_call_result' ||
      (message.type === 'tool_call_start' && toolCallState &&
       toolCallState.startTime !== message.timestamp)) {
    return null;  // 不渲染
  }
}
```

**显示效果**:
- 只渲染 `tool_call_start` 消息作为代表
- `tool_call_result` 消息被合并到 start 消息中显示
- 通过展开按钮查看详细结果

### 5. Thinking 消息处理

**缓存机制** (`ChatMessages.tsx:36, 66-69`):

```typescript
const thinkingCache = new Map<string, string>(); // 全局缓存

// 首次渲染时缓存原始文本
if (isThinkingType && message.text && !thinkingCache.has(message.id)) {
  thinkingCache.set(message.id, message.text);
}
```

**渲染逻辑** (`ChatMessages.tsx:372-389`):

```typescript
{isThinkingType ? (
  <details className="text-[10px] text-vscode-text-secondary/60 italic border-l border-vscode-border/40 pl-2 cursor-pointer group">
    <summary className="flex items-center gap-1 hover:text-vscode-text-secondary/80 transition-colors select-none">
      <Sparkles className="w-2.5 h-2.5 text-vscode-text-secondary/40" />
      <span>思考过程</span>
    </summary>
    <pre className="mt-1.5 pl-3 text-[10px] font-mono text-vscode-text-secondary/70 whitespace-pre-wrap break-all max-h-32 overflow-y-auto leading-relaxed">
      {message.text}
    </pre>
  </details>
) : ...}
```

**技术要点**:
- 默认折叠: 使用 `<details>` 无 `open` 属性，用户点击展开
- 低调样式: 更小字号 (10px)、更低透明度 (60%)、更细边框
- 无截断设计: 直接折叠，用户主动展开查看完整内容
- 视觉标识: 细边框 + 小号 Sparkles 图标 + 斜体文字

### 6. 消息内容渲染


**Markdown 渲染** (`ChatMessages.tsx:404-422`):

```typescript
<ReactMarkdown
  remarkPlugins={[remarkGfm]}
  className="text-sm text-vscode-text-primary prose prose-invert max-w-none"
  components={{
    p: ({ children }) => <p className="mb-2 last:mb-0">{children}</p>,
    code: ({ children }) => (
      <code className="bg-vscode-bg-tertiary px-1 py-0.5 rounded text-xs font-mono">
        {children}
      </code>
    ),
    pre: ({ children }) => (
      <pre className="bg-vscode-bg-tertiary p-2 rounded overflow-x-auto my-2 text-xs font-mono">
        {children}
      </pre>
    ),
  }}
>
  {textContent || ''}
</ReactMarkdown>
```

**代码块检测** (`ChatMessages.tsx:397-402`):

```typescript
{textContent && textContent.includes('```') ? (
  <div className="bg-vscode-bg-tertiary p-2 rounded overflow-x-auto">
    <pre className="text-sm font-mono text-vscode-text-primary whitespace-pre-wrap">
      <code>{textContent.replace(/```[\w]*\n?|```/g, '').trim()}</code>
    </pre>
  </div>
) : (
  // Markdown 渲染
)}
```

### 7. 消息操作 (Actions)

**Actions 定义结构** (`types/index.ts:48-53`):

```typescript
interface MessageAction {
  id: string;
  label: string;          // 按钮显示文本
  type: 'primary' | 'secondary' | 'danger';
  action: () => void;    // 回调函数
}
```

**渲染逻辑** (`ChatMessages.tsx:315-335`):

```typescript
const renderActions = () => {
  if (!message.metadata?.actions) return null;

  return (
    <div className="mt-2 flex gap-2">
      {message.metadata.actions.map((action) => (
        <button
          key={action.id}
          onClick={() => onMessageAction?.(message.id, action.id)}
          className={`px-3 py-1 rounded text-xs font-medium transition-colors ${
            action.type === 'primary'
              ? 'bg-vscode-accent text-white hover:bg-vscode-accent/80'
              : action.type === 'danger'
                ? 'bg-red-500 text-white hover:bg-red-600'
                : 'bg-vscode-bg-tertiary text-vscode-text-primary hover:bg-vscode-bg-quaternary border border-vscode-border'
          }`}
        >
          {action.label}
        </button>
      ))}
    </div>
  );
};
```

**操作处理** (`useChat.ts:519-555`):

```typescript
const handleMessageAction = (messageId: string, actionId: string) => {
  const message = chatState.messages.find(msg => msg.id === messageId);
  if (!message) return;

  switch (actionId) {
    case 'retry':
      // 重试：重新提交当前任务
      if (message.metadata?.taskId && taskState.currentTask) {
        submitTask(taskState.currentTask);
      }
      break;
    case 'continue':
      // 继续对话：清除错误状态
      setTaskState(prev => ({ ...prev, taskStatus: 'completed', isTaskRunning: false }));
      setProcessing(false);
      addMessage('✅ 您可以继续对话或提交新任务', 'system');
      break;
  }
};
```

### 8. 工具调用详情展示

**展开/折叠状态** (`ChatMessages.tsx:72-84`):

```typescript
const [expandedToolCalls, setExpandedToolCalls] = useState<Set<string>>(new Set());

const toggleToolCallExpansion = (toolCallId: string) => {
  setExpandedToolCalls(prev => {
    const newSet = new Set(prev);
    if (newSet.has(toolCallId)) {
      newSet.delete(toolCallId);
    } else {
      newSet.add(toolCallId);
    }
    return newSet;
  });
};
```

**结果内容解析** (`ChatMessages.tsx:238-257`):

```typescript
const getResultInfo = () => {
  if (!toolCallState.result) return null;

  let content = '';
  let lines = 0;

  if (typeof toolCallState.result === 'string') {
    content = toolCallState.result;
    lines = content.split('\n').length;
  } else if (toolCallState.result.content && toolCallState.result.lines) {
    // 处理 read_file 工具的结果格式
    content = toolCallState.result.content;
    lines = toolCallState.result.lines;
  } else {
    content = JSON.stringify(toolCallState.result, null, 2);
    lines = content.split('\n').length;
  }

  return { content, lines };
};
```

### 9. 任务状态渲染

**状态配置** (`ChatMessages.tsx:123-142`):

```typescript
const statusConfig = {
  'in_progress': {
    icon: Loader,
    color: 'text-blue-400',
    bgColor: 'bg-blue-500/10',
    label: '进行中'
  },
  'completed': {
    icon: CheckCircle,
    color: 'text-green-400',
    bgColor: 'bg-green-500/10',
    label: '已完成'
  },
  'failed': {
    icon: XCircle,
    color: 'text-red-400',
    bgColor: 'bg-red-500/10',
    label: '失败'
  }
};
```

### 10. 时间格式化

```typescript
const formatTime = (timestamp: number) => {
  return new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit'
  });
};
```

### 11. 消息图标映射

```typescript
const getIcon = () => {
  if (message.type === 'ai_response') {
    return <Bot className="w-4 h-4 text-green-400" />;
  }
  if (message.type === 'tool_call_start' || message.type === 'tool_call_result') {
    return <Play className="w-4 h-4 text-purple-400" />;
  }
  switch (message.sender) {
    case 'user':
      return <User className="w-4 h-4 text-blue-400" />;
    case 'assistant':
      return <Bot className="w-4 h-4 text-green-400" />;
    case 'system':
      return <AudioLines className="w-4 h-4 text-orange-400" />;
  }
};
```

### 12. 渲染性能优化

**关键技术点**:

1. **useMemo 缓存消息计数** (`ChatMessages.tsx:457-486`):
   ```typescript
   const messageCount = useMemo(() => {
     let count = 0;
     const seenToolCallIds = new Set<string>();
     messages.forEach(msg => {
       // 工具调用消息合并计数
       if (msg.type === 'tool_call_start' || msg.type === 'tool_call_result') {
         let toolCallId = msg.metadata?.toolCallId || msg.id;
         if (!seenToolCallIds.has(toolCallId)) {
           seenToolCallIds.add(toolCallId);
           count++;
         }
       } else {
         count++;
       }
     });
     return count;
   }, [messages, resultToStartIdMap]);
   ```

2. **自动滚动到底部** (`ChatMessages.tsx:646-648`):
   ```typescript
   useEffect(() => {
     scrollToBottom();
   }, [messages]);

   const scrollToBottom = () => {
     messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
   };
   ```

3. **useRef 引用 DOM**:
   - `messagesEndRef`: 滚动锚点
   - `messagesContainerRef`: 监听容器

### 13. 消息类型汇总

| Type | 来源 | 渲染方式 |
|------|------|----------|
| `ai_response` | AI 回复 | ReactMarkdown |
| `tool_call_start` | 工具开始 | 合并显示 + 状态徽章 |
| `tool_call_result` | 工具结果 | 合并到 start 消息 |
| `tool_call_error` | 工具错误 | 错误样式 |
| `thinking` | AI 思考 | 截断 + 可展开详情 |
| `result` | 任务结果 | 普通文本 |
| `init` | 初始化 | 跳过不显示 |
| `memory_change` | 内存变更 | 简短文本 |
| `status_update` | 状态更新 | 进度指示器 |
| `finish` | 完成 | 过滤不显示 |
| `taskSubmitted` | 任务提交 | 过滤不显示 |

---

## 注意事项

- 消息存储: 每次状态更新自动调用 `saveMessagesToFile()`
- 消息去重: 通过 `skipMessageIds` 和 `pendingAssistantId` 避免重复显示
- Thinking 缓存: 使用 `thinkingCache` Map 缓存原始 thinking 文本
- 工具调用合并: `toolCallStates` Map 追踪工具状态，合并 start/result 显示
