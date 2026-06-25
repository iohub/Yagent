package agents

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"codeactor/internal/memory"
)

// MemoryConfig 内存配置
type MemoryConfig struct {
	LocalMaxSize      int  // 本地内存最大消息数（默认 200）
	SharedMaxSize     int  // 共享内存最大消息数（默认 500）
	CompactionTrigger int  // 触发压缩的消息数阈值（0=不触发）
	AutoPersist       bool // 是否在压缩时自动持久化
}

// DefaultMemoryConfig 返回默认内存配置
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		LocalMaxSize:      200,
		SharedMaxSize:     500,
		CompactionTrigger: 0,
		AutoPersist:       false,
	}
}

// KeyFinding 表示 sub-agent 的关键发现摘要
// 替代全量 Memory 注入，只传递有价值的信息
type KeyFinding struct {
	Type    string                 `json:"type"`    // "file_change", "error", "decision", "artifact", "completion"
	Content string                 `json:"content"` // 简洁描述
	Meta    map[string]interface{} `json:"meta,omitempty"`
}

// MemoryStats 内存统计
type MemoryStats struct {
	AgentID       string `json:"agent_id"`
	LocalSize     int    `json:"local_size"`
	SharedSize    int    `json:"shared_size"`
	TotalSize     int    `json:"total_size"`
	LocalMaxSize  int    `json:"local_max_size"`
	SharedMaxSize int    `json:"shared_max_size"`
	CompactCount  int    `json:"compact_count"`
	LastCompactTime string `json:"last_compact_time,omitempty"`
}

// MemoryManager 协调所有 Agent 的内存生命周期。
// 从 Conductor 中提取的内存管理逻辑。
type MemoryManager struct {
	agentMemories   map[string]memory.Memory          // agentID → Memory（可能是 LayeredMemory 或 ConversationMemory）
	layeredMemories map[string]*memory.LayeredMemory  // agentID → LayeredMemory（非空表示启用了分层）
	shared          *memory.SharedMemory              // 全局共享内存

	stateStore StateStore          // 持久化存储（可选）
	config     MemoryConfig
	compactCount int

	mu sync.RWMutex
}

// NewMemoryManager 创建 MemoryManager
func NewMemoryManager(stateStore StateStore, config MemoryConfig) *MemoryManager {
	if config.LocalMaxSize <= 0 {
		config.LocalMaxSize = 200
	}
	if config.SharedMaxSize <= 0 {
		config.SharedMaxSize = 500
	}

	return &MemoryManager{
		agentMemories:   make(map[string]memory.Memory),
		layeredMemories: make(map[string]*memory.LayeredMemory),
		shared:          memory.NewSharedMemory(config.SharedMaxSize),
		stateStore:      stateStore,
		config:          config,
	}
}

// CreateMemory 为指定 Agent 创建分层内存
func (m *MemoryManager) CreateMemory(agentID string, cfg MemoryConfig) *memory.LayeredMemory {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 如果已存在，返回现有实例
	if existing, ok := m.layeredMemories[agentID]; ok {
		return existing
	}

	localMax := cfg.LocalMaxSize
	if localMax <= 0 {
		localMax = m.config.LocalMaxSize
	}

	local := memory.NewLocalMemory(agentID, localMax)
	layered := memory.NewLayeredMemory(local, m.shared, memory.DefaultLayeredConfig())

	m.layeredMemories[agentID] = layered
	m.agentMemories[agentID] = layered

	slog.Debug("Created layered memory for agent", "agent_id", agentID, "local_max", localMax)
	return layered
}

// CreateSimpleMemory 为指定 Agent 创建简单的 ConversationMemory（向后兼容）
func (m *MemoryManager) CreateSimpleMemory(agentID string, maxSize int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if maxSize <= 0 {
		maxSize = 300
	}

	cm := memory.NewConversationMemory(maxSize)
	m.agentMemories[agentID] = cm

	slog.Debug("Created simple memory for agent", "agent_id", agentID, "max_size", maxSize)
}

// GetMemory 获取 Agent 的内存（Memory 接口）
func (m *MemoryManager) GetMemory(agentID string) memory.Memory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.agentMemories[agentID]
}

