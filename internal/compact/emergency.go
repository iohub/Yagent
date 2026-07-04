package compact

import (
	"context"
	"fmt"
	"sync"
	"time"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// CircuitBreaker — 熔断器状态机
// ─────────────────────────────────────────────────────────

// CBState 熔断器状态
type CBState int

const (
	CBStateClosed   CBState = iota // 正常：请求通过
	CBStateOpen                    // 熔断：请求被拒绝
	CBStateHalfOpen                // 半开：允许一个探测请求
)

func (s CBState) String() string {
	switch s {
	case CBStateClosed:
		return "Closed"
	case CBStateOpen:
		return "Open"
	case CBStateHalfOpen:
		return "HalfOpen"
	default:
		return fmt.Sprintf("Unknown(%d)", int(s))
	}
}

// CircuitBreaker 保护压缩引擎免受级联故障影响。
// 当压缩持续失败（如 LLM 不可用）时阻断不必要的压缩尝试，
// 避免无限重试拖垮系统。
//
// 状态转换：
//
//	Closed ──(连续失败 ≥ 阈值)──▶ Open
//	Open   ──(resetDuration 到期)──▶ HalfOpen
//	HalfOpen ──(成功)──▶ Closed
//	HalfOpen ──(失败)──▶ Open
type CircuitBreaker struct {
	mu            sync.Mutex
	state         CBState
	failures      int
	threshold     int
	resetDuration time.Duration
	lastFailure   time.Time
	halfOpenUsed  bool // HalfOpen 状态下是否已发放探测票
}

// NewCircuitBreaker 创建熔断器。
//
// 参数：
//   - threshold: 连续失败次数阈值，≤0 时使用默认值 3
//   - resetDuration: 从 Open 切换到 HalfOpen 前的等待时间，≤0 时使用默认值 30s
func NewCircuitBreaker(threshold int, resetDuration time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = 3
	}
	if resetDuration <= 0 {
		resetDuration = 30 * time.Second
	}
	return &CircuitBreaker{
		state:         CBStateClosed,
		failures:      0,
		threshold:     threshold,
		resetDuration: resetDuration,
	}
}

// Allow 返回是否应允许进行压缩尝试。线程安全。
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CBStateClosed:
		return true

	case CBStateOpen:
		if time.Since(cb.lastFailure) >= cb.resetDuration {
			cb.state = CBStateHalfOpen
			cb.halfOpenUsed = false
			return true
		}
		return false

	case CBStateHalfOpen:
		if !cb.halfOpenUsed {
			cb.halfOpenUsed = true
			return true
		}
		return false

	default:
		return false
	}
}

// RecordSuccess 记录一次成功，将熔断器重置为 Closed。
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CBStateClosed
	cb.failures = 0
	cb.halfOpenUsed = false
}

// RecordFailure 记录一次失败。
//   - Closed 状态：递增失败计数，达到阈值时转为 Open
//   - HalfOpen 状态：探测失败，立即转回 Open
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.lastFailure = time.Now()
	cb.halfOpenUsed = false

	switch cb.state {
	case CBStateClosed:
		cb.failures++
		if cb.failures >= cb.threshold {
			cb.state = CBStateOpen
		}

	case CBStateHalfOpen:
		cb.state = CBStateOpen

	case CBStateOpen:
		// 已在 Open 状态，仅更新时间戳
	}
}

