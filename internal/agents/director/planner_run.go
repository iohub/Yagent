package director

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
)

// MaxNonDelegationPrompts 是没委派任何子 Agent 时允许注入"强制委派"提醒的次数。
// 导出是为了让门面层与测试引用同一个值（agents 里曾有一份只有测试在读的同名常量）。
const MaxNonDelegationPrompts = 3

// plannerStep 是主循环交给"一步之内"的两个协作者的那份局部状态：
// planner_llm.go（一次 LLM 调用与它的响应记账）与 planner_delegate.go（收尾判定与
// tool_calls 编排）原先都内联在 Run 里、靠闭包捕获这些变量。
//
// messages 与 journal 必须是指针：前者因为压缩会把列表整段重写、tool 回执要追加进主循环
// 那一份（值拷贝会把追加丢掉，下一步的模型就看不到工具结果）；后者因为 terminal 是"这一轮
// 谁认领了终止事件"的唯一账本，指针接收者落在副本上等于没记（见 planner_rollout.go）。
// measured 跨步存活，所以是字段而不是参数。
type plannerStep struct {
	ctx            context.Context
	mem            *memory.ConversationMemory
	journal        *directorJournal
	rollout        *memory.RolloutWriter
	messages       *[]llm.Message
	measured       compression.Prefix
	writeComplete  func(stopReason string)
	writeAborted   func(reason, stopReason string)
	writeCompacted func(kind, summary string, before, after, saved int)
}

