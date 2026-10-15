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

// Prefix 是"provider 已经替我们数过的那一段"：Tokens 来自上一步响应的
// Usage.TotalInputTokens（跨 provider 的统一口径 —— OpenAI 的 prompt_tokens 已含缓存，
// Anthropic 的 input_tokens 不含，引擎侧已归一），Len 是那个数覆盖到的消息条数。
//
// 用它而不是本地全量估算有两个理由：本地估算从不计工具声明（各 Agent 每步实际发出去
// 1.8k–5k token），而且每步要扫两遍全量 tiktoken。
type Prefix struct {
	Tokens int64 // 0 = 没有可用计量
	Len    int   // 该计量覆盖 messages 的前 Len 条
}

// usable 报告这份计量对当前消息列表是否还可用。Total 与第一档的口径选择共用它，
// 免得两处回落条件写漂 —— 写漂的表现是"判定用一个数、stats 用另一个数"。
func (p Prefix) usable(messages []llm.Message) bool {
	return p.Tokens > 0 && p.Len > 0 && p.Len <= len(messages)
}

// Total 返回本次请求的 prompt token 数：计量打底 + 此后新增消息的本地估算。
// 那个"此后"是关键：只用 Tokens 会漏掉本步新增的工具结果（一次 read_file 就能塞进
// 几万 token），触发会晚一步，极端情况直接撞窗口。
//
// 没有可用计量（没报 usage、还没跑过一步、或消息列表被压缩重写变短了）就回落到全量
// 本地估算 —— 与 Prefix 出现之前的行为逐字相同。
//
// 偏差一律偏大（更早触发）：送出去的列表被 NormalizeMessages 改写过、Director 那份
// 还多附了便签索引、provider 的数含工具声明、两边分词器不同。
// 所以这个数只用于"是否超阈值"这一个判断，不得当精确预算 —— stats 一律用本地口径。
func (p Prefix) Total(messages []llm.Message) int {
	if !p.usable(messages) {
		return EstimateMessagesTokens(messages)
	}
	return int(p.Tokens) + EstimateMessagesTokens(messages[p.Len:])
}

// OverThreshold 是触发判定的唯一实现：严格大于（等于阈值不触发）。
// 两个循环的四个判定点都走这里，不要再抄一份 `EstimateMessagesTokens(...) > threshold`
// —— 抄开的表现是"子 Agent 与 Director 的触发时机不一致"。
func OverThreshold(messages []llm.Message, prefix Prefix, threshold int) bool {
	return prefix.Total(messages) > threshold
}

// Refresh 按一次调用的结果推进计量口径，两个循环（及其重试路径）共用这一条规则：
//
//   - 调用失败 → 原样保留：重试发的是同一份 messages，旧基数只会偏大（更早触发）；
//   - 成功但网关没报 usage（或报了 0）→ 作废，下一步回落到全量本地估算；
//   - 成功且带回 usage → 以 sentLen 为新边界。sentLen 必须是**这次送出去的外层消息条数**，
//     在追加 assistant 回复与工具结果之前取。
func (p Prefix) Refresh(resp *llm.Response, callErr error, sentLen int) Prefix {
	if callErr != nil {
		return p
	}
	if resp == nil || resp.Usage == nil || resp.Usage.TotalInputTokens <= 0 {
		return Prefix{}
	}
	return Prefix{Tokens: resp.Usage.TotalInputTokens, Len: sentLen}
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
