import React from 'react';
import { Settings, History, MessageSquarePlus } from 'lucide-react';

interface HeaderProps {
  onSettings: () => void;
  onHistory?: () => void;
  onNewChat?: () => void;
  isServerMode?: boolean;
  isServerConnected?: boolean;
  serverStatusText?: string;
}

export const Header: React.FC<HeaderProps> = ({
  onSettings,
  onHistory,
  onNewChat,
  isServerMode,
  isServerConnected,
  serverStatusText,
}) => {
  const statusText = serverStatusText || (isServerConnected ? '已连接到引擎' : '正在连接引擎...');

  return (
    <div className="flex items-center justify-between p-1.5 bg-vscode-bg-secondary border-b border-vscode-border">
      {/* 左侧 - 服务器连接状态 */}
      <div className="flex items-center gap-3">
        {isServerMode && (
          <div className="flex items-center gap-2">
            <div className={`w-2 h-2 rounded-full ${isServerConnected ? 'bg-green-500' : 'bg-yellow-500 animate-pulse'}`} />
            <span className="text-sm text-vscode-text-secondary">
              {statusText}
            </span>
          </div>
        )}
      </div>

      {/* 右侧 - 操作按钮 */}
      <div className="flex items-center gap-1">
        {onNewChat && (
          <button
            onClick={onNewChat}
            className="p-1 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-tertiary rounded transition-colors"
            title="新建对话"
          >
            <MessageSquarePlus className="w-4 h-4" />
          </button>
        )}
        {onHistory && (
          <button
            onClick={onHistory}
            className="p-1 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-tertiary rounded transition-colors"
            title="History"
          >
            <History className="w-4 h-4" />
          </button>
        )}
        <button
          onClick={onSettings}
          className="p-1 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-tertiary rounded transition-colors"
          title="Settings"
        >
          <Settings className="w-4 h-4" />
        </button>
      </div>
    </div>
  );
};