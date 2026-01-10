package http

import (
	"context"
	"time"

	"codeactor/internal/memory"

	"github.com/olahol/melody"
)

// ========== Socket.IO 消息结构 ==========
type SocketMessage struct {
	Type    string      `json:"type"`
	Event   string      `json:"event"`
	Data    interface{} `json:"data"`
	From    string      `json:"from,omitempty"`
	TaskID  string      `json:"task_id,omitempty"`
	Message string      `json:"message,omitempty"`
}

type ChatMessage struct {
	Type      string `json:"type"` // "human" or "assistant"
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

type TaskUpdate struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
	Progress  string `json:"progress,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// ========== HTTP API 结构 ==========
type CodingTaskRequest struct {
	ProjectDir string `json:"project_dir"`
	TaskDesc   string `json:"task_desc"`
}

type CodingTaskResponse struct {
	TaskID string `json:"task_id"`
	Error  string `json:"error,omitempty"`
}

type MemoryResponse struct {
	Messages []memory.ChatMessage `json:"messages"`
	Size     int                  `json:"size"`
	MaxSize  int                  `json:"max_size"`
	Error    string               `json:"error,omitempty"`
}

type ClearMemoryResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Error   string `json:"error,omitempty"`
}

// ========== 任务管理相关结构 ==========
const (
	TaskStatusRunning   = "running"
	TaskStatusFinished  = "finished"
	TaskStatusFailed    = "failed"
	TaskStatusCancelled = "cancelled"
)

type Task struct {
	ID         string
	Status     string
	Result     string
	Error      string
	Progress   string
	ProjectDir string // 添加项目目录字段
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Memory     *memory.ConversationMemory
	Socket     *melody.Session    // 关联的WebSocket连接
	CancelFunc context.CancelFunc // 用于取消任务的函数
	Context    context.Context    // 任务执行的上下文
}
