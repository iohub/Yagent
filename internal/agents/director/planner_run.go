// Package director — planner_run.go — P0-1 Phase 3-2a-1：Planner.Run 完整实现。
//
// lift-and-shift 自 DirectorAgent.run（internal/agents/director.go:811-1490），
// 控制流逐字搬迁，行为等价是唯一标准（不顺手优化）。与原 run() 的差异均为
// 依赖映射约定的职责切分，不改变可观测行为：
//   - llmClient 引擎刷新 / currentMemory 维护 / 子 Agent 引擎刷新属于门面 Run
//     职责，Planner 不感知（llmClient 刷新逻辑属于门面 Run，Planner 不刷新）；
//   - 系统提示构建段（GlobalCtx.FormatPrompt(directorPrompt) + 仅首次
//     loadProjectContext + metaHandler.List() 自定义 Agent 列举 +
//     KnowledgeInjector.Inject + projectContext 延迟追加 + context_loaded 事件）
//     整段由门面包装为 cfg.Prompts 闭包注入（3-2b 接线）；
//   - delegate_* 检测/委派统计/子 Agent 记忆注入由门面 Tools 包装闭包更新
//     p.state（3-2b 接线），Planner 只读 state 驱动强制委派提醒控制流；
//   - validateAndRepairToolCallPairs 为 agents 包包级函数，director 包禁止
//     import agents，故经 cfg.NormalizeMessages 闭包注入（门面包装）；
//   - 熔断检查与步骤级重试经 cfg.Recovery（同包 RecoveryHandler，nil 容忍：
//     原 a.adapter 构造时恒非 nil，nil 视为无熔断、0 次重试）；
//   - 上下文压缩开关与预算由门面从 config.EnhancedCommanderConfig 提取注入
//     （原 threshold/keepTokens 的 ≤0 回退默认逻辑保留在 Planner 内，行为一致）。
//
// Phase 3-2a-1 本轮仅实现 Run 本体，不切换任何门面调用点（DirectorAgent.run
// 原样保留），不写新测试文件。
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
	"yagent/internal/tools"
)

// maxNonDelegationPrompts "未委派强制提醒"注入上限（原 agents 包 director.go:30
// 同名常量，director 子包独立定义等价值；达到上限后放行纯文本结束，防死循环，
// 循环本身还有 maxSteps 兜底）。
const maxNonDelegationPrompts = 3

