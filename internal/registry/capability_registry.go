package registry

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// AgentCapability 描述一个 agent 的能力
type AgentCapability struct {
	AgentID      string                 `json:"agent_id"`    // 唯一标识（如 "coding-agent"）
	Name         string                 `json:"name"`        // 人类可读名称（如 "Code Engineer"）
	Description  string                 `json:"description"` // 自然语言描述
	Tags         []string               `json:"tags"`        // 能力标签（如 ["go", "python", "testing"]）
	InputSchema  map[string]interface{} `json:"input_schema,omitempty"`
	OutputSchema map[string]interface{} `json:"output_schema,omitempty"`
	Version      string                 `json:"version"`
	RegisteredAt time.Time              `json:"registered_at"`
}

// CapabilityQuery 查询条件
type CapabilityQuery struct {
	Text  string   // 自然语言描述（关键词匹配）
	Tags  []string // 标签过滤
	Name  string   // 精确名称匹配
	Limit int      // 默认 10
}

// ChangeHandler 能力变更回调
type ChangeHandler func(CapabilityEvent)

// CapabilityEvent 能力变更事件
type CapabilityEvent struct {
	Type       string // "registered" | "unregistered" | "updated"
	Capability AgentCapability
	Timestamp  time.Time
}

// CapabilityRegistry 接口定义
type CapabilityRegistry interface {
	// Register 注册或更新一个 Agent 的能力
	Register(cap AgentCapability) error
	// Unregister 注销一个 Agent 的能力
	Unregister(agentID string) error
	// Get 获取指定 agentID 的能力
	Get(agentID string) (AgentCapability, bool)
	// Search 按条件搜索能力
	Search(query CapabilityQuery) ([]AgentCapability, error)
	// List 列出所有已注册的能力
	List() []AgentCapability
	// SubscribeChanges 订阅能力变更事件
	SubscribeChanges(handler ChangeHandler) (unsubscribe func())
}

// handlerEntry 带唯一 ID 的 handler 条目
type handlerEntry struct {
	id      int64
	handler ChangeHandler
}

// capabilityRegistry 能力注册中心实现
type capabilityRegistry struct {
	mu            sync.RWMutex
	caps          map[string]AgentCapability // agentID → capability
	tagIndex      map[string]map[string]bool // tag → set of agentIDs
	handlers      []handlerEntry
	handlersMu    sync.RWMutex
	nextHandlerID int64
}

// NewCapabilityRegistry 创建新的能力注册中心
func NewCapabilityRegistry() CapabilityRegistry {
	return &capabilityRegistry{
		caps:     make(map[string]AgentCapability),
		tagIndex: make(map[string]map[string]bool),
		handlers: make([]handlerEntry, 0),
	}
}

// Register 注册或更新一个 Agent 的能力
func (r *capabilityRegistry) Register(cap AgentCapability) error {
	if cap.AgentID == "" {
		return &RegistryError{
			Reason: "agentID cannot be empty",
			Cap:    cap,
		}
	}

	r.mu.Lock()
	existing, exists := r.caps[cap.AgentID]
	r.mu.Unlock()

	if exists {
		// 检查是否有实际变更
		if capabilitiesEqual(existing, cap) {
			return nil
		}
	}

	event := CapabilityEvent{
		Type:       "registered",
		Capability: cap,
		Timestamp:  time.Now(),
	}

	if exists {
		event.Type = "updated"
	}

	// 更新内部状态
	r.mu.Lock()
	if exists {
		// 先清除旧标签索引
		r.clearTagIndex(existing.Tags, cap.AgentID)
	}
	r.caps[cap.AgentID] = cap
	// 添加新标签索引
	r.addTagIndex(cap.Tags, cap.AgentID)
	r.mu.Unlock()

	// 通知所有订阅者
	r.notifyHandlers(event)

	return nil
}

// Unregister 注销一个 Agent 的能力
func (r *capabilityRegistry) Unregister(agentID string) error {
	if agentID == "" {
		return &RegistryError{
			Reason: "agentID cannot be empty",
		}
	}

	r.mu.Lock()
	cap, exists := r.caps[agentID]
	if !exists {
		r.mu.Unlock()
		return &RegistryError{
			Reason: "agent not found",
		}
	}

	// 清除所有标签索引
	r.clearTagIndex(cap.Tags, agentID)
	delete(r.caps, agentID)
	r.mu.Unlock()

	// 通知所有订阅者
	event := CapabilityEvent{
		Type:       "unregistered",
		Capability: cap,
		Timestamp:  time.Now(),
	}
	r.notifyHandlers(event)

	return nil
}

