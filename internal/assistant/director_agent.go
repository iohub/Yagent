package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"codeactor/pkg/messaging"

	"github.com/rs/zerolog/log"
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

// AgentInterface 定义Agent的通用接口
type AgentInterface interface {
	GetName() string
	GetCapabilities() []string
	ExecuteTask(ctx context.Context, task *AgentTask) (*TaskResult, error)
	GetStatus() string
	SetPublisher(publisher *MessagePublisher)
}

// TaskResult 表示任务执行结果
type TaskResult struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Message string                 `json:"message"`
	Error   string                 `json:"error,omitempty"`
}

// DirectorAgent 主协调者Agent
type DirectorAgent struct {
	client              *Client
	conversationMemory  *ConversationMemory
	workingDir          string
	enhancedTools       *EnhancedToolManager
	
	// 专业化Agent
	codeAgent       AgentInterface
	planningAgent   AgentInterface
	repositoryAgent AgentInterface
	
	// 任务管理
	tasks           map[string]*AgentTask
	tasksMutex      sync.RWMutex
	
	// 消息系统
	publisher       *MessagePublisher
	
	// 配置
	maxConcurrentTasks int
	defaultTimeout     time.Duration
	
	// 状态
	isRunning       bool
	runningMutex    sync.RWMutex
}

// NewDirectorAgent 创建新的Director Agent
func NewDirectorAgent(client *Client) (*DirectorAgent, error) {
	// 初始化增强工具管理器
	enhancedTools, err := NewEnhancedToolManager()
	if err != nil {
		return nil, fmt.Errorf("failed to create enhanced tool manager: %w", err)
	}

	director := &DirectorAgent{
		client:             client,
		conversationMemory: NewConversationMemory(200), // 更大的内存容量
		enhancedTools:      enhancedTools,
		tasks:              make(map[string]*AgentTask),
		maxConcurrentTasks: 5,
		defaultTimeout:     10 * time.Minute,
		isRunning:          false,
	}

	// 设置工具管理器对Director的引用
	enhancedTools.SetAssistant(&CodingAssistant{
		client:        client,
		enhancedTools: enhancedTools,
	})

	return director, nil
}

// IntegrateMessaging 集成消息系统
func (da *DirectorAgent) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	da.publisher = NewMessagePublisher(dispatcher)
	da.enhancedTools.publisher = da.publisher
}

// SetWorkingDirectory 设置工作目录
func (da *DirectorAgent) SetWorkingDirectory(dir string) {
	da.workingDir = dir
	da.enhancedTools.SetWorkingDirectory(dir)
}

// GetWorkingDirectory 获取工作目录
func (da *DirectorAgent) GetWorkingDirectory() string {
	return da.workingDir
}

// RegisterAgent 注册专业化Agent
func (da *DirectorAgent) RegisterAgent(agentType TaskType, agent AgentInterface) {
	switch agentType {
	case TaskTypeCode:
		da.codeAgent = agent
	case TaskTypePlanning:
		da.planningAgent = agent
	case TaskTypeRepository:
		da.repositoryAgent = agent
	}
	
	if da.publisher != nil {
		agent.SetPublisher(da.publisher)
	}
}

// ProcessUserRequest 处理用户请求
func (da *DirectorAgent) ProcessUserRequest(ctx context.Context, request *TaskRequest) (string, error) {
	da.runningMutex.Lock()
	da.isRunning = true
	da.runningMutex.Unlock()
	
	defer func() {
		da.runningMutex.Lock()
		da.isRunning = false
		da.runningMutex.Unlock()
	}()

	log.Info().
		Str("task_id", request.TaskID).
		Str("user_message", request.UserMessage).
		Msg("Director Agent processing user request")

	// 添加用户消息到对话记忆
	if request.Memory != nil {
		da.conversationMemory = request.Memory
	}
	da.conversationMemory.AddHumanMessage(request.UserMessage)

	// 分析用户意图并制定执行计划
	plan, err := da.analyzeAndPlan(ctx, request.UserMessage)
	if err != nil {
		return "", fmt.Errorf("failed to analyze and plan: %w", err)
	}

	// 执行计划
	result, err := da.executePlan(ctx, plan, request)
	if err != nil {
		return "", fmt.Errorf("failed to execute plan: %w", err)
	}

	// 添加助手回复到对话记忆
	da.conversationMemory.AddAssistantMessage(result, nil)

	return result, nil
}

