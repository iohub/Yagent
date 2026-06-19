package compact

import (
	"fmt"
	"sync"
)

// anchor.go — 统一消息位置坐标系
//
// 问题背景：
// 原有系统使用 CompressionState.LastCompressedIndex（单个整数）追踪压缩进度，
// 但紧急模式（sync compress）可能产生"压缩空洞"——某些中间消息被摘要而两头保留。
// 单个整数无法表达这种不连续的已摘要状态。
//
// 解决方案：
// AnchorSet 为每条消息维护一个锚点，标记其是否已被摘要。
// 所有压缩操作（增量/全量/紧急）都通过 AnchorSet 查询"下一个未摘要区间"，
// 从而统一坐标系，消除索引漂移。

// AnchorRange 标识一段连续的消息区间
type AnchorRange struct {
	StartIndex int `json:"start_index"` // 起始索引（包含）
	EndIndex   int `json:"end_index"`   // 结束索引（不包含）
}

// String 实现 fmt.Stringer
func (r AnchorRange) String() string {
	return fmt.Sprintf("[%d:%d]", r.StartIndex, r.EndIndex)
}

// IsValid 检查区间是否有效
func (r AnchorRange) IsValid() bool {
	return r.StartIndex >= 0 && r.EndIndex >= r.StartIndex
}

// Len 返回区间长度
func (r AnchorRange) Len() int {
	if r.EndIndex <= r.StartIndex {
		return 0
	}
	return r.EndIndex - r.StartIndex
}

// MessageAnchor 标识单条消息在原始消息序列中的位置和压缩状态
type MessageAnchor struct {
	OriginalIndex int  `json:"original_index"` // 在原始 []Message 中的索引
	IsSummarized  bool `json:"is_summarized"`  // 是否已被摘要
	SummaryRef    int  `json:"summary_ref"`    // 指向 SummaryStack 中的层号（-1 表示未摘要）
}

// AnchorSet 维护完整的锚点集合
//
// 功能：
// 1. 标记一段消息区间为"已摘要"（MarkSummarized）
// 2. 查询下一个未摘要的消息区间（NextUnsummarizedRange）
// 3. 计算未摘要消息的总 token 数（UnsummarizedTokenCount）
// 4. 获取所有未摘要的区间列表（UnsummarizedRanges）
// 5. 一致性快照（用于持久化）
//
// 线程安全：所有公开方法都通过 RWMutex 保护
type AnchorSet struct {
	mu      sync.RWMutex
	anchors []MessageAnchor
}

// NewAnchorSet 从消息数量创建初始锚点集合
// 初始状态：所有消息均标记为未摘要
func NewAnchorSet(messageCount int) *AnchorSet {
	if messageCount < 0 {
		messageCount = 0
	}
	anchors := make([]MessageAnchor, messageCount)
	for i := range anchors {
		anchors[i] = MessageAnchor{
			OriginalIndex: i,
			IsSummarized:  false,
			SummaryRef:    -1,
		}
	}
	return &AnchorSet{
		anchors: anchors,
	}
}

// NewAnchorSetFromSnapshots 从持久化的锚点快照恢复 AnchorSet
func NewAnchorSetFromSnapshots(anchors []MessageAnchor) *AnchorSet {
	if anchors == nil {
		anchors = make([]MessageAnchor, 0)
	}
	// 确保所有 SummaryRef 初始化为 -1
	for i := range anchors {
		if anchors[i].SummaryRef == 0 && !anchors[i].IsSummarized {
			anchors[i].SummaryRef = -1
		}
	}
	return &AnchorSet{
		anchors: anchors,
	}
}

// MarkSummarized 标记一段消息区间为已摘要
// startIdx：起始索引（包含）
// endIdx：结束索引（不包含）
// summaryLayer：指向 SummaryStack 中的层号
//
// 并发安全：写锁
func (a *AnchorSet) MarkSummarized(startIdx, endIdx int, summaryLayer int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if startIdx < 0 {
		startIdx = 0
	}
	if endIdx > len(a.anchors) {
		endIdx = len(a.anchors)
	}
	if startIdx >= endIdx {
		return
	}

	for i := startIdx; i < endIdx; i++ {
		a.anchors[i].IsSummarized = true
		a.anchors[i].SummaryRef = summaryLayer
	}
}

// MarkSummarizedByRange 通过 AnchorRange 标记已摘要
func (a *AnchorSet) MarkSummarizedByRange(r AnchorRange, summaryLayer int) {
	a.MarkSummarized(r.StartIndex, r.EndIndex, summaryLayer)
}

// IsSummarized 检查指定索引的消息是否已摘要
func (a *AnchorSet) IsSummarized(index int) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if index < 0 || index >= len(a.anchors) {
		return false
	}
	return a.anchors[index].IsSummarized
}

// NextUnsummarizedRange 返回下一个需要摘要的连续未摘要消息区间
// maxTokens 参数预留用于未来按 token 预算分段，当前仅做连续性查询
// 返回值：start（包含），end（不包含），ok（是否存在）
func (a *AnchorSet) NextUnsummarizedRange(maxTokens int) (start, end int, ok bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for i := 0; i < len(a.anchors); i++ {
		if !a.anchors[i].IsSummarized {
			start = i
			end = i + 1
			// 向后扩展连续区间
			for j := i + 1; j < len(a.anchors); j++ {
				if a.anchors[j].IsSummarized {
					break
				}
				end = j + 1
			}
			return start, end, true
		}
	}
	return 0, 0, false
}

// UnsummarizedRanges 获取所有未摘要的连续区间列表
func (a *AnchorSet) UnsummarizedRanges() []AnchorRange {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var ranges []AnchorRange
	i := 0
	for i < len(a.anchors) {
		if !a.anchors[i].IsSummarized {
			start := i
			for i < len(a.anchors) && !a.anchors[i].IsSummarized {
				i++
			}
			ranges = append(ranges, AnchorRange{StartIndex: start, EndIndex: i})
		} else {
			i++
		}
	}
	return ranges
}

// UnsummarizedCount 返回未摘要的消息数量
func (a *AnchorSet) UnsummarizedCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	count := 0
	for _, anchor := range a.anchors {
		if !anchor.IsSummarized {
			count++
		}
	}
	return count
}

// TotalCount 返回锚点总数（即消息总数）
func (a *AnchorSet) TotalCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return len(a.anchors)
}

// Snapshot 返回锚点集合的一致性快照（用于持久化）
func (a *AnchorSet) Snapshot() []MessageAnchor {
	a.mu.RLock()
	defer a.mu.RUnlock()

	snap := make([]MessageAnchor, len(a.anchors))
	copy(snap, a.anchors)
	return snap
}

// Extend 扩展锚点集合以匹配新的消息数量
// 当新消息到来时调用，新增的锚点默认标记为未摘要
func (a *AnchorSet) Extend(newTotalCount int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if newTotalCount <= len(a.anchors) {
		return
	}

	oldLen := len(a.anchors)
	newAnchors := make([]MessageAnchor, newTotalCount)
	copy(newAnchors, a.anchors)
	for i := oldLen; i < newTotalCount; i++ {
		newAnchors[i] = MessageAnchor{
			OriginalIndex: i,
			IsSummarized:  false,
			SummaryRef:    -1,
		}
	}
	a.anchors = newAnchors
}

// Reset 重置所有锚点为未摘要状态
func (a *AnchorSet) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.anchors {
		a.anchors[i].IsSummarized = false
		a.anchors[i].SummaryRef = -1
	}
}

// IsEmpty 检查是否没有任何锚点
func (a *AnchorSet) IsEmpty() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.anchors) == 0
}
