import React, { useRef, useEffect, useState, useMemo } from 'react';
import { WelcomeMessage } from './WelcomeMessage';
import { Message } from '../types';
import { CheckCircle, XCircle, Loader, Orbit, Sparkles, ChevronDown, ChevronRight, ThumbsUp, ThumbsDown, Copy, Check, FileText, Terminal, ArrowUpRight, ArrowDownLeft } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface ChatMessagesProps {
  messages: Message[];
  isProcessing: boolean;
  onFileReview?: (fileName: string) => void;
  onMessageAction?: (messageId: string, actionId: string) => void;
}

// 工具调用状态接口
interface ToolCallState {
  toolCallId: string;
  toolName: string;
  status: 'running' | 'completed' | 'error';
  startTime: number;
  endTime?: number;
  result?: any;
  error?: string;
  arguments?: Record<string, any>;
}

// MessageDemo风格的MessageItem组件
interface MessageItemProps {
  message: Message;
  onFileReview?: (fileName: string) => void;
  onMessageAction?: (messageId: string, actionId: string) => void;
  toolCallStates?: Map<string, ToolCallState>;
  resultToStartIdMap?: Map<string, string>;
  hideSenderIdentity?: boolean;
}

const thinkingCache = new Map<string, string>();

// Thinking 消息子组件
interface ThinkingMessageProps {
  text: string;
  thinkingCount?: number;
  allThinkingText?: string;
}

const ThinkingMessage: React.FC<ThinkingMessageProps> = ({ text, thinkingCount, allThinkingText }) => {
  const [isExpanded, setIsExpanded] = useState(false);
  const hasMultiple = thinkingCount && thinkingCount > 1;
  const displayText = isExpanded ? (allThinkingText || text) : text;

  return (
    <div
      className="flex items-start gap-2 px-3 py-2 rounded-lg bg-[var(--chat-think-bg)] border border-[var(--chat-think-border)] mb-2 cursor-pointer"
      onClick={() => setIsExpanded(!isExpanded)}
    >
      <Sparkles className="w-3 h-3 mt-0.5 shrink-0 text-[var(--chat-text-secondary)]" />
      <div className="flex-1 min-w-0">
        <div className="flex items-center justify-between gap-2 mb-1">
          <span className="text-[11px] font-medium text-[var(--chat-text-secondary)]">
            {isExpanded ? '全部思考过程' : '思考过程'}
          </span>
          <span className="text-[10px] text-[var(--chat-text-secondary)] opacity-60">
            {isExpanded ? '收起' : hasMultiple ? `${thinkingCount} 条` : '展开'}
          </span>
        </div>
        <pre className={`text-[10px] font-mono whitespace-pre-wrap break-all leading-relaxed text-[var(--chat-text-secondary)] ${isExpanded ? 'max-h-48 overflow-y-auto' : 'line-clamp-2'}`}>
          {displayText}
        </pre>
      </div>
    </div>
  );
};

const formatTokenCount = (n: number | undefined): string => {
  if (n === undefined || n === null) return '';
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1000).toFixed(1) + 'k';
  return Math.round(n / 1000) + 'k';
};

