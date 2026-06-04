import React, { useState, useEffect, useMemo } from 'react';
import { Header } from './components/Header';
import { ChatMessages } from './components/ChatMessages';
import { ChatInput } from './components/ChatInput';
import { TaskHistory, Task } from './components/TaskHistory';
import { TaskHistoryModal } from './components/TaskHistoryModal';
import { SettingsPageFull } from './components/SettingsPageFull';
import { ArrowUpRight, ArrowDownLeft } from 'lucide-react';

// import { AIPanel } from './components/AIPanel';  // 不再使用AIPanel
import { useChat } from './hooks/conversation';
import { vscodeAPI } from './utils/vscode';
import { serverAPI } from './utils/serverApi';
import { configManager } from './utils/configManager';
import './styles/globals.css';

// DEBUG 模式检测 (react-scripts 使用 process.env)
const isDebugMode = process.env.REACT_APP_DEBUG === 'true' || new URLSearchParams(window.location.search).has('debug');

// 下载存储的消息
const handleDownloadMessages = () => {
  vscodeAPI.downloadStoredMessages();
};

// 上传消息文件并触发渲染
const handleUploadMessages = (
  file: File,
  onLoad: (messages: any[]) => void
) => {
  const reader = new FileReader();
  reader.onload = (e) => {
    try {
      const text = e.target?.result;
      if (typeof text !== 'string') return;
      const parsed = JSON.parse(text);
      const messages = Array.isArray(parsed) ? parsed : [];
      if (messages.length === 0) {
        console.warn('⚠️ 文件中没有有效消息');
        return;
      }
      onLoad(messages);
      console.log(`✅ 已加载 ${messages.length} 条消息`);
    } catch (err) {
      console.error('❌ 解析消息文件失败:', err);
    }
  };
  reader.readAsText(file);
};

