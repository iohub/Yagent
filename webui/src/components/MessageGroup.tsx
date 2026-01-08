import { useState } from 'react';
import type { Message } from '../types';
import { MessageItem } from './MessageItem';
import { ChevronDown, ChevronRight, User, Bot, Server, Terminal, Sparkles, CheckCircle2, AlertCircle } from 'lucide-react';
import { cn } from '../lib/utils';

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
    if (lowerFrom.includes('user') || lowerFrom.includes('human')) 
      return <User className="w-4 h-4 text-primary/70" />;
    if (lowerFrom.includes('system')) 
      return <Server className="w-4 h-4 text-orange-500/70" />;
    if (lowerFrom.includes('coding') || lowerFrom.includes('assistant')) 
      return <Bot className="w-4 h-4 text-green-500/70" />;
    if (lowerFrom.includes('tool'))
      return <Terminal className="w-4 h-4 text-purple-500/70" />;
    
    // Default for other agents
    return <Sparkles className="w-4 h-4 text-indigo-500/70" />;
  };

  return (
    <div className="flex flex-col shrink-0">
      <button
        onClick={() => setIsOpen(!isOpen)}
        className="flex items-center gap-2 py-2 px-1 hover:bg-muted/30 rounded-md transition-colors text-sm group w-full text-left"
      >
        <div className={cn(
          "w-5 h-5 flex items-center justify-center transition-transform duration-200 text-muted-foreground/60 group-hover:text-foreground",
          isOpen ? "rotate-90" : ""
        )}>
          <ChevronRight className="w-4 h-4" />
        </div>
        
        <div className="flex items-center gap-2">
           {getIcon()}
           <span className="font-medium text-foreground/90">{from}</span>
        </div>
        
        <span className="text-xs text-muted-foreground/50 ml-1">
          {messages.length} msg{messages.length !== 1 ? 's' : ''}
        </span>
      </button>
      
      {isOpen && (
        <div className="flex relative ml-2.5">
          {/* Vertical line */}
          <div className="absolute left-[1px] top-0 bottom-0 w-[2px] bg-border/40" />
          
          <div className="flex flex-col gap-2 pl-6 py-2 w-full">
            {messages.map((msg, idx) => (
              <MessageItem key={idx} message={msg} />
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
