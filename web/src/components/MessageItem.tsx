import { useState } from 'react';
import type { Message } from '../types';
import { cn } from '../lib/utils';
import { Bot, User, Terminal, Cpu, ChevronDown, ChevronRight, Activity, CheckCircle2 } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

interface MessageItemProps {
  message: Message;
}

function SystemMessage({ content }: { content: string }) {
  // Check if it's a status update
  const isStatus = content.startsWith('[Status]');
  const cleanContent = isStatus ? content.replace('[Status]', '').trim() : content;

  return (
    <div className="flex items-center gap-3 py-1 px-1 my-1 mr-auto max-w-[85%] animate-in fade-in duration-300">
      <div className={cn(
        "w-4 h-4 flex items-center justify-center shrink-0",
        isStatus ? "text-blue-400" : "text-neutral-500"
      )}>
        {isStatus ? <Activity className="w-3.5 h-3.5 animate-pulse" /> : <Cpu className="w-3.5 h-3.5" />}
      </div>
      <span className={cn(
        "text-xs font-medium truncate max-w-[500px]",
        isStatus ? "text-blue-400/80" : "text-neutral-500"
      )}>
        {cleanContent}
      </span>
    </div>
  );
}

function ToolMessage({ content }: { content: string }) {
  const [isExpanded, setIsExpanded] = useState(false);
  
  // Try to parse tool name from content like "[tool_call_result] read_file ..."
  const match = content.match(/^\[tool_call_result\]\s*(\w+)/);
  const toolName = match ? match[1] : 'Tool Output';
  const cleanContent = content.replace(/^\[tool_call_result\]\s*\w+\s*/, '').trim();

  // Try to parse JSON to format it nicely if possible
  let displayContent = cleanContent;
  let isJson = false;
  try {
    if (cleanContent.startsWith('{') || cleanContent.startsWith('[')) {
      const parsed = JSON.parse(cleanContent);
      displayContent = JSON.stringify(parsed, null, 2);
      isJson = true;
    }
  } catch (e) {
    // Not JSON, keep as is
  }

  return (
    <div className="flex flex-col gap-1 my-2 mr-auto max-w-[85%] w-full">
      <button 
        onClick={() => setIsExpanded(!isExpanded)}
        className="flex items-center gap-3 p-2 bg-[#1e1e1e] hover:bg-[#252526] border border-[#333] rounded-md transition-all group w-full text-left shadow-sm"
      >
        <div className="w-6 h-6 rounded-md bg-purple-500/10 flex items-center justify-center shrink-0 border border-purple-500/20 group-hover:border-purple-500/40 transition-colors">
          <Terminal className="w-3.5 h-3.5 text-purple-400" />
        </div>
        <div className="flex flex-col flex-1 min-w-0">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-purple-300 font-mono">
              {toolName}
            </span>
            <span className="text-[10px] text-neutral-500 flex items-center gap-1">
              <CheckCircle2 className="w-3 h-3" />
              Success
            </span>
          </div>
        </div>
        <div className="text-neutral-500 group-hover:text-neutral-300 transition-colors">
          {isExpanded ? <ChevronDown className="w-4 h-4" /> : <ChevronRight className="w-4 h-4" />}
        </div>
      </button>

      {isExpanded && (
        <div className="ml-2 pl-4 border-l-2 border-[#333] animate-in slide-in-from-top-2 duration-200">
          <div className="bg-[#111] rounded-md p-3 overflow-x-auto border border-[#333]">
            <pre className={cn("text-xs font-mono leading-relaxed", isJson ? "text-green-400" : "text-neutral-300")}>
              {displayContent}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}

export function MessageItem({ message }: MessageItemProps) {
  const isUser = message.type === 'human';
  const isSystem = message.type === 'system';
  const isTool = message.type === 'tool';

  if (isSystem) {
    return <SystemMessage content={message.content} />;
  }

  if (isTool) {
    return <ToolMessage content={message.content} />;
  }

  return (
    <div
      className={cn(
        'flex items-start gap-3 p-3 rounded-lg max-w-[85%] text-sm shadow-sm transition-all',
        isUser 
          ? 'ml-auto bg-[#007acc] text-white hover:bg-[#007acc]/90' 
          : 'mr-auto bg-[#252526] border border-[#3e3e42] text-[#e0e0e0] hover:bg-[#2a2a2b]'
      )}
    >
      <div
        className={cn(
          'w-6 h-6 rounded-md flex items-center justify-center shrink-0 shadow-inner',
          isUser ? 'bg-[#0062a3]' : 'bg-[#3e3e42]'
        )}
      >
        {isUser ? <User className="w-4 h-4" /> : <Bot className="w-4 h-4 text-[#007acc]" />}
      </div>
      {isUser ? (
        <div className="whitespace-pre-wrap break-words leading-relaxed">{message.content}</div>
      ) : (
        <div className="prose prose-invert prose-sm max-w-none break-words leading-relaxed">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{message.content}</ReactMarkdown>
        </div>
      )}
    </div>
  );
}
