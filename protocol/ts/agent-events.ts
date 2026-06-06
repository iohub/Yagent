// =============================================================================
// CodeActor Agent Events - TypeScript 类型定义
// 版本: 1.0.0
// 此文件由 codegen 自动生成，请勿手动修改
// =============================================================================

// ===== 事件类型枚举 =====
export const EventTypes = {
  ModelInfo: "model_info",
  LlmCallStart: "llm_call_start",
  LlmCallEnd: "llm_call_end",
  AiResponse: "ai_response",
  ToolCallStart: "tool_call_start",
  ToolCallResult: "tool_call_result",
  ToolCallError: "tool_call_error",
  ContextLoaded: "context_loaded",
  ContextCompressed: "context_compressed",
  CommitContextLoaded: "commit_context_loaded",
  AiStreamStart: "ai_stream_start",
  AiChunk: "ai_chunk",
  AiStreamEnd: "ai_stream_end",
  UserHelpNeeded: "user_help_needed",
  UserHelpResponse: "user_help_response",
  ConversationError: "conversation_error",
  ConversationResult: "conversation_result",
  StatusUpdate: "status_update",
  Thinking: "thinking",
  TaskComplete: "task_complete",
} as const;

export type EventType = typeof EventTypes[keyof typeof EventTypes];

// ===== 事件类型接口 =====
export interface ModelInfoEvent {
  /** LLM 模型信息 */
  event: "model_info";
  model: string;
  agent: string;
  provider?: string;
}

export interface LlmCallStartEvent {
  /** LLM 调用开始 */
  event: "llm_call_start";
  model: string;
  agent: string;
}

export interface LlmCallEndEvent {
  /** LLM 调用结束 */
  event: "llm_call_end";
  model: string;
  agent: string;
  duration_seconds?: number;
  error?: string;
}

export interface AiResponseEvent {
  /** AI 响应内容（流式或一次性） */
  event: "ai_response";
  content: string;
  usage?: Record<string, any>;
}

export interface ToolCallStartEvent {
  /** 工具调用开始 */
  event: "tool_call_start";
  tool_name: string;
  arguments: string;
  tool_call_id: string;
}

export interface ToolCallResultEvent {
  /** 工具调用结果 */
  event: "tool_call_result";
  tool_name: string;
  result: string;
  tool_call_id: string;
}

export interface ToolCallErrorEvent {
  /** 工具调用错误 */
  event: "tool_call_error";
  tool_name: string;
  error: string;
  tool_call_id: string;
  arguments?: string;
}

export interface ContextLoadedEvent {
  /** 项目上下文加载完成 */
  event: "context_loaded";
  file_count: number;
  files?: any[];
}

export interface ContextCompressedEvent {
  /** 上下文压缩完成 */
  event: "context_compressed";
  original_tokens: number;
  compressed_tokens: number;
  ratio?: number;
}

export interface CommitContextLoadedEvent {
  /** Commit 学习器上下文加载 */
  event: "commit_context_loaded";
  commit_count: number;
}

export interface AiStreamStartEvent {
  /** AI 流式响应开始 */
  event: "ai_stream_start";
  agent: string;
}

export interface AiChunkEvent {
  /** AI 流式响应数据块 */
  event: "ai_chunk";
  content: string;
  agent?: string;
}

export interface AiStreamEndEvent {
  /** AI 流式响应结束 */
  event: "ai_stream_end";
  agent: string;
  usage?: Record<string, any>;
}

export interface UserHelpNeededEvent {
  /** 需要用户帮助/确认 */
  event: "user_help_needed";
  question: string;
  context?: string;
}

export interface UserHelpResponseEvent {
  /** 用户回复了帮助请求 */
  event: "user_help_response";
  response: string;
  approved?: boolean;
}

export interface ConversationErrorEvent {
  /** 对话处理错误 */
  event: "conversation_error";
  task_id: string;
  error: string;
}

export interface ConversationResultEvent {
  /** 对话任务完成 */
  event: "conversation_result";
  task_id: string;
  result: string;
}

export interface StatusUpdateEvent {
  /** 通用状态更新 */
  event: "status_update";
  status: string;
  progress?: number;
}

export interface ThinkingEvent {
  /** Agent 思考过程（推理过程） */
  event: "thinking";
  content: string;
  agent?: string;
}

export interface TaskCompleteEvent {
  /** 任务完成通知 */
  event: "task_complete";
  task_id: string;
  status: string;
  summary?: string;
}

