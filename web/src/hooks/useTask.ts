import { useState, useEffect, useRef, useCallback } from 'react';
import type { Message, Task } from '../types';
import { startTask as apiStartTask, getWebSocketUrl, getTaskStatus } from '../api/client';

export function useTask() {
  const [taskId, setTaskId] = useState<string | null>(null);
  const [status, setStatus] = useState<Task['status']>('finished'); // Default to finished so we can start new
  const [messages, setMessages] = useState<Message[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);

  const startTask = async (projectDir: string, taskDesc: string) => {
    setIsLoading(true);
    setError(null);
    try {
      const { task_id } = await apiStartTask(projectDir, taskDesc);
      setTaskId(task_id);
      setStatus('running');
      setMessages([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start task');
    } finally {
      setIsLoading(false);
    }
  };

  const connectWs = useCallback(() => {
    if (!taskId) return;
    if (wsRef.current?.readyState === WebSocket.OPEN) return;

    const wsUrl = getWebSocketUrl();
    const ws = new WebSocket(wsUrl);
    wsRef.current = ws;

    ws.onopen = () => {
      console.log('WS connected');
    };

    ws.onmessage = (event) => {
      try {
        const msg = JSON.parse(event.data);
        // Handle different message types
        if (msg.type === 'chat_message' && msg.event === 'ai_response' && msg.data?.content) {
            setMessages(prev => [...prev, { type: 'assistant', content: msg.data.content }]);
        } else if (msg.type === 'realtime' && msg.data?.task_id === taskId) {
            // Realtime updates (e.g. tool execution)
             setMessages(prev => [...prev, { type: 'tool', content: `[${msg.event}] ${msg.data.content}` }]);
        }
      } catch (e) {
        console.error('Failed to parse WS message', e);
      }
    };

    ws.onclose = () => {
        console.log('WS closed');
    };
    
    return () => {
        ws.close();
    };
  }, [taskId]);

  // Polling for status and memory as backup / for initial state
  useEffect(() => {
      if (!taskId || status !== 'running') return;

      const pollInterval = setInterval(async () => {
          try {
              const taskStatus = await getTaskStatus(taskId);
              setStatus(taskStatus.status);
              
              if (taskStatus.status === 'finished' || taskStatus.status === 'failed') {
                  clearInterval(pollInterval);
              }
              
              // Also sync memory to ensure we didn't miss anything
              // const memory = await getMemory(taskId);
              // Merge logic could be complex, for now just replace if significantly different length?
              // Or better: just rely on WS for live updates and use memory for initial load if we were to support resuming.
              // For this simple app, we might just trust WS + local state, 
              // but if we want to be robust we should de-duplicate.
              // Let's keep it simple: Use WS for live, but if we refresh, we loose state unless we load from memory.
              // TODO: Implement proper sync.
          } catch (e) {
              console.error('Poll failed', e);
          }
      }, 2000);

      return () => clearInterval(pollInterval);
  }, [taskId, status]);

  useEffect(() => {
      if (taskId) {
          connectWs();
      }
      return () => {
          wsRef.current?.close();
      }
  }, [taskId, connectWs]);

  return {
    taskId,
    status,
    messages,
    error,
    isLoading,
    startTask,
  };
}
