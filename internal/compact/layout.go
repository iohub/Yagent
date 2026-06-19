package compact

import (
	"codeactor/internal/llm"
	"regexp"
	"strings"
)

// CONTEXT_SUMMARY_PREFIX 摘要消息前缀标记
const CONTEXT_SUMMARY_PREFIX = "[CONTEXT SUMMARY]"

// ExtractResult 消息提取结果，包含原始索引信息
type ExtractResult struct {
	// Recent 保留区消息（高优先级，最近对话轮次）
	Recent []llm.Message

	// Compressible 待压缩消息（中间区域，低优先级）
	Compressible []llm.Message

	// RecentStartIndex 保留区在原始消息列表中的起始索引
	RecentStartIndex int

	// RecentEndIndex 保留区在原始消息列表中的结束索引（不包含）
	RecentEndIndex int

	// CompressibleStartIndex 待压缩区在原始消息列表中的起始索引
	CompressibleStartIndex int

	// CompressibleEndIndex 待压缩区在原始消息列表中的结束索引（不包含）
	CompressibleEndIndex int
}

// BuildCacheAwareMessages 构建缓存友好的消息布局
// 输入：原始 system 消息、约束块文本、摘要栈、高优先级保留区消息
// 输出：缓存友好的消息列表
//
// 布局顺序（Cache-Aware 优化）：
//   [System Message]         ← 绝对稳定前缀（始终不变）
//   [Constraints Block]      ← 稳定（提取后不变化）
//   [Summary Entry N-2]      ← 稳定（旧摘要已生成不变）
//   [Summary Entry N-1]      ← 稳定（旧摘要已生成不变）
//   [Summary Entry N]        ← 新追加（缓慢增长端）
//   [Recent Kept Messages]   ← 变动区域（用户最新消息）
func BuildCacheAwareMessages(
	systemMsg *llm.Message,     // 原始 System 消息（可为 nil）
	constraints string,         // 约束块文本
	summaryStack []SummaryBlock, // 摘要栈（从旧到新）
	recentMessages []llm.Message, // 保留区消息（User/Assistant/Tool）
) []llm.Message {

	result := make([]llm.Message, 0, 1+1+len(summaryStack)+len(recentMessages))

	// 1. 原始 System 消息（绝对稳定前缀，始终在第一位置）
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	// 2. 约束块（作为 System 消息，内容稳定）
	if constraints != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: "[CONTEXT CONSTRAINTS]\n" + constraints,
		})
	}

	// 3. 摘要栈（从旧到新排列，旧摘要稳定不变）
	for _, block := range summaryStack {
		if block.Summary != "" {
			result = append(result, llm.Message{
				Role:    llm.RoleSystem,
				Content: block.Summary,
			})
		}
	}

	// 4. 保留区消息（高优先级，保持原有顺序）
	result = append(result, recentMessages...)

	return result
}

// IsSummaryMessage 检查消息是否为摘要消息
func IsSummaryMessage(msg *llm.Message) bool {
	return strings.HasPrefix(msg.Content, CONTEXT_SUMMARY_PREFIX)
}

// ExtractRecentMessages 从消息列表中提取最近的保留区消息（不包括 System 和摘要）
// 返回保留区消息和剩余待压缩消息
func ExtractRecentMessages(
	messages []llm.Message,
	keepRecentRounds int,
) (recent []llm.Message, compressible []llm.Message) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 从后往前数 keepRecentRounds*2 条消息作为保留区（每轮 user+assistant 约2条）
	keepCount := keepRecentRounds * 2
	if keepCount <= 0 {
		keepCount = 6 // 默认保留3轮
	}
	if keepCount > len(messages) {
		keepCount = len(messages)
	}

	recentStart := len(messages) - keepCount

	// 保留区：最后 keepRecentRounds 轮的消息
	recent = make([]llm.Message, keepCount)
	copy(recent, messages[recentStart:])

	// 待压缩区：保留区之前且非 System/摘要的消息
	compressible = make([]llm.Message, 0, recentStart)
	for i := 0; i < recentStart; i++ {
		msg := messages[i]
		// 跳过 System 消息和已有摘要消息（它们不应再被压缩）
		if msg.Role == llm.RoleSystem || IsSummaryMessage(&msg) {
			continue
		}
		compressible = append(compressible, msg)
	}

	return recent, compressible
}

// ExtractRecentMessagesV2 从消息列表中提取最近的保留区消息和待压缩消息（带索引）
// 与 ExtractRecentMessages 的区别：返回 ExtractResult，包含原始索引信息
//
// 分区逻辑：
//   1. 从后往前数 keepRecentRounds*2 条消息作为保留区
//   2. 保留区之前的非 System/摘要消息作为待压缩区
func ExtractRecentMessagesV2(
	messages []llm.Message,
	keepRecentRounds int,
) *ExtractResult {
	result := &ExtractResult{}

	if len(messages) == 0 {
		return result
	}

	// 从后往前数 keepRecentRounds*2 条消息作为保留区（每轮 user+assistant 约2条）
	keepCount := keepRecentRounds * 2
	if keepCount <= 0 {
		keepCount = 6 // 默认保留3轮
	}
	if keepCount > len(messages) {
		keepCount = len(messages)
	}

	recentStart := len(messages) - keepCount

	// 保留区：最后 keepRecentRounds 轮的消息
	result.Recent = make([]llm.Message, keepCount)
	copy(result.Recent, messages[recentStart:])
	result.RecentStartIndex = recentStart
	result.RecentEndIndex = len(messages)

	// 待压缩区：保留区之前且非 System/摘要的消息
	result.Compressible = make([]llm.Message, 0, recentStart)
	result.CompressibleStartIndex = 0
	for i := 0; i < recentStart; i++ {
		msg := messages[i]
		// 跳过 System 消息和已有摘要消息（它们不应再被压缩）
		if msg.Role == llm.RoleSystem || IsSummaryMessage(&msg) {
			continue
		}
		// 记录第一个待压缩消息的索引
		if len(result.Compressible) == 0 {
			result.CompressibleStartIndex = i
		}
		result.Compressible = append(result.Compressible, msg)
	}
	result.CompressibleEndIndex = recentStart

	return result
}

