package director

// meta_handler.go — P0-1 Phase 2d：Meta-Agent 输出解析 + 自定义 agent 注册表组件。
//
// 从 internal/agents/director.go（agents 门面）搬迁而来：
//   - ParseMetaAgentOutput（原 parseMetaAgentOutput，逐字搬迁，控制流/错误文案不变）；
//   - ExtractJSONObject（原 extractJSONObject，逐字搬迁，解析函数的唯一依赖）；
//   - MetaAgentHandler（原 DirectorAgent.customAgents map 注册表，收敛为带锁组件）。
//
// 本文件属于 director 子包，绝不能 import agents（循环依赖守卫
// scripts/check_no_agents_import.sh）。CustomAgent 为纯数据类型（无闭包/执行器
// 引用），注册表直接存储 *CustomAgent；工具接线（Adapters/toolDefMap/执行闭包）
// 依赖 agents 包符号，保留在门面层 registerCustomAgent 中。

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrAlreadyRegistered 表示重复注册同一 delegate 工具名。
// 对应原 registerCustomAgent 的防重复检查语义：重复时拒绝并保持原条目不变
// （门面层捕获该错误后仅记录日志并跳过接线，与原行为逐字一致）。
var ErrAlreadyRegistered = errors.New("custom agent already registered")

// MetaAgentHandler 管理 Meta-Agent 动态设计的自定义 agent 注册表。
//
// 与原实现的差异（可控改进）：原 customAgents 为普通 map，列举时遍历顺序随机，
// 导致 system prompt 的 "### Custom Agents" 段落顺序不稳定（不利于 LLM Prompt
// Cache 复用）。本组件维护插入序切片，List/Names 按注册序确定性返回。
//
// 并发安全：内部使用 RWMutex 保护 map 与插入序。
type MetaAgentHandler struct {
	mu     sync.RWMutex
	agents map[string]*CustomAgent // key: "delegate_<name>" → agent design
	order  []string                // 注册序（delegate name），保证 List/Names 确定性
}

// NewMetaAgentHandler 创建空的注册表。
func NewMetaAgentHandler() *MetaAgentHandler {
	return &MetaAgentHandler{agents: make(map[string]*CustomAgent)}
}

// Register 注册一个自定义 agent，key 为 "delegate_" + ca.Name。
// 已存在同 key 时返回包装 ErrAlreadyRegistered 的错误，原条目不被覆盖。
func (h *MetaAgentHandler) Register(ca *CustomAgent) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	delegateName := "delegate_" + ca.Name
	if _, exists := h.agents[delegateName]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, delegateName)
	}
	h.agents[delegateName] = ca
	h.order = append(h.order, delegateName)
	return nil
}

// Get 按 delegate 工具名（如 "delegate_security_auditor"）查找已注册的自定义 agent。
func (h *MetaAgentHandler) Get(name string) (*CustomAgent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ca, ok := h.agents[name]
	return ca, ok
}

// List 按注册（插入）序返回全部已注册的自定义 agent。
// 返回切片为副本，调用方修改不影响内部状态。
func (h *MetaAgentHandler) List() []*CustomAgent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]*CustomAgent, 0, len(h.order))
	for _, name := range h.order {
		result = append(result, h.agents[name])
	}
	return result
}

// Names 按注册（插入）序返回全部 delegate 工具名。
// 返回切片为副本，调用方修改不影响内部状态。
func (h *MetaAgentHandler) Names() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.order...)
}

// Len 返回已注册的自定义 agent 数量。
func (h *MetaAgentHandler) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.order)
}

// ParseMetaAgentOutput extracts and validates the JSON object from Meta-Agent's raw output.
// It strips markdown code fences and surrounding text to find the JSON.
// （逐字搬迁自 agents.parseMetaAgentOutput：控制流、错误文案不变；
// 仅类型去重——统一返回 director.MetaAgentResult，原 agents 侧 metaAgentResult 删除）
func ParseMetaAgentOutput(output string) (systemPrompt string, execResult *MetaAgentResult, err error) {
	jsonStr := ExtractJSONObject(output)
	if jsonStr == "" {
		return "", nil, fmt.Errorf("no JSON object found in Meta-Agent output")
	}

	execResult = &MetaAgentResult{}
	if err := json.Unmarshal([]byte(jsonStr), execResult); err != nil {
		return "", nil, fmt.Errorf("failed to parse Meta-Agent JSON: %w", err)
	}

	// Validate required fields
	if execResult.AgentName == "" {
		return "", nil, fmt.Errorf("agent_name is empty in Meta-Agent JSON")
	}
	if execResult.AgentDesign == "" {
		return "", nil, fmt.Errorf("agent_design is empty in Meta-Agent JSON")
	}

	return execResult.AgentDesign, execResult, nil
}

// ExtractJSONObject finds the outermost JSON object in a string.
// It strips markdown code fences and handles surrounding text.
// （逐字搬迁自 agents.extractJSONObject；导出供门面层既有单测直接调用）
func ExtractJSONObject(s string) string {
	raw := s

	// Strip markdown code fences: ```json ... ``` or ``` ... ```
	if idx := strings.Index(raw, "```"); idx != -1 {
		endFence := strings.Index(raw[idx+3:], "```")
		if endFence != -1 {
			inner := raw[idx+3 : idx+3+endFence]
			// Skip optional language tag after opening ```
			if newline := strings.Index(inner, "\n"); newline != -1 {
				inner = inner[newline+1:]
			}
			raw = inner
		}
	}

	// Find the outermost { ... }
	start := strings.Index(raw, "{")
	if start == -1 {
		return ""
	}
	// Walk braces to find the matching close brace
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
