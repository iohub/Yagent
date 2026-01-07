import { useState, useRef, useEffect } from 'react';
import { SlidersHorizontal, ArrowUp, Square, FolderGit2, X, Clock, ChevronLeft } from 'lucide-react';

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
    <form onSubmit={handleSubmit} className="relative group bg-black rounded-xl border border-neutral-800 overflow-hidden shadow-lg">
      {/* Project Input Overlay */}
      {showProjectInput && (
        <div className="absolute inset-0 z-20 bg-[#1e1e1e] flex flex-col p-4 animate-in fade-in duration-200">
          <div className="flex items-center gap-2 mb-4">
            <button 
              type="button" 
              onClick={() => setShowProjectInput(false)}
              className="text-neutral-400 hover:text-white flex items-center gap-1 text-sm font-medium transition-colors"
            >
              <ChevronLeft className="w-4 h-4" />
              Back
            </button>
            <span className="text-sm text-neutral-500 font-medium ml-auto">Set Project Path</span>
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
              className="flex-1 bg-black border border-neutral-700 rounded-lg px-3 py-2 text-sm text-neutral-200 focus:outline-none focus:border-neutral-500 placeholder-neutral-600"
              placeholder="/path/to/project"
            />
            <button
              type="button"
              onClick={() => handleProjectSubmit()}
              className="bg-white text-black hover:bg-neutral-200 px-4 py-2 rounded-lg text-sm font-medium transition-colors"
            >
              Set
            </button>
          </div>

          {projectHistory.length > 0 && (
            <div className="flex-1 overflow-y-auto min-h-0">
              <div className="flex items-center gap-1 text-[10px] text-neutral-500 uppercase tracking-wider mb-2">
                <Clock className="w-3 h-3" />
                <span>Recent Paths</span>
              </div>
              <div className="space-y-1">
                {projectHistory.map((path, i) => (
                  <button
                    key={i}
                    type="button"
                    onClick={() => handleHistoryClick(path)}
                    className="w-full text-left text-sm text-neutral-400 hover:text-white hover:bg-white/5 rounded-lg px-3 py-2 truncate transition-colors border border-transparent hover:border-neutral-800"
                    title={path}
                  >
                    {path}
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
          placeholder="Ask v0 to build or plan..."
          className="w-full bg-transparent text-neutral-200 placeholder-neutral-500 resize-none focus:outline-none text-sm min-h-[80px] max-h-[200px]"
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
                ? 'text-blue-400 bg-blue-400/10' 
                : 'text-neutral-500 hover:text-neutral-300'
            }`}
            title={`Current project: ${projectDir}`}
          >
            <FolderGit2 className="w-4 h-4" />
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
