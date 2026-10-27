package director

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"yagent/internal/agents/toolbatch"
	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/pause"
	"yagent/internal/tools"
)

// MaxNonDelegationPrompts 是没委派任何子 Agent 时允许注入"强制委派"提醒的次数。
// 导出是为了让门面层与测试引用同一个值（agents 里曾有一份只有测试在读的同名常量）。
const MaxNonDelegationPrompts = 3

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

	// 工具声明每步重取，而不是整轮冻结一次：deferred schema 取回后、以及 Meta 在运行期
	// 注册 Custom Agent 之后，都要在下一步就对模型可见。返回 nil 表示本轮没有工具。
	defsForStep := func() []llm.ToolDef {
		if p.cfg.Tools == nil {
			return nil
		}
		defs := p.cfg.Tools.Specs()
		tools.SortToolDefs(defs)
		return defs
	}

	if p.cfg.Publisher != nil {
		_ = p.cfg.Publisher.Publish("model_info", map[string]interface{}{
			"model": p.cfg.LLM.Model(),
			"agent": "director",
		}, "director")
	}

	writeDirectorRollout := func(msg llm.Message) {
		if directorRolloutWriter == nil || !directorRolloutWriter.Enabled() {
			return
		}
		if !directorRolloutWriter.SessionMetaWritten() {
			cwd, _ := os.Getwd()
			directorRolloutWriter.WriteSessionMeta(memory.SessionMeta{
				ID:          directorRolloutWriter.SessionID(),
				SessionID:   directorRolloutWriter.SessionID(),
				Cwd:         cwd,
				Originator:  "yagent",
				Source:      "cli",
				HistoryMode: "standard",
			})
			turnID := directorRolloutWriter.NextTurn()
			directorModel := ""
			if p.cfg.LLM != nil {
				directorModel = p.cfg.LLM.Model()
			}
			directorRolloutWriter.WriteTurnContext(memory.TurnContext{
				TurnID:            turnID,
				Cwd:               cwd,
				Model:             directorModel,
				Effort:            "medium",
				CollaborationMode: "director",
			})
			directorRolloutWriter.WriteEventMsg(memory.EventMsg{
				Type: "task_started",
			})
		}
		msgID := directorRolloutWriter.NextMessageID()
		items := memory.LLMMessageToResponseItems(msg, msgID)
		for _, item := range items {
			if err := directorRolloutWriter.WriteResponseItem(item); err != nil {
				slog.Warn("Rollout: failed to write director message", "error", err)
			}
		}
	}

	journal := directorJournal{ctx: ctx, p: p, rw: directorRolloutWriter}
	writeComplete := func(stopReason string) { journal.complete(stopReason) }
	writeAborted := func(reason, stopReason string) { journal.aborted(reason, stopReason) }
	writeCompacted := func(kind, summary string, before, after, saved int) {
		journal.compacted(kind, summary, before, after, saved)
	}

	initialMsgCount := len(messages)
	for i := 0; i < initialMsgCount; i++ {
		writeDirectorRollout(messages[i])
	}

	var measured compression.Prefix
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
			slog.Error("Circuit breaker open, too many consecutive LLM failures",
				"step", i)
			writeAborted("circuit breaker open", memory.StopKindCircuitBreakerOpen)
			return PlanResult{Steps: p.state.Step, StopReason: "circuit_breaker_open"},
				fmt.Errorf("circuit breaker open: LLM calls blocked")
		}

		maxRetries := 0
		if p.cfg.Recovery != nil {
			maxRetries = p.cfg.Recovery.LLMRetries()
		}
		var resp *llm.Response
		var llmErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				wait := time.Duration(1<<(attempt-1)) * time.Second
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				slog.Warn("DirectorAgent retrying LLM call", "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					writeAborted(ctx.Err().Error(), StopKindOf(ctx.Err(), ctx))
					return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, ctx.Err()
				case <-time.After(wait):
				}
			}

			if p.cfg.NormalizeMessages != nil {
				messages = p.cfg.NormalizeMessages(messages)
			}

			// 三档压缩都在 planner_compress.go：判据只有 compression.OverThreshold 一处，
			// 别再把它抄回循环里。计量口径要穿过去、也要传回来（重写列表后必须作废）。
			messages, measured = p.applyContextCompression(ctx, messages, measured, in.Mem, writeCompacted)

			slog.Debug("DirectorAgent calling LLM", "step", i, "messages", messages)

			opts := &llm.CallOptions{}
			if p.cfg.Publisher != nil {
				opts.StreamHandler = func(ctx context.Context, chunk []byte) error {
					if len(chunk) > 0 {
						_ = p.cfg.Publisher.Publish("ai_chunk", map[string]interface{}{
							"content": string(chunk),
							"agent":   "director",
						}, "director")
					}
					return nil
				}
			}

			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": p.cfg.LLM.Model(),
					"agent": "director",
				}, "director")
			}

			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("ai_stream_start", map[string]interface{}{
					"agent": "director",
				}, "director")
			}

			llmStartTime := time.Now()
			llmCtx, llmCancel := context.WithTimeout(ctx, p.cfg.LLMTimeout)
			resp, llmErr = p.cfg.LLM.GenerateContent(llmCtx, p.messagesForLLMCall(messages), defsForStep(), opts)
			llmCancel()

			// 计量刷新点。sentLen 取**外层** messages 的长度：送出去的那份还末尾附了便签索引，
			// 它不在外层列表里 —— 偏差方向是更早触发，与工具声明同源（见 compression.Prefix）。
			measured = measured.Refresh(resp, llmErr, len(messages))
			llmDuration := time.Since(llmStartTime).Seconds()

			if p.cfg.Publisher != nil {
				metadata := map[string]interface{}{
					"agent": "director",
				}
				if llmErr == nil && resp != nil && resp.Usage != nil {
					metadata["usage"] = map[string]interface{}{
						"prompt_tokens":               resp.Usage.PromptTokens,
						"completion_tokens":           resp.Usage.CompletionTokens,
						"total_tokens":                resp.Usage.TotalTokens,
						"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
						"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
						"total_input_tokens":          resp.Usage.TotalInputTokens,
					}
				}
				_ = p.cfg.Publisher.PublishWithMetadata("ai_stream_end", "", "director", metadata)
			}

			if p.cfg.Metrics != nil {
				p.cfg.Metrics.RecordLLMDuration(time.Since(llmStartTime))
			}

			if llmErr == nil && p.cfg.Publisher != nil {
				if c, cErr := resp.FirstChoice(); cErr == nil && c.Reasoning != "" {
					_ = p.cfg.Publisher.Publish("thinking", map[string]interface{}{
						"content": c.Reasoning,
						"model":   p.cfg.LLM.Model(),
						"agent":   "director",
					}, "director")
				}
			}

			if p.cfg.Publisher != nil {
				metadata := map[string]interface{}{
					"model":            p.cfg.LLM.Model(),
					"agent":            "director",
					"duration_seconds": llmDuration,
				}
				if llmErr != nil {
					metadata["error"] = llmErr.Error()
				}
				_ = p.cfg.Publisher.PublishWithMetadata("llm_call_end", "", "director", metadata)
			}

			if llmErr == nil {
				if p.cfg.Recovery != nil {
					p.cfg.Recovery.RecordLLMSuccess()
				}
				break
			}
			if p.cfg.Recovery != nil {
				p.cfg.Recovery.RecordLLMFailure()
			}
			consecutiveFailures := 0
			if p.cfg.Recovery != nil {
				p.cfg.Recovery.RecordLLMFailureStats()
				consecutiveFailures = p.cfg.Recovery.ConsecutiveLLMFailures()
			}
			slog.Warn("DirectorAgent LLM error, will retry",
				"error", llmErr, "step", i, "attempt", attempt,
				"consecutive_failures", consecutiveFailures)
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
		slog.Debug("DirectorAgent LLM response", "step", i, "content", choice.Content, "tool_calls", len(choice.ToolCalls))

		var promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens int64
		if resp.Usage != nil {
			promptTokens = resp.Usage.PromptTokens
			completionTokens = resp.Usage.CompletionTokens
			totalTokens = resp.Usage.TotalTokens
			cacheCreationTokens = resp.Usage.CacheCreationInputTokens
			cacheReadTokens = resp.Usage.CacheReadInputTokens
		} else {
			estimateTokens := func(text string) int {
				if p.cfg.EstimateTokensFn != nil {
					return p.cfg.EstimateTokensFn(text)
				}
				return 0
			}
			estPrompt := 0
			for _, msg := range messages {
				estPrompt += estimateTokens(msg.Content)
			}
			estCompletion := estimateTokens(choice.Content)
			promptTokens = int64(estPrompt)
			completionTokens = int64(estCompletion)
			totalTokens = promptTokens + completionTokens
		}

		if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
			if writeErr := directorRolloutWriter.WriteTokenCount(promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens); writeErr != nil {
				slog.Warn("Rollout: failed to write director token_count", "error", writeErr)
			}
		}

		if p.cfg.Publisher != nil {
			metadata := map[string]interface{}{}
			if resp.Usage != nil {
				metadata["usage"] = map[string]interface{}{
					"prompt_tokens":               int(resp.Usage.PromptTokens),
					"completion_tokens":           int(resp.Usage.CompletionTokens),
					"total_tokens":                int(resp.Usage.TotalTokens),
					"cache_creation_input_tokens": int(resp.Usage.CacheCreationInputTokens),
					"cache_read_input_tokens":     int(resp.Usage.CacheReadInputTokens),
					"total_input_tokens":          int(resp.Usage.TotalInputTokens),
				}
			} else {
				metadata["usage"] = map[string]interface{}{
					"prompt_tokens":     int(promptTokens),
					"completion_tokens": int(completionTokens),
					"total_tokens":      int(totalTokens),
					"estimated":         true,
				}
			}
			_ = p.cfg.Publisher.PublishWithMetadata("ai_response", assistantMsg.Content, "director", metadata)
		}

		if in.Mem != nil {
			var toolCallData []memory.ToolCallData
			if p.cfg.ConvertToolCallsFn != nil {
				toolCallData = p.cfg.ConvertToolCallsFn(choice.ToolCalls)
			}
			in.Mem.AddAssistantMessage(assistantMsg.Content, toolCallData)
		}

		if p.cfg.Journal != nil {
			for _, block := range compression.ExtractThoughtAndPlanBlocks(choice.Content) {
				if entry, added := p.cfg.Journal.AddThoughtPlan(block, i); added {
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
		}

		messages = append(messages, assistantMsg)
		writeDirectorRollout(assistantMsg)
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
				messages = append(messages, userMsg)
				writeDirectorRollout(userMsg)
				slog.Debug("DirectorAgent force delegation via user message", "step", i, "prompt_count", p.state.NonDelegationPrompts)
				continue
			}
			writeComplete(memory.StopKindPlainText)
			return PlanResult{Text: assistantMsg.Content, Steps: p.state.Step, StopReason: "plain_text"}, nil
		}

		maxParallel := p.cfg.MaxParallelReadOnlyTools
		toolCalls := choice.ToolCalls
		plan := toolbatch.Plan(len(toolCalls), func(i int) bool {
			if p.cfg.IsParallelizableTool == nil {
				return false
			}
			return p.cfg.IsParallelizableTool(toolCalls[i].Function.Name)
		})

		type plannerToolOutcome struct {
			result    string
			cancelled bool
		}

		execPlannerToolCall := func(callCtx context.Context, i int) plannerToolOutcome {
			tc := toolCalls[i]
			var out plannerToolOutcome

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
			} else {
				out.result = toolResult
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

			if in.Mem != nil {
				in.Mem.AddToolMessage(out.result, tc.ID)
			}

			toolMsg := llm.Message{Role: llm.RoleTool, Content: out.result, ToolCallID: tc.ID, ToolName: tc.Function.Name}
			messages = append(messages, toolMsg)
			writeDirectorRollout(toolMsg)

			if tc.Function.Name == "agent_exit" {
				writeComplete(memory.StopKindAgentExit)
				return "agent_exit"
			}
			return ""
		}

		for _, seg := range plan {
			stop := ""
			toolbatch.Run(ctx, seg, maxParallel, execPlannerToolCall,
				func(i int, out plannerToolOutcome) {
					if stop != "" {
						return
					}
					stop = commitPlannerToolCall(i, out)
				})
			if stop == "cancelled" {
				writeAborted(ctx.Err().Error(), StopKindOf(ctx.Err(), ctx))
				return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, ctx.Err()
			}
			if stop == "agent_exit" {
				return PlanResult{Text: "Task completed successfully", Steps: p.state.Step, StopReason: "agent_exit"}, nil
			}
		}
	}

	writeAborted("DirectorAgent exceeded max steps", memory.StopKindMaxSteps)
	return PlanResult{Steps: p.state.Step, StopReason: "max_steps"}, fmt.Errorf("DirectorAgent exceeded max steps")
}
