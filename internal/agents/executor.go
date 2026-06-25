package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/agents/prompts"
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"
	"codeactor/internal/messaging/peer"
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
	// SystemAsHuman places the system prompt in a Human role message instead of System.
	// Used by RepoAgent which prefers this pattern.
	SystemAsHuman bool
	// RepoContext is appended to the system prompt (after the base prompt and environment info).
	// It contains stable repository summary context that changes infrequently, making it ideal
	// for the system prompt where it benefits from LLM prompt caching.
	RepoContext string
	// OnToolResult is an optional callback invoked after each tool executes.
	// Used by Conductor for special handling (e.g. delegate_repo → RepoSummary).
	OnToolResult func(toolName string, result string)
	// Mesh fields (Phase 1: P2P + Memory)
	// AgentID identifies this agent in the Mesh topology.
	AgentID string
	// LayeredMem is the layered memory instance for cross-session context persistence.
	// If nil, the loop behaves exactly as before (backward compatible).
	LayeredMem *memory.LayeredMemory
	// Peer provides P2P communication capability for this agent.
	Peer peer.AgentPeer

	// ── 分布式认知架构：协作能力字段（Phase 1） ──
	// EnableCollaboration 是否启用协作工具（默认 false，向后兼容）
	EnableCollaboration bool
	// CapRegistry 能力注册中心接口（用于 capability_search 工具）
	CapRegistry interface {
		Search(query interface{}) ([]interface{}, error)
	}
	// BlackboardAccess 黑板访问接口（用于 blackboard_read/post 工具）
	BlackboardAccess interface {
		Post(region string, author string, content map[string]interface{}, tags []string, references []string) (string, error)
		Read(region string, filter map[string]interface{}) ([]map[string]interface{}, error)
		Get(entryID string) (map[string]interface{}, bool)
	}
	// DelegChecker 委派安全检查器（环检测+深度+超时）
	DelegChecker interface {
		CanDelegate(targetID string, fromID string) error
		Fork(targetID string) interface{}
	}
	// P2PSupplementEnabled 是否启用角色化 P2P Supplement（由 BaseAgent.FillCollaborationConfig 注入）
	P2PSupplementEnabled bool
}