// Run 执行任务规划主循环（lift-and-shift 自 DirectorAgent.run，控制流逐字搬迁）。
// 退出路径与原 run() 一致：circuit_breaker_open / context cancel / agent_exit /
// plain_text / max_steps；StopReason 对应各退出路径，Steps 为实际执行步数。
func (p *Planner) Run(ctx context.Context, in PlanInput) (PlanResult, error) {
	// 执行检测机制：每次任务开始时重置委派状态，使检测基于"本次任务是否委派"
	// （原 a.hasDelegated = false / a.nonDelegationPrompts = 0，per-run 状态走 p.state）
	p.state.HasDelegated = false
	p.state.NonDelegationPrompts = 0

	// thinklink: 记录用户原始输入（供终极压缩重建上下文；任务开始时步数为 0，
	// 重复提交相同内容会被自动去重）
	if p.cfg.Journal != nil {
		if entry, added := p.cfg.Journal.AddUserInput(in.Input, 0); added {
			// 原 a.publishThinkLinkEntry(entry) 内联（DirectorAgent.publishThinkLinkEntry
			// 逐字搬迁；source 原为 a.Name()，本 Planner 即 "director"）
			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("thinklink_entry", map[string]interface{}{
					"id":        entry.ID,
					"kind":      entry.Kind.String(),
					"content":   entry.Content,
					"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
					"step":      entry.Step,
				}, "director")
			}
		}
	}

	if in.Mem != nil {
		// Check if the last message is the same as input to avoid duplication
		// because handleChatMessage might have already added it.
		lastMsg := in.Mem.GetLastMessage()
		if lastMsg == nil || lastMsg.Content != in.Input || lastMsg.Type != memory.MessageTypeHuman {
			in.Mem.AddHumanMessage(in.Input)
		}
	}

	var messages []llm.Message

	// Always start with System Prompt（原 run() 系统提示构建段整段由门面闭包
	// 包装注入：FormatPrompt(directorPrompt) + 仅首次对话 loadProjectContext +
	// metaHandler.List() 自定义 Agent 列举 + KnowledgeInjector.Inject，
	// context_loaded 事件发布与 projectContext 延迟追加均在门面闭包内）
	systemPrompt, err := p.cfg.Prompts(ctx, PromptInput{
		Input:             in.Input,
		FirstConversation: in.Mem == nil || len(in.Mem.GetMessages()) == 0,
	})
	if err != nil {
		// PromptBuilder 契约：门面闭包忽略注入错误继续，err 应恒为 nil；防御性返回
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
			// 过滤 sub-agent 内部消息，避免破坏 tool_calls → tool 消息配对规则
			// sub-agent 消息由 injectSubAgentMemory() 注入，仅用于内存记录，不应发送给 LLM
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

	// 工具定义（原 a.Adapters 逐个 ToToolDef 由门面 Specs() 包装；仍按原 run()
	// 行为排序，确保 LLM tools 参数确定性、利于 Prompt Cache 复用）
	var toolDefs []llm.ToolDef
	if p.cfg.Tools != nil {
		toolDefs = p.cfg.Tools.Specs()
		tools.SortToolDefs(toolDefs)
	}

	// Publish model info so the TUI can display it in the status bar.
	if p.cfg.Publisher != nil {
		_ = p.cfg.Publisher.Publish("model_info", map[string]interface{}{
			"model": p.cfg.LLM.Model(),
			"agent": "director",
		}, "director")
	}

	// ═══════ 初始化 Director Rollout Writer（原 run() 内经 createRolloutWriter
	// 创建，Planner 侧改用 cfg.Rollout——门面按 in.TaskID/ComputeProjectID 创建
	// 注入，nil 时跳过，创建失败的 slog.Warn 由门面负责） ═══════
	var directorRolloutWriter *memory.RolloutWriter
	if rw := p.cfg.Rollout; rw != nil {
		directorRolloutWriter = rw
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	writeDirectorRollout := func(msg llm.Message) {
		if directorRolloutWriter == nil || !directorRolloutWriter.Enabled() {
			return
		}
		// 首次写入时记录 session_meta 和 turn_context
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
			// 运行时真实模型名（llm.Engine.Model()），用于 rollout 恢复侧识别模型
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
				slog.Warn("Rollout: failed to write director message",
					"error", err,
				)
			}
		}
	}
	// ═══════ END Director Rollout Writer ═══════

	// ═══════ 写入初始消息（system prompt + user input） ═══════
	initialMsgCount := len(messages)
	for i := 0; i < initialMsgCount; i++ {
		writeDirectorRollout(messages[i])
	}

	for i := 0; i < p.cfg.MaxSteps; i++ {
		// per-run 步数状态（原 run() 使用循环变量 i，Planner 以 p.state.Step
		// 对外暴露"当前正在执行第 i+1 步"）
		p.state.Step = i + 1

		// --- 熔断检查（原 a.adapter.IsCircuitBreakerOpen，经 cfg.Recovery 同包组件）---
		if p.cfg.Recovery != nil && p.cfg.Recovery.IsCircuitBreakerOpen() {
			slog.Error("Circuit breaker open, too many consecutive LLM failures",
				"step", i)
			// Rollout: 写入任务中止事件
			if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
				directorRolloutWriter.WriteEventMsg(memory.EventMsg{
					Type:   "turn_aborted",
					Reason: "circuit breaker open",
				})
			}
			return PlanResult{Steps: p.state.Step, StopReason: "circuit_breaker_open"},
				fmt.Errorf("circuit breaker open: LLM calls blocked")
		}

		// --- 步骤级重试（原 a.adapter.LLMRetries，cfg.Recovery nil 视为 0 次）---
		maxRetries := 0
		if p.cfg.Recovery != nil {
			maxRetries = p.cfg.Recovery.LLMRetries()
		}
		var resp *llm.Response
		var llmErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				// 指数退避：1s, 2s, 4s, 8s, ... 最大30s
				wait := time.Duration(1<<(attempt-1)) * time.Second
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				slog.Warn("DirectorAgent retrying LLM call", "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					// Rollout: 写入任务中止事件
					if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
						directorRolloutWriter.WriteEventMsg(memory.EventMsg{
							Type:   "turn_aborted",
							Reason: ctx.Err().Error(),
						})
					}
					return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, ctx.Err()
				case <-time.After(wait):
				}
			}

			// 验证并修复 tool_call/tool_response 配对完整性
			// （原 agents 包包级函数 validateAndRepairToolCallPairs，director 禁止
			// import agents，经 cfg.NormalizeMessages 闭包注入，nil 时跳过）
			if p.cfg.NormalizeMessages != nil {
				messages = p.cfg.NormalizeMessages(messages)
			}

			// 上下文压缩:token 超阈值时按优先级截断 tool 执行结果
			// 阈值按当前模型上下文窗口的 1/3 推导（见 compression.ResolveThreshold）
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
				}

				// 紧急压缩:tool 结果已全部截断后仍超限 → 启动紧急模式, 极致压缩 memory 继续任务
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
				}

				// 终极压缩（第三级）：两级常规压缩处理后仍超限（含紧急压缩出错/未生效、
				// 或 system 消息本身过大导致强制截断失效的情况）时，
				// 用 thinklink 中保存的用户原始输入 + Thought & Plan 块重建上下文，
				// 将 messages 重置为 [system..., 单条 user 重建消息]。
				// 压缩发生在当前步骤内部，不额外消耗 maxSteps。
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
					}
				}
			}

			slog.Debug("DirectorAgent calling LLM", "step", i, "messages", messages)

			// Create opts with streaming handler for real-time output
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

			// Publish llm_call_start event
			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": p.cfg.LLM.Model(),
					"agent": "director",
				}, "director")
			}

			// Publish ai_stream_start before LLM call
			if p.cfg.Publisher != nil {
				_ = p.cfg.Publisher.Publish("ai_stream_start", map[string]interface{}{
					"agent": "director",
				}, "director")
			}

			llmStartTime := time.Now()
			// 使用可配置的 LLM 超时保护，防止远程服务无响应时永久阻塞
			llmCtx, llmCancel := context.WithTimeout(ctx, p.cfg.LLMTimeout)
			resp, llmErr = p.cfg.LLM.GenerateContent(llmCtx, messages, toolDefs, opts)
			llmCancel()
			llmDuration := time.Since(llmStartTime).Seconds()

			// Publish ai_stream_end after LLM call
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

			// 记录 LLM 耗时指标（原 a.adapter.RecordLLMDuration，经 cfg.Metrics 注入）
			if p.cfg.Metrics != nil {
				p.cfg.Metrics.RecordLLMDuration(time.Since(llmStartTime))
			}

			// Publish thinking event (reasoning content) before llm_call_end
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

			// Publish llm_call_end event
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
				// 通过适配器记录成功（重置熔断器状态）
				if p.cfg.Recovery != nil {
					p.cfg.Recovery.RecordLLMSuccess()
				}
				break
			}
			// 通过适配器记录失败（可能触发熔断）
			if p.cfg.Recovery != nil {
				p.cfg.Recovery.RecordLLMFailure()
			}
			// 原 a.adapter 恒非 nil，Planner 侧 Recovery nil 容忍：统计跳过、计数取 0
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
			// Rollout: 写入任务中止事件
			if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
				directorRolloutWriter.WriteEventMsg(memory.EventMsg{
					Type:   "turn_aborted",
					Reason: llmErr.Error(),
				})
			}
			return PlanResult{Steps: p.state.Step, StopReason: "llm_error"}, llmErr
		}

		choice := resp.Choices[0]
		slog.Debug("DirectorAgent LLM response", "step", i, "content", choice.Content, "tool_calls", len(choice.ToolCalls))

		// 计算 token usage（实际值或估算值）
		var promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens int64
		if resp.Usage != nil {
			promptTokens = resp.Usage.PromptTokens
			completionTokens = resp.Usage.CompletionTokens
			totalTokens = resp.Usage.TotalTokens
			cacheCreationTokens = resp.Usage.CacheCreationInputTokens
			cacheReadTokens = resp.Usage.CacheReadInputTokens
		} else {
			// 本地模型可能不返回 usage，按内容估算
			// （原 agents 包 EstimateTokens 经 cfg.EstimateTokensFn 注入，nil 时计 0）
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

		// Rollout: 写入 token_count 事件
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
			// 原 agents 包包级 convertToolCalls 经 cfg.ConvertToolCallsFn 注入
			// （director 禁止 import agents；fn 为 nil 时传 nil ToolCallData）
			var toolCallData []memory.ToolCallData
			if p.cfg.ConvertToolCallsFn != nil {
				toolCallData = p.cfg.ConvertToolCallsFn(choice.ToolCalls)
			}
			in.Mem.AddAssistantMessage(choice.Content, toolCallData)
		}

		// thinklink: 提取本轮回复中的 Thought & Plan 块并实时保存（供终极压缩重建上下文）。
		// 复用 compression 包的包级提取函数（thoughtAndPlanPattern 正则），不重复造轮子。
		if p.cfg.Journal != nil {
			for _, block := range compression.ExtractThoughtAndPlanBlocks(choice.Content) {
				if entry, added := p.cfg.Journal.AddThoughtPlan(block, i); added {
					// 原 a.publishThinkLinkEntry(entry) 内联
					if p.cfg.Publisher != nil {
						_ = p.cfg.Publisher.Publish("thinklink_entry", map[string]interface{}{
							"id":        entry.ID,
							"kind":      entry.Kind.String(),
							"content":   entry.Content,
							"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
							"step":      entry.Step,
						}, "director")
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
		// 执行检测机制：若 director 未委派任何 agent 就打算以纯文本结束任务，
		// 以用户角色注入简练英文消息，强制要求其必须 delegate 一个 agent 完成任务。
		// 达到 maxNonDelegationPrompts 上限后放行（防死循环，循环本身还有 maxSteps 兜底）。
		if len(choice.ToolCalls) == 0 {
			if !p.state.HasDelegated && p.state.NonDelegationPrompts < maxNonDelegationPrompts {
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
				// 注意：不写入 ConversationMemory（mem），该消息是系统模拟的用户指令，
				// 不应污染会话历史；下一轮 LLM 调用会看到该 user 消息并应调用 delegate 工具。
				continue
			}
			// Rollout: 写入任务完成事件
			if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
				directorRolloutWriter.WriteEventMsg(memory.EventMsg{
					Type: "task_complete",
				})
			}
			return PlanResult{Text: choice.Content, Steps: p.state.Step, StopReason: "plain_text"}, nil
		}

		// ═══ 工具调用调度：顺序保持的分段并发（第二阶段，与 agents.RunAgentLoop
		// ═══ 同一 toolbatch 调度，消除两处副本的工具循环行为分叉）═══
		// 把单步 tool_calls 按"连续只读游程"切分为调度段（toolbatch.Plan）：
		//   并行段 = 连续的可并行工具（门面注入的 IsParallelizableTool 谓词判定：
		//            适配器存在 && IsReadOnly && 非交互 && 不在 deny-list），
		//            组内并发执行，join 后按原索引序 commit；
		//   串行段 = 非并行元素，原位串行执行（与 lift-and-shift 原实现的
		//            逐元素串行循环零差异）。
		// 站点差异逐项保留（对照 agents/executor.go 的同一调度）：
		//   - tool_call_start 事件在本站点由 Planner 于执行前发出（原行为），
		//     tool_call_result 事件在 commit 阶段发出（原行为——本站点无
		//     "并发组内提前发 result 事件"的变体，串行与并发路径事件时机一致）；
		//   - per-tool ctx 预检（原循环体内的 ctx.Err() 检查 + turn_aborted）保留：
		//     元素级 exec 中预检失败时标记 cancelled 该元素不落账，站点循环
		//     统一写 turn_aborted（Reason=ctx.Err().Error()）并以 "cancelled" 终止；
		//   - 超时分派（delegate_* 活动感知空闲超时 / 交互式工具无限等待 /
		//     普通工具 120s）在门面 directorToolRunner.Call 内（ToolRunner 实现），
		//     本次不改动该单工具调用层；
		//   - 错误→结果格式化（1000 字符截断 / DeadlineExceeded 超时提示）逐字保留；
		//   - not-found 兜底逐字保留（exec 内）；
		//   - commit 落账（result 事件 → Mem.AddToolMessage → messages append →
		//     writeDirectorRollout → agent_exit 退出判定 task_complete）按原序进行。
		maxParallel := p.cfg.MaxParallelReadOnlyTools
		toolCalls := choice.ToolCalls
		// 谓词适配：toolbatch.Plan 用索引谓词，门面 IsParallelizableTool 用工具名。
		// 谓词 nil → 全不可并行（fail-safe，与逐元素串行一致）。
		plan := toolbatch.Plan(len(toolCalls), func(i int) bool {
			if p.cfg.IsParallelizableTool == nil {
				return false
			}
			return p.cfg.IsParallelizableTool(toolCalls[i].Function.Name)
		})

		// 单元素调度结果（exec 产出、commit 消费）。
		type plannerToolOutcome struct {
			result    string
			cancelled bool // ctx 预检失败：元素不落账，整体以 "cancelled" 终止
		}

		// execPlannerToolCall 单元素执行（串行快路径原线程；并发组 goroutine 内）：
		// start 事件 → toolDefs 按名查找 → ctx 预检 → Tools.Call（门面包装：超时
		// 分派/委派统计/子 Agent 记忆注入，3-2b 接线不变）→ 错误→结果格式化 →
		// not-found 兜底。只产出 outcome，不做任何落账。
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
			// 原按名查找 a.Adapters（t.Name() == tc.Function.Name）→ 门面 Specs() 结果
			// 按名查找 + ToolRunner.Call；delegate_* 专用超时（10min）/交互式工具无限
			// 等待/LogDelegateCall 委派日志、delegate_repo 结果 JSON 解包（RepoSummary
			// 更新）、delegate 检测统计（delegationAttempts/hasDelegated）与子 Agent
			// 记忆注入（pendingSubAgentMemory），均由门面 Tools.Call 包装闭包负责
			// （3-2b 接线，Planner 不感知，只读 state 驱动强制委派提醒控制流）。
			found := false
			for _, def := range toolDefs {
				if def.Function.Name == tc.Function.Name {
					found = true

					// 工具调用前检查 context（原站点语义：不调用、不落账、整体 cancelled）
					if callCtx.Err() != nil {
						out.cancelled = true
						return out
					}

					// 原为工具调用添加超时保护（120s 普通工具 / 10min delegate_* /
					// 交互式工具无限等待），由门面 Tools.Call 包装闭包负责（3-2b 接线）。
					toolResult, err := p.cfg.Tools.Call(callCtx, tc.Function.Name, tc.Function.Arguments)

					if err != nil {
						// 截断过长的错误消息，避免污染上下文
						errMsg := err.Error()
						if len(errMsg) > 1000 {
							errMsg = errMsg[:1000] + "... [truncated]"
						}
						// 对超时错误给出明确的超时时间提示（原 per-tool 超时秒数由门面
						// 感知；cfg.ToolTimeout 为普通工具超时值，delegate_* 的 10min
						// 超时提示由 3-2b 门面包装精化）
						if errors.Is(err, context.DeadlineExceeded) {
							out.result = fmt.Sprintf("Error: tool execution timed out after %d seconds", int(p.cfg.ToolTimeout.Seconds()))
						} else {
							out.result = fmt.Sprintf("Error: %s", errMsg)
						}
					} else {
						out.result = toolResult
					}
					// 原 else if t.Name() == "delegate_repo"（结果 JSON 解包更新
					// a.GlobalCtx.RepoSummary）由门面 Tools.Call 包装闭包负责（3-2b 接线）
					break
				}
			}
			if !found {
				out.result = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}
			return out
		}

		// commitPlannerToolCall 单线程落账（join 后按 original index 序回调）。
		// 返回非空 stop 信号表示站点循环需终止（“cancelled”/“agent_exit”）。
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
				// Rollout: 写入任务完成事件
				if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
					directorRolloutWriter.WriteEventMsg(memory.EventMsg{
						Type: "task_complete",
					})
				}
				return "agent_exit"
			}
			return ""
		}

		for _, seg := range plan {
			stop := ""
			toolbatch.Run(ctx, seg, maxParallel, execPlannerToolCall,
				func(i int, out plannerToolOutcome) {
					if stop != "" {
						// 防御：join 后本回调本就按原序单线程执行；此分支仅在
						// "cancelled/agent_exit 之后的兄弟元素"路径出现（cancelled
						// 元素 commit 即终止），跳过重复落账。
						return
					}
					stop = commitPlannerToolCall(i, out)
				})
			if stop == "cancelled" {
				// Rollout: 写入任务中止事件（原站点语义：Reason=ctx.Err().Error()）
				if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
					directorRolloutWriter.WriteEventMsg(memory.EventMsg{
						Type:   "turn_aborted",
						Reason: ctx.Err().Error(),
					})
				}
				return PlanResult{Steps: p.state.Step, StopReason: "cancelled"}, ctx.Err()
			}
			if stop == "agent_exit" {
				// 原站点语义：退出判定在 commit（落账）之后，其后的工具段不执行不落账
				// （退出判定/任务完成事件均在 commitPlannerToolCall 内完成，无重发）
				return PlanResult{Text: "Task completed successfully", Steps: p.state.Step, StopReason: "agent_exit"}, nil
			}
		}
	}

	// Rollout: 写入任务中止事件
	if directorRolloutWriter != nil && directorRolloutWriter.Enabled() {
		directorRolloutWriter.WriteEventMsg(memory.EventMsg{
			Type:   "turn_aborted",
			Reason: "DirectorAgent exceeded max steps",
		})
	}
	return PlanResult{Steps: p.state.Step, StopReason: "max_steps"}, fmt.Errorf("DirectorAgent exceeded max steps")
}
