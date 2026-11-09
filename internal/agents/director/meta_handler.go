package director

// meta_handler.go — 自定义 Agent 注册表（P0-1 Phase 2d，P9a 收缩）。
//
// P9a 之后这里**只剩注册表**：MetaAgentHandler（List/Register/Get/Names/Len）。
// 曾经同住的 ParseMetaAgentOutput / ExtractJSONObject 已删除 —— 生成职责移交 Python
// 之后，"解析 LLM 裸输出的 5 字段 JSON"这件事不存在了（bundle 由 plasma.Elaborate
// 归一化）。注册表则必须保留：加载期（P9b 的 store→Surface 接线）与执行期
// （delegate_<name> 的解析）都读它。
//
// 本文件属于 director 子包，绝不能 import agents（循环依赖守卫
// scripts/check_no_agents_import.sh）。CustomAgent 为纯数据类型（无闭包/执行器
// 引用），注册表直接存储 *CustomAgent；工具接线（Surface.Add/执行闭包）依赖 agents
// 包符号，保留在门面层 registerCustomAgent 中。

import (
	"errors"
	"fmt"
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
