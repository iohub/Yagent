package memory

import (
	"fmt"
	"strings"
	"sync"
)

// LayeredConfig configures how LayeredMemory merges local and shared context.
type LayeredConfig struct {
	LocalFirst      bool     // local messages first in GetContext (default: true)
	IncludeShared   bool     // include shared in context (default: true)
	SharedTagFilter []string // only include shared messages with these tags
}

// DefaultLayeredConfig returns default layered configuration.
func DefaultLayeredConfig() LayeredConfig {
	return LayeredConfig{
		LocalFirst:    true,
		IncludeShared: true,
	}
}

// LayeredMemory composes LocalMemory + SharedMemory.
// Reads merge both layers; writes go to Local by default.
type LayeredMemory struct {
	local  *LocalMemory
	shared *SharedMemory
	config LayeredConfig
	mu     sync.RWMutex
	// Auto-Promote fields
	policy           PromotionPolicy // auto-promote 策略
	agentID          string          // 所属 agent 的唯一 ID
	lastSummaryIndex int             // 上次摘要到的消息索引
}

// NewLayeredMemory creates a new layered memory.
func NewLayeredMemory(local *LocalMemory, shared *SharedMemory, config LayeredConfig) *LayeredMemory {
	if local == nil {
		panic("local memory must not be nil")
	}
	return &LayeredMemory{
		local:  local,
		shared: shared,
		config: config,
		policy: DefaultPromotionPolicy(), // 默认使用平衡策略
	}
}

// AddMessage adds a message to local memory.
func (lm *LayeredMemory) AddMessage(msg ChatMessage) error {
	return lm.local.AddMessage(msg)
}

// GetMessages returns merged messages (local + shared).
func (lm *LayeredMemory) GetMessages() []ChatMessage {
	localMsgs := lm.local.GetMessages()

	if !lm.config.IncludeShared || lm.shared == nil {
		return localMsgs
	}

	sharedMsgs := lm.shared.GetMessages()

	// Merge: shared first, then local (or vice versa based on config)
	merged := make([]ChatMessage, 0, len(localMsgs)+len(sharedMsgs))
	if lm.config.LocalFirst {
		merged = append(merged, localMsgs...)
		merged = append(merged, sharedMsgs...)
	} else {
		merged = append(merged, sharedMsgs...)
		merged = append(merged, localMsgs...)
	}

	return merged
}

// GetContext returns formatted context with layer markers.
func (lm *LayeredMemory) GetContext() string {
	var sb strings.Builder

	if lm.config.IncludeShared && lm.shared != nil {
		sharedCtx := lm.shared.GetContext()
		if sharedCtx != "" {
			sb.WriteString(sharedCtx)
			sb.WriteString("\n")
		}
	}

	localCtx := lm.local.GetContext()
	if localCtx != "" {
		sb.WriteString(localCtx)
	}

	return strings.TrimSpace(sb.String())
}

// GetContextWithLayers returns formatted context clearly showing layer separation.
func (lm *LayeredMemory) GetContextWithLayers() string {
	var sb strings.Builder

	if lm.config.IncludeShared && lm.shared != nil {
		sharedMsgs := lm.shared.GetMessages()
		if len(sharedMsgs) > 0 {
			sb.WriteString("─── Shared Context ───\n")
			for _, msg := range sharedMsgs {
				sb.WriteString(fmt.Sprintf("[%s] %s\n", string(msg.Type), msg.Content))
			}
			sb.WriteString("\n")
		}
	}

	localMsgs := lm.local.GetMessages()
	if len(localMsgs) > 0 {
		sb.WriteString(fmt.Sprintf("─── Local Context (Agent: %s) ───\n", lm.local.AgentID()))
		for _, msg := range localMsgs {
			sb.WriteString(fmt.Sprintf("[%s] %s\n", string(msg.Type), msg.Content))
		}
	}

	return strings.TrimSpace(sb.String())
}

// Clear clears local memory only (shared is global).
func (lm *LayeredMemory) Clear() error {
	return lm.local.Clear()
}

// Size returns total message count (local + shared).
func (lm *LayeredMemory) Size() int {
	total := lm.local.Size()
	if lm.config.IncludeShared && lm.shared != nil {
		total += lm.shared.Size()
	}
	return total
}

