import { ExtensionMessage } from '../types';
import { vscodeAPIMock } from './vscode-mock';
import { serverAPI, ServerAPI } from './serverApi';

// VSCode API 类型声明
declare global {
  interface Window {
    vscode?: {
      postMessage: (message: any) => void;
      getState: () => any;
      setState: (state: any) => void;
    };
  }
}

// VSCode 特有消息类型（非聊天类）
const VSCodeSpecificTypes = [
  'themeChange',
  'configResponse',
  'codeLensAction',
  'selectionCodeLensAction',
];

/**
 * VSCodeAPI - 仅用于与 VSCode 扩展通信
 * 在混合模式下，聊天消息通过 WebSocket 传输，VSCodeAPI 仅处理 VSCode 特有消息
 */
export class VSCodeAPI {
  private static instance: VSCodeAPI;
  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();

  private constructor() {
    this.setupMessageListener();
  }

  public static getInstance(): VSCodeAPI {
    if (!VSCodeAPI.instance) {
      VSCodeAPI.instance = new VSCodeAPI();
    }
    return VSCodeAPI.instance;
  }

  private setupMessageListener() {
    window.addEventListener('message', (event) => {
      const message = event.data as ExtensionMessage;
      this.handleMessage(message);
    });
  }

  private handleMessage(message: ExtensionMessage) {
    // 聚焦 VSCode 特有消息，其他消息也透传
    if (VSCodeSpecificTypes.includes(message.type)) {
      console.log('[VSCodeAPI] 收到 VSCode 特有消息:', message.type);
    }
    this.messageHandlers.forEach(handler => {
      try {
        handler(message);
      } catch (e) {
        console.error('[VSCodeAPI] 消息处理器异常:', e);
      }
    });
  }

  public postMessage(message: any) {
    if (window.vscode && window.vscode.postMessage) {
      try {
        window.vscode.postMessage(message);
      } catch (error) {
        console.error('[VSCodeAPI] postMessage 失败:', error);
      }
    } else {
      console.warn('[VSCodeAPI] VSCode API 不可用，消息未发送:', message.type);
    }
  }

  public onMessage(handler: (message: ExtensionMessage) => void): () => void {
    const id = Math.random().toString(36).substr(2, 9);
    this.messageHandlers.set(id, handler);
    return () => {
      this.messageHandlers.delete(id);
    };
  }

  public getState() {
    try {
      return window.vscode?.getState();
    } catch (error) {
      console.error('[VSCodeAPI] getState 失败:', error);
      return null;
    }
  }

  public setState(state: any) {
    if (window.vscode) {
      try {
        window.vscode.setState(state);
      } catch (error) {
        console.error('[VSCodeAPI] setState 失败:', error);
      }
    }
  }

  public isVSCodeEnvironment(): boolean {
    return !!window.vscode;
  }
}

/**
 * HybridAPI - VSCode 混合模式
 * 同时使用 WebSocket（serverAPI）和 VSCode postMessage（VSCodeAPI）：
 * - 聊天/任务消息（chat, agent, submitTask, terminateTask, clearChat, saveMessages）→ WebSocket
 * - 配置消息（configRequest, configUpdate）→ VSCode postMessage
 * - 接收消息：同时监听 WebSocket 和 VSCode postMessage，合并处理
 */
class HybridAPI {
  private serverAPI: ServerAPI;
  private vscodeAPI: VSCodeAPI;
  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();

  constructor(serverAPI: ServerAPI, vscodeAPI: VSCodeAPI) {
    this.serverAPI = serverAPI;
    this.vscodeAPI = vscodeAPI;
  }

  /**
   * 根据消息类型路由到正确的通道
   */
  public postMessage(message: any): void {
    const chatTypes = ['chat', 'agent', 'submitTask', 'terminateTask', 'clearChat', 'saveMessages'];
    const msgType = message.type as string;

    if (chatTypes.includes(msgType)) {
      // 聊天/任务相关消息 → WebSocket
      console.log('[HybridAPI] 聊天消息通过 WebSocket 发送:', msgType);
      this.serverAPI.postMessage(message);
    } else if (msgType === 'configRequest' || msgType === 'configUpdate') {
      // 配置相关消息 → VSCode postMessage
      console.log('[HybridAPI] 配置消息通过 VSCode postMessage 发送:', msgType);
      this.vscodeAPI.postMessage(message);
    } else {
      console.warn('[HybridAPI] 未知消息类型，默认通过 WebSocket 发送:', msgType);
      this.serverAPI.postMessage(message);
    }
  }

  /**
   * 同时监听 WebSocket 和 VSCode postMessage，合并处理
   */
  public onMessage(handler: (message: ExtensionMessage) => void): () => void {
    const id = Math.random().toString(36).substr(2, 9);
    this.messageHandlers.set(id, handler);

    // 订阅 serverAPI（WebSocket）
    const unsubServer = this.serverAPI.onMessage((msg) => {
      this.dispatchToHandlers(msg);
    });

    // 订阅 VSCodeAPI（postMessage）
    const unsubVSCode = this.vscodeAPI.onMessage((msg) => {
      this.dispatchToHandlers(msg);
    });

    // 返回取消订阅函数（同时取消两个通道的订阅）
    return () => {
      this.messageHandlers.delete(id);
      unsubServer();
      unsubVSCode();
    };
  }

  private dispatchToHandlers(message: ExtensionMessage): void {
    this.messageHandlers.forEach(handler => {
      try {
        handler(message);
      } catch (e) {
        console.error('[HybridAPI] 消息处理器异常:', e);
      }
    });
  }

  public isVSCodeEnvironment(): boolean {
    return true;
  }

  public isConnected(): boolean {
    return this.serverAPI.isConnected();
  }

  public getState(): any {
    return this.vscodeAPI.getState();
  }

  public setState(state: any): void {
    this.vscodeAPI.setState(state);
  }

  public saveMessagesToFile(messages: any[]): void {
    this.serverAPI.saveMessagesToFile(messages);
  }

  public downloadStoredMessages(filePath?: string): void {
    this.serverAPI.downloadStoredMessages(filePath);
  }
}

// 根据环境选择使用
// 优先级：VSCode 混合模式 > 纯服务器模式 > 浏览器预览
const isVSCodeEnvironment = !!window.vscode;
const isServerMode = !!window.SERVER_URL || new URLSearchParams(window.location.search).has('server');

export const vscodeAPI = isVSCodeEnvironment
  ? new HybridAPI(serverAPI, VSCodeAPI.getInstance())  // VSCode 混合模式（优先）
  : isServerMode
    ? serverAPI                           // 纯服务器模式（WebSocket 直连）
    : vscodeAPIMock;                      // 浏览器预览
