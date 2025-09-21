package assistant

import (
	"context"
	"fmt"
	"sync"
	"time"

	"codeactor/internal/assistant/agents"
)

// DirectorCoordinator 负责协调和管理各个专业Agent
type DirectorCoordinator struct {
	agents       map[string]agents.AgentInterface
	taskQueue    chan *agents.AgentTask
	results      map[string]*agents.TaskResult
	resultsMutex sync.RWMutex
	running      bool
	stopChan     chan struct{}
	wg           sync.WaitGroup
}

// NewDirectorCoordinator 创建新的Director协调器
func NewDirectorCoordinator() *DirectorCoordinator {
	coordinator := &DirectorCoordinator{
		agents:    make(map[string]agents.AgentInterface),
		taskQueue: make(chan *agents.AgentTask, 100),
		results:   make(map[string]*agents.TaskResult),
		stopChan:  make(chan struct{}),
	}

	// 注册专业Agent
	coordinator.registerAgents()
	
	return coordinator
}

// registerAgents 注册所有专业Agent
func (dc *DirectorCoordinator) registerAgents() {
	// 注册Code Agent
	codeAgent := agents.NewCodeAgent()
	dc.agents["code_agent"] = codeAgent

	// 注册Planning Agent
	planningAgent := agents.NewPlanningAgent()
	dc.agents["planning_agent"] = planningAgent

	// 注册Repository Agent
	repositoryAgent := agents.NewRepositoryAgent()
	dc.agents["repository_agent"] = repositoryAgent
}

// Start 启动协调器
func (dc *DirectorCoordinator) Start() {
	dc.running = true
	dc.wg.Add(1)
	go dc.processTaskQueue()
}

// Stop 停止协调器
func (dc *DirectorCoordinator) Stop() {
	if dc.running {
		dc.running = false
		close(dc.stopChan)
		dc.wg.Wait()
	}
}

// processTaskQueue 处理任务队列
func (dc *DirectorCoordinator) processTaskQueue() {
	defer dc.wg.Done()

	for {
		select {
		case task := <-dc.taskQueue:
			dc.executeTask(task)
		case <-dc.stopChan:
			return
		}
	}
}

// executeTask 执行单个任务
func (dc *DirectorCoordinator) executeTask(task *agents.AgentTask) {
	// 根据任务类型选择合适的Agent
	agentName := dc.selectAgent(task)
	if agentName == "" {
		dc.storeResult(task.ID, &agents.TaskResult{
			Success: false,
			Message: "No suitable agent found for task",
			Error:   fmt.Sprintf("Task type %s not supported", task.Type),
		})
		return
	}

	agent, exists := dc.agents[agentName]
	if !exists {
		dc.storeResult(task.ID, &agents.TaskResult{
			Success: false,
			Message: "Agent not found",
			Error:   fmt.Sprintf("Agent %s not registered", agentName),
		})
		return
	}

	// 更新任务状态
	task.Status = agents.TaskStatusRunning
	task.AssignedAgent = agentName
	task.UpdatedAt = time.Now()

	// 执行任务
	ctx := context.Background()
	result, err := agent.ExecuteTask(ctx, task)
	
	if err != nil {
		result = &agents.TaskResult{
			Success: false,
			Message: "Task execution failed",
			Error:   err.Error(),
		}
		task.Status = agents.TaskStatusFailed
	} else {
		task.Status = agents.TaskStatusCompleted
	}

	task.UpdatedAt = time.Now()
	dc.storeResult(task.ID, result)
}

// selectAgent 根据任务类型选择合适的Agent
func (dc *DirectorCoordinator) selectAgent(task *agents.AgentTask) string {
	switch task.Type {
	case agents.TaskTypeCode:
		return "code_agent"
	case agents.TaskTypePlanning:
		return "planning_agent"
	case agents.TaskTypeRepository:
		return "repository_agent"
	default:
		// 根据任务描述进行智能选择
		return dc.intelligentAgentSelection(task)
	}
}

// intelligentAgentSelection 智能Agent选择
func (dc *DirectorCoordinator) intelligentAgentSelection(task *agents.AgentTask) string {
	description := task.Description
	
	// 代码相关关键词
	codeKeywords := []string{"code", "代码", "function", "函数", "class", "类", "bug", "错误", "refactor", "重构"}
	for _, keyword := range codeKeywords {
		if contains(description, keyword) {
			return "code_agent"
		}
	}

	// 规划相关关键词
	planningKeywords := []string{"plan", "规划", "schedule", "计划", "milestone", "里程碑", "timeline", "时间线"}
	for _, keyword := range planningKeywords {
		if contains(description, keyword) {
			return "planning_agent"
		}
	}

	// 仓库相关关键词
	repoKeywords := []string{"git", "branch", "分支", "commit", "提交", "merge", "合并", "repository", "仓库"}
	for _, keyword := range repoKeywords {
		if contains(description, keyword) {
			return "repository_agent"
		}
	}

	// 默认选择Code Agent
	return "code_agent"
}

