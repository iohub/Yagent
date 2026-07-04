package compact

import (
	"fmt"
	"log/slog"
	"strings"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// 工具结果预算控制 (Tool Result Budget Control)
// ─────────────────────────────────────────────────────────
// 对单条工具输出设置 token 上限，超限后：
//   1. 二进制内容 → 替换为文本占位符
//   2. 文本内容 → 提取预览 + 完整内容外存存储
//   3. offload 不可用 → 降级为 head/tail 截断

// ToolBudgetResult 工具预算控制结果
type ToolBudgetResult struct {
	// CappedCount 被预算控制的工具消息数量（超过 token 上限）
	CappedCount int
	// Offloaded 成功外存存储的数量
	Offloaded int
	// Truncated 降级截断的数量（offload 不可用时）
	Truncated int
	// TokensSaved 估算节省的 token 数
	TokensSaved int
}

// String 返回 ToolBudgetResult 的可读字符串
func (t *ToolBudgetResult) String() string {
	parts := []string{
		fmt.Sprintf("capped=%d", t.CappedCount),
		fmt.Sprintf("offloaded=%d", t.Offloaded),
		fmt.Sprintf("truncated=%d", t.Truncated),
		fmt.Sprintf("tokens_saved=%d", t.TokensSaved),
	}
	return fmt.Sprintf("ToolBudgetResult{%s}", strings.Join(parts, ", "))
}

// applyToolBudget 对消息列表中的所有 Tool 消息应用预算控制
//
// 流程：
//   1. 遍历所有 RoleTool 消息
//   2. 计算消息 token 数，若超过 MaxToolOutputTokens → 触发预算控制
//   3. 检测是否为二进制内容：
//      - 是 → 替换为 "[Binary content — N bytes]"
//      - 否 → 提取预览，完整内容外存存储
//   4. 替换消息内容为预览+引用ID
//   5. offload 不可用时，降级为 head/tail 截断
//
// 参数：
//   - messages: 待处理的消息列表（会被就地修改）
//
// 返回：
//   - *ToolBudgetResult: 预算控制结果统计
//   - []llm.Message: 修改后的消息列表（与输入相同引用）
//   - error: 错误信息
func (e *Engine) applyToolBudget(messages []llm.Message) (*ToolBudgetResult, []llm.Message, error) {
	if messages == nil {
		return &ToolBudgetResult{}, nil, nil
	}

	// 未配置 token 上限 → 跳过预算控制
	if e.config.MaxToolOutputTokens <= 0 {
		return &ToolBudgetResult{}, messages, nil
	}

	result := &ToolBudgetResult{}

	for i, msg := range messages {
		if msg.Role != llm.RoleTool {
			continue
		}

		// 计算当前消息的 token 数
		tokens, err := e.tokenizer.CountTokens(msg.Content)
		if err != nil {
			// token 计数失败 → 跳过该消息
			slog.Warn("applyToolBudget: token count failed", "tool", msg.ToolName, "error", err)
			continue
		}

		// 未超过上限 → 跳过
		if tokens <= e.config.MaxToolOutputTokens {
			continue
		}

		// 超过上限 → 应用预算控制
		result.CappedCount++
		content := msg.Content

		// 检测是否为二进制内容
		if e.isBinaryContent(content) {
			// 二进制 → 替换为占位符
			preview := fmt.Sprintf("[Binary content — %d bytes]", len(content))
			tokensSaved := tokens - e.estimateTokenCount(preview)
			result.TokensSaved += tokensSaved

			messages[i].Content = preview
			messages[i].TruncationMarker = &llm.TruncationMarker{
				ToolName:       msg.ToolName,
				OriginalLen:    len(content),
				OmittedLen:     len(content) - len(preview),
				TruncationPass: 0,
			}
			continue
		}

		// 文本内容 → 尝试外存存储
		if e.offload != nil {
			meta, offloadErr := e.offToolResult(&messages[i], content)
			if offloadErr != nil {
				slog.Warn("applyToolBudget: offload failed, falling back to truncation",
					"tool", msg.ToolName, "error", offloadErr)
			} else if meta != nil {
				// 外存成功 → 统计
				result.Offloaded++
				tokensSaved := tokens - e.estimateTokenCount(meta.Preview)
				result.TokensSaved += tokensSaved
				continue
			}
		}

		// offload 不可用 → 降级为 head/tail 截断
		truncated := e.truncateWithMarker(messages[i].Content, e.config.MaxToolOutputTokens)
		tokensSaved := tokens - e.estimateTokenCount(truncated)
		result.TokensSaved += tokensSaved
		messages[i].Content = truncated
		result.Truncated++
	}

	return result, messages, nil
}

// offToolResult 将工具结果外存存储，替换消息内容为预览+引用
func (e *Engine) offToolResult(msg *llm.Message, content string) (*OffloadContent, error) {
	// 提取预览
	preview := e.extractPreview(content, e.config.ToolPreviewTokens)

	meta, err := e.offload.Store(
		msg.ToolName,
		msg.ToolCallID,
		content,
		preview,
		0, // token 计数在调用方完成
	)
	if err != nil {
		return nil, fmt.Errorf("offload store: %w", err)
	}

	// 替换消息内容为预览+引用ID
	msg.Content = fmt.Sprintf(
		"[%s output offloaded to external storage]\n\nPreview:\n%s\n\n"+
			"Reference ID: %s\n"+
			"Original size: %d bytes\n"+
			"Stored at: %s",
		msg.ToolName,
		preview,
		meta.ID,
		meta.OriginalSize,
		meta.StoredPath,
	)

	msg.TruncationMarker = &llm.TruncationMarker{
		ToolName:       msg.ToolName,
		OriginalLen:    meta.OriginalSize,
		OmittedLen:     meta.OriginalSize - len(msg.Content),
		TruncationPass: 0,
	}

	return meta, nil
}

// isBinaryContent 检测内容是否为二进制
//
// 判断标准：非可打印字符比例 > 10% 视为二进制
//
// 可打印字符定义：ASCII 可打印字符 (0x20-0x7E) + 常见空白字符 (\n \t \r) + Unicode 中文/日文等
func (e *Engine) isBinaryContent(content string) bool {
	if len(content) == 0 {
		return false
	}

	// 统计非可打印字符数
	nonPrintable := 0
	total := len(content)

	for _, r := range content {
		// 可打印 ASCII + 常见空白
		if (r >= 0x20 && r <= 0x7E) || r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		// Unicode 字符（中文、日文、韩文等）视为可打印
		if r > 0x7E {
			continue
		}
		// 控制字符（0x00-0x1F，除了已排除的空白）
		nonPrintable++
	}

	if total == 0 {
		return false
	}

	return float64(nonPrintable)/float64(total) > 0.10
}

// extractPreview 提取内容的前 N 个 token 作为预览
//
// 策略：
//   1. 使用 tokenizer 精确计数
//   2. 逐字符累积，直到达到 maxTokens
//   3. 在单词/行边界处截断（尽量不截断在单词中间）
//   4. 添加截断标记
func (e *Engine) extractPreview(content string, maxTokens int) string {
	if maxTokens <= 0 {
		return content
	}

	// 粗略估计字符预算（ASCII: 1字符 ≈ 0.25 token，UTF-8: 1字符 ≈ 0.5 token）
	estimatedChars := maxTokens * 4
	if len(content) <= estimatedChars {
		return content
	}

	// 逐 token 提取
	var preview strings.Builder
	tokenCount := 0
	charIdx := 0

	for charIdx < len(content) && tokenCount < maxTokens {
		// 获取下一个 rune
		var runeVal rune
		runeSize := 1
		for i := charIdx; i < len(content); i++ {
			r := content[i]
			if r < 0x80 {
				runeVal = rune(r)
				runeSize = 1
			} else if r < 0xC0 {
				runeVal = rune(r)
				runeSize = 1
			} else if r < 0xE0 {
				runeVal = rune(r)
				runeSize = 2
			} else if r < 0xF0 {
				runeVal = rune(r)
				runeSize = 3
			} else {
				runeVal = rune(r)
				runeSize = 4
			}
			break
		}

		// 估算该 rune 的 token 数
		var tokenAdd int
		if runeVal < 0x80 {
			tokenAdd = 1
		} else {
			tokenAdd = 2
		}

		if tokenCount+tokenAdd > maxTokens {
			break
		}

		// 尝试在行边界处截断
		nextCharIdx := charIdx + runeSize
		if preview.Len() > maxTokens*2 && runeVal == '\n' {
			preview.WriteRune(runeVal)
			charIdx = nextCharIdx
			break
		}

		preview.WriteRune(runeVal)
		tokenCount += tokenAdd
		charIdx = nextCharIdx
	}

	result := preview.String()

	// 如果在非边界处截断，添加标记
	if charIdx < len(content) {
		result += "\n\n... [truncated, see full content in external storage] ..."
	}

	return result
}

// truncateWithMarker 简单 head/tail 截断并添加标记
//
// 保留前 headLen 字符和后 tailLen 字符，中间用标记替换。
// 用于 offload 不可用时的降级策略。
func (e *Engine) truncateWithMarker(content string, maxTokens int) string {
	if maxTokens <= 0 || len(content) == 0 {
		return content
	}

	// 估算字符预算
	estimatedChars := maxTokens * 4
	if len(content) <= estimatedChars {
		return content
	}

	headLen := estimatedChars / 2
	tailLen := estimatedChars / 4
	if tailLen < 64 {
		tailLen = 64
	}

	head := content[:headLen]
	tail := content[len(content)-tailLen:]
	omitted := len(content) - headLen - tailLen

	truncationNotice := fmt.Sprintf(
		"\n\n[... %d bytes of output omitted for context efficiency ...]\n\n",
		omitted,
	)

	return head + truncationNotice + tail
}

// estimateTokenCount 粗略估算 token 数
// ASCII: 1字符 ≈ 0.25 token, UTF-8 多字节字符 ≈ 0.5 token
func (e *Engine) estimateTokenCount(s string) int {
	count := 0
	for _, r := range s {
		if r < 0x80 {
			count++
		} else {
			count += 2
		}
	}
	return (count + 3) / 4 // 四舍五入
}
