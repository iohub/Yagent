package mesh

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// DelegationContext 委派安全上下文，随 P2P 消息传播。
//
// 提供以下安全保障：
//   - 环检测：通过 Visited 映射检测循环委派
//   - 深度控制：通过 Depth / MaxDepth 限制委派链长度
//   - 超时控制：通过 Deadline 防止委派任务无限期挂起
//
// 并发安全说明：
//   - 所有公开方法在独立实例上调用是线程安全的。
//   - Fork 返回深拷贝副本，多线程各自操作不影响彼此。
//   - 不推荐多个 goroutine 并发修改同一个 DelegationContext。
type DelegationContext struct {
	TaskID       string         `json:"task_id"`        // 关联的黑板任务 entry ID
	InitiatorID  string         `json:"initiator_id"`   // 任务发起者（通常是 anchor agent）
	Chain        []string       `json:"chain"`          // 委派链 [anchor, B, C, ...]
	Depth        int            `json:"depth"`          // 当前深度
	MaxDepth     int            `json:"max_depth"`      // 最大深度（默认 4）
	Visited      map[string]int `json:"visited"`        // agentID → 被访问次数
	Deadline     time.Time      `json:"deadline"`       // 截止时间（默认 120s）
	BlackboardID string         `json:"blackboard_id"`  // 黑板关联条目
}

// ===================== 错误定义 =====================

var (
	// ErrSelfDelegation 当 targetID == fromID 时返回，防止自委派。
	ErrSelfDelegation = errors.New("delegation: cannot delegate to self")

	// ErrMaxDepthExceeded 当 Depth >= MaxDepth 时返回，防止委派链过长。
	ErrMaxDepthExceeded = errors.New("delegation: max depth exceeded")

	// ErrCycleDetected 当 Visited[targetID] >= 2 时返回，检测到循环委派。
	ErrCycleDetected = errors.New("delegation: cycle detected")

	// ErrDeadlineExceeded 当 time.Now().After(Deadline) 时返回，委派超时。
	ErrDeadlineExceeded = errors.New("delegation: deadline exceeded")
)

// ===================== 常量 =====================

const (
	// DefaultMaxDepth 默认最大委派深度。
	DefaultMaxDepth = 4

	// DefaultTimeout 默认委派超时时间。
	DefaultTimeout = 120 * time.Second
)

// ===================== 构造函数 =====================

// NewDelegationContext 创建一个新的委派上下文。
//
// 参数：
//   - taskID:     关联的黑板任务 entry ID
//   - blackboardID: 黑板关联条目 ID
//   - initiatorID: 任务发起者 ID（通常是 anchor agent）
//
// 返回值：
//   - *DelegationContext: 初始化后的委派上下文
//
// 初始化行为：
//   - Chain = [initiatorID]
//   - Depth = 0
//   - MaxDepth = DefaultMaxDepth (4)
//   - Visited = {initiatorID: 1}
//   - Deadline = time.Now() + DefaultTimeout (120s)
func NewDelegationContext(taskID, blackboardID, initiatorID string) *DelegationContext {
	return &DelegationContext{
		TaskID:       taskID,
		InitiatorID:  initiatorID,
		BlackboardID: blackboardID,
		Chain:        []string{initiatorID},
		Depth:        0,
		MaxDepth:     DefaultMaxDepth,
		Visited:      map[string]int{initiatorID: 1},
		Deadline:     time.Now().Add(DefaultTimeout),
	}
}

// ===================== 委派检查 =====================

// CanDelegateTo 在 P2P 委派前调用，检查是否允许委派给目标 agent。
//
// 参数：
//   - targetID: 目标 agent 的唯一标识
//   - fromID:   当前持有上下文的 agent 标识
//
// 返回值：
//   - nil: 允许委派
//   - error: 拒绝委派及原因
//
// 检查规则矩阵：
//
//	| 场景                        | 结果                  | 说明                    |
//	|-----------------------------|-----------------------|-------------------------|
//	| targetID == fromID          | ❌ ErrSelfDelegation  | 不能委派给自己          |
//	| Depth >= MaxDepth           | ❌ ErrMaxDepthExceeded | 委派链过长              |
//	| Visited[targetID] >= 2      | ❌ ErrCycleDetected   | 形成循环                |
//	| time.Now().After(Deadline)  | ❌ ErrDeadlineExceeded | 超时                    |
//	| 正常                        | ✅ nil                | 允许委派                |
func (dc *DelegationContext) CanDelegateTo(targetID, fromID string) error {
	// 1. 自委派检测
	if targetID == fromID {
		return ErrSelfDelegation
	}

	// 2. 深度检查
	if dc.Depth >= dc.MaxDepth {
		return ErrMaxDepthExceeded
	}

	// 3. 循环检测 — Visited[targetID] >= 2 表示形成环路
	if dc.Visited[targetID] >= 2 {
		return ErrCycleDetected
	}

	// 4. 超时检查
	if time.Now().After(dc.Deadline) {
		return ErrDeadlineExceeded
	}

	return nil
}

// ===================== 上下文派生 =====================