// contains 检查字符串是否包含子字符串（不区分大小写）
func contains(s, substr string) bool {
	return len(s) >= len(substr) && 
		   (s == substr || 
		    (len(s) > len(substr) && 
		     (s[:len(substr)] == substr || 
		      s[len(s)-len(substr):] == substr ||
		      containsInMiddle(s, substr))))
}

// containsInMiddle 检查字符串中间是否包含子字符串
func containsInMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// storeResult 存储任务结果
func (dc *DirectorCoordinator) storeResult(taskID string, result *agents.TaskResult) {
	dc.resultsMutex.Lock()
	defer dc.resultsMutex.Unlock()
	dc.results[taskID] = result
}

// SubmitTask 提交任务到队列
func (dc *DirectorCoordinator) SubmitTask(task *agents.AgentTask) error {
	if !dc.running {
		return fmt.Errorf("coordinator is not running")
	}

	task.Status = agents.TaskStatusPending
	task.CreatedAt = time.Now()
	task.UpdatedAt = time.Now()

	select {
	case dc.taskQueue <- task:
		return nil
	default:
		return fmt.Errorf("task queue is full")
	}
}

// GetTaskResult 获取任务结果
func (dc *DirectorCoordinator) GetTaskResult(taskID string) (*agents.TaskResult, bool) {
	dc.resultsMutex.RLock()
	defer dc.resultsMutex.RUnlock()
	result, exists := dc.results[taskID]
	return result, exists
}

// GetAllResults 获取所有任务结果
func (dc *DirectorCoordinator) GetAllResults() map[string]*agents.TaskResult {
	dc.resultsMutex.RLock()
	defer dc.resultsMutex.RUnlock()
	
	results := make(map[string]*agents.TaskResult)
	for k, v := range dc.results {
		results[k] = v
	}
	return results
}

// GetAgentStatus 获取Agent状态
func (dc *DirectorCoordinator) GetAgentStatus() map[string]string {
	status := make(map[string]string)
	for name, agent := range dc.agents {
		status[name] = agent.GetStatus()
	}
	return status
}

// GetAgentCapabilities 获取Agent能力
func (dc *DirectorCoordinator) GetAgentCapabilities() map[string][]string {
	capabilities := make(map[string][]string)
	for name, agent := range dc.agents {
		capabilities[name] = agent.GetCapabilities()
	}
	return capabilities
}

// ExecuteCompoundTask 执行复合任务
func (dc *DirectorCoordinator) ExecuteCompoundTask(ctx context.Context, compoundTask *CompoundTask) (*CompoundTaskResult, error) {
	result := &CompoundTaskResult{
		TaskID:      compoundTask.ID,
		TaskName:    compoundTask.Name,
		Status:      "running",
		SubResults:  make(map[string]*agents.TaskResult),
		StartTime:   time.Now(),
	}

	// 根据执行策略处理子任务
	switch compoundTask.ExecutionStrategy {
	case "sequential":
		err := dc.executeSequential(ctx, compoundTask, result)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
		} else {
			result.Status = "completed"
		}
	case "parallel":
		err := dc.executeParallel(ctx, compoundTask, result)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
		} else {
			result.Status = "completed"
		}
	case "mixed":
		err := dc.executeMixed(ctx, compoundTask, result)
		if err != nil {
			result.Status = "failed"
			result.Error = err.Error()
		} else {
			result.Status = "completed"
		}
	default:
		result.Status = "failed"
		result.Error = "Unknown execution strategy"
	}

	result.EndTime = time.Now()
	result.Duration = result.EndTime.Sub(result.StartTime)

	return result, nil
}

