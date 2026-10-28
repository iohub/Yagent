// planner_llm.go — Director 主循环里"一次 LLM 调用"的那段：带退避的重试、事件发布、
// provider 计量刷新，以及拿到响应之后的全部记账（token、rollout、ai_response 事件、
// ConversationMemory 的 assistant 消息、thinklink 的 Thought & Plan 条目）。
//
// 这些原本是 planner_run.go 循环体里的内联代码（重试段 173-300、响应记账 315-403），
// 搬出来是为了让主循环只剩骨架 —— 行数守卫见 scripts/check_file_sizes.sh。
// **搬迁是逐字的**：重试次数与退避上限、四条事件的发布顺序、RecordLLMSuccess/Failure
// 与"查询即试探"的配对、以及计量口径（Refresh 取的是**外层** messages 的长度，不是
// 送出去的那份）都不许在这里改。
//
// 唯一的形式差别是那份 Run 局部状态改用 *plannerStep 传递（定义在 planner_run.go）：
// messages 与 measured 必须写回主循环那一份，否则同一步的下一步就看不到它们。

package director

import (
	"context"
	"log/slog"
	"time"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// 工具声明每步重取，而不是整轮冻结一次：deferred schema 取回后、以及 Meta 在运行期
// 注册 Custom Agent 之后，都要在下一步就对模型可见。返回 nil 表示本轮没有工具。
func (p *Planner) toolsForStep() []llm.ToolDef {
	if p.cfg.Tools == nil {
		return nil
	}
	defs := p.cfg.Tools.Specs()
	tools.SortToolDefs(defs)
	return defs
}

// callLLMWithRetry 是这一步的 LLM 调用：至多重试 p.cfg.Recovery.LLMRetries() 次，
// 指数退避（上限 30 秒），每次重试等待都可以被 ctx 取消打断。
//
// 第三个返回值 aborted=true 表示"取消发生在重试等待里"：那时本函数已经按 cancelled
// 写好终止事件，Run 必须原样以 StopReason "cancelled" + ctx.Err() 返回 —— 它与
// llm_error 那条出口不是同一个停点，恢复侧读的是 reason 字符串。
//
// 熔断的配对契约在这里维持：每一步都"查询（planner_run.go）→ 调用（这里）→ 回报
// （这里最后那段 RecordLLMSuccess/RecordLLMFailure）"，多一个只查不报的调用点会消费掉
// half-open 的那一次试探（见本包 AGENTS.md 硬约束 11）。
func (p *Planner) callLLMWithRetry(step *plannerStep, i int) (resp *llm.Response, llmErr error, aborted bool) {
	maxRetries := 0
	if p.cfg.Recovery != nil {
		maxRetries = p.cfg.Recovery.LLMRetries()
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<(attempt-1)) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			slog.Warn("DirectorAgent retrying LLM call", "step", i, "attempt", attempt, "wait", wait)
			select {
			case <-step.ctx.Done():
				step.writeAborted(step.ctx.Err().Error(), StopKindOf(step.ctx.Err(), step.ctx))
				return nil, nil, true
			case <-time.After(wait):
			}
		}

		if p.cfg.NormalizeMessages != nil {
			*step.messages = p.cfg.NormalizeMessages(*step.messages)
		}

		// 三档压缩都在 planner_compress.go：判据只有 compression.OverThreshold 一处，
		// 别再把它抄回循环里。计量口径要穿过去、也要传回来（重写列表后必须作废）。
		*step.messages, step.measured = p.applyContextCompression(step.ctx, *step.messages, step.measured, step.mem, step.writeCompacted)

		slog.Debug("DirectorAgent calling LLM", "step", i, "messages", *step.messages)

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
		llmCtx, llmCancel := context.WithTimeout(step.ctx, p.cfg.LLMTimeout)
		resp, llmErr = p.cfg.LLM.GenerateContent(llmCtx, p.messagesForLLMCall(*step.messages), p.toolsForStep(), opts)
		llmCancel()

		// 计量刷新点。sentLen 取**外层** messages 的长度：送出去的那份还末尾附了便签索引，
		// 它不在外层列表里 —— 偏差方向是更早触发，与工具声明同源（见 compression.Prefix）。
		step.measured = step.measured.Refresh(resp, llmErr, len(*step.messages))
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

	return resp, llmErr, false
}

// accountAssistantResponse 把这一句 assistant 回复落到该落的所有地方，不决定"要不要停"
// （停不停是 planner_delegate.go 的收尾判定）。
//
// token 有两个来源：provider 给了 usage 就用它，没给才按 EstimateTokensFn 估算 ——
// 估算路径必须连 `estimated: true` 一起写进事件，否则 TUI 与 rollout 会把估算值读成实测值。
func (p *Planner) accountAssistantResponse(step *plannerStep, i int, resp *llm.Response, choice llm.Choice, assistantMsg llm.Message) {
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
		for _, msg := range *step.messages {
			estPrompt += estimateTokens(msg.Content)
		}
		estCompletion := estimateTokens(choice.Content)
		promptTokens = int64(estPrompt)
		completionTokens = int64(estCompletion)
		totalTokens = promptTokens + completionTokens
	}

	if step.rollout != nil && step.rollout.Enabled() {
		if writeErr := step.rollout.WriteTokenCount(promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens); writeErr != nil {
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

	if step.mem != nil {
		var toolCallData []memory.ToolCallData
		if p.cfg.ConvertToolCallsFn != nil {
			toolCallData = p.cfg.ConvertToolCallsFn(choice.ToolCalls)
		}
		step.mem.AddAssistantMessage(assistantMsg.Content, toolCallData)
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
				if step.rollout != nil && step.rollout.Enabled() {
					_ = step.rollout.WriteEventMsg(memory.EventMsg{
						Type:       "thinklink_entry",
						Kind:       entry.Kind.String(),
						Text:       entry.Content,
						StepNumber: entry.Step,
					})
				}
			}
		}
	}

	*step.messages = append(*step.messages, assistantMsg)
	step.journal.writeMessage(assistantMsg)
}
