package compact

import (
	"codeactor/internal/llm"
	"fmt"
	"log/slog"
)

// CompressionContext 压缩管道的上下文信息（逐层共享状态）
type CompressionContext struct {
	// ── 管道级字段 ──

	// Messages 当前消息列表（逐层修改）
	Messages []llm.Message

	// OriginalTokens 原始 token 数（管道入口时计算）
	OriginalTokens int

	// ExtractedState 压缩前提取的状态（用于 Layer 6 状态补偿）
	ExtractedState *ExtractedState

	// LayersApplied 已应用的层名称列表
	LayersApplied []string

	// HardLimit 硬阈值（MaxContextTokens），用于管道层判断
	HardLimit int

	// ── 剪裁专用字段 ──

	// Threshold 动态阈值计算结果（来自 DynamicEngine）
	Threshold *ThresholdResult

	// CurrentTokens 当前消息的总 token 数
	CurrentTokens int

	// TargetTokens 目标 token 数（剪裁后希望达到的值）
	TargetTokens int

	// MinPrunableAge 消息最小年龄（轮数），小于此值的消息不参与修剪
	// 0 表示无限制（所有旧消息均可修剪）
	MinPrunableAge int
}

// PruneResult 剪裁结果
type PruneResult struct {
	// PrunedCount 被剪裁的消息数量
	PrunedCount int

	// PrunedTokens 被剪裁的消息 token 总数
	PrunedTokens int

	// ProtectedCount 受保护（未剪裁）的消息数量
	ProtectedCount int

	// KeptCount 保留的消息数量（= ProtectedCount）
	KeptCount int
}

// String 返回 PruneResult 的可读字符串
func (pr *PruneResult) String() string {
	return fmt.Sprintf("PruneResult{pruned=%d messages (%d tokens), kept=%d messages}",
		pr.PrunedCount, pr.PrunedTokens, pr.KeptCount)
}

// AtomicityGroup 原子消息组
// tool_call 和对应的 tool_response 必须在一起处理（一起保留或一起剪裁）
type AtomicityGroup struct {
	// StartIdx 组中第一条消息在原消息列表中的索引
	StartIdx int
	// EndIdx 组中最后一条消息在原消息列表中的索引
	EndIdx int
	// messages 组内的消息副本（不修改原始切片）
	messages []llm.Message
}

// Len 返回原子组中的消息数量
func (g *AtomicityGroup) Len() int {
	return g.EndIdx - g.StartIdx + 1
}

// Messages 返回原子组内的消息副本
func (g *AtomicityGroup) Messages() []llm.Message {
	cp := make([]llm.Message, len(g.messages))
	copy(cp, g.messages)
	return cp
}

// buildAtomicityGroups 构建原子消息组
//
// 将连续的 tool_call 和对应的 tool_response 组合成原子组：
//   - 单个普通消息 → 独立原子组
//   - assistant(tool_calls) + 多个 tool response → 合并为一个原子组
//
// 原子组保证：同一个组内的消息会被一起保留或一起剪裁，不会拆分 tool_call/tool_response 对
func (e *Engine) buildAtomicityGroups(msgs []llm.Message) []*AtomicityGroup {
	if len(msgs) == 0 {
		return nil
	}

	groups := make([]*AtomicityGroup, 0, len(msgs))
	i := 0

	for i < len(msgs) {
		msg := msgs[i]

		// 检查是否为 assistant 消息且包含 tool_calls
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			// 找到这个 tool_call 组：assistant + 所有对应的 tool response
			startIdx := i
			endIdx := i

			// 向后遍历，将所有连续的 tool response 加入同一组
			j := i + 1
			for j < len(msgs) && msgs[j].Role == llm.RoleTool {
				endIdx = j
				j++
			}

			group := &AtomicityGroup{
				StartIdx: startIdx,
				EndIdx:   endIdx,
				messages: msgs[startIdx : endIdx+1],
			}
			groups = append(groups, group)
			i = j // 跳过已加入组的消息
		} else {
			// 普通消息 → 独立原子组
			group := &AtomicityGroup{
				StartIdx: i,
				EndIdx:   i,
				messages: msgs[i : i+1],
			}
			groups = append(groups, group)
			i++
		}
	}

	return groups
}

