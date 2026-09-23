package timeline

import (
	"fmt"
	"strings"

	"codeactor/internal/tokenutil"
)

// ultimateHeader 重建输入的首行标题。
const ultimateHeader = "[ULTIMATE CONTEXT COMPRESSION]"

// ultimateIntro 固定的中文说明段：告知模型当前输入的性质与继续任务的方式。
const ultimateIntro = "这是上下文超限后的重建输入，之前的对话历史已被丢弃。请基于下述「用户原始输入」与「Thought & Plan 时间线」继续任务：不要重新询问用户，不要重复已完成的结论；请对照各 Plan Update 的勾选状态，从第一个未完成步骤继续。"

// noUserInputPlaceholder 无用户原始输入记录时的占位文案。
const noUserInputPlaceholder = "（未记录到用户原始输入）"

// noPlanPlaceholder 无思考计划记录时的占位文案。
const noPlanPlaceholder = "（无记录）"

// truncationMarker 截断标记，追加在被截断内容之后。
const truncationMarker = "[…已截断]"

// planTimeFormat 思考计划条目标题中的时间格式。
const planTimeFormat = "2006-01-02 15:04:05"

// buildInput 按固定格式组装输入文本。
// dropped > 0 时在 Thought & Plan 区块开头插入丢弃标记行（N 为累计丢弃数）；
// dropped > 0 且 plans 已全部丢弃时不再输出「（无记录）」，以丢弃标记说明情况。
func buildInput(userInput string, plans []Entry, dropped int) string {
	var b strings.Builder
	b.WriteString(ultimateHeader)
	b.WriteString("\n\n")
	b.WriteString(ultimateIntro)
	b.WriteString("\n\n## 用户原始输入\n\n")
	b.WriteString(userInput)
	b.WriteString("\n\n## Thought & Plan 时间线\n\n")
	if dropped > 0 {
		fmt.Fprintf(&b, "[…已丢弃更早的 %d 条思考计划以满足 token 预算]\n\n", dropped)
	}
	if len(plans) == 0 {
		if dropped == 0 {
			b.WriteString(noPlanPlaceholder)
			b.WriteString("\n")
		}
		return b.String()
	}
	for _, e := range plans {
		fmt.Fprintf(&b, "### [#%d] %s\n\n%s\n\n", e.Seq, e.Timestamp.Format(planTimeFormat), e.Content)
	}
	return b.String()
}

// truncateUserToFit 二分查找用户原始输入可保留的最大字符数：
// 截断后的用户输入追加 truncationMarker 并连同其余部分一起估算，
// 需满足 tokenutil.EstimateTokens(全文) <= maxTokens。
// 返回截断后的用户输入（含标记）；连空输入都无法满足预算时返回 ("", false)。
func truncateUserToFit(userInput string, plans []Entry, dropped int, maxTokens int) (string, bool) {
	runes := []rune(userInput)
	lo, hi := 0, len(runes)
	best := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := buildInput(string(runes[:mid])+truncationMarker, plans, dropped)
		if tokenutil.EstimateTokens(cand) <= maxTokens {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		return "", false
	}
	return string(runes[:best]) + truncationMarker, true
}

// hardTruncate 最终兜底：对全文按字符做二分硬截断，附 truncationMarker，
// 保证返回值 token 数 <= maxTokens（尽力满足）。
// 若带上标记仍无法满足（maxTokens 极小），退化为不带标记截断；仍不行则返回空串。
func hardTruncate(text string, maxTokens int) string {
	runes := []rune(text)
	for _, withMarker := range [...]bool{true, false} {
		suffix := ""
		if withMarker {
			suffix = truncationMarker
		}
		lo, hi := 0, len(runes)
		best := -1
		for lo <= hi {
			mid := (lo + hi) / 2
			if tokenutil.EstimateTokens(string(runes[:mid])+suffix) <= maxTokens {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if best >= 0 {
			return string(runes[:best]) + suffix
		}
	}
	return ""
}

// BuildTimelineInput 把时间线条目组装为上下文超限后的全新输入文本。
//
// maxTokens <= 0 视为不限制，直接返回全量文本。
// 超预算时按三级策略压缩：
//  1. 从最旧的 thought_plan 条目开始逐条丢弃，丢弃处替换为累计计数标记行；
//  2. 全部丢完仍超预算：对用户原始输入尾部按字符二分截断（追加「[…已截断]」）；
//  3. 仍超：对全文按字符硬截断，保证返回值 token 数不超过 maxTokens。
func BuildTimelineInput(entries []Entry, maxTokens int) string {
	userInput := noUserInputPlaceholder
	var plans []Entry
	for _, e := range entries {
		if e.Kind == KindUserInput {
			userInput = e.Content
			break // 取第一条 KindUserInput
		}
	}
	for _, e := range entries {
		if e.Kind == KindThoughtPlan {
			plans = append(plans, e)
		}
	}

	if maxTokens <= 0 {
		return buildInput(userInput, plans, 0)
	}

	text := buildInput(userInput, plans, 0)
	if tokenutil.EstimateTokens(text) <= maxTokens {
		return text
	}

	// 第一步：从最旧的 thought_plan 条目开始逐条丢弃
	dropped := 0
	for len(plans) > 0 {
		plans = plans[1:]
		dropped++
		text = buildInput(userInput, plans, dropped)
		if tokenutil.EstimateTokens(text) <= maxTokens {
			return text
		}
	}

	// 第二步：全部丢完仍超预算，截断用户原始输入尾部
	if truncated, ok := truncateUserToFit(userInput, plans, dropped, maxTokens); ok {
		return buildInput(truncated, plans, dropped)
	}

	// 第三步：最终兜底，对全文硬截断
	return hardTruncate(buildInput("", plans, dropped), maxTokens)
}

// BuildFreshInput 基于当前内存快照构建重建输入文本。
func (r *Recorder) BuildFreshInput(maxTokens int) string {
	return BuildTimelineInput(r.Snapshot(), maxTokens)
}