// Get 获取指定 agentID 的能力
func (r *capabilityRegistry) Get(agentID string) (AgentCapability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cap, ok := r.caps[agentID]
	return cap, ok
}

// Search 按条件搜索能力
func (r *capabilityRegistry) Search(query CapabilityQuery) ([]AgentCapability, error) {
	// 设置默认值
	if query.Limit <= 0 {
		query.Limit = 10
	}

	// 构建标签查询集合（用于高效查找）
	tagQuery := make(map[string]bool)
	for _, tag := range query.Tags {
		tagQuery[tag] = true
	}

	// 收集所有匹配的能力及其得分
	type scoredCapability struct {
		cap   AgentCapability
		score float64
	}

	var results []scoredCapability

	r.mu.RLock()

	for _, cap := range r.caps {
		score := 0.0
		matched := false

		// 1. Name 精确匹配（最高优先级）
		if query.Name != "" && strings.EqualFold(cap.Name, query.Name) {
			score += 100.0
			matched = true
		}

		// 2. Tags 过滤 + 匹配数评分
		if len(query.Tags) > 0 {
			tagMatchCount := 0
			for _, capTag := range cap.Tags {
				if tagQuery[capTag] {
					tagMatchCount++
				}
			}
			if tagMatchCount > 0 {
				score += float64(tagMatchCount) * 10.0
				matched = true
			}
		}

		// 3. Text 关键词评分算法
		if query.Text != "" {
			textScore := r.scoreByText(query.Text, cap)
			if textScore > 0 {
				score += textScore
				matched = true
			}
		}

		// 如果没有任何查询条件，返回所有能力（基础分 0）
		if query.Name == "" && len(query.Tags) == 0 && query.Text == "" {
			matched = true
		}

		if matched {
			results = append(results, scoredCapability{
				cap:   cap,
				score: score,
			})
		}
	}

	r.mu.RUnlock()

	// 排序：按得分降序
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	// 截断到 Limit
	if len(results) > query.Limit {
		results = results[:query.Limit]
	}

	// 提取结果
	caps := make([]AgentCapability, len(results))
	for i, sc := range results {
		caps[i] = sc.cap
	}

	return caps, nil
}

// scoreByText 计算文本关键词评分
func (r *capabilityRegistry) scoreByText(queryText string, cap AgentCapability) float64 {
	lowerQuery := strings.ToLower(queryText)
	words := extractKeywords(lowerQuery)
	if len(words) == 0 {
		return 0
	}

	score := 0.0
	matchCount := 0

	// Name 匹配 = 10分（每个关键词）
	for _, word := range words {
		if strings.Contains(strings.ToLower(cap.Name), word) {
			score += 10.0
			matchCount++
		}
	}

	// Description 匹配 = 5分（每个关键词）
	for _, word := range words {
		if strings.Contains(strings.ToLower(cap.Description), word) {
			score += 5.0
			matchCount++
		}
	}

	// Tag 匹配 = 3分（每个关键词）
	for _, word := range words {
		for _, tag := range cap.Tags {
			if strings.Contains(strings.ToLower(tag), word) || strings.Contains(word, strings.ToLower(tag)) {
				score += 3.0
				matchCount++
				break // 每个关键词在 tags 中只匹配一次
			}
		}
	}

	// 如果所有关键词都匹配到，给予额外奖励
	if matchCount >= len(words) {
		score *= 1.2
	}

	return score
}

// extractKeywords 从查询文本中提取关键词
func extractKeywords(text string) []string {
	// 简单按空格和标点分割
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	// 替换常见标点为空格
	specialChars := []string{".", ",", "!", "?", ";", ":", "'", "\"", "(", ")", "[", "]", "{", "}"}
	for _, ch := range specialChars {
		text = strings.ReplaceAll(text, ch, " ")
	}

	parts := strings.Fields(text)
	var keywords []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) > 0 {
			keywords = append(keywords, p)
		}
	}
	return keywords
}

