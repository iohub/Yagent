package compact

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"codeactor/internal/llm"
)

// defaultSummarizationPrompt 默认摘要提示词（英文版本，与 agent prompts 风格一致）
const defaultSummarizationPrompt = `# Role
You are a **Conversation Summarizer** for an AI-powered coding assistant system. Your task is to compress conversation history without losing any critical context needed for ongoing development work.

# Task
Extract the following from the provided conversation fragment:

1. **Task Progress**: What tasks have been completed? What is currently in progress?
2. **Key Decisions**: What important architectural or design decisions were made? Why?
3. **Code Changes**: Which files were modified? What are the key code patterns introduced?
4. **Errors & Fixes**: What problems were encountered? How were they resolved?
5. **Critical Discoveries**: Important facts about the codebase — file structure, dependencies, tech stack, conventions, etc.

# Rules
- **Preserve Identifiers**: Retain ALL specific identifiers — file names, function names, class names, variable names, paths.
- **Preserve Error Details**: Keep concrete error messages and their corresponding fix strategies verbatim.
- **Ignore Redundancy**: Skip duplicated tool output content; keep only the meaningful results.
- **Be Complete**: Do NOT omit any context that could be useful for continuing the work.
- **Be Concise**: Summarize efficiently; prefer bullet points over verbose prose.

# Output Format
- Use clear, structured Markdown.
- Output in **English**.
- Organize extracted information under the 5 categories listed above.`

// SummarizationClient 摘要LLM客户端接口（已在compact_types.go中定义）

// LLMSummarizer LLM驱动的上下文摘要器
type LLMSummarizer struct {
	client        SummarizationClient
	config        *Config
	degradation   *DegradationResolver
}

// NewLLMSummarizer 创建LLM摘要器
func NewLLMSummarizer(client SummarizationClient, config *Config) *LLMSummarizer {
	return &LLMSummarizer{
		client:      client,
		config:      config,
		degradation: NewDegradationResolver(DefaultDegradationConfig, config),
	}
}

