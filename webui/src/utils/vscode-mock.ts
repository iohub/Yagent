import { ExtensionMessage, WebviewMessage } from '../types';

/**
 * VSCode API 模拟器 - 用于在浏览器环境中运行
 * 提供与真实VSCode API相同的接口，但使用模拟数据
 */
export class VSCodeAPIMock {
  private static instance: VSCodeAPIMock;
  private messageHandlers: Map<string, (message: ExtensionMessage) => void> = new Map();
  private mockState: any = {};

  private constructor() {
    this.setupMessageListener();
    console.log('VSCode API Mock initialized - running in browser preview mode');
  }

  public static getInstance(): VSCodeAPIMock {
    if (!VSCodeAPIMock.instance) {
      VSCodeAPIMock.instance = new VSCodeAPIMock();
    }
    return VSCodeAPIMock.instance;
  }

  private setupMessageListener() {
    // 在浏览器环境中，我们可以使用自定义事件或定时器来模拟消息
    window.addEventListener('message', (event) => {
      const message = event.data as ExtensionMessage;
      this.handleMessage(message);
    });
  }

  private handleMessage(message: ExtensionMessage) {
    // 处理不同类型的消息
    switch (message.type) {
      case 'initialize':
      case 'chatResponse':
      case 'agentStatus':
      case 'agentResponse':
      case 'codeExplanation':
      case 'error':
      case 'suggestion':
      case 'explainCode':
      case 'configResponse':
      // 添加实时消息类型支持
      case 'taskUpdate':
      case 'aiResponse':
      case 'taskComplete':
      case 'taskFailed':
      case 'agentError':
      case 'taskSubmitted':
      // 添加tool_call相关消息类型支持
      case 'toolCall':
      case 'toolCallStart':
      case 'toolCallResult':
      case 'toolCallError':
      // 添加下划线格式的消息类型支持
      case 'tool_call_start':
      case 'tool_call_result':
      case 'tool_call_error':
        // 通知所有注册的处理器
        this.messageHandlers.forEach(handler => {
          handler(message);
        });
        break;
      default:
        console.warn('Unknown message type:', message.type);
    }
  }

  /**
   * 模拟发送消息到扩展
   * 在浏览器环境中，我们会生成模拟响应
   */
  public postMessage(message: WebviewMessage) {
    console.log('Mock VSCode API - Sending message:', message);
    
    // 模拟异步响应
    setTimeout(() => {
      this.generateMockResponse(message);
    }, 1000 + Math.random() * 2000); // 1-3秒延迟
  }

  /**
   * 生成模拟响应
   */
  private generateMockResponse(message: WebviewMessage) {
    try {
      const responses: { [key: string]: () => ExtensionMessage } = {
        'chat': () => ({
          type: 'chatResponse',
          id: `mock-${Date.now()}`,
          response: this.generateMockChatResponse(message.payload?.text || '')
        }),
        'agent': () => ({
          type: 'agentResponse',
          id: `mock-${Date.now()}`,
          response: this.generateMockAgentResponse(message.payload?.text || '')
        }),
        'code': () => ({
          type: 'codeExplanation',
          id: `mock-${Date.now()}`,
          explanation: this.generateMockCodeExplanation()
        }),
        'configRequest': () => ({
          type: 'configResponse',
          id: `mock-${Date.now()}`,
          config: this.generateMockConfig()
        }),
        'configUpdate': () => ({
          type: 'configResponse',
          id: `mock-${Date.now()}`,
          config: (message as any).metadata?.config || this.generateMockConfig()
        }),
        // 添加submitTask的模拟响应
        'submitTask': () => {
          const taskId = `task-${Date.now()}`;
          // 先发送任务提交确认
          setTimeout(() => {
            this.handleMessage({
              type: 'taskSubmitted',
              id: `submitted-${Date.now()}`,
              taskId: taskId
            });
          }, 500);

          // 模拟任务更新消息
          setTimeout(() => {
            this.handleMessage({
              type: 'taskUpdate',
              id: `update-${Date.now()}`,
              data: { content: '正在分析项目结构...' }
            });
          }, 2000);

          setTimeout(() => {
            this.handleMessage({
              type: 'aiResponse',
              id: `ai-${Date.now()}`,
              data: { content: '我正在处理您的任务，请稍等...' }
            });
          }, 4000);

          setTimeout(() => {
            this.handleMessage({
              type: 'taskComplete',
              id: `complete-${Date.now()}`,
              data: { result: '任务执行完成！' }
            });
          }, 8000);

          return {
            type: 'taskSubmitted',
            id: `mock-${Date.now()}`,
            taskId: taskId
          };
        }
      };

      const responseGenerator = responses[message.type as string] || responses['chat'];
      const mockResponse = responseGenerator();
      
      // 触发消息处理
      this.handleMessage(mockResponse);
    } catch (error) {
      console.error('Error generating mock response:', error);
      // 生成一个错误响应
      this.handleMessage({
        type: 'error',
        id: `error-${Date.now()}`,
        error: 'Failed to generate mock response'
      });
    }
  }

