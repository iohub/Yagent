import type { StartTaskResponse, Task, MemoryResponse, TaskHistoryItem, LoadTaskResponse } from '../types';

const API_BASE_URL = 'http://localhost:9080';

export async function listHistory(): Promise<TaskHistoryItem[]> {
  const response = await fetch(`${API_BASE_URL}/api/history`);
  
  if (!response.ok) {
    throw new Error(`Failed to list history: ${response.statusText}`);
  }

  return response.json();
}

export async function loadTask(taskId: string, projectDir?: string): Promise<LoadTaskResponse> {
  const response = await fetch(`${API_BASE_URL}/api/load_task`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      task_id: taskId,
      project_dir: projectDir,
    }),
  });

  if (!response.ok) {
    throw new Error(`Failed to load task: ${response.statusText}`);
  }

  return response.json();
}

export async function startTask(projectDir: string, taskDesc: string): Promise<StartTaskResponse> {
  const response = await fetch(`${API_BASE_URL}/api/start_task`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      project_dir: projectDir,
      task_desc: taskDesc,
    }),
  });

  if (!response.ok) {
    throw new Error(`Failed to start task: ${response.statusText}`);
  }

  return response.json();
}

export async function getTaskStatus(taskId: string): Promise<Task> {
  const response = await fetch(`${API_BASE_URL}/api/task_status?task_id=${encodeURIComponent(taskId)}`);
  
  if (!response.ok) {
    throw new Error(`Failed to get task status: ${response.statusText}`);
  }

  return response.json();
}

export async function getMemory(taskId: string): Promise<MemoryResponse> {
  const response = await fetch(`${API_BASE_URL}/api/memory?task_id=${encodeURIComponent(taskId)}`);
  
  if (!response.ok) {
    throw new Error(`Failed to get memory: ${response.statusText}`);
  }

  return response.json();
}

export async function cancelTask(taskId: string): Promise<void> {
  const response = await fetch(`${API_BASE_URL}/api/cancel_task`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      task_id: taskId,
    }),
  });

  if (!response.ok) {
    throw new Error(`Failed to cancel task: ${response.statusText}`);
  }
}

export function getWebSocketUrl(): string {
  return API_BASE_URL.replace(/^http/, 'ws') + '/ws';
}
