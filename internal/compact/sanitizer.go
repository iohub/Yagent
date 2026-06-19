package compact

import (
	"fmt"
	"regexp"
	"strings"
)

// ─────────────────────────────────────────────────────────
// DefaultSummarySanitizer 默认摘要清洗器
// ─────────────────────────────────────────────────────────

// DefaultSummarySanitizer 默认摘要清洗器实现
// 采用管道模式（Pipeline）：按顺序执行多个清洗步骤
type DefaultSummarySanitizer struct {
	// steps 清洗步骤列表
	steps []SanitizeStep
}

// SanitizeStep 清洗步骤接口
type SanitizeStep interface {
	// Name 步骤名称（用于日志/调试）
	Name() string
	// Apply 执行清洗
	Apply(text string) string
}

// NewDefaultSummarySanitizer 创建默认摘要清洗器
// 包含一组预定义的清洗步骤
func NewDefaultSummarySanitizer() *DefaultSummarySanitizer {
	return &DefaultSummarySanitizer{
		steps: []SanitizeStep{
			&removeMarkdownFenceStep{},
			&removeCourtesyPrefixStep{},
			&removeDuplicateLinesStep{},
			&compressWhitespaceStep{},
			&validateMinimumLengthStep{minLen: 10},
		},
	}
}

// NewCustomSummarySanitizer 创建自定义清洗器（指定步骤列表）
func NewCustomSummarySanitizer(steps []SanitizeStep) *DefaultSummarySanitizer {
	return &DefaultSummarySanitizer{
		steps: steps,
	}
}

// Sanitize 实现 SummarySanitizer 接口
// 按顺序执行所有清洗步骤
func (s *DefaultSummarySanitizer) Sanitize(raw string) string {
	if raw == "" {
		return ""
	}

	result := raw
	for _, step := range s.steps {
		result = step.Apply(result)
	}

	return strings.TrimSpace(result)
}

// Validate 实现 SummarySanitizer 接口
// 验证清洗后的摘要是否有效
func (s *DefaultSummarySanitizer) Validate(cleaned string) error {
	if cleaned == "" {
		return fmt.Errorf("summary is empty after sanitization")
	}

	// 检查是否太短（少于 10 个字符可能是错误输出）
	if len(cleaned) < 10 {
		return fmt.Errorf("summary too short (%d chars), may be erroneous output", len(cleaned))
	}

	// 检查是否全是空白/标点
	nonSpace := 0
	for _, c := range cleaned {
		if c != ' ' && c != '\n' && c != '\t' && c != '.' && c != ',' && c != '!' && c != '?' {
			nonSpace++
		}
	}
	if nonSpace < 3 {
		return fmt.Errorf("summary contains insufficient meaningful content")
	}

	return nil
}

// ─────────────────────────────────────────────────────────
// 内置清洗步骤
// ─────────────────────────────────────────────────────────

// removeMarkdownFenceStep 移除 markdown 代码块围栏
type removeMarkdownFenceStep struct{}

func (s *removeMarkdownFenceStep) Name() string { return "remove_markdown_fence" }
func (s *removeMarkdownFenceStep) Apply(text string) string {
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

// removeCourtesyPrefixStep 移除 LLM 回复开头的客套话
type removeCourtesyPrefixStep struct{}

func (s *removeCourtesyPrefixStep) Name() string { return "remove_courtesy_prefix" }
func (s *removeCourtesyPrefixStep) Apply(text string) string {
	prefixes := []string{
		"Sure", "Sure,", "Here", "Here's", "Here is", "Here are",
		"Certainly", "Of course", "Of course,", "I'll", "I will", "I can",
		"好的", "好的，", "好的:", "当然", "当然，", "当然:",
		"我来", "我来给", "以下是", "下面", "这是",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(text, p) {
			lines := strings.SplitN(text, "\n", 2)
			if len(lines) > 1 {
				text = strings.TrimSpace(lines[1])
			} else {
				text = ""
			}
			break
		}
	}
	return text
}

// removeDuplicateLinesStep 移除连续重复的行
type removeDuplicateLinesStep struct{}

func (s *removeDuplicateLinesStep) Name() string { return "remove_duplicate_lines" }
func (s *removeDuplicateLinesStep) Apply(text string) string {
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
				continue // 跳过第3次及以上重复
			}
		} else {
			repeatCount = 1
		}
		result = append(result, lines[i-1])
	}
	// 添加最后一行
	result = append(result, lines[len(lines)-1])

	return strings.Join(result, "\n")
}

// compressWhitespaceStep 压缩连续空白
type compressWhitespaceStep struct {
	multiNewline *regexp.Regexp
	trailingWs   *regexp.Regexp
}

func (s *compressWhitespaceStep) Name() string { return "compress_whitespace" }
func (s *compressWhitespaceStep) Apply(text string) string {
	if s.multiNewline == nil {
		s.multiNewline = regexp.MustCompile(`\n{3,}`)
		s.trailingWs = regexp.MustCompile(`[ \t]+$`)
	}

	text = s.multiNewline.ReplaceAllString(text, "\n\n")
	text = s.trailingWs.ReplaceAllString(text, "")
	return text
}

// validateMinimumLengthStep 最小长度校验
type validateMinimumLengthStep struct {
	minLen int
}

func (s *validateMinimumLengthStep) Name() string { return "validate_minimum_length" }
func (s *validateMinimumLengthStep) Apply(text string) string {
	if len(text) < s.minLen {
		return ""
	}
	return text
}

// ─────────────────────────────────────────────────────────
// 便捷构造函数
// ─────────────────────────────────────────────────────────

// NewLenientSanitizer 创建宽松的清洗器（适用于快速预览）
// 只做基础清洗，不做严格校验
func NewLenientSanitizer() *DefaultSummarySanitizer {
	return NewCustomSummarySanitizer([]SanitizeStep{
		&removeMarkdownFenceStep{},
		&removeCourtesyPrefixStep{},
		&compressWhitespaceStep{},
	})
}

// NewStrictSanitizer 创建严格的清洗器（适用于持久化存储）
// 包含所有清洗步骤和严格校验
func NewStrictSanitizer() *DefaultSummarySanitizer {
	return NewCustomSummarySanitizer([]SanitizeStep{
		&removeMarkdownFenceStep{},
		&removeCourtesyPrefixStep{},
		&removeDuplicateLinesStep{},
		&compressWhitespaceStep{},
		&validateMinimumLengthStep{minLen: 10},
	})
}
