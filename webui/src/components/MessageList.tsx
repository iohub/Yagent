import { useEffect, useRef, useMemo } from 'react';
import type { Message } from '../types';
import { MessageGroup } from './MessageGroup';

interface MessageListProps {
  messages: Message[];
}

export function MessageList({ messages }: MessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null);

  // Group messages by 'from'
  const groupedMessages = useMemo(() => {
    const groups: { from: string; messages: Message[] }[] = [];
    
    if (messages.length === 0) return groups;

    let currentGroup: { from: string; messages: Message[] } | null = null;

    messages.forEach((msg) => {
      // Use 'from' field if available, otherwise derive from type
      let from = msg.from;
      
      if (!from) {
        if (msg.type === 'human') from = 'User';
        else if (msg.type === 'system') from = 'System';
        else if (msg.type === 'assistant') from = 'Assistant';
        else if (msg.type === 'tool') from = 'Tool';
        else from = 'Unknown';
      }
      
      if (!currentGroup || currentGroup.from !== from) {
        if (currentGroup) {
          groups.push(currentGroup);
        }
        currentGroup = { from, messages: [msg] };
      } else {
        currentGroup.messages.push(msg);
      }
    });

    if (currentGroup) {
      groups.push(currentGroup);
    }

    return groups;
  }, [messages]);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages]);

  return (
    <div className="flex flex-col gap-2 p-4 overflow-y-auto h-full min-h-0 custom-scrollbar">
      {messages.length === 0 ? (
        <div className="flex flex-col items-center justify-center h-full text-muted-foreground space-y-2">
          <p>No messages yet.</p>
          <p className="text-sm">Start a task to see the agent in action.</p>
        </div>
      ) : (
        groupedMessages.map((group, idx) => (
          <MessageGroup 
            key={idx} 
            from={group.from} 
            messages={group.messages}
            // Open the first 3 groups and the last 3 groups by default
            defaultOpen={idx < 3 || idx >= groupedMessages.length - 3}
          />
        ))
      )}
      <div ref={bottomRef} />
    </div>
  );
}
