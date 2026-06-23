package compact

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// RuleBasedConstraintExtractor 基于规则的约束提取器
// ─────────────────────────────────────────────────────────

// RuleBasedConstraintExtractor 使用正则和关键词规则提取约束
// 速度快、零成本，适合大多数场景
type RuleBasedConstraintExtractor struct {
	// patterns 按分类组织的匹配模式
	patterns map[string][]ConstraintPattern

	// minMatchCount 认为"足够"的最小匹配数
	minMatchCount int
}

// ConstraintPattern 约束匹配模式
type ConstraintPattern struct {
	Type    string         // 约束类型：technical/business/preferences/format/prohibition
	Pattern string         // 匹配模式字符串
	Regex   *regexp.Regexp // 编译后的正则
	Weight  float64        // 匹配权重（用于置信度计算）
}

// NewRuleBasedConstraintExtractor 创建规则约束提取器
func NewRuleBasedConstraintExtractor() *RuleBasedConstraintExtractor {
	ext := &RuleBasedConstraintExtractor{
		patterns:      make(map[string][]ConstraintPattern),
		minMatchCount: 1,
	}

	// 技术约束模式
	ext.addPattern("technical", `(?i)\b(use|using|with)\s+(\w+)\b`, 0.6)
	ext.addPattern("technical", `(?i)\b(built with|written in|implemented in)\s+(\w+)`, 0.7)
	ext.addPattern("technical", `(?i)\b(language|framework|library|tool|platform|database|api)\b`, 0.5)

	// 业务约束模式
	ext.addPattern("business", `(?i)\b(must|should|need to|required|requirement|need)\b`, 0.8)
	ext.addPattern("business", `(?i)\b(policy|rule|regulation|compliance|standard)\b`, 0.7)

	// 偏好约束模式
	ext.addPattern("preferences", `(?i)\b(prefer|preference|like to|rather|would like)\b`, 0.6)
	ext.addPattern("preferences", `(?i)\b(style|naming|convention|format|consistent)\b`, 0.5)

	// 格式约束模式
	ext.addPattern("format", `(?i)\b(output in|format as|return as|as json|as yaml|as markdown)\b`, 0.7)
	ext.addPattern("format", `(?i)\b(schema|structure|pattern|template|blueprint)\b`, 0.5)

	// 禁止约束模式
	ext.addPattern("prohibition", `(?i)\b(don'?t|never|avoid|must not|should not|cannot|forbidden|prohibited)\b`, 0.8)
	ext.addPattern("prohibition", `(?i)\b(禁止|不要|避免|不能|不允许)\b`, 0.7)

	return ext
}

// addPattern 添加匹配模式
func (r *RuleBasedConstraintExtractor) addPattern(constraintType, pattern string, weight float64) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		slog.Warn("Invalid constraint pattern, skipping", "pattern", pattern, "error", err)
		return
	}
	r.patterns[constraintType] = append(r.patterns[constraintType], ConstraintPattern{
		Type:    constraintType,
		Pattern: pattern,
		Regex:   re,
		Weight:  weight,
	})
}

// Name 返回提取器名称
func (r *RuleBasedConstraintExtractor) Name() string {
	return "rule_based"
}

// Extract 从消息列表中提取约束
func (r *RuleBasedConstraintExtractor) Extract(ctx context.Context, messages []llm.Message) (*Constraints, error) {
	constraints := &Constraints{
		Source: make(map[int]string),
	}

	for i, msg := range messages {
		if msg.Role != llm.RoleUser {
			continue
		}

		content := msg.Content
		if content == "" {
			continue
		}

		// 对每条用户消息运行所有模式
		for ctype, patterns := range r.patterns {
			for _, pat := range patterns {
				matches := pat.Regex.FindAllString(content, -1)
				if len(matches) > 0 {
					// 提取匹配的上下文（前后各50字符）
					contextText := extractContext(content, pat.Regex)
					if contextText != "" {
						constraints.Source[i] = contextText
					}

					// 分类添加
					switch ctype {
					case "technical":
						constraints.Technical = append(constraints.Technical, matches...)
					case "business":
						constraints.Business = append(constraints.Business, matches...)
					case "preferences":
						constraints.Preferences = append(constraints.Preferences, matches...)
					case "format":
						constraints.Format = append(constraints.Format, matches...)
					case "prohibition":
						constraints.Prohibitions = append(constraints.Prohibitions, matches...)
					}
					break // 每个类型只匹配一次
				}
			}
		}
	}

	// 去重
	constraints.deduplicate()

	return constraints, nil
}

// extractContext 提取匹配位置附近的上下文文本
func extractContext(content string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(content)
	if loc == nil {
		return ""
	}

	start := loc[0] - 50
	if start < 0 {
		start = 0
	}
	end := loc[1] + 50
	if end > len(content) {
		end = len(content)
	}

	result := content[start:end]
	// 添加省略号表示截断
	if start > 0 {
		result = "..." + result
	}
	if end < len(content) {
		result = result + "..."
	}
	return result
}

