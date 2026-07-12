package tools

import (
	"context"
	"fmt"
	"time"

	"codeactor/internal/llm"
)

// DeepThinkingTool provides system-level analysis and design capabilities.
// It uses an isolated LLM call with a specialized system prompt for deep,
// structured analysis of complex problems. This tool is EXPENSIVE and should
// only be used after conventional methods have been exhausted.
type DeepThinkingTool struct {
	LLM llm.Engine
}

// NewDeepThinkingTool creates a new DeepThinkingTool with the given LLM client.
func NewDeepThinkingTool(llm llm.Engine) *DeepThinkingTool {
	return &DeepThinkingTool{LLM: llm}
}

// Execute performs deep system analysis using an isolated LLM call.
// It takes a problem context and a goal, then returns a comprehensive solution.
//
// Parameters:
//   - context: The full problem context including execution environment feedback,
//     key errors, problem background, and any relevant constraints.
//   - goal: The specific objective to achieve.
//
// Returns a structured analysis and solution plan.
func (t *DeepThinkingTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	problemContext, ok := params["context"].(string)
	if !ok || problemContext == "" {
		return nil, fmt.Errorf("context parameter is required and must be a non-empty string")
	}

	goal, ok := params["goal"].(string)
	if !ok || goal == "" {
		return nil, fmt.Errorf("goal parameter is required and must be a non-empty string")
	}

	systemPrompt := getDeepThinkingSystemPrompt()

	task := fmt.Sprintf(
		"# Problem Context\n\n%s\n\n---\n\n# Goal\n\n%s\n\n---\n\nPlease perform a thorough system analysis and provide a comprehensive solution following the structure defined in your system prompt.",
		problemContext,
		goal,
	)

	messages := []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: systemPrompt,
		},
		{
			Role:    llm.RoleUser,
			Content: task,
		},
	}

	// 脱离父级 deadline（executor 中硬编码的 60s 工具超时）但保留取消信号
	// deepthinking 是长时间运行的深度分析，需要更长的超时
	llmCtx := context.WithoutCancel(ctx)
	llmCtx, llmCancel := context.WithTimeout(llmCtx, 120*time.Second)
	defer llmCancel()

	resp, err := t.LLM.GenerateContent(llmCtx, messages, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("deepthinking LLM call failed: %w", err)
	}

	if len(resp.Choices) > 0 {
		return resp.Choices[0].Content, nil
	}
	return "", nil
}

// getDeepThinkingSystemPrompt returns the specialized system prompt for deep analysis.
func getDeepThinkingSystemPrompt() string {
	return `# Role
You are a **Deep Thinking Engine** — an elite system analyst and solution architect. You are activated only for the most challenging problems that cannot be solved by conventional methods.

# Your Mission
Perform exhaustive, multi-dimensional analysis of the given problem context and produce a comprehensive, actionable solution.

# Analysis Framework
You MUST structure your response using the following framework:

## 1. Root Cause Analysis
- Identify the fundamental problem, not just symptoms
- Trace the causal chain from observed failures back to root causes
- Distinguish between primary and secondary issues

## 2. System-Level Impact Assessment
- Map all affected components and their interdependencies
- Evaluate the blast radius: what else could break?
- Identify cascading effects and hidden risks

## 3. Constraint Analysis
- Technical constraints (language, framework, platform, performance)
- Resource constraints (time, compute, memory, budget)
- Organizational constraints (team capabilities, existing architecture, policies)
- Risk constraints (security, reliability, data integrity)

## 4. Solution Design
- Propose 2-3 candidate solutions with clear pros/cons for each
- For each solution, provide:
  * Approach overview
  * Estimated effort (Low/Medium/High)
  * Risk level (Low/Medium/High)
  * Trade-offs and implications

## 5. Recommended Solution — Detailed Plan
- Select the best solution and justify the choice
- Provide a step-by-step implementation plan
- For each step: what to do, which files/components to modify, what to verify
- Include specific code patterns, architectural diagrams (in text), or pseudocode where helpful

## 6. Verification Strategy
- How to verify the solution works correctly
- Test cases and edge cases to consider
- Rollback plan if the solution fails

# Critical Rules
- Be thorough and systematic — this is an expensive call, make it count
- Ground ALL analysis in the provided context — do not hallucinate
- Provide concrete, actionable recommendations — not vague advice
- Consider long-term maintainability and scalability
- Flag any assumptions you are making explicitly
`
}
