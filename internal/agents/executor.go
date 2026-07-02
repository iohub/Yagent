package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"codeactor/internal/compact"
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"
	"codeactor/internal/tools"
)

// ExecutorConfig holds the configuration for running an LLM-tool agent loop.
type ExecutorConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	LLM          llm.Engine
	MaxSteps     int
	Publisher    *messaging.MessagePublisher
	AgentName    string
	StopOnFinish bool // if true, return immediately when agent_exit tool is called
	// StepRetries 步骤重试次数，0=不重试（默认）
	StepRetries int
	// CompactEngine is an optional context compression engine.
	// When non-nil, messages are compressed before each LLM call if they exceed
	// the configured token limit. This prevents context overflow in long-running loops.
	CompactEngine *compact.Engine
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
}

// DefaultExecutorConfig returns an ExecutorConfig with sensible defaults applied.
// Always prefer this constructor over bare ExecutorConfig{} literals to ensure
// new defaults are automatically picked up.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{}
}

// ExecutorResult 封装 RunAgentLoop 的完整执行结果
type ExecutorResult struct {
	Text    string        // 最终文本输出（agent 的最后一条消息内容或 agent_exit 的返回值）
	History []llm.Message // 完整的内部消息历史，包括 system prompt、user input、所有 assistant/tool 交互
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

	toolDefs := make([]llm.ToolDef, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		toolDefs[i] = ad.ToToolDef()
	}
	tools.SortToolDefs(toolDefs)

	opts := &llm.CallOptions{}

	// ─── OnAgentStart hook: run before entering the agent loop ───
	if cfg.OnAgentStart != nil {
		if err := cfg.OnAgentStart(ctx); err != nil {
			return ExecutorResult{}, fmt.Errorf("OnAgentStart hook failed: %w", err)
		}
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
		}()
	}

	stepNumber := 0
	for i := 0; i < cfg.MaxSteps; i++ {
		stepNumber++
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)

		maxRetries := cfg.StepRetries
		var resp *llm.Response
		var err error

		// ─── Context Compression Gateway ───
		// Before each LLM call, attempt to compress the message history if
		// a compact engine is configured. This is best-effort: on failure,
		// we log a warning and proceed with the original messages.
		if cfg.CompactEngine != nil {
			// Check for context cancellation before compression
			if ctx.Err() != nil {
				return ExecutorResult{}, ctx.Err()
			}
			compressResult, compressErr := cfg.CompactEngine.Compress(ctx, messages)
			if compressErr != nil {
				slog.Warn("Context compression failed, using original messages",
					"agent", cfg.AgentName, "step", i, "error", compressErr)
			} else if compressResult != nil && len(compressResult.CompressedMessages) > 0 {
				// Only replace messages if compression actually produced output
				messages = compressResult.CompressedMessages
				slog.Debug("Context compressed successfully",
					"agent", cfg.AgentName, "step", i,
					"original_tokens", compressResult.OriginalTokens,
					"compressed_tokens", compressResult.CompressedTokens,
					"ratio", fmt.Sprintf("%.2f%%", compressResult.CompressionRatio*100))
			}
		}

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

			resp, err = cfg.LLM.GenerateContent(ctx, messages, toolDefs, opts)

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
		if choice.Content != "" && cfg.Publisher != nil {
			metadata := map[string]interface{}{}
			if resp.Usage != nil {
				metadata["usage"] = map[string]interface{}{
					"prompt_tokens":             resp.Usage.PromptTokens,
					"completion_tokens":         resp.Usage.CompletionTokens,
					"total_tokens":              resp.Usage.TotalTokens,
					"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
					"cache_read_input_tokens":   resp.Usage.CacheReadInputTokens,
				}
			}
			if len(metadata) > 0 {
				cfg.Publisher.PublishWithMetadata("ai_response", choice.Content, cfg.AgentName, metadata)
			} else {
				cfg.Publisher.Publish("ai_response", choice.Content, cfg.AgentName)
			}
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

		if len(choice.ToolCalls) == 0 {
			return ExecutorResult{Text: choice.Content, History: history}, nil
		}

		for _, tc := range choice.ToolCalls {
			var toolResult string
			var callErr error
			found := false

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"arguments":    tc.Function.Arguments,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			for _, t := range cfg.Adapters {
				if t.Name() == tc.Function.Name {
					found = true
					startTime := time.Now()
					toolResult, callErr = t.Call(ctx, tc.Function.Arguments)
					if callErr != nil {
						toolResult = fmt.Sprintf("Error: %v", callErr)
					}
					logToolCall(tc.Function.Name, cfg.AgentName, tc.Function.Arguments, toolResult, callErr, startTime)
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			if cfg.OnToolResult != nil {
				cfg.OnToolResult(tc.Function.Name, toolResult)
			}

			if cfg.Publisher != nil {
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

			if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
				// Don't call OnStepEnd here — OnAgentExit will handle final state
				return ExecutorResult{Text: toolResult, History: history}, nil
			}
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
