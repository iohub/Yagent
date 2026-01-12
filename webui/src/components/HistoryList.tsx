import { useState, useEffect } from 'react';
import { listHistory } from '../api/client';
import type { TaskHistoryItem } from '../types';
import { History, Clock, MessageSquare, Loader2 } from 'lucide-react';
import { cn } from '../lib/utils';
import { Button } from './ui/Button';

interface HistoryListProps {
  onLoad: (taskId: string) => void;
  currentTaskId?: string | null;
  disabled?: boolean;
}

export function HistoryList({ onLoad, currentTaskId, disabled }: HistoryListProps) {
  const [history, setHistory] = useState<TaskHistoryItem[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchHistory = async () => {
    if (disabled) return;
    setIsLoading(true);
    setError(null);
    try {
      const data = await listHistory();
      // Sort by updated_at desc
      data.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime());
      setHistory(data);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load history');
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    fetchHistory();
  }, []);

  return (
    <div className={cn("space-y-4", disabled && "opacity-50 pointer-events-none select-none")}>
      <div className="flex items-center justify-between">
        <h2 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider flex items-center gap-2">
          <History className="w-3 h-3" />
          History
        </h2>
        <Button 
            variant="ghost" 
            size="sm" 
            onClick={fetchHistory}
            disabled={isLoading || disabled}
            className="h-6 w-6 p-0"
        >
            <Clock className={cn("w-3 h-3", isLoading && "animate-spin")} />
        </Button>
      </div>

      {error && (
        <div className="text-xs text-destructive bg-destructive/10 p-2 rounded">
          {error}
        </div>
      )}

      {isLoading && history.length === 0 ? (
        <div className="flex justify-center p-4">
          <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />
        </div>
      ) : (
        <div className="space-y-2 max-h-[300px] overflow-y-auto custom-scrollbar pr-2">
          {history.length === 0 ? (
            <div className="text-xs text-muted-foreground text-center py-4">
              No history found
            </div>
          ) : (
            history.map((item) => (
              <div 
                key={item.task_id}
                onClick={() => onLoad(item.task_id)}
                className={cn(
                  "p-2 rounded border border-border cursor-pointer hover:bg-secondary/50 transition-colors text-left",
                  currentTaskId === item.task_id && "bg-secondary border-primary/20"
                )}
              >
                <div className="flex justify-between items-start mb-1">
                  <div className="flex flex-col overflow-hidden mr-2">
                    <span className="text-xs font-semibold truncate" title={item.title}>
                        {item.title || item.task_id}
                    </span>
                    <span className="text-[10px] font-mono text-muted-foreground truncate" title={item.task_id}>
                        {item.task_id.slice(0, 8)}
                    </span>
                  </div>
                  <span className="text-[10px] text-muted-foreground whitespace-nowrap">
                    {new Date(item.updated_at).toLocaleString()}
                  </span>
                </div>
                <div className="flex items-center gap-1 text-[10px] text-muted-foreground">
                    <MessageSquare className="w-3 h-3" />
                    <span>{item.message_count} messages</span>
                </div>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}