func (p *Planner) Run(ctx context.Context, in PlanInput) (PlanResult, error) {
	p.state.HasDelegated = false
	p.state.NonDelegationPrompts = 0

	var directorRolloutWriter *memory.RolloutWriter
	if rw := p.cfg.Rollout; rw != nil {
		directorRolloutWriter = rw
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	// journal 是这一轮所有落盘动作的唯一出口：消息、压缩快照、终止事件。
	//
	// 兜底 defer 必须注册在**任何 return 之前**（这里是能选的最早点：writer 一建好就登记），
	// 否则注册点之前的 return 依旧绕过它 —— 与子 Agent 侧 executor.go 的单一 defer 收口对齐。
	// 提到 Prompts 之前的现成收益：装配期失败（拿不到 system prompt）以前一条终止事件都不写，
	// rollout 看起来像"被切断"，而不是"根本没跑起来"。
	// 上面那个 rollout Close 是本函数第一个 defer，LIFO 保证兜底先写、再关文件。
	journal := directorJournal{ctx: ctx, p: p, rw: directorRolloutWriter}
	defer journal.abortUnclaimed()
	writeComplete := func(stopReason string) { journal.complete(stopReason) }
	writeAborted := func(reason, stopReason string) { journal.aborted(reason, stopReason) }
	writeCompacted := func(kind, summary string, before, after, saved int) {
		journal.compacted(kind, summary, before, after, saved)
	}

	if p.cfg.Journal != nil {
		if entry, added := p.cfg.Journal.AddUserInput(in.Input, 0); added {
			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("thinklink_entry", map[string]interface{}{
					"id":        entry.ID,
					"kind":      entry.Kind.String(),
					"agent":     "Director",
					"content":   entry.Content,
					"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
					"step":      entry.Step,
				}, "director")
			}
			if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
				_ = directorRolloutWriter.WriteEventMsg(memory.EventMsg{
					Type:       "thinklink_entry",
					Kind:       entry.Kind.String(),
					Text:       entry.Content,
					StepNumber: entry.Step,
				})
			}
		}
	}

	// firstConversation = 「本轮开始时 memory 里还没有任何 AI 回应」＝新任务首轮：只在这一轮注入项目上下文，追问不重复注入。
	// 快照必须取在 seedHumanInput **之前**（判据问的是本轮开始时的状态，写完之后就把本轮自己算进去了，勿改回）。
	// HTTP/WebUI 提交前预写的那条 human 消息不影响判定，理由见 startsFirstTurn（planner_memory.go）。
	firstConversation := startsFirstTurn(in.Mem)
	seedHumanInput(in.Mem, in.Input)

	var messages []llm.Message

	systemPrompt, err := p.cfg.Prompts(ctx, PromptInput{
		Input:               in.Input,
		FirstConversation:   firstConversation,
		ForceProjectContext: in.Resumed,
	})
	if err != nil {
		return PlanResult{}, err
	}

	messages = append(messages, llm.Message{
		Role:    llm.RoleSystem,
		Content: systemPrompt,
	})

	if in.Mem != nil {
		for _, m := range in.Mem.GetMessages() {
			if m.Type == memory.MessageTypeSystem {
				continue
			}
			if m.IsSubAgent {
				continue
			}
			messages = append(messages, memory.ConvertMemoryMessageToLLMSMessage(m))
		}
	} else {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: in.Input,
		})
	}

	// 恢复的收尾（§4.3 / D6）：把重放结果作为**原 call_id 的第二条** tool output 追加。
	// 取消那一刻 Director 已经把 "Error: context canceled" 记进了 memory 与 rollout；
	// reader 与 LLM 侧都是 last-wins，所以这条真实结果会把它顶掉 ——
	// 模型看到的是"这次委派成功了"，它完全不需要知道中间停过。
	// 不调 LLM，所以恢复本身是确定性的、零 token 成本的。
	if res := in.PendingResumption; res != nil && res.DelegationID != "" {
		if in.Mem != nil {
			in.Mem.AddToolMessage(res.Result, res.DelegationID)
		}
		messages = append(messages, llm.Message{
			Role:       llm.RoleTool,
			Content:    res.Result,
			ToolCallID: res.DelegationID,
			ToolName:   res.ToolName,
		})
	}

	if p.cfg.Publisher != nil {
		_ = p.cfg.Publisher.Publish("model_info", map[string]interface{}{
			"model": p.cfg.LLM.Model(),
			"agent": "director",
		}, "director")
	}

	initialMsgCount := len(messages)
	for i := 0; i < initialMsgCount; i++ {
		journal.writeMessage(messages[i])
	}

	// 一步之内的三段职责按文件切分：LLM 调用与响应记账在 planner_llm.go，
	// 收尾判定与委派编排在 planner_delegate.go。它们共用下面这份状态束。
	step := &plannerStep{
		ctx: ctx, mem: in.Mem, journal: &journal, rollout: directorRolloutWriter,
		messages: &messages, writeComplete: writeComplete,
		writeAborted: writeAborted, writeCompacted: writeCompacted,
	}

	for i := 0; i < p.cfg.MaxSteps; i++ {
		// 暂停闸门（A 层）与子 Agent 用同一个：ctx 不取消，停在 step 边界，
		// 放开后原地继续。p.state.Step 不推进 —— 这一步根本没跑过，
		// 推进会让恢复侧的游标多算一步。
		if perr := pause.From(ctx).Wait(ctx); perr != nil {
			writeAborted(perr.Error(), memory.StopKindPaused)
			return PlanResult{Steps: p.state.Step, StopReason: memory.StopKindPaused}, perr
		}

		p.state.Step = i + 1

		if p.cfg.Recovery != nil && p.cfg.Recovery.IsCircuitBreakerOpen() {
			slog.Error("Circuit breaker open, too many consecutive LLM failures", "step", i)
			writeAborted("circuit breaker open", memory.StopKindCircuitBreakerOpen)
			return PlanResult{Steps: p.state.Step, StopReason: "circuit_breaker_open"},
				fmt.Errorf("circuit breaker open: LLM calls blocked")
		}

		// 重试、事件发布、计量刷新与压缩都在 planner_llm.go。aborted 表示取消打断的是重试
		// 等待：终止事件已按 cancelled 写好，这里只负责出口（与 llm_error 不是同一个停点）。
		resp, llmErr, aborted := p.callLLMWithRetry(step, i)
		if aborted {
			return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, ctx.Err()
		}

		if llmErr != nil {
			slog.Error("DirectorAgent LLM error after all retries",
				"error", llmErr, "step", i)
			writeAborted(llmErr.Error(), StopKindOf(llmErr, ctx))
			return PlanResult{Steps: p.state.Step, StopReason: "llm_error"}, llmErr
		}

		choice, choiceErr := resp.FirstChoice()
		if choiceErr != nil {
			writeAborted(choiceErr.Error(), StopKindOf(choiceErr, ctx))
			return PlanResult{Steps: p.state.Step, StopReason: "llm_error"}, choiceErr
		}
		assistantMsg := choice.AssistantMessage()

		// token/rollout/事件/memory/thinklink 的记账（planner_llm.go）
		p.accountAssistantResponse(step, i, resp, choice, assistantMsg)

		// 未委派提醒、纯文本收尾与 tool_calls 编排（planner_delegate.go）：
		// finished 时终止事件已由它认领，主循环直接出口。
		if result, err, finished := p.finishStepOrDelegate(step, i, choice, assistantMsg); finished {
			return result, err
		}
	}

	writeAborted("DirectorAgent exceeded max steps", memory.StopKindMaxSteps)
	return PlanResult{Steps: p.state.Step, StopReason: "max_steps"}, fmt.Errorf("DirectorAgent exceeded max steps")
}
