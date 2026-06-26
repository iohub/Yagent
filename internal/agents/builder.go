package agents

import (
	"context"
	"fmt"
	"log/slog"

	"codeactor/internal/tools"
)

// AgentFactory 是创建 Agent 的工厂函数。
// deps 参数包含所有已构建的子 Agent（key 为 Agent 名称），
// 工厂函数可以根据委派图从中选取需要的 Agent 引用。
type AgentFactory func(deps map[string]interface{}) interface{}

// BuildResult 保存 AgentBuilder 的构建结果。
type BuildResult struct {
	// Agents 包含所有已构建的 Agent，key 为 Agent 名称。
	Agents map[string]interface{}
	// Conductor 是根 Agent（conductor 节点）。
	Conductor *ConductorAgent
}

// AgentBuilder 按拓扑序构建 Agent，并根据 DelegationGraph 注入委派工具。
type AgentBuilder struct {
	// Graph 定义 Agent 间的委派权限。
	Graph DelegationGraph
	// Factories 是 Agent 名称到工厂函数的映射。
	// 必须包含图中所有节点的工厂函数。
	Factories map[string]AgentFactory
}

// Build 执行拓扑序构建。
// 1. 验证委派图
// 2. 拓扑排序（叶子节点先构建）
// 3. 按序构建每个 Agent，传递已构建的依赖
// 4. 返回包含所有 Agent 和 Conductor 引用的结果
func (b *AgentBuilder) Build() (*BuildResult, error) {
	// 1. 验证委派图
	if err := b.Graph.Validate(); err != nil {
		return nil, fmt.Errorf("agent builder: invalid delegation graph: %w", err)
	}

	// 2. 拓扑排序（叶子优先）
	order, err := b.Graph.TopologicalSort()
	if err != nil {
		return nil, fmt.Errorf("agent builder: topological sort failed: %w", err)
	}

	slog.Info("AgentBuilder: topological build order",
		"order", order,
		"graph", b.Graph)

	// 3. 按序构建
	agents := make(map[string]interface{}, len(order))
	for _, name := range order {
		factory, ok := b.Factories[name]
		if !ok {
			return nil, fmt.Errorf("agent builder: no factory for agent %q", name)
		}
		agent := factory(agents)
		if agent == nil {
			return nil, fmt.Errorf("agent builder: factory for %q returned nil", name)
		}
		agents[name] = agent
		slog.Debug("AgentBuilder: built agent", "name", name)
	}

	// 4. 提取 Conductor
	conductorRaw, ok := agents["conductor"]
	if !ok {
		return nil, fmt.Errorf("agent builder: conductor agent not found in build results")
	}
	conductor, ok := conductorRaw.(*ConductorAgent)
	if !ok {
		return nil, fmt.Errorf("agent builder: conductor agent is not *ConductorAgent, got %T", conductorRaw)
	}

	return &BuildResult{
		Agents:    agents,
		Conductor: conductor,
	}, nil
}

// NewDelegateAdapterForAgent 为指定名称的 Agent 创建一个 delegate_* 适配器。
// 它从 agents map 中查找目标 Agent，将其 Run 方法包装为 tools.Adapter。
func NewDelegateAdapterForAgent(agentName string, description string, agents map[string]interface{}) (*tools.Adapter, error) {
	raw, ok := agents[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in build results", agentName)
	}

	// 根据 Agent 类型进行类型断言
	// 所有内置 Agent 都有 Run(ctx, string) (AgentResult, error) 方法
	var runner tools.AgentRunner

	switch a := raw.(type) {
	case *RepoAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	case *CodingAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	case *ChatAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	case *DevOpsAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	case *BrowserAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	case *MetaAgent:
		runner = tools.DelegateFunc(func(ctx context.Context, task string) (string, error) {
			result, err := a.Run(ctx, task)
			if err != nil {
				return "", err
			}
			return result.Text, nil
		})
	default:
		return nil, fmt.Errorf("unsupported agent type for delegation: %T", raw)
	}

	// 生成描述（如果未提供）
	if description == "" {
		description = fmt.Sprintf("Delegate tasks to %s agent", agentName)
	}

	return tools.NewDelegateAdapter(agentName, description, runner), nil
}
