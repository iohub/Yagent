package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/memory"

	"github.com/tmc/langchaingo/llms"
)

//go:embed conductor.prompt.md
var conductorPrompt string

// CustomAgent stores a dynamically designed agent created by Meta-Agent.
// Once registered, it becomes available as a permanent delegate tool.
type CustomAgent struct {
	Name         string   // snake_case identifier used for the delegate tool name
	DisplayName  string   // human-readable agent name
	SystemPrompt string   // the full system prompt designed by Meta-Agent
	ToolsUsed    []string // tool names this agent was designed to use
	Description  string   // short description for the LLM
}

// metaAgentResult parses the <execution_result> JSON block from Meta-Agent output.
type metaAgentResult struct {
	AgentName string                 `json:"agent_name"`
	ToolsUsed []string               `json:"tools_used"`
	Result    map[string]interface{} `json:"result"`
}

type ConductorAgent struct {
	BaseAgent
	RepoAgent    *RepoAgent
	CodingAgent  *CodingAgent
	ChatAgent    *ChatAgent
	MetaAgent    *MetaAgent
	GlobalCtx    *globalctx.GlobalCtx
	Adapters     []*tools.Adapter
	maxSteps     int
	toolDefMap   map[string]ToolDefinition // tool name → definition from tools.json
	customAgents map[string]*CustomAgent   // delegate_<name> → agent design
}

func NewConductorAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM, repo *RepoAgent, coding *CodingAgent, chat *ChatAgent, meta *MetaAgent, maxSteps int) *ConductorAgent {
	// self-reference for closures that need the ConductorAgent after construction
	var self *ConductorAgent
	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		return repo.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Repo-Agent"},
		},
		"required": []string{"task"},
	})

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		if globalCtx.RepoSummary != "" {
			task = fmt.Sprintf("%s\n\n#Repository Context:\n%s", task, globalCtx.RepoSummary)
		}
		return coding.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Coding-Agent"},
		},
		"required": []string{"task"},
	})

	delegateChat := tools.NewAdapter("delegate_chat", "Delegate general conversation, explanation, or non-coding tasks to Chat-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		return chat.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The message or question for Chat-Agent"},
		},
		"required": []string{"task"},
	})

	delegateMeta := tools.NewAdapter("delegate_meta", "Delegate to Meta-Agent to design and execute a custom specialized agent. Use this when NO existing agent (Repo/Coding/Chat) can adequately handle the task. Meta-Agent will craft a tailored system prompt using prompt engineering best practices, select appropriate tools, execute the task, and return structured JSON results. After execution, the designed agent is automatically registered as a new permanent delegate tool for future use.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		slog.Info("Conductor delegating to Meta-Agent", "task", task)
		rawOutput, err := meta.Run(ctx, task)
		if err != nil {
			return nil, fmt.Errorf("Meta-Agent execution failed: %w", err)
		}

		// Try to parse the structured output and register the designed agent
				systemPrompt, execResult, parseErr := parseMetaAgentOutput(rawOutput)
				if parseErr != nil {
					slog.Warn("Failed to parse Meta-Agent structured output, returning raw output", "error", parseErr)
					return rawOutput, nil
				}

				// ── Strict parse succeeded ──
			// Register the newly designed agent if it has a valid name and prompt
			if execResult.AgentName != "" && systemPrompt != "" {
				// Convert agent name to snake_case for the delegate tool name
				snakeName := toSnakeCase(execResult.AgentName)
				customAgent := &CustomAgent{
					Name:         snakeName,
					DisplayName:  execResult.AgentName,
					SystemPrompt: systemPrompt,
					ToolsUsed:    execResult.ToolsUsed,
					Description:  fmt.Sprintf("Custom agent designed for: %s. Uses tools: %s.", execResult.AgentName, strings.Join(execResult.ToolsUsed, ", ")),
				}
				self.registerCustomAgent(customAgent)

				// Format the result to inform the LLM about the new agent
				resultJSON, _ := json.Marshal(execResult.Result)
				formattedResult := fmt.Sprintf(
					"[Meta-Agent Execution Result]\nAgent: %s\nTools used: %s\nResult: %s\n\n[New Agent Registered]\nA new specialized agent \"%s\" is now available via the `delegate_%s` tool for future tasks of this type.",
					execResult.AgentName,
					strings.Join(execResult.ToolsUsed, ", "),
					string(resultJSON),
					execResult.AgentName,
					snakeName,
				)
				return formattedResult, nil
			}

			// If parsing succeeded but no agent to register, just return the execution result
			if execResult != nil {
				resultJSON, _ := json.Marshal(execResult.Result)
				return fmt.Sprintf("[Meta-Agent Execution Result]\nResult: %s", string(resultJSON)), nil
			}

			return rawOutput, nil
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "Detailed task description for Meta-Agent. Include: what needs to be accomplished, why existing agents are insufficient, and what the expected output format should be."},
		},
		"required": []string{"task"},
	})

	adapters := []*tools.Adapter{
		tools.NewAdapter("finish", "Indicate that the current task is finished. The output of this tool call will be a description of why the task is finished, which could be because the task is completed or cannot be completed and must be terminated.", globalCtx.FlowOps.ExecuteFinish).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "A description of why the task is finished, e.g., task completed, cannot complete, or must terminate."},
			},
			"required": []string{"reason"},
		}),
	}

	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	// Build a map from tool name to definition for later use by custom agents
	toolDefMap := make(map[string]ToolDefinition, len(toolDefs))
	for _, def := range toolDefs {
		toolDefMap[def.Name] = def
	}

	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	self = &ConductorAgent{
		BaseAgent:    BaseAgent{LLM: llm, Publisher: globalCtx.Publisher},
		RepoAgent:    repo,
		CodingAgent:  coding,
		ChatAgent:    chat,
		MetaAgent:    meta,
		GlobalCtx:    globalCtx,
		Adapters:     append(adapters, delegateRepo, delegateCoding, delegateChat, delegateMeta),
		maxSteps:     maxSteps,
		toolDefMap:   toolDefMap,
		customAgents: make(map[string]*CustomAgent),
	}
	return self
}