const MessageItem: React.FC<MessageItemProps> = ({ message, onFileReview, onMessageAction, toolCallStates, resultToStartIdMap, hideSenderIdentity }) => {
  const [copiedText, setCopiedText] = useState(false);
  const [isUserContextExpanded, setIsUserContextExpanded] = useState(false);

  // 获取要显示的文本内容
  const getMessageText = () => {
    if (message.type === 'memory_change') return "Memory updated";
    if (message.type === 'result') return message.text || "任务已完成";
    if (message.type === 'thinking') {
      const cached = thinkingCache.get(message.id);
      if (cached) return cached;
      return message.text || "思考中...";
    }
    if (message.text) return message.text;
    if (message.data !== undefined) {
      return typeof message.data === 'string' ? message.data : JSON.stringify(message.data);
    }
    return '';
  };

  const textContent = getMessageText();
  const isThinkingType = message.type === 'thinking';
  if (isThinkingType && message.text && !thinkingCache.has(message.id)) {
    thinkingCache.set(message.id, message.text);
  }

  const parseUserContextPayload = (rawText: string) => {
    const contextHeader = '## Context';
    const requestHeader = '## User Request';
    if (!rawText.includes(contextHeader) || !rawText.includes(requestHeader)) {
      return null;
    }

    const contextStart = rawText.indexOf(contextHeader);
    const requestStart = rawText.indexOf(requestHeader);
    if (contextStart === -1 || requestStart === -1 || requestStart <= contextStart) {
      return null;
    }

    const contextSection = rawText
      .slice(contextStart + contextHeader.length, requestStart)
      .trim();
    const userRequest = rawText
      .slice(requestStart + requestHeader.length)
      .trim();

    if (!contextSection) return null;

    const contextCount = (contextSection.match(/### Context:/g) || []).length || 1;
    return { contextSection, userRequest, contextCount };
  };

  const isUserMessage = message.sender === 'user';
  const userContextPayload = isUserMessage ? parseUserContextPayload(textContent) : null;
  const userDisplayText = userContextPayload ? (userContextPayload.userRequest || textContent) : textContent;

  const [expandedToolCalls, setExpandedToolCalls] = useState<Set<string>>(new Set());
  const toggleToolCallExpansion = (toolCallId: string) => {
    setExpandedToolCalls(prev => {
      const newSet = new Set(prev);
      if (newSet.has(toolCallId)) { newSet.delete(toolCallId); } else { newSet.add(toolCallId); }
      return newSet;
    });
  };

  const toolResultPreRef = useRef<HTMLPreElement>(null);
  const [toolResultOverflows, setToolResultOverflows] = useState(false);

  useEffect(() => {
    if (toolResultPreRef.current) {
      const el = toolResultPreRef.current;
      setToolResultOverflows(el.scrollHeight > el.clientHeight);
    }
  }, [message.id, message.type, message.text, toolCallStates]);

  const formatTime = (timestamp: number) => new Date(timestamp).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

  const renderTaskStatus = () => {
    if (message.type !== 'task_complete' || !message.metadata?.taskStatus) return null;
    const status = message.metadata.taskStatus;
    const statusConfig = {
      'in_progress': { icon: Loader, color: 'text-blue-400', bgColor: 'bg-blue-500/10', label: '进行中' },
      'completed': { icon: CheckCircle, color: 'text-green-400', bgColor: 'bg-green-500/10', label: '已完成' },
      'failed': { icon: XCircle, color: 'text-red-400', bgColor: 'bg-red-500/10', label: '失败' }
    };
    const config = statusConfig[status];
    const IconComponent = config.icon;
    return (
      <div className={`inline-flex items-center gap-1 px-2 py-1 rounded text-xs ${config.bgColor} ${config.color}`}>
        <IconComponent className="w-3 h-3" />
        <span>{config.label}</span>
      </div>
    );
  };

  // 渲染工具调用内容 - 紧凑卡片风格
  const renderToolCallContent = () => {
    if (message.type !== 'tool_call_start' && message.type !== 'tool_call_result' && message.type !== 'tool_call_error') return null;
    let toolCallId = message.metadata?.toolCallId;
    let toolName = message.metadata?.toolName;
    if (!toolCallId) {
      if (message.type === 'tool_call_start') { toolCallId = message.id; toolName = (message.metadata as any)?.toolName; }
      else if (message.type === 'tool_call_result' && resultToStartIdMap) { toolCallId = resultToStartIdMap.get(message.id); toolName = (message.metadata as any)?.toolName; }
    }
    toolName = toolName || 'Unknown Tool';

    // tool_call_error 独立渲染，不依赖 toolCallStates
    if (message.type === 'tool_call_error') {
      const errorText = message.metadata?.error || 'Tool execution failed';
      const errorFilePath = message.metadata?.input?.file_path || message.metadata?.arguments?.file_path;
      const errorCommand = message.metadata?.input?.command || message.metadata?.arguments?.command;
      return (
        <div className="rounded-lg border border-red-500/30 bg-red-500/5 overflow-hidden">
          <div className="flex items-center gap-2 px-3 py-2">
            <XCircle className="w-3 h-3 text-red-500 shrink-0" />
            <span className="text-xs font-mono font-medium text-red-400 shrink-0">{toolName}</span>
            {errorFilePath && (
              <span className="inline-flex items-center gap-1 min-w-0 text-red-400/70">
                <span className="text-[10px] opacity-30 shrink-0">|</span>
                <FileText className="w-3 h-3 opacity-50 shrink-0" />
                <span className="text-[10px] font-mono opacity-60 truncate">{errorFilePath}</span>
              </span>
            )}
            {errorCommand && (
              <span className="inline-flex items-center gap-1 min-w-0 text-red-400/70">
                <span className="text-[10px] opacity-30 shrink-0">|</span>
                <Terminal className="w-3 h-3 opacity-50 shrink-0" />
                <span className="text-[10px] font-mono opacity-60 truncate">{errorCommand}</span>
              </span>
            )}
            <span className="ml-auto text-[10px] text-red-400/70 shrink-0">失败</span>
          </div>
          <div className="border-t border-red-500/20 p-3 text-[11px] text-red-400 whitespace-pre-wrap break-all">{errorText}</div>
        </div>
      );
    }

    if (!toolCallId || !toolCallStates) return null;
    const finalToolCallId = toolCallId;
    const toolCallState = toolCallStates.get(finalToolCallId);
    if (!toolCallState) return null;
    const isExpanded = expandedToolCalls.has(finalToolCallId);
    const getResultInfo = () => {
      if (!toolCallState.result) return null;
      let content = '';
      if (typeof toolCallState.result === 'string') { content = toolCallState.result; }
      else if (toolCallState.result.content && toolCallState.result.lines) { content = toolCallState.result.content; }
      else { content = JSON.stringify(toolCallState.result, null, 2); }
      return { content };
    };
    const resultInfo = getResultInfo();

    const statusIcon = toolCallState.status === 'running' ? <Loader className="w-3 h-3 animate-spin text-[var(--chat-accent)]" /> :
      toolCallState.status === 'completed' ? <CheckCircle className="w-3 h-3 text-green-500" /> :
      <XCircle className="w-3 h-3 text-red-500" />;
    const elapsed = toolCallState.endTime ? `${((toolCallState.endTime - toolCallState.startTime) / 1000).toFixed(1)}s` : '';
    const filePath = toolCallState.arguments?.file_path;
    const command = toolCallState.arguments?.command;
    return (
      <div className="rounded-lg border border-[var(--chat-tool-border)] bg-[var(--chat-tool-bg)] overflow-hidden">
        <div className="flex items-center gap-2 px-3 py-2 min-w-0">
          {statusIcon}
          <span className="text-xs font-mono font-medium text-[var(--chat-text-primary)] shrink-0">{toolName}</span>
          {elapsed && <span className="text-[10px] text-[var(--chat-text-secondary)] opacity-60 shrink-0">{elapsed}</span>}
          {filePath && (
            <span className="inline-flex items-center gap-1 min-w-0 text-[var(--chat-text-secondary)]">
              <span className="text-[10px] opacity-30 shrink-0">|</span>
              <FileText className="w-3 h-3 opacity-50 shrink-0" />
              <span className="text-[10px] font-mono opacity-60 truncate">{filePath}</span>
            </span>
          )}
          {command && (
            <span className="inline-flex items-center gap-1 min-w-0 text-[var(--chat-text-secondary)]">
              <span className="text-[10px] opacity-30 shrink-0">|</span>
              <Terminal className="w-3 h-3 opacity-50 shrink-0" />
              <span className="text-[10px] font-mono opacity-60 truncate">{command}</span>
            </span>
          )}
          {toolCallState.status === 'completed' && resultInfo && (
            <button onClick={() => toggleToolCallExpansion(finalToolCallId)} className="ml-auto flex items-center gap-1 text-[10px] text-[var(--chat-text-secondary)] hover:text-[var(--chat-accent)] transition-colors shrink-0">
              {isExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
            </button>
          )}
          {toolCallState.status === 'error' && toolCallState.error && toolResultOverflows && (
            <button onClick={() => toggleToolCallExpansion(finalToolCallId)} className="ml-auto flex items-center gap-1 text-[10px] text-red-400/70 hover:text-red-400 transition-colors shrink-0">
              {isExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
            </button>
          )}
        </div>
        {isExpanded && resultInfo && (
          <div className="border-t border-[var(--chat-tool-border)] p-3">
            <pre className="text-[10px] font-mono text-[var(--chat-text-secondary)] whitespace-pre-wrap break-all max-h-40 overflow-y-auto">{resultInfo.content}</pre>
          </div>
        )}
        {toolCallState.status === 'error' && toolCallState.error && (
          <div className="border-t border-red-500/20 p-3">
            <pre ref={toolResultPreRef} className={`text-[11px] text-red-400 whitespace-pre-wrap break-all ${isExpanded ? 'max-h-40 overflow-y-auto' : 'line-clamp-2'}`}>{toolCallState.error}</pre>
          </div>
        )}
      </div>
    );
  };

  // 如果是工具调用消息且已经被合并处理，则不显示
  let effectiveToolCallId = message.metadata?.toolCallId;
  if (!effectiveToolCallId) {
    if (message.type === 'tool_call_start') { effectiveToolCallId = message.id; }
    else if (message.type === 'tool_call_result' && resultToStartIdMap) { effectiveToolCallId = resultToStartIdMap.get(message.id); }
  }
  if ((message.type === 'tool_call_start' || message.type === 'tool_call_result') && effectiveToolCallId && toolCallStates?.has(effectiveToolCallId)) {
    const toolCallState = toolCallStates.get(effectiveToolCallId);
    if (message.type === 'tool_call_result' || (message.type === 'tool_call_start' && toolCallState && toolCallState.startTime !== message.timestamp)) return null;
  }

  // tool_call_error 不应作为普通文本渲染（已在 renderToolCallContent 中处理）
  if (message.type === 'tool_call_error') {
    const rendered = renderToolCallContent();
    if (rendered) return null;
  }

  const handleCopy = () => {
    navigator.clipboard.writeText(textContent).then(() => { setCopiedText(true); setTimeout(() => setCopiedText(false), 1500); });
  };

  // ===== USER MESSAGE: right-aligned pink bubble =====
  if (isUserMessage) {
    return (
      <div className="flex justify-end mb-3 animate-fadeIn">
        <div className="max-w-[78%]">
          <div className="bg-[var(--chat-user-bg)] text-[var(--chat-user-text)] rounded-2xl rounded-tr-sm px-4 py-3 text-sm leading-relaxed shadow-sm">
            {userDisplayText}

            {userContextPayload && (
              <div className="mt-3 border-t border-[var(--chat-user-text)]/20 pt-2">
                <button
                  type="button"
                  onClick={() => setIsUserContextExpanded(v => !v)}
                  className="inline-flex items-center gap-1.5 text-[11px] font-medium px-2 py-1 rounded-full bg-black/10 hover:bg-black/15 transition-colors"
                >
                  {isUserContextExpanded ? <ChevronDown className="w-3 h-3" /> : <ChevronRight className="w-3 h-3" />}
                  <span>Context ({userContextPayload.contextCount})</span>
                </button>

                {isUserContextExpanded && (
                  <div className="mt-2 rounded-lg bg-black/10 p-2.5">
                    <ReactMarkdown
                      remarkPlugins={[remarkGfm]}
                      className="text-xs prose-chat max-w-none"
                      components={{
                        p: ({ children }) => <p className="mb-1.5 last:mb-0 text-[var(--chat-user-text)]/90 text-xs leading-relaxed">{children}</p>,
                        strong: ({ children }) => <strong className="font-semibold text-[var(--chat-user-text)]">{children}</strong>,
                        code: ({ children }) => <code className="bg-black/20 border border-[var(--chat-user-text)]/20 px-1 py-0.5 rounded text-[10px] font-mono text-[var(--chat-user-text)]">{children}</code>,
                        pre: ({ children }) => <pre className="bg-black/20 border border-[var(--chat-user-text)]/20 p-2 rounded-md overflow-x-auto my-1.5 text-[10px] font-mono">{children}</pre>,
                        h3: ({ children }) => <h3 className="text-[11px] font-semibold mt-2 mb-1 text-[var(--chat-user-text)]">{children}</h3>,
                        ul: ({ children }) => <ul className="list-disc pl-4 space-y-1 my-1.5">{children}</ul>,
                        li: ({ children }) => <li className="text-xs text-[var(--chat-user-text)]/90">{children}</li>,
                      }}
                    >
                      {userContextPayload.contextSection}
                    </ReactMarkdown>
                  </div>
                )}
              </div>
            )}
          </div>
          <div className="text-right mt-1 pr-1">
            <span className="text-[10px] text-[var(--chat-text-secondary)]">{formatTime(message.timestamp)}</span>
          </div>
        </div>
      </div>
    );
  }

  // ===== ASSISTANT / TOOL / SYSTEM MESSAGES: left-aligned =====
  const isToolCall = message.type === 'tool_call_start' || message.type === 'tool_call_result' || message.type === 'tool_call_error';
  const isSystem = message.type === 'status_update' || message.type === 'memory_change';

  return (
    <div className="mb-3 animate-fadeIn">
      {/* Thinking */}
      {isThinkingType && (
        <ThinkingMessage text={message.text || ''} thinkingCount={message.metadata?.thinkingCount} allThinkingText={message.metadata?.allThinkingText} />
      )}

      {/* Tool call card */}
      {isToolCall && (
        <div className="mb-1">{renderToolCallContent()}</div>
      )}

      {/* System/status pill */}
      {isSystem && (
        <div className="flex items-center gap-1.5 px-3 py-1.5 rounded-full bg-[var(--chat-sys-bg)] border border-[var(--chat-sys-border)] w-fit mb-1">
          <Orbit className="w-3 h-3 text-[var(--chat-accent)]" />
          <span className="text-[11px] text-[var(--chat-text-secondary)]">{textContent}</span>
        </div>
      )}

      {/* Regular assistant text */}
      {!isThinkingType && !isToolCall && !isSystem && (
        <>
          <div className="text-[var(--chat-text-primary)] leading-relaxed">
            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              className="text-sm prose-chat max-w-none"
              components={{
                p: ({ children }) => <p className="mb-2 last:mb-0 text-[var(--chat-text-primary)] text-sm leading-relaxed">{children}</p>,
                ol: ({ children }) => <ol className="space-y-2 my-2 list-none pl-0">{children}</ol>,
                li: ({ children, ...props }) => {
                  // numbered ordered list item with circle badge
                  const index = (props as any).index;
                  return (
                    <li className="flex items-start gap-3 py-1">
                      {typeof index === 'number' ? (
                        <span className="chat-step-badge shrink-0 mt-0.5">{index + 1}</span>
                      ) : (
                        <span className="w-1.5 h-1.5 rounded-full bg-[var(--chat-accent)] shrink-0 mt-2" />
                      )}
                      <span className="text-sm text-[var(--chat-text-primary)] leading-relaxed flex-1">{children}</span>
                    </li>
                  );
                },
                strong: ({ children }) => <strong className="font-semibold text-[var(--chat-text-primary)]">{children}</strong>,
                code: ({ children }) => <code className="bg-[var(--chat-code-bg)] border border-[var(--chat-tool-border)] px-1.5 py-0.5 rounded text-[11px] font-mono text-[var(--chat-accent)]">{children}</code>,
                pre: ({ children }) => <pre className="bg-[var(--chat-code-bg)] border border-[var(--chat-tool-border)] p-3 rounded-lg overflow-x-auto my-2 text-xs font-mono">{children}</pre>,
                blockquote: ({ children }) => <blockquote className="border-l-2 border-[var(--chat-accent)] pl-3 italic text-[var(--chat-text-secondary)] my-2">{children}</blockquote>,
              }}
            >
              {textContent || ''}
            </ReactMarkdown>
          </div>

          {/* Token usage badge */}
          {message.metadata?.usage && (message.metadata.usage.input_tokens > 0 || message.metadata.usage.output_tokens > 0) && (
            <div className="mt-2 flex items-center justify-end gap-2 text-xs text-[var(--chat-text-secondary)] opacity-60">
              <span className="inline-flex items-center gap-0.5">
                <ArrowUpRight className="w-3 h-3 text-blue-400/70" />
                <span className="font-mono">{formatTokenCount(message.metadata.usage.input_tokens)}</span>
              </span>
              <span className="opacity-40">in</span>
              <span className="inline-flex items-center gap-0.5">
                <ArrowDownLeft className="w-3 h-3 text-purple-400/70" />
                <span className="font-mono">{formatTokenCount(message.metadata.usage.output_tokens)}</span>
              </span>
              <span className="opacity-40">out</span>
            </div>
          )}

          {/* Reaction buttons */}
          <div className="flex items-center gap-1 mt-2 opacity-0 hover:opacity-100 group-hover:opacity-100 transition-opacity">
            <button onClick={() => onMessageAction?.(message.id, 'thumbs_up')} className="p-1.5 rounded-full hover:bg-[var(--chat-tool-bg)] text-[var(--chat-text-secondary)] hover:text-[var(--chat-accent)] transition-colors">
              <ThumbsUp className="w-3.5 h-3.5" />
            </button>
            <button onClick={() => onMessageAction?.(message.id, 'thumbs_down')} className="p-1.5 rounded-full hover:bg-[var(--chat-tool-bg)] text-[var(--chat-text-secondary)] hover:text-[var(--chat-accent)] transition-colors">
              <ThumbsDown className="w-3.5 h-3.5" />
            </button>
            <button onClick={handleCopy} className="p-1.5 rounded-full hover:bg-[var(--chat-tool-bg)] text-[var(--chat-text-secondary)] hover:text-[var(--chat-accent)] transition-colors">
              {copiedText ? <Check className="w-3.5 h-3.5 text-green-500" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
          </div>
        </>
      )}

      {/* Task status badge */}
      {renderTaskStatus()}
    </div>
  );
};

// Helper: merge consecutive thinking messages in an array
const mergeThinkingMessages = (messages: Message[]): Message[] => {
  const result: Message[] = [];
  let thinkingBuffer: Message[] = [];
  const flush = () => {
    if (thinkingBuffer.length === 0) return;
    if (thinkingBuffer.length === 1) { result.push(thinkingBuffer[0]); }
    else {
      const last = thinkingBuffer[thinkingBuffer.length - 1];
      result.push({ ...last, id: `thinking-merged-${last.id}`, text: last.text, metadata: { ...last.metadata, thinkingCount: thinkingBuffer.length, allThinkingText: thinkingBuffer.map(m => m.text).filter(Boolean).join('\n\n---\n\n') } });
    }
    thinkingBuffer = [];
  };
  messages.forEach(msg => { if (msg.type === 'thinking') { thinkingBuffer.push(msg); } else { flush(); result.push(msg); } });
  flush();
  return result;
};

export const ChatMessages: React.FC<ChatMessagesProps> = ({ messages, isProcessing, onFileReview, onMessageAction }) => {
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const messagesContainerRef = useRef<HTMLDivElement>(null);
  const [toolCallStates, setToolCallStates] = useState<Map<string, ToolCallState>>(new Map());
  const [resultToStartIdMap, setResultToStartIdMap] = useState<Map<string, string>>(new Map());

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  // 处理工具调用状态
  useEffect(() => {
    const newToolCallStates = new Map<string, ToolCallState>();
    const newResultToStartIdMap = new Map<string, string>();
    const pendingTools = new Map<string, string>();
    messages.forEach(message => {
      if (message.type === 'tool_call_start' && message.metadata?.toolCallId) {
        const { toolCallId, toolName } = message.metadata;
        const args = message.metadata.input || message.metadata.arguments || {};
        newToolCallStates.set(toolCallId, { toolCallId, toolName: toolName || 'Unknown Tool', status: 'running', startTime: message.timestamp, arguments: args });
      }
      if (message.type === 'tool_call_result' && message.metadata?.toolCallId) {
        const existing = newToolCallStates.get(message.metadata.toolCallId);
        if (existing) newToolCallStates.set(message.metadata.toolCallId, { ...existing, status: 'completed', endTime: message.timestamp, result: message.metadata.result });
      }
      if (message.type === 'tool_call_error' && message.metadata?.toolCallId) {
        const existing = newToolCallStates.get(message.metadata.toolCallId);
        if (existing) newToolCallStates.set(message.metadata.toolCallId, { ...existing, status: 'error', endTime: message.timestamp, error: message.metadata.error });
      }
      if (!message.metadata?.toolCallId) {
        if (message.type === 'tool_call_start') {
          const toolName = (message.data as any)?.tool_name || 'Unknown Tool';
          const args = (message.data as any)?.input || (message.data as any)?.arguments || {};
          newToolCallStates.set(message.id, { toolCallId: message.id, toolName, status: 'running', startTime: message.timestamp, arguments: args });
          pendingTools.set(toolName, message.id);
        }
        if (message.type === 'tool_call_result') {
          const toolName = (message.data as any)?.tool_name;
          const toolCallId = toolName ? pendingTools.get(toolName) : undefined;
          if (toolCallId) {
            const existing = newToolCallStates.get(toolCallId);
            if (existing) { newToolCallStates.set(toolCallId, { ...existing, status: 'completed', endTime: message.timestamp, result: (message.data as any)?.result }); newResultToStartIdMap.set(message.id, toolCallId); pendingTools.delete(toolName); }
          }
        }
      }
    });
    setToolCallStates(newToolCallStates);
    setResultToStartIdMap(newResultToStartIdMap);
  }, [messages]);

  const visibleMessages = useMemo(() =>
    mergeThinkingMessages(messages.filter(m => m.type !== 'finish' && m.type !== 'taskSubmitted')),
    [messages]
  );

  if (messages.length === 0) return <WelcomeMessage />;

  return (
    <div ref={messagesContainerRef} className="flex-1 overflow-y-auto px-4 py-6 scrollbar-thin chat-messages-container">
      <div className="max-w-2xl mx-auto">
        {visibleMessages.map((message) => (
          <div key={message.id} className="group">
            <MessageItem
              message={message}
              onFileReview={onFileReview}
              onMessageAction={onMessageAction}
              toolCallStates={toolCallStates}
              resultToStartIdMap={resultToStartIdMap}
            />
          </div>
        ))}

        {isProcessing && (
          <div className="flex items-center gap-2 mb-3 animate-fadeIn">
            <div className="flex gap-1 px-4 py-3">
              <span className="w-2 h-2 rounded-full bg-[var(--chat-accent)] animate-bounce" style={{ animationDelay: '0ms' }} />
              <span className="w-2 h-2 rounded-full bg-[var(--chat-accent)] animate-bounce" style={{ animationDelay: '150ms' }} />
              <span className="w-2 h-2 rounded-full bg-[var(--chat-accent)] animate-bounce" style={{ animationDelay: '300ms' }} />
            </div>
          </div>
        )}
      </div>
      <div ref={messagesEndRef} />
    </div>
  );
};