// Summarize 对消息列表中的可压缩部分做LLM摘要
// 输入: 完整消息列表 + 优先级信息
// 输出: 替换方案 — 哪些消息被替换为摘要System消息
func (s *LLMSummarizer) Summarize(
	ctx context.Context,
	messages []llm.Message,
	priorities []MessagePriority,
) ([]llm.Message, error) {
	if s.client == nil {
		return messages, nil
	}

	// 1. 分区：按优先级将消息分为保留区、摘要区
	keepRegion := make([]llm.Message, 0)
	summaryRegion := make([]llm.Message, 0)

	for i, p := range priorities {
		msg := messages[i]

		// 已摘要消息（Content 以 [CONTEXT SUMMARY] 开头）强制保留，跳过二次压缩
		if p.IsSummary {
			keepRegion = append(keepRegion, msg)
			continue
		}

		// 始终保留的消息
		if p.IsSystem || p.IsUser || p.IsRecent {
			keepRegion = append(keepRegion, msg)
			continue
		}

		// 早期对话轻微保留（保留第一条和最后一条作为上下文锚点）
		if p.IsEarly {
			if i == 0 || i == len(messages)/3-1 {
				keepRegion = append(keepRegion, msg)
				continue
			}
		}

		// 其余消息进入摘要区
		summaryRegion = append(summaryRegion, msg)
	}

	// ─── Tool-Call Atomicity Repair ───
	// Ensure tool_call/tool_response pairs are never split between keep and summary regions.

	// Build a set of tool_call_ids referenced by assistant messages in keepRegion.
	keepToolCallIDs := make(map[string]bool)
	for _, msg := range keepRegion {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				keepToolCallIDs[tc.ID] = true
			}
		}
	}

	// If any tool_call_id from keepRegion has its tool response in summaryRegion,
	// move that tool response to keepRegion.
	if len(keepToolCallIDs) > 0 {
		var summaryToMove []llm.Message
		var filteredSummary []llm.Message
		for _, msg := range summaryRegion {
			if msg.Role == llm.RoleTool && msg.ToolCallID != "" && keepToolCallIDs[msg.ToolCallID] {
				summaryToMove = append(summaryToMove, msg)
			} else {
				filteredSummary = append(filteredSummary, msg)
			}
		}
		if len(summaryToMove) > 0 {
			keepRegion = append(keepRegion, summaryToMove...)
			summaryRegion = filteredSummary
			slog.Debug("Tool-call atomicity repair: moved tool messages from summary to keep",
				"moved_count", len(summaryToMove))
		}
	}

	// If any tool message in keepRegion references a tool_call_id whose
	// assistant message is NOT in keepRegion, move that tool message to summaryRegion.
	var keepToMove []llm.Message
	var filteredKeep []llm.Message
	for _, msg := range keepRegion {
		if msg.Role == llm.RoleTool && msg.ToolCallID != "" && !keepToolCallIDs[msg.ToolCallID] {
			keepToMove = append(keepToMove, msg)
		} else {
			filteredKeep = append(filteredKeep, msg)
		}
	}
	if len(keepToMove) > 0 {
		keepRegion = filteredKeep
		summaryRegion = append(summaryRegion, keepToMove...)
		slog.Debug("Tool-call atomicity repair: moved orphaned tool messages from keep to summary",
			"moved_count", len(keepToMove))
	}

	// 如果没有可摘要的消息，直接返回原始消息
	if len(summaryRegion) == 0 {
		slog.Debug("LLM summarizer: no messages to summarize")
		return messages, nil
	}

	slog.Info("LLM summarizer: summarizing messages",
		"total_messages", len(messages),
		"keep_region", len(keepRegion),
		"summary_region", len(summaryRegion))

	// 2. 分段：将摘要区消息按token限制分为多个批次
	batches := s.segmentMessages(summaryRegion)

	// 3. 并发摘要：对每个批次调用LLM
	summaryResults := make([]string, len(batches))
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for i, batch := range batches {
		wg.Add(1)
		go func(idx int, batchMsgs []llm.Message) {
			defer wg.Done()

			// 创建带超时的上下文
			sumCtx, cancel := context.WithTimeout(ctx, s.config.SummarizationTimeout)
			defer cancel()

			summary, err := s.client.GenerateSummary(sumCtx, batchMsgs)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("batch %d summarization failed: %w", idx, err)
				}
				errMu.Unlock()
				return
			}
			cleaned := CleanSummaryOutput(summary)
			summaryResults[idx] = cleaned
		}(i, batch)
	}

	wg.Wait()

	if firstErr != nil {
		slog.Warn("LLM summarization partially failed", "error", firstErr)
		// 部分失败：使用非空的摘要结果
		var validSummaries []string
		for _, s := range summaryResults {
			if s != "" {
				validSummaries = append(validSummaries, s)
			}
		}
		if len(validSummaries) == 0 {
			return messages, fmt.Errorf("all summarization batches failed")
		}
		summaryResults = validSummaries
	}

	// 4. 合并：将所有摘要合并为一条System消息
	summaryPrompt := s.config.SummarizationPrompt
	if summaryPrompt == "" {
		summaryPrompt = defaultSummarizationPrompt
	}

	var fullSummary strings.Builder
	fullSummary.WriteString(summaryPrompt + "\n\n---对话摘要---\n\n")
	for i, summary := range summaryResults {
		fullSummary.WriteString(fmt.Sprintf("## 摘要段 %d\n%s\n\n", i+1, summary))
	}

	// 5. 构建结果：[原始System消息] + [摘要System消息] + [保留区消息]
	result := s.buildResult(messages, keepRegion, fullSummary.String())

	slog.Info("LLM summarization completed",
		"original_messages", len(messages),
		"result_messages", len(result),
		"summaries_generated", len(summaryResults))

	return result, nil
}

