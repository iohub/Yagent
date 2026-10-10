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
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// ExecutorConfig holds the configuration for running an LLM-tool agent loop.
type ExecutorConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	LLM          llm.Engine
	MaxSteps     int
	Publisher    EventBus
	AgentName    string
	StopOnFinish bool // if true, return immediately when agent_exit tool is called
	// LLMTimeout 单次LLM调用的超时时间，0=使用默认值3分钟
	LLMTimeout time.Duration
	// StepRetries 步骤重试次数，0=不重试（默认）
	StepRetries int
	// SystemAsHuman places the system prompt in a Human role message instead of System.
	// Used by RepoAgent which prefers this pattern.
	SystemAsHuman bool
	// RepoContext is appended to the system prompt (after the base prompt and environment info).
	// It contains stable repository summary context that changes infrequently, making it ideal
	// for the system prompt where it benefits from LLM prompt caching.
	RepoContext string
	// OnToolResult is an optional callback invoked after each tool executes.
	// Used by Director for special handling (e.g. delegate_repo → RepoSummary).
	OnToolResult func(toolName string, result string)

	// OnAgentStart is called once before the agent loop begins.
	// If it returns an error, the loop is aborted.
	OnAgentStart func(ctx context.Context) error

	// OnAgentExit is called once after the agent loop ends, regardless of outcome.
	// Called via defer with panic recovery.
	OnAgentExit func(ctx context.Context, agentErr error) error

	// OnStepEnd is called after each step's tool calls complete.
	// Errors from this hook are logged but do not abort the loop.
	OnStepEnd func(ctx context.Context, stepInfo StepInfo) error

	// NoActionRetryLimit 允许 LLM 无工具调用纯文本输出的最大次数。
	// 0=禁用（保持旧行为：无工具调用的纯文本输出立即返回，不影响 Director 等现有调用方）；
	// >0 时，LLM 输出无工具调用的纯文本不立即返回，而是追加一条 user 角色提醒消息后继续循环，
	// 累计无工具调用纯文本输出次数达到上限后返回最后的文本（防止死循环）。
	NoActionRetryLimit int

	// 上下文压缩（tool 结果截断）配置，默认零值=关闭，仅调用方显式开启时生效
	EnableContextCompression bool
	// ContextCompressionThreshold 触发上下文压缩的 token 阈值回退值：
	// 模型被内嵌目录收录时按窗口 1/3 推导并忽略此值，未收录且 <=0 时用 compression.DefaultContextCompressionThreshold
	ContextCompressionThreshold int
	// ToolResultKeepTokens 截断后每条 tool 结果保留的 token 数，<=0 时使用默认值 compression.DefaultToolResultKeepTokens
	ToolResultKeepTokens int
	// UltimateThinkLink 三级终极压缩的重建源 (thinklink 读写窄接口，*thinklink.Store 天然实现);
	// 只读部分供 UltimateCompression 重建上下文，写入部分供 Agent 主循环实时记录;
	// nil=禁用终极压缩
	UltimateThinkLink compression.ThinkLinkJournal
	// UltimateKeepPlans 终极压缩保留 Thought & Plan 块数量上限，0=全部保留
	UltimateKeepPlans int

	// MaxParallelReadOnlyTools 单步内"连续只读工具游程组"的最大并行数:
	//   0/负 → 默认 4(DefaultMaxParallelReadOnlyTools,执行器内兜底);
	//   1 → 严格串行(kill-switch,行为与并发改造前完全一致);
	//   >8 → clamp 到 8(MaxMaxParallelReadOnlyTools)。
	// 只影响被 Adapter.IsReadOnly() 标记且不在 deny-list 的工具;
	// 写类/交互/deny-list 工具永远原位串行。
	MaxParallelReadOnlyTools int
}

// DefaultExecutorConfig returns an ExecutorConfig with sensible defaults applied.
// Always prefer this constructor over bare ExecutorConfig{} literals to ensure
// new defaults are automatically picked up.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		MaxParallelReadOnlyTools: DefaultMaxParallelReadOnlyTools,
	}
}

