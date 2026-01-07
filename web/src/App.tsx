import { useTask } from './hooks/useTask';
import { TaskForm } from './components/TaskForm';
import { MessageList } from './components/MessageList';
import { Code2, Activity, Terminal } from 'lucide-react';
import { cn } from './lib/utils';

function App() {
  const { taskId, status, messages, error, isLoading, startTask } = useTask();

  return (
    <div className="flex h-screen bg-gray-100 font-sans text-gray-900">
      {/* Sidebar / Configuration Area */}
      <div className="w-96 bg-white border-r border-gray-200 flex flex-col shadow-lg z-10">
        <div className="p-6 border-b border-gray-100">
          <div className="flex items-center gap-3 mb-1">
            <div className="bg-blue-600 p-2 rounded-lg">
              <Code2 className="w-6 h-6 text-white" />
            </div>
            <h1 className="text-xl font-bold text-gray-800 tracking-tight">CodeActor</h1>
          </div>
          <p className="text-sm text-gray-500 ml-1">AI-Powered Coding Agent</p>
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          <div className="space-y-8">
            <section>
              <h2 className="text-sm font-semibold text-gray-900 uppercase tracking-wider mb-4 flex items-center gap-2">
                <Terminal className="w-4 h-4" />
                New Task
              </h2>
              <TaskForm onSubmit={startTask} isLoading={isLoading} />
            </section>

            {taskId && (
              <section className="bg-gray-50 rounded-lg p-4 border border-gray-200">
                <h2 className="text-sm font-semibold text-gray-900 uppercase tracking-wider mb-3 flex items-center gap-2">
                  <Activity className="w-4 h-4" />
                  Status
                </h2>
                <div className="space-y-3">
                  <div className="flex justify-between items-center text-sm">
                    <span className="text-gray-500">Task ID</span>
                    <span className="font-mono text-xs bg-gray-200 px-2 py-1 rounded">{taskId.slice(0, 8)}...</span>
                  </div>
                  <div className="flex justify-between items-center text-sm">
                    <span className="text-gray-500">State</span>
                    <span className={cn(
                      "px-2 py-0.5 rounded-full text-xs font-medium uppercase",
                      {
                        'bg-blue-100 text-blue-700': status === 'running',
                        'bg-green-100 text-green-700': status === 'finished',
                        'bg-red-100 text-red-700': status === 'failed',
                      }
                    )}>
                      {status}
                    </span>
                  </div>
                </div>
              </section>
            )}

            {error && (
              <div className="p-4 bg-red-50 text-red-700 rounded-lg text-sm border border-red-100">
                {error}
              </div>
            )}
          </div>
        </div>
        
        <div className="p-4 border-t border-gray-100 text-xs text-center text-gray-400">
          v0.1.0 • Connected to Local Agent
        </div>
      </div>

      {/* Main Content Area */}
      <div className="flex-1 flex flex-col min-w-0 bg-gray-50/50">
        <header className="h-16 bg-white border-b border-gray-200 flex items-center px-6 justify-between shadow-sm">
          <h2 className="font-semibold text-gray-800">Task Execution Log</h2>
          {status === 'running' && (
            <div className="flex items-center gap-2 text-sm text-blue-600 animate-pulse">
              <span className="w-2 h-2 bg-blue-600 rounded-full"></span>
              Agent is working...
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
