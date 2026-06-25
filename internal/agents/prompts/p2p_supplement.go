package prompts

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// P2PSupplementRole 定义 Supplement 的角色类型
type P2PSupplementRole string

const (
	SupplementRoleExecutor    P2PSupplementRole = "executor"
	SupplementRoleExplorer    P2PSupplementRole = "explorer"
	SupplementRoleAnalyst     P2PSupplementRole = "analyst"
	SupplementRoleCoordinator P2PSupplementRole = "coordinator"
)

// P2PSupplementConfig P2P Supplement 渲染配置
type P2PSupplementConfig struct {
	Role               P2PSupplementRole
	AgentID            string
	AgentName          string
	Capabilities       string   // 逗号分隔的能力列表
	MaxDelegationDepth int
}

// ExecutorSupplement 执行型 Agent 的 P2P Supplement
// 适用于 Coding-Agent、DevOps-Agent 等
const ExecutorSupplement = `
## P2P Collaboration Capabilities (Executor)

You are part of a multi-agent system with direct peer-to-peer communication.

### Available Tools

1. **p2p_delegate**: Delegate a subtask to another executor agent.
   - Use when a task is outside your expertise
   - Always search capabilities first before delegating
   - Max delegation depth: {{.MaxDelegationDepth}}
   - Provide clear, self-contained task descriptions

2. **capability_search**: Find agents with specific skills.
   - Search by role, capabilities, or status
   - Required before delegation

3. **p2p_query**: Send a quick question to another agent.
   - For simple information requests (not full task delegation)

4. **p2p_notify**: Broadcast notifications to other agents.
   - For state changes, progress updates

5. **blackboard_read / blackboard_post**: Share information via shared blackboard.
   - Post intermediate findings
   - Read others' discoveries

### Behavior Guidelines
- Handle tasks yourself first; delegate only when necessary
- Search capabilities before delegating
- Post key findings to the blackboard for others
- Include full context when delegating

### Your Identity
- Agent ID: {{.AgentID}}
- Name: {{.AgentName}}
- Capabilities: {{.Capabilities}}
`

// ExplorerSupplement 探索型 Agent 的 P2P Supplement
// 适用于 Repo-Agent、Browser-Agent 等只读探索型 Agent
const ExplorerSupplement = `
## P2P Collaboration Capabilities (Explorer)

You are a read-only code/Web explorer in a multi-agent system.

### Available Tools

1. **p2p_query**: Respond to queries from other agents.
   - Other agents can ask you for information
   - Provide thorough, accurate responses

2. **p2p_notify**: Notify other agents about findings.
   - Share important discoveries proactively

3. **blackboard_post**: Post findings to the shared blackboard.
   - Share code structure, symbol locations, Web page content
   - Tag your posts for easy discovery

4. **blackboard_read**: Read what other agents need or have found.

### Constraints
- You CANNOT delegate tasks (no p2p_delegate tool)
- You CAN receive queries from executor agents
- Your role is read-only: provide information, don't modify the environment
- Share findings proactively via blackboard

### Your Identity
- Agent ID: {{.AgentID}}
- Name: {{.AgentName}}
- Capabilities: {{.Capabilities}}
`

// GetP2PSupplement 根据角色获取 P2P Supplement 模板
func GetP2PSupplement(role P2PSupplementRole) (string, error) {
	switch role {
	case SupplementRoleExecutor:
		return ExecutorSupplement, nil
	case SupplementRoleExplorer:
		return ExplorerSupplement, nil
	case SupplementRoleAnalyst:
		return ExplorerSupplement, nil // Analyst uses same template as explorer
	case SupplementRoleCoordinator:
		return ExecutorSupplement, nil // Coordinator uses same template as executor
	default:
		return "", fmt.Errorf("unknown supplement role: %s", role)
	}
}

// RenderSupplement 渲染 Supplement 模板
// 将变量注入模板并返回渲染后的文本
func RenderSupplement(config P2PSupplementConfig) (string, error) {
	tmplStr, err := GetP2PSupplement(config.Role)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New("p2p_supplement").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parse supplement template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, config); err != nil {
		return "", fmt.Errorf("render supplement: %w", err)
	}

	return strings.TrimSpace(buf.String()), nil
}

// DefaultCapabilities 返回各 Agent 的默认能力列表
func DefaultCapabilities(agentType string) []string {
	switch agentType {
	case "repo-agent", "repo":
		return []string{"code_search", "symbol_analysis", "dependency_analysis", "file_read"}
	case "coding-agent", "coding":
		return []string{"code_generation", "code_modification", "code_review", "test_generation"}
	case "devops-agent", "devops":
		return []string{"build", "deploy", "ci_cd", "infrastructure"}
	case "chat-agent", "chat":
		return []string{"analysis", "reasoning", "summarization", "qa"}
	case "browser-agent", "browser":
		return []string{"web_search", "page_read", "form_submit", "screenshot"}
	case "meta-agent", "meta":
		return []string{"planning", "task_decomposition", "coordination"}
	default:
		return []string{"general"}
	}
}

// DefaultRole 返回各 Agent 的默认角色
func DefaultRole(agentType string) P2PSupplementRole {
	switch agentType {
	case "repo-agent", "repo":
		return SupplementRoleExplorer
	case "browser-agent", "browser":
		return SupplementRoleExplorer
	case "coding-agent", "coding":
		return SupplementRoleExecutor
	case "devops-agent", "devops":
		return SupplementRoleExecutor
	case "chat-agent", "chat":
		return SupplementRoleAnalyst
	case "meta-agent", "meta":
		return SupplementRoleCoordinator
	default:
		return SupplementRoleExecutor
	}
}
