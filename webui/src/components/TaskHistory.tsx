import React from 'react';

export interface Task {
  id: string;
  title: string;
  timestamp: Date;
  status: 'success' | 'failed' | 'terminated';
}

interface TaskHistoryProps {
  tasks: Task[];
  onTaskSelect?: (task: Task) => void;
}

export const TaskHistory: React.FC<TaskHistoryProps> = ({ tasks, onTaskSelect }) => {
  // 只获取最近的一个任务
  const latestTask = tasks.length > 0 ? tasks[0] : null;

  const getStatusIcon = (status: Task['status']) => {
    switch (status) {
      case 'success':
        return <span className="w-1.5 h-1.5 bg-green-500 rounded-full flex-shrink-0"></span>;
      case 'failed':
        return <span className="w-1.5 h-1.5 bg-red-500 rounded-full flex-shrink-0"></span>;
      case 'terminated':
        return <span className="w-1.5 h-1.5 bg-yellow-500 rounded-full flex-shrink-0"></span>;
      default:
        return <span className="w-1.5 h-1.5 bg-gray-500 rounded-full flex-shrink-0"></span>;
    }
  };

  const formatTime = (date: Date) => {
    const now = new Date();
    const diff = now.getTime() - date.getTime();
    const minutes = Math.floor(diff / 60000);
    const hours = Math.floor(diff / 3600000);
    const days = Math.floor(diff / 86400000);

    if (minutes < 1) return '刚刚';
    if (minutes < 60) return `${minutes}分钟前`;
    if (hours < 24) return `${hours}小时前`;
    return `${days}天前`;
  };

  const handleTaskClick = (task: Task) => {
    onTaskSelect?.(task);
  };

  return (
    <div className="bg-vscode-bg-secondary rounded px-2 py-1">
      {latestTask ? (
        <div
          onClick={() => handleTaskClick(latestTask)}
          className="flex items-center gap-1.5 cursor-pointer hover:bg-vscode-bg-tertiary rounded px-1.5 py-0.5 transition-colors duration-200"
          title="点击查看任务详情"
        >
          {getStatusIcon(latestTask.status)}
          <span className="text-xs text-vscode-text-primary truncate flex-1 leading-tight">
            {latestTask.title}
          </span>
          <span className="text-xs text-vscode-text-secondary">
            {formatTime(latestTask.timestamp)}
          </span>
        </div>
      ) : (
        <div className="text-center py-0.5">
          <p className="text-xs text-vscode-text-secondary">暂无历史任务</p>
        </div>
      )}
    </div>
  );
};