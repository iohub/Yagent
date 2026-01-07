import { useTask } from './hooks/useTask';
import { TaskForm } from './components/TaskForm';
import { MessageList } from './components/MessageList';
import { Code2, Activity, Terminal } from 'lucide-react';
import { cn } from './lib/utils';

function App() {
  const { taskId, status, messages, error, isLoading, startTask } = useTask();

  return (
    <div className="flex h-screen bg-[#1e1e1e] font-sans text-[#cccccc] overflow-hidden">
      {/* Sidebar / Configuration Area */}
      <div className="w-1/3 bg-[#252526] border-r border-[#3e3e42] flex flex-col shadow-lg z-10">
        <div className="p-4 border-b border-[#3e3e42] flex items-center justify-between">
          <div className="flex items-center gap-2">
            <div className="bg-[#007acc] p-1.5 rounded">
              <Code2 className="w-5 h-5 text-white" />
            </div>
            <h1 className="text-lg font-bold text-[#cccccc] tracking-tight">CodeActor</h1>
          </div>
          <span className="text-[10px] bg-[#3e3e42] px-1.5 py-0.5 rounded text-[#cccccc]">v0.1.0</span>
        </div>

        <div className="flex-1 overflow-y-auto p-4 custom-scrollbar">
          <div className="space-y-6">
            <section>
              <h2 className="text-xs font-semibold text-[#6f6f6f] uppercase tracking-wider mb-3 flex items-center gap-2">
                <Terminal className="w-3 h-3" />
                New Task
              </h2>
              <TaskForm onSubmit={startTask} isLoading={isLoading} />
            </section>

            {taskId && (
              <section className="bg-[#2d2d2d] rounded-sm p-3 border border-[#3e3e42]">
                <h2 className="text-xs font-semibold text-[#6f6f6f] uppercase tracking-wider mb-2 flex items-center gap-2">
                  <Activity className="w-3 h-3" />
                  Status
                </h2>
                <div className="space-y-2">
                  <div className="flex justify-between items-center text-xs">
                    <span className="text-[#969696]">Task ID</span>
                    <span className="font-mono bg-[#3c3c3c] px-1.5 py-0.5 rounded text-[#cccccc]">{taskId.slice(0, 8)}...</span>
                  </div>
                  <div className="flex justify-between items-center text-xs">
                    <span className="text-[#969696]">State</span>
                    <span className={cn(
                      "px-1.5 py-0.5 rounded text-[10px] font-medium uppercase border",
                      {
                        'bg-[#007acc]/20 text-[#007acc] border-[#007acc]/30': status === 'running',
                        'bg-green-500/20 text-green-400 border-green-500/30': status === 'finished',
                        'bg-red-500/20 text-red-400 border-red-500/30': status === 'failed',
                      }
                    )}>
                      {status}
                    </span>
                  </div>
                </div>
              </section>
            )}

            {error && (
              <div className="p-3 bg-red-900/20 text-red-300 rounded-sm text-xs border border-red-900/30">
                {error}
              </div>
            )}
          </div>
        </div>
        
        <div className="p-2 border-t border-[#3e3e42] text-[10px] text-center text-[#6f6f6f]">
          Connected to Local Agent
        </div>
      </div>

      {/* Main Content Area */}
      <div className="flex-1 flex flex-col min-w-0 bg-[#1e1e1e]">
        <header className="h-10 bg-[#1e1e1e] border-b border-[#3e3e42] flex items-center px-4 justify-between">
          <div className="flex items-center gap-2 text-sm text-[#cccccc]">
            <Terminal className="w-4 h-4 text-[#007acc]" />
            <span className="font-medium">Actions</span>
          </div>
          {status === 'running' && (
            <div className="flex items-center gap-2 text-xs text-[#007acc]">
              <span className="w-1.5 h-1.5 bg-[#007acc] rounded-full animate-pulse"></span>
              Executing...
            </div>
          )}
        </header>

        <main className="flex-1 overflow-hidden relative">
          <div className="absolute inset-0">
             <MessageList messages={messages} />
          </div>
        </main>
      </div>
    </div>
  );
}

export default App;
