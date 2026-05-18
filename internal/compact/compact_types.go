package compact

import (
	"context"
	"codeactor/internal/llm"
)

// SummarizationClient 摘要LLM客户端接口（最小化，只用于摘要）
// 用于对低优先级消息做智能摘要压缩
type SummarizationClient interface {
	// GenerateSummary 生成消息摘要。输入一批消息，输出结构化摘要文本。
	GenerateSummary(ctx context.Context, messages []llm.Message) (string, error)
}

// ContextCompressor 上下文压缩器接口
type ContextCompressor interface {
	// Compress 压缩上下文，返回压缩后的messages和统计信息
	Compress(ctx context.Context, messages []llm.Message) (*CompressResult, error)

	// CountTokens 计算messages的总token数
	CountTokens(messages []llm.Message) (int, error)

	// GetPriorities 获取每条消息的优先级（用于调试）
	GetPriorities(messages []llm.Message) map[int]float64
}

// CompressResult 压缩结果
type CompressResult struct {
	CompressedMessages []llm.Message
	OriginalTokens     int
	CompressedTokens   int
	CompressionRatio   float64 // 压缩比 (0~1)，越小压缩越多
	CompressionStats   string  // 压缩统计信息
	SummaryInfo        string  // 摘要信息（可选）
}
