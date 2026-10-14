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
	"yagent/internal/pause"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

type ExecutorConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	// Surface 是注册表驱动的工具面（docs/tool-registry-design.md）。
	// 非 nil 时它接管三件事：每步重算工具声明、按名解析执行器（含冷工具兜底）、
	// 并发谓词。Adapters 仍保留给尚未迁移的调用点与既有循环测试。
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
	// 第三档需要两个条件同时成立：配置允许 + 有重建原料（ThinkLink），与 Director Planner 一致。
	EnableUltimateCompression bool

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

// 并发/串行的判据来自注册表（Spec.Serial），不再是本文件里的一张名字表：
// 新工具只要在实现旁边声明能力，就不用记得再来这里补一条。
func isSerialTool(name string) bool { return toolspec.IsSerial(name) }

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

	ad, found := resolveAdapter(cfg, tc.Function.Name)
	if !found {
		return "", false
	}
	// 模型跳过 load_tool 直接调冷工具：不执行、不报"没这个工具"，
	// 而是把完整 schema 回吐给它，同一步就能自己纠正过来。
	if isColdUnloaded(cfg, tc.Function.Name) {
		return teachColdTool(cfg, tc.Function.Name), true
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
	toolResult, callErr := ad.Call(toolCtx, tc.Function.Arguments)
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

// resolveAdapter 按名解析执行器：Surface 优先（注册表路径），回退到 Adapters 线性扫描。
// 两条路径都是"取第一个同名适配器"，语义一致。
func resolveAdapter(cfg *ExecutorConfig, name string) (*tools.Adapter, bool) {
	if cfg.Surface != nil {
		if ad, state := cfg.Surface.Lookup(name); state != tools.StateUnknown {
			return ad, true
		}
		return nil, false
	}
	for _, t := range cfg.Adapters {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

// isColdUnloaded 报告该工具在 Surface 里还没被模型取回过 schema。
func isColdUnloaded(cfg *ExecutorConfig, name string) bool {
	if cfg.Surface == nil {
		return false
	}
	_, state := cfg.Surface.Lookup(name)
	return state == tools.StateColdKnown
}

// teachColdTool 登记取回并把 schema 作为工具结果回给模型。
func teachColdTool(cfg *ExecutorConfig, name string) string {
	schema, err := cfg.Surface.Materialize([]string{name})
	if err != nil {
		return fmt.Sprintf("Error: cannot load schema for %s: %v", name, err)
	}
	return fmt.Sprintf("Error: schema for %s was not loaded. Full schema:\n%s\nRetry the call now with these parameters.", name, schema)
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
	// deferred 目录跟着系统提示走：它是稳定前缀的一部分，比每步重发的
	// 完整参数声明便宜一个数量级（docs/tool-registry-design.md §7.6）。
	if cfg.Surface != nil {
		if catalog := cfg.Surface.Catalog(); catalog != "" {
			systemPrompt += "\n\n" + catalog
		}
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

	// LLM 调用超时取值：上游显式传入 > 全局运行时值（TUI 模型设置面板可动态修改）
	// > DefaultLLMCallTimeout（默认 10 分钟）。
	llmTimeout := cfg.LLMTimeout
	if llmTimeout <= 0 {
		if t := config.GlobalLLMTimeout(); t > 0 {
			llmTimeout = t
		} else {
			llmTimeout = config.DefaultLLMCallTimeout
		}
	}

	// EffectContext 三兄弟（read_note / read_artifact / list_notes）的正常来源是 Surface：
	// 已核实的五个子 Agent（coding/browser/devops/repo/chat）全部设了 cfg.Surface。
	// 下面这段只服务"只传 Adapters 的旧调用点"——今天只剩循环单测走它，
	// 但删掉会让那些测试静默失去便签工具，所以留着并写清前提。
	if cfg.Surface == nil {
		if bid := blackboard.BoardIDFrom(ctx); bid != "" && blackboardEnabled() {
			has := func(name string) bool {
				for _, ad := range cfg.Adapters {
					if ad.Name() == name {
						return true
					}
				}
				return false
			}
			if !has("read_note") {
				cfg.Adapters = append(cfg.Adapters, newReadNoteAdapter(func() string { return bid }))
			}
			if !has("read_artifact") {
				cfg.Adapters = append(cfg.Adapters, newReadArtifactAdapter())
			}
		}
	}

	// 工具声明：Surface 存在时每步重算（deferred 取回与运行期注册都要立刻生效），
	// 否则沿用历史的一次性快照。
	frozenDefs := make([]llm.ToolDef, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		frozenDefs[i] = ad.ToToolDef()
	}
	tools.SortToolDefs(frozenDefs)
	defsForStep := func() []llm.ToolDef {
		if cfg.Surface != nil {
			return cfg.Surface.Defs()
		}
		return frozenDefs
	}

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
	// stopKind 记录这一轮是**怎么**结束的。终止事件此前只写 reason 字符串，恢复侧
	// 只能靠 errors.Is(err, context.Canceled) 去猜；而"猜"分不出用户主动停止、
	// idle 超时、total 超时，三者的正确续跑策略完全不同。
	// 每个出口都在 RunAgentLoop 内部，所以它只是个局部变量，不必进 ExecutorConfig。
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
				// 文案取自分类时采用的那个错误，而不是循环返回的那个：
				// 取消路径上引擎给的是裸 context.Canceled，真正的原因在 context.Cause 上。
				rw.WriteRunAborted(stopKind.reasonText(termErr.Error()), rs)
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
		// 暂停闸门（A 层）落在 step 边界：上一个批次的工具结果都已提交，所以停在这里
		// 不会留下"副作用做了一半"的状态。ctx 不取消，恢复后从这行原地往下走。
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
				wait := time.Duration(1<<(attempt-1)) * time.Second
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
							Kind:         memory.CompactionKindRule,
							SavedTokens:  compStats.SavedTokens,
						})
					}
				}

				if compression.EstimateMessagesTokens(messages) > threshold {
					// 摘要的独立 LLM 调用（内部上限 90s）不经过 step/llm 心跳，须显式续期。
					activity.Heartbeat(ctx, "compress:emergency")
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
							Kind:         memory.CompactionKindEmergency,
							SavedTokens:  emergencyStats.SavedTokens,
						})
					}
					if compression.EstimateMessagesTokens(messages) > threshold && cfg.EnableUltimateCompression && cfg.UltimateThinkLink != nil {
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
			stopKind.record("", err)
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
							"agent":     cfg.AgentName,
							"content":   entry.Content,
							"timestamp": entry.Timestamp.Format(time.RFC3339Nano),
							"step":      entry.Step,
						}, cfg.AgentName)
					}
					// 与 Director 侧 planner_run.go:48-55 对称：事件总线是**视图**的来源，
					// rollout 才是**恢复**的来源。只有总线上的一份额外好处是 TUI 面板，
					// 代价是这份子 Agent 日志在进程退出后彻底不存在——ultimate 压缩
					// 重建上下文全靠它（RebuildPrompt），恢复后它只能从"原始任务 + 零个
					// T&P 块"重建，等于把压缩后的子 Agent 变成失忆状态。
					// 只落 thought_plan：user_input 那份由各自 Run() 播的种子，
					// 内容等于 rollout 首条 user 消息，落两遍没有意义。
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
			stopKind.record(StopKindPlainTextReply, nil)
			return ExecutorResult{Text: choice.Content, History: history}, nil
		}

		toolCalls := choice.ToolCalls
		isParallelizable := func(i int) bool {
			name := toolCalls[i].Function.Name
			if isInteractiveUserTool(name) || isSerialTool(name) {
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