// ExecutorResult 封装 RunAgentLoop 的完整执行结果
type ExecutorResult struct {
	Text    string        // 最终文本输出（agent 的最后一条消息内容或 agent_exit 的返回值）
	History []llm.Message // 完整的内部消息历史，包括 system prompt、user input、所有 assistant/tool 交互
}

// ─── 只读工具并发参数 ─────────────────────────────────────────────────────────

// DefaultMaxParallelReadOnlyTools 连续只读工具游程组的默认最大并行数。
const DefaultMaxParallelReadOnlyTools = 4

// MaxMaxParallelReadOnlyTools 最大并行数硬上限（配置 >8 一律 clamp），防资源失控。
const MaxMaxParallelReadOnlyTools = 8

// defaultToolTimeout 非交互工具单次调用的超时上限。
// 包级变量作为可注入 seam（仅测试替换为短超时；生产代码不得修改，保持默认 180s）。
var defaultToolTimeout = 180 * time.Second

// nonParallelToolNames 硬编码不可并行的工具 deny-list（设计定稿）：
//   - agent_exit: 停止语义 —— 其之前的游程组必须先 flush 落账，其执行后立即
//     return（之后的 tool_calls 不执行不落账）；
//   - delegate_meta: 内部有 registerCustomAgent 注册表副作用，必须原位串行。
var nonParallelToolNames = map[string]bool{
	"agent_exit":    true,
	"delegate_meta": true,
}

// NormalizeMaxParallelReadOnlyTools 归一化配置值：
// 0/负 → 默认 4；1 → 1（严格串行 kill-switch）；>8 → clamp 8；其余原样。
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

// runToolCall 承载单个 tool_call 的完整执行链（与改造前串行循环逐字一致）：
//
//	Heartbeat(start) → Publish(tool_call_start) → 查找 adapter →
//	delegate_ 前缀 LogDelegateCall → WithCancel（非交互再包 WithTimeout
//	defaultToolTimeout，交互工具 ask_user_for_help 豁免 deadline）→
//	Call → 双 cancel → 错误格式化 → logToolCall → Heartbeat(done)
//
// 返回 found=false 表示未注册适配器（调用方按原语义补写 "Tool %s not found"，
// 并跳过 Heartbeat(done) —— 与改造前行为一致）。
// 串行元素与并发组元素两条路径复用本函数，保证行为一致。
func runToolCall(ctx context.Context, cfg *ExecutorConfig, tc llm.ToolCall) (string, bool) {
	// 活动心跳:工具调用发起(detail 在错误消息中可读化为 tool 'NAME' started)
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

			// Log delegate tool calls with full arguments to dedicated delegate log
			if strings.HasPrefix(tc.Function.Name, "delegate_") {
				agentName := strings.TrimPrefix(tc.Function.Name, "delegate_")
				LogDelegateCall(tc.Function.Name, agentName, tc.Function.Arguments)
			}

			// 为工具调用创建独立超时 context，防止非交互工具卡死
			// 同时 WithCancel 保证了父 context 取消时工具调用也会被取消
			cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
			// 交互式等待用户输入的工具（ask_user_for_help）需要无限等待用户响应，
			// 不能加 deadline，否则用户尚未响应调用就会被 context.DeadlineExceeded
			// 自动取消；仅保留 WithCancel，任务中止时仍可经父 context 取消打断等待。
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
			// 活动心跳:工具调用返回(含错误结果路径,用外层循环 ctx 而非已取消的 toolCtx)
			activity.Heartbeat(ctx, "tool:done:"+tc.Function.Name)
			return toolResult, true
		}
	}
	return "", false
}

