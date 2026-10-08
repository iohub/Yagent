package compression

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
)

const (
	// DefaultEmergencyCompressKeepLastN 紧急压缩保留的最后 N 个过程块
	// （方案C 批次3 前为 Thought & Plan 原始块，现为每条 assistant 文本消息整体）
	DefaultEmergencyCompressKeepLastN = 3
	// emergencySummaryMaxTokens LLM 总结输出最大 token 数
	emergencySummaryMaxTokens = 2000
	// emergencySummaryInputTokens 送入 LLM 总结的输入最大 token 数（超出先截断）
	emergencySummaryInputTokens = 20000
	// todoWriteToolName TodoWrite 工具名（方案C 批次4 注册的工具，
	// 紧急压缩"钉住"策略按名识别调用对）
	todoWriteToolName = "TodoWrite"
	// legacyTodoWriteNote 被取代的旧 TodoWrite 调用对整体替换成的紧凑注记
	legacyTodoWriteNote = "[旧任务清单已更新，见最新状态]"
)

// thoughtAndPlanPattern 匹配 "Thought & Plan" 标题关键字，忽略大小写与空白差异，最大化兼容各种变体：
// 如 "Thought & Plan"、"THOUGHT&PLAN"、"Thought  &  Plan"、"Thought\n&\nPlan"、
// "Thought＆Plan"（全角＆）、"Thought & Plan"（HTML 实体）、"Thought and Plan" 等。
var thoughtAndPlanPattern = regexp.MustCompile(`(?i)thought\s*(?:&|&|＆|and)\s*plan`)

// EmergencyCompressionStats 记录紧急上下文压缩的整体统计信息。
type EmergencyCompressionStats struct {
	OriginalTokens   int    `json:"original_tokens"`
	CompressedTokens int    `json:"compressed_tokens"`
	SavedTokens      int    `json:"saved_tokens"`
	ExtractedBlocks  int    `json:"extracted_blocks"`
	SummarizedBlocks int    `json:"summarized_blocks"`
	KeptBlocks       int    `json:"kept_blocks"`
	SummarizedByLLM  bool   `json:"summarized_by_llm"`
	Reason           string `json:"reason,omitempty"`
	// 方案C 5.6 TodoWrite 钉住策略统计
	PinnedTodoPairs   int `json:"pinned_todo_pairs"`   // 钉住原样保留的最新 TodoWrite 调用对数（0 或 1）
	ReplacedTodoPairs int `json:"replaced_todo_pairs"` // 被替换为紧凑注记的旧 TodoWrite 调用对数
}

// ─── 旧格式 Thought & Plan 提取（legacy 兜底专用）────────────────────────────

// extractThoughtAndPlanBlocks 从 assistant 消息的 Content 中提取所有 Thought & Plan 块。
// 关键字通过正则匹配：忽略大小写、忽略空白差异，兼容多种变体（全角＆、HTML 实体 &、and 写法）。
// 若内容中无关键字，返回 nil。
//
// Deprecated: 方案C 批次2 已删除 executor/planner 侧的记录链路调用，本函数
// 现仅被 compressor.go 的 collectLegacyThoughtPlanBlocks（5.10-f 兼容期静默
// 兜底）与同包测试使用；批次5 清理时随 legacy 渲染分支一并删除。
func extractThoughtAndPlanBlocks(content string) []string {
	if content == "" {
		return nil
	}
	var blocks []string
	searchFrom := 0
	for {
		start := findNextBlockStart(content, searchFrom)
		if start < 0 {
			break
		}
		// 回退到行首，确保包含 "## " 前缀
		blockStart := start
		for blockStart > 0 && content[blockStart-1] != '\n' {
			blockStart--
		}
		end := findNextBlockStart(content, start+1)
		if end < 0 {
			end = len(content)
		}
		blocks = append(blocks, content[blockStart:end])
		searchFrom = start + 1
	}
	return blocks
}

// findNextBlockStart 在 content[offset:] 中查找下一个 "Thought & Plan"（正则匹配，忽略大小写与空白差异）
func findNextBlockStart(content string, offset int) int {
	loc := thoughtAndPlanPattern.FindStringIndex(content[offset:])
	if loc == nil {
		return -1
	}
	return offset + loc[0]
}

// ─── summarizeBlocksWithLLM ──────────────────────────────────────────────────

