package compact

import (
	"sync"
)

// CompressionState 持久化压缩进度状态
// 用于增量压缩：追踪上次压缩位置、摘要栈、约束块等
type CompressionState struct {
	// LastCompressedIndex 上次压缩处理到的消息索引（消息列表长度）
	// 下次压缩时，从这个索引开始处理新增消息
	LastCompressedIndex int

	// SummaryStack 摘要栈，从旧到新排列（栈底最旧，栈顶最新）
	SummaryStack []SummaryBlock

	// ConstraintsBlock 提取的长期约束文本块
	// 这些是用户在对话中明确表达的关键约束，永不参与压缩
	ConstraintsBlock string

	// mu 保护并发读写
	mu sync.RWMutex
}

// SummaryBlock 摘要块，记录一次压缩生成的摘要信息
type SummaryBlock struct {
	// StartIndex 该摘要覆盖的消息起始索引
	StartIndex int

	// EndIndex 该摘要覆盖的消息结束索引
	EndIndex int

	// Summary 摘要内容文本（已包含 [CONTEXT SUMMARY] 前缀）
	Summary string

	// TokenCount 摘要文本的 token 数
	TokenCount int

	// CompressionLevel 压缩层级
	// 1 = 首次直接摘要
	// 2 = 多层摘要合并后的整合摘要
	CompressionLevel int
}

// Lock 提供互斥锁保护
func (cs *CompressionState) Lock() {
	cs.mu.Lock()
}

// Unlock 提供互斥锁保护
func (cs *CompressionState) Unlock() {
	cs.mu.Unlock()
}

// RLock 提供读互斥锁保护
func (cs *CompressionState) RLock() {
	cs.mu.RLock()
}

// RUnlock 提供读互斥锁释放
func (cs *CompressionState) RUnlock() {
	cs.mu.RUnlock()
}

// IsEmpty 检查状态是否为空（首次压缩）
func (cs *CompressionState) IsEmpty() bool {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.LastCompressedIndex == 0 && len(cs.SummaryStack) == 0
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

// DeepCopy 深拷贝状态（用于异步快照）
func (cs *CompressionState) DeepCopy() *CompressionState {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	newStack := make([]SummaryBlock, len(cs.SummaryStack))
	copy(newStack, cs.SummaryStack)

	return &CompressionState{
		LastCompressedIndex: cs.LastCompressedIndex,
		SummaryStack:        newStack,
		ConstraintsBlock:    cs.ConstraintsBlock,
	}
}

// AppendSummary 追加新的摘要块到栈顶
func (cs *CompressionState) AppendSummary(block SummaryBlock) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.SummaryStack = append(cs.SummaryStack, block)
}