// SummarizeSegment 对一批增量消息做 LLM 摘要（用于增量压缩）
// 输入：一批新消息（待摘要）
// 输出：摘要文本、摘要 token 数、错误
func (s *LLMSummarizer) SummarizeSegment(
	ctx context.Context,
	messages []llm.Message,
) (string, int, error) {
	if s.client == nil || len(messages) == 0 {
		return "", 0, nil
	}

	// 分段
	batches := s.segmentMessages(messages)
	if len(batches) == 0 {
		return "", 0, nil
	}

	// 使用信号量控制并发
	sem := make(chan struct{}, s.config.MaxConcurrentSummaries)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var summaries []string
	var firstErr error

	for _, batch := range batches {
		sem <- struct{}{} // 获取信号量
		wg.Add(1)

		go func(batchMsgs []llm.Message) {
			defer wg.Done()
			defer func() { <-sem }() // 释放信号量

			// 使用 DegradationResolver 包装 LLM 调用
			batchCopy := batchMsgs // 确保闭包安全
			result := s.degradation.ExecuteWithDegradation(
				ctx,
				"summarize_segment",
				// 主要的 LLM 调用
				func(innerCtx context.Context) (string, error) {
					sumCtx, cancel := context.WithTimeout(innerCtx, s.config.SummarizationTimeout)
					defer cancel()

					summary, err := s.client.GenerateSummary(sumCtx, batchCopy)
					if err != nil {
						return "", err
					}
					cleaned := CleanSummaryOutput(summary)
					if cleaned == "" {
						return "", fmt.Errorf("summarization returned empty after cleaning")
					}
					return cleaned, nil
				},
				// 降级回退
				func() string {
					// 从 batch 中提取关键信息做简单摘要
					return extractBatchFallback(batchCopy)
				},
			)

			mu.Lock()
			if result.Err != nil {
				if firstErr == nil {
					firstErr = result.Err
				}
			} else if result.Result != "" {
				summaries = append(summaries, result.Result)
				// 记录降级日志
				if result.Tier.IsFallback() {
					slog.Warn("Summarization degraded",
						"tier", result.Tier,
						"batch_size", len(batchCopy))
				}
			}
			mu.Unlock()
		}(batch)
	}

	wg.Wait()

	if firstErr != nil && len(summaries) == 0 {
		return "", 0, firstErr
	}

	// 合并多段摘要
	var sb strings.Builder
	sb.WriteString("[CONTEXT SUMMARY]\n")
	for i, summary := range summaries {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(summary)
	}

	finalSummary := sb.String()

	// 用精确的 tiktoken 计数
	tokenCount, err := s.countTokens(finalSummary)
	if err != nil {
		tokenCount = len([]rune(finalSummary)) / 4 // 降级估算
	}

	return finalSummary, tokenCount, nil
}

// calculateThreshold 计算优先级阈值
// 取所有消息优先级的中位数作为分界线
func (s *LLMSummarizer) calculateThreshold(priorities []MessagePriority) float64 {
	if len(priorities) == 0 {
		return 5.0
	}
	// 简单取前70%分数作为阈值
	idx := len(priorities) * 7 / 10
	if idx >= len(priorities) {
		idx = len(priorities) - 1
	}
	return priorities[idx].Priority
}

// segmentMessages 将摘要区消息按token限制分段
// 每段不超过 SummarizationMaxInputTokens
// 使用精确的 tiktoken 计数替代估算
func (s *LLMSummarizer) segmentMessages(messages []llm.Message) [][]llm.Message {
	if len(messages) == 0 {
		return nil
	}

	maxTokens := s.config.SummarizationMaxInputTokens
	if maxTokens <= 0 {
		maxTokens = 8000 // 默认值
	}

	var batches [][]llm.Message
	var currentBatch []llm.Message
	var currentTokens int

	for _, msg := range messages {
		// 使用精确的 tiktoken 计数
		msgTokens, err := s.countTokens(msg.Content)
		if err != nil {
			// 降级估算：约4个字符=1个token
			msgTokens = len([]rune(msg.Content)) / 4
		}

		// 单条消息就超限，强制拆分为一段
		if msgTokens > maxTokens && len(currentBatch) == 0 {
			// 直接加入当前批次，让后续逻辑处理
			currentBatch = append(currentBatch, msg)
			currentTokens = msgTokens
			continue
		}

		// 当前批次加上这条消息会超限
		if currentTokens+msgTokens > maxTokens && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
			currentBatch = []llm.Message{msg}
			currentTokens = msgTokens
		} else {
			currentBatch = append(currentBatch, msg)
			currentTokens += msgTokens
		}
	}

	// 添加最后一个批次
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	// 如果没有批次（空消息），返回nil
	if len(batches) == 0 {
		return nil
	}

	return batches
}

// buildResult 构建压缩后的消息列表
// 规则：[原始System消息] + [摘要System消息] + [保留区消息]
func (s *LLMSummarizer) buildResult(
	originalMessages []llm.Message,
	keepRegion []llm.Message,
	summary string,
) []llm.Message {
	result := make([]llm.Message, 0, len(keepRegion)+2)

	// 始终保留原始System消息（如果存在）
	if len(originalMessages) > 0 && originalMessages[0].Role == llm.RoleSystem {
		result = append(result, originalMessages[0])
	}

	// 添加摘要消息（作为System消息）
	if summary != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: "[CONTEXT SUMMARY]\n" + summary,
		})
	}

	// 添加保留区消息
	result = append(result, keepRegion...)

	return result
}

