export interface Task {
  task_id: string;
  status: 'running' | 'finished' | 'failed';
  result?: string;
  error?: string;
  progress?: number;
  created_at: string;
  updated_at: string;
}

export interface Message {
  type: 'system' | 'human' | 'assistant' | 'tool';
  content: string;
  event?: string; // For websocket messages
  from?: string;
  tool_calls?: any[]; // Added for memory debugger
  tool_call_id?: string; // Added for memory debugger
  timestamp?: string; // Added for memory debugger
}

export interface TaskHistoryItem {
  task_id: string;
  title: string;
  created_at: string;
  updated_at: string;
  message_count: number;
}

export interface LoadTaskResponse {
  task_id: string;
  message: string;
}

export interface StartTaskResponse {
  task_id: string;
}

export interface MemoryResponse {
  messages: Message[];
  size: number;
  max_size: number;
}
