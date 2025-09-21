package agents

import (
	"context"
	"fmt"
	"strings"
)

// PlanningAgent 项目规划Agent
type PlanningAgent struct {
	*BaseAgent
	capabilities []string
}

// NewPlanningAgent 创建新的Planning Agent
func NewPlanningAgent() *PlanningAgent {
	return &PlanningAgent{
		BaseAgent: NewBaseAgent("planning_agent"),
		capabilities: []string{
			"project_planning",
			"task_decomposition",
			"milestone_planning",
			"resource_allocation",
			"timeline_estimation",
			"dependency_analysis",
			"risk_assessment",
			"progress_tracking",
		},
	}
}

// GetCapabilities 返回Planning Agent的能力列表
func (pa *PlanningAgent) GetCapabilities() []string {
	return pa.capabilities
}

// ExecuteTask 执行规划相关任务
func (pa *PlanningAgent) ExecuteTask(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	pa.SetStatus("running")
	defer pa.SetStatus("idle")

	// 发布任务开始事件
	pa.PublishEvent("task_started", map[string]interface{}{
		"agent":   pa.GetName(),
		"task_id": task.ID,
		"type":    task.Type,
	})

	var result *TaskResult
	var err error

	// 根据任务描述判断具体的规划操作类型
	taskDesc := strings.ToLower(task.Description)
	
	switch {
	case strings.Contains(taskDesc, "plan") || strings.Contains(taskDesc, "规划"):
		result, err = pa.createProjectPlan(ctx, task)
	case strings.Contains(taskDesc, "decompose") || strings.Contains(taskDesc, "分解"):
		result, err = pa.decomposeTask(ctx, task)
	case strings.Contains(taskDesc, "milestone") || strings.Contains(taskDesc, "里程碑"):
		result, err = pa.planMilestones(ctx, task)
	case strings.Contains(taskDesc, "estimate") || strings.Contains(taskDesc, "估算"):
		result, err = pa.estimateTimeline(ctx, task)
	case strings.Contains(taskDesc, "dependency") || strings.Contains(taskDesc, "依赖"):
		result, err = pa.analyzeDependencies(ctx, task)
	case strings.Contains(taskDesc, "risk") || strings.Contains(taskDesc, "风险"):
		result, err = pa.assessRisks(ctx, task)
	case strings.Contains(taskDesc, "progress") || strings.Contains(taskDesc, "进度"):
		result, err = pa.trackProgress(ctx, task)
	default:
		result, err = pa.handleGeneralPlanningTask(ctx, task)
	}

	// 发布任务完成事件
	status := "completed"
	if err != nil {
		status = "failed"
	}
	
	pa.PublishEvent("task_completed", map[string]interface{}{
		"agent":   pa.GetName(),
		"task_id": task.ID,
		"status":  status,
		"result":  result,
	})

	return result, err
}

// createProjectPlan 创建项目计划
func (pa *PlanningAgent) createProjectPlan(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟项目规划逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"plan_type": "project_planning",
			"phases": []map[string]interface{}{
				{"name": "需求分析", "duration": "1周", "tasks": []string{}},
				{"name": "设计阶段", "duration": "2周", "tasks": []string{}},
				{"name": "开发阶段", "duration": "4周", "tasks": []string{}},
				{"name": "测试阶段", "duration": "1周", "tasks": []string{}},
			},
			"total_duration": "8周",
		},
		Message: "项目计划创建完成",
	}, nil
}

// decomposeTask 任务分解
func (pa *PlanningAgent) decomposeTask(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟任务分解逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"decomposition_type": "task_decomposition",
			"original_task": task.Description,
			"subtasks": []map[string]interface{}{
				{"id": "subtask_1", "description": "子任务1", "priority": "high"},
				{"id": "subtask_2", "description": "子任务2", "priority": "medium"},
				{"id": "subtask_3", "description": "子任务3", "priority": "low"},
			},
		},
		Message: "任务分解完成",
	}, nil
}

// planMilestones 规划里程碑
func (pa *PlanningAgent) planMilestones(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟里程碑规划逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"milestone_type": "milestone_planning",
			"milestones": []map[string]interface{}{
				{"name": "MVP完成", "date": "2024-02-15", "deliverables": []string{}},
				{"name": "Beta版本", "date": "2024-03-01", "deliverables": []string{}},
				{"name": "正式发布", "date": "2024-03-15", "deliverables": []string{}},
			},
		},
		Message: "里程碑规划完成",
	}, nil
}

// estimateTimeline 估算时间线
func (pa *PlanningAgent) estimateTimeline(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟时间估算逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"estimation_type": "timeline_estimation",
			"estimated_duration": "6周",
			"confidence_level": "80%",
			"factors_considered": []string{"复杂度", "资源可用性", "风险因素"},
		},
		Message: "时间线估算完成",
	}, nil
}

// analyzeDependencies 分析依赖关系
func (pa *PlanningAgent) analyzeDependencies(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟依赖分析逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"analysis_type": "dependency_analysis",
			"dependencies": []map[string]interface{}{
				{"from": "任务A", "to": "任务B", "type": "finish_to_start"},
				{"from": "任务B", "to": "任务C", "type": "finish_to_start"},
			},
			"critical_path": []string{"任务A", "任务B", "任务C"},
		},
		Message: "依赖关系分析完成",
	}, nil
}

// assessRisks 风险评估
func (pa *PlanningAgent) assessRisks(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟风险评估逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"assessment_type": "risk_assessment",
			"risks": []map[string]interface{}{
				{"name": "技术风险", "probability": "medium", "impact": "high", "mitigation": "技术预研"},
				{"name": "资源风险", "probability": "low", "impact": "medium", "mitigation": "备用资源"},
			},
		},
		Message: "风险评估完成",
	}, nil
}

// trackProgress 跟踪进度
func (pa *PlanningAgent) trackProgress(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟进度跟踪逻辑
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"tracking_type": "progress_tracking",
			"overall_progress": "65%",
			"completed_tasks": 13,
			"remaining_tasks": 7,
			"on_schedule": true,
		},
		Message: "进度跟踪完成",
	}, nil
}

// handleGeneralPlanningTask 处理通用规划任务
func (pa *PlanningAgent) handleGeneralPlanningTask(ctx context.Context, task *AgentTask) (*TaskResult, error) {
	// 模拟通用规划任务处理
	return &TaskResult{
		Success: true,
		Data: map[string]interface{}{
			"task_type": "general_planning_task",
			"description": task.Description,
			"processed": true,
		},
		Message: fmt.Sprintf("通用规划任务处理完成: %s", task.Description),
	}, nil
}