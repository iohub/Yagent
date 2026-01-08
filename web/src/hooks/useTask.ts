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
        console.log('WS Message:', msg); // Debug log

        // Validate Task ID if present in message (top level)
        if (msg.task_id && msg.task_id !== taskId) return;

        const eventType = msg.type || msg.event;
        const data = msg.data;
        const from = msg.from; // Extract sender info

        if (eventType === 'ai_response') {
            setMessages(prev => [...prev, { type: 'assistant', content: String(data), from }]);
        } 
        else if (['tool_call', 'tool_call_result'].includes(eventType)) {
            let content = data;
            if (typeof data === 'object' && data !== null) {
                const toolName = data.tool_name || 'unknown';
                content = `[${eventType}] ${toolName}\n${JSON.stringify(data, null, 2)}`;
            } else {
                content = `[${eventType}] ${String(data)}`;
            }
            setMessages(prev => [...prev, { type: 'tool', content: String(content), event: eventType, from }]);
        }
        else if (['status_update', 'ai_stream_start', 'ai_stream_end', 'ai_chunk'].includes(eventType)) {
            // Optional: Filter out chunks if too noisy, or accumulate them
            // For now, let's just show status updates and ignore raw chunks to avoid flood
            if (eventType === 'status_update') {
                 setMessages(prev => [...prev, { type: 'system', content: `[Status] ${String(data)}`, event: eventType, from }]);
            }
        }
        else if (eventType === 'user_help_needed') {
             setMessages(prev => [...prev, { type: 'system', content: `[Help Needed] ${String(data)}`, event: eventType, from }]);
        }
        else if (eventType === 'error') {
             setMessages(prev => [...prev, { type: 'system', content: `[Error] ${msg.message || String(data)}`, event: eventType, from }]);
        }
        // Legacy / Chat specific handlers
        else if (msg.type === 'chat_message' && msg.event === 'ai_response') {
            const content = data?.content || data;
            if (content) {
                setMessages(prev => [...prev, { type: 'assistant', content: String(content), from }]);
            }
        } 
        else if (msg.type === 'realtime' && data?.task_id === taskId) {
             setMessages(prev => [...prev, { type: 'tool', content: `[${msg.event}] ${data.content}`, from }]);
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
