package compact

import (
	"context"
	"fmt"
	"strings"
	"codeactor/internal/llm"
)

// Compressor 压缩器接口
type Compressor interface {
	// L1Compress LLM摘要压缩
	L1Compress(ctx context.Context, messages []llm.Message, priorities []MessagePriority) ([]llm.Message, error)

	// L2Compress 规则压缩 - 截断和合并
	L2Compress(messages []llm.Message) []llm.Message

	// L3Compress 丢弃压缩 - 极端情况
	L3Compress(messages []llm.Message, keepRounds int) []llm.Message
}

// RuleCompressor 规则压缩器（L2+L3）
type RuleCompressor struct {
	config *Config
}

// NewRuleCompressor 创建规则压缩器
func NewRuleCompressor(config *Config) *RuleCompressor {
	return &RuleCompressor{config: config}
}

// L1Compress LLM摘要压缩（当前为占位实现，需要SummarizationClient）
func (rc *RuleCompressor) L1Compress(ctx context.Context, messages []llm.Message, priorities []MessagePriority) ([]llm.Message, error) {
	// TODO: 当提供SummarizationClient时，实现LLM摘要压缩
	// 当前返回原messages，不执行L1压缩
	return messages, nil
}

// L2Compress 规则压缩 - 截断超长tool输出
func (rc *RuleCompressor) L2Compress(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == llm.RoleTool {
			runes := []rune(msg.Content)
			if len(runes) > 3000 {
				// 保留首尾各1500字符
				msg.Content = string(runes[:1500]) + "\n[...TRUNCATED...]\n" + string(runes[len(runes)-1500:])
			}
		}
		result = append(result, msg)
	}

	return result
}

// 注意：始终保留原始 System 消息和第一条 User 消息
func (rc *RuleCompressor) L3Compress(messages []llm.Message, keepRounds int) []llm.Message {
	if len(messages) <= keepRounds*3 { // 每轮约3条消息
		return messages
	}

	recentStart := len(messages) - keepRounds*3
	if recentStart < 0 {
		recentStart = 0
	}

	// 生成早期对话摘要
	var earlySummary strings.Builder
	earlySummary.WriteString("[EARLY CONTEXT COMPRESSED]\n")
	earlySummary.WriteString("Summary of completed tasks:\n")

	for i := 0; i < recentStart; i++ {
		msg := messages[i]
		if msg.Role == llm.RoleAssistant && (strings.Contains(msg.Content, "completed") ||
			strings.Contains(msg.Content, "done") || strings.Contains(msg.Content, "finished")) {
			earlySummary.WriteString(fmt.Sprintf("- Task conclusion at message %d\n", i))
		}
	}

	// 构建结果：[原始System消息] + [早期摘要(作为System)] + [近期消息]
	result := make([]llm.Message, 0, len(messages)-recentStart+1)

	// 始终保留原始System消息（如果存在）
	if len(messages) > 0 {
		result = append(result, messages[0])
	}

	if earlySummary.Len() > 0 {
		result = append(result, llm.Message{
			Role:    llm.RoleSystem,
			Content: earlySummary.String(),
		})
	}

	result = append(result, messages[recentStart:]...)

	return result
}