// GetLayeredMemory 获取 Agent 的分层内存
func (m *MemoryManager) GetLayeredMemory(agentID string) *memory.LayeredMemory {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.layeredMemories[agentID]
}

// GetSharedMemory 获取全局共享内存
func (m *MemoryManager) GetSharedMemory() *memory.SharedMemory {
	return m.shared
}

// HasMemory 检查 Agent 是否有内存
func (m *MemoryManager) HasMemory(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, ok := m.agentMemories[agentID]
	return ok
}

// RemoveMemory 移除 Agent 的内存
func (m *MemoryManager) RemoveMemory(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.agentMemories, agentID)
	delete(m.layeredMemories, agentID)
	slog.Debug("Removed memory for agent", "agent_id", agentID)
}

// Compact 触发指定 Agent 的内存压缩
func (m *MemoryManager) Compact(agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if layered, ok := m.layeredMemories[agentID]; ok {
		// 对分层内存：将本地消息提升到共享，然后裁剪本地
		if layered.SharedSize() > 0 {
			// 已有共享内容，裁剪本地
			return layered.GetLocalMemory().Trim(m.config.LocalMaxSize / 2)
		}
		// 首次压缩：将消息提升到共享
		msgs := layered.GetLocalMemory().GetMessages()
		if len(msgs) > m.config.CompactionTrigger && m.config.CompactionTrigger > 0 {
			// 将最近消息保留在本地，其余提升到共享
			keepCount := m.config.LocalMaxSize / 2
			if keepCount > len(msgs) {
				keepCount = len(msgs)
			}
			for _, msg := range msgs[:len(msgs)-keepCount] {
				_ = layered.PromoteToShared(msg)
			}
			// 裁剪本地
			return layered.GetLocalMemory().Trim(keepCount)
		}
	}
	return nil
}

// CompactAll 触发所有 Agent 的内存压缩
func (m *MemoryManager) CompactAll() {
	m.mu.RLock()
	agentIDs := make([]string, 0, len(m.layeredMemories))
	for id := range m.layeredMemories {
		agentIDs = append(agentIDs, id)
	}
	m.mu.RUnlock()

	for _, id := range agentIDs {
		if err := m.Compact(id); err != nil {
			slog.Warn("Memory compaction failed", "agent_id", id, "error", err)
		}
	}
	m.compactCount++
}

// Persist 持久化指定 Agent 的内存到 StateStore
func (m *MemoryManager) Persist(agentID string) error {
	if m.stateStore == nil {
		return fmt.Errorf("state store not available")
	}

	mem := m.GetMemory(agentID)
	if mem == nil {
		return fmt.Errorf("no memory for agent %s", agentID)
	}

	// 构建持久化状态
	state := &ConductorState{
		SessionID: fmt.Sprintf("memory_%s_%d", agentID, time.Now().UnixNano()),
		Phase:     PhaseIdle,
		Version:   1,
	}
	// 在 Context 中存储内存快照
	if state.Context == nil {
		state.Context = make(map[string]interface{})
	}
	state.Context["memory_type"] = fmt.Sprintf("%T", mem)
	state.Context["message_count"] = mem.Size()
	state.SavedAt = time.Now()

	return m.stateStore.Save(state)
}

// Restore 从 StateStore 恢复 Agent 的内存
func (m *MemoryManager) Restore(agentID string) error {
	if m.stateStore == nil {
		return fmt.Errorf("state store not available")
	}
	// 当前简化实现：只验证状态是否存在
	// 完整实现需要序列化/反序列化 ChatMessage
	sessions, err := m.stateStore.List()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		state, err := m.stateStore.Load(session)
		if err != nil {
			continue
		}
		if state.Context != nil {
			if id, ok := state.Context["agent_id"].(string); ok && id == agentID {
				slog.Info("Found persisted state for agent", "agent_id", agentID, "session", session)
				return nil
			}
		}
	}
	return fmt.Errorf("no persisted state found for agent %s", agentID)
}

