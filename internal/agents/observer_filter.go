package agents

import (
	"strings"
)

// ObserverFilter Observer 事件过滤器
// 防止 P2P 通信事件注入 Conductor LLM context
// 可控制是否启用、过滤哪些事件类型
type ObserverFilter struct {
	// filteredTypes 需要过滤的精确事件类型
	filteredTypes map[string]bool

	// filteredPrefixes 需要过滤的事件类型前缀
	filteredPrefixes []string

	// allowedTypes 例外：即使匹配前缀也放行的事件类型
	allowedTypes map[string]bool

	// enabled 是否启用过滤
	enabled bool
}

// NewObserverFilter 创建 Observer 过滤器
// 默认不过滤任何事件（enabled=false）
func NewObserverFilter() *ObserverFilter {
	return &ObserverFilter{
		filteredTypes: map[string]bool{
			// P2P 通信事件
			"p2p.query":             true,
			"p2p.notify":            true,
			"p2p.delegate":          true,
			"p2p.delegate.response": true,
			"p2p_event":             true, // 现有 Conductor 使用的 event type

			// Capability 搜索事件
			"capability.search":      true,
			"capability.search.result": true,

			// Blackboard 事件（中间协作，非最终产物）
			"blackboard.post": true,
			"blackboard.read": true,

			// Mesh 管理事件
			"mesh.register":      true,
			"mesh.unregister":    true,
			"mesh.status_update": true,
			"mesh.cleanup":       true,

			// Result compression 事件
			"result.compressed": true,
		},
		filteredPrefixes: []string{
			"p2p.",
			"mesh.",
			"blackboard.",
			"capability.",
		},
		allowedTypes: map[string]bool{
			// Agent 注册/完成事件需要放行（Conductor 需要知道哪些 Agent 可用）
			"mesh.register":      false, // 在 filteredTypes 中覆盖为 false
			"mesh.status_update": false,
		},
		enabled: false, // 默认关闭
	}
}

// SetEnabled 设置是否启用过滤
func (f *ObserverFilter) SetEnabled(enabled bool) {
	f.enabled = enabled
}

// IsEnabled 返回当前过滤状态
func (f *ObserverFilter) IsEnabled() bool {
	return f.enabled
}

// ShouldFilter 判断事件是否应该被过滤（不注入 Conductor context）
// eventType: 事件类型字符串（如 "p2p.delegate", "task.completed"）
// 返回 true 表示该事件应被过滤
func (f *ObserverFilter) ShouldFilter(eventType string) bool {
	if !f.enabled {
		return false // 未启用时不过滤任何事件
	}

	// 1. 精确匹配（优先级最高）
	if shouldFilter, exists := f.filteredTypes[eventType]; exists {
		return shouldFilter
	}

	// 2. 前缀匹配
	for _, prefix := range f.filteredPrefixes {
		if strings.HasPrefix(eventType, prefix) {
			return true
		}
	}

	return false
}

// AddFilteredType 添加需要过滤的事件类型
func (f *ObserverFilter) AddFilteredType(eventType string) {
	f.filteredTypes[eventType] = true
}

// RemoveFilteredType 移除事件类型的过滤（即放行）
func (f *ObserverFilter) RemoveFilteredType(eventType string) {
	f.filteredTypes[eventType] = false
}

// AddFilteredPrefix 添加需要过滤的事件类型前缀
func (f *ObserverFilter) AddFilteredPrefix(prefix string) {
	for _, p := range f.filteredPrefixes {
		if p == prefix {
			return // 已存在
		}
	}
	f.filteredPrefixes = append(f.filteredPrefixes, prefix)
}

// GetFilteredTypes 返回当前被过滤的事件类型列表（用于调试/日志）
func (f *ObserverFilter) GetFilteredTypes() []string {
	types := make([]string, 0, len(f.filteredTypes))
	for t, shouldFilter := range f.filteredTypes {
		if shouldFilter {
			types = append(types, t)
		}
	}
	return types
}

// FilterBusEventTopic 判断 bus.Event 的 Topic 是否应被过滤
// 这是专为 Conductor 的 Observer 回调设计的便捷方法
// 接收 bus.Event 的 Topic 字段，返回是否应跳过该事件
func (f *ObserverFilter) FilterBusEventTopic(topic string) bool {
	return f.ShouldFilter(topic)
}
