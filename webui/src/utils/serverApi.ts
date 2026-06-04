import { ExtensionMessage, WebviewMessage } from '../types';

// Server API 类型声明
declare global {
  interface Window {
    SERVER_URL?: string;
  }
}

export interface ServerMessage {
  type: 'welcome' | 'message' | 'tool_use' | 'tool_result' | 'error' | 'pong' | 'done' | 'thinking' | 'status';
  sessionId?: string;
  cwd?: string;
  data?: any;
}

export interface ClientMessage {
  type: 'prompt' | 'interrupt' | 'ping';
  content?: string;
}

export class ServerAPI {
  private static instance: ServerAPI;
  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();
  private ws: WebSocket | null = null;
  private sessionId: string | null = null;
  private wsUrl: string;  // WebSocket URL (ws:// or wss://)
  private httpUrl: string;  // HTTP URL for REST API (http:// or https://)
  private initialized: boolean = false;
  private reconnectAttempts: number = 0;
  private maxReconnectAttempts: number = 10;
  private connecting: boolean = false;

  private reconnectInterval: number | null = null;
  private reconnectCountdownSec: number = 0;
  private reconnectWaitSec: number = 30;
  private sessionTimeoutMs: number = 10_000;
  private wsOpenTimeoutMs: number = 10_000;
  private lastError: any = null;

  private constructor() {
    // 优先从 URL 参数获取服务器地址，其次从 window.SERVER_URL，默认连接到本地 3000 端口
    const urlParams = new URLSearchParams(window.location.search);
    const urlFromParams = urlParams.get('url');
    const baseUrl = urlFromParams || window.SERVER_URL || 'ws://localhost:3000';

    // 转换 ws:// <-> http://
    if (baseUrl.startsWith('ws://')) {
      this.wsUrl = baseUrl;
      this.httpUrl = baseUrl.replace('ws://', 'http://');
    } else if (baseUrl.startsWith('wss://')) {
      this.wsUrl = baseUrl;
      this.httpUrl = baseUrl.replace('wss://', 'https://');
    } else {
      // 没有协议前缀，默认添加 ws://
      this.wsUrl = 'ws://' + baseUrl;
      this.httpUrl = 'http://' + baseUrl;
    }

    console.log('ServerAPI initialized:');
    console.log('  WebSocket URL:', this.wsUrl);
    console.log('  HTTP URL:', this.httpUrl);
  }

  private broadcastStatus(kind: string, data?: any): void {
    // console + UI 都要
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

    this.messageHandlers.forEach((handler) => handler(extensionMessage));
  }

  private clearReconnectCountdown(): void {
    if (this.reconnectInterval != null) {
      clearInterval(this.reconnectInterval);
      this.reconnectInterval = null;
    }
    this.reconnectCountdownSec = 0;
  }

  public static getInstance(): ServerAPI {
    if (!ServerAPI.instance) {
      ServerAPI.instance = new ServerAPI();
    }
    return ServerAPI.instance;
  }

  public setServerUrl(url: string): void {
    this.clearReconnectCountdown();

    // 转换 ws:// <-> http://
    if (url.startsWith('ws://')) {
      this.wsUrl = url;
      this.httpUrl = url.replace('ws://', 'http://');
    } else if (url.startsWith('wss://')) {
      this.wsUrl = url;
      this.httpUrl = url.replace('wss://', 'https://');
    } else {
      this.wsUrl = 'ws://' + url;
      this.httpUrl = 'http://' + url;
    }

    // 避免继续沿用旧连接
    if (this.ws) {
      try { this.ws.close(); } catch {}
      this.ws = null;
    }
    this.sessionId = null;
    this.initialized = false;

    this.broadcastStatus('server url changed', { wsUrl: this.wsUrl, httpUrl: this.httpUrl });

  }

