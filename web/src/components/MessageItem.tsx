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
      <div className="flex items-start gap-3 p-4 bg-gray-50 rounded-lg text-sm text-gray-600 font-mono">
        <Cpu className="w-5 h-5 mt-0.5 shrink-0" />
        <div className="whitespace-pre-wrap">{message.content}</div>
      </div>
    );
  }

  if (isTool) {
    return (
      <div className="flex items-start gap-3 p-4 bg-gray-900 text-gray-100 rounded-lg text-sm font-mono overflow-x-auto">
        <Terminal className="w-5 h-5 mt-0.5 shrink-0 text-green-400" />
        <div className="whitespace-pre-wrap">{message.content}</div>
      </div>
    );
  }

  return (
    <div
      className={cn(
        'flex items-start gap-3 p-4 rounded-lg max-w-[85%]',
        isUser ? 'ml-auto bg-blue-600 text-white' : 'mr-auto bg-white border border-gray-200'
      )}
    >
      <div
        className={cn(
          'w-8 h-8 rounded-full flex items-center justify-center shrink-0',
          isUser ? 'bg-blue-700' : 'bg-gray-100'
        )}
      >
        {isUser ? <User className="w-5 h-5" /> : <Bot className="w-5 h-5 text-blue-600" />}
      </div>
      <div className="whitespace-pre-wrap break-words">{message.content}</div>
    </div>
  );
}
