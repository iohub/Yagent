package assistant

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/util"
	"codeactor/pkg/messaging"

	"github.com/rs/zerolog/log"
	// 引入 tools_embed.go 以获得 ToolsJSON
	_ "embed"
)

// 错误码常量定义
const (
	HTTPStatusTooManyRequests = 429              // 限流错误码
	MaxRetryWaitTime          = 5 * time.Minute  // 最大等待时间5分钟
	InitialRetryWaitTime      = 20 * time.Second // 初始等待时间20秒
)

// TaskRequest 封装任务请求的所有参数
type TaskRequest struct {
	Context     context.Context
	ProjectDir  string
	TaskDesc    string // 用于 ProcessCodingTask
	UserMessage string // 用于 ProcessConversation
	Memory      *ConversationMemory
	WSCallback  func(messageType string, content string)
	TaskID      string
	publisher   *MessagePublisher
}

// NewTaskRequest 创建新的任务请求
func NewTaskRequest(ctx context.Context, taskID string) *TaskRequest {
	return &TaskRequest{
		Context: ctx,
		TaskID:  taskID,
	}
}

// WithProjectDir 设置项目目录
func (tr *TaskRequest) WithProjectDir(projectDir string) *TaskRequest {
	tr.ProjectDir = projectDir
	return tr
}

// WithTaskDesc 设置任务描述（用于编程任务）
func (tr *TaskRequest) WithTaskDesc(taskDesc string) *TaskRequest {
	tr.TaskDesc = taskDesc
	return tr
}

// WithUserMessage 设置用户消息（用于对话）
func (tr *TaskRequest) WithUserMessage(userMessage string) *TaskRequest {
	tr.UserMessage = userMessage
	return tr
}

// WithMemory 设置对话记忆
func (tr *TaskRequest) WithMemory(memory *ConversationMemory) *TaskRequest {
	tr.Memory = memory
	return tr
}

// WithWSCallback 设置WebSocket回调函数
func (tr *TaskRequest) WithWSCallback(wsCallback func(messageType string, content string)) *TaskRequest {
	tr.WSCallback = wsCallback
	return tr
}

// WithMessagePublisher 设置消息发布者
func (tr *TaskRequest) WithMessagePublisher(publisher *MessagePublisher) *TaskRequest {
	tr.publisher = publisher
	return tr
}

type CodingAssistant struct {
	client        *Client
	workingDir    string
	systemPrompt  string
	enhancedTools *EnhancedToolManager // 使用增强的工具管理器
	dataManager   *DataManager         // 数据管理器，用于在home目录保存任务memory
	// 迭代控制相关字段
	currentIteration int
	maxIterations    int

	// 新增：各功能模块
	taskHandler         *TaskHandler
	conversationManager *ConversationManager
	rateLimiter         *RateLimiter
	subAgent            *SubAgent
	logger              *Logger

	// 用户回复通道映射
	userResponseChannels map[string]chan string
	mu                   sync.Mutex // 用于保护 userResponseChannels 的互斥锁
}

// NewCodingAssistant 创建新的编程助手实例
func NewCodingAssistant(client *Client) (*CodingAssistant, error) {
	// 初始化增强工具管理器
	enhancedTools, err := NewEnhancedToolManager()
	if err != nil {
		return nil, util.WrapError(context.Background(), err, "NewCodingAssistant::NewEnhancedToolManager")
	}

	// 创建数据管理器
	dataManager, err := NewDataManager()
	if err != nil {
		return nil, util.WrapError(context.Background(), err, "NewCodingAssistant::NewDataManager")
	}

	assistant := &CodingAssistant{
		client:               client,
		systemPrompt:         SystemPrompt,
		enhancedTools:        enhancedTools,
		currentIteration:     0,
		maxIterations:        150, // 默认最大迭代次数
		userResponseChannels: make(map[string]chan string),
		dataManager:          dataManager,
	}

	// 设置客户端对助手的引用
	client.assistant = assistant

	// 初始化各功能模块
	assistant.taskHandler = NewTaskHandler(assistant)
	assistant.conversationManager = NewConversationManager(assistant)
	assistant.rateLimiter = NewRateLimiter(assistant)
	assistant.subAgent = NewSubAgent(assistant)
	assistant.logger = NewLogger(assistant)

	// 设置工具管理器对助手的引用
	enhancedTools.SetAssistant(assistant)

	return assistant, nil
}

// IntegrateMessaging 集成消息系统
func (ca *CodingAssistant) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	publisher := NewMessagePublisher(dispatcher)
	ca.taskHandler.publisher = publisher
	ca.conversationManager.publisher = publisher
	ca.enhancedTools.publisher = publisher

	// Register a small consumer to route user responses back to the assistant
	dispatcher.RegisterConsumer(&userHelpRouter{assistant: ca})
}

// userHelpRouter routes user_help_response events back to the assistant
type userHelpRouter struct {
	assistant *CodingAssistant
}

func (r *userHelpRouter) Consume(event *messaging.MessageEvent) error {
	if event == nil {
		return nil
	}
	if event.Type != "user_help_response" {
		return nil
	}
	var taskID string
	var response string
	// Prefer metadata
	if event.Metadata != nil {
		if v, ok := event.Metadata["task_id"].(string); ok {
			taskID = v
		}
		if v, ok := event.Metadata["response"].(string); ok {
			response = v
		}
	}
	// Fallback to content map
	if m, ok := event.Content.(map[string]interface{}); ok {
		if taskID == "" {
			if v, ok := m["task_id"].(string); ok {
				taskID = v
			}
		}
		if response == "" {
			if v, ok := m["response"].(string); ok {
				response = v
			}
		}
	}
	if taskID != "" && response != "" {
		r.assistant.HandleUserResponse(taskID, response)
	}
	return nil
}

