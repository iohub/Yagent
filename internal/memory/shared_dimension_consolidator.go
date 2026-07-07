package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// LLM Client Interface
// ============================================================

// LLMClient 是LLM引擎的抽象接口，实际由调用方注入
type LLMClient interface {
	// Complete 发送system prompt + user prompt，返回补全文本
	Complete(systemPrompt, userPrompt string) (string, error)
}

// ============================================================
// Consolidation Prompt Builder
// ============================================================

// ConsolidationPromptBuilder 为每个维度构建LLM整合提示词
type ConsolidationPromptBuilder struct{}

func NewConsolidationPromptBuilder() *ConsolidationPromptBuilder {
	return &ConsolidationPromptBuilder{}
}

// BuildUserConsolidationPrompt 构建用户记忆整合提示
func (b *ConsolidationPromptBuilder) BuildUserConsolidationPrompt(current *UserMemory) string {
	return fmt.Sprintf(`You are a memory consolidation engine. Your job is to refine and compact user profile memory.

Rules:
- Keep only important, stable information about the user
- Remove redundant or contradictory entries
- Maximum 10 expertise areas, 5 custom preferences
- Prefer specific over vague (e.g., "Go expert with 5 years" over "knows Go")
- If two entries conflict, keep the more recent one
- Output ONLY valid JSON matching the UserMemory structure

Current User Memory:
%s

Provide a consolidated version. Remove anything trivial or obvious. Keep only what genuinely helps personalize interactions.`, mustMarshalJSON(current))
}

// BuildFeedbackConsolidationPrompt 构建反馈记忆整合提示
func (b *ConsolidationPromptBuilder) BuildFeedbackConsolidationPrompt(current *FeedbackMemory) string {
	return fmt.Sprintf(`You are a memory consolidation engine. Your job is to refine and compact feedback memory.

Rules:
- Merge similar corrections into patterns
- Keep at most 10 patterns and 10 recent individual corrections
- Remove corrections older than 30 days that are already captured in patterns
- Ensure patterns are actionable and specific
- Output ONLY valid JSON matching the FeedbackMemory structure

Current Feedback Memory:
%s

Provide a consolidated version. Focus on patterns over individual instances.`, mustMarshalJSON(current))
}

// BuildProjectConsolidationPrompt 构建项目记忆整合提示
func (b *ConsolidationPromptBuilder) BuildProjectConsolidationPrompt(current *ProjectMemory) string {
	return fmt.Sprintf(`You are a memory consolidation engine. Your job is to refine and compact project context memory.

Rules:
- Archive (set status="completed") objectives that seem done
- Remove stale team members (no updates in 30+ days)
- Keep at most 5 active objectives and 5 deadlines
- Ensure status is concise and current
- Output ONLY valid JSON matching the ProjectMemory structure

Current Project Memory:
%s

Provide a consolidated version. Keep only current, actionable information.`, mustMarshalJSON(current))
}

// BuildReferenceConsolidationPrompt 构建参考记忆整合提示
func (b *ConsolidationPromptBuilder) BuildReferenceConsolidationPrompt(current *ReferenceMemory) string {
	return fmt.Sprintf(`You are a memory consolidation engine. Your job is to refine and compact reference resource memory.

Rules:
- Remove duplicates (same location, different names -> keep clearer name)
- Remove broken or placeholder entries (empty location or description)
- Keep at most 20 resources
- Ensure categories are consistent and useful
- Output ONLY valid JSON matching the ReferenceMemory structure

Current Reference Memory:
%s

Provide a consolidated version. Focus on actionable, well-described resources.`, mustMarshalJSON(current))
}

// ============================================================
// SharedDimensionConsolidator
// ============================================================

// SharedDimensionConsolidator 执行LLM驱动的深度记忆整合
// 定期运行，对每个维度的记忆做去重、精简、模式提炼
type SharedDimensionConsolidator struct {
	store *SharedDimensionStore
	prompts *ConsolidationPromptBuilder
	llm LLMClient
}

// NewSharedDimensionConsolidator 创建整合器
func NewSharedDimensionConsolidator(store *SharedDimensionStore, llm LLMClient) *SharedDimensionConsolidator {
	return &SharedDimensionConsolidator{
		store:   store,
		prompts: NewConsolidationPromptBuilder(),
		llm:     llm,
	}
}