// deduplicate 约束去重
func (c *Constraints) deduplicate() {
	c.Technical = uniqueStrings(c.Technical)
	c.Business = uniqueStrings(c.Business)
	c.Preferences = uniqueStrings(c.Preferences)
	c.Format = uniqueStrings(c.Format)
	c.Prohibitions = uniqueStrings(c.Prohibitions)
}

// uniqueStrings 字符串去重
func uniqueStrings(input []string) []string {
	if len(input) == 0 {
		return input
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, s := range input {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			result = append(result, s)
		}
	}
	return result
}

// MatchConfidence 返回规则匹配的置信度（0.0~1.0）
// 基于匹配的约束类型数量和总权重
func (r *RuleBasedConstraintExtractor) MatchConfidence(constraints *Constraints) float64 {
	totalWeight := 0.0
	matchedTypes := 0

	for _, patterns := range r.patterns {
		hasMatch := false
		for _, pat := range patterns {
			// 检查这个类型的模式是否有匹配
			switch pat.Type {
			case "technical":
				if len(constraints.Technical) > 0 {
					hasMatch = true
				}
			case "business":
				if len(constraints.Business) > 0 {
					hasMatch = true
				}
			case "preferences":
				if len(constraints.Preferences) > 0 {
					hasMatch = true
				}
			case "format":
				if len(constraints.Format) > 0 {
					hasMatch = true
				}
			case "prohibition":
				if len(constraints.Prohibitions) > 0 {
					hasMatch = true
				}
			}
			if hasMatch {
				totalWeight += pat.Weight
				break
			}
		}
		if hasMatch {
			matchedTypes++
		}
	}

	// 置信度 = 匹配类型数 / 总类型数 * 平均权重
	typeRatio := float64(matchedTypes) / float64(len(r.patterns))
	avgWeight := totalWeight / float64(matchedTypes+1) // +1 避免除零

	return typeRatio * avgWeight
}

// ─────────────────────────────────────────────────────────
// LLMConstraintExtractor 基于 LLM 的约束提取器
// ─────────────────────────────────────────────────────────

// LLMConstraintExtractor 使用 LLM 进行语义级别的约束提取
// 比规则提取更准确，但有成本和延迟
type LLMConstraintExtractor struct {
	client   SummarizationClient
	template PromptTemplate
}

// NewLLMConstraintExtractor 创建 LLM 约束提取器
func NewLLMConstraintExtractor(client SummarizationClient, template PromptTemplate) *LLMConstraintExtractor {
	return &LLMConstraintExtractor{
		client:   client,
		template: template,
	}
}

// Name 返回提取器名称
func (l *LLMConstraintExtractor) Name() string {
	return "llm_based"
}

// Extract 使用 LLM 从消息中提取结构化约束
func (l *LLMConstraintExtractor) Extract(ctx context.Context, messages []llm.Message) (*Constraints, error) {
	if l.client == nil {
		return &Constraints{}, fmt.Errorf("LLM client not available")
	}

	// 只提取用户消息
	var userMessages []llm.Message
	for _, msg := range messages {
		if msg.Role == llm.RoleUser {
			userMessages = append(userMessages, msg)
		}
	}

	if len(userMessages) == 0 {
		return &Constraints{}, nil
	}

	// 构建提示词
	constraintPrompt := l.template.ConstraintPrompt()
	if constraintPrompt == "" {
		constraintPrompt = DefaultConstraintPrompt
	}

	// 如果用户消息太多，只取首尾（限制 token 使用）
	const maxUserMessages = 5
	if len(userMessages) > maxUserMessages {
		selected := make([]llm.Message, 0, maxUserMessages)
		selected = append(selected, userMessages[0])
		// 取最后 maxUserMessages-1 条
		start := len(userMessages) - (maxUserMessages - 1)
		if start < 1 {
			start = 1
		}
		selected = append(selected, userMessages[start:]...)
		userMessages = selected
	}

	// 调用 LLM
	promptMsgs := []llm.Message{
		{Role: llm.RoleSystem, Content: constraintPrompt},
	}
	promptMsgs = append(promptMsgs, userMessages...)

	summary, err := l.client.GenerateSummary(ctx, promptMsgs)
	if err != nil {
		return &Constraints{}, fmt.Errorf("LLM constraint extraction failed: %w", err)
	}

	// 解析 LLM 输出为结构化约束
	constraints := l.parseLLMResponse(summary)
	return constraints, nil
}

