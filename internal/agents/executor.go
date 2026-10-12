package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"yagent/internal/agents/activity"
	"yagent/internal/agents/toolbatch"
	"yagent/internal/blackboard"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

type ExecutorConfig struct {
	SystemPrompt  string
	UserInput     string
	Adapters      []*tools.Adapter
	LLM           llm.Engine
	MaxSteps      int
	Publisher     EventBus
	AgentName     string
	StopOnFinish  bool
	LLMTimeout    time.Duration
	StepRetries   int
	SystemAsHuman bool
	RepoContext   string
	OnToolResult  func(toolName string, result string)

	OnAgentStart func(ctx context.Context) error

	OnAgentExit func(ctx context.Context, agentErr error) error

	OnStepEnd func(ctx context.Context, stepInfo StepInfo) error

	NoActionRetryLimit int

	EnableContextCompression    bool
	ContextCompressionThreshold int
	ToolResultKeepTokens        int
	UltimateThinkLink           compression.ThinkLinkJournal
	UltimateKeepPlans           int

	MaxParallelReadOnlyTools int
}

func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxParallelReadOnlyTools: DefaultMaxParallelReadOnlyTools,
	}
}

type ExecutorResult struct {
	Text    string
	History []llm.Message
}

const DefaultMaxParallelReadOnlyTools = 4

const MaxMaxParallelReadOnlyTools = 8

var defaultToolTimeout = 180 * time.Second

var nonParallelToolNames = map[string]bool{
	"agent_exit":    true,
	"delegate_meta": true,
}

func NormalizeMaxParallelReadOnlyTools(n int) int {
	switch {
	case n == 1:
		return 1
	case n <= 0:
		return DefaultMaxParallelReadOnlyTools
	case n > MaxMaxParallelReadOnlyTools:
		return MaxMaxParallelReadOnlyTools
	default:
		return n
	}
}

func runToolCall(ctx context.Context, cfg *ExecutorConfig, tc llm.ToolCall) (string, bool) {
	activity.Heartbeat(ctx, "tool:start:"+tc.Function.Name)

	if cfg.Publisher != nil {
		cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
			"tool_name":    tc.Function.Name,
			"arguments":    tc.Function.Arguments,
			"tool_call_id": tc.ID,
		}, cfg.AgentName)
	}

	for _, t := range cfg.Adapters {
		if t.Name() == tc.Function.Name {
			startTime := time.Now()

			if strings.HasPrefix(tc.Function.Name, "delegate_") {
				agentName := strings.TrimPrefix(tc.Function.Name, "delegate_")
				LogDelegateCall(tc.Function.Name, agentName, tc.Function.Arguments)
			}

			cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
			toolCtx := cancelCtx
			toolCancel := cancelCtxCancel
			if !isInteractiveUserTool(tc.Function.Name) {
				toolCtx, toolCancel = context.WithTimeout(cancelCtx, defaultToolTimeout)
			}
			toolResult, callErr := t.Call(toolCtx, tc.Function.Arguments)
			cancelCtxCancel()
			toolCancel()
			if callErr != nil {
				if errors.Is(callErr, context.DeadlineExceeded) {
					toolResult = fmt.Sprintf("Error: tool execution timed out after %d seconds", int(defaultToolTimeout/time.Second))
				} else {
					toolResult = fmt.Sprintf("Error: %v", callErr)
				}
			}
			logToolCall(tc.Function.Name, cfg.AgentName, tc.Function.Arguments, toolResult, callErr, startTime)
			activity.Heartbeat(ctx, "tool:done:"+tc.Function.Name)
			return toolResult, true
		}
	}
	return "", false
}

