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
	"yagent/internal/agents/exitpolicy"
	"yagent/internal/agents/toolbatch"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/pause"
	"yagent/internal/tokenutil"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

type ExecutorConfig struct {
	SystemPrompt  string
	UserInput     string
	Surface       *tools.Surface
	LLM           llm.Engine
	MaxSteps      int
	Publisher     EventBus
	AgentName     string
	StopOnFinish  bool
	LLMTimeout    time.Duration
	StepRetries   int
	SystemAsHuman bool
	RepoContext   string
	SkillCatalog  string
	OnToolResult  func(toolName string, result string)

	OnAgentStart func(ctx context.Context) error

	OnAgentExit func(ctx context.Context, agentErr error) error

	NoActionRetryLimit int

	NoActionReminder string

	UltimateThinkLink compression.ThinkLinkJournal
	Compression       compression.Options

	MaxParallelReadOnlyTools int
}

func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxParallelReadOnlyTools: DefaultMaxParallelReadOnlyTools,
		Compression:              compression.DefaultOptions(),
	}
}

type ExecutorResult struct {
	Text    string
	History []llm.Message

	// 以下三项是 ADR-13 的执行指标：只给遥测/评分与可观测性用，不进交付物。
	// Steps 是已完成步数；Usage 是本轮所有 LLM 调用的累加；StopKind 复用
	// memory.StopKind* 词表（rollout_types.go），不另造一套。
	Steps    int
	Usage    llm.TokenUsage
	StopKind string
}

const DefaultMaxParallelReadOnlyTools = 4

const MaxMaxParallelReadOnlyTools = 8

var defaultToolTimeout = 180 * time.Second

var stepRetryBackoffBase = time.Second

func isSerialTool(name string) bool { return toolspec.IsSerial(name) }

func NormalizeMaxParallelReadOnlyTools(n int) int {
	if !config.ConcurrencyEnabled() {
		return 1
	}
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

func runToolCall(ctx context.Context, cfg *ExecutorConfig, tc llm.ToolCall) (string, bool, error) {
	activity.Heartbeat(ctx, "tool:start:"+tc.Function.Name)

	if cfg.Publisher != nil {
		cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
			"tool_name":    tc.Function.Name,
			"arguments":    tc.Function.Arguments,
			"tool_call_id": tc.ID,
		}, cfg.AgentName)
	}

	ad, found := resolveAdapter(cfg, tc.Function.Name)
	if !found {
		return "", false, nil
	}
	if isColdUnloaded(cfg, tc.Function.Name) {
		return teachColdTool(cfg, tc.Function.Name), true, nil
	}

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
	toolResult, execErr := ad.Call(toolCtx, tc.Function.Arguments)
	cancelCtxCancel()
	toolCancel()
	if execErr != nil {
		if errors.Is(execErr, context.DeadlineExceeded) {
			toolResult = fmt.Sprintf("Error: tool execution timed out after %d seconds", int(defaultToolTimeout/time.Second))
		} else {
			toolResult = fmt.Sprintf("Error: %v", execErr)
		}
	}
	logToolCall(tc.Function.Name, cfg.AgentName, tc.Function.Arguments, toolResult, execErr, startTime)
	activity.Heartbeat(ctx, "tool:done:"+tc.Function.Name)
	return toolResult, true, execErr
}

func toolCallIsParallelizable(surface *tools.Surface, name string) bool {
	if isInteractiveUserTool(name) || isSerialTool(name) {
		return false
	}
	if surface == nil {
		return false
	}
	ad, state := surface.Lookup(name)
	if state == tools.StateUnknown || ad == nil {
		return false
	}
	return ad.IsReadOnly()
}

func resolveAdapter(cfg *ExecutorConfig, name string) (*tools.Adapter, bool) {
	if cfg.Surface == nil {
		return nil, false
	}
	ad, state := cfg.Surface.Lookup(name)
	if state == tools.StateUnknown || ad == nil {
		return nil, false
	}
	return ad, true
}

func isColdUnloaded(cfg *ExecutorConfig, name string) bool {
	if cfg.Surface == nil {
		return false
	}
	_, state := cfg.Surface.Lookup(name)
	return state == tools.StateColdKnown
}

func teachColdTool(cfg *ExecutorConfig, name string) string {
	schema, err := cfg.Surface.Materialize([]string{name})
	if err != nil {
		return fmt.Sprintf("Error: cannot load schema for %s: %v", name, err)
	}
	return fmt.Sprintf("Error: schema for %s was not loaded. Full schema:\n%s\nRetry the call now with these parameters.", name, schema)
}

