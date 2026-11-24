package tools

import (
	"context"
	"fmt"
	"sync"

	"yagent/internal/llm"
	"yagent/internal/util"
)

// DeepThinkingTool provides system-level analysis and design capabilities.
// It uses an isolated LLM call with a specialized system prompt for deep,
// structured analysis of complex problems. This tool is EXPENSIVE and should
// only be used after conventional methods have been exhausted.
type DeepThinkingTool struct {
	// engineMu 保护 engine：TUI 现在能在任务运行中切 provider，而 SetEngine 与 Execute 不在同一线程。
	engineMu sync.RWMutex
	engine   llm.Engine

	StreamHandler llm.StreamHandler // 可选的流式处理器，如果设置则使用流式推理
}

// NewDeepThinkingTool creates a new DeepThinkingTool with the given LLM client.
func NewDeepThinkingTool(llm llm.Engine) *DeepThinkingTool {
	return &DeepThinkingTool{engine: llm}
}

// SetEngine 运行时替换推理引擎（provider 切换的热替换路径）。
func (t *DeepThinkingTool) SetEngine(e llm.Engine) {
	t.engineMu.Lock()
	t.engine = e
	t.engineMu.Unlock()
}

func (t *DeepThinkingTool) currentEngine() llm.Engine {
	t.engineMu.RLock()
	defer t.engineMu.RUnlock()
	return t.engine
}

// Execute performs deep system analysis using an isolated LLM call.
// It takes a problem context and a goal, then returns a concise solution-level document.
//
// Parameters:
//   - context: The full problem context including execution environment feedback,
//     key errors, problem background, and any relevant constraints.
//   - goal: The specific objective to achieve.
//
// Returns a concise, solution-level analysis document.
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
		"# Problem Context\n\n%s\n\n---\n\n# Goal\n\n%s\n\n---\n\nProduce the solution-level analysis defined in your system prompt. Keep it brief.",
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

	// 预算取 [tools.llm.deepthinking].timeout（装配层注入 CogToolBudget），
	// 未配置时是 max(10m, 全局 LLM 超时)。
	budget := cogToolBudget("deepthinking")
	// 脱离父级 deadline 但保留取消信号：executor 给每个工具的 180s 看门狗不适用于
	// 长时间深度分析，但用户按停止必须能立刻中断（旧代码用 WithoutCancel 丢掉了取消信号）。
	llmCtx, llmCancel := context.WithTimeout(util.DetachDeadline(ctx), budget)
	defer llmCancel()

	// 如果设置了 StreamHandler，则使用流式推理。
	// MaxTokens 是输出上限保险丝（见 cogPlanMaxTokens）：方案级文档只该写几百字，
	// 不设上限时模型会按 provider 默认额度把分析写成长报告，从而撞预算超时。
	opts := &llm.CallOptions{MaxTokens: cogPlanMaxTokens}
	if t.StreamHandler != nil {
		opts.StreamHandler = t.StreamHandler
	}

	resp, err := t.currentEngine().GenerateContent(llmCtx, messages, nil, opts)
	if err != nil {
		return nil, cogToolCallError("deepthinking", budget, err)
	}

	choice, err := resp.FirstChoice()
	if err != nil {
		return nil, fmt.Errorf("deepthinking: %w", err)
	}
	return choice.ContentWithNotice(), nil
}

// getDeepThinkingSystemPrompt returns the specialized system prompt for deep analysis.
//
// 篇幅是这个工具的交付契约的一部分，不是风格偏好：旧文案强制 6 个 MUST 章节、
// 第 4 节还要 2-3 个候选方案各带 4 个子项，输出下限就是 8-12 段，于是"想得深"
// 被翻译成"写得多"，而后者会撞 [tools.llm.deepthinking].timeout，报错却是超时。
// 现在只有四个章节（Root Cause / Decision / Steps / Risks & Verification）。
// 以后想加章节先问一句：没有这一节，下游会不会做错那个决定？不会就别加。
func getDeepThinkingSystemPrompt() string {
	return `# Role
You are a **Deep Thinking Engine** — a senior architect invoked only when conventional methods (thinking, repo analysis, delegation) have failed.

# Mission
Produce a **solution-level** document: the root cause that matters, the decision, and the ordered plan to execute it. Nothing else.

# Output Contract
Target ~500 Chinese characters, hard ceiling 40 lines. Fewer sections are fine when the problem is simple. Use exactly these sections, in order:

## Root Cause
- Up to 3 bullets: symptom -> decisive root cause. Drop secondary issues that do not change the decision.

## Decision
- The recommended solution in 2-3 lines: what it is, why it wins.
- At most ONE rejected alternative, one line: why it was rejected.
- Effort / risk as tags (Low|Med|High), never as prose.

## Steps
- 3-7 numbered one-line steps: action + target component/file + verification point.
- Note a dependency inline only where it is not the obvious ordering.

## Risks & Verification
- Up to 3 bullets: blast radius, accepted trade-off, one rollback line.
- The single decisive verification (command or check).

# Hard Rules
- DO NOT restate or summarize the input context. Reference identifiers and paths only.
- NO code blocks, no pseudocode, no diffs, no file-by-file walkthroughs, no nested lists below one level.
- No preamble, no closing summary, no meta commentary about the analysis itself.
- If a decision needs information absent from the context, write exactly one line starting with "Assumption:" — do not speculate further.
- Ground everything in the provided context; never invent files, symbols, or APIs.
- Brevity is part of correctness: if a section can be one line, it is one line.
`
}