func RunAgentLoop(ctx context.Context, cfg ExecutorConfig) (result ExecutorResult, retErr error) {
	// 自动路由：把 RoutingEngine 换成本轮档位（ctx 覆盖或角色基线）对应的具体引擎。
	// 固定模式 / 未启用路由时为 no-op。之后 Model()、压缩阈值、model_info 事件
	// 都会反映实际路由到的模型。
	cfg.LLM = llm.ResolveTierEngine(ctx, cfg.LLM)

	if cfg.Publisher != nil {
		cfg.Publisher.Publish("model_info", map[string]interface{}{
			"model": cfg.LLM.Model(),
			"agent": cfg.AgentName,
		}, cfg.AgentName)
	}

	_ = InitToolLogger()

	systemRole := llm.RoleSystem
	if cfg.SystemAsHuman {
		systemRole = llm.RoleUser
	}

	systemPrompt := cfg.SystemPrompt
	if cfg.RepoContext != "" {
		systemPrompt += "\n\n" + cfg.RepoContext
	}
	systemMsg := llm.Message{
		Role:    systemRole,
		Content: systemPrompt,
	}
	userMsg := llm.Message{
		Role:    llm.RoleUser,
		Content: cfg.UserInput,
	}

	messages := []llm.Message{systemMsg, userMsg}
	history := make([]llm.Message, 0)
	history = append(history, systemMsg, userMsg)

	// LLM 调用超时取值：上游显式传入 > 全局运行时值（TUI /timeout 可动态修改）
	// > DefaultLLMCallTimeout（默认 10 分钟）。
	llmTimeout := cfg.LLMTimeout
	if llmTimeout <= 0 {
		if t := config.GlobalLLMTimeout(); t > 0 {
			llmTimeout = t
		} else {
			llmTimeout = config.DefaultLLMCallTimeout
		}
	}

	if bid := blackboard.BoardIDFrom(ctx); bid != "" && blackboardEnabled() {
		hasReadNote := false
		hasReadArtifact := false
		for _, ad := range cfg.Adapters {
			switch ad.Name() {
			case "read_note":
				hasReadNote = true
			case "read_artifact":
				hasReadArtifact = true
			}
		}
		if !hasReadNote {
			cfg.Adapters = append(cfg.Adapters, newReadNoteAdapter(func() string { return bid }))
		}
		if !hasReadArtifact {
			cfg.Adapters = append(cfg.Adapters, newReadArtifactAdapter())
		}
	}

	toolDefs := make([]llm.ToolDef, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		toolDefs[i] = ad.ToToolDef()
	}
	tools.SortToolDefs(toolDefs)

	opts := &llm.CallOptions{}

	if cfg.Publisher != nil {
		opts.StreamHandler = func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				cfg.Publisher.Publish("ai_chunk", map[string]interface{}{
					"content": string(chunk),
					"agent":   cfg.AgentName,
				}, cfg.AgentName)
			}
			return nil
		}
	}

	if cfg.OnAgentStart != nil {
		if err := cfg.OnAgentStart(ctx); err != nil {
			return ExecutorResult{}, fmt.Errorf("OnAgentStart hook failed: %w", err)
		}
	}

	if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
		if !rw.SessionMetaWritten() {
			cwd, _ := os.Getwd()
			rw.WriteSessionMeta(memory.SessionMeta{
				ID:          rw.SessionID(),
				SessionID:   rw.SessionID(),
				Cwd:         cwd,
				Originator:  "yagent",
				Source:      "cli",
				HistoryMode: "standard",
			})
		}

		turnID := rw.NextTurn()
		cwd, _ := os.Getwd()
		executorModel := ""
		if cfg.LLM != nil {
			executorModel = cfg.LLM.Model()
		}
		rw.WriteTurnContext(memory.TurnContext{
			TurnID:            turnID,
			Cwd:               cwd,
			Model:             executorModel,
			Effort:            "medium",
			CollaborationMode: "single",
		})

		rw.WriteEventMsg(memory.EventMsg{
			Type: "task_started",
		})
	}

	var agentErr error
	stepNumber := 0
	defer func() {
		r := recover()
		if r != nil {
			agentErr = fmt.Errorf("agent panic: %v", r)
		}
		if cfg.OnAgentExit != nil {
			if exitErr := cfg.OnAgentExit(ctx, agentErr); exitErr != nil {
				slog.Warn("OnAgentExit hook failed", "agent", cfg.AgentName, "error", exitErr)
			}
		}
		if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
			termErr := agentErr
			if termErr == nil {
				termErr = retErr
			}
			rs := memory.RunState{Steps: stepNumber, MaxSteps: cfg.MaxSteps}
			if termErr != nil {
				rw.WriteRunAborted(termErr.Error(), rs)
			} else {
				rw.WriteRunComplete(rs)
			}
		}
		if r != nil && cfg.OnAgentExit == nil {
			panic(r)
		}
	}()

	noActionCount := 0

	writeRollout := func(msg llm.Message) {
		writer := memory.GetRolloutWriter(ctx)
		if writer == nil || !writer.Enabled() {
			return
		}
		msgID := writer.NextMessageID()
		items := memory.LLMMessageToResponseItems(msg, msgID)
		for _, item := range items {
			if err := writer.WriteResponseItem(item); err != nil {
				log.Printf("rollout write error: %v", err)
			}
		}
	}

	writeRollout(systemMsg)
	writeRollout(userMsg)

	maxParallel := NormalizeMaxParallelReadOnlyTools(cfg.MaxParallelReadOnlyTools)

	for i := 0; i < cfg.MaxSteps; i++ {
		stepNumber++
		activity.Heartbeat(ctx, fmt.Sprintf("step:%d", stepNumber))
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)

		maxRetries := cfg.StepRetries
		var resp *llm.Response
		var err error

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				wait := time.Duration(1<<(attempt-1)) * time.Second
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				slog.Warn("AgentExecutor retrying LLM call", "agent", cfg.AgentName, "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					return ExecutorResult{}, ctx.Err()
				case <-time.After(wait):
				}
				activity.Heartbeat(ctx, fmt.Sprintf("llm:retry:%d", attempt))
			}

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": cfg.LLM.Model(),
					"agent": cfg.AgentName,
				}, cfg.AgentName)
			}

			llmStartTime := time.Now()

			messages = llm.NormalizeMessages(messages)

			if cfg.EnableContextCompression {
				var modelName string
				if cfg.LLM != nil {
					modelName = cfg.LLM.Model()
				}
				threshold := compression.ResolveThreshold(modelName, cfg.ContextCompressionThreshold)
				keepTokens := cfg.ToolResultKeepTokens
				if keepTokens <= 0 {
					keepTokens = compression.DefaultToolResultKeepTokens
				}
				var compStats *compression.ContextCompressionStats
				messages, compStats = compression.TruncateToolResultsToBudget(messages, threshold, keepTokens)
				if compStats != nil && compStats.TruncatedCount > 0 && cfg.Publisher != nil {
					truncatedTools := make([]map[string]interface{}, len(compStats.TruncatedTools))
					for ti, tool := range compStats.TruncatedTools {
						truncatedTools[ti] = map[string]interface{}{
							"tool_name":       tool.ToolName,
							"original_tokens": tool.OriginalTokens,
							"kept_tokens":     tool.KeptTokens,
							"omitted_tokens":  tool.OmittedTokens,
						}
					}
					cfg.Publisher.Publish("context_compressed", map[string]interface{}{
						"original_tokens":   compStats.OriginalTokens,
						"compressed_tokens": compStats.CompressedTokens,
						"saved_tokens":      compStats.SavedTokens,
						"saved_percent":     compStats.SavedPercent,
						"truncated_count":   compStats.TruncatedCount,
						"truncated_tools":   truncatedTools,
					}, cfg.AgentName)
					slog.Debug("context compression applied",
						"agent", cfg.AgentName,
						"original_tokens", compStats.OriginalTokens,
						"compressed_tokens", compStats.CompressedTokens,
						"saved_tokens", compStats.SavedTokens,
						"saved_percent", compStats.SavedPercent,
						"truncated_count", compStats.TruncatedCount)
					if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
						_ = rw.WriteCompacted(memory.CompactedPayload{
							Summary: fmt.Sprintf(
								"tool-result truncation: %d results compacted, saved %d tokens (%d → %d)",
								compStats.TruncatedCount, compStats.SavedTokens,
								compStats.OriginalTokens, compStats.CompressedTokens),
							TokensBefore: compStats.OriginalTokens,
							TokensAfter:  compStats.CompressedTokens,
						})
					}
				}

				if compression.EstimateMessagesTokens(messages) > threshold {
					newMessages, emergencyStats := compression.EmergencyCompressMessages(ctx, messages, cfg.UserInput, threshold, cfg.LLM, cfg.AgentName, compression.DefaultEmergencyCompressKeepLastN)
					messages = newMessages
					history = make([]llm.Message, len(messages))
					copy(history, messages)
					if emergencyStats != nil && cfg.Publisher != nil {
						cfg.Publisher.Publish("context_emergency_compressed", map[string]interface{}{
							"original_tokens":   emergencyStats.OriginalTokens,
							"compressed_tokens": emergencyStats.CompressedTokens,
							"saved_tokens":      emergencyStats.SavedTokens,
							"extracted_blocks":  emergencyStats.ExtractedBlocks,
							"summarized_blocks": emergencyStats.SummarizedBlocks,
							"kept_blocks":       emergencyStats.KeptBlocks,
							"summarized_by_llm": emergencyStats.SummarizedByLLM,
						}, cfg.AgentName)
					}
					slog.Warn("emergency context compression applied",
						"agent", cfg.AgentName,
						"original_tokens", emergencyStats.OriginalTokens,
						"compressed_tokens", emergencyStats.CompressedTokens,
						"extracted_blocks", emergencyStats.ExtractedBlocks,
						"kept_blocks", emergencyStats.KeptBlocks,
						"summarized_by_llm", emergencyStats.SummarizedByLLM)
					if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
						_ = rw.WriteCompacted(memory.CompactedPayload{
							Summary: fmt.Sprintf(
								"emergency compression: %d blocks extracted, %d summarized, %d kept, saved %d tokens (%d → %d)",
								emergencyStats.ExtractedBlocks, emergencyStats.SummarizedBlocks,
								emergencyStats.KeptBlocks, emergencyStats.SavedTokens,
								emergencyStats.OriginalTokens, emergencyStats.CompressedTokens),
							TokensBefore: emergencyStats.OriginalTokens,
							TokensAfter:  emergencyStats.CompressedTokens,
						})
					}
					if compression.EstimateMessagesTokens(messages) > threshold && cfg.UltimateThinkLink != nil {
						ultCompressor := compression.NewContextCompressor(cfg.LLM, cfg.AgentName, cfg.UltimateThinkLink, nil)
						newMessages, ultStats := ultCompressor.ApplyUltimate(messages, threshold, nil, cfg.UltimateKeepPlans)
						messages = newMessages
						history = make([]llm.Message, len(messages))
						copy(history, messages)
						if ultStats != nil && cfg.Publisher != nil {
							cfg.Publisher.Publish("context_ultimate_compressed", map[string]interface{}{
								"original_tokens":   ultStats.OriginalTokens,
								"compressed_tokens": ultStats.CompressedTokens,
								"saved_tokens":      ultStats.SavedTokens,
								"total_plans":       ultStats.TotalPlans,
								"kept_plans":        ultStats.KeptPlans,
								"user_inputs":       ultStats.UserInputs,
								"truncated":         ultStats.Truncated,
							}, cfg.AgentName)
						}
						slog.Warn("ultimate context compression applied",
							"agent", cfg.AgentName,
							"original_tokens", ultStats.OriginalTokens,
							"compressed_tokens", ultStats.CompressedTokens,
							"saved_tokens", ultStats.SavedTokens,
							"threshold", threshold,
							"total_plans", ultStats.TotalPlans,
							"kept_plans", ultStats.KeptPlans,
							"user_inputs", ultStats.UserInputs,
							"truncated", ultStats.Truncated)
						if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
							_ = rw.WriteCompacted(memory.CompactedPayload{
								Summary: fmt.Sprintf(
									"ultimate compression: context rebuilt from %d user inputs + %d/%d Thought & Plan blocks, saved %d tokens (%d → %d)",
									ultStats.UserInputs, ultStats.KeptPlans, ultStats.TotalPlans,
									ultStats.SavedTokens, ultStats.OriginalTokens, ultStats.CompressedTokens),
								TokensBefore: ultStats.OriginalTokens,
								TokensAfter:  ultStats.CompressedTokens,
							})
						}
					}
				}
			}

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("ai_stream_start", map[string]interface{}{
					"agent": cfg.AgentName,
				}, cfg.AgentName)
			}

			activity.Heartbeat(ctx, "llm:start")

			llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
			resp, err = cfg.LLM.GenerateContent(llmCtx, messages, toolDefs, opts)
			llmCancel()

			activity.Heartbeat(ctx, "llm:done")

			if cfg.Publisher != nil {
				metadata := map[string]interface{}{
					"agent": cfg.AgentName,
				}
				if err == nil && resp != nil && resp.Usage != nil {
					metadata["usage"] = map[string]interface{}{
						"prompt_tokens":               resp.Usage.PromptTokens,
						"completion_tokens":           resp.Usage.CompletionTokens,
						"total_tokens":                resp.Usage.TotalTokens,
						"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
						"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
						"total_input_tokens":          resp.Usage.TotalInputTokens,
					}
				}
				cfg.Publisher.PublishWithMetadata("ai_stream_end", "", cfg.AgentName, metadata)
			}

			llmDuration := time.Since(llmStartTime).Seconds()

			if err == nil && cfg.Publisher != nil && len(resp.Choices) > 0 {
				reasoning := resp.Choices[0].Reasoning
				if reasoning != "" {
					cfg.Publisher.Publish("thinking", map[string]interface{}{
						"content": reasoning,
						"model":   cfg.LLM.Model(),
						"agent":   cfg.AgentName,
					}, cfg.AgentName)
				}
			}

			if cfg.Publisher != nil {
				metadata := map[string]interface{}{
					"model":            cfg.LLM.Model(),
					"agent":            cfg.AgentName,
					"duration_seconds": llmDuration,
				}
				if err != nil {
					metadata["error"] = err.Error()
				}
				cfg.Publisher.PublishWithMetadata("llm_call_end", "", cfg.AgentName, metadata)
			}

			if err == nil {
				break
			}
			slog.Warn("AgentExecutor LLM error, will retry", "agent", cfg.AgentName, "error", err, "step", i, "attempt", attempt)
			llm.LogLLMError("AgentExecutor LLM error, will retry",
				"agent", cfg.AgentName, "error", err, "step", i, "attempt", attempt,
			)
		}

		if err != nil {
			slog.Error("AgentExecutor LLM error after all retries", "agent", cfg.AgentName, "error", err, "step", i)
			llm.LogLLMError("AgentExecutor LLM error after all retries",
				"agent", cfg.AgentName, "error", err, "step", i,
			)
			return ExecutorResult{}, err
		}

		choice := resp.Choices[0]

		var promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens int64
		if resp.Usage != nil {
			promptTokens = resp.Usage.PromptTokens
			completionTokens = resp.Usage.CompletionTokens
			totalTokens = resp.Usage.TotalTokens
			cacheCreationTokens = resp.Usage.CacheCreationInputTokens
			cacheReadTokens = resp.Usage.CacheReadInputTokens
		} else {
			estPrompt := 0
			for _, msg := range messages {
				estPrompt += EstimateTokens(msg.Content)
			}
			estCompletion := EstimateTokens(choice.Content)
			promptTokens = int64(estPrompt)
			completionTokens = int64(estCompletion)
			totalTokens = promptTokens + completionTokens
		}

		if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
			if writeErr := rw.WriteTokenCount(promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens); writeErr != nil {
				slog.Warn("Rollout: failed to write token_count", "error", writeErr)
			}
		}

		if cfg.Publisher != nil {
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
			cfg.Publisher.PublishWithMetadata("ai_response", choice.Content, cfg.AgentName, metadata)
		}

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		}
		messages = append(messages, assistantMsg)
		history = append(history, assistantMsg)

		if cfg.UltimateThinkLink != nil {
			for _, block := range compression.ExtractThoughtAndPlanBlocks(assistantMsg.Content) {
				if entry, added := cfg.UltimateThinkLink.AddThoughtPlan(block, i); added {
					if cfg.Publisher != nil {
						_ = cfg.Publisher.Publish("thinklink_entry", map[string]interface{}{
							"id":        entry.ID,
							"kind":      entry.Kind.String(),
							"content":   entry.Content,
							"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
							"step":      entry.Step,
						}, cfg.AgentName)
					}
				}
			}
		}

		writeRollout(assistantMsg)

		if len(choice.ToolCalls) == 0 {
			if cfg.NoActionRetryLimit > 0 && noActionCount < cfg.NoActionRetryLimit {
				noActionCount++
				slog.Warn("LLM returned text-only response, retrying", "agent", cfg.AgentName, "step", i, "no_action_count", noActionCount)

				reminderContent := "SYSTEM REMINDER: You just ended your response without calling any tools, which immediately terminates your run and leaves the task unexecuted. This is unacceptable.\n\n" +
					"You must immediately invoke a tool to execute the next step of your plan (explore/edit/run/verify); a `<thought>` block alone does not constitute action.\n\n" +
					"Only call `agent_exit` when the task is truly complete or you are genuinely unable to proceed.\n\n" +
					"Do not repeat your plan; take immediate action."

				reminderMsg := llm.Message{
					Role:    llm.RoleUser,
					Content: reminderContent,
				}
				messages = append(messages, reminderMsg)
				history = append(history, reminderMsg)
				writeRollout(reminderMsg)
				continue
			}
			return ExecutorResult{Text: choice.Content, History: history}, nil
		}

		toolCalls := choice.ToolCalls
		isParallelizable := func(i int) bool {
			name := toolCalls[i].Function.Name
			if isInteractiveUserTool(name) || nonParallelToolNames[name] {
				return false
			}
			for _, t := range cfg.Adapters {
				if t.Name() == name {
					return t.IsReadOnly()
				}
			}
			return false
		}
		plan := toolbatch.Plan(len(toolCalls), isParallelizable)

		commitToolCall := func(tc llm.ToolCall, toolResult string, publishResultEvent bool) {
			if cfg.OnToolResult != nil {
				cfg.OnToolResult(tc.Function.Name, toolResult)
			}

			if publishResultEvent && cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    toolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})
			toolMsg := llm.Message{
				Role:       llm.RoleTool,
				Content:    toolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			}
			history = append(history, toolMsg)

			writeRollout(toolMsg)
		}

		execOneToolCall := func(idx int) *ExecutorResult {
			tc := toolCalls[idx]
			toolResult, found := runToolCall(ctx, &cfg, tc)
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			commitToolCall(tc, toolResult, true)

			if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
				return &ExecutorResult{Text: toolResult, History: history}
			}
			return nil
		}

		for _, seg := range plan {
			if !seg.Parallel {
				if res := execOneToolCall(seg.Indices[0]); res != nil {
					return *res, nil
				}
				continue
			}

			toolbatch.Run(ctx, seg, maxParallel,
				func(callCtx context.Context, i int) (toolResult string) {
					tc := toolCalls[i]
					defer func() {
						if r := recover(); r != nil {
							slog.Error("tool call panicked in parallel read-only batch",
								"agent", cfg.AgentName, "tool", tc.Function.Name,
								"tool_call_id", tc.ID, "panic", r,
								"stack", string(debug.Stack()))
							toolResult = fmt.Sprintf("Error: panic: %v", r)
						}
					}()
					res, found := runToolCall(callCtx, &cfg, tc)
					if !found {
						res = fmt.Sprintf("Tool %s not found", tc.Function.Name)
					}
					if cfg.Publisher != nil {
						cfg.Publisher.Publish("tool_call_result", map[string]interface{}{
							"tool_name":    tc.Function.Name,
							"result":       res,
							"tool_call_id": tc.ID,
						}, cfg.AgentName)
					}
					return res
				},
				func(i int, res string) {
					commitToolCall(toolCalls[i], res, false)
				},
			)
		}

		if cfg.OnStepEnd != nil && len(choice.ToolCalls) > 0 {
			toolName := ""
			if len(choice.ToolCalls) > 0 {
				toolName = choice.ToolCalls[0].Function.Name
			}
			stepInfo := StepInfo{
				StepNumber: stepNumber,
				ToolName:   toolName,
				Success:    true,
			}
			if err := cfg.OnStepEnd(ctx, stepInfo); err != nil {
				slog.Warn("OnStepEnd hook error", "agent", cfg.AgentName, "step", stepNumber, "error", err)
			}
			if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
				event := memory.StepInfoToEventMsg(stepNumber, toolName, nil, true)
				rw.WriteEventMsg(event)
			}
		}
	}

	return ExecutorResult{}, fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
}

