/**
 * CodeActor Agent 事件协议 — VSCode 插件端类型定义
 * 
 * 这些类型从 protocol/ts/agent-events.ts 生成，用于 VSCode 插件
 * 对 WebSocket 消息的类型安全处理。
 * 
 * 插件不直接连接 WebSocket，但可以通过这些类型理解协议，
 * 并在调试面板中显示结构化的事件信息。
 */

// ===== 事件类型枚举 =====
export const EventTypes = {
  ModelInfo: "model_info",
  LlmCallStart: "llm_call_start",
  LlmCallEnd: "llm_call_end",
  AiResponse: "ai_response",
  AiStreamStart: "ai_stream_start",
  AiChunk: "ai_chunk",
  AiStreamEnd: "ai_stream_end",
  ToolCallStart: "tool_call_start",
  ToolCallResult: "tool_call_result",
  ToolCallError: "tool_call_error",
  ContextLoaded: "context_loaded",
  ContextCompressed: "context_compressed",
  CommitContextLoaded: "commit_context_loaded",
  UserHelpNeeded: "user_help_needed",
  UserHelpResponse: "user_help_response",
  ConversationError: "conversation_error",
  ConversationResult: "conversation_result",
  StatusUpdate: "status_update",
  Thinking: "thinking",
  TaskComplete: "task_complete",
} as const;

export type EventType = typeof EventTypes[keyof typeof EventTypes];

/** WebSocket 消息信封（与后端 AgentEventEnvelope 对应） */
export interface AgentEventEnvelope {
  type: string;
  event: EventType;
  data?: Record<string, unknown>;
  from?: string;
  task_id?: string;
  message?: string;
}

/** 已知事件类型列表 */
export const ALL_EVENT_TYPES: EventType[] = Object.values(EventTypes);

/**
 * 获取事件的中文描述
 */
export function getEventDescription(eventType: EventType): string {
  const descriptions: Record<EventType, string> = {
    [EventTypes.ModelInfo]: "LLM 模型信息",
    [EventTypes.LlmCallStart]: "LLM 调用开始",
    [EventTypes.LlmCallEnd]: "LLM 调用结束",
    [EventTypes.AiResponse]: "AI 响应内容",
    [EventTypes.AiStreamStart]: "AI 流式响应开始",
    [EventTypes.AiChunk]: "AI 流式数据块",
    [EventTypes.AiStreamEnd]: "AI 流式响应结束",
    [EventTypes.ToolCallStart]: "工具调用开始",
    [EventTypes.ToolCallResult]: "工具调用结果",
    [EventTypes.ToolCallError]: "工具调用错误",
    [EventTypes.ContextLoaded]: "上下文加载完成",
    [EventTypes.ContextCompressed]: "上下文压缩完成",
    [EventTypes.CommitContextLoaded]: "Commit 上下文加载",
    [EventTypes.UserHelpNeeded]: "需要用户帮助",
    [EventTypes.UserHelpResponse]: "用户回复",
    [EventTypes.ConversationError]: "对话错误",
    [EventTypes.ConversationResult]: "对话结果",
    [EventTypes.StatusUpdate]: "状态更新",
    [EventTypes.Thinking]: "思考过程",
    [EventTypes.TaskComplete]: "任务完成",
  };
  return descriptions[eventType] || eventType;
}

/**
 * 检查事件类型是否有效
 */
export function isValidEventType(type: string): type is EventType {
  return ALL_EVENT_TYPES.includes(type as EventType);
}
