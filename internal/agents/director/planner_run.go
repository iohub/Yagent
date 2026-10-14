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

	if in.Mem != nil {
		lastMsg := in.Mem.GetLastMessage()
		if lastMsg == nil || lastMsg.Content != in.Input || lastMsg.Type != memory.MessageTypeHuman {
			in.Mem.AddHumanMessage(in.Input)
		}
	}

	var messages []llm.Message

	systemPrompt, err := p.cfg.Prompts(ctx, PromptInput{
		Input:               in.Input,
		FirstConversation:   in.Mem == nil || len(in.Mem.GetMessages()) == 0,
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

	writeComplete := func(stopReason string) {
		if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
			directorRolloutWriter.WriteRunComplete(p.runState(stopReason))
		}
	}
	writeAborted := func(reason, stopReason string) {
		if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
			directorRolloutWriter.WriteRunAborted(reason, p.runState(stopReason))
		}
	}
	// kind 与 savedTokens 是给 /resume 重建 ⧉ 压缩段用的；Summary 的文案不动
	// （历史恢复与人工排查都依赖它）。
	writeCompacted := func(kind, summary string, tokensBefore, tokensAfter, savedTokens int) {
		if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
			_ = directorRolloutWriter.WriteCompacted(memory.CompactedPayload{
				Summary:      summary,
				TokensBefore: tokensBefore,
				TokensAfter:  tokensAfter,
				Kind:         kind,
				SavedTokens:  savedTokens,
			})
		}
	}

	initialMsgCount := len(messages)
	for i := 0; i < initialMsgCount; i++ {
		writeDirectorRollout(messages[i])
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

			if p.cfg.CompressEnable {
				var modelName string
				if p.cfg.LLM != nil {
					modelName = p.cfg.LLM.Model()
				}
				threshold := compression.ResolveThreshold(modelName, p.cfg.CompressThreshold)
				keepTokens := p.cfg.CompressKeepTokens
				if keepTokens <= 0 {
					keepTokens = compression.DefaultToolResultKeepTokens
				}
				var compStats *compression.ContextCompressionStats
				messages, compStats = compression.TruncateToolResultsToBudget(messages, threshold, keepTokens)
				if compStats != nil && compStats.TruncatedCount > 0 && p.cfg.Publisher != nil {
					truncatedTools := make([]map[string]interface{}, len(compStats.TruncatedTools))
					for ti, tool := range compStats.TruncatedTools {
						truncatedTools[ti] = map[string]interface{}{
							"tool_name":       tool.ToolName,
							"original_tokens": tool.OriginalTokens,
							"kept_tokens":     tool.KeptTokens,
							"omitted_tokens":  tool.OmittedTokens,
						}
					}
					_ = p.cfg.Publisher.Publish("context_compressed", map[string]interface{}{
						"original_tokens":   compStats.OriginalTokens,
						"compressed_tokens": compStats.CompressedTokens,
						"saved_tokens":      compStats.SavedTokens,
						"saved_percent":     compStats.SavedPercent,
						"truncated_count":   compStats.TruncatedCount,
						"truncated_tools":   truncatedTools,
					}, "director")
					slog.Debug("context compression applied",
						"original_tokens", compStats.OriginalTokens,
						"compressed_tokens", compStats.CompressedTokens,
						"saved_tokens", compStats.SavedTokens,
						"saved_percent", compStats.SavedPercent,
						"truncated_count", compStats.TruncatedCount)
					writeCompacted(memory.CompactionKindRule, fmt.Sprintf(
						"tool-result truncation: %d results compacted, saved %d tokens (%d → %d)",
						compStats.TruncatedCount, compStats.SavedTokens,
						compStats.OriginalTokens, compStats.CompressedTokens),
						compStats.OriginalTokens, compStats.CompressedTokens, compStats.SavedTokens)
				}

				if compression.EstimateMessagesTokens(messages) > threshold {
					newMessages, emergencyStats := p.cfg.Compressor.ApplyEmergency(ctx, messages, threshold, in.Mem)
					messages = newMessages
					if emergencyStats != nil && p.cfg.Publisher != nil {
						_ = p.cfg.Publisher.Publish("context_emergency_compressed", map[string]interface{}{
							"original_tokens":   emergencyStats.OriginalTokens,
							"compressed_tokens": emergencyStats.CompressedTokens,
							"saved_tokens":      emergencyStats.SavedTokens,
							"extracted_blocks":  emergencyStats.ExtractedBlocks,
							"summarized_blocks": emergencyStats.SummarizedBlocks,
							"kept_blocks":       emergencyStats.KeptBlocks,
							"summarized_by_llm": emergencyStats.SummarizedByLLM,
						}, "director")
					}
					slog.Warn("emergency context compression applied",
						"original_tokens", emergencyStats.OriginalTokens,
						"compressed_tokens", emergencyStats.CompressedTokens,
						"extracted_blocks", emergencyStats.ExtractedBlocks,
						"kept_blocks", emergencyStats.KeptBlocks,
						"summarized_by_llm", emergencyStats.SummarizedByLLM)
					writeCompacted(memory.CompactionKindEmergency, fmt.Sprintf(
						"emergency compression: %d blocks extracted, %d summarized, %d kept, saved %d tokens (%d → %d)",
						emergencyStats.ExtractedBlocks, emergencyStats.SummarizedBlocks,
						emergencyStats.KeptBlocks, emergencyStats.SavedTokens,
						emergencyStats.OriginalTokens, emergencyStats.CompressedTokens),
						emergencyStats.OriginalTokens, emergencyStats.CompressedTokens, emergencyStats.SavedTokens)
				}

				if p.cfg.UltimateCompressEnable && p.cfg.Journal != nil && compression.EstimateMessagesTokens(messages) > threshold {
					newMessages, ultStats := p.cfg.Compressor.ApplyUltimate(messages, threshold, in.Mem, p.cfg.UltimateCompressKeepPlans)
					messages = newMessages
					if p.cfg.Publisher != nil && ultStats != nil {
						_ = p.cfg.Publisher.Publish("context_ultimate_compressed", map[string]interface{}{
							"original_tokens":   ultStats.OriginalTokens,
							"compressed_tokens": ultStats.CompressedTokens,
							"saved_tokens":      ultStats.SavedTokens,
							"total_plans":       ultStats.TotalPlans,
							"kept_plans":        ultStats.KeptPlans,
							"user_inputs":       ultStats.UserInputs,
							"truncated":         ultStats.Truncated,
							"detail":            "终极压缩(ultimate compression)已触发,上下文已重置为用户原始输入 + Thought & Plan 块",
						}, "director")
						writeCompacted(memory.CompactionKindUltimate, fmt.Sprintf(
							"ultimate compression: context rebuilt from %d user inputs + %d/%d Thought & Plan blocks, saved %d tokens (%d → %d)",
							ultStats.UserInputs, ultStats.KeptPlans, ultStats.TotalPlans,
							ultStats.SavedTokens, ultStats.OriginalTokens, ultStats.CompressedTokens),
							ultStats.OriginalTokens, ultStats.CompressedTokens, ultStats.SavedTokens)
					}
				}
			}

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

			if llmErr == nil && p.cfg.Publisher != nil && len(resp.Choices) > 0 {
				reasoning := resp.Choices[0].Reasoning
				if reasoning != "" {
					_ = p.cfg.Publisher.Publish("thinking", map[string]interface{}{
						"content": reasoning,
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

		choice := resp.Choices[0]
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
			_ = p.cfg.Publisher.PublishWithMetadata("ai_response", choice.Content, "director", metadata)
		}

		if in.Mem != nil {
			var toolCallData []memory.ToolCallData
			if p.cfg.ConvertToolCallsFn != nil {
				toolCallData = p.cfg.ConvertToolCallsFn(choice.ToolCalls)
			}
			in.Mem.AddAssistantMessage(choice.Content, toolCallData)
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

		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		})

		writeDirectorRollout(llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		})
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
			return PlanResult{Text: choice.Content, Steps: p.state.Step, StopReason: "plain_text"}, nil
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
			toolResult, err := p.cfg.Tools.Call(callCtx, tc.Function.Name, tc.Function.Arguments)
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

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    out.result,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})

			writeDirectorRollout(llm.Message{
				Role:       llm.RoleTool,
				Content:    out.result,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})

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