// State 返回当前状态（用于诊断/监控）。
func (cb *CircuitBreaker) State() CBState {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// ─────────────────────────────────────────────────────────
// EmergencyConfig / EmergencyResult
// ─────────────────────────────────────────────────────────

// EmergencyConfig 应急压缩的独立配置
type EmergencyConfig struct {
	CBThreshold     int           // 熔断器连续失败阈值
	CBResetDuration time.Duration // 熔断器重置持续时间
}

// EmergencyResult 描述一次应急压缩操作的结果。
type EmergencyResult struct {
	Method         string // "onion" | "fold" | "fallback" | "none"
	LayersStripped int    // 剥洋葱层数（onion 方法中从两端移除的组数）
	TokensRecovered int   // 恢复的 token 数（原始 - 压缩后）
	MessagesKept   int    // 保留的消息数量
}

// ─────────────────────────────────────────────────────────
// Engine 应急压缩方法
// ─────────────────────────────────────────────────────────

// emergencyCB 懒加载熔断器
func (e *Engine) emergencyCB() *CircuitBreaker {
	if e.cb == nil {
		e.cb = NewCircuitBreaker(
			e.config.EmergencyCBThreshold,
			e.config.EmergencyCBResetDuration,
		)
	}
	return e.cb
}

// EmergencyCompress 应急压缩主入口。
// 当正常 LLM 摘要压缩失败或不可用时调用。
//
// 流水线：
//
//	1. 检查熔断器 — 若已熔断，直接进入记忆回退
//	2. 尝试剥洋葱截断 — 从两端逐层剥离消息
//	3. 尝试应急折叠 — 合并相邻同角色消息（无 LLM 调用）
//	4. 记忆回退 — 仅保留 system + 最后一条 user 消息
//	5. 根据结果更新熔断器
//
// 参数：
//   - ctx: 上下文
//   - msgs: 完整消息列表
//   - maxTokens: 目标 token 预算（建议设为 MaxContextTokens 的 50%~75%）
//
// 返回：
//   - []llm.Message: 压缩后的消息列表（可能为空）
//   - *EmergencyResult: 结果详情
//   - error: 错误（nil 表示成功，即使使用了降级策略）
func (e *Engine) EmergencyCompress(ctx context.Context, msgs []llm.Message, maxTokens int) ([]llm.Message, *EmergencyResult, error) {
	if len(msgs) == 0 {
		return msgs, &EmergencyResult{Method: "none", MessagesKept: 0}, nil
	}

	originalTokens := e.countMessagesTokens(msgs)

	// ── 步骤 0: 检查熔断器 ──
	cb := e.emergencyCB()
	if !cb.Allow() {
		// 熔断器处于 Open 状态，跳过所有压缩尝试，直接进入记忆回退
		result, _ := memoryFallback(msgs)
		er := &EmergencyResult{
			Method:          "fallback",
			LayersStripped:  0,
			TokensRecovered: originalTokens - e.countMessagesTokens(result),
			MessagesKept:    len(result),
		}
		return result, er, fmt.Errorf("circuit breaker open: emergency fallback applied")
	}

	// ── 步骤 1: 尝试剥洋葱截断 ──
	result, layers, _ := onionTruncate(e, msgs, maxTokens)
	if len(result) > 0 && e.countMessagesTokens(result) <= maxTokens {
		cb.RecordSuccess()
		return result, &EmergencyResult{
			Method:          "onion",
			LayersStripped:  layers,
			TokensRecovered: originalTokens - e.countMessagesTokens(result),
			MessagesKept:    len(result),
		}, nil
	}

	// ── 步骤 2: 尝试应急折叠（无 LLM 调用） ──
	folded := emergencyFold(msgs)
	if len(folded) > 0 && e.countMessagesTokens(folded) <= maxTokens && len(folded) < len(msgs) {
		cb.RecordSuccess()
		return folded, &EmergencyResult{
			Method:          "fold",
			LayersStripped:  0,
			TokensRecovered: originalTokens - e.countMessagesTokens(folded),
			MessagesKept:    len(folded),
		}, nil
	}

	// ── 步骤 3: 记忆回退（最终降级） ──
	result, _ = memoryFallback(msgs)
	if len(result) > 0 {
		// 回退产生了结果，视为"成功"（至少保留了基本上下文）
		cb.RecordSuccess()
		return result, &EmergencyResult{
			Method:          "fallback",
			LayersStripped:  0,
			TokensRecovered: originalTokens - e.countMessagesTokens(result),
			MessagesKept:    len(result),
		}, nil
	}

	// ── 完全失败 ──
	cb.RecordFailure()
	return nil, &EmergencyResult{
		Method:          "none",
		LayersStripped:  0,
		TokensRecovered: 0,
		MessagesKept:    0,
	}, fmt.Errorf("emergency compress failed: no messages survived any strategy")
}

// ─────────────────────────────────────────────────────────
// onionTruncate — 剥洋葱截断
// ─────────────────────────────────────────────────────────

// onionTruncate 从消息列表两端逐层剥离消息，直到 token 数不超过目标预算。
//
// 约束：
//   - 绝不移除 System 消息
//   - 保留最后一条 User 消息
//   - 保持工具调用原子性（tool_call ↔ tool_response 成对移除）
//
// 参数：
//   - e: 压缩引擎（用于 token 计数）
//   - msgs: 原始消息列表
//   - maxTokens: 目标 token 预算
//
// 返回：
//   - []llm.Message: 截断后的消息
//   - int: 剥离的组数（layers）
//   - llm.TruncationMarker: 截断标记（Strategy 通过 EmergencyResult.Method 传递）
func onionTruncate(e *Engine, msgs []llm.Message, maxTokens int) ([]llm.Message, int, llm.TruncationMarker) {
	marker := llm.TruncationMarker{
		OriginalLen:    len(msgs),
		TruncationPass: 0,
	}

	if len(msgs) == 0 || maxTokens <= 0 {
		return nil, 0, marker
	}

	// 如果已满足预算，直接返回
	if e.countMessagesTokens(msgs) <= maxTokens {
		return msgs, 0, marker
	}

	// 构建原子组
	groups := buildToolCallAtomicGroups(msgs)

	// 标记系统消息索引（不可移除）
	systemSet := make(map[int]bool)
	for i, msg := range msgs {
		if msg.Role == llm.RoleSystem {
			systemSet[i] = true
		}
	}

	// 标记最后一条 User 消息索引（不可移除）
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			lastUserIdx = i
			break
		}
	}

	// 确定每个组是否可移除
	removable := make([]bool, len(groups))
	for gi, group := range groups {
		removable[gi] = true
		for _, idx := range group {
			if systemSet[idx] || idx == lastUserIdx {
				removable[gi] = false
				break
			}
		}
	}

	// 工作状态：每个组是否被保留
	kept := make([]bool, len(groups))
	for i := range kept {
		kept[i] = true
	}

	layers := 0
	// left 指向最旧的未处理组，right 指向最新的未处理组
	left, right := 0, len(groups)-1
	turnLeft := true // 交替从两端开始

	for left <= right {
		// 检查当前状态是否满足预算
		currentMsgs := gatherMessagesFromGroups(msgs, groups, kept)
		if e.countMessagesTokens(currentMsgs) <= maxTokens {
			break
		}

		var targetGroup int
		var found bool

		if turnLeft {
			// Phase 1: 从旧端剥离（left → right）
			for left <= right {
				if removable[left] {
					targetGroup = left
					found = true
					break
				}
				left++
			}
		} else {
			// Phase 2: 从新端剥离（right → left）
			for right >= left {
				if removable[right] {
					targetGroup = right
					found = true
					break
				}
				right--
			}
		}

		if !found {
			break // 没有更多可移除的组
		}

		kept[targetGroup] = false
		layers++

		if turnLeft {
			left++
		} else {
			right--
		}
		turnLeft = !turnLeft // 交替方向
	}

	result := gatherMessagesFromGroups(msgs, groups, kept)
	return result, layers, marker
}