// LocalSize returns local memory size.
func (lm *LayeredMemory) LocalSize() int {
	return lm.local.Size()
}

// SharedSize returns shared memory size.
func (lm *LayeredMemory) SharedSize() int {
	if lm.shared == nil {
		return 0
	}
	return lm.shared.Size()
}

// GetLocalMemory returns the underlying local memory.
func (lm *LayeredMemory) GetLocalMemory() *LocalMemory {
	return lm.local
}

// GetSharedMemory returns the underlying shared memory.
func (lm *LayeredMemory) GetSharedMemory() *SharedMemory {
	return lm.shared
}

// PromoteToShared promotes a local message to shared memory.
// The message must exist in local memory (matched by content and type).
func (lm *LayeredMemory) PromoteToShared(msg ChatMessage) error {
	if lm.shared == nil {
		return fmt.Errorf("shared memory not available")
	}
	// Tag the message with agent info
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata["agent_id"] = lm.local.AgentID()
	msg.Metadata["promoted"] = true

	return lm.shared.AddMessage(msg)
}

// PromoteLastToShared promotes the most recent local message to shared memory.
func (lm *LayeredMemory) PromoteLastToShared() error {
	msgs := lm.local.GetMessages()
	if len(msgs) == 0 {
		return fmt.Errorf("no local messages to promote")
	}
	return lm.PromoteToShared(msgs[len(msgs)-1])
}

// ============================================================================
// Phase 3: Auto-Promote Policy
// ============================================================================

// PromotionPolicy defines when/how to promote local messages to shared memory
type PromotionPolicy struct {
	// MaxLocalMessages 触发评估的消息阈值（超过此数量检查是否需要 promote）
	MaxLocalMessages int
	// AutoPromoteTypes 消息 metadata.type 匹配时自动提升
	AutoPromoteTypes []string
	// AutoPromoteKeywords 消息内容包含这些关键词时自动提升（不区分大小写）
	AutoPromoteKeywords []string
	// SummaryThreshold 每 N 条消息生成一次摘要并 promote（0=禁用）
	SummaryThreshold int
}

// DefaultCodingPromotionPolicy returns default promotion policy for coding agents
func DefaultCodingPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{
		MaxLocalMessages:   50,
		AutoPromoteTypes:   []string{"task_completion", "file_change", "error", "build_result", "key_finding"},
		AutoPromoteKeywords: []string{"error", "failed", "created", "modified", "deleted", "summary"},
		SummaryThreshold:   20,
	}
}

// DefaultRepoPromotionPolicy returns default promotion policy for repo agents
func DefaultRepoPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{
		MaxLocalMessages:   30,
		AutoPromoteTypes:   []string{"symbol_table_update", "dependency_change", "task_completion", "error", "key_finding"},
		AutoPromoteKeywords: []string{"symbol", "import", "export", "dependency", "architecture"},
		SummaryThreshold:   15,
	}
}

// DefaultPromotionPolicy returns a balanced default promotion policy
func DefaultPromotionPolicy() PromotionPolicy {
	return PromotionPolicy{
		MaxLocalMessages:   40,
		AutoPromoteTypes:   []string{"task_completion", "error", "key_finding"},
		AutoPromoteKeywords: []string{"error", "completed", "summary"},
		SummaryThreshold:   20,
	}
}

// SetPromotionPolicy 设置自动提升策略
func (lm *LayeredMemory) SetPromotionPolicy(policy PromotionPolicy) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.policy = policy
}

// GetPromotionPolicy 获取自动提升策略
func (lm *LayeredMemory) GetPromotionPolicy() PromotionPolicy {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return lm.policy
}

// SetAgentID 设置 agent ID
func (lm *LayeredMemory) SetAgentID(agentID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.agentID = agentID
}

