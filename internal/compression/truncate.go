package compression

import (
	"fmt"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
)

// TruncateToTokenBudget 将文本内容截断至不超过 keepTokens 个 token。
// 实现策略：先用粗粒度估算（keepTokens*4 字符）裁剪，再用二分/递减微调至精确满足 token 预算。
// 保证不 panic：长度处理均有边界检查。
func TruncateToTokenBudget(content string, keepTokens int) string {
	if keepTokens <= 0 {
		return "[truncated: 内容已截断]"
	}
	// 估算目标字符数：粗裁 keepTokens*4 字符作为上界
	candidateLen := keepTokens * 4
	if len(content) <= candidateLen {
		// 内容本身就不大，直接估算是否真的超了
		if tokenutil.EstimateTokens(content) <= keepTokens {
			return content
		}
	} else {
		content = content[:candidateLen]
	}
	// 二分查找最大前缀，使其 token 数 ≤ keepTokens
	lo, hi := 0, len(content)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if tokenutil.EstimateTokens(content[:mid]) <= keepTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	// 兜底：lo 可能为 0（连一个 token 都放不下），此时保留少量字符并标记
	if lo == 0 {
		if len(content) > 0 {
			return content[:min(10, len(content))] + "\n\n[truncated: 内容已截断，原始约 1 tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		}
		return content
	}
	return content[:lo]
}

// msgTokens 估算单条消息占用的 token：正文 + 会回灌给 provider 的推理 +
// assistant 的工具调用参数。逐条与全量共用这一个口径（EstimateMessagesTokens
// 就是对它求和），截断后的增量记账也靠它。
func msgTokens(msg *llm.Message) int {
	total := tokenutil.EstimateTokens(msg.Content)
	total += tokenutil.EstimateTokens(msg.Reasoning)
	if msg.Role == llm.RoleAssistant {
		for _, tc := range msg.ToolCalls {
			total += tokenutil.EstimateTokens(tc.Function.Arguments)
		}
	}
	return total
}

// truncationNote 是截断标记的固定文案：模型要靠它知道正文是被裁过的，
// 而不是"文件里就只有这些"。生产与测试基线共用这一份，改文案不至于让等价性测试误报。
func truncationNote(originalTokens, keepTokens int) string {
	return fmt.Sprintf("\n\n[truncated: 内容已截断，原始 %d tokens, 保留 %d tokens]", originalTokens, keepTokens)
}

// EstimateMessagesTokens 遍历消息列表，累加各消息的 token 估算值。
// 对 assistant 消息，除 Content 外还将 ToolCalls[].Function.Arguments 文本计入。
// Reasoning 同样计入：thinking/reasoning_content 会被原样回灌给 provider，漏算会让压缩触发过晚。
func EstimateMessagesTokens(messages []llm.Message) int {
	total := 0
	for i := range messages {
		total += msgTokens(&messages[i])
	}
	return total
}