func (a *ConductorAgent) Name() string {
	return "Conductor"
}

// getToolFunc returns the ToolFunc implementation for a given tool name.
// This is used when constructing tool adapters for dynamically created agents.
func (a *ConductorAgent) getToolFunc(name string) tools.ToolFunc {
	switch name {
	case "read_file":
		return a.GlobalCtx.FileOps.ExecuteReadFile
	case "search_replace_in_file":
		return a.GlobalCtx.ReplaceTool.ExecuteReplaceBlock
	case "create_file":
		return a.GlobalCtx.FileOps.ExecuteCreateFile
	case "run_terminal_cmd":
		return a.GlobalCtx.SysOps.ExecuteRunTerminalCmd
	case "search_by_regex":
		return a.GlobalCtx.SearchOps.ExecuteGrepSearch
	case "delete_file":
		return a.GlobalCtx.FileOps.ExecuteDeleteFile
	case "rename_file":
		return a.GlobalCtx.FileOps.ExecuteRenameFile
	case "list_dir":
		return a.GlobalCtx.FileOps.ExecuteListDir
	case "print_dir_tree":
		return a.GlobalCtx.FileOps.ExecutePrintDirTree
	case "semantic_search":
		return a.GlobalCtx.RepoOps.ExecuteSemanticSearch
	case "query_code_skeleton":
		return a.GlobalCtx.RepoOps.ExecuteQueryCodeSkeleton
	case "query_code_snippet":
		return a.GlobalCtx.RepoOps.ExecuteQueryCodeSnippet
	case "thinking":
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return a.GlobalCtx.ThinkingTool.Call(ctx, string(inputBytes))
		}
	case "finish":
		return a.GlobalCtx.FlowOps.ExecuteFinish
	default:
		return nil
	}
}

// parseMetaAgentOutput extracts the <agent_design> system prompt and <execution_result> JSON
// from the raw Meta-Agent output string.
func parseMetaAgentOutput(output string) (systemPrompt string, execResult *metaAgentResult, err error) {
	// Extract <agent_design> block
	designStart := strings.Index(output, "<agent_design>")
	designEnd := strings.Index(output, "</agent_design>")
	if designStart != -1 && designEnd != -1 {
		systemPrompt = strings.TrimSpace(output[designStart+len("<agent_design>") : designEnd])
	}

	// Extract <execution_result> block
	resultStart := strings.Index(output, "<execution_result>")
	resultEnd := strings.Index(output, "</execution_result>")
	if resultStart == -1 || resultEnd == -1 {
		return systemPrompt, nil, fmt.Errorf("execution_result block not found in Meta-Agent output")
	}

	jsonStr := strings.TrimSpace(output[resultStart+len("<execution_result>") : resultEnd])
	execResult = &metaAgentResult{}
	if err := json.Unmarshal([]byte(jsonStr), execResult); err != nil {
		return systemPrompt, nil, fmt.Errorf("failed to parse execution_result JSON: %w", err)
	}

	return systemPrompt, execResult, nil
}

