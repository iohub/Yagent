import { ExtensionMessage, WebviewMessage } from '../types';

// ============================================================
// Agent 后端 WebSocket 协议类型定义
// ============================================================

/** 客户端 → 服务器消息 */
export interface AgentClientMessage {
  type: 'event';
  event: 'start_task' | 'chat_message' | 'cancel_task';
  data?: any;
  task_id?: string;
}

// Server API 类型声明
declare global {
  interface Window {
    SERVER_URL?: string;
    PROJECT_DIR?: string;
  }
}

// ============================================================
// ServerAPI 类
// ============================================================

export class ServerAPI {
  private static instance: ServerAPI;

  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();
  private ws: WebSocket | null = null;
  private wsUrl: string = 'ws://localhost:3000/ws';
  private httpUrl: string = 'http://localhost:3000';

  // 任务状态
  private currentTaskId: string | null = null;

  // 连接状态
  private connecting: boolean = false;
  private intentionalClose: boolean = false;

  // 重连相关
  private reconnectAttempts: number = 0;
  private maxReconnectAttempts: number = 10;
  private reconnectTimer: number | null = null;
  private reconnectIntervalMs: number = 5000; // 基础间隔 5 秒

  // WebSocket 打开超时
  private wsOpenTimeoutMs: number = 10000;

  // 消息队列（连接未就绪时缓存）
  private pendingMessages: AgentClientMessage[] = [];

  private constructor() {
    // 优先从 URL 参数获取服务器地址，其次从 window.SERVER_URL，默认连接到本地 3000 端口
    const urlParams = new URLSearchParams(window.location.search);
    const urlFromParams = urlParams.get('url');
    const urlFromServer = window.SERVER_URL;
    const baseUrl = urlFromParams || urlFromServer || 'ws://localhost:3000';

    // 确保 URL 有协议前缀
    let normalizedUrl = baseUrl;
    if (!normalizedUrl.startsWith('ws://') && !normalizedUrl.startsWith('wss://')) {
      normalizedUrl = 'ws://' + normalizedUrl;
    }

    // 移除末尾的 /ws（如果有）以避免重复
    normalizedUrl = normalizedUrl.replace(/\/ws$/, '');

    // 转换 ws:// <-> http://
    if (normalizedUrl.startsWith('ws://')) {
      this.wsUrl = normalizedUrl + '/ws';
      this.httpUrl = normalizedUrl.replace('ws://', 'http://');
    } else if (normalizedUrl.startsWith('wss://')) {
      this.wsUrl = normalizedUrl + '/ws';
      this.httpUrl = normalizedUrl.replace('wss://', 'https://');
    }

    console.log('ServerAPI initialized:');
    console.log('  WebSocket URL:', this.wsUrl);
    console.log('  HTTP URL:', this.httpUrl);
  }

  public static getInstance(): ServerAPI {
    if (!ServerAPI.instance) {
      ServerAPI.instance = new ServerAPI();
    }
    return ServerAPI.instance;
  }

  /**
   * 设置服务器地址，会断开现有连接
   */
  public setServerUrl(url: string): void {
    // 清理重连
    this.clearReconnectTimer();

    let normalizedUrl = url;
    if (!normalizedUrl.startsWith('ws://') && !normalizedUrl.startsWith('wss://')) {
      normalizedUrl = 'ws://' + normalizedUrl;
    }
    normalizedUrl = normalizedUrl.replace(/\/ws$/, '');

    if (normalizedUrl.startsWith('ws://')) {
      this.wsUrl = normalizedUrl + '/ws';
      this.httpUrl = normalizedUrl.replace('ws://', 'http://');
    } else if (normalizedUrl.startsWith('wss://')) {
      this.wsUrl = normalizedUrl + '/ws';
      this.httpUrl = normalizedUrl.replace('wss://', 'https://');
    }

    // 断开旧连接
    if (this.ws) {
      this.intentionalClose = true;
      try {
        this.ws.close();
      } catch { /* empty */ }
      this.ws = null;
    }
    this.currentTaskId = null;
    this.connecting = false;
    this.reconnectAttempts = 0;

    this.broadcastStatus('server_url_changed', { wsUrl: this.wsUrl, httpUrl: this.httpUrl });
  }

  // ============================================================
  // 连接管理
  // ============================================================

