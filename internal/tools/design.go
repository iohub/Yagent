package tools

import (
	"context"
	"fmt"
	"sync"

	"yagent/internal/llm"
	"yagent/internal/util"
)

// DesignTool produces an agent-friendly implementation design BEFORE coding.
// It uses an isolated LLM call with a specialized system prompt to turn an
// explored task into a concise, solution-level design (goal & scope, files,
// ordered steps, risks & verification) that the Coding-Agent can follow
// directly during the Implement phase. It is proactive (runs before coding),
// in contrast to DeepThinkingTool which is reactive (used when conventional
// methods have failed).
type DesignTool struct {
	// engineMu 保护 engine：TUI 现在能在任务运行中切 provider，而 SetEngine 与 Execute 不在同一线程。
	engineMu sync.RWMutex
	engine   llm.Engine

	StreamHandler llm.StreamHandler // 可选的流式处理器，如果设置则使用流式推理
}

// NewDesignTool creates a new DesignTool with the given LLM client.
func NewDesignTool(engine llm.Engine) *DesignTool {
	return &DesignTool{engine: engine}
}

// SetEngine 运行时替换推理引擎（provider 切换的热替换路径）。
func (t *DesignTool) SetEngine(e llm.Engine) {
	t.engineMu.Lock()
	t.engine = e
	t.engineMu.Unlock()
}

func (t *DesignTool) currentEngine() llm.Engine {
	t.engineMu.RLock()
	defer t.engineMu.RUnlock()
	return t.engine
}

// Execute performs solution design using an isolated LLM call.
// It takes the task context (requirements, explored codebase findings,
// constraints) and a design goal, then returns a solution-level implementation
// plan (goal & scope, files, ordered steps, risks & verification).
//
// Parameters:
//   - context: The task requirements, explored codebase findings (key file
//     paths, types/functions, existing patterns), and constraints.
//   - goal: What the design must decide or produce.
//
// Returns a concise, solution-level implementation design.
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
		"# Task Context\n\n%s\n\n---\n\n# Design Goal\n\n%s\n\n---\n\nProduce the solution-level design defined in your system prompt. Keep it brief.",
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

	// 预算取 [tools.llm.design].timeout（装配层注入 CogToolBudget），
	// 未配置时是 max(10m, 全局 LLM 超时)。
	budget := cogToolBudget("design")
	// 脱离父级 deadline 但保留取消信号：executor 给每个工具的 180s 看门狗不适用于
	// 一次完整方案设计，但用户按停止必须能立刻中断（旧代码用 WithoutCancel 丢掉了取消信号）。
	llmCtx, llmCancel := context.WithTimeout(util.DetachDeadline(ctx), budget)
	defer llmCancel()

	// 如果设置了 StreamHandler，则使用流式推理。
	// MaxTokens 是输出上限保险丝（见 cogPlanMaxTokens）：方案级设计只该写几百字，
	// 不设上限时模型会按 provider 默认额度把"理解代码"写成一篇报告，从而撞预算超时。
	opts := &llm.CallOptions{MaxTokens: cogPlanMaxTokens}
	if t.StreamHandler != nil {
		opts.StreamHandler = t.StreamHandler
	}

	resp, err := t.currentEngine().GenerateContent(llmCtx, messages, nil, opts)
	if err != nil {
		return nil, cogToolCallError("design", budget, err)
	}

	choice, err := resp.FirstChoice()
	if err != nil {
		return nil, fmt.Errorf("design: %w", err)
	}
	return choice.ContentWithNotice(), nil
}

// getDesignSystemPrompt returns the specialized system prompt for
// implementation design. The output targets the downstream Coding-Agent,
// not human documentation.
//
// 章节数是刻意压到四个的：旧文案的 7 个 MUST 章节里有一节要求"罗列关键文件路径、
// 类型/函数名与既有模式"，那一节的下场总是膨胀成一份代码阅读报告——既占篇幅又
// 撞 [tools.llm.design].timeout。真正要交给 Coding-Agent 的只有四件事：改什么、
// 改哪些文件、按什么顺序改、怎么验证。
func getDesignSystemPrompt() string {
	return `# Role
You are a **Solution Design Engine** — a senior architect producing the blueprint a downstream Coding-Agent will follow. You run BEFORE any code is written.

# Mission
Produce a **solution-level** design: what changes, in what order, and how to verify it. Directly executable, never granular.

# Output Contract
Target ~500 Chinese characters, hard ceiling 45 lines. Use exactly these four sections, in order:

## Goal & Scope
- One-sentence goal; in-scope and non-goals as short phrases (up to 2 lines total).

## Files
- One line per file: NEW|MODIFY <path> — one-sentence intent.
- Only files that actually change. No commentary, no quoting of code.

## Steps
- 3-8 numbered one-line steps: action + file(s) + verification point.
- Mark a dependency inline (e.g. after 2) only when it is not the obvious ordering.

## Risks & Verification
- Up to 3 bullets: blast radius / accepted trade-offs.
- The decisive verification (build/vet/test command) and one rollback line.

# Hard Rules
- DO NOT restate the task context or re-explain the codebase. Cite identifiers and paths only.
- NO code blocks, no pseudocode, no per-function breakdowns, no nested lists below one level, no repeated content across sections.
- Do NOT add sections such as "Background", "Summary", or "Next Steps", and do not write a separate recap of the code you read.
- Every file path must be concrete; every step must be actionable.
- If the context is insufficient, state it in ONE line starting with "Assumption:" — do not speculate at length.
- Brevity is part of correctness: if a section can be one line, it is one line.
`
}
