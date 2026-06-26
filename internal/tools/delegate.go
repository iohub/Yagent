package tools

import (
	"context"
	"fmt"
)

// AgentRunner 是委派工具需要的最小接口。
// 任何实现了 Run(ctx, task) 方法的对象都可以被包装为委派工具。
type AgentRunner interface {
	Run(ctx context.Context, task string) (string, error)
}

// NewDelegateAdapter 创建一个委派工具适配器。
// 它将 target Agent 包装为一个 delegate_<name> 工具，使其他 Agent 可以直接调用。
//
// 参数:
//   - name: Agent 名称，用于生成工具名（如 "repo" → "delegate_repo"）
//   - description: 工具描述，供 LLM 决定何时使用
//   - target: 目标 Agent，需要实现 AgentRunner 接口
//
// 返回:
//   - *Adapter: 标准工具适配器
func NewDelegateAdapter(name string, description string, target AgentRunner) *Adapter {
	toolName := "delegate_" + name
	adapter := NewAdapter(toolName, description, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok || task == "" {
			return nil, fmt.Errorf("%s: task parameter is required", toolName)
		}
		return target.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The task description for the agent",
			},
		},
		"required": []string{"task"},
	})
	return adapter
}

// DelegateFunc 是一个适配器类型，将返回 (AgentResult, error) 的 Run 方法
// 适配为返回 (string, error) 的 Run 方法。
// 用于在 agents 包中将 Agent 接口适配为 AgentRunner 接口。
type DelegateFunc func(ctx context.Context, task string) (string, error)

func (f DelegateFunc) Run(ctx context.Context, task string) (string, error) {
	return f(ctx, task)
}
