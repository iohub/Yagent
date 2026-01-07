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
}

export interface StartTaskResponse {
  task_id: string;
}

export interface MemoryResponse {
  messages: Message[];
  size: number;
  max_size: number;
}
