package compact

import (
	"context"
	"strings"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// Constraints 结构化约束
// ─────────────────────────────────────────────────────────

// Constraints 表示从对话中提取的结构化约束
type Constraints struct {
	// Technical 技术约束：语言、框架、版本、API 等
	Technical []string `json:"technical,omitempty"`

	// Business 业务约束：规则、流程、策略等
	Business []string `json:"business,omitempty"`

	// Preferences 用户偏好：风格、命名约定等
	Preferences []string `json:"preferences,omitempty"`

	// Format 格式要求：输出格式、结构要求等
	Format []string `json:"format,omitempty"`

	// Prohibitions 禁止事项：不能做的操作
	Prohibitions []string `json:"prohibitions,omitempty"`

	// Source 约束来源追踪（消息索引 → 提取的约束文本）
	Source map[int]string `json:"source,omitempty"`
}

// IsEmpty 检查是否没有任何约束
func (c *Constraints) IsEmpty() bool {
	return len(c.Technical) == 0 &&
		len(c.Business) == 0 &&
		len(c.Preferences) == 0 &&
		len(c.Format) == 0 &&
		len(c.Prohibitions) == 0
}

// All 返回所有约束的平铺列表
func (c *Constraints) All() []string {
	var result []string
	result = append(result, c.Technical...)
	result = append(result, c.Business...)
	result = append(result, c.Preferences...)
	result = append(result, c.Format...)
	result = append(result, c.Prohibitions...)
	return result
}

// String 返回格式化的约束文本
func (c *Constraints) String() string {
	if c.IsEmpty() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Extracted Constraints:\n")

	if len(c.Technical) > 0 {
		sb.WriteString("\nTechnical:\n")
		for _, t := range c.Technical {
			sb.WriteString("  - " + t + "\n")
		}
	}
	if len(c.Business) > 0 {
		sb.WriteString("\nBusiness:\n")
		for _, b := range c.Business {
			sb.WriteString("  - " + b + "\n")
		}
	}
	if len(c.Preferences) > 0 {
		sb.WriteString("\nPreferences:\n")
		for _, p := range c.Preferences {
			sb.WriteString("  - " + p + "\n")
		}
	}
	if len(c.Format) > 0 {
		sb.WriteString("\nFormat:\n")
		for _, f := range c.Format {
			sb.WriteString("  - " + f + "\n")
		}
	}
	if len(c.Prohibitions) > 0 {
		sb.WriteString("\nProhibitions:\n")
		for _, p := range c.Prohibitions {
			sb.WriteString("  - " + p + "\n")
		}
	}

	return sb.String()
}

// ─────────────────────────────────────────────────────────
// ConstraintExtractor 约束提取器接口
// ─────────────────────────────────────────────────────────

// ConstraintExtractor 从对话消息中提取结构化约束
type ConstraintExtractor interface {
	// Extract 从消息列表中提取约束
	Extract(ctx context.Context, messages []llm.Message) (*Constraints, error)

	// Name 返回提取器名称（用于日志和监控）
	Name() string
}

// ─────────────────────────────────────────────────────────
// SummarySanitizer 摘要清洗器接口
// ─────────────────────────────────────────────────────────

// SummarySanitizer 清洗 LLM 生成的摘要输出
// 移除脏数据、客套话、重复内容等
type SummarySanitizer interface {
	// Sanitize 清洗原始 LLM 输出，返回干净的摘要文本
	Sanitize(raw string) string

	// Validate 验证清洗后的摘要是否有效
	// 如果摘要太短或内容异常，返回错误
	Validate(cleaned string) error
}

// ─────────────────────────────────────────────────────────
// PromptTemplate 提示词模板接口
// ─────────────────────────────────────────────────────────

// PromptTemplate 提供结构化摘要使用的提示词模板
type PromptTemplate interface {
	// SegmentPrompt 返回分段摘要的提示词
	SegmentPrompt() string

	// MergePrompt 返回摘要合并的提示词
	MergePrompt() string

	// FullCompressPrompt 返回全量压缩的提示词
	FullCompressPrompt() string

	// ConstraintPrompt 返回约束提取的提示词
	ConstraintPrompt() string
}