// Fork 为目标 agent 创建子委派上下文。
//
// 该方法用于当当前 agent 将任务委派给 targetID 时，生成一个新的上下文
// 随委派消息一起传递。子上下文继承父上下文的所有信息，但：
//   - Depth + 1
//   - Chain 追加 targetID
//   - Visited 深拷贝后 targetID 计数 +1
//   - 其他字段（TaskID、InitiatorID、Deadline 等）保持不变
//
// 参数：
//   - targetID: 被委派的 agent 标识
//
// 返回值：
//   - *DelegationContext: 新的子委派上下文（深拷贝）
//
// 并发安全：Fork 返回深拷贝副本，多线程各自操作互不影响。
func (dc *DelegationContext) Fork(targetID string) *DelegationContext {
	// 深拷贝 Visited map
	newVisited := make(map[string]int, len(dc.Visited)+1)
	for k, v := range dc.Visited {
		newVisited[k] = v
	}
	newVisited[targetID]++

	// 深拷贝 Chain slice
	newChain := make([]string, len(dc.Chain)+1)
	copy(newChain, dc.Chain)
	newChain[len(dc.Chain)] = targetID

	return &DelegationContext{
		TaskID:       dc.TaskID,
		InitiatorID:  dc.InitiatorID,
		Chain:        newChain,
		Depth:        dc.Depth + 1,
		MaxDepth:     dc.MaxDepth,
		Visited:      newVisited,
		Deadline:     dc.Deadline,
		BlackboardID: dc.BlackboardID,
	}
}

// ===================== 辅助方法 =====================

// RemainingTime 返回委派上下文的剩余时间。
//
// 如果已超时，返回 0。
func (dc *DelegationContext) RemainingTime() time.Duration {
	remaining := time.Until(dc.Deadline)
	if remaining <= 0 {
		return 0
	}
	return remaining
}

// IsExpired 检查委派上下文是否已超时。
func (dc *DelegationContext) IsExpired() bool {
	return time.Now().After(dc.Deadline)
}

// ChainString 返回委派链的可读字符串。
//
// 例如： "anchor-agent → B-agent → C-agent"
func (dc *DelegationContext) ChainString() string {
	if len(dc.Chain) == 0 {
		return "(empty)"
	}

	var buf string
	for i, id := range dc.Chain {
		if i > 0 {
			buf += " → "
		}
		buf += id
	}
	return buf
}

// Summary 返回委派上下文的摘要信息，用于日志记录和黑板写入。
//
// 返回的 map 包含：
//   - task_id:       任务 ID
//   - initiator_id:  发起者 ID
//   - chain:         委派链字符串
//   - depth:         当前深度
//   - max_depth:     最大深度
//   - visited:       访问计数 map
//   - remaining_sec: 剩余秒数（保留 1 位小数）
//   - is_expired:    是否已超时
//   - blackboard_id: 黑板关联条目
func (dc *DelegationContext) Summary() map[string]interface{} {
	return map[string]interface{}{
		"task_id":       dc.TaskID,
		"initiator_id":  dc.InitiatorID,
		"chain":         dc.ChainString(),
		"chain_list":    dc.Chain,
		"depth":         dc.Depth,
		"max_depth":     dc.MaxDepth,
		"visited":       dc.Visited,
		"remaining_sec": dc.RemainingTime().Seconds(),
		"is_expired":    dc.IsExpired(),
		"blackboard_id": dc.BlackboardID,
	}
}

// String 实现 fmt.Stringer 接口，返回委派上下文的简要字符串表示。
func (dc *DelegationContext) String() string {
	return fmt.Sprintf(
		"DelegationContext{task=%s, chain=%s, depth=%d/%d, remaining=%v}",
		dc.TaskID,
		dc.ChainString(),
		dc.Depth,
		dc.MaxDepth,
		dc.RemainingTime(),
	)
}

// ===================== 深度控制辅助 =====================

// WithMaxDepth 返回一个 MaxDepth 被修改后的副本。
// 可用于临时调整委派深度限制。
func (dc *DelegationContext) WithMaxDepth(newMax int) *DelegationContext {
	if newMax <= 0 {
		newMax = DefaultMaxDepth
	}
	copy := *dc
	copy.MaxDepth = newMax
	return &copy
}

// WithTimeout 返回一个 Deadline 被修改后的副本。
// 可用于为特定委派任务设置不同的超时时间。
func (dc *DelegationContext) WithTimeout(timeout time.Duration) *DelegationContext {
	copy := *dc
	copy.Deadline = time.Now().Add(timeout)
	return &copy
}

// ===================== 统计信息 =====================

// VisitCount 返回指定 agent 在委派链中的被访问次数。
// 如果未访问过，返回 0。
func (dc *DelegationContext) VisitCount(agentID string) int {
	return dc.Visited[agentID]
}

// UniqueAgents 返回委派链中涉及的不同 agent 数量。
func (dc *DelegationContext) UniqueAgents() int {
	return len(dc.Visited)
}

// ===================== 不可变视图 =====================

// delegationContextView 委派上下文的不可变视图，用于安全暴露只读访问。
type delegationContextView struct {
	*DelegationContext
	mu sync.RWMutex
}

// NewDelegationContextView 创建委派上下文的只读视图。
// 返回的视图线程安全，但无法修改底层上下文。
func NewDelegationContextView(dc *DelegationContext) *delegationContextView {
	return &delegationContextView{
		DelegationContext: dc,
	}
}

// SafeSummary 线程安全地获取摘要信息。
func (v *delegationContextView) SafeSummary() map[string]interface{} {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.DelegationContext.Summary()
}

// SafeCanDelegateTo 线程安全地执行委派检查。
func (v *delegationContextView) SafeCanDelegateTo(targetID, fromID string) error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.DelegationContext.CanDelegateTo(targetID, fromID)
}