// ShareToGlobal 将消息共享到全局内存
func (m *MemoryManager) ShareToGlobal(agentID string, msg memory.ChatMessage) error {
	layered := m.GetLayeredMemory(agentID)
	if layered == nil {
		// 如果没有分层内存，直接写入共享内存
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]interface{})
		}
		msg.Metadata["agent_id"] = agentID
		return m.shared.AddMessage(msg)
	}
	return layered.PromoteToShared(msg)
}

// UpdateConfig 更新内存管理器配置
func (m *MemoryManager) UpdateConfig(cfg MemoryConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cfg.LocalMaxSize > 0 {
		m.config.LocalMaxSize = cfg.LocalMaxSize
	}
	if cfg.SharedMaxSize > 0 {
		m.config.SharedMaxSize = cfg.SharedMaxSize
	}
	if cfg.CompactionTrigger > 0 {
		m.config.CompactionTrigger = cfg.CompactionTrigger
	}
	m.config.AutoPersist = cfg.AutoPersist

	slog.Info("MemoryManager config updated", "config", m.config)
}

// GetStats 获取所有 Agent 的内存统计
func (m *MemoryManager) GetStats() map[string]MemoryStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make(map[string]MemoryStats)

	for agentID, mem := range m.agentMemories {
		s := MemoryStats{
			AgentID:   agentID,
			LocalSize: mem.Size(),
			TotalSize: mem.Size(),
		}

		// 如果是分层内存，获取更详细的统计
		if layered, ok := m.layeredMemories[agentID]; ok {
			s.LocalSize = layered.LocalSize()
			s.SharedSize = layered.SharedSize()
			s.TotalSize = layered.Size()
			s.LocalMaxSize = m.config.LocalMaxSize
			s.SharedMaxSize = m.config.SharedMaxSize
		}

		s.CompactCount = m.compactCount

		stats[agentID] = s
	}

	return stats
}

// GetGlobalStats 获取共享内存统计
func (m *MemoryManager) GetGlobalStats() MemoryStats {
	return MemoryStats{
		AgentID:       "__shared__",
		SharedSize:    m.shared.Size(),
		SharedMaxSize: m.config.SharedMaxSize,
	}
}

// ClearAll 清空所有 Agent 的内存
func (m *MemoryManager) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, mem := range m.agentMemories {
		_ = mem.Clear()
	}
	_ = m.shared.Clear()

	slog.Info("Cleared all agent memories")
}

// Migration 相关

// MigrateToLayered 将指定 Agent 从 ConversationMemory 迁移到 LayeredMemory
func (m *MemoryManager) MigrateToLayered(agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 检查是否已经是分层内存
	if _, ok := m.layeredMemories[agentID]; ok {
		return false // 已经迁移过了
	}

	// 获取现有内存
	existing, ok := m.agentMemories[agentID]
	if !ok {
		return false // 没有现有内存
	}

	// 检查是否是 ConversationMemory
	cm, ok := existing.(*memory.ConversationMemory)
	if !ok {
		return false // 不是 ConversationMemory，无法迁移
	}

	// 执行迁移
	layered := memory.MigrateToLayered(cm, agentID, m.shared)
	m.layeredMemories[agentID] = layered
	m.agentMemories[agentID] = layered

	slog.Info("Migrated agent memory to layered", "agent_id", agentID)
	return true
}

// ListAgentIDs 列出所有有内存的 Agent ID
func (m *MemoryManager) ListAgentIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.agentMemories))
	for id := range m.agentMemories {
		ids = append(ids, id)
	}
	return ids
}

// ============================================================================
// Phase 3: Layered Memory Lifecycle Management
// ============================================================================

// GetPromotionPolicyForAgent 根据 agent 类型返回合适的 PromotionPolicy
func (m *MemoryManager) GetPromotionPolicyForAgent(agentType string) memory.PromotionPolicy {
	switch agentType {
	case "coding":
		return memory.DefaultCodingPromotionPolicy()
	case "repo":
		return memory.DefaultRepoPromotionPolicy()
	case "chat":
		return memory.DefaultPromotionPolicy()
	case "devops":
		return memory.DefaultPromotionPolicy()
	case "browser":
		return memory.DefaultPromotionPolicy()
	case "meta":
		return memory.DefaultPromotionPolicy()
	default:
		return memory.DefaultPromotionPolicy()
	}
}

