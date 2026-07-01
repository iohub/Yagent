package compact

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"codeactor/internal/llm"
)

// Config 压缩配置（精简版）
type Config struct {
	// MaxContextTokens 最大上下文token数，默认 128000
	MaxContextTokens int `toml:"max_context_tokens"`

	// EnableAutoCompact 是否自动触发压缩
	EnableAutoCompact bool `toml:"enable_auto_compact"`

	// KeepRecentRounds 始终保留的最近对话轮数（默认 3）
	KeepRecentRounds int `toml:"keep_recent_rounds"`

	// SummarizationTimeout 摘要超时时间（默认 60s）
	SummarizationTimeout time.Duration `toml:"summarization_timeout"`

	// SummarizationMaxInputTokens 摘要时单批次最大输入token数（默认 100000）
	SummarizationMaxInputTokens int `toml:"summarization_max_input_tokens"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	MaxContextTokens:            128000,
	EnableAutoCompact:           true,
	KeepRecentRounds:            3,
	SummarizationTimeout:        60 * time.Second,
	SummarizationMaxInputTokens: 100000,
}

// CompressResult 压缩结果
type CompressResult struct {
	CompressedMessages []llm.Message
	OriginalTokens     int
	CompressedTokens   int
	CompressionRatio   float64 // 压缩比 (0~1)，越小压缩越多
	CompressionStats   string  // 压缩统计信息
	SummaryInfo        string  // 摘要文本（可选）
}

// SummarizationClient 摘要LLM客户端接口
type SummarizationClient interface {
	GenerateSummary(ctx context.Context, messages []llm.Message) (string, error)
}

// Engine 压缩引擎（全量重新压缩）
type Engine struct {
	config        *Config
	tokenizer     Tokenizer
	summarizer    SummarizationClient
	frozenSummary string // 已冻结的历史摘要，用于增量压缩
}

// NewEngine 创建压缩引擎
func NewEngine(config *Config, summarizer SummarizationClient) (*Engine, error) {
	if config == nil {
		config = &DefaultConfig
	}
	if config.MaxContextTokens <= 0 {
		config.MaxContextTokens = DefaultConfig.MaxContextTokens
	}
	if config.KeepRecentRounds <= 0 {
		config.KeepRecentRounds = DefaultConfig.KeepRecentRounds
	}
	if config.SummarizationTimeout <= 0 {
		config.SummarizationTimeout = DefaultConfig.SummarizationTimeout
	}
	if config.SummarizationMaxInputTokens <= 0 {
		config.SummarizationMaxInputTokens = DefaultConfig.SummarizationMaxInputTokens
	}

	return &Engine{
		config:     config,
		tokenizer:  GetGlobalTokenizer(),
		summarizer: summarizer,
	}, nil
}

// Compress 执行全量重新压缩
// 核心逻辑：
// 1. 计算 token
// 2. 如果未超限，直接返回
// 3. 如果没有 summarizer，返回原始消息
// 4. 分区：用 extractRecentMessages 将消息分为 older 和 recent（含 Tool-Call Atomicity）
// 5. 对 older 调用 summarizer.Summarize()
// 6. 用 buildCacheAwareMessages 构建 [System] + [Summary] + [Recent] 布局
// 7. 返回 CompressResult
func (e *Engine) Compress(ctx context.Context, messages []llm.Message) (*CompressResult, error) {
	if len(messages) == 0 {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     0,
			CompressedTokens:   0,
		}, nil
	}

	// 1. 计算原始 token 数
	originalTokens, err := e.CountTokens(messages)
	if err != nil {
		return nil, fmt.Errorf("failed to count tokens: %w", err)
	}

	// 2. 未超限直接返回
	if originalTokens <= e.config.MaxContextTokens {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No compression needed",
		}, nil
	}

	// 3. 如果没有摘要器，降级为仅返回 recent 消息
	if e.summarizer == nil {
		slog.Warn("Context compression triggered but no summarizer available")
		recent, _ := extractRecentMessages(messages, e.config.KeepRecentRounds)
		return &CompressResult{
			CompressedMessages: recent,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No summarizer available, returning recent messages only",
			SummaryInfo:        "",
		}, nil
	}

	slog.Info("Context compression triggered (full re-compress)",
		"original_tokens", originalTokens,
		"max_tokens", e.config.MaxContextTokens)

	// 4. 分区：recent（保留） + older（待压缩）
	recent, older := extractRecentMessages(messages, e.config.KeepRecentRounds)

	// 如果 older 为空，说明不需要压缩
	if len(older) == 0 {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No compressible messages",
		}, nil
	}

	// 5. 对 older 做 LLM 摘要（支持增量压缩）
	ctx, cancel := context.WithTimeout(ctx, e.config.SummarizationTimeout)
	defer cancel()

	// 增量压缩：如果已有 frozenSummary
	var summary string
	if e.frozenSummary != "" && len(older) > 0 {
		s, err := e.incrementalCompress(ctx, older)
		if err != nil {
			slog.Warn("Incremental compression failed, falling back to full", "error", err)
			summary, err = e.summarizer.GenerateSummary(ctx, older)
			if err != nil {
				slog.Warn("LLM summarization failed, falling back to recent only", "error", err)
				return &CompressResult{
					CompressedMessages: recent,
					OriginalTokens:     originalTokens,
					CompressedTokens:   e.countMessagesTokens(recent),
					CompressionRatio:   float64(e.countMessagesTokens(recent)) / float64(originalTokens),
					CompressionStats:   fmt.Sprintf("Summarization failed: %v", err),
					SummaryInfo:        "",
				}, nil
			}
		} else {
			summary = s
		}
	} else {
		var err error
		summary, err = e.summarizer.GenerateSummary(ctx, older)
		if err != nil {
			slog.Warn("LLM summarization failed, falling back to recent only", "error", err)
			return &CompressResult{
				CompressedMessages: recent,
				OriginalTokens:     originalTokens,
				CompressedTokens:   e.countMessagesTokens(recent),
				CompressionRatio:   float64(e.countMessagesTokens(recent)) / float64(originalTokens),
				CompressionStats:   fmt.Sprintf("Summarization failed: %v", err),
				SummaryInfo:        "",
			}, nil
		}
	}

	// 保存 frozenSummary
	e.frozenSummary = summary

	// 6. 构建缓存友好的消息布局：[System] + [Summary] + [Recent]
	compressed := buildCacheAwareMessages(messages, summary, e.config.KeepRecentRounds)

	// 7. 计算压缩后 token 数和比率
	compressedTokens := e.countMessagesTokens(compressed)
	compressionRatio := float64(compressedTokens) / float64(originalTokens)

	stats := fmt.Sprintf("Full re-compress | Original: %d tokens | Compressed: %d tokens | Ratio: %.2f | Recent rounds: %d",
		originalTokens, compressedTokens, compressionRatio, e.config.KeepRecentRounds)

	return &CompressResult{
		CompressedMessages: compressed,
		OriginalTokens:     originalTokens,
		CompressedTokens:   compressedTokens,
		CompressionRatio:   compressionRatio,
		CompressionStats:   stats,
		SummaryInfo:        summary,
	}, nil
}

// CountTokens 计算 messages 的总 token 数
func (e *Engine) CountTokens(messages []llm.Message) (int, error) {
	return e.tokenizer.CountMessagesTokens(messages)
}

// countMessagesTokens 内部计数，不返回错误（降级估算）
func (e *Engine) countMessagesTokens(messages []llm.Message) int {
	tokens, err := e.tokenizer.CountMessagesTokens(messages)
	if err != nil {
		// 降级估算：约4个字符=1个token
		total := 0
		for _, msg := range messages {
			total += len([]rune(msg.Content)) / 4
		}
		return total
	}
	return tokens
}

// ─────────────────────────────────────────────────────────
// extractRecentMessages — 消息分区（含 Tool-Call Atomicity Repair）
// ─────────────────────────────────────────────────────────

// extractRecentMessages splits messages into (recent, older) based on keepRounds.
// Ensures tool_call/tool_response pairs are never split across the boundary.
func extractRecentMessages(messages []llm.Message, keepRounds int) (recent, older []llm.Message) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 从后往前数 keepRounds*2 条消息作为保留区（每轮 user+assistant 约2条）
	keepCount := keepRounds * 2
	if keepCount <= 0 {
		keepCount = 6 // 默认保留3轮
	}
	if keepCount > len(messages) {
		keepCount = len(messages)
	}

	recentStart := len(messages) - keepCount

	// ─── Tool-Call Atomicity Repair ───
	// Ensure the boundary does not split tool_call/tool_response pairs.
	if recentStart > 0 {
		toolCallIDsInRecent := make(map[string]bool)
		for i := recentStart; i < len(messages); i++ {
			msg := messages[i]
			if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
				toolCallIDsInRecent[msg.ToolCallID] = true
			}
		}
		if len(toolCallIDsInRecent) > 0 {
			for i := recentStart - 1; i >= 0; i-- {
				msg := messages[i]
				if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
					needsAdjustment := false
					for _, tc := range msg.ToolCalls {
						if toolCallIDsInRecent[tc.ID] {
							needsAdjustment = true
							break
						}
					}
					if needsAdjustment {
						recentStart = i
						break
					}
				}
				if msg.Role == llm.RoleUser {
					break
				}
			}
		}
	}

	// 保留区：最后 keepRounds 轮的消息
	recent = make([]llm.Message, keepCount)
	copy(recent, messages[recentStart:])

	// 待压缩区：保留区之前且非 System 的消息
	older = make([]llm.Message, 0, recentStart)
	for i := 0; i < recentStart; i++ {
		msg := messages[i]
		// 跳过 System 消息（它们不应再被压缩）
		if msg.Role == llm.RoleSystem {
			continue
		}
		older = append(older, msg)
	}

	// 锚定消息保护：将 older 中的锚定消息移到 recent
	var anchoredMsgs []llm.Message
	var nonAnchored []llm.Message
	for _, msg := range older {
		if msg.IsAnchored {
			anchoredMsgs = append(anchoredMsgs, msg)
		} else {
			nonAnchored = append(nonAnchored, msg)
		}
	}
	older = nonAnchored
	// 锚定消息插入到 recent 最前面
	recent = append(anchoredMsgs, recent...)

	return recent, older
}

// ─────────────────────────────────────────────────────────
// buildCacheAwareMessages — 缓存友好布局
// ─────────────────────────────────────────────────────────

// buildCacheAwareMessages 构建缓存友好的消息布局
// 布局顺序：[System] + [Summary] + [Recent]
func buildCacheAwareMessages(messages []llm.Message, summary string, keepRounds int) []llm.Message {
	result := make([]llm.Message, 0, 3+len(messages))

	// 1. 原始 System 消息（绝对稳定前缀）
	systemMsg := extractSystemMessage(messages)
	if systemMsg != nil {
		result = append(result, *systemMsg)
	}

	// 2. 摘要消息
	if summary != "" {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: "[CONTEXT SUMMARY]\n" + CleanSummaryOutput(summary),
		})
	}

	// 3. 保留区消息（recent）
	recent, _ := extractRecentMessages(messages, keepRounds)
	result = append(result, recent...)

	return result
}

// extractSystemMessage 从消息列表中提取第一条 System 消息
func extractSystemMessage(messages []llm.Message) *llm.Message {
	for _, msg := range messages {
		if msg.Role == llm.RoleSystem {
			return &msg
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────
// CleanSummaryOutput — 清洗 LLM 摘要输出
// ─────────────────────────────────────────────────────────

// CleanSummaryOutput 清洗 LLM 摘要输出
func CleanSummaryOutput(raw string) string {
	if raw == "" {
		return ""
	}

	cleaned := raw

	cleaned = removeMarkdownFence(cleaned)
	cleaned = removeDuplicateLines(cleaned)
	cleaned = compactWhitespace(cleaned)

	return strings.TrimSpace(cleaned)
}

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
				continue
			}
		} else {
			repeatCount = 1
		}
		result = append(result, lines[i-1])
	}
	result = append(result, lines[len(lines)-1])

	return strings.Join(result, "\n")
}

func compactWhitespace(text string) string {
	re := regexp.MustCompile(`\n{3,}`)
	text = re.ReplaceAllString(text, "\n\n")
	re = regexp.MustCompile(`[ \t]+$`)
	text = re.ReplaceAllString(text, "")
	return text
}

// incrementalCompress 执行增量压缩
// 从 older 中提取已有的 [CONTEXT SUMMARY] 作为基准，仅压缩后续的新消息
func (e *Engine) incrementalCompress(ctx context.Context, older []llm.Message) (string, error) {
	// 1. 查找已有的 [CONTEXT SUMMARY] 消息
	existingSummary := ""
	newStartIdx := 0
	for i, msg := range older {
		if msg.Role == llm.RoleSystem && strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			// 提取摘要文本（去掉前缀）
			raw := msg.Content
			raw = strings.TrimPrefix(raw, "[CONTEXT SUMMARY]")
			raw = strings.TrimPrefix(raw, "\n")
			existingSummary = raw
			newStartIdx = i + 1
			break
		}
	}

	// 2. 如果没有找到已有摘要（理论上不会发生，因为 frozenSummary != ""）
	if existingSummary == "" {
		return e.summarizer.GenerateSummary(ctx, older)
	}

	// 3. 提取新增消息
	newMsgs := older[newStartIdx:]
	if len(newMsgs) == 0 {
		// 没有新消息，直接返回已有摘要
		return e.frozenSummary, nil
	}

	// 4. 构建增量压缩输入
	var sb strings.Builder
	sb.WriteString("[EXISTING SUMMARY]\n")
	sb.WriteString(existingSummary)
	sb.WriteString("\n\n[NEW MESSAGES TO INCORPORATE]\n")
	for _, msg := range newMsgs {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Role, msg.Content))
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(fmt.Sprintf("  → ToolCall: %s(%s)\n", tc.Function.Name, tc.Function.Arguments))
			}
		}
	}

	userContent := sb.String()

	// 5. 调用 LLM 做增量摘要（复用现有接口）
	// 将已有摘要 + 新消息打包成一条 User 消息
	incMsgs := []llm.Message{
		{Role: llm.RoleUser, Content: userContent},
	}
	return e.summarizer.GenerateSummary(ctx, incMsgs)
}
