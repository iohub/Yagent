export interface Message {
  id: string;
  text?: string; // Optional for compatibility
  sender?: string; // 'user' | 'assistant' | 'system' | 'Director' | 'Coding-Agent'
  timestamp: number;
  type?: string;
  data?: any;
  metadata?: {
    language?: string;
    fileName?: string;
    lineNumber?: number;
    // 文件变更相关
    fileChanges?: FileChange[];
    changeStats?: {
      additions: number;
      deletions: number;
    };
    // 任务完成相关
    taskStatus?: 'completed' | 'failed' | 'in_progress';
    // 变更确认相关
    actions?: MessageAction[];
    // 文件审查相关
    reviewStats?: {
      totalFiles: number;
      reviewedFiles: number;
    };
    // tool_call相关
    toolName?: string;
    toolCallId?: string;
    arguments?: any;
    result?: any;
    error?: string;
    // LLM错误相关
    taskId?: string;
    canRetry?: boolean;
    [key: string]: any;
  };
}

export interface FileChange {
  fileName: string;
  additions: number;
  deletions: number;
  status: 'added' | 'modified' | 'deleted' | 'renamed';
  preview?: string;
}

export interface MessageAction {
  id: string;
  label: string;
  type: 'primary' | 'secondary' | 'danger';
  action: () => void;
}

export interface ChatState {
  messages: Message[];
  isProcessing: boolean;
  currentMode: 'chat' | 'agent';
  theme: 'light' | 'dark';
}

export interface VSCodeTheme {
  kind: 'light' | 'dark' | 'highContrast' | 'highContrastLight';
  colors: {
    [key: string]: string;
  };
}

export interface ExtensionMessage {
  type: string;
  id?: string;
  payload?: any;
  [key: string]: any;
}

export interface WebviewMessage {
  type: 'chat' | 'agent' | 'code' | 'submitTask' | 'terminateTask' | 'clearChat' | 'saveMessages';
  id: string;
  payload: {
    text?: string;
    action?: string;
    code?: string;
    mode?: string;
    projectDir?: string;
    taskDesc?: string;
    timestamp: number;
    messages?: Message[];
    filePath?: string;
    [key: string]: any;
  };
}

// =============================================================================
// 以下类型由 protocol/ts/agent-events.ts 生成，保持同步
// =============================================================================

/** 
 * WebSocket 消息类型（来自后端的原始消息）
 * 与 protocol.AgentEventEnvelope 对应
 */
export interface ServerWebSocketMessage {
  type: string;
  event: string;
  data?: any;
  from?: string;
  task_id?: string;
  message?: string;
}

/**
 * 从 ServerWebSocketMessage 转换为前端的 Message
 */
export function serverMessageToChatMessage(
  serverMsg: ServerWebSocketMessage,
  existingMessages: Message[]
): Message | null {
  const event = serverMsg.event;
  const data = serverMsg.data || {};
  const timestamp = data?.timestamp || Date.now();
  const taskId = data?.task_id || serverMsg.task_id;

  switch (event) {
    case 'ai_response':
      return {
        id: `msg-${taskId}-${timestamp}`,
        text: data?.content || serverMsg.message || '',
        sender: serverMsg.from || 'assistant',
        timestamp,
        type: 'result',
        metadata: {
          taskId,
          usage: data?.usage,
        },
      };

    case 'tool_call_start':
      return {
        id: `tool-start-${data?.tool_call_id || timestamp}`,
        text: `⚡ ${data?.tool_name || '未知工具'}`,
        sender: 'system',
        timestamp,
        type: 'tool_call_start',
        metadata: {
          toolName: data?.tool_name,
          toolCallId: data?.tool_call_id,
          arguments: data?.arguments,
        },
      };

    case 'tool_call_result':
      return {
        id: `tool-result-${data?.tool_call_id || timestamp}`,
        text: data?.result || '',
        sender: 'system',
        timestamp,
        type: 'tool_call_result',
        metadata: {
          toolName: data?.tool_name,
          toolCallId: data?.tool_call_id,
          result: data?.result,
        },
      };

    case 'tool_call_error':
      return {
        id: `tool-error-${data?.tool_call_id || timestamp}`,
        text: `❌ ${data?.error || '工具执行失败'}`,
        sender: 'system',
        timestamp,
        type: 'tool_call_error',
        metadata: {
          toolName: data?.tool_name,
          toolCallId: data?.tool_call_id,
          error: data?.error,
        },
      };

    case 'thinking':
      return {
        id: `thinking-${taskId}-${timestamp}`,
        text: data?.content || '',
        sender: 'assistant',
        timestamp,
        type: 'thinking',
        metadata: {
          taskId,
          agent: data?.agent,
        },
      };

    case 'task_complete':
      return {
        id: `complete-${taskId}-${timestamp}`,
        text: data?.summary || '任务已完成',
        sender: 'system',
        timestamp,
        type: 'task_complete',
        metadata: {
          taskId,
          taskStatus: data?.status === 'completed' ? 'completed' : 
                      data?.status === 'failed' ? 'failed' : 'in_progress',
        },
      };

    case 'status_update':
      return {
        id: `status-${taskId}-${timestamp}`,
        text: data?.status || '',
        sender: 'system',
        timestamp,
        type: 'status_update',
        metadata: { taskId },
      };

    case 'conversation_error':
      return {
        id: `error-${taskId}-${timestamp}`,
        text: `❌ ${data?.error || serverMsg.message || '发生错误'}`,
        sender: 'system',
        timestamp,
        type: 'error',
        metadata: { taskId, canRetry: true },
      };

    case 'conversation_result':
      return {
        id: `result-${taskId}-${timestamp}`,
        text: data?.result || '',
        sender: 'assistant',
        timestamp,
        type: 'result',
        metadata: { taskId },
      };

    case 'context_loaded':
      return {
        id: `ctx-${taskId}-${timestamp}`,
        text: `📂 已加载 ${data?.file_count || 0} 个文件`,
        sender: 'system',
        timestamp,
        type: 'status_update',
        metadata: { taskId },
      };

    case 'context_compressed':
      return {
        id: `compress-${taskId}-${timestamp}`,
        text: `📦 上下文已压缩 (${data?.compressed_tokens || 0} tokens)`,
        sender: 'system',
        timestamp,
        type: 'status_update',
        metadata: { taskId },
      };

    default:
      // 未知事件类型，降级显示
      if (serverMsg.message) {
        return {
          id: `unknown-${timestamp}`,
          text: serverMsg.message,
          sender: serverMsg.from || 'system',
          timestamp,
          type: 'system',
        };
      }
      return null;
  }
}