// countTokens 使用 tiktoken 精确计数
func (s *LLMSummarizer) countTokens(content string) (int, error) {
	return GetGlobalTokenizer().CountTokens(content)
}

// MergeTwoSummaries 用 LLM 合并两个摘要块
// 用于摘要栈管理：当栈过深时合并底层摘要
func (s *LLMSummarizer) MergeTwoSummaries(
	ctx context.Context,
	summaryA, summaryB string,
) (string, int, error) {
	if s.client == nil {
		return "", 0, fmt.Errorf("no summarization client available")
	}

	mergePrompt := `Merge the following two conversation summaries into a single consolidated summary. 
Preserve all important technical details, decisions, and constraints.
Output in the same structured format as the originals.

Summary A:
` + summaryA + `

Summary B:
` + summaryB + `

Consolidated Summary:`

	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: mergePrompt},
	}

	// 使用 DegradationResolver
	result := s.degradation.ExecuteWithDegradation(
		ctx,
		"merge_summaries",
		func(innerCtx context.Context) (string, error) {
			sumCtx, cancel := context.WithTimeout(innerCtx, s.config.SummarizationTimeout)
			defer cancel()

			merged, err := s.client.GenerateSummary(sumCtx, msgs)
			if err != nil {
				return "", err
			}
			cleaned := CleanSummaryOutput(merged)
			if cleaned == "" {
				return "", fmt.Errorf("merge returned empty after cleaning")
			}
			return cleaned, nil
		},
		// 降级回退：简单拼接
		func() string {
			return "[CONTEXT SUMMARY] (concatenated fallback)\n\n" +
				summaryA + "\n\n---\n\n" + summaryB
		},
	)

	if result.Err != nil {
		return "", 0, result.Err
	}

	tokenCount, _ := s.countTokens(result.Result)

	if result.Tier.IsFallback() {
		slog.Warn("Summary merge degraded",
			"tier", result.Tier,
			"tokens", tokenCount)
	}

	return result.Result, tokenCount, nil
}

// extractBatchFallback 从一批消息中提取关键信息的简单摘要
// 用作 LLM 摘要调用失败的保底降级策略
func extractBatchFallback(messages []llm.Message) string {
	if len(messages) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[CONTEXT SUMMARY] (auto-extracted)\n\n")

	userMsgs := 0
	assistantMsgs := 0
	toolMsgs := 0
	totalLen := 0

	for _, msg := range messages {
		totalLen += len(msg.Content)
		switch msg.Role {
		case llm.RoleUser:
			userMsgs++
		case llm.RoleAssistant:
			assistantMsgs++
		case llm.RoleTool:
			toolMsgs++
		}
	}

	sb.WriteString(fmt.Sprintf("This segment contains %d messages:\n", len(messages)))
	sb.WriteString(fmt.Sprintf("- User messages: %d\n", userMsgs))
	sb.WriteString(fmt.Sprintf("- Assistant messages: %d\n", assistantMsgs))
	sb.WriteString(fmt.Sprintf("- Tool result messages: %d\n", toolMsgs))
	sb.WriteString(fmt.Sprintf("- Total content length: %d characters\n", totalLen))

	// 提取关键信息：包含 first/last user message
	for _, msg := range messages {
		if msg.Role == llm.RoleUser {
			content := msg.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			sb.WriteString(fmt.Sprintf("\nKey user message: %s", content))
			break
		}
	}

	return sb.String()
}

// ─────────────────────────────────────────────────────────
// 适配器：将 llm.Engine 适配为 SummarizationClient
// ─────────────────────────────────────────────────────────

// SummaryAdapter 将 llm.Engine 适配为 SummarizationClient
type SummaryAdapter struct {
	LLM         llm.Engine
	Model       string
	Temperature float64
	MaxTokens   int
}

// GenerateSummary 实现 SummarizationClient 接口
func (a *SummaryAdapter) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	// 构造摘要请求
	systemMsg := llm.Message{
		Role:    llm.RoleSystem,
		Content: defaultSummarizationPrompt,
	}
	allMessages := append([]llm.Message{systemMsg}, messages...)

	opts := &llm.CallOptions{
		MaxTokens:   a.MaxTokens,
		Temperature: a.Temperature,
	}

	resp, err := a.LLM.GenerateContent(ctx, allMessages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarization returned empty response")
	}

	return resp.Choices[0].Content, nil
}
