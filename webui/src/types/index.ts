export interface Message {
  id: string;
  text?: string; // Optional for compatibility
  sender?: string; // 'user' | 'assistant' | 'system' | 'Conductor' | 'Coding-Agent'
  timestamp: number;
  type?: string;
  data?: any;
  metadata?: {
    language?: string;
    fileName?: string;
    lineNumber?: number;
    // 文件变更相关
    fileChanges?: FileChange[];
    changeStats?: {
      additions: number;
      deletions: number;
    };
    // 任务完成相关
    taskStatus?: 'completed' | 'failed' | 'in_progress';
    // 变更确认相关
    actions?: MessageAction[];
    // 文件审查相关
    reviewStats?: {
      totalFiles: number;
      reviewedFiles: number;
    };
    // tool_call相关
    toolName?: string;
    toolCallId?: string;
    arguments?: any;
    result?: any;
    error?: string;
    // LLM错误相关
    taskId?: string;
    canRetry?: boolean;
    [key: string]: any;
  };
}

export interface FileChange {
  fileName: string;
  additions: number;
  deletions: number;
  status: 'added' | 'modified' | 'deleted' | 'renamed';
  preview?: string;
}

export interface MessageAction {
  id: string;
  label: string;
  type: 'primary' | 'secondary' | 'danger';
  action: () => void;
}

export interface ChatState {
  messages: Message[];
  isProcessing: boolean;
  currentMode: 'chat' | 'agent';
  theme: 'light' | 'dark';
}

export interface VSCodeTheme {
  kind: 'light' | 'dark' | 'highContrast' | 'highContrastLight';
  colors: {
    [key: string]: string;
  };
}

export interface ExtensionMessage {
  type: string;
  id?: string;
  payload?: any;
  [key: string]: any;
}

export interface WebviewMessage {
  type: 'chat' | 'agent' | 'code' | 'submitTask' | 'terminateTask' | 'clearChat' | 'saveMessages';
  id: string;
  payload: {
    text?: string;
    action?: string;
    code?: string;
    mode?: string;
    projectDir?: string;
    taskDesc?: string;
    timestamp: number;
    messages?: Message[];
    filePath?: string;
    [key: string]: any;
  };
}
