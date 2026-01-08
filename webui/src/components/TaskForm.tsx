import { useState, useRef, useEffect } from 'react';
import { SlidersHorizontal, ArrowUp, Square, FolderGit2, Clock, ChevronLeft, Check } from 'lucide-react';

interface TaskFormProps {
  onSubmit: (projectDir: string, taskDesc: string) => void;
  isLoading?: boolean;
}

export function TaskForm({ onSubmit, isLoading }: TaskFormProps) {
  const [projectDir, setProjectDir] = useState('.');
  const [showProjectInput, setShowProjectInput] = useState(false);
  const [projectHistory, setProjectHistory] = useState<string[]>([]);
  const [tempProjectDir, setTempProjectDir] = useState('');
  
  const [taskDesc, setTaskDesc] = useState('');
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const projectInputRef = useRef<HTMLInputElement>(null);

  // Load history from localStorage
  useEffect(() => {
    try {
      const saved = localStorage.getItem('project_path_history');
      if (saved) {
        setProjectHistory(JSON.parse(saved));
      }
    } catch (e) {
      console.error('Failed to parse project history', e);
    }
  }, []);

  const saveToHistory = (path: string) => {
    if (!path.trim() || path === '.') return;
    
    const newHistory = [path, ...projectHistory.filter(h => h !== path)].slice(0, 5);
    setProjectHistory(newHistory);
    localStorage.setItem('project_path_history', JSON.stringify(newHistory));
  };

  const handleProjectSubmit = (e?: React.FormEvent) => {
    if (e) e.preventDefault();
    if (tempProjectDir.trim()) {
      const newPath = tempProjectDir.trim();
      setProjectDir(newPath);
      saveToHistory(newPath);
      setShowProjectInput(false);
    }
  };

  const handleHistoryClick = (path: string) => {
    setTempProjectDir(path);
    // Optionally auto-submit or just fill
    // setProjectDir(path);
    // setShowProjectInput(false);
  };

  useEffect(() => {
    if (showProjectInput && projectInputRef.current) {
      setTempProjectDir(projectDir);
      setTimeout(() => projectInputRef.current?.focus(), 50);
    }
  }, [showProjectInput, projectDir]);

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
    <form onSubmit={handleSubmit} className={`relative group bg-card rounded-xl border border-border overflow-hidden shadow-lg transition-[height,min-height] duration-200 ${showProjectInput ? 'min-h-[320px]' : ''}`}>
      {/* Project Input Overlay */}
      {showProjectInput && (
        <div className="absolute inset-0 z-20 bg-background flex flex-col p-4 animate-in fade-in duration-200">
          <div className="flex items-center gap-2 mb-4">
            <button 
              type="button" 
              onClick={() => setShowProjectInput(false)}
              className="text-muted-foreground hover:text-foreground flex items-center gap-1 text-sm font-medium transition-colors"
            >
              <ChevronLeft className="w-4 h-4" />
              Back
            </button>
            <span className="text-sm text-muted-foreground font-medium ml-auto">repository settings</span>
          </div>
          
          <div className="flex gap-2 mb-4">
            <input
              ref={projectInputRef}
              value={tempProjectDir}
              onChange={(e) => setTempProjectDir(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault();
                  handleProjectSubmit();
                }
              }}
              className="flex-1 bg-input border border-input rounded-lg px-3 py-2 text-sm text-foreground focus:outline-none focus:border-ring placeholder:text-muted-foreground"
              placeholder="/path/to/project"
            />
            <button
              type="button"
              onClick={() => handleProjectSubmit()}
              className="bg-primary text-primary-foreground hover:bg-primary/90 p-2.5 rounded-lg transition-colors shrink-0 flex items-center justify-center aspect-square"
              title="Set Project Path"
            >
              <Check className="w-4 h-4" />
            </button>
          </div>

          {projectHistory.length > 0 && (
            <div className="flex-1 overflow-y-auto min-h-0 mt-2">
              <div className="flex items-center gap-2 text-xs text-muted-foreground font-medium mb-2 px-1">
                <Clock className="w-3.5 h-3.5" />
                <span>Recent Paths</span>
              </div>
              <div className="space-y-1">
                {projectHistory.map((path, i) => (
                  <button
                    key={i}
                    type="button"
                    onClick={() => handleHistoryClick(path)}
                    className="w-full flex items-center gap-2 text-left text-sm text-muted-foreground hover:text-foreground hover:bg-accent/50 rounded-lg px-3 py-2.5 transition-all border border-transparent hover:border-border/50 group/item"
                    title={path}
                  >
                    <FolderGit2 className="w-4 h-4 opacity-50 group-hover/item:opacity-100 transition-opacity shrink-0" />
                    <span className="truncate font-mono text-xs opacity-90">{path}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      <div className="p-3 pb-0">
        <textarea
          ref={textareaRef}
          value={taskDesc}
          onChange={(e) => setTaskDesc(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder="Ask the agent to perform a task..."
          className="w-full bg-transparent text-foreground placeholder:text-muted-foreground resize-none focus:outline-none text-sm min-h-[80px] max-h-[200px]"
          rows={1}
        />
      </div>

      {/* Footer */}
      <div className="flex items-center justify-between p-2 px-3 relative">
        <div className="flex items-center gap-2">
          <button 
            type="button" 
            onClick={() => setShowProjectInput(true)}
            className={`transition-colors p-1 rounded-md ${
              projectDir !== '.' 
                ? 'text-primary bg-primary/10' 
                : 'text-muted-foreground hover:text-foreground'
            }`}
            title={`Current project: ${projectDir}`}
          >
            <FolderGit2 className="w-4 h-4" />
          </button>
          <button 
            type="button" 
            className="text-muted-foreground hover:text-foreground transition-colors p-1"
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
              ? 'bg-muted text-muted-foreground cursor-not-allowed' 
              : !taskDesc.trim() 
                ? 'bg-muted text-muted-foreground cursor-not-allowed'
                : 'bg-primary text-primary-foreground hover:bg-primary/90'
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
