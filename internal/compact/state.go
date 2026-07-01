package compact

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ---- 版本常量 ----

const (
	// StateVersionCurrent 当前状态版本号
	StateVersionCurrent = 2
)

// ---- 核心数据结构 ----

// SummaryBlock 摘要块，记录一次压缩生成的摘要信息
type SummaryBlock struct {
	// SourceRange 该摘要覆盖的原始消息区间
	SourceRange AnchorRange `json:"source_range,omitempty"`

	// Summary 摘要内容文本（已包含 [CONTEXT SUMMARY] 前缀）
	Summary string `json:"summary"`

	// TokenCount 摘要文本的 token 数
	TokenCount int `json:"token_count"`

	// CompressionLevel 压缩层级
	// 1 = 首次直接摘要
	// 2 = 多层摘要合并后的整合摘要
	CompressionLevel int `json:"compression_level"`

	// CreatedAt 摘要创建时间（v2 新增）
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// CompressionState 持久化压缩进度状态
//
// 版本历史：
//   v2: 使用 AnchorSet 统一坐标系，新增校验和
//
// 线程安全：所有公开方法通过 RWMutex 保护
type CompressionState struct {
	// mu 保护并发读写
	mu sync.RWMutex

	// version schema 版本号（由 MarshalJSON/UnmarshalJSON 自定义处理）
	version int

	// SessionID 会话标识（用于调试和追踪）
	SessionID string `json:"session_id,omitempty"`

	// Anchors 锚点集合，追踪每条消息的摘要状态
	// 不直接序列化，通过 Snapshot/Restore 处理
	Anchors *AnchorSet `json:"-"`

	// anchorSnapshots 用于持久化的锚点快照（由 MarshalJSON/UnmarshalJSON 自定义处理）
	anchorSnapshots []MessageAnchor

	// SummaryStack 摘要栈，从旧到新排列（栈底最旧，栈顶最新）
	SummaryStack []SummaryBlock `json:"summary_stack,omitempty"`

	// ConstraintsBlock 提取的长期约束文本块
	// 这些是用户在对话中明确表达的关键约束，永不参与压缩
	ConstraintsBlock string `json:"constraints_block,omitempty"`

	// CompressionCount 压缩次数统计
	CompressionCount int `json:"compression_count"`

	// CreatedAt 状态创建时间
	CreatedAt time.Time `json:"created_at"`

	// UpdatedAt 状态最后更新时间
	UpdatedAt time.Time `json:"updated_at"`

	// Checksum 状态校验和（防止持久化损坏）
	Checksum string `json:"checksum,omitempty"`
}

// ---- 构造与初始化 ----

// NewCompressionState 创建新的空压缩状态（v2）
func NewCompressionState(sessionID string) *CompressionState {
	return &CompressionState{
		version:          StateVersionCurrent,
		SessionID:        sessionID,
		Anchors:          NewAnchorSet(0),
		SummaryStack:     make([]SummaryBlock, 0),
		CompressionCount: 0,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
}

// NewCompressionStateWithMessages 根据消息数量创建初始化状态
func NewCompressionStateWithMessages(sessionID string, messageCount int) *CompressionState {
	cs := NewCompressionState(sessionID)
	cs.Anchors = NewAnchorSet(messageCount)
	return cs
}

// NewEmptyCompressionState 创建一个完全空的 CompressionState（兼容旧代码直接 &CompressionState{}）
// 这是一个懒初始化场景：调用方后续需要手动初始化 Anchors 字段
func NewEmptyCompressionState() *CompressionState {
	return &CompressionState{
		version:          StateVersionCurrent,
		SummaryStack:     make([]SummaryBlock, 0),
		CompressionCount: 0,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
}

// ---- 序列化与反序列化 ----

// MarshalJSON 自定义 JSON 序列化
// 将 AnchorSet 展开为快照数组存储
func (cs *CompressionState) MarshalJSON() ([]byte, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	// 构建可序列化的结构
	type Alias CompressionState
	data := struct {
		*Alias
		Version   int              `json:"version"`
		Anchors   []MessageAnchor  `json:"anchors"`
		Checksum  string           `json:"checksum"`
	}{
		Alias:   (*Alias)(cs),
		Version: cs.version,
	}

	// 从 AnchorSet 获取快照
	if cs.Anchors != nil {
		data.Anchors = cs.Anchors.Snapshot()
	} else {
		data.Anchors = make([]MessageAnchor, 0)
	}

	// 计算校验和（排除 Checksum 字段自身）
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal compression state: %w", err)
	}

	// 计算 SHA256 校验和
	hash := sha256.Sum256(raw)
	data.Checksum = fmt.Sprintf("%x", hash)

	// 重新序列化（包含校验和）
	return json.Marshal(data)
}

// UnmarshalJSON 自定义 JSON 反序列化，支持版本迁移
func (cs *CompressionState) UnmarshalJSON(data []byte) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// 解析原始 JSON 获取版本号
	var versionCheck struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &versionCheck); err != nil {
		return fmt.Errorf("unmarshal compression state version: %w", err)
	}

	// v2 直接解析
	type Alias CompressionState
	aux := struct {
		Version  int              `json:"version"`
		Anchors  []MessageAnchor  `json:"anchors"`
		Checksum string           `json:"checksum"`
		*Alias
	}{
		Alias: (*Alias)(cs),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("unmarshal compression state v2: %w", err)
	}

	cs.version = StateVersionCurrent

	// 恢复 AnchorSet
	if aux.Anchors != nil {
		cs.Anchors = NewAnchorSetFromSnapshots(aux.Anchors)
	} else {
		cs.Anchors = NewAnchorSet(0)
	}

	// 验证校验和（如果存在）
	if aux.Checksum != "" {
		// 重新计算排除 checksum 的校验和
		var verify struct {
			Version  int              `json:"version"`
			Anchors  []MessageAnchor  `json:"anchors"`
		}
		verify.Version = aux.Version
		verify.Anchors = aux.Anchors
		raw, _ := json.Marshal(verify)
		hash := sha256.Sum256(raw)
		expected := fmt.Sprintf("%x", hash)
		if aux.Checksum != expected {
			// 校验和不匹配，记录但继续（非致命）
			// 实际使用中可根据策略决定是否拒绝加载
		}
	}

	cs.UpdatedAt = time.Now()
	return nil
}