// DefaultExecutorConfig returns an ExecutorConfig with sensible defaults applied.
// Always prefer this constructor over bare ExecutorConfig{} literals to ensure
// new defaults are automatically picked up.
//
// To explicitly disable collaboration, set cfg.EnableCollaboration = false after construction.
func DefaultExecutorConfig() ExecutorConfig {
	return ExecutorConfig{
		EnableCollaboration: true,
	}
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
	// 追加协作能力描述
	if cfg.EnableCollaboration {
		if cfg.P2PSupplementEnabled {
			// 使用角色化的 P2P Supplement（增强型 Commander 模式）
			role := prompts.DefaultRole(cfg.AgentID)
			supplement, err := prompts.RenderSupplement(prompts.P2PSupplementConfig{
				Role:               role,
				AgentID:            cfg.AgentID,
				AgentName:          cfg.AgentName,
				Capabilities:       strings.Join(prompts.DefaultCapabilities(cfg.AgentID), ", "),
				MaxDelegationDepth: 3,
			})
			if err == nil {
				systemPrompt += "\n\n" + supplement
			} else {
				// 渲染失败时降级：记录警告但不注入旧版协作 prompt
				// （旧版 SubAgentCollaborationPrompt 已被 p2p_supplement.go 取代）
				slog.Warn("Failed to render P2P supplement, skipping collaboration prompt",
					"role", role, "agent", cfg.AgentID, "error", err)
			}
		}
	}
	// 注意：P2PSupplementEnabled=false 时不注入任何协作 prompt
	// （旧版 SubAgentCollaborationPrompt 已被移除）
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

	// Phase 1: Memory recovery — restore local messages from LayeredMem
	if cfg.LayeredMem != nil {
		if localMsgs := cfg.LayeredMem.GetLocalMessages(); len(localMsgs) > 0 {
			for _, msg := range localMsgs {
				// Convert memory.ChatMessage to llm.Message
				var role llm.Role
				switch msg.Type {
				case memory.MessageTypeSystem:
					role = llm.RoleSystem
				case memory.MessageTypeHuman:
					role = llm.RoleUser
				case memory.MessageTypeAssistant:
					role = llm.RoleAssistant
				case memory.MessageTypeTool:
					role = llm.RoleTool
				default:
					role = llm.RoleUser
				}
				llmMsg := llm.Message{
					Role:    role,
					Content: msg.Content,
				}
				// Map tool calls for assistant messages
				if role == llm.RoleAssistant {
					for _, tc := range msg.ToolCalls {
						llmMsg.ToolCalls = append(llmMsg.ToolCalls, llm.ToolCall{
							ID:   tc.ID,
							Type: tc.Type,
							Function: llm.FunctionCall{
								Name:      tc.Function.Name,
								Arguments: string(tc.Function.Arguments),
							},
						})
					}
				}
				// Map tool call id for tool messages
				if role == llm.RoleTool && msg.ToolCallID != nil {
					llmMsg.ToolCallID = *msg.ToolCallID
				}
				messages = append(messages, llmMsg)
				history = append(history, llmMsg)
			}
		}
	}

	// Phase 1: P2P tool injection
	var p2pAdapters []*tools.Adapter
	if cfg.Peer != nil && cfg.AgentID != "" {
		p2pAdapters = buildP2PTeamAdapters(cfg.Peer, cfg.AgentID)
		cfg.Adapters = append(cfg.Adapters, p2pAdapters...)
	}

	// Phase 1b: Collaboration tool injection (分布式认知架构)
	if cfg.EnableCollaboration && cfg.Peer != nil && cfg.AgentID != "" {
		collabAdapters := buildCollaborationAdapters(cfg)
		cfg.Adapters = append(cfg.Adapters, collabAdapters...)
	}

	toolDefs := make([]llm.ToolDef, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		toolDefs[i] = ad.ToToolDef()
	}
	tools.SortToolDefs(toolDefs)

	opts := &llm.CallOptions{}

	for i := 0; i < cfg.MaxSteps; i++ {
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

	// Phase 1: Write assistant message to LayeredMem with auto-promote
		if cfg.LayeredMem != nil {
			if err := cfg.LayeredMem.AddMessageWithPromote(convertLLMToChatMessage(assistantMsg)); err != nil {
				slog.Warn("Failed to store assistant message with promote", "error", err, "agent", cfg.AgentName)
			}
		}

		if len(choice.ToolCalls) == 0 {
			// Phase 3: No tool calls — trigger batch promote before returning
			if cfg.LayeredMem != nil {
				if lm := cfg.LayeredMem.GetLocalMemory(); lm != nil && lm.Size() > 0 {
					if err := cfg.LayeredMem.PromoteLastToShared(); err != nil {
						slog.Debug("Post-loop promote skipped", "reason", err, "agent", cfg.AgentName)
					}
				}
			}
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

			// Phase 1: Write tool message to LayeredMem with auto-promote
			if cfg.LayeredMem != nil {
				if err := cfg.LayeredMem.AddMessageWithPromote(convertLLMToChatMessage(toolMsg)); err != nil {
					slog.Warn("Failed to store tool message with promote", "error", err, "agent", cfg.AgentName, "tool", tc.Function.Name)
				}
			}

			if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
				// Phase 3: agent_exit triggered — trigger batch promote before returning
				if cfg.LayeredMem != nil {
					if lm := cfg.LayeredMem.GetLocalMemory(); lm != nil && lm.Size() > 0 {
						if err := cfg.LayeredMem.PromoteLastToShared(); err != nil {
							slog.Debug("Post-loop promote skipped", "reason", err, "agent", cfg.AgentName)
						}
					}
				}
				return ExecutorResult{Text: toolResult, History: history}, nil
			}
		}
	}

	// Phase 3: Loop exhausted — trigger batch promote before timeout return
	if cfg.LayeredMem != nil {
		if lm := cfg.LayeredMem.GetLocalMemory(); lm != nil && lm.Size() > 0 {
			if err := cfg.LayeredMem.PromoteLastToShared(); err != nil {
				slog.Debug("Post-loop promote skipped", "reason", err, "agent", cfg.AgentName)
			}
		}
	}
	return ExecutorResult{}, fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
}

// ConvertLLMHistoryToMemory 将 RunAgentLoop 返回的 llm.Message 历史转换为 memory.ChatMessage 切片
// 所有消息的 IsSubAgent 设为 true（GroupID 和 ParentID 由 Conductor 后续填充）
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

// convertLLMToChatMessage 将单条 llm.Message 转换为 memory.ChatMessage
// 不标记 IsSubAgent，用于 LayeredMem 内部存储
func convertLLMToChatMessage(msg llm.Message) memory.ChatMessage {
	cm := memory.ChatMessage{}
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
	return cm
}