// hasAnchoredMessage 检查原子组中是否有锚定消息
//
// 锚定消息（IsAnchored=true）受保护，永不剪裁
func (e *Engine) hasAnchoredMessage(group *AtomicityGroup) bool {
	for _, msg := range group.messages {
		if msg.IsAnchored {
			return true
		}
	}
	return false
}

// findKeepBoundary 找到保留区的起始索引
//
// 从末尾数 KeepRecentRounds 轮（每轮 = 2 条消息：user + assistant）
// 返回保留区第一条消息的索引。
//
// 返回值说明：
//   - 如果所有消息都应保留，返回 len(msgs)
//   - 否则返回需要保留的第一条消息的索引
func (e *Engine) findKeepBoundary(msgs []llm.Message) int {
	if len(msgs) == 0 {
		return 0
	}

	keepCount := e.config.KeepRecentRounds * 2
	if keepCount <= 0 {
		keepCount = 6 // 默认保留 3 轮
	}
	if keepCount >= len(msgs) {
		return len(msgs) // 全部保留
	}

	return len(msgs) - keepCount
}

// pruneOldMessages 主入口：根据方向执行老消息剪裁
//
// 流程：
//   1. 根据 CompressionDirection 决定剪裁方向
//   2. 计算需要释放的 token 数
//   3. 构建原子组，跳过锚定消息
//   4. 按方向剪裁，直到 token 数达标或无可剪裁消息
//   5. 返回剪裁结果
//
// 方向策略：
//   - PreserveRecent: 从旧端（头部）剪裁，保留最近消息
//   - PreserveOld: 从近端（保留区前）剪裁，保留旧消息
func (e *Engine) pruneOldMessages(msgs []llm.Message, cc *CompressionContext) *PruneResult {
	if cc == nil || cc.Threshold == nil {
		slog.Warn("pruneOldMessages: nil context or threshold, skipping")
		return &PruneResult{KeptCount: 0}
	}

	// 获取可剪裁的消息（排除最近 MinPrunableAge 轮）
	prunableMsgs := e.getPrunableMessages(msgs, cc.MinPrunableAge)
	if len(prunableMsgs) == 0 {
		slog.Debug("pruneOldMessages: no prunable messages")
		return &PruneResult{KeptCount: 0}
	}

	// 1. 计算需要释放的 token 数
	tokensToFree := cc.CurrentTokens - cc.TargetTokens
	if tokensToFree <= 0 {
		slog.Debug("pruneOldMessages: no tokens to free")
		return &PruneResult{KeptCount: len(msgs)}
	}

	direction := cc.Threshold.Direction
	slog.Info("pruneOldMessages: starting",
		"direction", direction,
		"tokens_to_free", tokensToFree,
		"total_messages", len(prunableMsgs))

	// 2. 构建原子组
	groups := e.buildAtomicityGroups(prunableMsgs)
	if len(groups) == 0 {
		return &PruneResult{KeptCount: 0}
	}

	// 3. 根据方向选择剪裁顺序
	prunableGroups := e.filterPrunableGroups(groups, cc.MinPrunableAge)

	var prunedCount int
	var prunedTokens int
	var protectedCount int

	switch direction {
	case PreserveRecent:
		// 保最近：从旧端（头部）开始剪裁
		prunedCount, prunedTokens = e.pruneFromHead(prunableGroups, tokensToFree)

	case PreserveOld:
		// 保旧消息：从近端（保留区前）开始剪裁
		prunedCount, prunedTokens = e.pruneFromTail(prunableGroups, tokensToFree)

	default:
		slog.Warn("pruneOldMessages: unknown direction, defaulting to PreserveRecent",
			"direction", direction)
		prunedCount, prunedTokens = e.pruneFromHead(prunableGroups, tokensToFree)
	}

	protectedCount = len(prunableGroups) - prunedCount

	// 4. 统计受保护消息（锚定消息 + 未被剪裁的消息）
	//    注意：protectedCount 只统计 prunable groups 中的保护数
	//    总保护数 = protectedCount + 任何不可 pruning 的消息

	prunedRatio := 1.0
	if tokensToFree > 0 {
		prunedRatio = 1.0 - float64(prunedTokens)/float64(tokensToFree)
	}

	slog.Info("pruneOldMessages: completed",
		"pruned_count", prunedCount,
		"pruned_tokens", prunedTokens,
		"protected_count", protectedCount,
		"completion_ratio", fmt.Sprintf("%.1f%%", prunedRatio*100))

	return &PruneResult{
		PrunedCount:    prunedCount,
		PrunedTokens:   prunedTokens,
		ProtectedCount: protectedCount,
		KeptCount:      protectedCount,
	}
}

