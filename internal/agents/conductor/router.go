package conductor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// Router 负责 Agent 间消息路由和分发。
// 职责：
// 1. 根据任务类型选择目标 Agent
// 2. 管理 Agent 注册表
// 3. 分发消息到目标 Agent 并收集结果
type Router struct {
	mu       sync.RWMutex
	agents   map[string]AgentRunner // name → AgentRunner
	defaults []AgentRunner          // 默认 Agent 列表（按优先级排序）
}

// NewRouter 创建路由器。
func NewRouter() *Router {
	return &Router{
		agents:   make(map[string]AgentRunner),
		defaults: make([]AgentRunner, 0),
	}
}

// Register 注册一个 Agent 到路由表。
func (r *Router) Register(name string, agent AgentRunner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[name] = agent
	slog.Debug("[Conductor] Agent registered in router", "name", name)
}

// RegisterDefault 注册默认 Agent（将添加到默认列表末尾）。
func (r *Router) RegisterDefault(agent AgentRunner) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaults = append(r.defaults, agent)
	if _, exists := r.agents[agent.Name()]; !exists {
		r.agents[agent.Name()] = agent
	}
}

// GetAgent 按名称获取 Agent。
func (r *Router) GetAgent(name string) (AgentRunner, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agent, ok := r.agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return agent, nil
}

// Route 根据步骤选择并执行目标 Agent。
func (r *Router) Route(ctx context.Context, step Step) (AgentRunner, error) {
	// 如果步骤指定了 Agent 名称，直接查找
	if step.AgentType != "" && step.AgentType != "auto" {
		agent, err := r.GetAgent(step.AgentType)
		if err == nil {
			return agent, nil
		}
		slog.Warn("[Conductor] Specified agent not found, falling back to routing", "agent", step.AgentType)
	}

	// 启发式路由：根据任务内容选择 Agent
	return r.routeByHeuristic(ctx, step.Task)
}

// Dispatch 分发消息到指定 Agent 并执行。
func (r *Router) Dispatch(ctx context.Context, agentName string, task string) (string, error) {
	agent, err := r.GetAgent(agentName)
	if err != nil {
		return "", err
	}

	result, err := agent.Run(ctx, task)
	if err != nil {
		return "", fmt.Errorf("agent %q execution failed: %w", agentName, err)
	}

	return result.Text, nil
}

// ListAgents 返回所有已注册的 Agent 名称。
func (r *Router) ListAgents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	return names
}

// AgentCount 返回已注册的 Agent 数量。
func (r *Router) AgentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.agents)
}

// RemoveAgent 从路由表中移除指定 Agent。
func (r *Router) RemoveAgent(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, name)

	// 也从默认列表移除
	filtered := make([]AgentRunner, 0, len(r.defaults))
	for _, agent := range r.defaults {
		if agent.Name() != name {
			filtered = append(filtered, agent)
		}
	}
	r.defaults = filtered

	slog.Debug("[Conductor] Agent removed from router", "name", name)
}

// routeByHeuristic 基于任务内容的启发式路由。
// 当没有明确指定 Agent 时，根据任务关键词选择合适的 Agent。
func (r *Router) routeByHeuristic(ctx context.Context, task string) (AgentRunner, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 优先尝试默认 Agent 列表
	for _, agent := range r.defaults {
		return agent, nil // 简化实现：返回第一个默认 Agent
	}

	// 如果有任何注册的 Agent，返回第一个
	for _, agent := range r.agents {
		return agent, nil
	}

	return nil, fmt.Errorf("no agents available for routing")
}

// RouteToConductor 处理需要 Conductor 自身处理的消息。
// 当路由表中有 "conductor" 特殊 Agent 时使用。
func (r *Router) RouteToConductor(ctx context.Context, task string) (string, error) {
	agent, err := r.GetAgent("conductor")
	if err != nil {
		return "", fmt.Errorf("conductor self-routing not available: %w", err)
	}
	return r.Dispatch(ctx, agent.Name(), task)
}

// HasAgent 检查 Agent 是否已注册。
func (r *Router) HasAgent(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.agents[name]
	return ok
}
