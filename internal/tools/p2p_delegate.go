package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// P2PDelegator 是 p2p_delegate 工具需要的依赖接口
// 该接口可由 agents 包中的 Mesh 或 P2P 通信层实现
type P2PDelegator interface {
	// Delegate 向目标 Agent 发送委派请求并等待响应
	// targetID: 目标 Agent ID
	// taskDescription: 任务描述
	// contextJSON: 上下文 JSON 字符串（可选附加信息）
	// timeout: 超时时间
	// 返回: 目标 Agent 的响应文本
	Delegate(targetID string, taskDescription string, contextJSON string, timeout time.Duration) (string, error)

	// GetAgentID 返回当前 Agent 的 ID
	GetAgentID() string
}

// DelegationChecker 提供委派安全检查（环检测+深度+超时）
// 该接口与 DelegationContext 保持一致
type DelegationChecker interface {
	// CanDelegate 检查委派是否安全
	// targetID: 目标 Agent ID
	// fromID: 发起者 Agent ID
	// 返回: 错误原因或 nil（允许委派）
	CanDelegate(targetID string, fromID string) error

	// Fork 为子 Agent 创建委派上下文
	Fork(targetID string) interface{}
}

// NewP2PDelegateAdapter 创建 p2p_delegate 工具适配器
// LLM 看到的描述：
// "Delegate a subtask to another agent via P2P communication.
//  The target agent will receive your request and return a response.
//  Use capability_search first to find the right agent.
//  IMPORTANT:
//  - Always search for capabilities first to find the best agent
//  - Provide clear, self-contained task descriptions
//  - Include relevant context (file paths, previous findings, etc.)"
func NewP2PDelegateAdapter(delegator P2PDelegator, checker DelegationChecker) *Adapter {
	desc := `Delegate a subtask to another agent via P2P direct communication. The target agent will execute your request and return results. Use capability_search first to find the right agent for the job.

IMPORTANT:
1. Always search for capabilities FIRST using capability_search before delegating
2. Provide clear, self-contained task descriptions with all necessary context
3. Include relevant context (file paths, previous findings, constraints)
4. Only delegate tasks that are outside your core expertise
5. For quick questions, use p2p_query instead of p2p_delegate`

	adapter := NewAdapter("p2p_delegate", desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		targetID, _ := params["target_agent"].(string)
		if targetID == "" {
			return nil, fmt.Errorf("p2p_delegate: target_agent is required (use capability_search first to find the right agent)")
		}

		taskDescription, _ := params["task"].(string)
		if taskDescription == "" {
			return nil, fmt.Errorf("p2p_delegate: task description is required")
		}

		// 降级处理：P2P 不可用（legacy 模式）
		if delegator == nil {
			return "P2P delegate not available (running in legacy mode without P2P mesh)", nil
		}

		// 自委派检查
		if delegator.GetAgentID() == targetID {
			return "Error: Cannot delegate to yourself. Handle the task yourself or choose a different agent.", nil
		}

		// 安全检查（环检测 + 深度 + 超时）
		if checker != nil {
			if err := checker.CanDelegate(targetID, delegator.GetAgentID()); err != nil {
				return fmt.Sprintf("Delegation safety check failed: %v\nYou should handle this task yourself or try a different approach.", err), nil
			}
		}

		// 构建上下文 JSON
		contextJSON := ""
		if contextRaw, ok := params["context"]; ok {
			switch v := contextRaw.(type) {
			case string:
				contextJSON = v
			case map[string]interface{}:
				bytes, _ := json.Marshal(v)
				contextJSON = string(bytes)
			}
		}

		// 获取超时时间
		timeout := 120 * time.Second
		if timeoutRaw, ok := params["timeout"].(float64); ok && timeoutRaw > 0 {
			timeout = time.Duration(timeoutRaw) * time.Second
		}

		// 执行委派
		result, err := delegator.Delegate(targetID, taskDescription, contextJSON, timeout)
		if err != nil {
			return fmt.Sprintf("P2P delegate to %s failed: %v\nYou should handle this task yourself.", targetID, err), nil
		}

		return result, nil
	})

	adapter.schema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target_agent": map[string]interface{}{
				"type":        "string",
				"description": "Target agent ID (e.g., 'repo-agent', 'coding-agent'). Use capability_search first to find the right agent.",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Clear, self-contained task description for the target agent",
			},
			"context": map[string]interface{}{
				"type":        "string",
				"description": "Optional additional context (file paths, previous findings, constraints) as JSON string or object",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Timeout in seconds (default: 120)",
			},
		},
		"required": []string{"target_agent", "task"},
	}

	return adapter
}

// NewP2PQueryAdapter 创建增强版 p2p_query 工具适配器
// 在现有 p2p_query 基础上增加 DelegationContext 集成
func NewP2PQueryAdapter(delegator P2PDelegator) *Adapter {
	desc := `Send a quick P2P query to another agent and get a response. Use this for quick questions (e.g., asking repo-agent about a code symbol). For complex tasks, use p2p_delegate instead.`

	adapter := NewAdapter("p2p_query", desc, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		targetID, _ := params["target_agent"].(string)
		if targetID == "" {
			return nil, fmt.Errorf("p2p_query: target_agent is required")
		}

		method, _ := params["method"].(string)
		if method == "" {
			return nil, fmt.Errorf("p2p_query: method is required")
		}

		// 降级处理：P2P 不可用（legacy 模式）
		if delegator == nil {
			return "P2P query not available (running in legacy mode)", nil
		}

		contextJSON := ""
		if contextRaw, ok := params["payload"]; ok {
			switch v := contextRaw.(type) {
			case string:
				contextJSON = v
			case map[string]interface{}:
				bytes, _ := json.Marshal(v)
				contextJSON = string(bytes)
			}
		}

		timeout := 10 * time.Second
		if timeoutRaw, ok := params["timeout"].(float64); ok && timeoutRaw > 0 {
			timeout = time.Duration(timeoutRaw) * time.Second
		}

		// 使用 Delegator 发送查询
		result, err := delegator.Delegate(targetID, method, contextJSON, timeout)
		if err != nil {
			return fmt.Sprintf("P2P query to %s failed: %v", targetID, err), nil
		}

		return result, nil
	})

	adapter.schema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"target_agent": map[string]interface{}{
				"type":        "string",
				"description": "Target agent ID (e.g., 'repo-agent', 'browser-agent')",
			},
			"method": map[string]interface{}{
				"type":        "string",
				"description": "The method/rpc name to call on the target agent",
			},
			"payload": map[string]interface{}{
				"type":        "object",
				"description": "JSON-serializable payload to send",
			},
			"timeout": map[string]interface{}{
				"type":        "number",
				"description": "Timeout in seconds (default: 10)",
			},
		},
		"required": []string{"target_agent", "method"},
	}

	return adapter
}
