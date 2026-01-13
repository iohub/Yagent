import { useState, useRef, useEffect } from 'react';
import { SlidersHorizontal, ArrowUp, Square, FolderGit2, Clock, ChevronLeft, Check } from 'lucide-react';

interface TaskFormProps {
  onSubmit: (projectDir: string, taskDesc: string) => void;
  onStop?: () => void;
  isLoading?: boolean;
  isRunning?: boolean;
}

export function TaskForm({ onSubmit, onStop, isLoading, isRunning }: TaskFormProps) {
  const [projectDir, setProjectDir] = useState('');
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
    
    if (isRunning && onStop) {
      onStop();
      return;
    }

    if (!projectDir) {
        setShowProjectInput(true);
        return;
    }

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

          <div className="space-y-4">
            <div>
                <label className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 block">
                    Project Directory
                </label>
                <div className="relative">
                    <FolderGit2 className="absolute left-3 top-2.5 w-4 h-4 text-muted-foreground" />
                    <input
                        ref={projectInputRef}
                        type="text"
                        value={tempProjectDir}
                        onChange={(e) => setTempProjectDir(e.target.value)}
                        className="w-full bg-secondary border border-border rounded-lg pl-9 pr-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-primary/50 transition-all"
                        placeholder="/path/to/project"
                        autoFocus
                    />
                </div>
                <p className="text-[10px] text-muted-foreground mt-1.5">
                    Absolute path to the project you want to work on.
                </p>
            </div>

            {projectHistory.length > 0 && (
                <div>
                    <label className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 flex items-center gap-1">
                        <Clock className="w-3 h-3" />
                        Recent Projects
                    </label>
                    <div className="space-y-1">
                        {projectHistory.map((path, i) => (
                            <button
                                key={i}
                                type="button"
                                onClick={() => handleHistoryClick(path)}
                                className="w-full text-left px-3 py-2 rounded-md hover:bg-secondary/50 text-xs text-muted-foreground hover:text-foreground transition-colors truncate font-mono border border-transparent hover:border-border"
                                title={path}
                            >
                                {path}
                            </button>
                        ))}
                    </div>
                </div>
            )}

             <div className="mt-auto pt-4 flex justify-end">
                <button
                    type="button"
                    onClick={handleProjectSubmit}
                    className="bg-primary text-primary-foreground px-4 py-2 rounded-lg text-sm font-medium hover:bg-primary/90 transition-colors flex items-center gap-2"
                >
                    <Check className="w-4 h-4" />
                    Apply Changes
                </button>
             </div>
          </div>
        </div>
      )}

      {/* Main Form Content */}
      <div className="p-3">
        <div className="relative">
            <textarea
                ref={textareaRef}
                value={taskDesc}
                onChange={(e) => setTaskDesc(e.target.value)}
                onKeyDown={handleKeyDown}
                disabled={isLoading || isRunning}
                placeholder={isRunning ? "Task is running..." : "Describe your task..."}
                className="w-full bg-transparent text-sm text-foreground placeholder:text-muted-foreground resize-none focus:outline-none min-h-[80px] disabled:opacity-50"
                rows={3}
            />
            
            {/* Bottom Actions Bar */}
            <div className="flex items-center justify-between mt-2 pt-2 border-t border-border/50">
                <button
                    type="button"
                    onClick={() => !isRunning && setShowProjectInput(true)}
                    disabled={isRunning}
                    className={`flex items-center gap-1.5 text-xs ${!projectDir ? 'text-primary font-medium' : 'text-muted-foreground'} hover:text-primary transition-colors disabled:opacity-50 disabled:cursor-not-allowed`}
                    title={projectDir || 'Set Project Directory'}
                >
                    <FolderGit2 className="w-3.5 h-3.5" />
                    <span className="max-w-[150px] truncate font-mono">
                        {!projectDir ? 'repository' : projectDir === '.' ? 'Current Directory' : projectDir.split('/').pop()}
                    </span>
                </button>

                <div className="flex items-center gap-2">
                     {/* Model Selector Placeholder - could be added later */}
                     
                    <button
                        type="submit"
                        disabled={isLoading}
                        className={`flex items-center gap-2 px-3 py-1.5 rounded-lg text-xs font-medium transition-all duration-200 ${
                            isRunning
                             ? "bg-destructive text-destructive-foreground hover:bg-destructive/90"
                             : "bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
                        }`}
                    >
                        {isLoading ? (
                             <span className="w-4 h-4 border-2 border-current border-t-transparent rounded-full animate-spin" />
                        ) : isRunning ? (
                            <>
                                <Square className="w-3.5 h-3.5 fill-current" />
                                Terminate
                            </>
                        ) : (
                            <>
                                <ArrowUp className="w-3.5 h-3.5" />
                                Submit
                            </>
                        )}
                    </button>
                </div>
            </div>
        </div>
      </div>
    </form>
  );
}