  /**
   * 连接到 WebSocket 服务器（直接连接 /ws 端点）
   */
  public connect(): Promise<void> {
    if (this.connecting) {
      console.log('[ServerAPI] 正在连接中...');
      return Promise.resolve();
    }
    if (this.isConnected()) {
      console.log('[ServerAPI] 已连接');
      return Promise.resolve();
    }

    this.connecting = true;
    this.intentionalClose = false;
    this.broadcastStatus('connecting');

    return new Promise((resolve, reject) => {
      console.log('[ServerAPI] 正在连接到:', this.wsUrl);
      this.ws = new WebSocket(this.wsUrl);

      let wsOpenTimer: number | null = null;
      wsOpenTimer = window.setTimeout(() => {
        this.broadcastStatus('websocket_open_timeout', { timeoutMs: this.wsOpenTimeoutMs });
        try {
          this.ws?.close();
        } catch { /* empty */ }
        this.connecting = false;
        reject(new Error(`WebSocket 连接超时 (${this.wsOpenTimeoutMs}ms)`));
      }, this.wsOpenTimeoutMs);

      const clearWsOpenTimer = () => {
        if (wsOpenTimer != null) {
          clearTimeout(wsOpenTimer);
          wsOpenTimer = null;
        }
      };

      this.ws.onopen = () => {
        clearWsOpenTimer();
        console.log('[ServerAPI] WebSocket 已连接');
        this.connecting = false;
        this.reconnectAttempts = 0;
        this.broadcastStatus('connected');

        // 发送队列中的消息
        this.flushPendingMessages();

        resolve();
      };

      this.ws.onmessage = (event: MessageEvent) => {
        try {
          const rawMessage = JSON.parse(event.data);
          console.log('[ServerAPI] 收到消息:', rawMessage.type, rawMessage.event);
          this.handleServerMessage(rawMessage);
        } catch (e) {
          console.error('[ServerAPI] 解析消息失败:', e);
        }
      };

      this.ws.onerror = (error: Event) => {
        clearWsOpenTimer();
        console.error('[ServerAPI] WebSocket 错误:', error);
        this.connecting = false;
        // onerror 后通常紧接着 onclose，重连在 onclose 处理
      };

      this.ws.onclose = (event: CloseEvent) => {
        clearWsOpenTimer();
        console.log('[ServerAPI] WebSocket 已关闭:', event.code, event.reason);
        this.ws = null;
        this.connecting = false;

        if (!this.intentionalClose) {
          this.attemptReconnect();
        } else {
          this.broadcastStatus('disconnected');
        }
      };
    });
  }

  /**
   * 确保连接已建立
   */
  public async ensureConnected(): Promise<void> {
    if (!this.isConnected() && !this.connecting) {
      await this.connect();
    }
  }

  /**
   * 断开连接
   */
  public disconnect(): void {
    this.intentionalClose = true;
    this.clearReconnectTimer();
    this.reconnectAttempts = 0;
    this.connecting = false;

    if (this.ws) {
      try {
        this.ws.close(1000, '客户端主动断开');
      } catch { /* empty */ }
      this.ws = null;
    }
    this.currentTaskId = null;
    this.pendingMessages = [];
    this.broadcastStatus('disconnected');
  }

  /**
   * 检查是否已连接
   */
  public isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  /**
   * 是否在 VSCode 环境中（始终返回 false，表示这是独立服务器模式）
   */
  public isVSCodeEnvironment(): boolean {
    return false;
  }

  // ============================================================
  // 消息发送
  // ============================================================

  /**
   * 将前端的 WebviewMessage 转换为 Agent 协议格式并发送
   */
  public postMessage(message: WebviewMessage): void {
    const agentMsg = this.convertWebviewToAgent(message);
    if (!agentMsg) {
      return; // 不需要发送的消息（如 clearChat）
    }
    this.sendOrQueue(agentMsg);
  }

  /**
   * 直接发送 Agent 协议格式消息
   */
  public sendMessage(payload: AgentClientMessage): void {
    this.sendOrQueue(payload);
  }

  // ============================================================
  // 消息接收注册
  // ============================================================

