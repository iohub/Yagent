import { useState } from 'react';
import { useTask } from './hooks/useTask';
import { TaskForm } from './components/TaskForm';
import { HistoryList } from './components/HistoryList';
import { MessageList } from './components/MessageList';
import { DebuggerPanel } from './components/DebuggerPanel';
import { Activity, Terminal, Bug } from 'lucide-react';
import { cn } from './lib/utils';
import { ThemeProvider } from './components/theme-provider';
import { ThemeToggle } from './components/theme-toggle';

function AppContent() {
  const { taskId, status, messages, conductorMemory, error, isLoading, isHistorical, startTask, stopTask, loadExistingTask, refreshMemory } = useTask();
  const [showDebugger, setShowDebugger] = useState(false);
  const isRunning = status === 'running' && !isHistorical;

  return (
    <div className="flex h-screen bg-background font-sans text-foreground overflow-hidden">
      {/* Sidebar / Configuration Area */}
      <div className="w-1/3 bg-card border-r border-border flex flex-col shadow-lg z-10">

        <div className="flex-1 overflow-y-auto p-4 custom-scrollbar">
          <div className="space-y-6">
            <section>
              <h2 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3 flex items-center gap-2">
                <img src="/logo.svg" className="w-8 h-6" alt="CodeActor" />
                CodeActor
              </h2>
              <TaskForm onSubmit={startTask} onStop={stopTask} isLoading={isLoading} isRunning={isRunning} />
            </section>

            <section>
                <HistoryList onLoad={loadExistingTask} currentTaskId={taskId} disabled={isRunning} />
            </section>

            {error && (
              <div className="p-3 bg-destructive/10 text-destructive rounded-sm text-xs border border-destructive/20">
                {error}
              </div>
            )}
          </div>
        </div>
        
        <div className="p-2 border-t border-border text-[10px] text-center text-muted-foreground flex justify-between items-center px-4">
          <span>Connected to Local Agent</span>
          <ThemeToggle />
        </div>
      </div>

      {/* Main Content Area */}
      <div className="flex-1 flex flex-col min-w-0 bg-background">
        <header className="h-10 bg-background border-b border-border flex items-center px-4 justify-between">
          <div className="flex items-center gap-2 text-sm text-foreground">
            <Terminal className="w-4 h-4 text-primary" />
            <span className="font-medium">Actions</span>
          </div>
          <div className="flex items-center gap-4">
             {status === 'running' && !isHistorical && (
              <div className="flex items-center gap-2 text-xs text-primary">
                <span className="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
                Executing...
              </div>
            )}
            
            <button
              onClick={() => setShowDebugger(!showDebugger)}
              className={cn(
                "flex items-center gap-1.5 text-xs px-2 py-1 rounded-sm border transition-colors",
                showDebugger 
                  ? "bg-primary/10 text-primary border-primary/20" 
                  : "bg-secondary text-muted-foreground border-border hover:text-foreground"
              )}
            >
              <Bug className="w-3 h-3" />
              Debugger
            </button>
          </div>
        </header>

        <main className="flex-1 overflow-hidden relative flex">
          <div className="flex-1 relative">
             <MessageList messages={messages} />
          </div>
          
          <DebuggerPanel 
            isOpen={showDebugger} 
            onClose={() => setShowDebugger(false)} 
            memory={conductorMemory} 
            taskId={taskId}
            onRefresh={() => taskId && refreshMemory(taskId)}
          />
        </main>
      </div>
    </div>
  );
}

function App() {
  return (
    <ThemeProvider defaultTheme="dark" storageKey="codeactor-theme">
      <AppContent />
    </ThemeProvider>
  );
}

export default App;
