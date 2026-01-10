import { useState, useEffect, useRef, useCallback } from 'react';
import type { Message, Task } from '../types';
import { startTask as apiStartTask, getWebSocketUrl, getTaskStatus, cancelTask } from '../api/client';

export function useTask() {
  const [taskId, setTaskId] = useState<string | null>(null);
  const [status, setStatus] = useState<Task['status']>('finished'); // Default to finished so we can start new
  const [messages, setMessages] = useState<Message[]>([]);
  const [conductorMemory, setConductorMemory] = useState<Message[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [isHistorical, setIsHistorical] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);

  const refreshMemory = useCallback(async (currentTaskId: string) => {
    try {
      const mem = await import('../api/client').then(m => m.getMemory(currentTaskId));
      setConductorMemory(mem.messages);
    } catch (e) {
      console.error('Failed to fetch memory:', e);
    }
  }, []);

  const stopTask = async () => {
    if (!taskId) return;
    try {
      await cancelTask(taskId);
      // We don't manually set status here, we wait for WS update or assume it will be handled
      // But to be responsive we can set it to finished or failed if needed, 
      // however the backend should send a status update.
      // For now let's just log.
      console.log('Task cancellation requested');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to cancel task');
    }
  };

  const startTask = async (projectDir: string, taskDesc: string) => {
    setIsLoading(true);
    setError(null);
    try {
      const { task_id } = await apiStartTask(projectDir, taskDesc);
      setTaskId(task_id);
      setStatus('running');
      setIsHistorical(false);
      setMessages([]);
      setConductorMemory([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to start task');
    } finally {
      setIsLoading(false);
    }
  };

  const loadExistingTask = async (taskIdToLoad: string, projectDir: string = '') => {
    setIsLoading(true);
    setError(null);
    try {
      await import('../api/client').then(m => m.loadTask(taskIdToLoad, projectDir));
      setTaskId(taskIdToLoad);
      setStatus('running');
      setIsHistorical(true);
      
      // Fetch memory
      const mem = await import('../api/client').then(m => m.getMemory(taskIdToLoad));
      setConductorMemory(mem.messages);
      
      // Populate messages from memory
      setMessages(mem.messages);
      
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load task');
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
        else if (eventType === 'task_update') {
            // Handle task status updates
            if (data.status) {
                setStatus(data.status);
            }
            // Optionally log significant status changes to chat
            if (data.status === 'finished') {
                 setMessages(prev => [...prev, { type: 'system', content: `[Task Finished] ${data.result || ''}`, event: eventType, from }]);
            } else if (data.status === 'failed') {
                 setMessages(prev => [...prev, { type: 'system', content: `[Task Failed] ${data.error || ''}`, event: eventType, from }]);
            } else if (data.status === 'cancelled') {
                 setMessages(prev => [...prev, { type: 'system', content: `[Task Cancelled]`, event: eventType, from }]);
            }
        }
        else if (['tool_call', 'tool_call_result'].includes(eventType)) {
            let content = data;
            if (typeof data === 'object' && data !== null) {
                const toolName = data.tool_name || 'unknown';
                content = `[${eventType}] ${toolName}\n${JSON.stringify(JSON.parse(data.result), null, 2)}`;
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
             if (msg.event === 'memory_change') {
                 refreshMemory(taskId);
             } else {
                 setMessages(prev => [...prev, { type: 'tool', content: `[${msg.event}] ${data.content}`, from }]);
             }
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
  }, [taskId, refreshMemory]);

  useEffect(() => {
    if (taskId) {
      connectWs();
    }
    return () => {
      if (wsRef.current) {
        wsRef.current.close();
      }
    };
  }, [taskId, connectWs]);

  // Polling for task status to ensure consistency
  useEffect(() => {
    if (!taskId || status === 'finished' || status === 'failed') return;

    const interval = setInterval(async () => {
      try {
        const task = await getTaskStatus(taskId);
        if (task.status && task.status !== status) {
           setStatus(task.status);
           if (task.status === 'finished') {
             setMessages(prev => [...prev, { type: 'system', content: `[Task Finished] ${task.result || ''}` }]);
           } else if (task.status === 'failed') {
             setMessages(prev => [...prev, { type: 'system', content: `[Task Failed] ${task.error || ''}` }]);
           }
        }
      } catch (e) {
        console.error('Failed to poll task status', e);
      }
    }, 2000);

    return () => clearInterval(interval);
  }, [taskId, status]);

  return {
    taskId,
    status,
    messages,
    conductorMemory,
    error,
    isLoading,
    isHistorical,
    startTask,
    loadExistingTask,
    refreshMemory,
    stopTask,
    // Expose sendChatMessage if implemented or if we want to allow sending messages
  };
}
