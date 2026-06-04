import { ExtensionMessage, WebviewMessage } from '../types';
import { vscodeAPIMock } from './vscode-mock';
import { serverAPI } from './serverApi';

// VSCode API 类型声明
declare global {
  interface Window {
    vscode?: {
      postMessage: (message: WebviewMessage) => void;
      getState: () => any;
      setState: (state: any) => void;
    };
  }
}

export class VSCodeAPI {
  private static instance: VSCodeAPI;
  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();
  private initialized: boolean = false;

  private constructor() {
    this.setupMessageListener();
    this.initialized = true;
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
    // 处理不同类型的消息
    switch (message.type) {
      case 'initialize':
      case 'error':
      case 'configResponse':
      case 'ai_response':
      case 'agentError':
      case 'taskSubmitted':
      case 'tool_call_start':
      case 'tool_call_result':
      case 'tool_call_error':
      case 'task_complete':
        // 通知所有注册的处理器
        this.messageHandlers.forEach(handler => {
          handler(message);
        });
        break;
      default:
        console.warn('Unknown message type:', message.type);
    }
  }

  public postMessage(message: WebviewMessage) {
    if (window.vscode && window.vscode.postMessage) {
      try {
        window.vscode.postMessage(message);
      } catch (error) {
        console.error('Error posting message to VSCode:', error);
      }
    } else {
      console.warn('VSCode API not available - message not sent:', message);
    }
  }

  // 存储的消息
  private storedMessages: any[] = [];

  // 保存消息到本地文件（VSCode 模式下发送到扩展端）
  public saveMessagesToFile(messages: any[]): void {
    this.storedMessages = messages;
    const message = {
      type: 'saveMessages' as const,
      id: `save-${Date.now()}`,
      payload: {
        messages,
        timestamp: Date.now()
      }
    };
    this.postMessage(message);
    console.log('📁 消息已存储，请求保存到文件');
  }

  // 手动下载存储的消息（VSCode 模式下也触发下载）
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

  public onMessage(handler: (message: ExtensionMessage) => void): () => void {
    const id = Math.random().toString(36).substr(2, 9);
    this.messageHandlers.set(id, handler);

    // 返回取消订阅函数
    return () => {
      this.messageHandlers.delete(id);
    };
  }

  public getState() {
    try {
      return window.vscode?.getState();
    } catch (error) {
      console.error('Error getting VSCode state:', error);
      return null;
    }
  }

  public setState(state: any) {
    if (window.vscode) {
      try {
        window.vscode.setState(state);
      } catch (error) {
        console.error('Error setting VSCode state:', error);
      }
    }
  }

  public isVSCodeEnvironment(): boolean {
    return !!window.vscode;
  }
}

// 根据环境选择使用真实API还是模拟API
// 在开发环境中，如果没有VSCode API，使用模拟API或服务器API
const isVSCodeEnvironment = !!window.vscode;
const isServerMode = !!window.SERVER_URL || new URLSearchParams(window.location.search).has('server');
// 优先使用 server mode（WebSocket），如果可用的话
export const vscodeAPI = isServerMode
  ? serverAPI
  : isVSCodeEnvironment
    ? VSCodeAPI.getInstance()
    : vscodeAPIMock;
