import { useTask } from './hooks/useTask';
import { TaskForm } from './components/TaskForm';
import { MessageList } from './components/MessageList';
import { Activity, Terminal } from 'lucide-react';
import { cn } from './lib/utils';
import { ThemeProvider } from './components/theme-provider';
import { ThemeToggle } from './components/theme-toggle';

function AppContent() {
  const { taskId, status, messages, error, isLoading, startTask } = useTask();

  return (
    <div className="flex h-screen bg-background font-sans text-foreground overflow-hidden">
      {/* Sidebar / Configuration Area */}
      <div className="w-1/3 bg-card border-r border-border flex flex-col shadow-lg z-10">

        <div className="flex-1 overflow-y-auto p-4 custom-scrollbar">
          <div className="space-y-6">
            <section>
              <h2 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-3 flex items-center gap-2">
                <Terminal className="w-3 h-3" />
                CodeActor
              </h2>
              <TaskForm onSubmit={startTask} isLoading={isLoading} />
            </section>

            {taskId && (
              <section className="bg-secondary/30 rounded-sm p-3 border border-border">
                <h2 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-2 flex items-center gap-2">
                  <Activity className="w-3 h-3" />
                  Status
                </h2>
                <div className="space-y-2">
                  <div className="flex justify-between items-center text-xs">
                    <span className="text-muted-foreground">Task ID</span>
                    <span className="font-mono bg-muted px-1.5 py-0.5 rounded text-foreground">{taskId.slice(0, 8)}...</span>
                  </div>
                  <div className="flex justify-between items-center text-xs">
                    <span className="text-muted-foreground">State</span>
                    <span className={cn(
                      "px-1.5 py-0.5 rounded text-[10px] font-medium uppercase border",
                      {
                        'bg-primary/10 text-primary border-primary/20': status === 'running',
                        'bg-green-500/10 text-green-500 border-green-500/20': status === 'finished',
                        'bg-destructive/10 text-destructive border-destructive/20': status === 'failed',
                      }
                    )}>
                      {status}
                    </span>
                  </div>
                </div>
              </section>
            )}

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
          {status === 'running' && (
            <div className="flex items-center gap-2 text-xs text-primary">
              <span className="w-1.5 h-1.5 bg-primary rounded-full animate-pulse"></span>
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

function App() {
  return (
    <ThemeProvider defaultTheme="dark" storageKey="codeactor-theme">
      <AppContent />
    </ThemeProvider>
  );
}

export default App;