// toSnakeCase converts a display name like "Security Auditor" to "security_auditor".
func toSnakeCase(name string) string {
	// Lowercase and replace non-alphanumeric characters with underscores
	var result strings.Builder
	for i, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if i > 0 {
				result.WriteRune('_')
			}
		} else {
			result.WriteRune('_')
		}
	}
	// Trim leading/trailing underscores and collapse consecutive underscores
	raw := result.String()
	// Collapse consecutive underscores
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "custom_agent"
	}
	return raw
}

// registerCustomAgent creates a new delegate_<name> tool for a custom agent designed by Meta-Agent
// and adds it to the Conductor's Adapters list. The agent becomes permanently available.
func (a *ConductorAgent) registerCustomAgent(ca *CustomAgent) {
	delegateName := "delegate_" + ca.Name

	// Check if already registered
	if _, exists := a.customAgents[delegateName]; exists {
		slog.Info("Custom agent already registered", "name", delegateName)
		return
	}

	// Build tool adapters for the custom agent's selected tools
	customAdapters := make([]*tools.Adapter, 0, len(ca.ToolsUsed))
	for _, toolName := range ca.ToolsUsed {
		fn := a.getToolFunc(toolName)
		if fn == nil {
			slog.Warn("Custom agent references unknown tool", "agent", ca.Name, "tool", toolName)
			continue
		}
		def, ok := a.toolDefMap[toolName]
		if !ok {
			slog.Warn("Tool definition not found in toolDefMap", "tool", toolName)
			continue
		}
		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		customAdapters = append(customAdapters, adapter)
	}

	// Add finish tool so the custom agent can signal completion
	finishDef, ok := a.toolDefMap["finish"]
	if ok {
		fn := a.getToolFunc("finish")
		adapter := tools.NewAdapter("finish", finishDef.Description, fn).WithSchema(finishDef.Parameters)
		customAdapters = append(customAdapters, adapter)
	}

	// Create the delegate tool that executes the custom agent
	// Capture ca and customAdapters in closure
	agentRef := ca
	adaptersRef := customAdapters

	description := fmt.Sprintf("Delegate to %s — a custom specialized agent designed by Meta-Agent. %s",
		ca.DisplayName, ca.Description)

	delegateAdapter := tools.NewAdapter(delegateName, description,
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			return a.executeCustomAgent(ctx, agentRef, adaptersRef, task)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for " + ca.DisplayName},
		},
		"required": []string{"task"},
	})

	a.Adapters = append(a.Adapters, delegateAdapter)
	a.customAgents[delegateName] = ca

	slog.Info("Custom agent registered", "delegate_name", delegateName, "display_name", ca.DisplayName, "tools", ca.ToolsUsed)
}

// executeCustomAgent runs a custom agent with its designed system prompt and selected tools.
// It follows the same LLM-tool loop pattern as other agents.
func (a *ConductorAgent) executeCustomAgent(ctx context.Context, ca *CustomAgent, adapters []*tools.Adapter, task string) (string, error) {
	systemPrompt := a.GlobalCtx.FormatPrompt(ca.SystemPrompt)

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(task)},
		},
	}

	llmTools := make([]llms.Tool, len(adapters))
	for i, ad := range adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	// Use a reasonable max steps for custom agents
	maxSteps := 15
	for i := 0; i < maxSteps; i++ {
		slog.Debug("CustomAgent calling LLM", "agent", ca.Name, "step", i)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("CustomAgent LLM error", "agent", ca.Name, "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		if msg.Content != "" {
			if a.Publisher != nil {
				a.Publisher.Publish("ai_response", msg.Content, ca.DisplayName)
			}
		}

		parts := []llms.ContentPart{llms.TextPart(msg.Content)}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, tc)
		}

		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: parts,
		})

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, tc := range msg.ToolCalls {
			var toolResult string
			var callErr error
			found := false

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"arguments":    tc.FunctionCall.Arguments,
					"tool_call_id": tc.ID,
				}, ca.DisplayName)
			}

			for _, t := range adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, callErr = t.Call(ctx, tc.FunctionCall.Arguments)
					if callErr != nil {
						toolResult = fmt.Sprintf("Error: %v", callErr)
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
			}

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, ca.DisplayName)
			}

			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    toolResult,
					},
				},
			})

			if tc.FunctionCall.Name == "finish" {
				return toolResult, nil
			}
		}
	}

	return "", fmt.Errorf("CustomAgent %s exceeded max steps", ca.Name)
}

