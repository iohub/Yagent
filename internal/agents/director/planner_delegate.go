// planner_delegate.go — 一步之内的收尾判定与委派编排。
//
// 原本是 planner_run.go 主循环末尾那两段（404-527）：没有 tool_calls 时决定"提醒还没委派"
// 还是"以 plain_text 收尾"，有 tool_calls 时按 toolbatch 的分段执行并逐条落账。
// 搬迁逐字，包括几条容易看丢的语义：
//   - 强制委派提醒是"配额 3 + 两条文案"，注入的是本步 messages 与 rollout，绝不写进
//     ConversationMemory（与便签索引同一条理由，见 AGENTS.md 硬约束 3/12）；配额用尽走
//     plain_text 正常返回，写成 error 会让上层把一次已经写完的回答当成失败重做。
//   - 是否收尾只认 exitpolicy.Terminate（本文件是 Director 侧唯一的调用点）：把判据内联回
//     循环就是第二份实现，闸门 TestTerminationDecisionIsNotInlinedInLoops 扫的是本文件。
//   - exec 闭包自己 recover 并把 failed 置 true：toolbatch 的 guardedExec 只能把 panic 折成
//     T 的零值，而零值回执恰好满足收尾判据，会把一次崩溃冒领成"任务完成"。
//   - toolbatch.Run 的分段顺序就是"写类元素执行窗口内没有任何只读元素在飞"的断言前提。
//
// cancelled 与 agent_exit 两条 return 的 StopReason 字符串是恢复侧读的值，不许与
// llm_error / max_steps 合并成同一个出口。

package director

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"yagent/internal/agents/exitpolicy"
	"yagent/internal/agents/toolbatch"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// plannerToolOutcome 是一次 tool_call 的执行回执（原本是 Run 里的局部类型）。
type plannerToolOutcome struct {
	result    string
	cancelled bool
	failed    bool // 这次工具执行报错了；失败那次的 agent_exit 不许冒领收尾（判据在 exitpolicy）
}

// finishStepOrDelegate 处理"模型这一句回复"之后的全部去向。
//
// 第三个返回值为 true 时 Run 必须立刻 return 前两个值 —— 终止事件（task_complete /
// turn_aborted）已由本函数认领，主循环不许再补写第二条。为 false 表示这一步跑完、
// 继续下一步（原本循环里那个 continue）。
func (p *Planner) finishStepOrDelegate(step *plannerStep, i int, choice llm.Choice, assistantMsg llm.Message) (PlanResult, error, bool) {
	if len(choice.ToolCalls) == 0 {
		if !p.state.HasDelegated && p.state.NonDelegationPrompts < MaxNonDelegationPrompts {
			p.state.NonDelegationPrompts++
			var forceMsg string
			if p.state.NonDelegationPrompts == 1 {
				forceMsg = "You must delegate an agent to complete the task. Do not reply with plain text — call a delegate_* tool now."
			} else {
				forceMsg = "You still have not delegated any agent. You MUST call a delegate_* tool to complete the task before responding."
			}
			userMsg := llm.Message{Role: llm.RoleUser, Content: forceMsg}
			*step.messages = append(*step.messages, userMsg)
			step.journal.writeMessage(userMsg)
			slog.Debug("DirectorAgent force delegation via user message", "step", i, "prompt_count", p.state.NonDelegationPrompts)
			return PlanResult{}, nil, false
		}
		step.writeComplete(memory.StopKindPlainText)
		return PlanResult{Text: assistantMsg.Content, Steps: p.state.Step, StopReason: "plain_text"}, nil, true
	}

	maxParallel := p.cfg.MaxParallelReadOnlyTools
	toolCalls := choice.ToolCalls
	plan := toolbatch.Plan(len(toolCalls), func(i int) bool {
		if p.cfg.IsParallelizableTool == nil {
			return false
		}
		return p.cfg.IsParallelizableTool(toolCalls[i].Function.Name)
	})

	// 具名返回 + 自己 recover：toolbatch 的 guardedExec 只能把 panic 折成 T 的**零值**
	// （result=""、failed=false），而零值回执会满足下面 exitpolicy 的收尾判据、把一次
	// 崩溃冒领成"任务完成"。泛型签名 Run[T any] 决定了那一层无法给任意 T 的 failed
	// 赋值，所以这道防线只能在业务 exec 闭包里建（与 executor.go 的并行闭包同形）。
	execPlannerToolCall := func(callCtx context.Context, i int) (out plannerToolOutcome) {
		defer func() {
			if r := recover(); r != nil {
				out = plannerToolOutcome{result: fmt.Sprintf("Error: panic: %v", r), failed: true}
			}
		}()
		tc := toolCalls[i]

		if p.cfg.Publisher != nil {
			_ = p.cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
				"tool_name":    tc.Function.Name,
				"arguments":    tc.Function.Arguments,
				"tool_call_id": tc.ID,
			}, "director")
		}
		if callCtx.Err() != nil {
			out.cancelled = true
			return out
		}

		// 是否可执行由 runner 按注册表判定（未注册 → "Tool x not found"，
		// 冷工具未取回 → 回吐 schema），这里不再拿一份可能已过时的声明快照当门控。
		toolResult, err := p.cfg.Tools.Call(callCtx, tc.Function.Name, tc.Function.Arguments, tc.ID)
		out.result, out.failed = toolResult, err != nil
		if err != nil {
			errMsg := err.Error()
			if len(errMsg) > 1000 {
				errMsg = errMsg[:1000] + "... [truncated]"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				out.result = fmt.Sprintf("Error: tool execution timed out after %d seconds", int(p.cfg.ToolTimeout.Seconds()))
			} else {
				out.result = fmt.Sprintf("Error: %s", errMsg)
			}
		}
		return out
	}

	commitPlannerToolCall := func(i int, out plannerToolOutcome) string {
		tc := toolCalls[i]
		if out.cancelled {
			return "cancelled"
		}
		// 残缺参数会被工具层当成裸字符串执行，回执不点名截断的话模型只看到一条陌生的参数错误。
		out.result += choice.ToolCallNote(tc)

		if p.cfg.Publisher != nil {
			_ = p.cfg.Publisher.Publish("tool_call_result", map[string]interface{}{
				"tool_name":    tc.Function.Name,
				"result":       out.result,
				"tool_call_id": tc.ID,
			}, "director")
		}

		if step.mem != nil {
			step.mem.AddToolMessage(out.result, tc.ID)
		}

		toolMsg := llm.Message{Role: llm.RoleTool, Content: out.result, ToolCallID: tc.ID, ToolName: tc.Function.Name}
		*step.messages = append(*step.messages, toolMsg)
		step.journal.writeMessage(toolMsg)

		if exitpolicy.Terminate(tc.Function.Name, out.failed) {
			step.writeComplete(memory.StopKindAgentExit)
			return memory.StopKindAgentExit
		}
		return ""
	}

	for _, seg := range plan {
		stop := ""
		toolbatch.Run(step.ctx, seg, maxParallel, execPlannerToolCall,
			func(i int, out plannerToolOutcome) {
				if stop != "" {
					return
				}
				stop = commitPlannerToolCall(i, out)
			})
		if stop == "cancelled" {
			step.writeAborted(step.ctx.Err().Error(), StopKindOf(step.ctx.Err(), step.ctx))
			return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, step.ctx.Err(), true
		}
		if stop == memory.StopKindAgentExit {
			return PlanResult{Text: "Task completed successfully", Steps: p.state.Step, StopReason: stop}, nil, true
		}
	}

	return PlanResult{}, nil, false
}
