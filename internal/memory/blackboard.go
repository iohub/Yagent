package memory

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// BlackboardRegion 黑板区域类型，用于结构化协作空间
type BlackboardRegion string

const (
	// RegionTasks 任务定义与子任务
	RegionTasks BlackboardRegion = "tasks"
	// RegionFindings 中间发现/分析结果
	RegionFindings BlackboardRegion = "findings"
	// RegionDecisions 设计决策与取舍
	RegionDecisions BlackboardRegion = "decisions"
	// RegionQuestions 待回答问题
	RegionQuestions BlackboardRegion = "questions"
	// RegionArtifacts 最终产物
	RegionArtifacts BlackboardRegion = "artifacts"
)

// AllRegions 返回所有预定义的黑板区域列表
var AllRegions = []BlackboardRegion{
	RegionTasks, RegionFindings, RegionDecisions, RegionQuestions, RegionArtifacts,
}

// BlackboardEntry 黑板条目，表示一条结构化知识单元
type BlackboardEntry struct {
	// ID 唯一标识，格式为 "bb-{seq}"
	ID string `json:"id"`
	// Region 所属区域
	Region BlackboardRegion `json:"region"`
	// Author 发布者 agent ID
	Author string `json:"author"`
	// Content 结构化内容
	Content map[string]interface{} `json:"content"`
	// Tags 可发现性标签
	Tags []string `json:"tags"`
	// References 引用的其他 entry ID
	References []string `json:"references"`
	// Status 条目状态: draft|committed|superseded|closed
	Status string `json:"status"`
	// Version MVCC 版本号
	Version int `json:"version"`
	// CreatedAt 创建时间
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt 最后更新时间
	UpdatedAt time.Time `json:"updated_at"`
}

// BlackboardFilter 用于过滤黑板条目的查询条件
type BlackboardFilter struct {
	// Tags 按标签过滤（需全部匹配）
	Tags []string
	// Author 按发布者 ID 过滤
	Author string
	// Status 按状态过滤
	Status string
	// Since 仅返回此时间之后的条目
	Since time.Time
	// Limit 返回数量上限，默认 50
	Limit int
}

// BlackboardEventType 黑板事件类型
type BlackboardEventType string

const (
	// EventPosted 新条目发布
	EventPosted BlackboardEventType = "posted"
	// EventUpdated 条目更新
	EventUpdated BlackboardEventType = "updated"
	// EventStatusChanged 状态变更
	EventStatusChanged BlackboardEventType = "status_changed"
)

// BlackboardEvent 黑板变更事件
type BlackboardEvent struct {
	// Type 事件类型
	Type BlackboardEventType
	// Entry 相关的条目
	Entry BlackboardEntry
}

// Blackboard 黑板接口，提供分布式协作空间的读写与订阅能力
type Blackboard interface {
	// Post 向指定区域发布新条目，返回条目 ID
	Post(region BlackboardRegion, author string, content map[string]interface{},
		tags []string, references []string) (string, error)
	// Read 按区域和过滤器读取条目
	Read(region BlackboardRegion, filter BlackboardFilter) ([]BlackboardEntry, error)
	// Get 按 ID 获取单个条目
	Get(entryID string) (BlackboardEntry, bool)
	// Update 更新条目的内容（MVCC 乐观锁）
	Update(entryID string, author string, content map[string]interface{}) error
	// SetStatus 修改条目状态，触发状态变更事件
	SetStatus(entryID string, author string, status string) error
	// Subscribe 按区域订阅变更事件，返回取消订阅函数
	Subscribe(region BlackboardRegion, handler func(BlackboardEvent)) func()
	// Snapshot 导出全量快照（深拷贝）
	Snapshot() map[BlackboardRegion][]BlackboardEntry
}

// blackboard 黑板的具体实现，线程安全
type blackboard struct {
	mu          sync.RWMutex
	entries     map[string]BlackboardEntry
	regionIndex map[BlackboardRegion][]string
	tagIndex    map[string]map[string]bool
	subscribers map[BlackboardRegion]*subscriberRegistry
	idCounter   int64
}

// NewBlackboard 创建新的黑板实例
func NewBlackboard() Blackboard {
	return &blackboard{
		entries:     make(map[string]BlackboardEntry),
		regionIndex: make(map[BlackboardRegion][]string),
		tagIndex:    make(map[string]map[string]bool),
		subscribers: make(map[BlackboardRegion]*subscriberRegistry),
	}
}