// buildToolCallAtomicGroups 将消息划分为原子组。
// 每个 assistant(with ToolCalls) + 其对应的 tool 响应组成一个组。
// 其他消息各自独立成组。
//
// 返回: [group1_indices, group2_indices, ...]
func buildToolCallAtomicGroups(msgs []llm.Message) [][]int {
	groups := make([][]int, 0, len(msgs))
	toolCallIDToGroup := make(map[string]int)

	for i, msg := range msgs {
		switch msg.Role {
		case llm.RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				estimated := len(msg.ToolCalls) * 2 + 1
				group := make([]int, 0, estimated)
				group = append(group, i)
				gi := len(groups)
				groups = append(groups, group)
				for _, tc := range msg.ToolCalls {
					toolCallIDToGroup[tc.ID] = gi
				}
			} else {
				groups = append(groups, []int{i})
			}

		case llm.RoleTool:
			if gi, ok := toolCallIDToGroup[msg.ToolCallID]; ok {
				groups[gi] = append(groups[gi], i)
			} else {
				groups = append(groups, []int{i})
			}

		default:
			groups = append(groups, []int{i})
		}
	}

	return groups
}

// gatherMessagesFromGroups 按组收集被保留的消息
func gatherMessagesFromGroups(msgs []llm.Message, groups [][]int, kept []bool) []llm.Message {
	result := make([]llm.Message, 0, len(msgs))
	for gi, group := range groups {
		if kept[gi] {
			for _, idx := range group {
				if idx < len(msgs) {
					result = append(result, msgs[idx])
				}
			}
		}
	}
	return result
}

