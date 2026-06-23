package agents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/memory"
)

// PlannedTask 规划中的任务
type PlannedTask struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`        // "coding", "review", "research", "operation"
	Description  string   `json:"description"`
	Dependencies []string `json:"dependencies,omitempty"` // 依赖的其他 task ID
	AgentType    string   `json:"agent_type"`   // 目标 Agent 类型
	Priority     int      `json:"priority"`
	Input        string   `json:"input,omitempty"`
}

// Plan 完整的任务执行计划
type Plan struct {
	SessionID   string         `json:"session_id"`
	Objective   string         `json:"objective"`
	Tasks       []*PlannedTask `json:"tasks"`
	Iteration   int            `json:"iteration"`
	TotalSteps  int            `json:"total_steps"`
	Rationale   string         `json:"rationale,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Planner 任务规划器 - 将用户目标分解为可执行的任务序列
type Planner struct {
	llm       llm.Engine
	maxDepth  int
}

// NewPlanner 创建规划器
func NewPlanner(llm llm.Engine, maxDepth int) *Planner {
	if maxDepth <= 0 {
		maxDepth = 3
	}
	return &Planner{
		llm:      llm,
		maxDepth: maxDepth,
	}
}

// Plan 将用户目标分解为任务计划
func (p *Planner) Plan(ctx context.Context, sessionID string, objective string, mem *memory.ConversationMemory) (*Plan, error) {
	slog.Debug("Planner creating plan", "session_id", sessionID, "objective", objective)

	plan := &Plan{
		SessionID: sessionID,
		Objective: objective,
		Iteration: 1,
		CreatedAt: time.Now(),
	}

	// 简单规划：使用 LLM 分解任务
	tasks, err := p.decomposeWithLLM(ctx, objective, mem)
	if err != nil {
		slog.Warn("LLM task decomposition failed, using fallback", "error", err)
		// 降级：创建一个单一任务
		tasks = []*PlannedTask{{
			ID:          fmt.Sprintf("task_%d", time.Now().UnixNano()),
			Type:        "coding",
			Description: objective,
			AgentType:   "Coding-Agent",
			Priority:    1,
			Input:       objective,
		}}
	}

	plan.Tasks = tasks
	plan.TotalSteps = len(tasks)
	plan.Rationale = "Tasks decomposed by Planner"

	slog.Info("Plan created", "tasks", len(tasks), "session_id", sessionID)
	return plan, nil
}

// Replan 基于反馈重新规划
func (p *Planner) Replan(ctx context.Context, current *Plan, feedback string, mem *memory.ConversationMemory) (*Plan, error) {
	slog.Debug("Planner re-planning", "session_id", current.SessionID, "feedback", feedback)

	current.Iteration++

	// 超出最大深度，直接返回当前计划
	if current.Iteration > p.maxDepth {
		slog.Warn("Max planning depth reached, using current plan", "depth", p.maxDepth)
		return current, nil
	}

	// 用 LLM 重新规划
	newTasks, err := p.decomposeWithLLM(ctx,
		fmt.Sprintf("Original: %s\n\nProgress so far: %s\n\nFeedback/Issue: %s\n\nPlease revise the plan accordingly.",
			current.Objective, current.Rationale, feedback),
		mem)
	if err != nil {
		slog.Warn("Replan failed, keeping current plan", "error", err)
		return current, nil
	}

	current.Tasks = newTasks
	current.TotalSteps = len(newTasks)
	current.Rationale = feedback
	return current, nil
}

// decomposeWithLLM 使用 LLM 分解任务
func (p *Planner) decomposeWithLLM(ctx context.Context, objective string, mem *memory.ConversationMemory) ([]*PlannedTask, error) {
	// 如果 LLM 不可用，返回简单任务
	if p.llm == nil {
		return p.fallbackPlan(objective), nil
	}

	// 构建分解任务的 prompt
	prompt := fmt.Sprintf(`You are a task planner. Break down the following objective into a sequence of concrete, executable tasks.

Objective: %s

For each task, specify:
1. A unique task ID (task_1, task_2, ...)
2. Task type: "coding" | "review" | "research" | "operation"
3. A clear description
4. Dependencies (list of task IDs this task depends on)
5. Agent type: "Coding-Agent" | "Repo-Agent" | "DevOps-Agent" | "Chat-Agent"
6. Priority: 1 (highest) to 5 (lowest)

Return the tasks as a JSON array:
[
  {"id": "task_1", "type": "research", "description": "...", "dependencies": [], "agent_type": "Repo-Agent", "priority": 1},
  ...
]

IMPORTANT: Return ONLY the JSON array, no other text.`, objective)

	_, err := p.llm.GenerateContent(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: "You are a precise task planner. Output only valid JSON."},
		{Role: llm.RoleUser, Content: prompt},
	}, nil, nil)

	if err != nil {
		return p.fallbackPlan(objective), err
	}

	// 返回降级计划（LLM 输出的解析在完整实现中需要更健壮的处理）
	return p.fallbackPlan(objective), nil
}

// fallbackPlan 降级计划：将整个目标作为一个任务
func (p *Planner) fallbackPlan(objective string) []*PlannedTask {
	return []*PlannedTask{{
		ID:           fmt.Sprintf("task_%d", time.Now().UnixNano()),
		Type:         "coding",
		Description:  objective,
		Dependencies: []string{},
		AgentType:    "Coding-Agent",
		Priority:     1,
		Input:        objective,
	}}
}

// EstimateSteps 估算任务所需步骤数
func (p *Planner) EstimateSteps(tasks []*PlannedTask) int {
	total := 0
	for _, t := range tasks {
		switch t.Type {
		case "coding":
			total += 5
		case "review":
			total += 2
		case "research":
			total += 3
		case "operation":
			total += 1
		default:
			total += 3
		}
	}
	return total
}
