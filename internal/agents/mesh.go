package agents

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"codeactor/internal/messaging/bus"
	"codeactor/internal/messaging/peer"
	"codeactor/internal/registry"
)

// AgentStatus Agent 状态枚举
type AgentStatus string

const (
	AgentStatusActive    AgentStatus = "active"
	AgentStatusBusy      AgentStatus = "busy"
	AgentStatusCompleted AgentStatus = "completed"
	AgentStatusFailed    AgentStatus = "failed"
	AgentStatusEvicted   AgentStatus = "evicted"
)

// AgentRole Agent 角色枚举
type AgentRole string

const (
	AgentRoleExecutor    AgentRole = "executor"
	AgentRoleExplorer    AgentRole = "explorer"
	AgentRoleAnalyst     AgentRole = "analyst"
	AgentRoleCoordinator AgentRole = "coordinator"
)

// EnhancedAgentCapability 增强的 Agent 能力描述
type EnhancedAgentCapability struct {
	AgentID      string        `json:"agent_id"`
	Name         string        `json:"name"`
	Role         AgentRole     `json:"role"`
	Status       AgentStatus   `json:"status"`
	TaskID       string        `json:"task_id,omitempty"`
	Capabilities []string      `json:"capabilities"`
	RegisteredAt time.Time     `json:"registered_at"`
	CompletedAt  *time.Time    `json:"completed_at,omitempty"`
	ExpiresAt    time.Time     `json:"expires_at"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// AgentMesh 管理 Agent 间的 P2P 通信网格。
// 持有共享的 EventBus，并为每个 Agent 初始化 Peer。
type AgentMesh struct {
	EventBus     *bus.EventBus
	agents       []*BaseAgent
	CapRegistry  registry.CapabilityRegistry

	// 增强型 Commander 支持
	mu           sync.RWMutex
	capabilities map[string]*EnhancedAgentCapability
}

// NewAgentMesh 创建新的 Agent 通信网格。
func NewAgentMesh() *AgentMesh {
	return &AgentMesh{
		EventBus:     bus.NewEventBus(),
		agents:       make([]*BaseAgent, 0),
		CapRegistry:  registry.NewCapabilityRegistry(),
		capabilities: make(map[string]*EnhancedAgentCapability),
	}
}

// RegisterAgent 在网格中注册 Agent，初始化其 P2P 身份。
// id 是 Agent 的唯一标识（如 "repo-agent", "coding-agent"）。
// caps 是可选的 AgentCapability，如果提供则自动注册到 CapabilityRegistry。
func (m *AgentMesh) RegisterAgent(id string, base *BaseAgent, caps ...registry.AgentCapability) error {
	if base == nil {
		return errors.New("agentmesh: base agent is nil")
	}
	if err := base.InitPeer(id, m.EventBus); err != nil {
		return err
	}
	m.agents = append(m.agents, base)

	// 自动注册能力到 CapabilityRegistry
	if len(caps) > 0 && m.CapRegistry != nil {
		cap := caps[0]
		cap.AgentID = id
		if cap.RegisteredAt.IsZero() {
			cap.RegisteredAt = time.Now()
		}
		if err := m.CapRegistry.Register(cap); err != nil {
			return err
		}
	}

	return nil
}

// GetCapRegistry 返回能力注册中心
func (m *AgentMesh) GetCapRegistry() registry.CapabilityRegistry {
	return m.CapRegistry
}

// RegisterAgentCapability 为已注册的 Agent 注册/更新能力
func (m *AgentMesh) RegisterAgentCapability(cap registry.AgentCapability) error {
	if m.CapRegistry == nil {
		return errors.New("agentmesh: capability registry not initialized")
	}
	return m.CapRegistry.Register(cap)
}

// RegisterConductorObserver 将 Conductor 注册为全局 Observer，
// 使其能感知所有 P2P 事件。handler 接收所有 topic 的事件副本。
func (m *AgentMesh) RegisterConductorObserver(handler func(ctx context.Context, ev *bus.Event) error) error {
	return m.EventBus.AddObserver("conductor", handler)
}

// Close 关闭网格，释放所有 Agent 的 Peer 资源和 EventBus。
func (m *AgentMesh) Close() error {
	var errs []error
	for _, base := range m.agents {
		if err := base.ClosePeer(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := m.EventBus.Close(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// GetMetrics 返回 EventBus 的性能指标。
func (m *AgentMesh) GetMetrics() *bus.Metrics {
	return &m.EventBus.Metrics
}

// PublishEvent 是向网格发布事件的便捷方法。
func (m *AgentMesh) PublishEvent(ctx context.Context, topic string, payload []byte) error {
	ev := &bus.Event{
		Topic:     topic,
		Payload:   payload,
		Timestamp: time.Now(),
	}
	return m.EventBus.Publish(ctx, ev)
}

// ─── 便捷方法：检查 topic 路由策略 ───

// IsP2PTopic 检查 topic 是否走 P2P 直连
func IsP2PTopic(topic string) bool {
	return peer.IsP2PTopic(topic)
}

// IsConductorTopic 检查 topic 是否需要 Conductor 仲裁
func IsConductorTopic(topic string) bool {
	return peer.IsConductorTopic(topic)
}

// Find 根据 AgentID 查找 Agent，返回 nil 表示未找到或已过期
func (m *AgentMesh) Find(agentID string) *EnhancedAgentCapability {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cap, exists := m.capabilities[agentID]
	if !exists {
		return nil
	}
	// 检查是否过期
	if !cap.ExpiresAt.IsZero() && time.Now().After(cap.ExpiresAt) {
		return nil
	}
	return cap
}

// ListActiveAgents 列出所有活跃状态的 Agent
func (m *AgentMesh) ListActiveAgents() []*EnhancedAgentCapability {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	var result []*EnhancedAgentCapability
	for _, cap := range m.capabilities {
		if !cap.ExpiresAt.IsZero() && now.After(cap.ExpiresAt) {
			continue // 跳过已过期
		}
		if cap.Status == AgentStatusActive || cap.Status == AgentStatusBusy {
			result = append(result, cap)
		}
	}
	return result
}

// QueryByRole 根据角色查询未过期的 Agent
func (m *AgentMesh) QueryByRole(role AgentRole) []*EnhancedAgentCapability {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now()
	var result []*EnhancedAgentCapability
	for _, cap := range m.capabilities {
		if !cap.ExpiresAt.IsZero() && now.After(cap.ExpiresAt) {
			continue
		}
		if cap.Role == role {
			result = append(result, cap)
		}
	}
	return result
}

// UpdateStatus 更新 Agent 状态
// 如果 taskID 不为空，同时更新关联的任务 ID
// 如果状态为 completed 或 failed，自动设置 CompletedAt 时间
func (m *AgentMesh) UpdateStatus(agentID string, status AgentStatus, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	cap, exists := m.capabilities[agentID]
	if !exists {
		return fmt.Errorf("agent %s not found in mesh", agentID)
	}

	cap.Status = status
	if taskID != "" {
		cap.TaskID = taskID
	}
	if status == AgentStatusCompleted || status == AgentStatusFailed {
		now := time.Now()
		cap.CompletedAt = &now
	}
	m.capabilities[agentID] = cap
	return nil
}

// CleanupExpired 清理过期的 Agent 注册，返回清理数量
func (m *AgentMesh) CleanupExpired() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	count := 0
	for id, cap := range m.capabilities {
		if !cap.ExpiresAt.IsZero() && now.After(cap.ExpiresAt) {
			delete(m.capabilities, id)
			count++
		}
	}
	return count
}

// RegisterEnhanced 注册增强型 Agent 能力
// 返回注册后的 Agent 引用
func (m *AgentMesh) RegisterEnhanced(cap *EnhancedAgentCapability) error {
	if cap == nil {
		return fmt.Errorf("agentmesh: capability is nil")
	}
	if cap.AgentID == "" {
		return fmt.Errorf("agentmesh: agent_id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if cap.RegisteredAt.IsZero() {
		cap.RegisteredAt = time.Now()
	}
	m.capabilities[cap.AgentID] = cap
	return nil
}

// UnregisterEnhanced 注销增强型 Agent
func (m *AgentMesh) UnregisterEnhanced(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.capabilities[agentID]; !exists {
		return fmt.Errorf("agent %s not found in mesh", agentID)
	}
	delete(m.capabilities, agentID)
	return nil
}
