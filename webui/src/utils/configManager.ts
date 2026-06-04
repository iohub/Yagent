import { vscodeAPI } from './vscode';
import { serverAPI } from './serverApi';

export interface CodeActorConfig {
  selectedModel: string;
  selectedRole: string;
  theme: 'light' | 'dark';
  language: string;
  enableAutoSave: boolean;
  enableNotifications: boolean;
  customModels: Array<{
    name: string;
    apiKey: string;
    endpoint: string;
    model: string;
  }>;
}

export const defaultConfig: CodeActorConfig = {
  selectedModel: 'Claude 3.5 Sonnet',
  selectedRole: 'Developer',
  theme: 'dark',
  language: 'en',
  enableAutoSave: true,
  enableNotifications: true,
  customModels: []
};

class ConfigManager {
  private config: CodeActorConfig = { ...defaultConfig };
  private listeners: Set<(config: CodeActorConfig) => void> = new Set();

  constructor() {
    this.setupMessageListener();
    this.loadConfig();
  }

  private setupMessageListener() {
    // 监听配置响应消息
    vscodeAPI.onMessage((message) => {
      if (message.type === 'configResponse') {
        this.handleConfigResponse(message.config);
      }
    });
  }

  public loadConfig() {
    // 服务器模式使用 WebSocket 连接，不通过 postMessage
    const isServerMode = !vscodeAPI.isVSCodeEnvironment() &&
      (!!window.SERVER_URL || new URLSearchParams(window.location.search).has('server'));

    if (isServerMode) {
      console.log('Server mode - skipping configRequest');
      return;
    }

    // 使用 vscodeAPI 发送配置请求
    vscodeAPI.postMessage({
      type: 'configRequest' as any,
      id: `config-${Date.now()}`,
      payload: {
        timestamp: Date.now()
      }
    });
  }

  public getConfig(): CodeActorConfig {
    return { ...this.config };
  }

  public updateConfig(updates: Partial<CodeActorConfig>) {
    this.config = { ...this.config, ...updates };
    
    // 使用 vscodeAPI 发送配置更新
    vscodeAPI.postMessage({
      type: 'configUpdate' as any,
      id: `config-${Date.now()}`,
      payload: {
        timestamp: Date.now(),
        metadata: {
          config: this.config
        }
      }
    });

    // 通知监听器
    this.listeners.forEach(listener => listener(this.config));
  }

  public onConfigChange(listener: (config: CodeActorConfig) => void) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  public handleConfigResponse(config: CodeActorConfig) {
    this.config = config;
    this.listeners.forEach(listener => listener(this.config));
  }

  public resetToDefault() {
    this.updateConfig(defaultConfig);
  }

  // 快捷方法
  public getSelectedModel(): string {
    return this.config.selectedModel;
  }

  public getSelectedRole(): string {
    return this.config.selectedRole;
  }

  public updateSelectedModel(model: string) {
    this.updateConfig({ selectedModel: model });
  }

  public updateSelectedRole(role: string) {
    this.updateConfig({ selectedRole: role });
  }

  public addCustomModel(model: { name: string; apiKey: string; endpoint: string; model: string } | { name: string; provider: string; modelId: string; apiKey: string; baseUrl: string }) {
    // Convert ModelConfig to internal format if needed
    const internalModel = 'provider' in model ? {
      name: model.name,
      apiKey: model.apiKey,
      endpoint: model.baseUrl,
      model: model.modelId
    } : model;
    
    const customModels = [...this.config.customModels, internalModel];
    this.updateConfig({ customModels });
  }

  public removeCustomModel(modelName: string) {
    const customModels = this.config.customModels.filter(m => m.name !== modelName);
    this.updateConfig({ customModels });
  }
}

export const configManager = new ConfigManager();