// analyzeAndPlan 分析用户意图并制定执行计划
func (da *DirectorAgent) analyzeAndPlan(ctx context.Context, userMessage string) (*ExecutionPlan, error) {
	// Use LLM to analyze user intent
	systemPrompt := `You are a Director Agent responsible for analyzing user requests and creating execution plans.

You need to:
1. Understand the user's intent and requirements
2. Determine which specialized Agents should participate (code, planning, repository)
3. Create execution steps and dependencies
4. Return a structured execution plan

Available specialized Agents:
- Code Agent: Responsible for code generation, debugging, testing, refactoring
- Planning Agent: Responsible for technical analysis, architecture design, implementation planning
- Repository Agent: Responsible for codebase analysis, documentation generation, architecture understanding

Please return the execution plan in JSON format.`

	memory := NewConversationMemory(50)
	memory.AddSystemMessage(systemPrompt)
	memory.AddHumanMessage(fmt.Sprintf("User request: %s\n\nPlease analyze this request and create an execution plan.", userMessage))

	messages := memory.ToLangChainMessages()
	response, err := da.client.GenerateCompletionWithTools(ctx, messages, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate plan: %w", err)
	}

	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// 解析执行计划
	plan, err := da.parseExecutionPlan(response.Choices[0].Content)
	if err != nil {
		// 如果解析失败，创建一个默认计划
		log.Warn().Err(err).Msg("Failed to parse execution plan, creating default plan")
		plan = da.createDefaultPlan(userMessage)
	}

	return plan, nil
}

// ExecutionPlan 执行计划
type ExecutionPlan struct {
	Steps []ExecutionStep `json:"steps"`
}

// ExecutionStep 执行步骤
type ExecutionStep struct {
	ID           string            `json:"id"`
	AgentType    TaskType          `json:"agent_type"`
	Description  string            `json:"description"`
	Dependencies []string          `json:"dependencies"`
	Context      map[string]string `json:"context"`
}

// parseExecutionPlan 解析执行计划
func (da *DirectorAgent) parseExecutionPlan(content string) (*ExecutionPlan, error) {
	var plan ExecutionPlan
	err := json.Unmarshal([]byte(content), &plan)
	if err != nil {
		return nil, fmt.Errorf("failed to parse execution plan: %w", err)
	}
	return &plan, nil
}

// createDefaultPlan 创建默认执行计划
func (da *DirectorAgent) createDefaultPlan(userMessage string) *ExecutionPlan {
	return &ExecutionPlan{
		Steps: []ExecutionStep{
			{
				ID:          "step_1",
				AgentType:   TaskTypeCode,
				Description: userMessage,
				Dependencies: []string{},
				Context:     map[string]string{"original_request": userMessage},
			},
		},
	}
}