// Post 向指定区域发布新条目
func (bb *blackboard) Post(region BlackboardRegion, author string, content map[string]interface{},
	tags []string, references []string) (string, error) {

	// 验证区域
	valid := false
	for _, r := range AllRegions {
		if r == region {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("invalid blackboard region: %s", region)
	}

	// 验证内容
	if content == nil {
		return "", fmt.Errorf("content cannot be nil")
	}

	now := time.Now()

	bb.mu.Lock()
	defer bb.mu.Unlock()

	// 生成唯一 ID
	seq := atomic.AddInt64(&bb.idCounter, 1)
	entryID := fmt.Sprintf("bb-%d", seq)

	entry := BlackboardEntry{
		ID:         entryID,
		Region:     region,
		Author:     author,
		Content:    content,
		Tags:       copyStringSlice(tags),
		References: copyStringSlice(references),
		Status:     "committed",
		Version:    1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	// 存储条目
	bb.entries[entryID] = entry

	// 更新区域索引
	bb.regionIndex[region] = append(bb.regionIndex[region], entryID)

	// 更新标签索引
	for _, tag := range entry.Tags {
		if bb.tagIndex[tag] == nil {
			bb.tagIndex[tag] = make(map[string]bool)
		}
		bb.tagIndex[tag][entryID] = true
	}

	// 异步通知订阅者
	go bb.notifySubscribers(region, EventPosted, entry)

	return entryID, nil
}

// Read 按区域和过滤器读取条目
func (bb *blackboard) Read(region BlackboardRegion, filter BlackboardFilter) ([]BlackboardEntry, error) {
	// 验证区域
	valid := false
	for _, r := range AllRegions {
		if r == region {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("invalid blackboard region: %s", region)
	}

	// 设置默认 Limit
	if filter.Limit <= 0 {
		filter.Limit = 50
	}

	bb.mu.RLock()
	defer bb.mu.RUnlock()

	// 获取区域下的所有条目 ID
	ids, ok := bb.regionIndex[region]
	if !ok {
		return nil, nil
	}

	// 收集匹配的条目
	var results []BlackboardEntry
	for _, id := range ids {
		entry, exists := bb.entries[id]
		if !exists {
			continue
		}

		// 按状态过滤
		if filter.Status != "" && entry.Status != filter.Status {
			continue
		}

		// 按作者过滤
		if filter.Author != "" && entry.Author != filter.Author {
			continue
		}

		// 按时间过滤
		if !filter.Since.IsZero() && entry.CreatedAt.Before(filter.Since) {
			continue
		}

		// 按标签过滤（需全部匹配）
		if len(filter.Tags) > 0 {
			if !containsAllTags(entry.Tags, filter.Tags) {
				continue
			}
		}

		results = append(results, entry)
	}

	// 按 CreatedAt 降序排列
	sort.Slice(results, func(i, j int) bool {
		return results[i].CreatedAt.After(results[j].CreatedAt)
	})

	// Limit 截断
	if len(results) > filter.Limit {
		results = results[:filter.Limit]
	}

	return results, nil
}

// Get 按 ID 获取单个条目
func (bb *blackboard) Get(entryID string) (BlackboardEntry, bool) {
	bb.mu.RLock()
	defer bb.mu.RUnlock()

	entry, ok := bb.entries[entryID]
	if !ok {
		return BlackboardEntry{}, false
	}
	return entry, true
}

// Update 更新条目的内容（MVCC 乐观锁）
func (bb *blackboard) Update(entryID string, author string, content map[string]interface{}) error {
	if content == nil {
		return fmt.Errorf("content cannot be nil")
	}

	bb.mu.Lock()
	defer bb.mu.Unlock()

	entry, ok := bb.entries[entryID]
	if !ok {
		return fmt.Errorf("entry not found: %s", entryID)
	}

	// 乐观锁验证：条目必须为 committed 状态
	if entry.Status != "committed" {
		return fmt.Errorf("entry %s has status %q, can only update committed entries", entryID, entry.Status)
	}

	// MVCC 版本号递增
	entry.Version++
	entry.Content = content
	entry.UpdatedAt = time.Now()

	bb.entries[entryID] = entry

	// 异步通知订阅者
	go bb.notifySubscribers(entry.Region, EventUpdated, entry)

	return nil
}

// SetStatus 修改条目状态，触发状态变更事件
func (bb *blackboard) SetStatus(entryID string, author string, status string) error {
	// 验证目标状态
	validStatuses := map[string]bool{
		"draft":        true,
		"committed":    true,
		"superseded":   true,
		"closed":       true,
	}
	if !validStatuses[status] {
		return fmt.Errorf("invalid status: %s", status)
	}

	bb.mu.Lock()
	defer bb.mu.Unlock()

	entry, ok := bb.entries[entryID]
	if !ok {
		return fmt.Errorf("entry not found: %s", entryID)
	}

	// 状态转换验证
	if err := bb.validateStatusTransition(entry.Status, status); err != nil {
		return err
	}

	entry.Status = status
	entry.UpdatedAt = time.Now()
	bb.entries[entryID] = entry

	// 异步通知订阅者
	go bb.notifySubscribers(entry.Region, EventStatusChanged, entry)

	return nil
}

// validateStatusTransition 验证状态转换的合法性
func (bb *blackboard) validateStatusTransition(from, to string) error {
	// 定义允许的状态转换图
	validTransitions := map[string]map[string]bool{
		"draft": {
			"committed": true,
			"closed":    true,
		},
		"committed": {
			"superseded": true,
			"closed":     true,
		},
		"superseded": {
			"closed": true,
		},
		// closed 为终态，不可转换
	}

	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("no valid transitions from status %q", from)
	}
	if !allowed[to] {
		return fmt.Errorf("invalid status transition from %q to %q", from, to)
	}
	return nil
}

// subscriberRegistry 订阅者注册表
type subscriberRegistry struct {
	handlers []func(BlackboardEvent)
	ids      map[int]struct{} // 已删除的索引集合，用于避免重复删除
}

// Subscribe 按区域订阅变更事件，返回取消订阅函数
func (bb *blackboard) Subscribe(region BlackboardRegion, handler func(BlackboardEvent)) func() {
	// 验证区域
	valid := false
	for _, r := range AllRegions {
		if r == region {
			valid = true
			break
		}
	}
	if !valid {
		panic(fmt.Sprintf("invalid blackboard region: %s", region))
	}

	if handler == nil {
		panic("handler cannot be nil")
	}

	bb.mu.Lock()
	defer bb.mu.Unlock()

	// 确保区域的订阅者注册表已初始化
	if bb.subscribers[region] == nil {
		bb.subscribers[region] = &subscriberRegistry{
			handlers: make([]func(BlackboardEvent), 0, 1),
			ids:      make(map[int]struct{}),
		}
	}

	reg := bb.subscribers[region]

	// 注册 handler，分配索引
	reg.handlers = append(reg.handlers, handler)
	idx := len(reg.handlers) - 1

	// 返回取消订阅函数
	return func() {
		bb.mu.Lock()
		defer bb.mu.Unlock()

		reg, ok := bb.subscribers[region]
		if !ok {
			return
		}

		// 避免重复删除
		if _, deleted := reg.ids[idx]; deleted {
			return
		}
		reg.ids[idx] = struct{}{}

		// 用 nil 替换 handler（惰性清理）
		reg.handlers[idx] = nil
	}
}

// Snapshot 导出全量快照（深拷贝）
func (bb *blackboard) Snapshot() map[BlackboardRegion][]BlackboardEntry {
	bb.mu.RLock()
	defer bb.mu.RUnlock()

	snapshot := make(map[BlackboardRegion][]BlackboardEntry)

	for region, ids := range bb.regionIndex {
		entries := make([]BlackboardEntry, 0, len(ids))
		for _, id := range ids {
			entry, exists := bb.entries[id]
			if !exists {
				continue
			}
			// 深拷贝
			entries = append(entries, deepCopyEntry(entry))
		}
		snapshot[region] = entries
	}

	return snapshot
}

// notifySubscribers 异步通知指定区域的所有订阅者
func (bb *blackboard) notifySubscribers(region BlackboardRegion, eventType BlackboardEventType, entry BlackboardEntry) {
	bb.mu.RLock()
	reg, ok := bb.subscribers[region]
	bb.mu.RUnlock()

	if !ok || len(reg.handlers) == 0 {
		return
	}

	// 为每个 handler 独立 panic 保护
	for idx, handler := range reg.handlers {
		// 跳过已取消的 handler
		if handler == nil {
			continue
		}

		go func(i int, h func(BlackboardEvent)) {
			defer func() {
				if err := recover(); err != nil {
					// handler panic 不应影响其他 handler 或黑板本身
					_ = err
				}
			}()

			event := BlackboardEvent{
				Type:  eventType,
				Entry: entry,
			}
			h(event)
		}(idx, handler)
	}
}

// ==================== 辅助函数 ====================

// copyStringSlice 复制字符串切片
func copyStringSlice(src []string) []string {
	if src == nil {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

// containsAllTags 检查条目的标签是否包含过滤器中的所有标签
func containsAllTags(entryTags, filterTags []string) bool {
	entryTagSet := make(map[string]bool, len(entryTags))
	for _, tag := range entryTags {
		entryTagSet[tag] = true
	}
	for _, tag := range filterTags {
		if !entryTagSet[tag] {
			return false
		}
	}
	return true
}

// deepCopyEntry 深拷贝条目及其嵌套结构
func deepCopyEntry(entry BlackboardEntry) BlackboardEntry {
	copied := entry

	// 深拷贝 Content map
	if entry.Content != nil {
		copied.Content = make(map[string]interface{}, len(entry.Content))
		for k, v := range entry.Content {
			// 简单值直接复制，map/slice 需要递归深拷贝
			switch val := v.(type) {
			case map[string]interface{}:
				copied.Content[k] = deepCopyMap(val)
			case []interface{}:
				copied.Content[k] = deepCopySlice(val)
			default:
				copied.Content[k] = v
			}
		}
	}

	// 深拷贝 Tags
	if entry.Tags != nil {
		copied.Tags = make([]string, len(entry.Tags))
		copy(copied.Tags, entry.Tags)
	}

	// 深拷贝 References
	if entry.References != nil {
		copied.References = make([]string, len(entry.References))
		copy(copied.References, entry.References)
	}

	return copied
}

// deepCopyMap 深拷贝 map[string]interface{}
func deepCopyMap(src map[string]interface{}) map[string]interface{} {
	if src == nil {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		switch val := v.(type) {
		case map[string]interface{}:
			dst[k] = deepCopyMap(val)
		case []interface{}:
			dst[k] = deepCopySlice(val)
		default:
			dst[k] = v
		}
	}
	return dst
}

// deepCopySlice 深拷贝 []interface{}
func deepCopySlice(src []interface{}) []interface{} {
	if src == nil {
		return nil
	}
	dst := make([]interface{}, len(src))
	for i, v := range src {
		switch val := v.(type) {
		case map[string]interface{}:
			dst[i] = deepCopyMap(val)
		case []interface{}:
			dst[i] = deepCopySlice(val)
		default:
			dst[i] = v
		}
	}
	return dst
}

// ============================================================
// BlackboardAccessAdapter — 桥接 memory.Blackboard 与 BlackboardAccess
// ============================================================

// BlackboardAccessAdapter wraps a Blackboard to satisfy the
// BlackboardAccess interface (string-based signatures) expected
// by ExecutorConfig and tools.BlackboardAccessor.
type BlackboardAccessAdapter struct {
	bb Blackboard
}

// NewBlackboardAccessAdapter creates a new adapter.
func NewBlackboardAccessAdapter(bb Blackboard) *BlackboardAccessAdapter {
	return &BlackboardAccessAdapter{bb: bb}
}

func (a *BlackboardAccessAdapter) Post(region string, author string, content map[string]interface{}, tags []string, references []string) (string, error) {
	return a.bb.Post(BlackboardRegion(region), author, content, tags, references)
}

func (a *BlackboardAccessAdapter) Read(region string, filter map[string]interface{}) ([]map[string]interface{}, error) {
	entries, err := a.bb.Read(BlackboardRegion(region), convertToBlackboardFilter(filter))
	if err != nil {
		return nil, err
	}
	result := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		result = append(result, blackboardEntryToMap(e))
	}
	return result, nil
}

func (a *BlackboardAccessAdapter) Get(entryID string) (map[string]interface{}, bool) {
	entry, ok := a.bb.Get(entryID)
	if !ok {
		return nil, false
	}
	return blackboardEntryToMap(entry), true
}

// convertToBlackboardFilter converts a generic map (from JSON/LLM) to BlackboardFilter.
func convertToBlackboardFilter(m map[string]interface{}) BlackboardFilter {
	f := BlackboardFilter{}
	if m == nil {
		return f
	}
	if v, ok := m["tags"]; ok {
		f.Tags = toStringSlice(v)
	}
	if v, ok := m["author"].(string); ok {
		f.Author = v
	}
	if v, ok := m["status"].(string); ok {
		f.Status = v
	}
	if v, ok := m["limit"]; ok {
		switch n := v.(type) {
		case int:
			f.Limit = n
		case float64:
			f.Limit = int(n)
		}
	}
	return f
}

// blackboardEntryToMap converts a BlackboardEntry to a generic map.
func blackboardEntryToMap(e BlackboardEntry) map[string]interface{} {
	return map[string]interface{}{
		"id":         e.ID,
		"region":     string(e.Region),
		"author":     e.Author,
		"content":    e.Content,
		"tags":       e.Tags,
		"references": e.References,
		"status":     e.Status,
		"version":    e.Version,
		"created_at": e.CreatedAt.Format(time.RFC3339),
		"updated_at": e.UpdatedAt.Format(time.RFC3339),
	}
}

// toStringSlice safely converts interface{} to []string,
// handling both []string and []interface{} (JSON array case).
func toStringSlice(v interface{}) []string {
	if v == nil {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []interface{}:
		result := make([]string, 0, len(s))
		for _, item := range s {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	default:
		return nil
	}
}

// Compile-time interface check
var _ interface {
	Post(string, string, map[string]interface{}, []string, []string) (string, error)
	Read(string, map[string]interface{}) ([]map[string]interface{}, error)
	Get(string) (map[string]interface{}, bool)
} = (*BlackboardAccessAdapter)(nil)
