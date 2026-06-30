package agents

import (
	"fmt"
)

// DelegationGraph 定义 Agent 间的静态委派权限图。
// key 是 Agent 名称，value 是该 Agent 可以直接委派的 Agent 列表。
// 空 slice 表示该 Agent 是叶子节点（不能委派其他 Agent）。
type DelegationGraph map[string][]string

// DefaultDelegationGraph 返回默认的委派关系图。
// Repo Agent 是终结点（叶子节点），不能委派其他 Agent。
// Chat 和 Meta Agent 也是叶子节点。
// Coding、DevOps、Browser 可以直接委派 Repo Agent。
func DefaultDelegationGraph() DelegationGraph {
	return DelegationGraph{
		"director": {"repo", "coding", "chat", "meta", "devops", "browser"},
		"coding":    {"repo"},
		"devops":    {"repo"},
		"browser":   {"repo"},
		"repo":      {},
		"chat":      {},
		"meta":      {},
	}
}

// Validate 验证委派图是否为合法的 DAG：
// 1. 所有被委派的 Agent 必须在图中定义（有对应的 key）
// 2. 无环（使用 DFS 三色标记法）
func (g DelegationGraph) Validate() error {
	// 检查所有被引用的 Agent 是否已定义
	for from, targets := range g {
		for _, to := range targets {
			if _, exists := g[to]; !exists {
				return fmt.Errorf("delegation graph: agent %q (referenced by %q) is not defined in the graph", to, from)
			}
		}
	}

	// DFS 三色标记法检测环
	// 0 = 未访问, 1 = 正在访问, 2 = 已访问完成
	state := make(map[string]int)
	for node := range g {
		state[node] = 0
	}

	var dfs func(node string) error
	dfs = func(node string) error {
		if state[node] == 1 {
			return fmt.Errorf("delegation graph: cycle detected involving agent %q", node)
		}
		if state[node] == 2 {
			return nil
		}
		state[node] = 1
		for _, neighbor := range g[node] {
			if err := dfs(neighbor); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}

	for node := range g {
		if state[node] == 0 {
			if err := dfs(node); err != nil {
				return err
			}
		}
	}

	return nil
}

// CanDelegate 检查 from Agent 是否可以直接委派 to Agent。
func (g DelegationGraph) CanDelegate(from, to string) bool {
	for _, target := range g[from] {
		if target == to {
			return true
		}
	}
	return false
}

// Leaves 返回所有叶子节点（委派列表为空的 Agent）。
func (g DelegationGraph) Leaves() []string {
	var leaves []string
	for node, targets := range g {
		if len(targets) == 0 {
			leaves = append(leaves, node)
		}
	}
	return leaves
}

// TopologicalSort 返回按拓扑序排列的 Agent 名称列表（从叶子到根）。
// 使用 Kahn 算法。
func (g DelegationGraph) TopologicalSort() ([]string, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}

	// 计算入度
	inDegree := make(map[string]int)
	for node := range g {
		inDegree[node] = 0
	}
	for _, targets := range g {
		for _, to := range targets {
			inDegree[to]++
		}
	}

	// 入度为 0 的节点（先构建叶子节点）
	var queue []string
	for node, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, node)
		}
	}

	var result []string
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		result = append(result, node)
		for _, neighbor := range g[node] {
			inDegree[neighbor]--
			if inDegree[neighbor] == 0 {
				queue = append(queue, neighbor)
			}
		}
	}

	if len(result) != len(g) {
		return nil, fmt.Errorf("delegation graph: topological sort failed - graph may have a cycle")
	}

	return result, nil
}