// executePlan 执行计划
func (da *DirectorAgent) executePlan(ctx context.Context, plan *ExecutionPlan, request *TaskRequest) (string, error) {
	results := make(map[string]*TaskResult)
	
	for _, step := range plan.Steps {
		// 检查依赖是否完成
		for _, dep := range step.Dependencies {
			if result, exists := results[dep]; !exists || !result.Success {
				return "", fmt.Errorf("dependency %s not completed successfully", dep)
			}
		}

		// 创建任务
		task := &AgentTask{
			ID:          step.ID,
			Type:        step.AgentType,
			Description: step.Description,
			Status:      TaskStatusPending,
			Context:     make(map[string]interface{}),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		// 添加上下文
		for k, v := range step.Context {
			task.Context[k] = v
		}

		// 执行任务
		result, err := da.executeTask(ctx, task, request)
		if err != nil {
			return "", fmt.Errorf("failed to execute task %s: %w", step.ID, err)
		}

		results[step.ID] = result
	}

	// 整合结果
	return da.aggregateResults(results), nil
}

// executeTask 执行单个任务
func (da *DirectorAgent) executeTask(ctx context.Context, task *AgentTask, request *TaskRequest) (*TaskResult, error) {
	var agent AgentInterface
	
	switch task.Type {
	case TaskTypeCode:
		agent = da.codeAgent
	case TaskTypePlanning:
		agent = da.planningAgent
	case TaskTypeRepository:
		agent = da.repositoryAgent
	default:
		// 如果没有专门的Agent，使用现有的CodingAssistant
		return da.executeWithCodingAssistant(ctx, task, request)
	}

	if agent == nil {
		// 如果专门的Agent不存在，使用现有的CodingAssistant
		return da.executeWithCodingAssistant(ctx, task, request)
	}

	// 更新任务状态
	task.Status = TaskStatusRunning
	task.UpdatedAt = time.Now()
	da.storeTask(task)

	// 执行任务
	result, err := agent.ExecuteTask(ctx, task)
	if err != nil {
		task.Status = TaskStatusFailed
		task.UpdatedAt = time.Now()
		da.storeTask(task)
		return nil, err
	}

	// 更新任务状态
	task.Status = TaskStatusCompleted
	task.Results = result.Data
	task.UpdatedAt = time.Now()
	da.storeTask(task)

	return result, nil
}

// executeWithCodingAssistant 使用现有的CodingAssistant执行任务
func (da *DirectorAgent) executeWithCodingAssistant(ctx context.Context, task *AgentTask, request *TaskRequest) (*TaskResult, error) {
	// 创建一个临时的CodingAssistant来执行任务
	assistant, err := NewCodingAssistant(da.client)
	if err != nil {
		return nil, fmt.Errorf("failed to create coding assistant: %w", err)
	}

	assistant.SetWorkingDirectory(da.workingDir)
	if da.publisher != nil {
		assistant.IntegrateMessaging(&messaging.MessageDispatcher{})
	}

	// 创建任务请求
	taskRequest := NewTaskRequest(ctx, task.ID).
		WithProjectDir(da.workingDir).
		WithTaskDesc(task.Description).
		WithMemory(da.conversationMemory).
		WithWSCallback(request.WSCallback)

	// 执行任务
	result, err := assistant.ProcessCodingTask(taskRequest)
	if err != nil {
		return &TaskResult{
			Success: false,
			Error:   err.Error(),
			Message: "Task execution failed",
		}, nil
	}

	return &TaskResult{
		Success: true,
		Data:    map[string]interface{}{"result": result},
		Message: "Task completed successfully",
	}, nil
}

// storeTask 存储任务
func (da *DirectorAgent) storeTask(task *AgentTask) {
	da.tasksMutex.Lock()
	defer da.tasksMutex.Unlock()
	da.tasks[task.ID] = task
}

// GetTask 获取任务
func (da *DirectorAgent) GetTask(taskID string) (*AgentTask, bool) {
	da.tasksMutex.RLock()
	defer da.tasksMutex.RUnlock()
	task, exists := da.tasks[taskID]
	return task, exists
}

// aggregateResults 整合结果
func (da *DirectorAgent) aggregateResults(results map[string]*TaskResult) string {
	var finalResult strings.Builder
	
	finalResult.WriteString("Task execution completed, results as follows:\n\n")
	
	for stepID, result := range results {
		finalResult.WriteString(fmt.Sprintf("Step %s:\n", stepID))
		if result.Success {
			finalResult.WriteString(fmt.Sprintf("✅ Success: %s\n", result.Message))
			if data, ok := result.Data["result"].(string); ok && data != "" {
				finalResult.WriteString(fmt.Sprintf("Result: %s\n", data))
			}
		} else {
			finalResult.WriteString(fmt.Sprintf("❌ Failed: %s\n", result.Error))
		}
		finalResult.WriteString("\n")
	}
	
	return finalResult.String()
}

// IsRunning 检查是否正在运行
func (da *DirectorAgent) IsRunning() bool {
	da.runningMutex.RLock()
	defer da.runningMutex.RUnlock()
	return da.isRunning
}

// GetStatus 获取状态
func (da *DirectorAgent) GetStatus() string {
	if da.IsRunning() {
		return "running"
	}
	return "idle"
}