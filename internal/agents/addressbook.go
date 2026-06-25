package agents

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// AddressBook 维护 Agent 端点注册表，支持 P2P 发现
type AddressBook struct {
	endpoints map[string]*AgentEndpoint   // agentID → endpoint
	typeIndex map[AgentType][]*AgentEndpoint // type → endpoints
	router    *AgentRouter                // 兜底路由器

	mu sync.RWMutex
}

// NewAddressBook 创建地址簿
func NewAddressBook() *AddressBook {
	return &AddressBook{
		endpoints: make(map[string]*AgentEndpoint),
		typeIndex: make(map[AgentType][]*AgentEndpoint),
	}
}

// Register 注册端点
func (ab *AddressBook) Register(endpoint *AgentEndpoint) error {
	if endpoint == nil {
		return fmt.Errorf("cannot register nil endpoint")
	}

	ab.mu.Lock()
	defer ab.mu.Unlock()

	agentID := endpoint.AgentID()

	// 检查是否已注册
	if _, exists := ab.endpoints[agentID]; exists {
		return fmt.Errorf("endpoint already registered for agent %s", agentID)
	}

	ab.endpoints[agentID] = endpoint
	ab.typeIndex[endpoint.AgentType()] = append(ab.typeIndex[endpoint.AgentType()], endpoint)

	// 设置地址簿引用
	endpoint.SetAddressBook(ab)

	slog.Info("Endpoint registered", "agent_id", agentID, "type", endpoint.AgentType())
	return nil
}

// Unregister 注销端点
func (ab *AddressBook) Unregister(agentID string) {
	ab.mu.Lock()
	defer ab.mu.Unlock()

	endpoint, exists := ab.endpoints[agentID]
	if !exists {
		return
	}

	// 从类型索引中移除
	agentType := endpoint.AgentType()
	if endpoints, ok := ab.typeIndex[agentType]; ok {
		filtered := make([]*AgentEndpoint, 0, len(endpoints)-1)
		for _, ep := range endpoints {
			if ep.AgentID() != agentID {
				filtered = append(filtered, ep)
			}
		}
		if len(filtered) == 0 {
			delete(ab.typeIndex, agentType)
		} else {
			ab.typeIndex[agentType] = filtered
		}
	}

	delete(ab.endpoints, agentID)
	slog.Info("Endpoint unregistered", "agent_id", agentID)
}

// Lookup 通过 Agent ID 查找端点
func (ab *AddressBook) Lookup(agentID string) (*AgentEndpoint, bool) {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	ep, ok := ab.endpoints[agentID]
	return ep, ok
}

// LookupByType 按类型查找端点（返回负载最低的）
func (ab *AddressBook) LookupByType(agentType AgentType) (*AgentEndpoint, bool) {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	endpoints, ok := ab.typeIndex[agentType]
	if !ok || len(endpoints) == 0 {
		return nil, false
	}

	// 返回负载最低的端点（inbox 深度最小的）
	best := endpoints[0]
	for _, ep := range endpoints[1:] {
		if !ep.IsAlive() {
			continue
		}
		// 简单的负载均衡：选择 inbox 最浅的
		info1 := best.GetInfo()
		info2 := ep.GetInfo()
		if info2.InboxDepth < info1.InboxDepth {
			best = ep
		}
	}

	return best, best.IsAlive()
}

// LookupAny 按类型查找任意活跃端点
func (ab *AddressBook) LookupAny(agentType AgentType) (*AgentEndpoint, bool) {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	endpoints, ok := ab.typeIndex[agentType]
	if !ok {
		return nil, false
	}

	for _, ep := range endpoints {
		if ep.IsAlive() {
			return ep, true
		}
	}

	return nil, false
}

// ListAll 列出所有注册的端点
func (ab *AddressBook) ListAll() []*AgentEndpoint {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	result := make([]*AgentEndpoint, 0, len(ab.endpoints))
	for _, ep := range ab.endpoints {
		result = append(result, ep)
	}
	return result
}

// ListByType 按类型列出端点
func (ab *AddressBook) ListByType(agentType AgentType) []*AgentEndpoint {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	endpoints, ok := ab.typeIndex[agentType]
	if !ok {
		return nil
	}
	result := make([]*AgentEndpoint, len(endpoints))
	copy(result, endpoints)
	return result
}

// SetRouter 设置兜底路由器
func (ab *AddressBook) SetRouter(router *AgentRouter) {
	ab.mu.Lock()
	defer ab.mu.Unlock()
	ab.router = router
}

// Resolve 解析 Agent ID，尝试 P2P，失败则走路由器兜底
func (ab *AddressBook) Resolve(agentID string) (*AgentEndpoint, error) {
	// 1. 尝试本地查找
	if ep, ok := ab.Lookup(agentID); ok {
		if ep.IsAlive() {
			return ep, nil
		}
	}

	// 2. 尝试通过路由器兜底
	ab.mu.RLock()
	router := ab.router
	ab.mu.RUnlock()

	if router != nil {
		// 通过路由器查找
		for _, agentType := range router.ListAgents() {
			agent, ok := router.GetAgent(agentType)
			if ok && agent.Name() == agentID {
				// 找到 Agent 但没有端点，创建临时端点
				slog.Debug("Agent found via router but no P2P endpoint", "agent_id", agentID)
				return nil, fmt.Errorf("agent %s has no P2P endpoint", agentID)
			}
		}
	}

	return nil, fmt.Errorf("agent %s not found in address book", agentID)
}

// HealthCheck 对所有端点进行健康检查
func (ab *AddressBook) HealthCheck() []string {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	var unhealthy []string
	for id, ep := range ab.endpoints {
		if !ep.IsAlive() {
			unhealthy = append(unhealthy, id)
		}
	}
	return unhealthy
}

// Count 返回注册端点数量
func (ab *AddressBook) Count() int {
	ab.mu.RLock()
	defer ab.mu.RUnlock()
	return len(ab.endpoints)
}

// ListTypes 列出所有已注册的 Agent 类型
func (ab *AddressBook) ListTypes() []AgentType {
	ab.mu.RLock()
	defer ab.mu.RUnlock()

	types := make([]AgentType, 0, len(ab.typeIndex))
	for t := range ab.typeIndex {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		return string(types[i]) < string(types[j])
	})
	return types
}