// CreateAgentMemory 为指定 agent 创建带策略的 LayeredMemory
// 这是 CreateMemory 的增强版本，支持按 agent 类型配置 promotion 策略
func (m *MemoryManager) CreateAgentMemory(agentID string, agentType string) *memory.LayeredMemory {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 如果已存在，返回现有实例
	if existing, ok := m.layeredMemories[agentID]; ok {
		return existing
	}

	localMax := m.config.LocalMaxSize
	if localMax <= 0 {
		localMax = 200
	}

	local := memory.NewLocalMemory(agentID, localMax)
	layered := memory.NewLayeredMemory(local, m.shared, memory.DefaultLayeredConfig())

	// 设置 promotion 策略
	policy := m.GetPromotionPolicyForAgent(agentType)
	layered.SetPromotionPolicy(policy)
	layered.SetAgentID(agentID)

	m.layeredMemories[agentID] = layered
	m.agentMemories[agentID] = layered

	slog.Debug("Created agent memory with promotion policy",
		"agent_id", agentID,
		"agent_type", agentType,
		"local_max", localMax,
		"auto_promote_types", policy.AutoPromoteTypes,
		"summary_threshold", policy.SummaryThreshold)

	return layered
}

// ShareKeyFinding 将关键发现发布到 SharedMemory
// 这是 sub-agent 与 Conductor 共享信息的轻量方式（非全量 Memory 注入）
func (m *MemoryManager) ShareKeyFinding(agentID string, finding KeyFinding) error {
	layered := m.GetLayeredMemory(agentID)
	if layered == nil {
		return fmt.Errorf("no layered memory for agent %s", agentID)
	}

	msg := memory.ChatMessage{
		Type:      memory.MessageTypeAssistant,
		Content:   fmt.Sprintf("[KeyFinding:%s] %s", finding.Type, finding.Content),
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"type":         "key_finding",
			"finding_type": finding.Type,
			"agent_id":     agentID,
		},
	}
	// 合并额外的 meta
	if finding.Meta != nil {
		for k, v := range finding.Meta {
			msg.Metadata[k] = v
		}
	}

	return layered.PromoteToShared(msg)
}

// GetAgentMemoryStats 获取指定 agent 的详细内存统计
func (m *MemoryManager) GetAgentMemoryStats(agentID string) (*MemoryStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := &MemoryStats{
		AgentID:       agentID,
		LocalMaxSize:  m.config.LocalMaxSize,
		SharedMaxSize: m.config.SharedMaxSize,
		CompactCount:  m.compactCount,
	}

	if layered, ok := m.layeredMemories[agentID]; ok {
		stats.LocalSize = layered.LocalSize()
		stats.SharedSize = layered.SharedSize()
		stats.TotalSize = layered.Size()
	} else if mem, ok := m.agentMemories[agentID]; ok {
		stats.LocalSize = mem.Size()
		stats.TotalSize = mem.Size()
	} else {
		return nil, fmt.Errorf("no memory for agent %s", agentID)
	}

	return stats, nil
}

// GetMemorySnapshot 获取所有 agent 内存的快照信息（用于调试和监控）
func (m *MemoryManager) GetMemorySnapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := make(map[string]interface{})

	agentList := make([]map[string]interface{}, 0)
	for id, mem := range m.agentMemories {
		info := map[string]interface{}{
			"agent_id": id,
			"type":     fmt.Sprintf("%T", mem),
			"size":     mem.Size(),
		}
		if layered, ok := m.layeredMemories[id]; ok {
			info["local_size"] = layered.LocalSize()
			info["shared_size"] = layered.SharedSize()
		}
		agentList = append(agentList, info)
	}

	snapshot["agents"] = agentList
	snapshot["shared_size"] = m.shared.Size()
	snapshot["shared_version"] = m.shared.Snapshot()

	return snapshot
}
