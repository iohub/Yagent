package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/llms"
)

// SubAgent 负责子代理的创建和管理
type SubAgent struct {
	assistant *CodingAssistant
}

// NewSubAgent 创建新的子代理
func NewSubAgent(assistant *CodingAssistant) *SubAgent {
	return &SubAgent{
		assistant: assistant,
	}
}

func (sam *SubAgent) RunSubAgentWithTools(ctx context.Context, systemPrompt string, initialMessage string, taskID string, wsCallback func(messageType string, content string), allowTools []string, disallowTools []string) (string, error) {
	// 创建子代理的独立内存
	subMemory := NewConversationMemory(100) // 较小的内存限制

	// 替换系统提示词中的工作目录占位符
	workingDir := sam.assistant.workingDir
	if workingDir == "" {
		workingDir = "未设置"
	}
	// 添加系统提示词
	subMemory.AddSystemMessage(systemPrompt)

	// 添加初始消息
	subMemory.AddHumanMessage(initialMessage)
	var subTools *EnhancedToolManager
	var err error
	if allowTools == nil && disallowTools == nil {
		subTools, err = NewEnhancedToolManager()
	} else {
		subTools, err = NewEnhancedToolManagerWithTools(allowTools, disallowTools)
	}
	if err != nil {
		return "", fmt.Errorf("failed to create enhanced tool manager: %w", err)
	}

	// 设置工作目录
	subTools.SetWorkingDirectory(sam.assistant.workingDir)

	// 限制子代理的工具集，避免递归调用
	subTools.SetRestrictedMode(true)

	// 设置子代理对主代理的引用
	subTools.SetAssistant(sam.assistant)

	log.Info().
		Str("task_id", taskID).
		Str("system_prompt", systemPrompt[:50]+"...").
		Str("initial_message", initialMessage).
		Msg("Starting sub-agent execution")

	// 执行子代理的对话循环
	subIteration := 0
	maxSubIterations := 20 // 子代理的最大迭代次数

	for subIteration < maxSubIterations {
		subIteration++

		// 获取工具
		tools := subTools.GetToolsForLLM()

		// 获取消息
		messages := subMemory.ToLangChainMessages()

		// 调用LLM
		response, err := sam.assistant.client.GenerateCompletionWithTools(ctx, messages, tools, nil)
		if err != nil {
			if sam.assistant.isRateLimitError(err) {
				log.Warn().Msg("Rate limit error in sub-agent, starting retry")
				if wsCallback != nil {
					wsCallback("rate_limit_wait", "子代理检测到限流错误，正在等待重试...")
				}

				if retryErr := sam.assistant.rateLimiter.HandleRateLimitRetry(ctx, wsCallback); retryErr != nil {
					return "", fmt.Errorf("failed to handle rate limit retry in sub-agent: %w", retryErr)
				}
				continue
			}
			return "", fmt.Errorf("failed to generate completion in sub-agent: %w", err)
		}

		// 检查响应
		if len(response.Choices) == 0 {
			log.Warn().Msg("No choices returned from LLM in sub-agent")
			break
		}

		choice := response.Choices[0]

		// 实时发送响应
		if wsCallback != nil && choice.Content != "" {
			wsCallback("sub_agent_response", choice.Content)
		}

		// 提取工具调用
		var toolCalls []ToolCallData
		if len(choice.ToolCalls) > 0 {
			for _, toolCall := range choice.ToolCalls {
				if toolCall.FunctionCall != nil {
					toolCallData := ToolCallData{
						ID:   toolCall.ID,
						Type: toolCall.Type,
						Function: ToolCallFunction{
							Name:      toolCall.FunctionCall.Name,
							Arguments: json.RawMessage(toolCall.FunctionCall.Arguments),
						},
					}
					toolCalls = append(toolCalls, toolCallData)
				}
			}
		}

		subMemory.AddAssistantMessage(choice.Content, toolCalls)

		// 执行工具调用
		finishCalled := false
		for _, toolCall := range toolCalls {
			log.Info().
				Str("sub_agent_tool", toolCall.Function.Name).
				Str("task_id", taskID).
				Msg("Executing sub-agent tool call")

			if wsCallback != nil {
				wsCallback("sub_agent_tool_call", fmt.Sprintf("子代理正在执行工具: %s", toolCall.Function.Name))
			}

			// 解析参数
			var params map[string]interface{}
			if err := json.Unmarshal(toolCall.Function.Arguments, &params); err != nil {
				log.Error().Err(err).Str("tool_call_id", toolCall.ID).Msg("Failed to parse sub-agent tool arguments")
				subMemory.AddToolMessage(fmt.Sprintf("Error: %v", err), toolCall.ID)
				continue
			}

			// 执行工具调用
			functionCall := llms.FunctionCall{
				Name:      toolCall.Function.Name,
				Arguments: string(toolCall.Function.Arguments),
			}

			result, err := subTools.ExecuteFunctionCall(ctx, functionCall)
			if err != nil {
				log.Error().Err(err).Str("tool_call_id", toolCall.ID).Msg("Failed to execute sub-agent function call")
				subMemory.AddToolMessage(fmt.Sprintf("Error: %v", err), toolCall.ID)

				if wsCallback != nil {
					wsCallback("sub_agent_tool_error", fmt.Sprintf("子代理工具执行错误: %s - %v", toolCall.Function.Name, err))
				}
				continue
			}

			// 添加工具结果
			resultJSON, _ := json.Marshal(result)
			subMemory.AddToolMessage(string(resultJSON), toolCall.ID)

			if wsCallback != nil {
				wsCallback("sub_agent_tool_result", fmt.Sprintf("子代理工具 %s 执行完成", toolCall.Function.Name))
			}

			// 检查是否为finish工具及其执行结果
			if toolCall.Function.Name == "finish" {
				finishCalled = true
				log.Info().Str("task_id", taskID).Msg("Sub-agent finish tool called, exiting")
				if wsCallback != nil {
					wsCallback("sub_agent_complete", "子代理任务完成")
				}
				break
			}
		}

		// 如果finish被调用，退出循环
		if finishCalled {
			log.Info().Str("task_id", taskID).Msg("Sub-agent finish tool called, exiting")
			if wsCallback != nil {
				wsCallback("sub_agent_complete", "子代理任务完成")
			}
			break
		}
	}

	// 获取最终结果
	messages := subMemory.GetMessagesByType(MessageTypeAssistant)
	if len(messages) > 0 {
		return messages[len(messages)-1].Content, nil
	}

	return "子代理执行完成，但未返回结果", nil
}

// isRateLimitError 检查错误是否为429限流错误
func (sam *SubAgent) isRateLimitError(err error) bool {
	return sam.assistant.rateLimiter.IsRateLimitError(err)
}