  /**
   * 注册消息处理器，返回取消订阅函数
   */
  public onMessage(handler: (message: ExtensionMessage) => void): () => void {
    const id = Math.random().toString(36).substr(2, 9);
    this.messageHandlers.set(id, handler);

    // 立即尝试连接
    if (!this.isConnected() && !this.connecting) {
      this.connect().catch((err) => {
        console.error('[ServerAPI] 连接失败:', err);
      });
    }

    return () => {
      this.messageHandlers.delete(id);
    };
  }

  // ============================================================
  // 状态查询
  // ============================================================

  public getState(): any {
    return {
      taskId: this.currentTaskId,
      connected: this.isConnected(),
      connecting: this.connecting,
      wsUrl: this.wsUrl,
    };
  }

  public setState(_state: any): void {
    // 服务器模式不需要本地状态管理
  }

  // ============================================================
  // 消息存储（保持兼容）
  // ============================================================

  private storedMessages: any[] = [];

  public saveMessagesToFile(messages: any[]): void {
    this.storedMessages = messages;
    console.log('[ServerAPI] 消息已存储，当前数量:', messages.length);
  }

  public downloadStoredMessages(filePath?: string): void {
    if (this.storedMessages.length === 0) {
      console.warn('[ServerAPI] 没有可下载的消息');
      return;
    }

    const fileName = filePath || 'webui-messages.json';
    try {
      const jsonContent = JSON.stringify(this.storedMessages, null, 2);
      const blob = new Blob([jsonContent], { type: 'application/json' });
      const url = URL.createObjectURL(blob);

      const link = document.createElement('a');
      link.href = url;
      link.download = fileName;
      document.body.appendChild(link);
      link.click();
      document.body.removeChild(link);

      URL.revokeObjectURL(url);
      console.log('[ServerAPI] 消息已下载保存到:', fileName);
    } catch (error) {
      console.error('[ServerAPI] 下载消息失败:', error);
    }
  }

  // ============================================================
  // 内部方法：消息转换
  // ============================================================

  /**
   * 将 WebviewMessage 转换为 Agent 协议格式
   */
  private convertWebviewToAgent(message: WebviewMessage): AgentClientMessage | null {
    // 获取 projectDir：优先从消息 payload 获取，其次从 window.PROJECT_DIR 全局变量
    const getProjectDir = (payloadProjectDir?: string): string => {
      if (payloadProjectDir) return payloadProjectDir;
      if (typeof window !== 'undefined' && window.PROJECT_DIR) return window.PROJECT_DIR;
      return '';
    };

    switch (message.type) {
      case 'submitTask': {
        // 首次提交任务 → start_task
        const taskDesc = message.payload.taskDesc || message.payload.text || '';
        const projectDir = getProjectDir(message.payload.projectDir);
        return {
          type: 'event',
          event: 'start_task',
          data: {
            project_dir: projectDir,
            task_desc: taskDesc,
          },
        };
      }

      case 'chat':
      case 'agent': {
        const text = message.payload.text || '';
        const projectDir = getProjectDir(message.payload.projectDir);

        if (this.currentTaskId) {
          // 已有任务 ID → chat_message
          return {
            type: 'event',
            event: 'chat_message',
            task_id: this.currentTaskId,
            data: {
              task_id: this.currentTaskId,
              message: text,
              project_dir: projectDir,
            },
          };
        } else {
          // 无任务 ID → 作为新任务启动
          return {
            type: 'event',
            event: 'start_task',
            data: {
              project_dir: projectDir,
              task_desc: text,
            },
          };
        }
      }

      case 'terminateTask': {
        // 终止任务
        if (!this.currentTaskId) {
          console.warn('[ServerAPI] 没有活跃任务可终止');
          return null;
        }
        return {
          type: 'event',
          event: 'cancel_task',
          task_id: this.currentTaskId,
          data: {},
        };
      }

      case 'clearChat': {
        // 清空聊天是本地操作，无需发送 WebSocket 消息
        this.currentTaskId = null;
        return null;
      }

      case 'saveMessages': {
        // 保存消息是本地操作
        return null;
      }

      case 'code': {
        // 代码相关消息，作为聊天消息发送
        const text = message.payload.text || '';
        const projectDir = getProjectDir(message.payload.projectDir);
        if (this.currentTaskId) {
          return {
            type: 'event',
            event: 'chat_message',
            task_id: this.currentTaskId,
            data: {
              task_id: this.currentTaskId,
              message: text,
              project_dir: projectDir,
            },
          };
        }
        return null;
      }

      default: {
        console.warn('[ServerAPI] 未知的消息类型:', message.type);
        return null;
      }
    }
  }

