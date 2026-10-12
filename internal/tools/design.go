package tools

import (
	"context"
	"fmt"
	"time"

	"yagent/internal/llm"
)

// DesignTool produces an agent-friendly implementation design BEFORE coding.
// It uses an isolated LLM call with a specialized system prompt to turn an
// explored task into a structured, step-by-step implementation plan that the
// Coding-Agent can follow directly during the Implement phase. It is
// proactive (runs before coding), in contrast to DeepThinkingTool which is
// reactive (used when conventional methods have failed).
type DesignTool struct {
	LLM           llm.Engine
	StreamHandler llm.StreamHandler // 可选的流式处理器，如果设置则使用流式推理
}

// NewDesignTool creates a new DesignTool with the given LLM client.
func NewDesignTool(engine llm.Engine) *DesignTool {
	return &DesignTool{LLM: engine}
}

// Execute performs solution design using an isolated LLM call.
// It takes the task context (requirements, explored codebase findings,
// constraints) and a design goal, then returns a structured implementation
// plan (goal/scope, affected files, ordered steps, risks, verification).
//
// Parameters:
//   - context: The task requirements, explored codebase findings (key file
//     paths, types/functions, existing patterns), and constraints.
//   - goal: What the design must decide or produce.
//
// Returns a structured implementation design.
func (t *DesignTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	problemContext, ok := params["context"].(string)
	if !ok || problemContext == "" {
		return nil, fmt.Errorf("context parameter is required and must be a non-empty string")
	}

	goal, ok := params["goal"].(string)
	if !ok || goal == "" {
		return nil, fmt.Errorf("goal parameter is required and must be a non-empty string")
	}

	systemPrompt := getDesignSystemPrompt()

	task := fmt.Sprintf(
		"# Task Context\n\n%s\n\n---\n\n# Design Goal\n\n%s\n\n---\n\nPlease produce an actionable implementation design following the structure defined in your system prompt.",
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

	// 脱离父级 deadline 但保留取消信号
	// design 是编码前的方案设计，同样可能耗时较长，需要预留超时
	llmCtx := context.WithoutCancel(ctx)
	llmCtx, llmCancel := context.WithTimeout(llmCtx, 10*time.Minute)
	defer llmCancel()

	// 如果设置了 StreamHandler，则使用流式推理
	opts := &llm.CallOptions{}
	if t.StreamHandler != nil {
		opts.StreamHandler = t.StreamHandler
	}

	resp, err := t.LLM.GenerateContent(llmCtx, messages, nil, opts)
	if err != nil {
		return nil, fmt.Errorf("design LLM call failed: %w", err)
	}

	if len(resp.Choices) > 0 {
		return resp.Choices[0].Content, nil
	}
	return "", nil
}

// getDesignSystemPrompt returns the specialized system prompt for
// implementation design. The output targets the downstream Coding-Agent,
// not human documentation.
func getDesignSystemPrompt() string {
	return `# Role
You are a **Solution Design Engine** — a senior software architect producing directly executable implementation designs for a downstream Coding-Agent. You run PROACTIVELY, BEFORE any code is written: your design is the blueprint the agent will follow during implementation.

# Your Mission
Turn the provided task context (requirements, explored codebase findings, constraints) into a precise, ordered implementation plan. Your output is consumed by an autonomous coding agent, not by humans.

# Design Framework
You MUST structure your response using the following sections, in order:

## Goal & Scope
- One-sentence goal, then explicit in-scope items and non-goals (what this design deliberately does NOT cover)

## Current Understanding
- Cite the key file paths, type/function names, and existing patterns from the context that this design builds on
- This section proves the design is grounded in the real codebase; NEVER speculate about code you have not seen

## Affected Files
- Distinguish NEW vs MODIFY, one line each: precise file path + one-sentence intent of the change

## Step-by-Step Implementation Plan
- Numbered, ordered steps; each step names: target file(s), the action (what to do), and the output/verification point
- Steps must state their dependency order explicitly (e.g., "step 2 depends on step 1")

## Risks & Trade-offs
- Potential blast radius, cascading effects, and trade-offs accepted by this design

## Verification Criteria
- How to verify (build/vet/tests/manual), edge cases, and the rollback plan if the design fails

## Assumptions
- List ALL assumptions explicitly; if the context is insufficient, state what is missing here

# Critical Rules
- NEVER output full code skeletons, actual code snippets, or pseudocode — describe changes in natural language, optionally referencing identifiers, file paths, and textual architecture diagrams. This keeps the output concise and token-efficient.
- Be imperative, specific, and executable — no vague advice
- Every affected file must be a precise path
- Ground ALL design decisions in the provided context — do not hallucinate; if the context is insufficient, state what is missing in Assumptions
- Keep output concise and structured for downstream agent consumption, not human documentation
`
}
