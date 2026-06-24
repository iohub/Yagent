package agents

import (
	"context"
	"errors"
	"time"

	"codeactor/internal/messaging/bus"
	"codeactor/internal/messaging/peer"
	"codeactor/internal/registry"
)

// AgentMesh 管理 Agent 间的 P2P 通信网格。
// 持有共享的 EventBus，并为每个 Agent 初始化 Peer。
type AgentMesh struct {
	EventBus *bus.EventBus
	agents   []*BaseAgent
	// 分布式认知架构：能力注册中心
	CapRegistry registry.CapabilityRegistry
}

// NewAgentMesh 创建新的 Agent 通信网格。
func NewAgentMesh() *AgentMesh {
	return &AgentMesh{
		EventBus:    bus.NewEventBus(),
		agents:      make([]*BaseAgent, 0),
		CapRegistry: registry.NewCapabilityRegistry(),
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