// summarizeBlocksWithLLM 调用 LLM 对多个过程块进行总结。
// 成功返回总结文本和 true；失败返回 "" 和 false。
func summarizeBlocksWithLLM(ctx context.Context, engine llm.Engine, blocks []string, agentName string) (string, bool) {
	blocksText := strings.Join(blocks, "\n---\n")
	// 若 token 超 budget 先截断
	if tokenutil.EstimateTokens(blocksText) > emergencySummaryInputTokens {
		blocksText = TruncateToTokenBudget(blocksText, emergencySummaryInputTokens)
	}

	summaryCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	resp, err := engine.GenerateContent(summaryCtx, []llm.Message{
		{
			Role: llm.RoleSystem,
			Content: "你是一个任务执行过程总结器。下面是 Agent 在任务执行过程中的思考与计划记录（Thought & Plan 块）。" +
				"请用简洁的语言总结已完成的执行过程：做了什么决策、调用了哪些工具、取得了什么进展、当前状态如何。" +
				"输出纯文本，不要使用 markdown 标题，控制在 500 字以内。",
		},
		{
			Role:    llm.RoleUser,
			Content: "以下是需要总结的思考与计划记录：\n\n" + blocksText,
		},
	}, nil, &llm.CallOptions{MaxTokens: emergencySummaryMaxTokens})

	if err != nil {
		slog.Warn("emergency compression: LLM summarize failed", "agent", agentName, "error", err)
		return "", false
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].Content == "" {
		slog.Warn("emergency compression: LLM summarize returned empty content")
		return "", false
	}
	return strings.TrimSpace(resp.Choices[0].Content), true
}

// ─── TodoWrite 钉住策略（方案C 5.6）──────────────────────────────────────────

// hasTodoWriteCall 判断 assistant 消息中是否包含 TodoWrite 工具调用。
func hasTodoWriteCall(msg llm.Message) bool {
	for _, tc := range msg.ToolCalls {
		if tc.Function.Name == todoWriteToolName {
			return true
		}
	}
	return false
}

// todoWriteCallRanges 扫描消息序列，定位全部 TodoWrite 调用对的消息范围：
// assistant tool_call 消息（含 TodoWrite 调用）+ 紧随的连续 tool result 消息。
// assistant 消息同时含其他工具调用时，紧随的全部 tool 消息一并纳入范围
// （配对完整性由"assistant tool_calls + 紧随 tool results"整体保证，无法拆分）。
// 返回每个对的范围 [start, end]（闭区间，按出现顺序）。
func todoWriteCallRanges(messages []llm.Message) [][2]int {
	var ranges [][2]int
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != llm.RoleAssistant || !hasTodoWriteCall(msg) {
			continue
		}
		end := i
		for j := i + 1; j < len(messages); j++ {
			if messages[j].Role != llm.RoleTool {
				break
			}
			end = j
		}
		ranges = append(ranges, [2]int{i, end})
		i = end // 跳过已归对的 tool 消息
	}
	return ranges
}

// ─── EmergencyCompressMessages ───────────────────────────────────────────────