// ProcessCodingTask 处理编程任务
func (ca *CodingAssistant) ProcessCodingTask(request *TaskRequest) (string, error) {
	// 验证请求
	if err := ca.taskHandler.ValidateRequest(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessCodingTask::ValidateRequest")
	}

	// 设置任务环境
	if err := ca.taskHandler.SetupTaskEnvironment(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessCodingTask::SetupTaskEnvironment")
	}

	// 处理编程任务
	return ca.taskHandler.ProcessCodingTask(request)
}

// ProcessCodingTaskWithCallback 处理编程任务，支持 WebSocket 回调
func (ca *CodingAssistant) ProcessCodingTaskWithCallback(request *TaskRequest) (string, error) {
	// 验证请求
	if err := ca.taskHandler.ValidateRequest(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessCodingTaskWithCallback::ValidateRequest")
	}

	// 设置任务环境
	if err := ca.taskHandler.SetupTaskEnvironment(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessCodingTaskWithCallback::SetupTaskEnvironment")
	}

	// 处理编程任务
	return ca.taskHandler.ProcessCodingTask(request)
}

// ProcessConversation 处理持续对话，支持 WebSocket 回调
func (ca *CodingAssistant) ProcessConversation(request *TaskRequest) (string, error) {
	// 验证请求
	if err := ca.taskHandler.ValidateRequest(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessConversation::ValidateRequest")
	}

	// 设置任务环境
	if err := ca.taskHandler.SetupTaskEnvironment(request); err != nil {
		return "", util.WrapError(context.Background(), err, "ProcessConversation::SetupTaskEnvironment")
	}

	// 处理对话任务
	return ca.taskHandler.ProcessConversation(request)
}

// setupWorkingDirectory 设置工作目录
func (ca *CodingAssistant) setupWorkingDirectory(projectDir string) error {
	if projectDir == "" {
		return nil
	}

	// 验证项目目录是否存在
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		return err
	}

	// 获取绝对路径
	absProjectDir, err := filepath.Abs(projectDir)
	if err != nil {
		return err
	}

	ca.workingDir = absProjectDir
	if ca.enhancedTools != nil {
		ca.enhancedTools.SetWorkingDirectory(absProjectDir)
	}

	return nil
}

// prepareMemory 准备对话记忆
func (ca *CodingAssistant) prepareMemory(memory *ConversationMemory) *ConversationMemory {
	return ca.conversationManager.PrepareMemory(memory)
}

// SetWorkingDirectory 设置工作目录（供工具调用使用）
func (ca *CodingAssistant) SetWorkingDirectory(dir string) {
	ca.workingDir = dir
	// 当工作目录改变时，确保工具管理器也更新工作目录
	if ca.enhancedTools != nil {
		ca.enhancedTools.SetWorkingDirectory(dir)
	}
}

// GetWorkingDirectory 获取当前工作目录
func (ca *CodingAssistant) GetWorkingDirectory() string {
	return ca.workingDir
}

// GetCurrentSystemPrompt 获取当前包含工作目录的系统提示词
func (ca *CodingAssistant) GetCurrentSystemPrompt() string {
	return ca.generateSystemPrompt()
}

// SetMaxIterations 设置最大迭代次数
func (ca *CodingAssistant) SetMaxIterations(max int) {
	ca.maxIterations = max
}

// GetCurrentIteration 获取当前迭代次数
func (ca *CodingAssistant) GetCurrentIteration() int {
	return ca.currentIteration
}

// IsMaxIterationsReached 检查是否达到最大迭代次数
func (ca *CodingAssistant) IsMaxIterationsReached() bool {
	return ca.currentIteration >= ca.maxIterations
}

// IncrementIteration 增加迭代次数并检查是否超过限制
func (ca *CodingAssistant) IncrementIteration() bool {
	ca.currentIteration++
	log.Info().Int("iteration", ca.currentIteration).Msg("Processing conversation iteration")
	return ca.currentIteration <= ca.maxIterations
}

// generateSystemPrompt 生成包含工作目录的系统提示词
func (ca *CodingAssistant) generateSystemPrompt() string {
	// 简单的模板替换，将 {{.WorkingDir}} 替换为实际的工作目录
	workingDir := ca.workingDir
	if workingDir == "" {
		workingDir = "未设置"
	}

	// 使用 strings.ReplaceAll 进行简单的模板替换
	prompt := strings.ReplaceAll(SystemPrompt, "{{.WorkingDir}}", workingDir)
	return prompt
}

// isRateLimitError 检查错误是否为429限流错误
func (ca *CodingAssistant) isRateLimitError(err error) bool {
	return ca.rateLimiter.IsRateLimitError(err)
}

// handleRateLimitRetry 处理429限流错误的重试逻辑
func (ca *CodingAssistant) handleRateLimitRetry(ctx context.Context, wsCallback func(messageType string, content string)) error {
	return ca.rateLimiter.HandleRateLimitRetry(ctx, wsCallback)
}

// GetBedrockProviderInfo 获取Bedrock提供商信息
func (ca *CodingAssistant) GetBedrockProviderInfo(modelID string) string {
	if ca.client != nil && ca.client.config != nil {
		provider := config.DetectBedrockProvider(modelID)
		return fmt.Sprintf("Detected Bedrock provider: %s for model: %s", provider, modelID)
	}
	return ""
}
