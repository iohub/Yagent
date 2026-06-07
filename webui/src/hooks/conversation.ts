import { useEffect, useRef, useState } from 'react';
import { Message, ExtensionMessage, ChatState } from '../types';
import { vscodeAPI } from '../utils/vscode';
import { mockMessageLoader } from '../utils/mockData';

export const useChat = () => {
  const [chatState, setChatState] = useState<ChatState>({
    messages: [],
    isProcessing: false,
    currentMode: 'agent', // Default to agent mode
    theme: 'dark',
  });

  // 添加任务状态管理
  const [taskState, setTaskState] = useState<{
    isTaskRunning: boolean;
    currentTask: string;
    taskProgress: number;
    currentStep: string;
    taskStatus: 'running' | 'completed' | 'failed' | 'terminated' | 'error';
    taskId?: string;
  }>({
    isTaskRunning: false,
    currentTask: '',
    taskProgress: 0,
    currentStep: '',
    taskStatus: 'completed', // 修改初始状态为completed，避免显示错误状态
    taskId: undefined
  });
  const taskIdRef = useRef<string | undefined>(undefined);

  // 添加调试日志
  useEffect(() => {
    console.log('🔄 useChat - taskState changed:', taskState);
  }, [taskState]);
  useEffect(() => {
    taskIdRef.current = taskState.taskId;
  }, [taskState.taskId]);

  // 消息去重跟踪：存储已显示的 result 对应的 assistant 消息ID
  const displayedResultIds = useRef<Set<string>>(new Set());
  // 跳过显示的 assistant 消息ID（因为 result 会包含相同内容）
  const skipMessageIds = useRef<Set<string>>(new Set());
  // 待显示的文本内容（用于 result 场景）
  const pendingTextContent = useRef<string | null>(null);
  // 当前正在等待 result 的 assistant 消息ID
  const pendingAssistantId = useRef<string | null>(null);
  // 待使用的 token usage 数据（来自 assistant 消息）
  const pendingUsage = useRef<any>(null);
  // 消息内容指纹去重缓存（用于短时间内内容重复的消息过滤）
  const recentFingerprints = useRef<{ fingerprint: string; time: number }[]>([]);
  // 记录最后一次 result 处理时间，用于防重复保护
  const lastResultTime = useRef<number | null>(null);

  const normalizeAIResponseContent = (content: string): string => {
    if (!content) return '';

    if (content.startsWith('"') && content.endsWith('"')) {
      try {
        return JSON.parse(content);
      } catch (e) {
      }
    }

    if (!content.includes('\n') && /\\n|\\r|\\t/.test(content)) {
      return content
        .replace(/\\n/g, '\n')
        .replace(/\\r/g, '\r')
        .replace(/\\t/g, '\t');
    }

    return content;
  };

  // 添加消息到聊天状态（带去重检查）
  const addMessageFromExtension = (message: Message) => {
    // --- 新增：内容去重检查（非 thinking 类型）---
    if (message.type !== 'thinking') {
      const fingerprint = `${message.type}|${message.text || ''}|${message.metadata?.stop_reason || ''}`;
      const now = Date.now();
      // 清理超过3秒的旧记录
      recentFingerprints.current = recentFingerprints.current.filter(f => now - f.time < 3000);
      const isDuplicate = recentFingerprints.current.some(f => f.fingerprint === fingerprint);
      if (isDuplicate) {
        console.log(`⏭️ 跳过重复消息: ${fingerprint}`);
        return;
      }
      recentFingerprints.current.push({ fingerprint, time: now });
    }
    // --- 新增结束 ---

    // 检查是否应该跳过此消息
    if (skipMessageIds.current.has(message.id)) {
      console.log(`⏭️ 跳过消息: ${message.id}`);
      skipMessageIds.current.delete(message.id);
      return;
    }

    // 跳过没有消息文本的消息（如 conversation_end 等）
    if (!message.text || message.text.trim() === '') {
      console.log(`⏭️ 跳过空消息: ${message.id}`);
      return;
    }

    // 跳过无意义的系统消息（如会话已初始化）
    if (message.type === 'init') {
      console.log(`⏭️ 跳过 init 消息: ${message.id}`);
      return;
    }

    setChatState(prev => {
      let newMessages: Message[];

      // 对于 thinking 类型消息，检查是否已存在相同 id，替换而非追加
      if (message.type === 'thinking') {
        const existingIndex = prev.messages.findIndex(m => m.id === message.id);
        if (existingIndex !== -1) {
          // 替换已存在的 thinking 消息（更新内容）
          console.log(`🔄 替换 thinking 消息: ${message.id}`);
          newMessages = prev.messages.map((m, i) =>
            i === existingIndex ? message : m
          );
        } else {
          newMessages = [...prev.messages, message];
        }
      } else {
        newMessages = [...prev.messages, message];
      }

      // 保存消息到本地文件
      vscodeAPI.saveMessagesToFile(newMessages);
      return {
        ...prev,
        messages: newMessages
      };
    });
  };

  const handleExtensionMessage = (message: ExtensionMessage) => {
    console.log(`webview收到消息: ${JSON.stringify(message)}`);

    // 跳过 conversation_end 消息
    if (message.type === 'conversation_end') {
      console.log(`⏭️ 跳过 conversation_end 消息`);
      return;
    }

    const { type, data } = message;

    // 更新 taskId
    const nextTaskId = message.taskId || data?.task_id;
    if (nextTaskId && nextTaskId !== taskState.taskId) {
      setTaskState(prev => ({ ...prev, taskId: nextTaskId }));
    }

    // 处理不同类型的消息
    if (type === 'ai_response') {
      if (data?.type === 'system' && data?.subtype === 'init') {
        // 初始化消息 - 显示为系统消息
        const initMessage: Message = {
          id: message.id || `init-${Date.now()}`,
          text: '会话已初始化',
          sender: 'system',
          timestamp: Date.now(),
          type: 'init',
          metadata: {
            sessionId: data.session_id,
            model: data.model,
            tools: data.tools,
          }
        };
        addMessageFromExtension(initMessage);
        return;
      }

      if (data?.type === 'assistant' && data?.message) {
        const msgData = data.message;
        const msgId = msgData.id || message.id;
        const content = msgData.content || [];

        // 提取 thinking 内容
        const thinkingContent = content.find((c: any) => c.type === 'thinking');
        // 提取 text 内容
        const textContent = content.find((c: any) => c.type === 'text');
        // 提取 tool_use 内容
        const toolUseBlocks = content.filter((c: any) => c.type === 'tool_use');

        // 捕获 token usage 数据
        if (msgData.usage) {
          pendingUsage.current = msgData.usage;
        }

        // 如果同时有 thinking 和 text，说明 text 是最终回复，thinking 是中间过程
        // 显示 thinking 内容，让用户能看到思考过程
        if (thinkingContent?.thinking && textContent?.text) {
          // 同时有 thinking 和 text，存储 text 等待 result
          pendingAssistantId.current = msgId;
          pendingTextContent.current = textContent.text;
          // 不再跳过 thinking，让它正常显示
          console.log(`📝 同时收到 thinking 和 text，显示 thinking 持续展示`);
          // 继续执行到下面的 thinking 显示逻辑
        }

        // 处理 tool_use 块 - 为每个工具调用发送 tool_call_start 消息
        for (const toolUse of toolUseBlocks) {
          const toolMessage: Message = {
            id: `tool-start-${toolUse.id || msgId}`,
            text: `⚡ ${toolUse.name || 'Unknown Tool'}`,
            sender: 'system',
            timestamp: Date.now(),
            type: 'tool_call_start',
            metadata: {
              toolName: toolUse.name,
              toolCallId: toolUse.id,
              input: toolUse.input,
            }
          };
          addMessageFromExtension(toolMessage);
        }

        // 只有 thinking 内容 - 立即显示
        if (thinkingContent?.thinking && !textContent?.text) {
          const thinkingMessage: Message = {
            id: `thinking-${msgId}`,
            text: thinkingContent.thinking,
            sender: 'assistant',
            timestamp: Date.now(),
            type: 'thinking',
            metadata: {
              messageId: msgId,
            }
          };
          addMessageFromExtension(thinkingMessage);
          return;
        }

        // 只有 text 内容 - 存储等待 result
        if (textContent?.text && !thinkingContent?.thinking) {
          pendingAssistantId.current = msgId;
          pendingTextContent.current = textContent.text;
          console.log(`📝 收到 text，存储等待 result: ${msgId}`);
          return;
        }

        // 捕获 assistant 消息中的 token usage 数据
        if (msgData.usage) {
          pendingUsage.current = msgData.usage;
        }
        return;
      }

      // 处理 user 消息中的 tool_result
      if (data?.type === 'user') {
        // 从 message.content 数组中提取 tool_result 块
        const userContent = data.message?.content || [];
        const toolResultBlocks = userContent.filter((c: any) => c.type === 'tool_result');

        // 也检查 message.tool_use_result 字段（顶层的工具结果）
        const topLevelToolResult = data.message?.tool_use_result;

        // 处理 content 数组中的 tool_result 块
        for (const toolResult of toolResultBlocks) {
          const toolMessage: Message = {
            id: `tool-result-${toolResult.tool_use_id || Date.now()}`,
            text: toolResult.content || '',
            sender: 'system',
            timestamp: Date.now(),
            type: toolResult.is_error ? 'tool_call_error' : 'tool_call_result',
            metadata: {
              toolName: 'tool',
              toolCallId: toolResult.tool_use_id,
              result: toolResult.content,
              error: toolResult.is_error ? toolResult.content : undefined,
            }
          };
          addMessageFromExtension(toolMessage);
        }

        // 处理 message.tool_use_result（如 bash/cargo 等命令的执行结果）
        if (topLevelToolResult && toolResultBlocks.length === 0) {
          const isError = topLevelToolResult.startsWith('Error:') || data.is_error;
          const toolMessage: Message = {
            id: `tool-result-${userContent?.[0]?.tool_use_id || Date.now()}`,
            text: topLevelToolResult,
            sender: 'system',
            timestamp: Date.now(),
            type: isError ? 'tool_call_error' : 'tool_call_result',
            metadata: {
              toolName: 'Bash',
              toolCallId: userContent?.[0]?.tool_use_id,
              result: topLevelToolResult,
              error: isError ? topLevelToolResult : undefined,
            }
          };
          addMessageFromExtension(toolMessage);
        }

        return;
      }

      if (data?.type === 'result') {
        // result 消息 - 这是最终结果
        const resultText = data.result || '';
        const resultId = data.uuid || message.id;

        // --- 新增：防重复 result 保护 ---
        if (!pendingAssistantId.current && !pendingTextContent.current) {
          // 没有等待中的 pending 状态，检查是否刚处理过 result
          if (lastResultTime.current && Date.now() - lastResultTime.current < 2000) {
            console.log(`⏭️ 跳过重复 result: ${resultId}`);
            return;
          }
        }
        lastResultTime.current = Date.now();
        // --- 新增结束 ---

        // 如果有 pending 的 text，用 result 替换
        if (pendingAssistantId.current && pendingTextContent.current) {
          const msgId = pendingAssistantId.current;
          // 标记跳过 pending 的 text
          skipMessageIds.current.add(msgId);
          console.log(`✅ result 到达，替换 pending text: ${msgId}`);

          // 重置 pending 状态
          pendingAssistantId.current = null;
          pendingTextContent.current = null;
        }

        // 当 stop_reason 为 end_turn 时，任务完成
        if (data.stop_reason === 'end_turn') {
          console.log('✅ 任务完成 (end_turn)');
          setTaskState(prev => ({
            ...prev,
            taskStatus: 'completed',
            isTaskRunning: false,
            taskProgress: 100,
            currentStep: '任务已完成'
          }));
          setProcessing(false);
        }

        // 合并 pending usage 与 result 的 usage
        const mergedUsage = {
          ...(pendingUsage.current || {}),
          ...(data.usage || {}),
        };

        const resultMessage: Message = {
          id: `result-${resultId}-${Date.now()}`,
          text: resultText,
          sender: 'assistant',
          timestamp: Date.now(),
          type: 'result',
          metadata: {
            is_error: data.is_error,
            duration_ms: data.duration_ms,
            num_turns: data.num_turns,
            stop_reason: data.stop_reason,
            total_cost_usd: data.total_cost_usd,
            usage: Object.keys(mergedUsage).length > 0 ? mergedUsage : undefined,
            modelUsage: data.modelUsage,
          }
        };
        pendingUsage.current = null;
        addMessageFromExtension(resultMessage);
        return;
      }

      // 兜底处理：当以上 assistant / user / result 分支都不匹配时，
      // 直接基于 message.data 构建一条 result 消息（如纯字符串 data 场景）
      if (message.data) {
        const rawText = typeof message.data === 'string' ? message.data : String(message.data);
        const contentText = normalizeAIResponseContent(rawText);

        if (contentText.trim()) {
          const fallbackMessage: Message = {
            id: message.id || `result-${Date.now()}`,
            text: contentText,
            sender: message.from || 'assistant',
            timestamp: Date.now(),
            type: 'result',
            metadata: {
              taskId: message.taskId,
            },
          };
          addMessageFromExtension(fallbackMessage);
        }
        return;
      }
    }

    if (type === 'tool_call_start') {
      // 工具调用开始
      const toolMessage: Message = {
        id: message.id || `tool-start-${Date.now()}`,
        text: `⚡ ${data?.tool_name || 'Unknown Tool'}`,
        sender: 'system',
        timestamp: Date.now(),
        type: 'tool_call_start',
        metadata: {
          toolName: data?.tool_name || data?.toolName,
          toolCallId: data?.tool_call_id || data?.toolCallId,
        }
      };
      addMessageFromExtension(toolMessage);
      return;
    }

    if (type === 'tool_call_result') {
      if (data?.tool_name === 'session') {
        return;
      }
      // 处理 agent_exit 消息
      // 注意：只有 Conductor（主控 agent）发送的 agent_exit 才表示任务真正结束
      // 子 agent（如 Chat-Agent、Coding-Agent 等）的 agent_exit 只是完成子任务
      if (data?.tool_name === 'agent_exit') {
        // 只有 Conductor 发送的 agent_exit 才表示任务结束
        if (message.from === 'Conductor') {
          console.log('🏁 收到 Conductor 的 agent_exit 消息，任务完成');
          
          // 解析 result 获取完成任务的详细信息
          let exitReason = '任务已完成';
          try {
            const exitData = data?.result ? JSON.parse(data.result) : null;
            if (exitData?.finished === true) {
              exitReason = exitData.reason || '任务已完成';
              console.log('✅ 任务完成: ' + exitReason);
            }
          } catch (e) {
            console.warn('⚠️ 解析 agent_exit result 失败:', e);
          }
          
          // 更新任务状态为已完成
          setTaskState(prev => ({
            ...prev,
            taskStatus: 'completed',
            isTaskRunning: false,
            taskProgress: 100,
            currentStep: '任务已完成'
          }));
          
          // 停止 loading 动画
          setProcessing(false);
          
          // 添加任务完成通知消息
          const completeMessage: Message = {
            id: `task-complete-${Date.now()}`,
            text: `✅ ${exitReason}`,
            sender: 'system',
            timestamp: Date.now(),
            type: 'status_update',
            metadata: {
              taskId: data?.task_id || message.taskId
            }
          };
          addMessageFromExtension(completeMessage);
          
          return;
        } else {
          // 子 agent 的 agent_exit，仅显示为普通工具调用结果
          console.log('📌 收到子 agent (' + message.from + ') 的 agent_exit，仅记录不结束任务');
        }
      }
      
      // 工具调用结果（其他工具）
      const toolMessage: Message = {
        id: message.id || `tool-${Date.now()}`,
        text: data?.result || '',
        sender: 'system',
        timestamp: Date.now(),
        type: 'tool_call_result',
        metadata: {
          toolName: data?.tool_name,
          toolCallId: data?.tool_call_id,
          result: data?.result,
          error: data?.error,
        }
      };
      addMessageFromExtension(toolMessage);
      return;
    }

    if (type === 'tool_call_error') {
      // 工具调用错误
      const toolMessage: Message = {
        id: message.id || `tool-error-${Date.now()}`,
        text: `❌ ${data?.error || 'Tool execution failed'}`,
        sender: 'system',
        timestamp: Date.now(),
        type: 'tool_call_error',
        metadata: {
          toolName: data?.tool_name,
          toolCallId: data?.tool_call_id,
          error: data?.error,
        }
      };
      addMessageFromExtension(toolMessage);
      return;
    }

    if (type === 'error') {
      // 全局/对话错误 - 与 tool_call_error 不同，这是整体对话层面的错误
      console.log(`❌ 对话错误: ${message.error || data?.error || '未知错误'}`);

      // 停止处理状态
      setProcessing(false);

      // 更新任务状态
      setTaskState(prev => ({
        ...prev,
        taskStatus: 'error',
        isTaskRunning: false,
      }));

      // 将错误显示为聊天消息
      const errorMessage: Message = {
        id: message.id || generateMessageId(),
        text: `❌ Error: ${message.error || data?.error || '未知错误'}`,
        sender: 'system',
        timestamp: Date.now(),
        type: 'error',
        metadata: {
          taskId: message.taskId || data?.task_id,
        }
      };
      addMessageFromExtension(errorMessage);
      return;
    }
  };

  useEffect(() => {
    // 检测是否在服务器模式
    const isServerMode = !vscodeAPI.isVSCodeEnvironment() &&
      (!!window.SERVER_URL || new URLSearchParams(window.location.search).has('server'));

    // 如果在浏览器环境中，发送初始化消息
    if (!vscodeAPI.isVSCodeEnvironment()) {
      // 模拟初始化消息
      setTimeout(() => {
        const mockInitMessage: ExtensionMessage = {
          type: 'initialize',
          theme: 0 // 暗色主题
        };
        handleExtensionMessage(mockInitMessage);
      }, 500);

      // 在服务器模式下跳过 mock 数据自动加载
      if (isServerMode) {
        console.log('🖥️ 服务器模式 - 连接到 Claude Code 服务器');
        // 服务器模式会在 vscodeAPI.onMessage 中接收消息
        const unsubscribe = vscodeAPI.onMessage(handleExtensionMessage);
        return () => {
          if (unsubscribe) {
            unsubscribe();
          }
        };
      }

      // 在浏览器预览模式下启动 mock 数据自动加载
      console.log('🎭 启动 Mock 数据自动加载模式');

      // 设置消息回调
      mockMessageLoader.setOnMessage((message: Message) => {
        console.log('📨 加载 Mock 消息:', message);
        setChatState(prev => ({
          ...prev,
          messages: [...prev.messages, message]
        }));
      });

      // 延迟3秒后开始自动加载消息
      setTimeout(() => {
        mockMessageLoader.startAutoLoad();
      }, 3000);

      // 清理函数
      return () => {
        mockMessageLoader.stopAutoLoad();
      };
    }

    const unsubscribe = vscodeAPI.onMessage(handleExtensionMessage);

    return () => {
      if (unsubscribe) {
        unsubscribe();
      }
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const addMessage = (text: string, sender: string, messageId?: string, messageType?: string, metadata?: any) => {
    const newMessage: Message = {
      id: messageId || generateMessageId(),
      text,
      sender,
      timestamp: Date.now(),
      type: messageType as any || (sender === 'assistant' && text.includes('```') ? 'code' : 'text'),
      metadata: metadata
    };

    setChatState(prev => ({
      ...prev,
      messages: [...prev.messages, newMessage]
    }));
  };

  const showError = (error: string) => {
    const newMessage: Message = {
      id: generateMessageId(),
      text: `❌ **Error:** ${error}`,
      sender: 'system',
      timestamp: Date.now(),
      type: 'error'
    };

    setChatState(prev => ({
      ...prev,
      messages: [...prev.messages, newMessage]
    }));
  };

  const sendMessage = (text: string) => {
    if (!text.trim() || chatState.isProcessing) return;

    // 添加用户消息
    addMessage(text, 'User');
    
    // 设置处理状态
    setProcessing(true);

    try {
      // 发送消息到扩展
      const message = {
        type: chatState.currentMode,
        id: generateMessageId(),
        payload: {
          text: text.trim(),
          mode: chatState.currentMode,
          timestamp: Date.now()
        }
      };

      vscodeAPI.postMessage(message);
    } catch (error) {
      console.error('Error sending message:', error);
      showError('Failed to send message. Please try again.');
      setProcessing(false);
    }
  };


  const submitTask = (taskDesc: string) => {
    if (!taskDesc.trim() || chatState.isProcessing) return;

    console.log('🚀 submitTask called:', { taskDesc });

    // 添加用户消息显示任务提交
    addMessage(`${taskDesc}`, 'user');
    
    // 设置处理状态和任务状态
    setProcessing(true);
    const newTaskState = {
      isTaskRunning: true,
      currentTask: taskDesc,
      taskProgress: 0,
      currentStep: '准备执行任务...',
      taskStatus: 'running' as const
    };
    
    console.log('🔄 Setting taskState to:', newTaskState);
    setTaskState(prev => ({ ...prev, ...newTaskState }));

    try {
      const existingTaskId = taskIdRef.current;
      if (existingTaskId) {
        const message = {
          type: chatState.currentMode,
          id: generateMessageId(),
          payload: {
            text: taskDesc.trim(),
            mode: chatState.currentMode,
            timestamp: Date.now()
          }
        };
        vscodeAPI.postMessage(message);
      } else {
        const message = {
          type: 'submitTask' as const,
          id: generateMessageId(),
          payload: {
            taskDesc: taskDesc.trim(),
            timestamp: Date.now()
          }
        };
        vscodeAPI.postMessage(message);
      }
    } catch (error) {
      console.error('Error submitting task:', error);
      showError('Failed to submit task. Please try again.');
      setProcessing(false);
      setTaskState(prev => ({ ...prev, isTaskRunning: false, taskStatus: 'failed' }));
    }
  };

  // 添加终止任务功能
  const terminateTask = () => {
    if (!taskState.isTaskRunning) return;
    try {
      const message = {
        type: 'terminateTask' as const,
        id: generateMessageId(),
        payload: {
          timestamp: Date.now()
        }
      };

      vscodeAPI.postMessage(message);
      
      // 更新任务状态
      setTaskState(prev => ({
        ...prev,
        taskStatus: 'terminated',
        isTaskRunning: false
      }));
      
      setProcessing(false);
      addMessage('🛑 任务已终止', 'system');
    } catch (error) {
      console.error('Error terminating task:', error);
      showError('Failed to terminate task.');
    }
  };

  // 关闭任务进度显示
  const closeTaskProgress = () => {
    setTaskState(prev => ({ ...prev, isTaskRunning: false }));
  };

  const clearChat = () => {
    setChatState(prev => ({
      ...prev,
      messages: []
    }));

    // 清理去重缓存
    recentFingerprints.current = [];
    lastResultTime.current = null;

    // Reset task state
    setTaskState(prev => ({
        ...prev,
        currentTask: '',
        taskStatus: 'completed',
        isTaskRunning: false,
        taskProgress: 0,
        currentStep: '',
        taskId: undefined
    }));

    // Send clearChat message to extension to clear task context
    try {
      const message = {
        type: 'clearChat' as const,
        id: generateMessageId(),
        payload: {
          timestamp: Date.now()
        }
      };
      vscodeAPI.postMessage(message);
    } catch (error) {
      console.error('Error sending clearChat message:', error);
    }
  };

  const setMode = (mode: 'chat' | 'agent') => {
    setChatState(prev => ({
      ...prev,
      currentMode: mode
    }));
  };

  const setProcessing = (processing: boolean) => {
    setChatState(prev => ({
      ...prev,
      isProcessing: processing
    }));
  };


  const generateMessageId = (): string => {
    return `msg-${Date.now()}-${Math.random().toString(36).substr(2, 9)}`;
  };

  // 处理消息操作（重试、继续对话等）
  const handleMessageAction = (messageId: string, actionId: string) => {
    console.log(`处理消息操作: ${messageId}, 操作: ${actionId}`);
    
    const message = chatState.messages.find(msg => msg.id === messageId);
    if (!message) {
      console.error('未找到消息:', messageId);
      return;
    }

    switch (actionId) {
      case 'retry':
        // 重试：重新提交当前任务
        if (message.metadata?.taskId && taskState.currentTask) {
          console.log('重试任务:', taskState.currentTask);
          submitTask(taskState.currentTask);
        } else {
          console.log('重试失败：缺少任务信息');
          showError('无法重试：缺少任务信息');
        }
        break;
        
      case 'continue':
        // 继续对话：清除错误状态，允许用户继续输入
        console.log('继续对话');
        setTaskState(prev => ({
          ...prev,
          taskStatus: 'completed',
          isTaskRunning: false
        }));
        setProcessing(false);
        addMessage('✅ 您可以继续对话或提交新任务', 'system');
        break;
        
      default:
        console.log('未知操作:', actionId);
    }
  };

  // 从外部加载消息（如上传 JSON 文件），触发消息卡片渲染
  const loadMessages = (messages: Message[]) => {
    setChatState(prev => ({
      ...prev,
      messages: [...prev.messages, ...messages]
    }));
    // 同时保存到文件
    vscodeAPI.saveMessagesToFile([...chatState.messages, ...messages]);
  };

  return {
    chatState,
    taskState,
    sendMessage,
    clearChat,
    setMode,
    submitTask,
    terminateTask,
    closeTaskProgress,
    handleMessageAction,
    loadMessages,
  };
};
