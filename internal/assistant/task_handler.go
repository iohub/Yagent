package assistant

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

// TaskHandler 负责任务请求的处理和验证
type TaskHandler struct {
	assistant *CodingAssistant
	publisher *MessagePublisher
}

// NewTaskHandler 创建新的任务处理器
func NewTaskHandler(assistant *CodingAssistant) *TaskHandler {
	return &TaskHandler{
		assistant: assistant,
	}
}

// ProcessCodingTask 处理编程任务
func (th *TaskHandler) ProcessCodingTask(request *TaskRequest) (string, error) {
	if request.TaskDesc == "" {
		return "", fmt.Errorf("task description is required for coding task")
	}

	// 准备对话记忆
	memory := th.assistant.prepareMemory(request.Memory)

	// 添加用户消息
	memory.AddHumanMessage(request.TaskDesc)

	// 处理对话
	result, err := th.assistant.conversationManager.ProcessConversation(request.Context, memory, request.TaskID)
	if err != nil {
		return "", err
	}

	// 保存最终的memory到home目录的隐藏数据目录
	if th.assistant.dataManager != nil {
		if err := th.assistant.dataManager.SaveTaskMemory(request.TaskID, memory); err != nil {
			log.Warn().Err(err).Str("task_id", request.TaskID).Msg("Failed to save task memory to home directory")
		}
	}

	return result, nil
}

// ProcessConversation 处理对话任务
func (th *TaskHandler) ProcessConversation(request *TaskRequest) (string, error) {
	if request.UserMessage == "" {
		return "", fmt.Errorf("user message is required for conversation")
	}

	// 准备对话记忆
	memory := th.assistant.prepareMemory(request.Memory)

	// 添加用户消息
	memory.AddHumanMessage(request.UserMessage)

	// 处理对话
	return th.assistant.conversationManager.ProcessConversation(request.Context, memory, request.TaskID)
}

// ValidateRequest 验证任务请求的完整性
func (th *TaskHandler) ValidateRequest(request *TaskRequest) error {
	if request.Context == nil {
		return fmt.Errorf("context is required")
	}
	if request.TaskID == "" {
		return fmt.Errorf("task ID is required")
	}
	return nil
}

// SetupTaskEnvironment 设置任务执行环境
func (th *TaskHandler) SetupTaskEnvironment(request *TaskRequest) error {
	if request.ProjectDir != "" {
		if err := th.assistant.setupWorkingDirectory(request.ProjectDir); err != nil {
			return fmt.Errorf("failed to setup working directory: %w", err)
		}
	}
	return nil
}