// executeSequential 顺序执行子任务
func (dc *DirectorCoordinator) executeSequential(ctx context.Context, compoundTask *CompoundTask, result *CompoundTaskResult) error {
	for _, subtask := range compoundTask.SubTasks {
		agentTask := &agents.AgentTask{
			ID:          subtask.ID,
			Type:        dc.getTaskType(subtask.AssignedAgent),
			Description: subtask.Description,
			Context:     subtask.Context,
		}

		err := dc.SubmitTask(agentTask)
		if err != nil {
			return fmt.Errorf("failed to submit subtask %s: %w", subtask.ID, err)
		}

		// 等待任务完成
		for {
			taskResult, exists := dc.GetTaskResult(subtask.ID)
			if exists {
				result.SubResults[subtask.ID] = taskResult
				if !taskResult.Success {
					return fmt.Errorf("subtask %s failed: %s", subtask.ID, taskResult.Error)
				}
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	return nil
}

// executeParallel 并行执行子任务
func (dc *DirectorCoordinator) executeParallel(ctx context.Context, compoundTask *CompoundTask, result *CompoundTaskResult) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(compoundTask.SubTasks))

	for _, subtask := range compoundTask.SubTasks {
		wg.Add(1)
		go func(st SubTask) {
			defer wg.Done()

			agentTask := &agents.AgentTask{
				ID:          st.ID,
				Type:        dc.getTaskType(st.AssignedAgent),
				Description: st.Description,
				Context:     st.Context,
			}

			err := dc.SubmitTask(agentTask)
			if err != nil {
				errChan <- fmt.Errorf("failed to submit subtask %s: %w", st.ID, err)
				return
			}

			// 等待任务完成
			for {
				taskResult, exists := dc.GetTaskResult(st.ID)
				if exists {
					dc.resultsMutex.Lock()
					result.SubResults[st.ID] = taskResult
					dc.resultsMutex.Unlock()
					
					if !taskResult.Success {
						errChan <- fmt.Errorf("subtask %s failed: %s", st.ID, taskResult.Error)
					}
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(subtask)
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	return nil
}

// executeMixed 混合执行子任务（根据依赖关系）
func (dc *DirectorCoordinator) executeMixed(ctx context.Context, compoundTask *CompoundTask, result *CompoundTaskResult) error {
	// 简化实现：先执行没有依赖的任务，然后按依赖顺序执行
	completed := make(map[string]bool)
	
	for len(completed) < len(compoundTask.SubTasks) {
		progress := false
		
		for _, subtask := range compoundTask.SubTasks {
			if completed[subtask.ID] {
				continue
			}

			// 检查依赖是否都已完成
			canExecute := true
			for _, dep := range subtask.Dependencies {
				if !completed[dep] {
					canExecute = false
					break
				}
			}

			if canExecute {
				agentTask := &agents.AgentTask{
					ID:          subtask.ID,
					Type:        dc.getTaskType(subtask.AssignedAgent),
					Description: subtask.Description,
					Context:     subtask.Context,
				}

				err := dc.SubmitTask(agentTask)
				if err != nil {
					return fmt.Errorf("failed to submit subtask %s: %w", subtask.ID, err)
				}

				// 等待任务完成
				for {
					taskResult, exists := dc.GetTaskResult(subtask.ID)
					if exists {
						result.SubResults[subtask.ID] = taskResult
						if !taskResult.Success {
							return fmt.Errorf("subtask %s failed: %s", subtask.ID, taskResult.Error)
						}
						completed[subtask.ID] = true
						progress = true
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}

		if !progress {
			return fmt.Errorf("circular dependency detected or unresolvable dependencies")
		}
	}

	return nil
}

// getTaskType 根据Agent名称获取任务类型
func (dc *DirectorCoordinator) getTaskType(agentName string) agents.TaskType {
	switch agentName {
	case "code_agent":
		return agents.TaskTypeCode
	case "planning_agent":
		return agents.TaskTypePlanning
	case "repository_agent":
		return agents.TaskTypeRepository
	default:
		return agents.TaskTypeGeneral
	}
}

// CompoundTask 复合任务定义
type CompoundTask struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	SubTasks          []SubTask `json:"subtasks"`
	ExecutionStrategy string    `json:"execution_strategy"`
	CreatedAt         time.Time `json:"created_at"`
}

// SubTask 子任务定义
type SubTask struct {
	ID            string                 `json:"id"`
	Description   string                 `json:"description"`
	AssignedAgent string                 `json:"assigned_agent"`
	Dependencies  []string               `json:"dependencies"`
	Priority      string                 `json:"priority"`
	Context       map[string]interface{} `json:"context"`
}

// CompoundTaskResult 复合任务结果
type CompoundTaskResult struct {
	TaskID     string                            `json:"task_id"`
	TaskName   string                            `json:"task_name"`
	Status     string                            `json:"status"`
	SubResults map[string]*agents.TaskResult     `json:"sub_results"`
	StartTime  time.Time                         `json:"start_time"`
	EndTime    time.Time                         `json:"end_time"`
	Duration   time.Duration                     `json:"duration"`
	Error      string                            `json:"error,omitempty"`
}