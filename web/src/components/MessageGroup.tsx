import { useState } from 'react';
import type { Message } from '../types';
import { MessageItem } from './MessageItem';
import { ChevronDown, ChevronRight, User, Bot, Server, Terminal } from 'lucide-react';

interface MessageGroupProps {
  from: string;
  messages: Message[];
  defaultOpen?: boolean;
}

export function MessageGroup({ from, messages, defaultOpen = true }: MessageGroupProps) {
  const [isOpen, setIsOpen] = useState(defaultOpen);

  // Determine icon based on 'from'
  const getIcon = () => {
    const lowerFrom = from.toLowerCase();
    if (lowerFrom.includes('user') || lowerFrom.includes('human')) return <User className="w-4 h-4 text-blue-500" />;
    if (lowerFrom.includes('system')) return <Server className="w-4 h-4 text-orange-500" />;
    if (lowerFrom.includes('coding')) return <Bot className="w-4 h-4 text-green-500" />;
    return <Terminal className="w-4 h-4 text-purple-500" />;
  };

  return (
    <div className="border border-border rounded-lg overflow-hidden bg-card/30 shadow-sm shrink-0">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="w-full flex items-center gap-3 p-2.5 bg-muted/40 hover:bg-muted/60 transition-colors text-sm font-medium border-b border-border/40 group"
      >
        <div className="flex items-center justify-center w-6 h-6 rounded bg-background border border-border shadow-sm">
          {getIcon()}
        </div>
        <span className="flex-1 text-left truncate font-semibold opacity-90 text-foreground">{from}</span>
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70 font-mono bg-muted px-1.5 py-0.5 rounded border border-border/50">
          {messages.length} msg{messages.length !== 1 ? 's' : ''}
        </span>
        <div className="text-muted-foreground/50 group-hover:text-foreground transition-colors">
          {isOpen ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
        </div>
      </button>
      
      {isOpen && (
        <div className="flex flex-col gap-1 p-3 bg-background/50">
          {messages.map((msg, idx) => (
            <MessageItem key={idx} message={msg} />
          ))}
        </div>
      )}
    </div>
  );
}