func ConvertLLMHistoryToMemory(history []llm.Message) []memory.ChatMessage {
	result := make([]memory.ChatMessage, 0, len(history))
	for _, msg := range history {
		cm := memory.ChatMessage{
			IsSubAgent: true,
		}
		switch msg.Role {
		case llm.RoleSystem:
			cm.Type = memory.MessageTypeSystem
		case llm.RoleUser:
			cm.Type = memory.MessageTypeHuman
		case llm.RoleAssistant:
			cm.Type = memory.MessageTypeAssistant
			for _, tc := range msg.ToolCalls {
				cm.ToolCalls = append(cm.ToolCalls, memory.ToolCallData{
					ID:   tc.ID,
					Type: tc.Type,
					Function: memory.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: json.RawMessage(tc.Function.Arguments),
					},
				})
			}
		case llm.RoleTool:
			cm.Type = memory.MessageTypeTool
			cm.ToolCallID = &msg.ToolCallID
		}
		cm.Content = msg.Content
		result = append(result, cm)
	}
	return result
}

func logToolCall(toolName, agentName, args string, result string, err error, startTime time.Time) {
	argsJSON := args
	if data, err := json.MarshalIndent(json.RawMessage(args), "", "  "); err == nil {
		argsJSON = string(data)
	}

	_ = InitToolLogger()

	duration := time.Since(startTime)

	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	LogToolCall(toolName, agentName, argsJSON, result, errMsg, duration)
}