  /**
   * 将 Agent 服务器消息转换为 ExtensionMessage
   */
  private convertAgentToExtension(raw: any): ExtensionMessage | null {
    const type = raw.type;
    const event = raw.event;

    switch (type) {
      // ---- 连接确认 ----
      case 'connection': {
        console.log('[ServerAPI] 连接确认:', raw.data?.message);
        // 连接状态已在 broadcastStatus 中处理，无需发送 ExtensionMessage
        return null;
      }

      // ---- 任务创建确认 ----
      case 'task_created': {
        const taskId = raw.data?.task_id;
        if (taskId) {
          this.currentTaskId = taskId;
          console.log('[ServerAPI] 任务已创建, task_id:', taskId);
        }
        // 发送任务已创建消息
        const extMsg: ExtensionMessage = {
          type: 'ai_response',
          id: `task-created-${Date.now()}`,
          data: {
            type: 'system',
            subtype: 'init',
            session_id: taskId,
          },
          taskId: taskId,
        };
        return extMsg;
      }

      // ---- 实时消息（最重要的类型） ----
      case 'realtime': {
        return this.convertRealtimeEvent(raw);
      }

      // ---- 对话最终回复 ----
      case 'chat_message': {
        // { type: 'chat_message', event: 'ai_response', data: { type: 'assistant', content: '文本', timestamp }, from: 'CodingAgent' }
        if (event === 'ai_response' && raw.data) {
          const extMsg: ExtensionMessage = {
            type: 'ai_response',
            id: `chat-msg-${Date.now()}`,
            data: {
              type: 'assistant',
              message: {
                id: `msg-${Date.now()}`,
                content: [
                  { type: 'text', text: raw.data.content || '' },
                ],
              },
            },
            taskId: this.currentTaskId || undefined,
          };
          return extMsg;
        }
        return null;
      }

      // ---- 错误消息 ----
      case 'error': {
        const extMsg: ExtensionMessage = {
          type: 'error',
          id: `error-${Date.now()}`,
          error: raw.message || '未知错误',
          data: {
            error: raw.message || '未知错误',
          },
        };
        return extMsg;
      }

      // ---- 任务状态更新（完成时发送 result 消息让 conversation.ts 处理） ----
      case 'task_update': {
        const taskId = raw.data?.task_id;
        const status = raw.data?.status;
        if (taskId) {
          this.currentTaskId = taskId;
        }

        // 任务完成或失败时发送 result 消息（conversation.ts 的 handleExtensionMessage 处理）
        if (status === 'completed' || status === 'failed' || status === 'cancelled') {
          const extMsg: ExtensionMessage = {
            type: 'ai_response',
            id: `task-end-${Date.now()}`,
            data: {
              type: 'result' as const,
              result: status === 'completed' ? '任务完成' : `任务${status}`,
              stop_reason: 'end_turn' as const,
              task_id: taskId,
              is_error: status === 'failed' || status === 'cancelled',
            },
            taskId: taskId,
          };
          return extMsg;
        }

        // 其他状态更新作为连接状态广播
        this.broadcastStatus('task_update', {
          task_id: taskId,
          status: status,
          progress: raw.data?.progress,
        });
        return null;
      }

      default: {
        console.warn('[ServerAPI] 未知的服务器消息类型:', type);
        return null;
      }
    }
  }