// ─────────────────────────────────────────────────────────
// emergencyFold — 无 LLM 调用的应急折叠
// ─────────────────────────────────────────────────────────

// emergencyFold 合并相邻的同角色消息，减少消息数量。
// 这是最轻量的应急操作，无需 LLM 调用。
//
// 规则：
//   - 相邻同角色消息 → 合并为一个（用 \n\n---\n\n 分隔）
//   - 有 ToolCalls 或 ToolCallID 的消息 → 绝不合并（保持原子性）
//   - 不同角色消息 → 绝不合并
func emergencyFold(msgs []llm.Message) []llm.Message {
	if len(msgs) <= 1 {
		return msgs
	}

	result := make([]llm.Message, 0, len(msgs))
	result = append(result, msgs[0])

	for i := 1; i < len(msgs); i++ {
		curr := msgs[i]
		prev := &result[len(result)-1]

		if canMerge(prev, &curr) {
			prev.Content = joinContent(prev.Content, curr.Content)
		} else {
			result = append(result, curr)
		}
	}

	return result
}

// canMerge 判断两条相邻消息是否可以合并
func canMerge(a, b *llm.Message) bool {
	if a.Role != b.Role {
		return false
	}
	if len(a.ToolCalls) > 0 || len(b.ToolCalls) > 0 {
		return false
	}
	if a.Role == llm.RoleTool || b.Role == llm.RoleTool {
		return false
	}
	if a.ToolCallID != "" || b.ToolCallID != "" {
		return false
	}
	return true
}

// joinContent 合并两条消息的内容
func joinContent(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n---\n\n" + b
}

// ─────────────────────────────────────────────────────────
// memoryFallback — 记忆回退（最终降级）
// ─────────────────────────────────────────────────────────

// memoryFallback 仅保留 System 消息和最后一条 User 消息。
// 这是最极端的降级策略，适用于所有其他方法都失败的情况。
//
// 保留：
//   1. 所有 System 消息（按原始顺序）
//   2. 最后一条 User 消息
//
// 丢弃：
//   - 所有 Assistant 消息
//   - 所有 Tool 消息
//   - 除最后一条外的 User 消息
func memoryFallback(msgs []llm.Message) ([]llm.Message, llm.TruncationMarker) {
	marker := llm.TruncationMarker{
		OriginalLen:    len(msgs),
		TruncationPass: 0,
	}

	if len(msgs) == 0 {
		return nil, marker
	}

	result := make([]llm.Message, 0, len(msgs))

	// 保留所有 System 消息
	for _, msg := range msgs {
		if msg.Role == llm.RoleSystem {
			result = append(result, msg)
		}
	}

	// 找到最后一条 User 消息（从后往前搜索）
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			result = append(result, msgs[i])
			break
		}
	}

	marker.OmittedLen = len(msgs) - len(result)
	return result, marker
}
