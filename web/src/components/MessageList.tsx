import { useEffect, useRef } from 'react';
import type { Message } from '../types';
import { MessageItem } from './MessageItem';

interface MessageListProps {
  messages: Message[];
}

export function MessageList({ messages }: MessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  return (
    <div className="flex flex-col gap-4 p-4 overflow-y-auto h-full min-h-0 bg-gray-50/50">
      {messages.length === 0 ? (
        <div className="flex flex-col items-center justify-center h-full text-gray-400 space-y-2">
          <p>No messages yet.</p>
          <p className="text-sm">Start a task to see the agent in action.</p>
        </div>
      ) : (
        messages.map((msg, idx) => (
          <MessageItem key={idx} message={msg} />
        ))
      )}
      <div ref={bottomRef} />
    </div>
  );
}