// List 列出所有已注册的能力
func (r *capabilityRegistry) List() []AgentCapability {
	r.mu.RLock()
	defer r.mu.RUnlock()

	caps := make([]AgentCapability, 0, len(r.caps))
	for _, cap := range r.caps {
		caps = append(caps, cap)
	}

	// 按注册时间排序（最新的在前）
	sort.SliceStable(caps, func(i, j int) bool {
		return caps[i].RegisteredAt.After(caps[j].RegisteredAt)
	})

	return caps
}

// SubscribeChanges 订阅能力变更事件
func (r *capabilityRegistry) SubscribeChanges(handler ChangeHandler) (unsubscribe func()) {
	if handler == nil {
		return func() {}
	}

	r.handlersMu.Lock()
	entry := handlerEntry{
		id:      r.nextHandlerID,
		handler: handler,
	}
	r.nextHandlerID++
	r.handlers = append(r.handlers, entry)
	entryID := entry.id
	r.handlersMu.Unlock()

	return func() {
		r.handlersMu.Lock()
		defer r.handlersMu.Unlock()
		for i, e := range r.handlers {
			if e.id == entryID {
				r.handlers = append(r.handlers[:i], r.handlers[i+1:]...)
				return
			}
		}
	}
}

// notifyHandlers 通知所有订阅者
func (r *capabilityRegistry) notifyHandlers(event CapabilityEvent) {
	r.handlersMu.RLock()
	handlers := make([]ChangeHandler, len(r.handlers))
	for i, e := range r.handlers {
		handlers[i] = e.handler
	}
	r.handlersMu.RUnlock()

	for _, handler := range handlers {
		// 防止回调 panic 导致崩溃
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					// 静默处理 panic，避免影响其他 handler
					_ = rec
				}
			}()
			handler(event)
		}()
	}
}

// clearTagIndex 从标签索引中移除指定的标签
func (r *capabilityRegistry) clearTagIndex(tags []string, agentID string) {
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if agentIDs, ok := r.tagIndex[tag]; ok {
			delete(agentIDs, agentID)
			if len(agentIDs) == 0 {
				delete(r.tagIndex, tag)
			}
		}
	}
}

// addTagIndex 添加标签到索引
func (r *capabilityRegistry) addTagIndex(tags []string, agentID string) {
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := r.tagIndex[tag]; !ok {
			r.tagIndex[tag] = make(map[string]bool)
		}
		r.tagIndex[tag][agentID] = true
	}
}

// capabilitiesEqual 检查两个能力是否相同
func capabilitiesEqual(a, b AgentCapability) bool {
	if a.AgentID != b.AgentID ||
		a.Name != b.Name ||
		a.Description != b.Description ||
		a.Version != b.Version {
		return false
	}

	// 比较 Tags
	if len(a.Tags) != len(b.Tags) {
		return false
	}
	tagSetA := make(map[string]bool, len(a.Tags))
	for _, t := range a.Tags {
		tagSetA[t] = true
	}
	for _, t := range b.Tags {
		if !tagSetA[t] {
			return false
		}
	}

	// 比较 InputSchema
	if len(a.InputSchema) != len(b.InputSchema) {
		return false
	}
	for k, v := range a.InputSchema {
		if b.InputSchema[k] != v {
			return false
		}
	}

	// 比较 OutputSchema
	if len(a.OutputSchema) != len(b.OutputSchema) {
		return false
	}
	for k, v := range a.OutputSchema {
		if b.OutputSchema[k] != v {
			return false
		}
	}

	return true
}

// RegistryError 注册中心错误
type RegistryError struct {
	Reason string
	Cap    AgentCapability
}

func (e *RegistryError) Error() string {
	if e.Reason != "" {
		return "registry error: " + e.Reason
	}
	return "registry error"
}

// GetCapabilitiesByTag 通过标签获取能力列表
func (r *capabilityRegistry) GetCapabilitiesByTag(tag string) []AgentCapability {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	agentIDs, ok := r.tagIndex[tag]
	if !ok || len(agentIDs) == 0 {
		return nil
	}

	result := make([]AgentCapability, 0, len(agentIDs))
	for id := range agentIDs {
		if cap, exists := r.caps[id]; exists {
			result = append(result, cap)
		}
	}

	// 按注册时间排序
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].RegisteredAt.After(result[j].RegisteredAt)
	})

	return result
}

// GetAgentCount 获取已注册的 Agent 数量
func (r *capabilityRegistry) GetAgentCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.caps)
}

// GetTagCount 获取索引的标签数量
func (r *capabilityRegistry) GetTagCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tagIndex)
}
