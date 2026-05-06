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
	client SummarizationClient
	config *Config
}

// NewLLMSummarizer 创建LLM摘要器
func NewLLMSummarizer(client SummarizationClient, config *Config) *LLMSummarizer {
	return &LLMSummarizer{
		client: client,
		config: config,
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
			summaryResults[idx] = summary
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
	return priorities[idx].Score
}

// segmentMessages 将摘要区消息按token限制分段
// 每段不超过 SummarizationMaxInputTokens
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

	getApproxTokens := func(content string) int {
		// 粗略估算：约4个字符=1个token
		return len([]rune(content)) / 4
	}

	for _, msg := range messages {
		msgTokens := getApproxTokens(msg.Content)

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
