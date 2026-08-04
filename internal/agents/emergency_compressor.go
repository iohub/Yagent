package agents

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/tokenutil"
)

const (
	// DefaultEmergencyCompressKeepLastN 紧急压缩保留的最后 N 个 Thought & Plan 原始块
	DefaultEmergencyCompressKeepLastN = 3
	// emergencySummaryMaxTokens LLM 总结输出最大 token 数
	emergencySummaryMaxTokens = 2000
	// emergencySummaryInputTokens 送入 LLM 总结的输入最大 token 数（超出先截断）
	emergencySummaryInputTokens = 20000
)

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
}

// ─── extractThoughtAndPlanBlocks ─────────────────────────────────────────────

// extractThoughtAndPlanBlocks 从 assistant 消息的 Content 中提取所有 Thought & Plan 块。
// 关键字匹配大小写不敏感，兼容 "Thought & Plan" 和 "Throught & Plan"（拼写错误）。
// 若内容中无关键字，返回 nil。
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

// findNextBlockStart 在 content[offset:] 中查找下一个 "thought & plan" 或 "throught & plan"（大小写不敏感）的字节偏移。
func findNextBlockStart(content string, offset int) int {
	lower := strings.ToLower(content[offset:])
	idxTP := strings.Index(lower, "thought & plan")
	idxTH := strings.Index(lower, "throught & plan")
	if idxTP >= 0 && idxTH >= 0 {
		if idxTP <= idxTH {
			return offset + idxTP
		}
		return offset + idxTH
	}
	if idxTP >= 0 {
		return offset + idxTP
	}
	if idxTH >= 0 {
		return offset + idxTH
	}
	return -1
}

// ─── summarizeBlocksWithLLM ──────────────────────────────────────────────────

// summarizeBlocksWithLLM 调用 LLM 对多个过程块进行总结。
// 成功返回总结文本和 true；失败返回 "" 和 false。
func summarizeBlocksWithLLM(ctx context.Context, engine llm.Engine, blocks []string, agentName string) (string, bool) {
	blocksText := strings.Join(blocks, "\n---\n")
	// 若 token 超 budget 先截断
	if tokenutil.EstimateTokens(blocksText) > emergencySummaryInputTokens {
		blocksText = truncateToTokenBudget(blocksText, emergencySummaryInputTokens)
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

// ─── EmergencyCompressMessages ───────────────────────────────────────────────

// EmergencyCompressMessages 当 tool 结果已全部截断后仍超限，启动紧急模式：
// 提取用户原始任务 + 总结/保留 Thought & Plan 历史，覆盖为单条 user 消息。
func EmergencyCompressMessages(ctx context.Context, messages []llm.Message, originalInput string, maxTokens int, engine llm.Engine, agentName string, keepLastN int) ([]llm.Message, *EmergencyCompressionStats) {
	originalTokens := estimateMessagesTokens(messages)

	stats := &EmergencyCompressionStats{
		OriginalTokens: originalTokens,
	}

	// 1. 收集所有 assistant 消息中的 T&P 块
	var allBlocks []string
	for _, msg := range messages {
		if msg.Role != llm.RoleAssistant || msg.Content == "" {
			continue
		}
		blocks := extractThoughtAndPlanBlocks(msg.Content)
		if len(blocks) > 0 {
			allBlocks = append(allBlocks, blocks...)
			stats.ExtractedBlocks += len(blocks)
		} else {
			// 保底：整条内容作为一个过程块，保证信息不丢
			allBlocks = append(allBlocks, msg.Content)
			stats.ExtractedBlocks++
		}
	}

	// 2. 若块数 <= keepLastN，全部保留，不调用 LLM
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

		// 3. 调用 LLM 总结 beforeBlocks
		summary, stats.SummarizedByLLM = summarizeBlocksWithLLM(ctx, engine, beforeBlocks, agentName)
		if !stats.SummarizedByLLM {
			// 降级：用 truncateToTokenBudget 截断拼接
			joined := strings.Join(beforeBlocks, "\n---\n")
			summary = truncateToTokenBudget(joined, emergencySummaryMaxTokens)
			slog.Warn("emergency compression: LLM summarize failed, using truncation fallback")
		}
	}

	// 4. 确定原始任务内容
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

	// 5. 组装最终 user 消息内容
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
	sb.WriteString("### 最近思考与计划（保留的原始 Thought & Plan）\n")
	for _, block := range keptBlocks {
		sb.WriteString(block)
		sb.WriteString("\n\n")
	}
	finalUserContent := sb.String()

	// 6. 构造新 messages：保留 messages[0]（system），追加一条 user 消息
	newMessages := make([]llm.Message, 0, 2)
	if len(messages) > 0 {
		newMessages = append(newMessages, messages[0])
	}
	newMessages = append(newMessages, llm.Message{
		Role:    llm.RoleUser,
		Content: finalUserContent,
	})

	// 7. 若压缩后仍超限（极端情况），强制截断 user 消息内容
	// 仅当原始消息已超阈值时才触发强制截断，避免对 Already-under-budget 的消息二次截断
	compressedTokens := estimateMessagesTokens(newMessages)
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
			nonUserTokens += estimateMessagesTokens([]llm.Message{msg})
		}
		userBudget := maxTokens - nonUserTokens
		if userBudget < 0 {
			userBudget = 0
		}
		finalUserContent = truncateToTokenBudget(finalUserContent, userBudget)
		newMessages[len(newMessages)-1].Content = finalUserContent
		stats.CompressedTokens = estimateMessagesTokens(newMessages)
		stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
		if stats.SavedTokens < 0 {
			stats.SavedTokens = 0
		}
	}

	return newMessages, stats
}