func RunAgentLoop(ctx context.Context, cfg ExecutorConfig) (result ExecutorResult, retErr error) {
	if id := memory.DelegationIDFrom(ctx); id != "" && cfg.Publisher != nil {
		if scoped, ok := cfg.Publisher.(interface {
			ForDelegation(string) *messaging.MessagePublisher
		}); ok {
			cfg.Publisher = scoped.ForDelegation(id)
		}
	}

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
	if cfg.Surface != nil {
		if catalog := cfg.Surface.Catalog(); catalog != "" {
			systemPrompt += "\n\n" + catalog
		}
	}
	if cfg.SkillCatalog != "" {
		systemPrompt += "\n\n" + cfg.SkillCatalog
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

	startStep := 0
	if rs := resumeStateFrom(ctx); rs != nil && len(rs.Messages) > 0 {
		messages = append([]llm.Message(nil), rs.Messages...)
		history = append([]llm.Message(nil), rs.Messages...)
		startStep = rs.StartStep
		if cfg.Surface != nil && len(rs.Materialized) > 0 {
			cfg.Surface.RestoreMaterialized(rs.Materialized)
		}
		if seeder, ok := cfg.UltimateThinkLink.(ThinklinkSeeder); ok {
			SeedThinkLink(seeder, rs.ThinkLink)
		}
	}

	llmTimeout := cfg.LLMTimeout
	if llmTimeout <= 0 {
		if t := config.GlobalLLMTimeout(); t > 0 {
			llmTimeout = t
		} else {
			llmTimeout = config.DefaultLLMCallTimeout
		}
	}

	defsForStep := func() []llm.ToolDef {
		if cfg.Surface == nil {
			return nil
		}
		return cfg.Surface.Defs()
	}

	opts := &llm.CallOptions{AgentName: cfg.AgentName}

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
	stepNumber := startStep
	// totalUsage 跨步累加；ADR-13 的遥测要"这次委派一共花了多少"。
	var totalUsage llm.TokenUsage
	stopKind := newStopKindTracker(ctx)
	defer func() {
		r := recover()
		if r != nil {
			agentErr = fmt.Errorf("agent panic: %v", r)
			stopKind.record(StopKindPanic, agentErr)
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
			rs := memory.RunState{
				Steps:      stepNumber,
				MaxSteps:   cfg.MaxSteps,
				StopReason: stopKind.kindOf(),
			}
			if termErr != nil {
				rw.WriteRunAborted(stopKind.reasonText(termErr.Error()), rs)
				writeExecutorCheckpoint(ctx, rw, cfg, stopKind.kindOf(), stepNumber)
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

	if resumeStateFrom(ctx) == nil {
		writeRollout(systemMsg)
		writeRollout(userMsg)
	}

	maxParallel := NormalizeMaxParallelReadOnlyTools(cfg.MaxParallelReadOnlyTools)

	var measured compression.Prefix

	for i := startStep; i < cfg.MaxSteps; i++ {
		if perr := pause.From(ctx).Wait(ctx); perr != nil {
			stopKind.record("", perr)
			return ExecutorResult{}, perr
		}

		stepNumber++
		activity.Heartbeat(ctx, fmt.Sprintf("step:%d", stepNumber))
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)

		maxRetries := cfg.StepRetries
		var resp *llm.Response
		var err error

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				wait := stepRetryBackoffBase * time.Duration(1<<(attempt-1))
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				slog.Warn("AgentExecutor retrying LLM call", "agent", cfg.AgentName, "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					stopKind.record("", ctx.Err())
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

			if cfg.Compression.Enabled {
				var modelName string
				if cfg.LLM != nil {
					modelName = cfg.LLM.Model()
				}
				threshold := compression.ResolveThreshold(modelName, cfg.Compression.Threshold)
				keepTokens := cfg.Compression.KeepTokens
				if keepTokens <= 0 {
					keepTokens = compression.DefaultToolResultKeepTokens
				}
				var compStats *compression.ContextCompressionStats
				messages, compStats = compression.TruncateToolResultsToBudget(messages, measured, threshold, keepTokens)
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
							Kind:         memory.CompactionKindRule,
							SavedTokens:  compStats.SavedTokens,
						})
					}
				}

				if compression.OverThreshold(messages, measured, threshold) {
					activity.Heartbeat(ctx, "compress:emergency")
					newMessages, emergencyStats := compression.EmergencyCompressMessages(ctx, messages, cfg.UserInput, threshold, cfg.LLM, cfg.AgentName, compression.DefaultEmergencyCompressKeepLastN)
					messages = newMessages
					measured = compression.Prefix{}
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
							Kind:         memory.CompactionKindEmergency,
							SavedTokens:  emergencyStats.SavedTokens,
						})
					}
					if compression.OverThreshold(messages, measured, threshold) && cfg.Compression.UltimateEnabled && cfg.UltimateThinkLink != nil {
						ultCompressor := compression.NewContextCompressor(cfg.LLM, cfg.AgentName, cfg.UltimateThinkLink)
						newMessages, ultStats := ultCompressor.ApplyUltimate(messages, threshold, nil, cfg.Compression.UltimateKeepPlans)
						messages = newMessages
						measured = compression.Prefix{}
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
								Kind:         memory.CompactionKindUltimate,
								SavedTokens:  ultStats.SavedTokens,
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
			resp, err = cfg.LLM.GenerateContent(llmCtx, messages, defsForStep(), opts)
			llmCancel()

			measured = measured.Refresh(resp, err, len(messages))

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

			if err == nil && cfg.Publisher != nil {
				if c, cErr := resp.FirstChoice(); cErr == nil && c.Reasoning != "" {
					cfg.Publisher.Publish("thinking", map[string]interface{}{
						"content": c.Reasoning,
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
			stopKind.record("", err)
			return ExecutorResult{}, err
		}

		choice, choiceErr := resp.FirstChoice()
		if choiceErr != nil {
			slog.Error("AgentExecutor LLM response unusable", "agent", cfg.AgentName, "error", choiceErr, "step", i)
			llm.LogLLMError("AgentExecutor LLM response unusable",
				"agent", cfg.AgentName, "error", choiceErr, "step", i,
			)
			stopKind.record("", choiceErr)
			return ExecutorResult{}, choiceErr
		}

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
				estPrompt += tokenutil.EstimateTokens(msg.Content)
			}
			estCompletion := tokenutil.EstimateTokens(choice.Content)
			promptTokens = int64(estPrompt)
			completionTokens = int64(estCompletion)
			totalTokens = promptTokens + completionTokens
		}

		// 跨步累加：遥测要的是"这次委派一共花了多少"，不是最后一步花了多少。
		totalUsage.PromptTokens += promptTokens
		totalUsage.CompletionTokens += completionTokens
		totalUsage.TotalTokens += totalTokens
		totalUsage.CacheCreationInputTokens += cacheCreationTokens
		totalUsage.CacheReadInputTokens += cacheReadTokens
		if resp.Usage != nil {
			totalUsage.TotalInputTokens += resp.Usage.TotalInputTokens
		}

		if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
			if writeErr := rw.WriteTokenCount(promptTokens, completionTokens, totalTokens, cacheCreationTokens, cacheReadTokens); writeErr != nil {
				slog.Warn("Rollout: failed to write token_count", "error", writeErr)
			}
		}

		assistantMsg := choice.AssistantMessage()

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
			cfg.Publisher.PublishWithMetadata("ai_response", assistantMsg.Content, cfg.AgentName, metadata)
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
							"agent":     cfg.AgentName,
							"content":   entry.Content,
							"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
							"step":      entry.Step,
						}, cfg.AgentName)
					}
					if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
						_ = rw.WriteEventMsg(memory.EventMsg{
							Type:       "thinklink_entry",
							Kind:       entry.Kind.String(),
							Text:       entry.Content,
							StepNumber: entry.Step,
						})
					}
				}
			}
		}

		writeRollout(assistantMsg)

		if len(choice.ToolCalls) == 0 {
			if cfg.NoActionRetryLimit > 0 && noActionCount < cfg.NoActionRetryLimit {
				noActionCount++
				slog.Warn("LLM returned text-only response, retrying", "agent", cfg.AgentName, "step", i, "no_action_count", noActionCount)

				reminderContent := cfg.NoActionReminder
				if reminderContent == "" {
					reminderContent = "SYSTEM REMINDER: You just ended your response without calling any tools, which immediately terminates your run and leaves the task unexecuted. This is unacceptable.\n\n" +
						"You must immediately invoke a tool to execute the next step of your plan (explore/edit/run/verify); a `<thought>` block alone does not constitute action.\n\n" +
						"Only call `agent_exit` when the task is truly complete or you are genuinely unable to proceed.\n\n" +
						"Do not repeat your plan; take immediate action."
				}

				reminderMsg := llm.Message{
					Role:    llm.RoleUser,
					Content: reminderContent,
				}
				messages = append(messages, reminderMsg)
				history = append(history, reminderMsg)
				writeRollout(reminderMsg)
				continue
			}
			stopKind.record(StopKindPlainTextReply, nil)
			return ExecutorResult{Text: assistantMsg.Content, History: history,
				Steps: stepNumber, Usage: totalUsage, StopKind: StopKindPlainTextReply}, nil
		}

		toolCalls := choice.ToolCalls
		isParallelizable := func(i int) bool {
			return toolCallIsParallelizable(cfg.Surface, toolCalls[i].Function.Name)
		}
		plan := toolbatch.Plan(len(toolCalls), isParallelizable)

		commitToolCall := func(tc llm.ToolCall, toolResult string, publishResultEvent bool) {
			toolResult += choice.ToolCallNote(tc)

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
			toolResult, found, execErr := runToolCall(ctx, &cfg, tc)
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			commitToolCall(tc, toolResult, true)

			if exitpolicy.TerminateOnAgentExit(cfg.StopOnFinish, tc.Function.Name, execErr != nil) {
				return &ExecutorResult{Text: toolResult, History: history,
					Steps: stepNumber, Usage: totalUsage, StopKind: StopKindAgentExit}
			}
			return nil
		}

		for _, seg := range plan {
			if !seg.Parallel {
				if res := execOneToolCall(seg.Indices[0]); res != nil {
					stopKind.record(StopKindAgentExit, nil)
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
					res, found, _ := runToolCall(callCtx, &cfg, tc)
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

		if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() && len(choice.ToolCalls) > 0 {
			rw.WriteEventMsg(memory.StepInfoToEventMsg(stepNumber, choice.ToolCalls[0].Function.Name, nil, true))
		}
	}

	exhausted := fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
	stopKind.record(StopKindMaxSteps, exhausted)
	return ExecutorResult{}, exhausted
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