// buildP2PTeamAdapters 构建 P2P 工具适配器（p2p_query 和 p2p_notify）
func buildP2PTeamAdapters(p peer.AgentPeer, agentID string) []*tools.Adapter {
	return []*tools.Adapter{
		tools.NewAdapter("p2p_query",
			"Send a synchronous P2P query to another agent and get a structured response. Use this when you need information that another agent has (e.g., ask repo-agent for code analysis, browser-agent for web page state).",
			func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				targetID, _ := params["target_agent"].(string)
				method, _ := params["method"].(string)
				payloadRaw, _ := params["payload"]
				if targetID == "" || method == "" {
					return nil, fmt.Errorf("p2p_query: target_agent and method are required")
				}
				var payloadBytes []byte
				if payloadRaw != nil {
					switch v := payloadRaw.(type) {
					case string:
						payloadBytes = []byte(v)
					case []byte:
						payloadBytes = v
					case map[string]interface{}:
						var err error
						payloadBytes, err = json.Marshal(v)
						if err != nil {
							return nil, fmt.Errorf("p2p_query: marshal payload: %w", err)
						}
					case nil:
						payloadBytes = []byte("{}")
					default:
						payloadBytes = []byte("{}")
					}
				} else {
					payloadBytes = []byte("{}")
				}
				timeout := 10 * time.Second
				if to, ok := params["timeout"].(float64); ok && to > 0 {
					timeout = time.Duration(to) * time.Second
				}
				resp, err := p.Request(ctx, method, targetID, payloadBytes, timeout)
				if err != nil {
					return nil, fmt.Errorf("p2p_query to %s via %s: %w", targetID, method, err)
				}
				return string(resp), nil
			}).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_agent": map[string]interface{}{
					"type":        "string",
					"description": "Target agent ID (e.g., 'repo-agent', 'browser-agent', 'devops-agent')",
				},
				"method": map[string]interface{}{
					"type":        "string",
					"description": "The P2P method/rpc name to call on the target agent",
				},
				"payload": map[string]interface{}{
					"type":        "object",
					"description": "JSON-serializable payload to send to the target agent",
				},
				"timeout": map[string]interface{}{
					"type":        "number",
					"description": "Timeout in seconds (default: 10)",
				},
			},
			"required": []string{"target_agent", "method"},
		}),
		tools.NewAdapter("p2p_notify",
			"Broadcast a P2P notification event to all subscribed agents. Use this to inform other agents about state changes (e.g., file modified, task completed).",
			func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				topic, _ := params["topic"].(string)
				payloadRaw, _ := params["payload"]
				if topic == "" {
					return nil, fmt.Errorf("p2p_notify: topic is required")
				}
				var payloadBytes []byte
				if payloadRaw != nil {
					switch v := payloadRaw.(type) {
					case string:
						payloadBytes = []byte(v)
					case []byte:
						payloadBytes = v
					case map[string]interface{}:
						var err error
						payloadBytes, err = json.Marshal(v)
						if err != nil {
							return nil, fmt.Errorf("p2p_notify: marshal payload: %w", err)
						}
					case nil:
						payloadBytes = []byte("{}")
					default:
						payloadBytes = []byte("{}")
					}
				} else {
					payloadBytes = []byte("{}")
				}
				err := p.Publish(ctx, topic, payloadBytes)
				if err != nil {
					return nil, fmt.Errorf("p2p_notify on topic %s: %w", topic, err)
				}
				return map[string]interface{}{"status": "published", "topic": topic}, nil
			}).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"topic": map[string]interface{}{
					"type":        "string",
					"description": "The topic to publish to (e.g., 'file.modified', 'task.completed')",
				},
				"payload": map[string]interface{}{
					"type":        "object",
					"description": "JSON-serializable event data to broadcast",
				},
			},
			"required": []string{"topic"},
		}),
	}
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

// buildCollaborationAdapters 构建分布式认知协作工具集
func buildCollaborationAdapters(cfg ExecutorConfig) []*tools.Adapter {
	var adapters []*tools.Adapter

	// 1. capability_search
	if cfg.CapRegistry != nil {
		adapters = append(adapters,
			tools.NewCapabilitySearchAdapter(cfg.CapRegistry))
	}

	// 2. blackboard_read + blackboard_post
	if cfg.BlackboardAccess != nil {
		adapters = append(adapters,
			tools.NewBlackboardReadAdapter(cfg.BlackboardAccess))
		adapters = append(adapters,
			tools.NewBlackboardPostAdapter(cfg.BlackboardAccess))
	}

	// 3. p2p_delegate (增强版: 带 DelegationContext 安全检查)
	if cfg.Peer != nil && cfg.AgentID != "" {
		delegator := &executorP2PDelegator{
			peer:    cfg.Peer,
			agentID: cfg.AgentID,
		}
		var checker tools.DelegationChecker
		if cfg.DelegChecker != nil {
			checker = cfg.DelegChecker
		}
		adapters = append(adapters,
			tools.NewP2PDelegateAdapter(delegator, checker))
	}

	return adapters
}

// executorP2PDelegator 实现 tools.P2PDelegator 接口
type executorP2PDelegator struct {
	peer    peer.AgentPeer
	agentID string
}

func (d *executorP2PDelegator) Delegate(targetID string, taskDescription string, contextJSON string, timeout time.Duration) (string, error) {
	if d.peer == nil {
		return "", fmt.Errorf("p2p: peer not initialized")
	}

	// 将任务描述作为 method，context 作为 payload 发送
	payload := map[string]interface{}{
		"task":    taskDescription,
		"context": contextJSON,
	}
	payloadBytes, _ := json.Marshal(payload)

	resp, err := d.peer.Request(context.Background(), taskDescription, targetID, payloadBytes, timeout)
	if err != nil {
		return "", fmt.Errorf("p2p delegate to %s: %w", targetID, err)
	}
	return string(resp), nil
}

func (d *executorP2PDelegator) GetAgentID() string {
	return d.agentID
}