  public isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }

  private async createSession(): Promise<{ sessionId: string; cwd: string }> {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.sessionTimeoutMs);

    try {
      console.log('Creating session via:', `${this.httpUrl}/api/sessions`);
      const response = await fetch(`${this.httpUrl}/api/sessions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        signal: controller.signal,
      });
      if (!response.ok) {
        throw new Error(`Failed to create session: ${response.statusText}`);
      }
      const data = await response.json();
      console.log('Session created:', data);
      return data;
    } catch (err: any) {
      const msg = err?.name === 'AbortError' ? `createSession timeout (${this.sessionTimeoutMs}ms)` : err;
      this.lastError = msg;
      throw msg instanceof Error ? msg : new Error(String(msg));
    } finally {
      clearTimeout(timeoutId);
    }
  }

  private connect(): Promise<void> {
    if (this.connecting) {
      console.log('Already connecting...');
      return Promise.resolve();
    }
    if (this.isConnected()) {
      console.log('Already connected');
      return Promise.resolve();
    }

    this.connecting = true;
    this.broadcastStatus('connecting');

    return new Promise((resolve, reject) => {
      // 首先创建 session
      this.createSession()
        .then((sessionInfo) => {
          this.sessionId = sessionInfo.sessionId;
          const wsTargetUrl = `${this.wsUrl}/session/${this.sessionId}`;
          console.log('Connecting to WebSocket:', wsTargetUrl);

          this.ws = new WebSocket(wsTargetUrl);

          let wsOpenTimer: number | null = null;
          wsOpenTimer = window.setTimeout(() => {
            this.lastError = `WebSocket open timeout (${this.wsOpenTimeoutMs}ms)`;
            this.broadcastStatus('websocket_open_timeout', { timeoutMs: this.wsOpenTimeoutMs });
            try { this.ws?.close(); } catch {}
            this.connecting = false;
            reject(new Error(this.lastError));
          }, this.wsOpenTimeoutMs);

          const clearWsOpenTimer = () => {
            if (wsOpenTimer != null) {
              clearTimeout(wsOpenTimer);
              wsOpenTimer = null;
            }
          };

          this.ws.onopen = () => {
            clearWsOpenTimer();
            console.log('WebSocket connected!');
            this.initialized = true;
            this.connecting = false;
            this.reconnectAttempts = 0;
            this.lastError = null;
            this.clearReconnectCountdown();
            this.broadcastStatus('connected');
            resolve();
          };

          this.ws.onmessage = (event) => {
            try {
              const message = JSON.parse(event.data) as ServerMessage;
              console.log('Received:', message.type);
              this.handleServerMessage(message);
            } catch (e) {
              console.error('Error parsing server message:', e);
            }
          };

          this.ws.onerror = (error) => {
            clearWsOpenTimer();
            console.error('WebSocket error:', error);
            this.lastError = error;
            this.connecting = false;
          };

          this.ws.onclose = (event) => {
            clearWsOpenTimer();
            console.log('WebSocket closed:', event.code, event.reason);
            this.lastError = { code: event.code, reason: event.reason };
            this.initialized = false;
            this.connecting = false;
            if (this.reconnectAttempts < this.maxReconnectAttempts) {
              this.attemptReconnect();
            } else {
              this.broadcastStatus('reconnect_give_up', { attempts: this.reconnectAttempts, max: this.maxReconnectAttempts, lastError: this.lastError });
            }
          };
        })
        .catch((error) => {
          console.error('Failed to create session:', error);
          this.lastError = error;
          this.connecting = false;
          this.startReconnectCountdown();
          reject(error);
        });
    });
  }

  private startReconnectCountdown(): void {
    if (this.reconnectAttempts >= this.maxReconnectAttempts) {
      this.broadcastStatus('reconnect_give_up', { attempts: this.reconnectAttempts, max: this.maxReconnectAttempts, lastError: this.lastError });
      return;
    }

    this.clearReconnectCountdown();

    this.reconnectAttempts++;
    this.reconnectCountdownSec = this.reconnectWaitSec;

    this.broadcastStatus('reconnect_countdown', {
      remainingSec: this.reconnectCountdownSec,
      attempt: this.reconnectAttempts,
      max: this.maxReconnectAttempts,
      lastError: this.lastError,
    });

    this.reconnectInterval = window.setInterval(() => {
      this.reconnectCountdownSec -= 1;
      if (this.reconnectCountdownSec <= 0) {
        this.clearReconnectCountdown();
        this.connect().catch((err) => {
          this.lastError = err;
        });
        return;
      }

      this.broadcastStatus('reconnect_countdown', {
        remainingSec: this.reconnectCountdownSec,
        attempt: this.reconnectAttempts,
        max: this.maxReconnectAttempts,
        lastError: this.lastError,
      });
    }, 1000);
  }

  private attemptReconnect(): void {
    this.startReconnectCountdown();
  }

  private handleServerMessage(message: ServerMessage): void {
    // 将服务器消息转换为 ExtensionMessage 格式
    let extensionMessage: ExtensionMessage;

    switch (message.type) {
      case 'welcome':
        console.log(`Connected to session ${message.sessionId} in ${message.cwd}`);
        return;

      case 'message':
        extensionMessage = {
          type: 'ai_response',
          id: `msg-${Date.now()}`,
          data: message.data,
          from: 'assistant',
        };
        break;

      case 'tool_use':
        extensionMessage = {
          type: 'tool_call_start',
          id: message.data?.id || `tool-${Date.now()}`,
          data: {
            tool_name: message.data?.tool,
            tool_call_id: message.data?.id,
            task_id: this.sessionId,
          },
          from: 'assistant',
        };
        break;

      case 'tool_result':
        extensionMessage = {
          type: 'tool_call_result',
          id: `tool-result-${Date.now()}`,
          data: {
            tool_name: message.data?.tool,
            tool_call_id: message.data?.toolUseId,
            result: message.data?.output,
            error: message.data?.error,
            task_id: this.sessionId,
          },
          from: 'assistant',
        };
        break;

      case 'error':
        extensionMessage = {
          type: 'error',
          id: `error-${Date.now()}`,
          error: message.data,
        };
        break;

      case 'thinking':
        extensionMessage = {
          type: 'ai_response',
          id: `thinking-${Date.now()}`,
          data: `🤔 ${message.data}`,
          from: 'assistant',
        };
        break;

      case 'status':
        if (message.data === 'idle' || message.data === 'interrupted') {
          extensionMessage = {
            type: 'conversation_end',
            id: `status-${Date.now()}`,
            data: { result: message.data },
          };
        } else {
          return; // 不转发状态消息
        }
        break;

      case 'done':
        extensionMessage = {
          type: 'conversation_end',
          id: `done-${Date.now()}`,
          data: { result: 'completed' },
        };
        break;

      case 'pong':
        return; // 不转发 pong 消息

      default:
        console.warn('Unknown server message type:', message.type);
        return;
    }

    // 通知所有注册的处理器
    this.messageHandlers.forEach((handler) => {
      handler(extensionMessage);
    });
  }

  public async ensureConnected(): Promise<void> {
    if (!this.isConnected() && !this.connecting) {
      await this.connect();
    }
  }

  public postMessage(message: WebviewMessage): void {
    if (!this.sessionId) {
      console.error('No session ID - not connected to server');
      return;
    }

    this.ensureConnected().then(() => {
      if (this.ws?.readyState === WebSocket.OPEN) {
        const clientMsg: ClientMessage = {
          type: 'prompt',
          content: message.payload.text,
        };

        // 根据消息类型处理
        if (message.type === 'submitTask' || message.type === 'agent') {
          clientMsg.content = message.payload.text || message.payload.taskDesc;
        } else if (message.type === 'terminateTask') {
          clientMsg.type = 'interrupt';
          clientMsg.content = undefined;
        } else if (message.type === 'clearChat') {
          // 清空聊天不需要特殊处理，服务器会维护会话状态
          return;
        }

        this.ws.send(JSON.stringify(clientMsg));
      }
    }).catch(console.error);
  }

  public onMessage(handler: (message: ExtensionMessage) => void): () => void {
    const id = Math.random().toString(36).substr(2, 9);
    this.messageHandlers.set(id, handler);

    // 立即尝试连接
    if (!this.isConnected() && !this.connecting) {
      this.connect().catch((err) => {
        console.error('Connection failed:', err);
      });
    }

    // 返回取消订阅函数
    return () => {
      this.messageHandlers.delete(id);
    };
  }

  public getState(): any {
    return {
      sessionId: this.sessionId,
      connected: this.isConnected(),
      connecting: this.connecting,
      wsUrl: this.wsUrl,
    };
  }

  public setState(_state: any): void {
    // 服务器模式不需要本地状态管理
  }

  public isVSCodeEnvironment(): boolean {
    // 在服务器模式下，返回 false 以使用 ServerAPI
    return false;
  }

  // 存储的消息
  private storedMessages: any[] = [];

  // 保存消息到本地文件（服务器模式下存储消息）
  public saveMessagesToFile(messages: any[]): void {
    this.storedMessages = messages;
    console.log('📁 [Server] 消息已存储，当前数量:', messages.length);
  }

  // 手动下载存储的消息
  public downloadStoredMessages(filePath?: string): void {
    if (this.storedMessages.length === 0) {
      console.warn('⚠️ 没有可下载的消息');
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
      console.log('✅ 消息已下载保存到:', fileName);
    } catch (error) {
      console.error('❌ 下载消息失败:', error);
    }
  }

  public disconnect(): void {
    this.clearReconnectCountdown();
    this.reconnectAttempts = 0;
    this.connecting = false;

    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.sessionId = null;
    this.initialized = false;
    this.broadcastStatus('disconnected');
  }
}

// 导出单例
export const serverAPI = ServerAPI.getInstance();
