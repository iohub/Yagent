import { useEffect, useRef } from 'react';
import type { Message } from '../types';
import { X, RefreshCw } from 'lucide-react';
import { cn } from '../lib/utils';

interface DebuggerPanelProps {
  isOpen: boolean;
  onClose: () => void;
  memory: Message[];
  taskId: string | null;
  onRefresh: () => void;
}

export function DebuggerPanel({ isOpen, onClose, memory, taskId, onRefresh }: DebuggerPanelProps) {
  const scrollRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [memory]);

  if (!isOpen) return null;

  return (
    <div className="w-1/3 border-l border-border bg-card flex flex-col shadow-xl z-20 h-full absolute right-0 top-0 bottom-0">
      <div className="flex items-center justify-between p-3 border-b border-border bg-secondary/20">
        <h2 className="text-sm font-semibold flex items-center gap-2">
          <span className="w-2 h-2 rounded-full bg-orange-500"></span>
          Conductor Memory
        </h2>
        <div className="flex items-center gap-1">
          <button 
            onClick={onRefresh}
            className="p-1 hover:bg-secondary rounded-sm text-muted-foreground hover:text-foreground transition-colors"
            title="Refresh Memory"
          >
            <RefreshCw className="w-4 h-4" />
          </button>
          <button 
            onClick={onClose}
            className="p-1 hover:bg-secondary rounded-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto p-4 space-y-4 custom-scrollbar" ref={scrollRef}>
        {!taskId ? (
          <div className="text-center text-muted-foreground text-xs py-8">
            No active task
          </div>
        ) : memory.length === 0 ? (
          <div className="text-center text-muted-foreground text-xs py-8">
            Memory is empty
          </div>
        ) : (
          memory.map((msg, idx) => (
            <div key={idx} className="space-y-1">
              <div className="flex items-center gap-2">
                <span className={cn(
                  "text-[10px] uppercase font-bold px-1.5 py-0.5 rounded border",
                  {
                    'bg-blue-500/10 text-blue-500 border-blue-500/20': msg.type === 'system',
                    'bg-green-500/10 text-green-500 border-green-500/20': msg.type === 'human',
                    'bg-purple-500/10 text-purple-500 border-purple-500/20': msg.type === 'assistant',
                    'bg-orange-500/10 text-orange-500 border-orange-500/20': msg.type === 'tool',
                  }
                )}>
                  {msg.type}
                </span>
                {msg.timestamp && (
                   <span className="text-[10px] text-muted-foreground">
                     {new Date(msg.timestamp).toLocaleTimeString()}
                   </span>
                )}
              </div>
              
              <div className="bg-muted/50 rounded-md p-3 text-xs font-mono overflow-x-auto whitespace-pre-wrap border border-border">
                {msg.content}
                {msg.tool_calls && msg.tool_calls.length > 0 && (
                  <div className="mt-2 pt-2 border-t border-border/50">
                    <div className="text-[10px] font-semibold text-muted-foreground mb-1">Tool Calls:</div>
                    {msg.tool_calls.map((tc, i) => (
                      <div key={i} className="bg-background/50 p-2 rounded mb-1">
                        <div className="text-purple-400 font-bold">{tc.function.name}</div>
                        <div className="text-muted-foreground">{JSON.stringify(tc.function.arguments)}</div>
                      </div>
                    ))}
                  </div>
                )}
                {msg.tool_call_id && (
                    <div className="mt-1 text-[10px] text-muted-foreground">
                        ID: {msg.tool_call_id}
                    </div>
                )}
              </div>
            </div>
          ))
        )}
      </div>
      
      <div className="p-2 border-t border-border bg-secondary/10 text-[10px] text-muted-foreground flex justify-between">
        <span>Total Messages: {memory.length}</span>
        <span>Auto-updates active</span>
      </div>
    </div>
  );
}
