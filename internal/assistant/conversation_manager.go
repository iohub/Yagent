package assistant

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/llms"
)

// ConversationManager 负责对话管理和上下文维护
type ConversationManager struct {
	assistant *CodingAssistant
	publisher *MessagePublisher
}

// NewConversationManager 创建新的对话管理器
func NewConversationManager(assistant *CodingAssistant) *ConversationManager {
	return &ConversationManager{
		assistant: assistant,
	}
}

// PrepareMemory 准备对话记忆
func (cm *ConversationManager) PrepareMemory(memory *ConversationMemory) *ConversationMemory {
	taskMemory := memory
	if taskMemory == nil {
		taskMemory = NewConversationMemory(300)
	}

	// Ensure system prompt is always the first message in memory
	if taskMemory.Size() == 0 {
		taskMemory.AddSystemMessage(cm.assistant.generateSystemPrompt())
	} else {
		// Check if system message exists and is current
		systemMessages := taskMemory.GetMessagesByType(MessageTypeSystem)
		if len(systemMessages) == 0 || systemMessages[0].Content != cm.assistant.generateSystemPrompt() {
			taskMemory.Clear()
			taskMemory.AddSystemMessage(cm.assistant.generateSystemPrompt())
		}
	}

	return taskMemory
}

// ProcessConversation 处理对话流程
func (cm *ConversationManager) ProcessConversation(ctx context.Context, memory *ConversationMemory, taskID string) (string, error) {
	// 重置迭代计数器
	cm.assistant.currentIteration = 0
	var lastAssistantResponse string

	// Log initial memory state
	if cm.assistant.logger != nil {
		cm.assistant.logger.LogMemoryState(taskID, memory)
	}

	// Publish conversation start event
	if cm.publisher != nil {
		cm.publisher.Publish("conversation_start", map[string]interface{}{
			"task_id": taskID,
			"message": "Conversation started",
		})
	}

	for {
		// 检查上下文是否已被取消
		select {
		case <-ctx.Done():
			log.Info().Str("task_id", taskID).Msg("Task cancelled, stopping conversation")
			// Publish task cancelled event
			if cm.publisher != nil {
				cm.publisher.Publish("task_cancelled", map[string]interface{}{
					"task_id": taskID,
					"message": "Task cancelled by user",
				})
			}
			return lastAssistantResponse, ctx.Err()
		default:
		}

		// 检查是否超过最大迭代次数
		if !cm.assistant.IncrementIteration() {
			log.Warn().Int("max_iterations", cm.assistant.maxIterations).Msg("Maximum iterations reached, stopping conversation")
			// Publish max iterations reached event
			if cm.publisher != nil {
				cm.publisher.Publish("max_iterations_reached", map[string]interface{}{
					"task_id": taskID,
					"count":   cm.assistant.maxIterations,
				})
			}
			break
		}

		// 获取工具
		tools := cm.assistant.enhancedTools.GetToolsForLLM()

		// 使用指定的 memory 调用 LLM
		messages := memory.ToLangChainMessages()

		// 调用 LLM
		response, err := cm.assistant.client.GenerateCompletionWithTools(ctx, messages, tools, nil)
		if err != nil {
			// Publish LLM generation error event
			if cm.publisher != nil {
				cm.publisher.Publish("llm_generation_error", map[string]interface{}{
					"task_id": taskID,
					"error":   err.Error(),
				})
			}
			// 检查是否为429限流错误
			if cm.assistant.isRateLimitError(err) {
				log.Warn().Msg("Rate limit error (429) detected, starting retry with exponential backoff")

				// 使用指数退避重试
				if retryErr := cm.assistant.rateLimiter.HandleRateLimitRetry(ctx); retryErr != nil {
					return "", fmt.Errorf("failed to handle rate limit retry: %w", retryErr)
				}

				// 重试成功后继续当前迭代
				continue
			}

			return "", fmt.Errorf("failed to generate completion: %w", err)
		}

		// 检查响应是否有效
		if len(response.Choices) == 0 {
			log.Warn().Msg("No choices returned from LLM")
			break
		}

		choice := response.Choices[0]
		lastAssistantResponse = choice.Content

		// Publish LLM response event
		if cm.publisher != nil && choice.Content != "" {
			cm.publisher.Publish("llm_response", map[string]interface{}{
				"task_id": taskID,
				"content": choice.Content,
			})
		}

		// 提取工具调用
		var toolCalls []ToolCallData
		if len(choice.ToolCalls) > 0 {
			for _, toolCall := range choice.ToolCalls {
				if toolCall.FunctionCall != nil {
					// 转换 langchaingo ToolCall 到我们的 ToolCallData 格式
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
		toolCallsJSON, _ := json.Marshal(toolCalls)
		log.Info().Msgf("toolCalls: %+v", string(toolCallsJSON))
		memory.AddAssistantMessage(choice.Content, toolCalls)

		// 执行所有工具调用
		finishCalled := len(toolCalls) == 0
		askUserHelpCalled := len(toolCalls) == 0
		for _, toolCall := range toolCalls {
			log.Info().
				Str("tool_name", toolCall.Function.Name).
				Str("tool_call_id", toolCall.ID).
				Msg("Executing tool call")

			// Publish tool call start event
			if cm.publisher != nil {
				cm.publisher.Publish("tool_call_start", map[string]interface{}{
					"task_id":      taskID,
					"tool_name":    toolCall.Function.Name,
					"tool_call_id": toolCall.ID,
				})
			}

			// 转换为 langchaingo FunctionCall 格式
			var params map[string]interface{}
			if err := json.Unmarshal(toolCall.Function.Arguments, &params); err != nil {
				log.Error().Err(err).Str("tool_call_id", toolCall.ID).Msg("Failed to parse tool arguments")
				memory.AddToolMessage(fmt.Sprintf("Error: %v", err), toolCall.ID)
				continue
			}

			// 检查是否为 finish 工具调用
			if toolCall.Function.Name == "finish" {
				finishCalled = true
			}

			// 检查是否为 ask_user_for_help 工具调用
			if toolCall.Function.Name == "ask_user_for_help" {
				askUserHelpCalled = true
				// 解析参数以获取详细信息
				var helpParams map[string]interface{}
				if err := json.Unmarshal(toolCall.Function.Arguments, &helpParams); err == nil {
					reason, _ := helpParams["reason"].(string)
					question, _ := helpParams["specific_question"].(string)
					options, _ := helpParams["suggested_options"].(string)

					helpMessage := fmt.Sprintf("需要用户帮助:\n原因: %s\n问题: %s", reason, question)
					if options != "" {
						helpMessage += fmt.Sprintf("\n建议选项: %s", options)
					}
				}
			}

			// 使用增强工具管理器执行函数调用
			functionCall := llms.FunctionCall{
				Name:      toolCall.Function.Name,
				Arguments: string(toolCall.Function.Arguments),
			}

			result, err := cm.assistant.enhancedTools.ExecuteFunctionCall(ctx, functionCall)
			if err != nil {
				log.Error().Err(err).Str("tool_call_id", toolCall.ID).Msg("Failed to execute function call")
				memory.AddToolMessage(fmt.Sprintf("Error: %v", err), toolCall.ID)

				// Log tool execution error
				if cm.assistant.logger != nil {
					cm.assistant.logger.LogToolExecution(toolCall.Function.Name, toolCall.ID, false, err)
				}

				// Publish tool call error event
				if cm.publisher != nil {
					cm.publisher.Publish("tool_call_error", map[string]interface{}{
						"task_id":      taskID,
						"tool_name":    toolCall.Function.Name,
						"tool_call_id": toolCall.ID,
						"error":        err.Error(),
					})
				}
				continue
			}

			// Publish tool call result event
			if cm.publisher != nil {
				cm.publisher.Publish("tool_call_result", map[string]interface{}{
					"task_id":      taskID,
					"tool_name":    toolCall.Function.Name,
					"tool_call_id": toolCall.ID,
					"result":       result,
				})
			}

			// 将工具执行结果添加到 memory
			resultJSON, _ := json.Marshal(result)
			memory.AddToolMessage(string(resultJSON), toolCall.ID)

			// Log tool execution success
			if cm.assistant.logger != nil {
				cm.assistant.logger.LogToolExecution(toolCall.Function.Name, toolCall.ID, true, nil)
			}

			// 保存当前memory到home目录的隐藏数据目录
			if cm.assistant.dataManager != nil {
				if err := cm.assistant.dataManager.SaveTaskMemory(taskID, memory); err != nil {
					log.Warn().Err(err).Str("task_id", taskID).Msg("Failed to save task memory to home directory")
				}
			}
			log.Info().
				Str("tool_name", toolCall.Function.Name).
				Str("tool_call_id", toolCall.ID).
				Msg("Function call execution completed")
		}

		// 如果 finish 工具被调用，发送完成消息但不退出迭代
		if finishCalled {
			log.Info().Msg("Finish tool called, but continuing conversation.")
			// Publish task complete event
			if cm.publisher != nil {
				cm.publisher.Publish("task_complete", map[string]interface{}{
					"task_id": taskID,
					"message": "Task completed successfully",
				})
			}
			break
		}

		// 如果 ask_user_for_help 工具被调用，暂停迭代并等待用户回复
		if askUserHelpCalled {
			log.Info().Msg("Ask user for help tool called, pausing iteration loop.")
			// Publish user help needed event
			if cm.publisher != nil {
				cm.publisher.Publish("user_help_needed", map[string]interface{}{
					"task_id": taskID,
					"message": "User help needed, iteration paused",
				})
			}

			// 等待用户回复（通过消息系统接收）
			log.Info().Msg("Waiting for user response...")
			userResponse := <-cm.assistant.waitForUserResponse(taskID)

			if userResponse != "" {
				log.Info().Msg("Received user response, resuming iteration.")
				// 将用户回复添加到对话记忆中
				memory.AddHumanMessage(userResponse)
				// 继续下一轮对话
				continue
			} else {
				log.Warn().Msg("User response cancelled or timeout, breaking iteration.")
				break
			}
		}

		// 继续下一轮对话，让 LLM 基于工具结果生成最终响应
	}

	// 保存最终的memory到home目录的隐藏数据目录
	if cm.assistant.dataManager != nil {
		if err := cm.assistant.dataManager.SaveTaskMemory(taskID, memory); err != nil {
			log.Warn().Err(err).Str("task_id", taskID).Msg("Failed to save task memory to home directory")
		}
	}

	// Publish conversation end event
	if cm.publisher != nil {
		cm.publisher.Publish("conversation_end", map[string]interface{}{
			"task_id": taskID,
			"result":  lastAssistantResponse,
		})
	}

	return lastAssistantResponse, nil
}