// parseLLMResponse 解析 LLM 返回的约束文本为结构化 Constraints
// LLM 输出格式预期为标记文本，如：
//   Technical: ...
//   Business: ...
// 或简单列表形式
func (l *LLMConstraintExtractor) parseLLMResponse(response string) *Constraints {
	constraints := &Constraints{
		Source: make(map[int]string),
	}

	if response == "" || strings.Contains(response, "No specific constraints found") {
		return constraints
	}

	lines := strings.Split(response, "\n")
	currentType := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 检测分类标题
		lower := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lower, "technical"):
			currentType = "technical"
			continue
		case strings.Contains(lower, "business"):
			currentType = "business"
			continue
		case strings.Contains(lower, "preferences"):
			currentType = "preferences"
			continue
		case strings.Contains(lower, "format"):
			currentType = "format"
			continue
		case strings.Contains(lower, "prohibition"):
			currentType = "prohibition"
			continue
		}

		// 移除列表前缀
		cleaned := strings.TrimLeft(trimmed, "- *•·")
		cleaned = strings.TrimSpace(cleaned)
		if cleaned == "" {
			continue
		}

		// 按分类添加
		switch currentType {
		case "technical":
			constraints.Technical = append(constraints.Technical, cleaned)
		case "business":
			constraints.Business = append(constraints.Business, cleaned)
		case "preferences":
			constraints.Preferences = append(constraints.Preferences, cleaned)
		case "format":
			constraints.Format = append(constraints.Format, cleaned)
		case "prohibition":
			constraints.Prohibitions = append(constraints.Prohibitions, cleaned)
		}
	}

	constraints.deduplicate()
	return constraints
}

// ─────────────────────────────────────────────────────────
// HybridConstraintExtractor 混合策略约束提取器
// ─────────────────────────────────────────────────────────

// HybridConstraintExtractor 结合规则和 LLM 的混合提取器
// 先用规则快速提取，如果置信度不够再用 LLM 增强
type HybridConstraintExtractor struct {
	ruleBased *RuleBasedConstraintExtractor
	llmBased  *LLMConstraintExtractor

	// confidenceThreshold 触发 LLM 增强的置信度阈值
	// 默认 0.3：规则匹配置信度低于此值时用 LLM 增强
	confidenceThreshold float64

	// enableLLM 是否允许使用 LLM 增强
	enableLLM bool
}

// NewHybridConstraintExtractor 创建混合约束提取器
func NewHybridConstraintExtractor(
	ruleBased *RuleBasedConstraintExtractor,
	llmBased *LLMConstraintExtractor,
	threshold float64,
	enableLLM bool,
) *HybridConstraintExtractor {
	if ruleBased == nil {
		ruleBased = NewRuleBasedConstraintExtractor()
	}
	if threshold <= 0 || threshold > 1 {
		threshold = 0.3 // 默认阈值
	}

	return &HybridConstraintExtractor{
		ruleBased:           ruleBased,
		llmBased:            llmBased,
		confidenceThreshold: threshold,
		enableLLM:           enableLLM,
	}
}

// Name 返回提取器名称
func (h *HybridConstraintExtractor) Name() string {
	return "hybrid"
}

// Extract 混合策略提取约束
func (h *HybridConstraintExtractor) Extract(ctx context.Context, messages []llm.Message) (*Constraints, error) {
	// Step 1: 规则快速提取
	constraints, err := h.ruleBased.Extract(ctx, messages)
	if err != nil {
		return constraints, err
	}

	// Step 2: 计算置信度
	confidence := h.ruleBased.MatchConfidence(constraints)

	// Step 3: 根据置信度决定是否需要 LLM 增强
	if h.enableLLM && h.llmBased != nil && confidence < h.confidenceThreshold {
		slog.Debug("Constraint extraction confidence below threshold, using LLM enhancement",
			"confidence", confidence,
			"threshold", h.confidenceThreshold)

		llmConstraints, err := h.llmBased.Extract(ctx, messages)
		if err != nil {
			// LLM 失败，接受规则匹配结果
			slog.Warn("LLM constraint extraction failed, using rule-based results",
				"error", err)
			return constraints, nil
		}

		// 合并规则和 LLM 结果
		constraints = mergeConstraints(constraints, llmConstraints)
	}

	return constraints, nil
}

// mergeConstraints 合并两个 ConstraintExtractor 的结果
func mergeConstraints(a, b *Constraints) *Constraints {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	merged := &Constraints{
		Technical:    uniqueStrings(append(a.Technical, b.Technical...)),
		Business:     uniqueStrings(append(a.Business, b.Business...)),
		Preferences:  uniqueStrings(append(a.Preferences, b.Preferences...)),
		Format:       uniqueStrings(append(a.Format, b.Format...)),
		Prohibitions: uniqueStrings(append(a.Prohibitions, b.Prohibitions...)),
		Source:       make(map[int]string),
	}

	// 合并 Source 追踪
	for k, v := range a.Source {
		merged.Source[k] = v
	}
	for k, v := range b.Source {
		merged.Source[k] = v
	}

	return merged
}