// EmergencyCompressMessages 当 tool 结果已全部截断后仍超限，启动紧急模式：
// 提取用户原始任务 + 总结/保留执行过程块，并按"TodoWrite 钉住"策略
// （方案C 5.6）保留最新的 TodoWrite 调用对（原样）、将更早的 TodoWrite 对
// 整体替换为紧凑注记，最终组装为
// [system, ...TodoWrite 调用对/注记（按原顺序）, user 重建消息]。
func EmergencyCompressMessages(ctx context.Context, messages []llm.Message, originalInput string, maxTokens int, engine llm.Engine, agentName string, keepLastN int) ([]llm.Message, *EmergencyCompressionStats) {
	originalTokens := EstimateMessagesTokens(messages)

	stats := &EmergencyCompressionStats{
		OriginalTokens: originalTokens,
	}

	// 1. 识别全部 TodoWrite 调用对（方案C 5.6 钉住策略）：
	//    最新一个对原样保留，更早的对整体替换为紧凑注记
	ranges := todoWriteCallRanges(messages)
	inTodoWritePair := make([]bool, len(messages))
	for _, r := range ranges {
		for k := r[0]; k <= r[1]; k++ {
			inTodoWritePair[k] = true
		}
	}
	if len(ranges) > 0 {
		stats.PinnedTodoPairs = 1
		stats.ReplacedTodoPairs = len(ranges) - 1
	}

	// 2. 收集非 TodoWrite 对的 assistant 文本消息为过程块
	//    （每条整体作为一个块，保证信息不丢；TodoWrite 调用对已被钉住/替换，
	//    不参与块收集与 LLM 总结）
	var allBlocks []string
	for i, msg := range messages {
		if inTodoWritePair[i] || msg.Role != llm.RoleAssistant || msg.Content == "" {
			continue
		}
		allBlocks = append(allBlocks, msg.Content)
		stats.ExtractedBlocks++
	}

	// 3. 若块数 <= keepLastN，全部保留，不调用 LLM
	var summary string
	var beforeBlocks, keptBlocks []string
	if len(allBlocks) <= keepLastN {
		keptBlocks = allBlocks
		stats.KeptBlocks = len(allBlocks)
	} else {
		beforeBlocks = allBlocks[:len(allBlocks)-keepLastN]
		keptBlocks = allBlocks[len(allBlocks)-keepLastN:]
		stats.SummarizedBlocks = len(beforeBlocks)
		stats.KeptBlocks = len(keptBlocks)

		// 4. 调用 LLM 总结 beforeBlocks
		summary, stats.SummarizedByLLM = summarizeBlocksWithLLM(ctx, engine, beforeBlocks, agentName)
		if !stats.SummarizedByLLM {
			// 降级：用 TruncateToTokenBudget 截断拼接
			joined := strings.Join(beforeBlocks, "\n---\n")
			summary = TruncateToTokenBudget(joined, emergencySummaryMaxTokens)
			slog.Warn("emergency compression: LLM summarize failed, using truncation fallback")
		}
	}

	// 5. 确定原始任务内容
	task := originalInput
	if task == "" {
		for _, msg := range messages {
			if msg.Role == llm.RoleUser && msg.Content != "" {
				task = msg.Content
				break
			}
		}
	}
	if task == "" {
		task = "(无原始任务输入)"
	}

	// 6. 组装最终 user 消息内容
	var sb strings.Builder
	sb.WriteString("[紧急上下文压缩：历史上下文因超出 token 上限被极致压缩，请基于以下信息继续任务]\n\n")
	sb.WriteString("### 原始任务\n")
	sb.WriteString(task)
	sb.WriteString("\n\n")
	sb.WriteString("### 执行过程总结（LLM 生成，压缩前历史）\n")
	if summary != "" {
		sb.WriteString(summary)
	} else {
		sb.WriteString("(无)")
	}
	sb.WriteString("\n\n")
	sb.WriteString("### 最近执行记录（保留的原始过程块）\n")
	for _, block := range keptBlocks {
		sb.WriteString(block)
		sb.WriteString("\n\n")
	}
	finalUserContent := sb.String()

	// 7. 构造新 messages：保留 messages[0]（system），追加钉住的 TodoWrite 调用对
	//    （最新对原样保留、更早的对替换为紧凑注记，按原相对顺序排列），最后追加
	//    user 重建消息（保持 user 消息在末尾，与 ApplyEmergency 的 memory 覆盖
	//    逻辑兼容）
	newMessages := make([]llm.Message, 0, 2+len(ranges)*2)
	if len(messages) > 0 {
		newMessages = append(newMessages, messages[0])
	}
	for idx, r := range ranges {
		if idx == len(ranges)-1 {
			// 最新一个 TodoWrite 调用对：原样钉住保留（assistant tool_call +
			// 紧随的全部 tool result 消息，保证 tool_call/tool_response 配对完整）
			for k := r[0]; k <= r[1]; k++ {
				newMessages = append(newMessages, messages[k])
			}
		} else {
			// 被取代的旧对：整体替换为一条紧凑注记（assistant 文本消息）
			newMessages = append(newMessages, llm.Message{
				Role:    llm.RoleAssistant,
				Content: legacyTodoWriteNote,
			})
		}
	}
	newMessages = append(newMessages, llm.Message{
		Role:    llm.RoleUser,
		Content: finalUserContent,
	})

	// 8. 若压缩后仍超限（极端情况），强制截断 user 消息内容
	// 仅当原始消息已超阈值时才触发强制截断，避免对 Already-under-budget 的消息二次截断
	compressedTokens := EstimateMessagesTokens(newMessages)
	stats.CompressedTokens = compressedTokens
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if compressedTokens > maxTokens && stats.OriginalTokens > maxTokens {
		stats.Reason = "forced truncation after emergency compression"
		// 扣除非用户消息（如 system）的 token 预算，确保截断后整体不超限
		nonUserTokens := 0
		for i, msg := range newMessages {
			if i == len(newMessages)-1 {
				break // 跳过 user 消息
			}
			nonUserTokens += EstimateMessagesTokens([]llm.Message{msg})
		}
		userBudget := maxTokens - nonUserTokens
		if userBudget < 0 {
			userBudget = 0
		}
		finalUserContent = TruncateToTokenBudget(finalUserContent, userBudget)
		newMessages[len(newMessages)-1].Content = finalUserContent
		stats.CompressedTokens = EstimateMessagesTokens(newMessages)
		stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
		if stats.SavedTokens < 0 {
			stats.SavedTokens = 0
		}
	}

	return newMessages, stats
}