func convertToolCalls(tcs []llms.ToolCall) []memory.ToolCallData {
	var res []memory.ToolCallData
	for _, tc := range tcs {
		res = append(res, memory.ToolCallData{
			ID:   tc.ID,
			Type: string(tc.Type),
			Function: memory.ToolCallFunction{
				Name:      tc.FunctionCall.Name,
				Arguments: json.RawMessage(tc.FunctionCall.Arguments),
			},
		})
	}
	return res
}

func convertMemoryMessageToLLMSMessage(msg memory.ChatMessage) llms.MessageContent {
	role := llms.ChatMessageTypeHuman
	switch msg.Type {
	case memory.MessageTypeSystem:
		role = llms.ChatMessageTypeSystem
	case memory.MessageTypeHuman:
		role = llms.ChatMessageTypeHuman
	case memory.MessageTypeAssistant:
		role = llms.ChatMessageTypeAI
	case memory.MessageTypeTool:
		role = llms.ChatMessageTypeTool
	}

	parts := []llms.ContentPart{}

	if msg.Content != "" && msg.Type != memory.MessageTypeTool {
		parts = append(parts, llms.TextPart(msg.Content))
	}

	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			parts = append(parts, llms.ToolCall{
				ID:   tc.ID,
				Type: string(tc.Type),
				FunctionCall: &llms.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: string(tc.Function.Arguments),
				},
			})
		}
	}

	if msg.Type == memory.MessageTypeTool && msg.ToolCallID != nil {
		parts = append(parts, llms.ToolCallResponse{
			ToolCallID: *msg.ToolCallID,
			Content:    msg.Content,
		})
	}

	return llms.MessageContent{
		Role:  role,
		Parts: parts,
	}
}

func (a *ConductorAgent) Run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	if mem != nil {
		// Check if the last message is the same as input to avoid duplication
		// because handleChatMessage might have already added it.
		lastMsg := mem.GetLastMessage()
		if lastMsg == nil || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
			mem.AddHumanMessage(input)
		}
	}

	var messages []llms.MessageContent

	// Always start with System Prompt (with any registered custom agents appended)
	systemPrompt := a.GlobalCtx.FormatPrompt(conductorPrompt)
	if len(a.customAgents) > 0 {
		systemPrompt += "\n\n<custom_agents>\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
		for _, ca := range a.customAgents {
			systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
		}
		systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n</custom_agents>\n"
	}
	messages = append(messages, llms.MessageContent{
		Role:  llms.ChatMessageTypeSystem,
		Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
	})

	if mem != nil {
		for _, m := range mem.GetMessages() {
			// Skip system messages from memory to avoid conflict with the fresh prompt
			if m.Type == memory.MessageTypeSystem {
				continue
			}
			messages = append(messages, convertMemoryMessageToLLMSMessage(m))
		}
	} else {
		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		})
	}

	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	for i := 0; i < a.maxSteps; i++ {
		slog.Debug("ConductorAgent calling LLM", "step", i, "messages", messages)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("ConductorAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		slog.Debug("ConductorAgent LLM response", "step", i, "message", msg)

		if msg.Content != "" {
			if a.Publisher != nil {
				a.Publisher.Publish("ai_response", msg.Content, a.Name())
			}
		}

		if mem != nil {
			mem.AddAssistantMessage(msg.Content, convertToolCalls(msg.ToolCalls))
		}

		parts := []llms.ContentPart{llms.TextPart(msg.Content)}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, tc)
		}

		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: parts,
		})

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, tc := range msg.ToolCalls {
			var toolResult string
			var err error
			found := false

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"arguments":    tc.FunctionCall.Arguments,
					"tool_call_id": tc.ID,
				}, a.Name())
			}
			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error: %v", err)
					} else if t.Name() == "delegate_repo" {
						// toolResult is a JSON string (e.g. "\"summary...\""), so we need to unmarshal it
						// to get the actual text content
						var summary string
						if err := json.Unmarshal([]byte(toolResult), &summary); err == nil {
							a.GlobalCtx.RepoSummary = summary
						} else {
							a.GlobalCtx.RepoSummary = toolResult
						}
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
			}

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, a.Name())
			}

			if mem != nil {
				mem.AddToolMessage(toolResult, tc.ID)
			}

			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    toolResult,
					},
				},
			})
			if tc.FunctionCall.Name == "finish" {
				return "Task completed successfully", nil
			}

		}
	}

	return "", fmt.Errorf("ConductorAgent exceeded max steps")
}