// ===== 事件联合类型 =====
export type AgentEvent =
  | ModelInfoEvent
  | LlmCallStartEvent
  | LlmCallEndEvent
  | AiResponseEvent
  | ToolCallStartEvent
  | ToolCallResultEvent
  | ToolCallErrorEvent
  | ContextLoadedEvent
  | ContextCompressedEvent
  | CommitContextLoadedEvent
  | AiStreamStartEvent
  | AiChunkEvent
  | AiStreamEndEvent
  | UserHelpNeededEvent
  | UserHelpResponseEvent
  | ConversationErrorEvent
  | ConversationResultEvent
  | StatusUpdateEvent
  | ThinkingEvent
  | TaskCompleteEvent;

// ===== WebSocket 消息包装 =====
export interface WebSocketMessage {
  /** 消息类型标识（如 'realtime', 'connection', 'error'） */
  type: string;
  /** 事件名称（对应 event_types 中的 name） */
  event: string;
  /** 事件数据（具体结构由 event 决定） */
  data: Record<string, any>;
  /** 消息来源（Agent 名称或 'System'） */
  from?: string;
  /** 关联的任务 ID */
  task_id?: string;
  /** 简短文本消息（用于错误通知等） */
  message?: string;
}

// ===== 类型守卫 =====
export function isModelInfo(msg: AgentEvent | WebSocketMessage): msg is ModelInfoEvent {
  return 'event' in msg && msg.event === "model_info";
}

export function isLlmCallStart(msg: AgentEvent | WebSocketMessage): msg is LlmCallStartEvent {
  return 'event' in msg && msg.event === "llm_call_start";
}

export function isLlmCallEnd(msg: AgentEvent | WebSocketMessage): msg is LlmCallEndEvent {
  return 'event' in msg && msg.event === "llm_call_end";
}

export function isAiResponse(msg: AgentEvent | WebSocketMessage): msg is AiResponseEvent {
  return 'event' in msg && msg.event === "ai_response";
}

export function isToolCallStart(msg: AgentEvent | WebSocketMessage): msg is ToolCallStartEvent {
  return 'event' in msg && msg.event === "tool_call_start";
}

export function isToolCallResult(msg: AgentEvent | WebSocketMessage): msg is ToolCallResultEvent {
  return 'event' in msg && msg.event === "tool_call_result";
}

export function isToolCallError(msg: AgentEvent | WebSocketMessage): msg is ToolCallErrorEvent {
  return 'event' in msg && msg.event === "tool_call_error";
}

export function isContextLoaded(msg: AgentEvent | WebSocketMessage): msg is ContextLoadedEvent {
  return 'event' in msg && msg.event === "context_loaded";
}

export function isContextCompressed(msg: AgentEvent | WebSocketMessage): msg is ContextCompressedEvent {
  return 'event' in msg && msg.event === "context_compressed";
}

export function isCommitContextLoaded(msg: AgentEvent | WebSocketMessage): msg is CommitContextLoadedEvent {
  return 'event' in msg && msg.event === "commit_context_loaded";
}

export function isAiStreamStart(msg: AgentEvent | WebSocketMessage): msg is AiStreamStartEvent {
  return 'event' in msg && msg.event === "ai_stream_start";
}

export function isAiChunk(msg: AgentEvent | WebSocketMessage): msg is AiChunkEvent {
  return 'event' in msg && msg.event === "ai_chunk";
}

export function isAiStreamEnd(msg: AgentEvent | WebSocketMessage): msg is AiStreamEndEvent {
  return 'event' in msg && msg.event === "ai_stream_end";
}

export function isUserHelpNeeded(msg: AgentEvent | WebSocketMessage): msg is UserHelpNeededEvent {
  return 'event' in msg && msg.event === "user_help_needed";
}

export function isUserHelpResponse(msg: AgentEvent | WebSocketMessage): msg is UserHelpResponseEvent {
  return 'event' in msg && msg.event === "user_help_response";
}

export function isConversationError(msg: AgentEvent | WebSocketMessage): msg is ConversationErrorEvent {
  return 'event' in msg && msg.event === "conversation_error";
}

export function isConversationResult(msg: AgentEvent | WebSocketMessage): msg is ConversationResultEvent {
  return 'event' in msg && msg.event === "conversation_result";
}

export function isStatusUpdate(msg: AgentEvent | WebSocketMessage): msg is StatusUpdateEvent {
  return 'event' in msg && msg.event === "status_update";
}

export function isThinking(msg: AgentEvent | WebSocketMessage): msg is ThinkingEvent {
  return 'event' in msg && msg.event === "thinking";
}

export function isTaskComplete(msg: AgentEvent | WebSocketMessage): msg is TaskCompleteEvent {
  return 'event' in msg && msg.event === "task_complete";
}