// ExtractCompressibleRange 从消息列表中确定可压缩的消息索引范围
// 返回 [startIdx, endIdx) 区间，该区间内的消息（排除 System 和摘要）应被压缩
// 用于 AnchorSet 的场景：基于保留区边界确定压缩范围
//
// 参数：
//   - messages: 完整消息列表
//   - keepRecentRounds: 保留的最近对话轮数
// 返回：
//   - startIdx: 可压缩区起始索引（包含）
//   - endIdx: 可压缩区结束索引（不包含）
//   - ok: 是否存在可压缩消息
func ExtractCompressibleRange(
	messages []llm.Message,
	keepRecentRounds int,
) (startIdx, endIdx int, ok bool) {
	if len(messages) == 0 {
		return 0, 0, false
	}

	keepCount := keepRecentRounds * 2
	if keepCount <= 0 {
		keepCount = 6
	}
	if keepCount > len(messages) {
		keepCount = len(messages)
	}

	endIdx = len(messages) - keepCount
	if endIdx <= 0 {
		return 0, 0, false
	}

	// 从前往后找到第一个非 System/摘要的消息
	for i := 0; i < endIdx; i++ {
		msg := messages[i]
		if msg.Role != llm.RoleSystem && !IsSummaryMessage(&msg) {
			return i, endIdx, true
		}
	}

	return 0, 0, false
}

// ExtractSystemMessage 从消息列表中提取第一条 System 消息
func ExtractSystemMessage(messages []llm.Message) *llm.Message {
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			return &msg
		}
	}
	return nil
}

// FormatConstraintsFromMessages 从消息列表中提取约束文本
// 简单实现：查找包含明确约束关键词的用户消息
// 后续可以升级为 LLM 提取
func FormatConstraintsFromMessages(messages []llm.Message) string {
	var sb strings.Builder
	constraintKeywords := []string{"must", "should", "need to", "require", "ensure",
		"don't", "never", "avoid", "must not",
		"必须", "需要", "确保", "一定要", "务必",
		"不要", "禁止", "避免", "不能"}

	for _, msg := range messages {
		if msg.Role != llm.RoleUser {
			continue
		}
		contentLower := strings.ToLower(msg.Content)
		for _, kw := range constraintKeywords {
			if strings.Contains(contentLower, strings.ToLower(kw)) {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("- " + msg.Content)
				break
			}
		}
	}
	return sb.String()
}

// CleanSummaryOutput 清洗 LLM 摘要输出
// 移除常见的脏数据：指令镜像、开头客套话、格式不统一、重复行等问题
func CleanSummaryOutput(raw string) string {
	if raw == "" {
		return ""
	}

	cleaned := raw

	// 1. 移除 markdown 代码块包装
	cleaned = removeMarkdownFence(cleaned)

	// 2. 移除常见的开头客套话
	cleaned = removeCourtesyPrefix(cleaned)

	// 3. 移除连续重复行（同一行出现3次以上）
	cleaned = removeDuplicateLines(cleaned)

	// 4. 移除过长的空白序列
	cleaned = compactWhitespace(cleaned)

	return strings.TrimSpace(cleaned)
}

// removeMarkdownFence 移除 markdown 代码块围栏
func removeMarkdownFence(text string) string {
	if !strings.HasPrefix(text, "```") {
		return text
	}
	first := strings.Index(text, "\n")
	last := strings.LastIndex(text, "```")
	if first > 0 && last > first {
		text = text[first:last]
	}
	return strings.TrimSpace(text)
}

// removeCourtesyPrefix 移除 LLM 回复开头的客套话
func removeCourtesyPrefix(text string) string {
	prefixes := []string{
		"Sure", "Sure,", "Here", "Here's", "Here is", "Here are",
		"Certainly", "Of course", "I'll", "I will", "I can",
		"好的", "好的，", "好的:", "当然", "当然，", "当然:",
		"我来", "我来给", "以下是", "下面",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(text, p) {
			lines := strings.SplitN(text, "\n", 2)
			if len(lines) > 1 {
				text = strings.TrimSpace(lines[1])
			} else {
				text = ""
			}
			break
		}
	}
	return text
}

// removeDuplicateLines 移除连续重复的行
func removeDuplicateLines(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) < 3 {
		return text
	}

	var result []string
	repeatCount := 1
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		prevTrimmed := strings.TrimSpace(lines[i-1])

		if trimmed == prevTrimmed && trimmed != "" {
			repeatCount++
			if repeatCount > 2 {
				// 跳过这一行（已经是第3次重复）
				continue
			}
		} else {
			repeatCount = 1
		}
		result = append(result, lines[i-1])
	}
	// 添加最后一行
	result = append(result, lines[len(lines)-1])

	return strings.Join(result, "\n")
}

// compactWhitespace 压缩连续空白
func compactWhitespace(text string) string {
	// 将3个以上的连续换行替换为2个
	re := regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	// 将行尾空白移除
	re = regexp.MustCompile(`[ \t]+$`)
	text = re.ReplaceAllString(text, "")
	return text
}