// RunAgentLoop runs the standard LLM-tool interaction loop.
func RunAgentLoop(ctx context.Context, cfg ExecutorConfig) (ExecutorResult, error) {
	// Publish model info so the TUI can display it in the status bar.
	if cfg.Publisher != nil {
		cfg.Publisher.Publish("model_info", map[string]interface{}{
			"model": cfg.LLM.Model(),
			"agent": cfg.AgentName,
		}, cfg.AgentName)
	}

	// Initialize tool logger at the start to ensure tool-{date}.log
	// exists before any tool calls occur.
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

	// 计算 LLM 调用超时时间
	llmTimeout := cfg.LLMTimeout
	if llmTimeout <= 0 {
		llmTimeout = 5 * time.Minute
	}

	// Blackboard auto-injection: when ctx carries a boardID (set by delegate handler),
	// append read_note + read_artifact so sub-agents can self-serve truncated context
	// without round-tripping through the Director.
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

	// Setup streaming handler for real-time output via ai_chunk events
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

	// ─── OnAgentStart hook: run before entering the agent loop ───
	if cfg.OnAgentStart != nil {
		if err := cfg.OnAgentStart(ctx); err != nil {
			return ExecutorResult{}, fmt.Errorf("OnAgentStart hook failed: %w", err)
		}
	}

	// ─── Rollout: 写入 session_meta 和 turn_context ───
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
		// 运行时真实模型名（llm.Engine.Model()），用于 rollout 恢复侧识别模型
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

		// 写入 task_started 事件
		rw.WriteEventMsg(memory.EventMsg{
			Type: "task_started",
		})
	}

	// ─── OnAgentExit hook: run via defer with panic recovery ───
	var agentErr error
	if cfg.OnAgentExit != nil {
		defer func() {
			if r := recover(); r != nil {
				agentErr = fmt.Errorf("agent panic: %v", r)
			}
			if exitErr := cfg.OnAgentExit(ctx, agentErr); exitErr != nil {
				slog.Warn("OnAgentExit hook failed", "agent", cfg.AgentName, "error", exitErr)
			}
			// Rollout: 写入任务结束事件
			if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
				if agentErr != nil {
					rw.WriteEventMsg(memory.EventMsg{
						Type:   "turn_aborted",
						Reason: agentErr.Error(),
					})
				} else {
					rw.WriteEventMsg(memory.EventMsg{
						Type: "task_complete",
					})
				}
			}
		}()
	}

	stepNumber := 0
	noActionCount := 0 // 累计无工具调用纯文本输出次数

	// writeRollout 实时写入消息到 Rollout 文件（如果 context 中配置了 writer）
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

	// ═══════ 写入初始 system 和 user 消息 ═══════
	writeRollout(systemMsg)
	writeRollout(userMsg)

	// 只读工具游程组的最大并行数(配置值归一化;kill-switch=1)
	maxParallel := NormalizeMaxParallelReadOnlyTools(cfg.MaxParallelReadOnlyTools)

	for i := 0; i < cfg.MaxSteps; i++ {
		stepNumber++
		// 活动心跳:步骤开始,向 activity 监视链上报(无监视器时为静默 no-op)
		activity.Heartbeat(ctx, fmt.Sprintf("step:%d", stepNumber))
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)

		maxRetries := cfg.StepRetries
		var resp *llm.Response
		var err error

		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				// 指数退避，上限30s
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
				// 活动心跳:重试退避结束,续期监视器(退避可能长达数十秒)
				activity.Heartbeat(ctx, fmt.Sprintf("llm:retry:%d", attempt))
			}

			// Publish llm_call_start event before LLM invocation
			if cfg.Publisher != nil {
				cfg.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": cfg.LLM.Model(),
					"agent": cfg.AgentName,
				}, cfg.AgentName)
			}

			// Record start time
			llmStartTime := time.Now()

			// Normalize messages before LLM call to merge consecutive assistants
			messages = llm.NormalizeMessages(messages)

			// 上下文压缩: token 超阈值时按优先级截断 tool 执行结果
			// 阈值按当前模型上下文窗口的 1/3 推导（见 compression.ResolveThreshold）
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
				}

				// 紧急压缩:tool 结果已全部截断后仍超限 → 启动紧急模式, 极致压缩上下文继续任务
				if compression.EstimateMessagesTokens(messages) > threshold {
					newMessages, emergencyStats := compression.EmergencyCompressMessages(ctx, messages, cfg.UserInput, threshold, cfg.LLM, cfg.AgentName, compression.DefaultEmergencyCompressKeepLastN)
					messages = newMessages
					// 同步 history, 避免调用方 ConvertLLMHistoryToMemory 拿到未压缩的旧历史
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
					// 终极压缩 (第三级):两级常规压缩后仍超限，用 thinklink 重建上下文继续任务
					if compression.EstimateMessagesTokens(messages) > threshold && cfg.UltimateThinkLink != nil {
						ultCompressor := compression.NewContextCompressor(cfg.LLM, cfg.AgentName, cfg.UltimateThinkLink, nil)
						newMessages, ultStats := ultCompressor.ApplyUltimate(messages, threshold, nil, cfg.UltimateKeepPlans)
						messages = newMessages
						// 同步 history，避免调用方 ConvertLLMHistoryToMemory 拿到未压缩的旧历史
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
					}
				}
			}

			// Publish ai_stream_start before LLM call
			if cfg.Publisher != nil {
				cfg.Publisher.Publish("ai_stream_start", map[string]interface{}{
					"agent": cfg.AgentName,
				}, cfg.AgentName)
			}

			// 活动心跳:LLM 调用发起(用外层循环 ctx,不受 llmTimeout 影响)
			activity.Heartbeat(ctx, "llm:start")

			// 为每个 LLM 调用添加超时保护，防止远程服务无响应时永久阻塞
			llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
			resp, err = cfg.LLM.GenerateContent(llmCtx, messages, toolDefs, opts)
			llmCancel()

			// 活动心跳:LLM 调用返回(成功/失败公共汇合点,覆盖重试判断前的所有路径)
			activity.Heartbeat(ctx, "llm:done")

			// Publish ai_stream_end after LLM call
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

			// Calculate duration
			llmDuration := time.Since(llmStartTime).Seconds()

			// Publish thinking event before llm_call_end
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

			// Publish llm_call_end event after LLM invocation (regardless of error)
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
			estPrompt := 0
			for _, msg := range messages {
				estPrompt += EstimateTokens(msg.Content)
			}
			estCompletion := EstimateTokens(choice.Content)
			promptTokens = int64(estPrompt)
			completionTokens = int64(estCompletion)
			totalTokens = promptTokens + completionTokens
		}

		// Rollout: 写入 token_count 事件
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

		// Build assistant message
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		}
		messages = append(messages, assistantMsg)
		history = append(history, assistantMsg)

		// thinklink: 提取本轮回复中的 Thought & Plan 块并实时保存（供终极压缩重建上下文）
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
					"You must immediately invoke a tool to execute the next step of your plan (explore/edit/run/verify); a `## Thought & Plan` block alone does not constitute action.\n\n" +
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

		// ═══ 工具调用调度：顺序保持的分段并发 ═══
		// 把单步 tool_calls 按"连续只读游程"切分为调度段（toolbatch.Plan）：
		//   并行段 = 连续的可并行工具（适配器存在 && IsReadOnly &&
		//            非交互 && 不在 deny-list），组内并发执行、join 后按原序 commit；
		//   串行段 = 非并行元素，原位串行执行（与改造前逐元素串行零差异）。
		// fail-safe：适配器不存在或无法判定只读性一律视为不可并行。
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

		// commitToolCall 单线程落账（可观察时序语义：OnToolResult 回调、
		// messages/history append、rollout 写入严格按 tool_calls 原序执行）。
		// publishResultEvent=true 时在此处发出 tool_call_result 事件
		// （串行元素路径，时机与改造前完全一致——OnToolResult 之后）；
		// =false 用于并发组元素：事件已在组内 goroutine 完成时按真实时间
		// 发出，commit 阶段不重发，其余落账语义不变。
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

		// execOneToolCall 原位串行执行单个 tool_call：完整保留改造前循环体
		// 的副作用链与顺序（runToolCall → "Tool %s not found" 兜底 →
		// OnToolResult → Publish(result) → append → writeRollout → 退出判定）。
		// 返回非 nil 表示 agent_exit 触发提前终止（StopOnFinish）。
		execOneToolCall := func(idx int) *ExecutorResult {
			tc := toolCalls[idx]
			toolResult, found := runToolCall(ctx, &cfg, tc)
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			commitToolCall(tc, toolResult, true)

			if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
				// Don't call OnStepEnd here — OnAgentExit will handle final state
				return &ExecutorResult{Text: toolResult, History: history}
			}
			return nil
		}

		for _, seg := range plan {
			if !seg.Parallel {
				// 非并行元素：原位串行（与改造前逐元素执行零差异）。
				// agent_exit：其之前的游程组已 flush 落账，执行后立即 return，
				// 之后的 tool_calls 不执行不落账，无需取消任何并发组。
				if res := execOneToolCall(seg.Indices[0]); res != nil {
					return *res, nil
				}
				continue
			}

			// 并发只读游程组：
			//   组内 goroutine：获取信号量 → Heartbeat(start) →
			//     Publish(tool_call_start) → delegate_ 前缀 LogDelegateCall →
			//     WithCancel(+WithTimeout，交互豁免) → Call → cancel →
			//     错误格式化（与串行逐字一致）→ logToolCall → Heartbeat(done) →
			//     Publish(tool_call_result)
			//   panic 防守（相对串行行为的有意变化）：exec 内具名返回值 recover，
			//     panic 转化为 "Error: panic: %v" 结果并 slog 记录堆栈，
			//     兄弟工具不受影响（串行时代单 panic 会终止整个进程）。
			//   失败传播：组内单个工具失败只影响自身消息，不取消兄弟
			//     （不共享 cancelCtx）；父 ctx 取消经 ctx 链传播到所有在飞工具，
			//     信号量排队未启动的调用也照常启动并落错误消息（toolbatch 负责）。
			//   group 之外：OnToolResult → messages/history → writeRollout
			//     由 commitToolCall 在父线程按原索引序执行。
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

		// OnStepEnd hook — only when not exiting via agent_exit
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
			// Rollout: 写入 sub_agent_activity 事件
			if rw := memory.GetRolloutWriter(ctx); rw != nil && rw.Enabled() {
				event := memory.StepInfoToEventMsg(stepNumber, toolName, nil, true)
				rw.WriteEventMsg(event)
			}
		}
	}

	return ExecutorResult{}, fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
}

