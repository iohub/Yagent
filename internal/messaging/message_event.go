package messaging

import (
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// 类型定义
// ---------------------------------------------------------------------------

// Priority 消息优先级
type Priority int

const (
	PriorityLow      Priority = 0 // 低优先级，可延迟处理
	PriorityNormal   Priority = 1 // 正常优先级（默认）
	PriorityHigh     Priority = 2 // 高优先级，优先处理
	PriorityCritical Priority = 3 // 紧急优先级，立即处理
)

// String 返回优先级的字符串表示
func (p Priority) String() string {
	switch p {
	case PriorityLow:
		return "low"
	case PriorityNormal:
		return "normal"
	case PriorityHigh:
		return "high"
	case PriorityCritical:
		return "critical"
	default:
		return fmt.Sprintf("priority(%d)", p)
	}
}

// EventType 事件类型
type EventType string

// 预定义事件类型
const (
	EventTypesSystem  EventType = "system"
	EventTypeInfo     EventType = "info"
	EventTypeError    EventType = "error"
	EventUserConfirm  EventType = "user_confirm"
	EventConversation EventType = "conversation"
	EventTaskProgress EventType = "task_progress"
	EventToolCall     EventType = "tool_call"
)

// ---------------------------------------------------------------------------
// 事件结构体
// ---------------------------------------------------------------------------

// Event 可靠消息传递的事件结构
//
// 设计说明：
//   - ID: 由 IDGen 生成的全局唯一标识
//   - Source/Target: 发布-订阅模型中的发送方和接收方（空 Target 表示广播）
//   - Priority: 消息处理优先级，影响消费顺序
//   - RetryCount/MaxRetries: 重试机制，支持消息投递失败后的自动重试
//   - Deadline: 消息有效期，过期后自动丢弃（用于超时控制）
//   - TraceID: 分布式链路追踪 ID，用于跨服务追踪
//   - SeqNum: 由 WAL（Write-Ahead Log）分配的单调递增序列号，保证持久化顺序
type Event struct {
	// 基础字段
	ID        string                 `json:"id"`           // 全局唯一 ID（UUID/雪花算法格式）
	Type      EventType              `json:"type"`         // 事件类型
	Source    string                 `json:"source"`       // 发送者标识
	Target    string                 `json:"target"`       // 接收者标识（空=广播）
	Content   interface{}            `json:"content"`      // 消息内容
	Priority  Priority               `json:"priority"`     // 优先级
	Timestamp time.Time              `json:"timestamp"`    // 创建时间

	// 可靠传递相关
	RetryCount int                  `json:"retry_count"`  // 已重试次数
	MaxRetries int                  `json:"max_retries"`  // 最大重试次数（默认3）
	Deadline   *time.Time           `json:"deadline,omitempty"` // 超时时间（nil=永不过期）

	// 追踪与排序
	TraceID string                 `json:"trace_id"`     // 链路追踪 ID
	SeqNum  uint64                 `json:"seq_num"`      // 序列号（由 WAL 分配）

	// 扩展元数据
	Metadata map[string]interface{} `json:"metadata,omitempty"` // 自定义元数据

	// Deprecated: 使用 Source 替代
	From string `json:"from,omitempty"` // 旧版发送者字段，保留用于向后兼容
}

// MessageEvent 是 Event 的向后兼容类型别名
//
// 所有现有代码使用 MessageEvent 的地方可以无缝迁移，
// 新代码应直接使用 Event。
type MessageEvent = Event

// ---------------------------------------------------------------------------
// 构造函数
// ---------------------------------------------------------------------------

// NewEvent 创建一个完整的 Event 实例
//
// 参数:
//   - typ: 事件类型
//   - source: 发送者标识
//   - payload: 消息内容
//
// 返回值:
//   - *Event: 初始化的事件指针
func NewEvent(typ EventType, source string, payload interface{}) *Event {
	return &Event{
		Type:       typ,
		Source:     source,
		Content:    payload,
		Priority:   PriorityNormal, // 默认优先级
		MaxRetries: 3,              // 默认最大重试 3 次
		Timestamp:  time.Now(),
		Metadata:   make(map[string]interface{}),
	}
}

// NewMessageEvent 创建 Event（向后兼容包装函数）
//
// 内部调用 NewEvent 实现，保留旧 API 签名。
// 新代码请优先使用 NewEvent。
//
// 参数:
//   - eventType: 事件类型（字符串）
//   - content: 消息内容
//   - from: 发送者标识
//
// 返回值:
//   - *Event: 初始化的事件指针（返回类型升级为 *Event）
func NewMessageEvent(eventType string, content interface{}, from string) *Event {
	return NewEvent(EventType(eventType), from, content)
}

// ---------------------------------------------------------------------------
// 方法
// ---------------------------------------------------------------------------

// Validate 验证 Event 的合法性
//
// 检查项:
//   1. ID 不能为空
//   2. Type 不能为空
//   3. Source 不能为空
//   4. 如果设置了 Deadline，必须晚于 Timestamp
//   5. MaxRetries 必须 >= 0
//   6. RetryCount 不能超过 MaxRetries
//
// 返回值:
//   - error: 如果验证失败，返回具体的错误信息
func (e *Event) Validate() error {
	if e.ID == "" {
		return errors.New("event: ID is required")
	}
	if e.Type == "" {
		return errors.New("event: Type is required")
	}
	if e.Source == "" && e.From == "" {
		return errors.New("event: Source is required (Source or deprecated From)")
	}
	if e.Deadline != nil && e.Timestamp.After(*e.Deadline) {
		return errors.New("event: deadline must be after timestamp")
	}
	if e.MaxRetries < 0 {
		return errors.New("event: max_retries must be >= 0")
	}
	if e.RetryCount > e.MaxRetries {
		return fmt.Errorf("event: retry_count (%d) exceeds max_retries (%d)", e.RetryCount, e.MaxRetries)
	}
	return nil
}

// IsExpired 检查消息是否已过期
func (e *Event) IsExpired() bool {
	if e.Deadline == nil {
		return false
	}
	return time.Now().After(*e.Deadline)
}

// ShouldRetry 检查是否应该重试
func (e *Event) ShouldRetry() bool {
	return e.RetryCount < e.MaxRetries && !e.IsExpired()
}

// Clone 创建 Event 的深度副本
func (e *Event) Clone() *Event {
	copy := &Event{
		ID:         e.ID,
		Type:       e.Type,
		Source:     e.Source,
		Target:     e.Target,
		Content:    e.Content,
		Priority:   e.Priority,
		Timestamp:  e.Timestamp,
		RetryCount: e.RetryCount,
		MaxRetries: e.MaxRetries,
		TraceID:    e.TraceID,
		SeqNum:     e.SeqNum,
		Metadata:   make(map[string]interface{}),
	}
	if e.Deadline != nil {
		d := *e.Deadline
		copy.Deadline = &d
	}
	// 浅拷贝 Metadata
	for k, v := range e.Metadata {
		copy.Metadata[k] = v
	}
	return copy
}