  /**
   * 生成模拟聊天响应
   */
  private generateMockChatResponse(text: string): string {
    const responses = [
      `I understand you're asking about: "${text}". In a VSCode extension context, I would help you with coding tasks, but since we're in browser preview mode, here's a general response.`,
      `That's an interesting question about "${text}". Let me provide some insights...`,
      `I can help you with "${text}". Here's what I think...`,
      `Regarding your question about "${text}", here are my thoughts...`,
      `Let me analyze "${text}" and provide you with helpful information.`
    ];
    
    return responses[Math.floor(Math.random() * responses.length)];
  }

  /**
   * 生成模拟Agent响应
   */
  private generateMockAgentResponse(text: string): string {
    const responses = [
      `🤖 **Agent Mode**: I would normally perform the task: "${text}" in your VSCode workspace. In browser mode, here's a simulated response.`,
      `🔧 **Agent Response**: Based on your request "${text}", I would analyze your code and provide detailed assistance.`,
      `⚡ **Agent Action**: I understand you want me to "${text}". In VSCode, I would execute this task with full workspace context.`,
      `🎯 **Agent Processing**: Your request "${text}" would trigger intelligent code analysis and modifications in the actual extension.`
    ];
    
    return responses[Math.floor(Math.random() * responses.length)];
  }

  /**
   * 生成模拟代码解释
   */
  private generateMockCodeExplanation(): string {
    return `\`\`\`javascript
// Mock Code Explanation
function example() {
  console.log('This is a simulated code explanation');
  return {
    message: 'In VSCode extension mode, I would analyze your actual code',
    features: ['Syntax highlighting', 'Error detection', 'Optimization suggestions'],
    note: 'Browser preview shows this mock response'
  };
}
\`\`\`

**Explanation**: This is a simulated code explanation. In the actual VSCode extension, I would analyze your selected code and provide detailed insights, error detection, and improvement suggestions.`;
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
    return this.mockState;
  }

  public setState(state: any) {
    this.mockState = state;
  }

  public isVSCodeEnvironment(): boolean {
    return false; // 明确返回false表示这是模拟环境
  }

  // 存储的消息
  private storedMessages: any[] = [];

  // 保存消息到本地文件（浏览器模式下存储消息）
  public saveMessagesToFile(messages: any[]): void {
    this.storedMessages = messages;
    console.log('📁 [Mock] 消息已存储，当前数量:', messages.length);
  }

  // 手动下载存储的消息
  public downloadStoredMessages(filePath?: string): void {
    const fileName = filePath || 'webui-messages.json';

    if (this.storedMessages.length === 0) {
      console.warn('⚠️ 没有可下载的消息');
      return;
    }

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

  /**
   * 生成模拟配置
   */
  private generateMockConfig(): any {
    return {
      selectedModel: 'Claude 3.5 Sonnet',
      selectedRole: 'Developer',
      theme: 'dark',
      language: 'en',
      enableAutoSave: true,
      enableNotifications: true,
      customModels: [
        {
          name: 'Custom GPT-4',
          apiKey: 'mock-api-key',
          endpoint: 'https://api.openai.com/v1',
          model: 'gpt-4',
          provider: 'OpenAI',
          contextLength: 128000,
          isCustom: true
        }
      ]
    };
  }
}

export const vscodeAPIMock = VSCodeAPIMock.getInstance();