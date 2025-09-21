package agents

import (
	"context"
	"time"
)

// TaskType 定义任务类型
type TaskType string

const (
	TaskTypeCode       TaskType = "code"
	TaskTypePlanning   TaskType = "planning"
	TaskTypeRepository TaskType = "repository"
	TaskTypeGeneral    TaskType = "general"
)

// TaskStatus 定义任务状态
type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusRunning    TaskStatus = "running"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
)

// AgentTask 表示一个任务
type AgentTask struct {
	ID           string                 `json:"id"`
	Type         TaskType               `json:"type"`
	Description  string                 `json:"description"`
	Status       TaskStatus             `json:"status"`
	AssignedAgent string                `json:"assigned_agent"`
	Dependencies []string               `json:"dependencies"`
	Results      map[string]interface{} `json:"results"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	Context      map[string]interface{} `json:"context"`
}

// TaskResult 表示任务执行结果
type TaskResult struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Message string                 `json:"message"`
	Error   string                 `json:"error,omitempty"`
}

// AgentInterface 定义Agent的通用接口
type AgentInterface interface {
	// GetName 返回Agent名称
	GetName() string
	
	// GetCapabilities 返回Agent能力列表
	GetCapabilities() []string
	
	// ExecuteTask 执行任务
	ExecuteTask(ctx context.Context, task *AgentTask) (*TaskResult, error)
	
	// GetStatus 获取Agent状态
	GetStatus() string
	
	// SetPublisher 设置消息发布者
	SetPublisher(publisher MessagePublisher)
	
	// SetWorkingDirectory 设置工作目录
	SetWorkingDirectory(dir string)
	
	// GetWorkingDirectory 获取工作目录
	GetWorkingDirectory() string
}

// MessagePublisher 消息发布者接口（简化版）
type MessagePublisher interface {
	Publish(eventType string, data map[string]interface{}) error
}

// BaseAgent 基础Agent实现
type BaseAgent struct {
	name        string
	workingDir  string
	publisher   MessagePublisher
	status      string
}

// NewBaseAgent 创建基础Agent
func NewBaseAgent(name string) *BaseAgent {
	return &BaseAgent{
		name:   name,
		status: "idle",
	}
}

// GetName 返回Agent名称
func (ba *BaseAgent) GetName() string {
	return ba.name
}

// GetStatus 获取Agent状态
func (ba *BaseAgent) GetStatus() string {
	return ba.status
}

// SetPublisher 设置消息发布者
func (ba *BaseAgent) SetPublisher(publisher MessagePublisher) {
	ba.publisher = publisher
}

// SetWorkingDirectory 设置工作目录
func (ba *BaseAgent) SetWorkingDirectory(dir string) {
	ba.workingDir = dir
}

// GetWorkingDirectory 获取工作目录
func (ba *BaseAgent) GetWorkingDirectory() string {
	return ba.workingDir
}

// SetStatus 设置状态
func (ba *BaseAgent) SetStatus(status string) {
	ba.status = status
}

// PublishEvent 发布事件
func (ba *BaseAgent) PublishEvent(eventType string, data map[string]interface{}) {
	if ba.publisher != nil {
		ba.publisher.Publish(eventType, data)
	}
}