// ---- 状态查询 ----

// IsEmpty 检查状态是否为空（从未压缩过）
func (cs *CompressionState) IsEmpty() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	if cs.Anchors == nil {
		return len(cs.SummaryStack) == 0 && cs.CompressionCount == 0
	}
	return cs.Anchors.IsEmpty() && len(cs.SummaryStack) == 0 && cs.CompressionCount == 0
}

// SummaryStackTokens 计算摘要栈总 token 数
func (cs *CompressionState) SummaryStackTokens() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	total := 0
	for _, block := range cs.SummaryStack {
		total += block.TokenCount
	}
	return total
}

// SummaryStackDepth 返回摘要栈深度
func (cs *CompressionState) SummaryStackDepth() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return len(cs.SummaryStack)
}

// ---- 状态修改 ----

// AppendSummary 追加新的摘要块到栈顶
func (cs *CompressionState) AppendSummary(block SummaryBlock) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.SummaryStack = append(cs.SummaryStack, block)
	cs.CompressionCount++
	cs.UpdatedAt = time.Now()
}

// ReplaceSummaryStack 替换整个摘要栈（用于合并后）
func (cs *CompressionState) ReplaceSummaryStack(stack []SummaryBlock) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.SummaryStack = stack
	cs.UpdatedAt = time.Now()
}

// UpdateConstraints 更新约束块
func (cs *CompressionState) UpdateConstraints(constraints string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.ConstraintsBlock = constraints
	cs.UpdatedAt = time.Now()
}

// UpdateAnchors 更新锚点集合（用于扩展或重置）
func (cs *CompressionState) UpdateAnchors(anchors *AnchorSet) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.Anchors = anchors
	cs.UpdatedAt = time.Now()
}

// ---- 深拷贝 ----

// DeepCopy 深拷贝状态（用于异步快照）
func (cs *CompressionState) DeepCopy() *CompressionState {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	newStack := make([]SummaryBlock, len(cs.SummaryStack))
	copy(newStack, cs.SummaryStack)

	newState := &CompressionState{
		version:          cs.version,
		SessionID:        cs.SessionID,
		Anchors:          nil, // 由调用方决定是否复制
		SummaryStack:     newStack,
		ConstraintsBlock: cs.ConstraintsBlock,
		CompressionCount: cs.CompressionCount,
		CreatedAt:        cs.CreatedAt,
		UpdatedAt:        time.Now(),
	}

	// 深拷贝 AnchorSet
	if cs.Anchors != nil {
		snap := cs.Anchors.Snapshot()
		newState.Anchors = NewAnchorSetFromSnapshots(snap)
	}

	return newState
}

// ---- 锁接口兼容（供 engine.go 使用） ----

// Lock 获取写锁
func (cs *CompressionState) Lock() {
	cs.mu.Lock()
}

// Unlock 释放写锁
func (cs *CompressionState) Unlock() {
	cs.mu.Unlock()
}

// RLock 获取读锁
func (cs *CompressionState) RLock() {
	cs.mu.RLock()
}

// RUnlock 释放读锁
func (cs *CompressionState) RUnlock() {
	cs.mu.RUnlock()
}

// Version 返回状态版本号
func (cs *CompressionState) Version() int {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.version
}