// ConvertLLMHistoryToMemory 将 RunAgentLoop 返回的 llm.Message 历史转换为 memory.ChatMessage 切片
// 所有消息的 IsSubAgent 设为 true（GroupID 和 ParentID 由 Director 后续填充）
func ConvertLLMHistoryToMemory(history []llm.Message) []memory.ChatMessage {
	result := make([]memory.ChatMessage, 0, len(history))
	for _, msg := range history {
		cm := memory.ChatMessage{
			IsSubAgent: true, // 标记为 sub-agent 内部消息
		}
		// 根据 role 映射 Type
		switch msg.Role {
		case llm.RoleSystem:
			cm.Type = memory.MessageTypeSystem
		case llm.RoleUser:
			cm.Type = memory.MessageTypeHuman
		case llm.RoleAssistant:
			cm.Type = memory.MessageTypeAssistant
			// 转换 ToolCalls
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

// logToolCall records a tool call with formatted arguments, duration, and error info.
func logToolCall(toolName, agentName, args string, result string, err error, startTime time.Time) {
	// Format arguments as JSON if possible
	argsJSON := args
	if data, err := json.MarshalIndent(json.RawMessage(args), "", "  "); err == nil {
		argsJSON = string(data)
	}

	// Ensure tool logger is initialized (idempotent)
	_ = InitToolLogger()

	// Calculate duration
	duration := time.Since(startTime)

	// Log the tool call
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	LogToolCall(toolName, agentName, argsJSON, result, errMsg, duration)
}
