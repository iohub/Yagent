package compact

import (
	"codeactor/internal/llm"
	"strings"
)

// CONTEXT_SUMMARY_PREFIX 摘要消息前缀标记
const CONTEXT_SUMMARY_PREFIX = "[CONTEXT SUMMARY]"

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
// 移除常见的脏数据：指令镜像、开头客套话、格式不统一等问题
func CleanSummaryOutput(raw string) string {
	cleaned := raw

	// 移除 markdown 代码块包装
	if strings.HasPrefix(cleaned, "```") {
		// 找到第一个 ``` 和最后一个 ``` 之间的内容
		first := strings.Index(cleaned, "\n")
		last := strings.LastIndex(cleaned, "```")
		if first > 0 && last > first {
			cleaned = cleaned[first:last]
		}
		cleaned = strings.TrimSpace(cleaned)
	}

	// 移除常见的开头客套话
	prefixes := []string{
		"Sure", "Sure,", "Here", "Here's", "Here is",
		"Certainly", "Of course", "I'll", "I will",
		"好的", "好的，", "当然", "当然，", "我来",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(cleaned, p) {
			// 移除第一行（客套话所在行）
			lines := strings.SplitN(cleaned, "\n", 2)
			if len(lines) > 1 {
				cleaned = strings.TrimSpace(lines[1])
			} else {
				cleaned = ""
			}
			break
		}
	}

	return strings.TrimSpace(cleaned)
}
