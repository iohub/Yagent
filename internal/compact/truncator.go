package compact

import (
	"fmt"
	"sort"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// 类型感知的详细结果截断 (Type-Aware Verbose Result Truncation)
// ─────────────────────────────────────────────────────────

// VerboseTools 内容多、贡献少的工具列表。
// 这些工具（如 run_bash, read_file）通常产生大量输出但信息密度低，
// 在上下文压缩时应优先截断其输出。
var VerboseTools = map[string]bool{
	"run_bash":   true,
	"read_file":  true,
	"list_files": true,
	"grep":       true,
	"search":     true,
}

// MessagePriority 消息压缩优先级（数字越大，越优先被截断）
type MessagePriority int

const (
	// PriorityNever 永不压缩：锚定消息、System消息
	PriorityNever MessagePriority = 0
	// PriorityHigh 仅LLM摘要：User消息、助理推理链
	PriorityHigh MessagePriority = 1
	// PriorityMedium 非详细工具的结果
	PriorityMedium MessagePriority = 2
	// PriorityLow 优先截断：详细工具的结果 (run_bash/read_file 等)
	PriorityLow MessagePriority = 3
	// PriorityTruncated 最优先：已被截断过的内容，可进一步截断
	PriorityTruncated MessagePriority = 4
)

// TruncationConfig 截断配置
type TruncationConfig struct {
	// HeadLen 保留头部字符数（默认 512）
	HeadLen int
	// TailLen 保留尾部字符数（默认 128）
	TailLen int
	// TruncatedLen 已被截断过的内容保留更少（默认 256）
	TruncatedLen int
}

// DefaultTruncationConfig 默认截断配置
var DefaultTruncationConfig = TruncationConfig{
	HeadLen:      512,
	TailLen:      128,
	TruncatedLen: 256,
}

// getMessagePriority 获取消息的压缩优先级
func getMessagePriority(msg llm.Message) MessagePriority {
	if msg.IsAnchored || msg.Role == llm.RoleSystem {
		return PriorityNever
	}
	if msg.Role == llm.RoleUser || msg.Role == llm.RoleAssistant {
		return PriorityHigh
	}
	if msg.Role == llm.RoleTool {
		if msg.TruncationMarker != nil {
			return PriorityTruncated
		}
		if VerboseTools[msg.ToolName] {
			return PriorityLow
		}
		return PriorityMedium
	}
	return PriorityMedium
}

// truncateToolResult 截断单条工具结果消息
// 返回实际释放的字节数
func truncateToolResult(msg *llm.Message, cfg TruncationConfig) int {
	content := msg.Content
	if len(content) == 0 {
		return 0
	}

	limit := cfg.HeadLen
	tailLen := cfg.TailLen
	pass := 0
	if msg.TruncationMarker != nil {
		limit = cfg.TruncatedLen
		pass = msg.TruncationMarker.TruncationPass + 1
	}

	// 如果内容不够长，不截断（保留阈值：至少需要 head + tail + 一些中间内容）
	if len(content) <= limit+tailLen+64 {
		return 0
	}

	originalLen := len(content)

	// 保留头部和尾部
	head := content[:limit]
	tail := content[len(content)-tailLen:]
	omitted := originalLen - limit - tailLen

	// 设置截断标记
	msg.TruncationMarker = &llm.TruncationMarker{
		ToolName:       msg.ToolName,
		OriginalLen:    originalLen,
		OmittedLen:     omitted,
		TruncationPass: pass,
	}

	// 构建截断后的内容（头部 + 截断标记 + 尾部）
	truncationNotice := ""
	if pass > 0 {
		truncationNotice = fmt.Sprintf(
			"\n\n[... %d bytes of %s output omitted (pass %d, previously truncated) ...]\n\n",
			omitted, msg.ToolName, pass,
		)
	} else {
		truncationNotice = fmt.Sprintf(
			"\n\n[... %d bytes of %s output omitted for context efficiency ...]\n\n",
			omitted, msg.ToolName,
		)
	}

	msg.Content = head + truncationNotice + tail

	return originalLen - len(msg.Content)
}

// truncatableMsg 内部结构，用于排序
type truncatableMsg struct {
	index    int
	priority MessagePriority
}

// ForceTruncateAll 传给 needToFreeTokens 的特殊值，表示截断所有可截断的消息
const ForceTruncateAll = -1

// TruncateToolResults 批量截断工具结果，优先截断优先级高的（信息价值低的）。
// 参数：
//   - messages: 待截断的消息列表（不会被修改，内部会深拷贝）
//   - needToFreeTokens: 需要释放的目标token数（<=0 表示截断所有可截断的）
//   - cfg: 截断配置
//
// 返回：
//   - 截断后的消息列表
//   - 实际释放的字节数
func TruncateToolResults(messages []llm.Message, needToFreeTokens int, cfg TruncationConfig) ([]llm.Message, int) {
	if needToFreeTokens <= 0 && needToFreeTokens != ForceTruncateAll {
		return messages, 0
	}
	if len(messages) == 0 {
		return messages, 0
	}

	// 收集可截断消息及其优先级
	var truncatable []truncatableMsg
	for i, msg := range messages {
		p := getMessagePriority(msg)
		if p >= PriorityLow {
			truncatable = append(truncatable, truncatableMsg{i, p})
		}
	}

	if len(truncatable) == 0 {
		return messages, 0
	}

	// 按优先级从高到低排序（PriorityTruncated > PriorityLow 优先截断）
	sort.SliceStable(truncatable, func(i, j int) bool {
		return truncatable[i].priority > truncatable[j].priority
	})

	// 深拷贝消息列表，避免修改原始数据
	result := make([]llm.Message, len(messages))
	copy(result, messages)

	totalFreed := 0
	truncateAll := needToFreeTokens == ForceTruncateAll

	for _, tm := range truncatable {
		if !truncateAll && totalFreed >= needToFreeTokens {
			break
		}

		freed := truncateToolResult(&result[tm.index], cfg)
		totalFreed += freed
	}

	return result, totalFreed
}

// CountTruncatableTokens 估算可截断消息的总token数
// 用于决策层判断截断是否能释放足够的token
func CountTruncatableTokens(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		p := getMessagePriority(msg)
		if p >= PriorityLow {
			// 粗略估算：4字符=1token
			total += len(msg.Content) / 4
		}
	}
	return total
}
