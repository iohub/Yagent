import type { Message } from '../types';
import { cn } from '../lib/utils';
import { Bot, User, Terminal, Cpu } from 'lucide-react';

interface MessageItemProps {
  message: Message;
}

export function MessageItem({ message }: MessageItemProps) {
  const isUser = message.type === 'human';
  const isSystem = message.type === 'system';
  const isTool = message.type === 'tool';

  // Handle different message types with distinct styles
  if (isSystem) {
    return (
      <div className="flex items-start gap-3 p-3 bg-[#252526] border border-[#3e3e42] rounded-sm text-xs text-[#cccccc] font-mono">
        <Cpu className="w-4 h-4 mt-0.5 shrink-0 text-[#007acc]" />
        <div className="whitespace-pre-wrap">{message.content}</div>
      </div>
    );
  }

  if (isTool) {
    return (
      <div className="flex items-start gap-3 p-3 bg-[#1e1e1e] border border-[#3e3e42] text-[#cccccc] rounded-sm text-xs font-mono overflow-x-auto">
        <Terminal className="w-4 h-4 mt-0.5 shrink-0 text-[#007acc]" />
        <div className="whitespace-pre-wrap">{message.content}</div>
      </div>
    );
  }

  return (
    <div
      className={cn(
        'flex items-start gap-3 p-3 rounded-sm max-w-[85%] text-sm',
        isUser ? 'ml-auto bg-[#007acc] text-white' : 'mr-auto bg-[#252526] border border-[#3e3e42] text-[#cccccc]'
      )}
    >
      <div
        className={cn(
          'w-6 h-6 rounded-sm flex items-center justify-center shrink-0',
          isUser ? 'bg-[#0062a3]' : 'bg-[#3e3e42]'
        )}
      >
        {isUser ? <User className="w-4 h-4" /> : <Bot className="w-4 h-4 text-[#007acc]" />}
      </div>
      <div className="whitespace-pre-wrap break-words">{message.content}</div>
    </div>
  );
}