  /**
   * 转换 realtime 消息（按 event 字段分发）
   */
  private convertRealtimeEvent(raw: any): ExtensionMessage | null {
    const event = raw.event;
    const content = raw.data?.content;
    const taskId = raw.data?.task_id || this.currentTaskId;
    const from = raw.from;

    switch (event) {
      // ---- AI 响应（核心） ----
      case 'ai_response': {
        if (!content) {
          console.warn('[ServerAPI] ai_response 消息缺少 content 字段');
          return null;
        }

        // content 结构可能为:
        // { type: 'assistant', message: { id, content: [{type:'text',text:'...'}, {type:'tool_use',id,name,input}], usage } }
        // { type: 'user', message: { content: [{type:'tool_result',tool_use_id,content,is_error}] } }
        // { type: 'result', result, stop_reason, usage, uuid }
        // conversation.ts 的 handleExtensionMessage 能直接处理这三种格式

        const extMsg: ExtensionMessage = {
          type: 'ai_response',
          id: `ai-resp-${Date.now()}`,
          data: content,
          taskId: taskId || undefined,
          from: from,
        };
        return extMsg;
      }

      // ---- 工具调用开始 ----
      case 'tool_call_start': {
        const extMsg: ExtensionMessage = {
          type: 'tool_call_start',
          id: `tool-start-${content?.tool_call_id || content?.id || Date.now()}`,
          data: {
            tool_name: content?.tool_name || content?.name || 'Unknown',
            tool_call_id: content?.tool_call_id || content?.id || '',
            task_id: taskId,
            arguments: content?.input || content?.arguments || {},
          },
          taskId: taskId || undefined,
          from: from,
        };
        return extMsg;
      }

      // ---- 工具调用结果 ----
      case 'tool_call_result': {
        const isError = content?.is_error || false;
        // 工具结果可能在 content 的多个可能字段中
        const resultContent =
          content?.content || content?.result || content?.output || content?.text || '';

        if (isError) {
          const extMsg: ExtensionMessage = {
            type: 'tool_call_error',
            id: `tool-error-${content?.tool_use_id || Date.now()}`,
            data: {
              tool_name: content?.tool_name || 'tool',
              tool_call_id: content?.tool_use_id || content?.tool_call_id || '',
              error: resultContent,
              task_id: taskId,
            },
            taskId: taskId || undefined,
            from: from,
          };
          return extMsg;
        }

        const extMsg: ExtensionMessage = {
          type: 'tool_call_result',
          id: `tool-result-${content?.tool_use_id || Date.now()}`,
          data: {
            tool_name: content?.tool_name || 'tool',
            tool_call_id: content?.tool_use_id || content?.tool_call_id || '',
            result: resultContent,
            task_id: taskId,
          },
          taskId: taskId || undefined,
          from: from,
        };
        return extMsg;
      }

      // ---- 上下文加载/压缩 ----
      case 'context_loaded':
      case 'commit_context_loaded': {
        this.broadcastStatus('context_loaded', {
          task_id: taskId,
          detail: content,
        });
        return null;
      }

      case 'context_compressed': {
        this.broadcastStatus('context_compressed', {
          task_id: taskId,
          detail: content,
        });
        return null;
      }

      // ---- LLM 调用状态 ----
      case 'llm_call_start': {
        this.broadcastStatus('llm_call_start', {
          task_id: taskId,
          detail: content,
        });
        return null;
      }

      case 'llm_call_end': {
        this.broadcastStatus('llm_call_end', {
          task_id: taskId,
          detail: content,
          metadata: raw.data?.metadata,
        });
        return null;
      }

      // ---- 模型信息 ----
      case 'model_info': {
        this.broadcastStatus('model_info', {
          task_id: taskId,
          agent: content?.agent,
          model: content?.model,
        });
        return null;
      }

      // ---- 对话错误 ----
      case 'conversation_error': {
        const extMsg: ExtensionMessage = {
          type: 'error',
          id: `conv-error-${Date.now()}`,
          error: content?.error || content?.message || '对话处理错误',
          data: {
            error: content?.error || content?.message || '对话处理错误',
            task_id: taskId,
          },
          taskId: taskId || undefined,
        };
        return extMsg;
      }

      // ---- 对话结束（先发 result 消息让 conversation.ts 处理） ----
      case 'conversation_result': {
        const extMsg: ExtensionMessage = {
          type: 'ai_response',
          id: `conv-result-${Date.now()}`,
          data: {
            type: 'result' as const,
            result: content?.result || content?.summary || '',
            stop_reason: 'end_turn',
            task_id: taskId,
            uuid: content?.uuid || Date.now().toString(),
            usage: content?.usage || undefined,
          },
          taskId: taskId || undefined,
        };
        return extMsg;
      }

      // ---- 需要用户帮助 ----
      case 'user_help_needed': {
        const extMsg: ExtensionMessage = {
          type: 'user_help_needed',
          id: `user-help-${Date.now()}`,
          data: {
            prompt: content?.prompt || content?.message || '需要用户确认',
            task_id: taskId,
          },
          taskId: taskId || undefined,
        };
        return extMsg;
      }

      // ---- 状态更新（历史兼容） ----
      case 'status_update': {
        this.broadcastStatus('status_update', {
          task_id: taskId,
          status: content?.status || content,
        });
        return null;
      }

      default: {
        console.warn('[ServerAPI] 未知的 realtime 事件类型:', event);
        return null;
      }
    }
  }

