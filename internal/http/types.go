package http

import (
	"context"
	"time"

	"yagent/internal/memory"
	"yagent/internal/pause"
	"yagent/internal/protocol"

	"github.com/olahol/melody"
)

// ========== Socket.IO 消息结构 ==========
// SocketMessage 是 AgentEventEnvelope 的类型别名，保持向后兼容
type SocketMessage = protocol.AgentEventEnvelope

// NewRealtimeMessage 创建一个标准实时消息
func NewRealtimeMessage(event protocol.EventType, data interface{}, from string, taskID string) protocol.AgentEventEnvelope {
	return protocol.AgentEventEnvelope{
		Type:   "realtime",
		Event:  string(event),
		Data:   data,
		From:   from,
		TaskId: taskID,
	}
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
	TaskID     string `json:"task_id,omitempty"` // 可选，用于多轮对话
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
	TaskStatusRunning = "running"
	// TaskStatusPaused 是"暂停闸门已按下"（docs/pause-resume-design.md §8.1）。
	// 加这一档是因为闸门**不产生** task_complete —— 执行只是停在 step 边界上，
	// ctx 还活着，所以旧的四个状态里没有一个能表达"停住但可继续"。
	TaskStatusPaused    = "paused"
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

// Gate 返回这个任务的暂停闸门 —— 从 Context 上读回来，不在结构体里另存一份。
//
// 只存一份是刻意的：闸门必须与"执行正在用的那棵 ctx"是同一个对象。分开存的话，
// 任何换了 Context 却忘了换闸门的地方（handleStartTask 的续跑分支就是这种地方）
// 都会让 /pause 停在一个没人在等的闸门上 —— 不报错，只是什么都没发生。
//
// 没有闸门的 ctx（非交互 --prompt、任何绕过 NewTaskContext 的构造）返回 nil，
// 调用方必须把它当成"这个任务停不了"并拒绝。
func (t *Task) Gate() *pause.Gate {
	if t == nil || t.Context == nil {
		return nil
	}
	return pause.From(t.Context)
}