// getPrunableMessages 获取可剪裁的消息列表
//
// 根据 MinPrunableAge 过滤掉最近的消息（最近的 N 轮不参与剪裁）
// 返回修剪后的消息切片（不包含最近 MinPrunableAge 轮的消息）
func (e *Engine) getPrunableMessages(msgs []llm.Message, minPrunableAge int) []llm.Message {
	if len(msgs) == 0 {
		return nil
	}

	// 计算需要排除的最近消息数量
	keepCount := minPrunableAge * 2
	if keepCount <= 0 {
		keepCount = e.config.KeepRecentRounds * 2
		if keepCount <= 0 {
			keepCount = 6 // 默认保留 3 轮
		}
	}
	if keepCount >= len(msgs) {
		return nil // 所有消息都在保留区内
	}

	// 返回保留区之前的消息（可剪裁部分）
	return msgs[:len(msgs)-keepCount]
}

// filterPrunableGroups 过滤出可剪裁的原子组
//
// 排除包含锚定消息的组
func (e *Engine) filterPrunableGroups(groups []*AtomicityGroup, _ int) []*AtomicityGroup {
	prunable := make([]*AtomicityGroup, 0, len(groups))

	for _, group := range groups {
		if !e.hasAnchoredMessage(group) {
			prunable = append(prunable, group)
		}
	}

	return prunable
}

// pruneFromHead 从旧端（头部）开始剪裁
//
// 按原子组顺序，从第一个可剪裁组开始，逐个移除直到满足 token 要求
func (e *Engine) pruneFromHead(groups []*AtomicityGroup, targetTokens int) (prunedCount, prunedTokens int) {
	remaining := targetTokens

	for _, group := range groups {
		if remaining <= 0 {
			break
		}

		// 计算该组的 token 数
		groupTokens := e.countGroupTokens(group)
		if groupTokens <= 0 {
			continue
		}

		// 移除该组
		prunedCount++
		prunedTokens += groupTokens
		remaining -= groupTokens
	}

	return prunedCount, prunedTokens
}

// pruneFromTail 从近端（尾部/保留区前）开始剪裁
//
// 按原子组逆序，从最后一个可剪裁组开始，逐个移除直到满足 token 要求
// 这保留的是旧消息（头部），移除的是新消息（尾部）
func (e *Engine) pruneFromTail(groups []*AtomicityGroup, targetTokens int) (prunedCount, prunedTokens int) {
	remaining := targetTokens

	// 逆序遍历
	for i := len(groups) - 1; i >= 0; i-- {
		if remaining <= 0 {
			break
		}

		group := groups[i]
		groupTokens := e.countGroupTokens(group)
		if groupTokens <= 0 {
			continue
		}

		// 移除该组
		prunedCount++
		prunedTokens += groupTokens
		remaining -= groupTokens
	}

	return prunedCount, prunedTokens
}

// countGroupTokens 计算原子组中所有消息的 token 总数
func (e *Engine) countGroupTokens(group *AtomicityGroup) int {
	totalTokens := 0

	for _, msg := range group.messages {
		// Content tokens
		contentTokens, err := e.tokenizer.CountTokens(msg.Content)
		if err == nil {
			totalTokens += contentTokens
		} else {
			// Fallback: 估算
			totalTokens += len([]rune(msg.Content)) / 4
		}

		// Reasoning tokens
		if msg.Reasoning != "" {
			reasoningTokens, err := e.tokenizer.CountTokens(msg.Reasoning)
			if err == nil {
				totalTokens += reasoningTokens
			}
		}

		// Tool call tokens
		for _, tc := range msg.ToolCalls {
			// ToolCall ID + Function Name + Arguments
			tcTokens, _ := e.tokenizer.CountTokens(tc.ID + tc.Function.Name + tc.Function.Arguments)
			totalTokens += tcTokens
		}
	}

	return totalTokens
}
