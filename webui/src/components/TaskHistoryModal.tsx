import React from 'react';
import { X, Clock, CheckCircle, XCircle, AlertCircle, Calendar } from 'lucide-react';
import { Task } from './TaskHistory';

interface TaskHistoryModalProps {
  isOpen: boolean;
  onClose: () => void;
  tasks: Task[];
  onTaskSelect?: (task: Task) => void;
}

export const TaskHistoryModal: React.FC<TaskHistoryModalProps> = ({
  isOpen,
  onClose,
  tasks,
  onTaskSelect
}) => {
  if (!isOpen) return null;

  const getStatusIcon = (status: Task['status']) => {
    switch (status) {
      case 'success':
        return <CheckCircle className="w-5 h-5 text-green-500" />;
      case 'failed':
        return <XCircle className="w-5 h-5 text-red-500" />;
      case 'terminated':
        return <AlertCircle className="w-5 h-5 text-yellow-500" />;
      default:
        return <Clock className="w-5 h-5 text-gray-500" />;
    }
  };

  const getStatusText = (status: Task['status']) => {
    switch (status) {
      case 'success':
        return '成功';
      case 'failed':
        return '失败';
      case 'terminated':
        return '终止';
      default:
        return '未知';
    }
  };

  const formatDate = (date: Date) => {
    return date.toLocaleDateString('zh-CN', {
      year: 'numeric',
      month: 'long',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
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

  const groupedTasks = tasks.reduce((acc, task) => {
    const date = task.timestamp.toDateString();
    if (!acc[date]) {
      acc[date] = [];
    }
    acc[date].push(task);
    return acc;
  }, {} as Record<string, Task[]>);

  const handleTaskClick = (task: Task) => {
    onTaskSelect?.(task);
    onClose();
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4">
      <div className="bg-gradient-to-br from-vscode-bg-primary to-vscode-bg-tertiary border border-vscode-border rounded-xl shadow-2xl max-w-4xl w-full max-h-[80vh] backdrop-blur-sm">
        {/* 头部 */}
        <div className="flex items-center justify-between p-6 border-b border-vscode-border">
          <h2 className="text-lg font-semibold text-vscode-text-primary flex items-center gap-3">
            <Clock className="w-5 h-5" />
            历史任务记录
          </h2>
          <button
            onClick={onClose}
            className="p-2 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-secondary rounded-lg transition-all duration-200"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* 内容区域 */}
        <div className="p-6 overflow-y-auto max-h-[60vh]">
          {tasks.length === 0 ? (
            <div className="text-center py-12">
              <Clock className="w-12 h-12 text-vscode-text-secondary mx-auto mb-4" />
              <p className="text-vscode-text-secondary">暂无历史任务记录</p>
            </div>
          ) : (
            <div className="space-y-6">
              {Object.entries(groupedTasks)
                .sort(([a], [b]) => new Date(b).getTime() - new Date(a).getTime())
                .map(([date, dayTasks]) => (
                  <div key={date}>
                    <div className="flex items-center gap-2 mb-3">
                      <Calendar className="w-4 h-4 text-vscode-text-secondary" />
                      <h3 className="text-sm font-medium text-vscode-text-secondary">
                        {new Date(date).toLocaleDateString('zh-CN', {
                          year: 'numeric',
                          month: 'long',
                          day: 'numeric',
                          weekday: 'long'
                        })}
                      </h3>
                      <span className="text-xs text-vscode-text-secondary bg-vscode-bg-secondary px-2 py-1 rounded-full">
                        {dayTasks.length} 个任务
                      </span>
                    </div>
                    <div className="space-y-2">
                      {dayTasks.map((task) => (
                        <div
                          key={task.id}
                          onClick={() => handleTaskClick(task)}
                          className="p-4 rounded-lg border border-vscode-border cursor-pointer transition-all duration-200 hover:shadow-lg hover:scale-[1.01] bg-vscode-bg-primary hover:bg-vscode-bg-secondary"
                        >
                          <div className="flex items-center justify-between mb-3">
                            <div className="flex items-center gap-3">
                              {getStatusIcon(task.status)}
                              <h4 className="font-medium text-vscode-text-primary">
                                {task.title}
                              </h4>
                            </div>
                            <div className="text-right">
                              <div className="text-sm text-vscode-text-secondary">
                                {formatTime(task.timestamp)}
                              </div>
                              <div className="text-xs text-vscode-text-secondary">
                                {formatDate(task.timestamp)}
                              </div>
                            </div>
                          </div>
                          <div className="flex items-center justify-between">
                            <span className="text-sm font-medium px-3 py-1 rounded-full bg-vscode-bg-tertiary text-vscode-text-secondary">
                              {getStatusText(task.status)}
                            </span>
                            <div className="text-xs text-vscode-text-secondary">
                              任务ID: {task.id}
                            </div>
                          </div>
                        </div>
                      ))}
                    </div>
                  </div>
                ))}
            </div>
          )}
        </div>

        {/* 底部统计 */}
        {tasks.length > 0 && (
          <div className="p-6 border-t border-vscode-border bg-vscode-bg-secondary/50">
            <div className="flex items-center justify-between">
              <span className="text-sm text-vscode-text-secondary">
                总计 {tasks.length} 个任务
              </span>
              <div className="flex items-center gap-6">
                <div className="flex items-center gap-2">
                  <CheckCircle className="w-4 h-4 text-green-500" />
                  <span className="text-sm text-vscode-text-secondary">
                    成功 {tasks.filter(t => t.status === 'success').length}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <XCircle className="w-4 h-4 text-red-500" />
                  <span className="text-sm text-vscode-text-secondary">
                    失败 {tasks.filter(t => t.status === 'failed').length}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  <AlertCircle className="w-4 h-4 text-yellow-500" />
                  <span className="text-sm text-vscode-text-secondary">
                    终止 {tasks.filter(t => t.status === 'terminated').length}
                  </span>
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
};