package messaging

import (
	"sync/atomic"
	"time"
)

// publisherCore 是"一次任务"共享的那一份：dispatcher 会在每个新任务被换一次
// （IntegrateMessaging），而 Publish 从各条 goroutine 上读它。
// 用 atomic.Pointer 而不是普通字段：换的是指针，读的永远是完整值。
type publisherCore struct {
	dispatcher atomic.Pointer[MessageDispatcher]
}

// MessagePublisher 消息发布者。
//
// 委派坐标不是逐个调用点传的，而是 ForDelegation 绑定一次、整条循环都带着：
// 并行委派下 Source（Agent 名）不再唯一，两个 Coding 同时输出会把两股流粘成一段文字，
// 而"忘了在一处传 delegation_id"的表现正是那种没人能解释的"界面少了字"。
// 绑定的视图与母体**共享同一个 core** —— 否则任务换了 dispatcher，
// 已经绑定的那份还发往旧的 dispatcher。
type MessagePublisher struct {
	core         *publisherCore
	delegationID string
}

// NewMessagePublisher 创建新的消息发布者
func NewMessagePublisher(dispatcher *MessageDispatcher) *MessagePublisher {
	core := &publisherCore{}
	if dispatcher != nil {
		core.dispatcher.Store(dispatcher)
	}
	return &MessagePublisher{core: core}
}

// SetDispatcher 更新内部 dispatcher 引用，用于在 Publisher 创建后切换 dispatcher。
// 所有 ForDelegation 出来的视图一起生效（共享 core）。
func (p *MessagePublisher) SetDispatcher(dispatcher *MessageDispatcher) {
	if p == nil || p.core == nil {
		return
	}
	p.core.dispatcher.Store(dispatcher)
}

// ForDelegation 返回一个绑定到某一次委派的发布者视图；delegationID 为空时返回自己。
func (p *MessagePublisher) ForDelegation(delegationID string) *MessagePublisher {
	if p == nil || delegationID == "" {
		return p
	}
	return &MessagePublisher{core: p.core, delegationID: delegationID}
}

// Publish 发布消息（新接口，返回 error）
func (p *MessagePublisher) Publish(eventType string, content interface{}, from string) error {
	return p.publish(eventType, content, from, nil)
}

// PublishWithMetadata 发布带元数据的消息
func (p *MessagePublisher) PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) error {
	return p.publish(eventType, content, from, metadata)
}

// publish 是所有对外的发布口的唯一落点：序号之外的两个坐标（delegation）在这里一起盖上，
// 所以新增一种事件类型不可能"忘记带委派坐标"。
func (p *MessagePublisher) publish(eventType string, content interface{}, from string, metadata map[string]interface{}) error {
	if p == nil || p.core == nil {
		return nil
	}
	d := p.core.dispatcher.Load()
	if d == nil {
		return nil
	}
	return d.Publish(&Event{
		Type:         EventType(eventType),
		Source:       from,
		Content:      content,
		Timestamp:    time.Now(),
		Metadata:     metadata,
		DelegationID: p.delegationID,
	})
}
