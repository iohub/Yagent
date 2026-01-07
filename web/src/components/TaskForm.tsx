import { useState, useRef, useEffect } from 'react';
import { Paperclip, SlidersHorizontal, ArrowUp, Square } from 'lucide-react';

interface TaskFormProps {
  onSubmit: (projectDir: string, taskDesc: string) => void;
  isLoading?: boolean;
}

export function TaskForm({ onSubmit, isLoading }: TaskFormProps) {
  const [projectDir] = useState('.');
  const [taskDesc, setTaskDesc] = useState('');
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const handleSubmit = (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (taskDesc.trim() && !isLoading) {
      onSubmit(projectDir, taskDesc);
      // Optional: Clear input after submit if needed, but usually controlled by parent or success
    }
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSubmit();
    }
  };

  // Auto-resize textarea
  useEffect(() => {
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
      textareaRef.current.style.height = `${textareaRef.current.scrollHeight}px`;
    }
  }, [taskDesc]);

  return (
    <form onSubmit={handleSubmit} className="relative group bg-black rounded-xl border border-neutral-800 overflow-hidden shadow-lg">
      <div className="p-3 pb-0">
        <textarea
          ref={textareaRef}
          value={taskDesc}
          onChange={(e) => setTaskDesc(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Ask v0 to build or plan..."
          className="w-full bg-transparent text-neutral-200 placeholder-neutral-500 resize-none focus:outline-none text-sm min-h-[40px] max-h-[200px]"
          rows={1}
        />
      </div>

      {/* Footer */}
      <div className="flex items-center justify-between p-2 px-3">
        <div className="flex items-center gap-2">
          <button 
            type="button" 
            className="text-neutral-500 hover:text-neutral-300 transition-colors p-1"
            title="Attach file"
          >
            <Paperclip className="w-4 h-4" />
          </button>
          <button 
            type="button" 
            className="text-neutral-500 hover:text-neutral-300 transition-colors p-1"
            title="Settings"
          >
            <SlidersHorizontal className="w-4 h-4" />
          </button>
        </div>

        <button
          type="submit"
          disabled={isLoading || !taskDesc.trim()}
          className={`
            rounded-full p-1.5 h-8 w-8 flex items-center justify-center transition-all duration-200
            ${isLoading 
              ? 'bg-neutral-800 text-white cursor-not-allowed' 
              : !taskDesc.trim() 
                ? 'bg-neutral-800 text-neutral-500 cursor-not-allowed'
                : 'bg-white text-black hover:bg-neutral-200'
            }
          `}
        >
          {isLoading ? (
            <Square className="w-3 h-3 fill-current" />
          ) : (
            <ArrowUp className="w-4 h-4" />
          )}
        </button>
      </div>
    </form>
  );
}