// ConsolidateUser 整合用户记忆
func (c *SharedDimensionConsolidator) ConsolidateUser(userID string) error {
	current, err := c.store.GetUserMemory(userID)
	if err != nil {
		return fmt.Errorf("get user memory: %w", err)
	}
	if current.IsEmpty() {
		return nil
	}

	prompt := c.prompts.BuildUserConsolidationPrompt(current)
	currentJSON := mustMarshalJSON(current)

	response, err := c.llm.Complete(
		"You are a memory consolidation engine. Output ONLY valid JSON, no explanation.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	response = extractJSON(response)
	if response == "" {
		return fmt.Errorf("no valid JSON in LLM response")
	}

	// 安全检查：整合后的内容不应比原始内容大太多
	if len(response) > len(currentJSON)*2 {
		return fmt.Errorf("consolidated version is suspiciously large (%.1fKB vs %.1fKB) — skipping",
			float64(len(response))/1024, float64(len(currentJSON))/1024)
	}

	var consolidated UserMemory
	if err := json.Unmarshal([]byte(response), &consolidated); err != nil {
		return fmt.Errorf("unmarshal consolidated: %w", err)
	}

	consolidated.UserID = userID
	consolidated.Version = current.Version + 1
	consolidated.UpdatedAt = time.Now()

	return c.store.SetUserMemory(&consolidated)
}

// ConsolidateFeedback 整合反馈记忆
func (c *SharedDimensionConsolidator) ConsolidateFeedback(userID string) error {
	current, err := c.store.GetFeedbackMemory(userID)
	if err != nil {
		return fmt.Errorf("get feedback memory: %w", err)
	}
	if current.IsEmpty() {
		return nil
	}

	prompt := c.prompts.BuildFeedbackConsolidationPrompt(current)

	response, err := c.llm.Complete(
		"You are a memory consolidation engine. Output ONLY valid JSON, no explanation.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	response = extractJSON(response)
	if response == "" {
		return fmt.Errorf("no valid JSON in LLM response")
	}

	var consolidated FeedbackMemory
	if err := json.Unmarshal([]byte(response), &consolidated); err != nil {
		return fmt.Errorf("unmarshal consolidated: %w", err)
	}

	consolidated.UserID = userID
	consolidated.Version = current.Version + 1
	consolidated.UpdatedAt = time.Now()

	return c.store.SetFeedbackMemory(&consolidated)
}

// ConsolidateProject 整合项目记忆
func (c *SharedDimensionConsolidator) ConsolidateProject(projectID string) error {
	current, err := c.store.GetProjectMemory(projectID)
	if err != nil {
		return fmt.Errorf("get project memory: %w", err)
	}
	if current.IsEmpty() {
		return nil
	}

	prompt := c.prompts.BuildProjectConsolidationPrompt(current)

	response, err := c.llm.Complete(
		"You are a memory consolidation engine. Output ONLY valid JSON, no explanation.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	response = extractJSON(response)
	if response == "" {
		return fmt.Errorf("no valid JSON in LLM response")
	}

	var consolidated ProjectMemory
	if err := json.Unmarshal([]byte(response), &consolidated); err != nil {
		return fmt.Errorf("unmarshal consolidated: %w", err)
	}

	consolidated.ProjectID = projectID
	consolidated.Version = current.Version + 1
	consolidated.UpdatedAt = time.Now()

	return c.store.SetProjectMemory(&consolidated)
}

// ConsolidateReference 整合参考记忆
func (c *SharedDimensionConsolidator) ConsolidateReference(projectID string) error {
	current, err := c.store.GetReferenceMemory(projectID)
	if err != nil {
		return fmt.Errorf("get reference memory: %w", err)
	}
	if current.IsEmpty() {
		return nil
	}

	prompt := c.prompts.BuildReferenceConsolidationPrompt(current)

	response, err := c.llm.Complete(
		"You are a memory consolidation engine. Output ONLY valid JSON, no explanation.",
		prompt,
	)
	if err != nil {
		return fmt.Errorf("LLM call failed: %w", err)
	}

	response = extractJSON(response)
	if response == "" {
		return fmt.Errorf("no valid JSON in LLM response")
	}

	var consolidated ReferenceMemory
	if err := json.Unmarshal([]byte(response), &consolidated); err != nil {
		return fmt.Errorf("unmarshal consolidated: %w", err)
	}

	consolidated.ProjectID = projectID
	consolidated.Version = current.Version + 1
	consolidated.UpdatedAt = time.Now()

	return c.store.SetReferenceMemory(&consolidated)
}

// ConsolidateAll 整合指定用户/项目的所有维度
func (c *SharedDimensionConsolidator) ConsolidateAll(userID, projectID string) []error {
	var errs []error

	if err := c.ConsolidateUser(userID); err != nil {
		errs = append(errs, fmt.Errorf("user: %w", err))
	}
	if err := c.ConsolidateFeedback(userID); err != nil {
		errs = append(errs, fmt.Errorf("feedback: %w", err))
	}
	if err := c.ConsolidateProject(projectID); err != nil {
		errs = append(errs, fmt.Errorf("project: %w", err))
	}
	if err := c.ConsolidateReference(projectID); err != nil {
		errs = append(errs, fmt.Errorf("reference: %w", err))
	}

	return errs
}

// ============================================================
// Helpers
// ============================================================

// mustMarshalJSON 将对象序列化为JSON字符串（panic on error）
func mustMarshalJSON(v interface{}) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": "%v"}`, err)
	}
	return string(data)
}

// extractJSON 从LLM响应中提取第一个JSON对象或数组
// 处理LLM有时会在JSON前后加markdown代码块或说明文字的情况
func extractJSON(response string) string {
	// 尝试找到 ```json ... ``` 包裹
	if idx := strings.Index(response, "```json"); idx >= 0 {
		start := idx + 7
		if end := strings.Index(response[start:], "```"); end >= 0 {
			content := strings.TrimSpace(response[start : start+end])
			if content != "" {
				return content
			}
		}
	}

	// 尝试找到 { 和 } 匹配的JSON对象
	braceStart := strings.IndexByte(response, '{')
	if braceStart >= 0 {
		depth := 0
		for i := braceStart; i < len(response); i++ {
			switch response[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return response[braceStart : i+1]
				}
			}
		}
	}

	// 尝试找到 [ 和 ] 匹配的JSON数组
	bracketStart := strings.IndexByte(response, '[')
	if bracketStart >= 0 {
		depth := 0
		for i := bracketStart; i < len(response); i++ {
			switch response[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					return response[bracketStart : i+1]
				}
			}
		}
	}

	return ""
}
