package conductor

import (
	"context"
	"fmt"
	"log/slog"
)

// Planner 负责任务分解与调度策略。
// 职责：
// 1. 将复杂任务分解为可执行步骤
// 2. 管理步骤间的依赖关系
// 3. 提供调度决策（并行/串行执行）
type Planner struct {
	maxDepth int
}

// Plan 是分解后的执行计划。
type Plan struct {
	Task            string
	Steps           []Step
	DependencyOrder []string // 拓扑排序的步骤 ID 列表
}

// Step 是计划中的单个执行步骤。
type Step struct {
	ID        string
	AgentType string   // 建议的 Agent 类型
	Task      string   // 子任务描述
	DependsOn []string // 依赖的步骤 ID 列表
	Priority  int      // 优先级（数值越低优先级越高）
	Retryable bool     // 是否可重试
}

// ExecutionMode 执行模式。
type ExecutionMode int

const (
	ModeSequential ExecutionMode = iota // 串行执行
	ModeParallel                        // 并行执行
	ModeDAG                             // 按依赖图执行
)

// NewPlanner 创建规划器。
func NewPlanner(maxDepth int) *Planner {
	if maxDepth <= 0 {
		maxDepth = 5
	}
	return &Planner{maxDepth: maxDepth}
}

// Decompose 将复杂任务分解为可执行步骤。
// 当前简化实现：单步骤计划。
// TODO: 集成 LLM 驱动的智能任务分解。
func (p *Planner) Decompose(ctx context.Context, task string) (*Plan, error) {
	if task == "" {
		return nil, fmt.Errorf("empty task")
	}

	plan := &Plan{
		Task: task,
		Steps: []Step{
			{
				ID:        "step_1",
				AgentType: "auto",
				Task:      task,
				Retryable: true,
				Priority:  0,
			},
		},
		DependencyOrder: []string{"step_1"},
	}

	slog.Debug("[Conductor] Task decomposed into plan",
		"task", truncateString(task, 100),
		"steps", len(plan.Steps))

	return plan, nil
}

// Schedule 对步骤进行排序，决定执行顺序。
func (p *Planner) Schedule(plan *Plan) []Step {
	if plan == nil || len(plan.Steps) == 0 {
		return nil
	}

	// 按依赖关系拓扑排序
	ordered := topologicalSort(plan.Steps)
	plan.DependencyOrder = ordered

	// 按 DependencyOrder 返回步骤
	stepMap := make(map[string]Step, len(plan.Steps))
	for _, step := range plan.Steps {
		stepMap[step.ID] = step
	}

	result := make([]Step, 0, len(ordered))
	for _, id := range ordered {
		if step, ok := stepMap[id]; ok {
			result = append(result, step)
		}
	}

	return result
}

// Replan 在失败后重新规划。返回调整后的计划。
func (p *Planner) Replan(ctx context.Context, original *Plan, failedStep *Step) (*Plan, error) {
	if original == nil {
		return nil, fmt.Errorf("original plan is nil")
	}

	slog.Warn("[Conductor] Replanning after step failure",
		"failed_step", failedStep.ID,
		"task", failedStep.Task)

	// 简化实现：移除失败步骤的依赖关系，其余步骤继续
	newPlan := &Plan{
		Task:  original.Task,
		Steps: make([]Step, 0, len(original.Steps)),
	}

	for _, step := range original.Steps {
		if step.ID == failedStep.ID {
			continue // 跳过失败步骤
		}
		// 移除对失败步骤的依赖
		filteredDeps := make([]string, 0, len(step.DependsOn))
		for _, dep := range step.DependsOn {
			if dep != failedStep.ID {
				filteredDeps = append(filteredDeps, dep)
			}
		}
		step.DependsOn = filteredDeps
		newPlan.Steps = append(newPlan.Steps, step)
	}

	newPlan.DependencyOrder = topologicalSort(newPlan.Steps)
	return newPlan, nil
}

// ShouldParallelize 判断步骤是否可以并行执行。
func (p *Planner) ShouldParallelize(steps []Step) bool {
	if len(steps) < 2 {
		return false
	}

	// 如果步骤之间没有依赖关系，可以并行
	stepIDs := make(map[string]bool, len(steps))
	for _, step := range steps {
		stepIDs[step.ID] = true
		for _, dep := range step.DependsOn {
			if stepIDs[dep] {
				return false // 存在依赖关系，不能并行
			}
		}
	}

	return true
}

// topologicalSort 对步骤进行拓扑排序（Kahn 算法）。
func topologicalSort(steps []Step) []string {
	// 构建邻接表和入度表
	graph := make(map[string][]string)
	inDegree := make(map[string]int)

	for _, step := range steps {
		if _, ok := graph[step.ID]; !ok {
			graph[step.ID] = []string{}
		}
		if _, ok := inDegree[step.ID]; !ok {
			inDegree[step.ID] = 0
		}
	}

	for _, step := range steps {
		for _, dep := range step.DependsOn {
			graph[dep] = append(graph[dep], step.ID)
			inDegree[step.ID]++
		}
	}

	// 收集入度为 0 的节点
	var queue []string
	for _, step := range steps {
		if inDegree[step.ID] == 0 {
			queue = append(queue, step.ID)
		}
	}

	// BFS 拓扑排序
	var result []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)

		for _, neighbor := range graph[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	return result
}

// truncateString 截断字符串到指定长度。
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