function App() {
  const [isServerConnected, setIsServerConnected] = useState(false);
  const [serverStatusText, setServerStatusText] = useState<string | undefined>(undefined);

  const {
    chatState,
    taskState,
    sendMessage,
    clearChat,
    setMode,
    submitTask,
    terminateTask,
    handleMessageAction,
    loadMessages,
  } = useChat();

  // 检测是否在服务器模式
  const isServerMode = !vscodeAPI.isVSCodeEnvironment() &&
    (!!window.SERVER_URL || new URLSearchParams(window.location.search).has('server'));

  // 添加调试日志
  useEffect(() => {
    console.log('🔍 App - taskState changed:', taskState);
  }, [taskState]);

  useEffect(() => {
    console.log('🔍 App - chatState changed:', chatState);
  }, [chatState]);

  // 服务器连接状态检测
  useEffect(() => {
    if (isServerMode) {
      const checkConnection = () => {
        const connected = serverAPI.isConnected();
        setIsServerConnected(connected);
        // 连接成功后停止检查
        if (connected && checkConnectionInterval) {
          clearInterval(checkConnectionInterval);
        }
      };

      const checkConnectionInterval = setInterval(checkConnection, 500);

      // 订阅 ServerAPI 状态消息（用于状态栏/Toast 展示）
      const unsubscribe = serverAPI.onMessage((msg: any) => {
        if (msg?.type !== 'server_connection_status') return;

        const kind = msg?.data?.kind;
        if (kind === 'connected') {
          setServerStatusText(undefined);
          setIsServerConnected(true);
          return;
        }

        if (kind === 'connecting') {
          setServerStatusText('正在连接引擎...');
          return;
        }

        // 倒计时：显示在 Header
        if (kind === 'reconnect_countdown') {
          const remainingSec = msg?.data?.remainingSec;
          const attempt = msg?.data?.attempt;
          const max = msg?.data?.max;
          if (typeof remainingSec === 'number') {
            setServerStatusText(`连接失败，${remainingSec}s 后重连（${attempt}/${max}）`);
          } else {
            setServerStatusText('连接失败，正在准备重连...');
          }
          return;
        }

        if (kind === 'reconnect_give_up') {
          setServerStatusText('连接失败，已停止重连');
          setIsServerConnected(false);
          return;
        }

        if (kind === 'disconnected') {
          setServerStatusText('已断开连接');
          setIsServerConnected(false);
          return;
        }

        if (kind === 'websocket_open_timeout') {
          setServerStatusText('连接超时，准备重连...');
          setIsServerConnected(false);
          return;
        }

        if (kind === 'server url changed') {
          setServerStatusText('服务器地址已更新，正在重连...');
          setIsServerConnected(false);
          return;
        }
      });

      return () => {
        clearInterval(checkConnectionInterval);
        unsubscribe();
      };
    }
  }, [isServerMode]);

  const [showSettings, setShowSettings] = useState(false);
  const [showTaskHistoryModal, setShowTaskHistoryModal] = useState(false);

  // 计算全局累计 token 消耗
  const { totalInputTokens, totalOutputTokens } = useMemo(() => {
    let input = 0;
    let output = 0;
    for (const msg of chatState.messages) {
      if (msg.metadata?.usage) {
        input += msg.metadata.usage.input_tokens || 0;
        output += msg.metadata.usage.output_tokens || 0;
      }
    }
    return { totalInputTokens: input, totalOutputTokens: output };
  }, [chatState.messages]);

  // 初始化vscode API mock
  useEffect(() => {
    console.log('App initializing...');
    console.log('VSCode Environment:', vscodeAPI.isVSCodeEnvironment());
    console.log('Server Mode:', isServerMode);

    if (isServerMode) {
      console.log('Running in server mode - using Server API');
    } else if (!vscodeAPI.isVSCodeEnvironment()) {
      console.log('Running in browser preview mode - using VSCode API Mock');
    } else {
      console.log('Running in VSCode extension mode - using real VSCode API');
    }

    // 应用 vscode 主题 class 到 body（与 extension 的 themeChange 消息同步）
    const applyThemeClass = (theme: string) => {
      const body = document.body;
      body.classList.remove(
        'vscode-light',
        'vscode-dark',
        'vscode-high-contrast',
        'vscode-high-contrast-light'
      );
      const cls = `vscode-${theme}`;
      body.classList.add(cls);
    };

    // 设置配置消息监听器
    const handleMessage = (event: MessageEvent) => {
      const message = event.data;

      if (message.type === 'themeChange' && typeof message.theme === 'string') {
        applyThemeClass(message.theme);
        return;
      }

      if (message.type === 'configResponse') {
        if (message.config) {
          configManager.handleConfigResponse(message.config);
        } else if (message.metadata?.config) {
          configManager.handleConfigResponse(message.metadata.config);
        }
      }

      // Handle selection CodeLens action - add selected code as chat context
      if (message.type === 'selectionCodeLensAction' && message.action === 'add-in-chat') {
        // Dispatch a custom event that ChatInput can listen to
        const contextEvent = new CustomEvent('add-chat-context', {
          detail: {
            id: `selection-${Date.now()}`,
            type: 'selection',
            name: message.fileName || 'Selection',
            path: message.relativePath || '',
            code: message.code || '',
            language: message.language || '',
            startLine: message.startLine,
            endLine: message.endLine,
          }
        });
        window.dispatchEvent(contextEvent);
      }
    };

    window.addEventListener('message', handleMessage);

    // 请求初始配置
    configManager.loadConfig();

    return () => {
      window.removeEventListener('message', handleMessage);
    };
  }, []);

  // 模拟历史任务数据
  const [tasks] = useState<Task[]>([
    {
      id: 'task-001',
      title: '代码重构优化',
      timestamp: new Date(Date.now() - 1000 * 60 * 30), // 30分钟前
      status: 'success'
    },
    {
      id: 'task-002', 
      title: '单元测试生成',
      timestamp: new Date(Date.now() - 1000 * 60 * 60 * 2), // 2小时前
      status: 'failed'
    },
    {
      id: 'task-003',
      title: '代码审查分析',
      timestamp: new Date(Date.now() - 1000 * 60 * 60 * 3), // 3小时前
      status: 'terminated'
    },
    {
      id: 'task-004',
      title: '性能优化建议',
      timestamp: new Date(Date.now() - 1000 * 60 * 60 * 24), // 1天前
      status: 'success'
    },
    {
      id: 'task-005',
      title: 'Bug修复检测',
      timestamp: new Date(Date.now() - 1000 * 60 * 60 * 25), // 1天多前
      status: 'success'
    },
    {
      id: 'task-006',
      title: '文档生成',
      timestamp: new Date(Date.now() - 1000 * 60 * 60 * 48), // 2天前
      status: 'failed'
    }
  ]);
  // 移除侧边栏状态
  // const [sidebarOpen, setSidebarOpen] = useState(true);
  // const [activePanel, setActivePanel] = useState<'chat' | 'composer'>('chat');  // 移除模式切换状态
  // const [showAIPanel, setShowAIPanel] = useState(false);  // 不再使用右侧面板
  // 移除快速操作面板状态
  // const [showQuickActions, setShowQuickActions] = useState(false);

  const formatToken = (n: number | undefined): string => {
    if (n == null) return '0';
    if (n < 1000) return String(n);
    if (n < 10000) return (n / 1000).toFixed(1) + 'k';
    return Math.round(n / 1000) + 'k';
  };

  const hasTokens = totalInputTokens > 0 || totalOutputTokens > 0;

  const handleSettings = () => {
    setShowSettings(!showSettings);
  };

  const handleTaskSelect = (task: Task) => {
    console.log('Selected task:', task);
    // 这里可以添加任务重试或其他逻辑
  };

  const handleShowAllTasks = () => {
    setShowTaskHistoryModal(true);
  };

  // 移除快速操作面板切换功能
  // const handleToggleQuickActions = () => {
  //   setShowQuickActions(!showQuickActions);
  // };

  return (
    <div className="h-screen w-full flex flex-col bg-vscode-bg-primary text-vscode-text-primary m-0 p-0">
      <Header
        onSettings={handleSettings}
        onHistory={handleShowAllTasks}
        onNewChat={clearChat}
        // activePanel={activePanel}  // 移除模式切换props
        // onPanelSwitch={handlePanelSwitch}  // 移除模式切换props
        isServerMode={isServerMode}
        isServerConnected={isServerConnected}
        serverStatusText={serverStatusText}
      />

      {/* DEBUG 下载/上传按钮 */}
      {isDebugMode && (
        <div className="px-4 py-2 bg-yellow-900/30 border-b border-yellow-700 flex items-center gap-3">
          <span className="text-sm text-yellow-400 font-mono">[DEBUG]</span>
          <button
            onClick={handleDownloadMessages}
            className="px-3 py-1 text-xs bg-yellow-600 hover:bg-yellow-500 text-black rounded font-mono"
          >
            下载消息
          </button>
          <label className="px-3 py-1 text-xs bg-blue-600 hover:bg-blue-500 text-white rounded font-mono cursor-pointer">
            上传消息
            <input
              type="file"
              accept=".json"
              className="hidden"
              onChange={(e) => {
                const file = e.target.files?.[0];
                if (file) {
                  handleUploadMessages(file, loadMessages);
                }
                e.target.value = '';
              }}
            />
          </label>
        </div>
      )}

      <div className="flex-1 flex overflow-hidden">
        {/* 主内容区域 */}
        <div className="flex-1 flex flex-col relative">
          
          {/* 聊天消息区域 */}
          <div className="flex-1 overflow-y-auto">
            <ChatMessages 
              messages={chatState.messages}
              isProcessing={chatState.isProcessing}
              onMessageAction={handleMessageAction}
            />
          </div>

          {/* 输入框区域 - 移至底部 */}
          <div className="p-4 pt-0 bg-vscode-bg-primary z-10">
            {/* 全局 token 累计 badge — 输入区域右上角 */}
            {hasTokens && (
              <div className="flex justify-end">
                <div className="inline-flex items-center gap-2 px-2.5 py-1 rounded-md text-xs text-[var(--chat-text-secondary)]">
                  <span className="inline-flex items-center gap-1">
                    <ArrowUpRight className="w-3 h-3 text-blue-400/60" />
                    <span className="font-mono">{formatToken(totalInputTokens)}</span>
                  </span>
                  <span className="opacity-30">in</span>
                  <span className="inline-flex items-center gap-1">
                    <ArrowDownLeft className="w-3 h-3 text-purple-400/60" />
                    <span className="font-mono">{formatToken(totalOutputTokens)}</span>
                  </span>
                  <span className="opacity-30">out</span>
                </div>
              </div>
            )}
              <ChatInput
                onSendMessage={sendMessage}
                onClearChat={clearChat}
                onSettings={handleSettings}
                currentMode={chatState.currentMode}
                onModeChange={setMode}
                isProcessing={chatState.isProcessing}
                isTaskRunning={taskState.isTaskRunning}
                onAbort={terminateTask}
                onSubmitTask={submitTask}
              />
          </div>
          
        </div>
      </div>

      {/* 历史任务模态框 */}
      {showTaskHistoryModal && (
        <TaskHistoryModal
          isOpen={showTaskHistoryModal}
          onClose={() => setShowTaskHistoryModal(false)}
          tasks={tasks}
          onTaskSelect={handleTaskSelect}
        />
      )}

      {/* 设置页面 - 全页面模式 */}
      {showSettings && (
        <SettingsPageFull
          onBack={() => setShowSettings(false)}
        />
      )}
    </div>
  );
}

export default App;