// AddMessageWithPromote 是 AddMessage 的增强版本，添加 auto-promote 逻辑
// 如果策略启用，会在添加消息后自动评估是否需要提升到 SharedMemory
func (lm *LayeredMemory) AddMessageWithPromote(msg ChatMessage) error {
	lm.mu.Lock()

	// 添加到 local memory
	if err := lm.local.AddMessage(msg); err != nil {
		lm.mu.Unlock()
		return err
	}

	// 检查是否需要提升当前消息
	if lm.shared != nil && lm.shouldPromote(msg) {
		promotedMsg := lm.preparePromoteMessage(msg)
		lm.mu.Unlock()
		// Publish outside lock
		_ = lm.shared.Publish(promotedMsg, "agent_updates")
		return nil
	}

	// 检查是否需要批量摘要
	if lm.policy.SummaryThreshold > 0 && lm.shared != nil {
		localMsgs := lm.local.GetMessages()
		if len(localMsgs)-lm.lastSummaryIndex >= lm.policy.SummaryThreshold {
			summaryMsg := lm.generateSummary(localMsgs)
			lm.lastSummaryIndex = len(localMsgs)
			lm.mu.Unlock()
			_ = lm.shared.Publish(summaryMsg, "auto_summary")
			return nil
		}
	}

	// 检查是否需要裁剪 local memory（超过最大阈值时先 promote 重要的再裁剪）
	if lm.policy.MaxLocalMessages > 0 && lm.shared != nil {
		localMsgs := lm.local.GetMessages()
		if len(localMsgs) > lm.policy.MaxLocalMessages {
			trimTo := lm.policy.MaxLocalMessages * 2 / 3 // 裁剪到 2/3
			lm.promoteBeforeTrim(localMsgs, trimTo)
			lm.mu.Unlock()
			_ = lm.local.Trim(trimTo)
			return nil
		}
	}

	lm.mu.Unlock()
	return nil
}

// shouldPromote 检查消息是否满足提升条件
func (lm *LayeredMemory) shouldPromote(msg ChatMessage) bool {
	if lm.policy.MaxLocalMessages == 0 {
		return false // 没有启用策略
	}

	// 检查消息类型
	if msgType, ok := msg.Metadata["type"].(string); ok {
		for _, t := range lm.policy.AutoPromoteTypes {
			if msgType == t {
				return true
			}
		}
	}

	// 检查内容关键词
	if len(lm.policy.AutoPromoteKeywords) > 0 && msg.Content != "" {
		contentLower := strings.ToLower(msg.Content)
		for _, kw := range lm.policy.AutoPromoteKeywords {
			if strings.Contains(contentLower, strings.ToLower(kw)) {
				return true
			}
		}
	}

	return false
}

// preparePromoteMessage 准备要提升的消息（添加元数据）
func (lm *LayeredMemory) preparePromoteMessage(msg ChatMessage) ChatMessage {
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata["agent_id"] = lm.agentID
	msg.Metadata["promoted"] = true
	msg.Metadata["promoted_at_version"] = fmt.Sprintf("%d", lm.local.Size())
	return msg
}

// generateSummary 生成批量摘要消息
func (lm *LayeredMemory) generateSummary(messages []ChatMessage) ChatMessage {
	newMsgs := messages[lm.lastSummaryIndex:]
	if len(newMsgs) == 0 {
		return ChatMessage{}
	}

	var summaryParts []string
	for _, msg := range newMsgs {
		if msg.Type == MessageTypeAssistant && msg.Content != "" {
			content := msg.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			summaryParts = append(summaryParts, content)
		}
	}

	summaryContent := "[Auto Summary]\n"
	if len(summaryParts) > 0 {
		summaryContent += strings.Join(summaryParts, "\n---\n")
	} else {
		summaryContent += fmt.Sprintf("Processed %d messages", len(newMsgs))
	}

	return ChatMessage{
		Type:    MessageTypeAssistant,
		Content: summaryContent,
		Metadata: map[string]interface{}{
			"type":      "auto_summary",
			"agent_id":  lm.agentID,
			"msg_count": len(newMsgs),
		},
	}
}

// promoteBeforeTrim 在裁剪 local memory 之前提升重要消息
func (lm *LayeredMemory) promoteBeforeTrim(messages []ChatMessage, keepCount int) {
	if len(messages) <= keepCount {
		return
	}
	// 只考虑要被裁剪的部分（前 len - keepCount 条）
	trimCount := len(messages) - keepCount
	for i := 0; i < trimCount; i++ {
		if lm.shouldPromote(messages[i]) {
			promotedMsg := lm.preparePromoteMessage(messages[i])
			_ = lm.shared.Publish(promotedMsg, "agent_updates")
		}
	}
}

// GetLocalMessages 获取仅 local 的消息（不合并 shared）
func (lm *LayeredMemory) GetLocalMessages() []ChatMessage {
	return lm.local.GetMessages()
}