  // ============================================================
  // 内部方法：消息路由与分发
  // ============================================================

  /**
   * 处理服务器消息，转换为 ExtensionMessage 并分发
   */
  private handleServerMessage(raw: any): void {
    const extMsg = this.convertAgentToExtension(raw);

    if (extMsg) {
      this.dispatchMessage(extMsg);
    }
  }

  /**
   * 分发 ExtensionMessage 到所有注册的处理器
   */
  private dispatchMessage(message: ExtensionMessage): void {
    this.messageHandlers.forEach((handler) => {
      try {
        handler(message);
      } catch (e) {
        console.error('[ServerAPI] 消息处理器异常:', e);
      }
    });
  }

  // ============================================================
  // 内部方法：发送与队列
  // ============================================================

  /**
   * 发送消息，如果未连接则加入队列
   */
  private sendOrQueue(msg: AgentClientMessage): void {
    if (this.isConnected()) {
      this.ws!.send(JSON.stringify(msg));
      console.log('[ServerAPI] 发送消息:', msg.event);
    } else {
      console.log('[ServerAPI] 消息入队（等待连接）:', msg.event);
      this.pendingMessages.push(msg);
      // 尝试连接
      if (!this.connecting) {
        this.connect().catch((err) => {
          console.error('[ServerAPI] 连接失败:', err);
        });
      }
    }
  }

  /**
   * 发送队列中的所有消息
   */
  private flushPendingMessages(): void {
    if (this.pendingMessages.length === 0) return;

    console.log('[ServerAPI] 发送队列中的消息, 数量:', this.pendingMessages.length);
    const messages = [...this.pendingMessages];
    this.pendingMessages = [];

    for (const msg of messages) {
      if (this.isConnected()) {
        this.ws!.send(JSON.stringify(msg));
      }
    }
  }

  // ============================================================
  // 内部方法：重连机制
  // ============================================================

  /**
   * 尝试重连（间隔 5 秒递增：5s, 10s, 15s, ...）
   */
  private attemptReconnect(): void {
    if (this.reconnectAttempts >= this.maxReconnectAttempts) {
      console.error('[ServerAPI] 已达到最大重连次数:', this.maxReconnectAttempts);
      this.broadcastStatus('reconnect_give_up', {
        attempts: this.reconnectAttempts,
        max: this.maxReconnectAttempts,
      });
      return;
    }

    this.reconnectAttempts++;
    // 间隔 5 秒递增：当前尝试次数 × 5 秒
    const delay = this.reconnectAttempts * this.reconnectIntervalMs;

    console.log(`[ServerAPI] 将在 ${delay / 1000}s 后重连 (第 ${this.reconnectAttempts} 次)`);
    this.broadcastStatus('reconnect_countdown', {
      remainingSec: delay / 1000,
      attempt: this.reconnectAttempts,
      max: this.maxReconnectAttempts,
    });

    this.reconnectTimer = window.setTimeout(() => {
      if (this.intentionalClose) {
        return; // 主动断开不重连
      }
      this.connect().catch((err) => {
        console.error('[ServerAPI] 重连失败:', err);
      });
    }, delay);
  }

  /**
   * 清理重连定时器
   */
  private clearReconnectTimer(): void {
    if (this.reconnectTimer != null) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
  }

  // ============================================================
  // 内部方法：状态广播
  // ============================================================

  /**
   * 广播连接状态到所有处理器
   */
  private broadcastStatus(kind: string, data?: any): void {
    if (data !== undefined) {
      console.log(`[ServerAPI] ${kind}`, data);
    } else {
      console.log(`[ServerAPI] ${kind}`);
    }

    const extensionMessage: ExtensionMessage = {
      type: 'server_connection_status',
      id: `server-status-${Date.now()}`,
      data: {
        kind,
        ...data,
      },
    };

    this.messageHandlers.forEach((handler) => {
      try {
        handler(extensionMessage);
      } catch (e) {
        console.error('[ServerAPI] 状态广播处理器异常:', e);
      }
    });
  }
}

// 导出单例
export const serverAPI = ServerAPI.getInstance();
