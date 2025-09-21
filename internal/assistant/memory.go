package assistant

import (
	"encoding/json" // Added for fmt.Sprintf
	"time"

	"github.com/tmc/langchaingo/llms"
)

// MessageType 定义消息类型
type MessageType string

const (
	MessageTypeSystem    MessageType = "system"
	MessageTypeHuman     MessageType = "human"
	MessageTypeAssistant MessageType = "assistant"
	MessageTypeTool      MessageType = "tool"
)

// ToolCallData 表示工具调用的数据
type ToolCallData struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // 通常是 "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 表示工具调用的函数信息
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ChatMessage 表示一条聊天消息，支持完整的对话上下文
type ChatMessage struct {
	Type       MessageType            `json:"type"`
	Content    string                 `json:"content"`
	ToolCalls  []ToolCallData         `json:"tool_calls,omitempty"`
	ToolCallID *string                `json:"tool_call_id,omitempty"` // 用于 tool message
	Timestamp  time.Time              `json:"timestamp"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// ConversationMemory 管理完整的对话上下文
type ConversationMemory struct {
	Messages []ChatMessage `json:"messages"`
	MaxSize  int           `json:"max_size"`
}

// NewConversationMemory 创建新的对话内存管理器
func NewConversationMemory(maxSize int) *ConversationMemory {
	if maxSize <= 0 {
		maxSize = 300 // 默认最大消息数
	}
	return &ConversationMemory{
		Messages: make([]ChatMessage, 0),
		MaxSize:  maxSize,
	}
}

// AddSystemMessage 添加系统消息
func (cm *ConversationMemory) AddSystemMessage(content string) {
	msg := ChatMessage{
		Type:      MessageTypeSystem,
		Content:   content,
		Timestamp: time.Now(),
	}
	cm.addMessage(msg)
}

// AddHumanMessage 添加用户消息
func (cm *ConversationMemory) AddHumanMessage(content string) {
	msg := ChatMessage{
		Type:      MessageTypeHuman,
		Content:   content,
		Timestamp: time.Now(),
	}
	cm.addMessage(msg)
}

// AddAssistantMessage 添加助手消息
func (cm *ConversationMemory) AddAssistantMessage(content string, toolCalls []ToolCallData) {
	msg := ChatMessage{
		Type:      MessageTypeAssistant,
		Content:   content,
		ToolCalls: toolCalls,
		Timestamp: time.Now(),
	}
	cm.addMessage(msg)
}

// AddToolMessage 添加工具执行结果消息
func (cm *ConversationMemory) AddToolMessage(content string, toolCallID string) {
	msg := ChatMessage{
		Type:       MessageTypeTool,
		Content:    content,
		ToolCallID: &toolCallID,
		Timestamp:  time.Now(),
	}
	cm.addMessage(msg)
}

// addMessage 内部方法：添加消息并维护最大大小限制
func (cm *ConversationMemory) addMessage(msg ChatMessage) {
	cm.Messages = append(cm.Messages, msg)

	// 如果超过最大大小，移除最旧的非系统消息
	if len(cm.Messages) > cm.MaxSize {
		// 保留系统消息（通常是第一条）
		systemMessages := make([]ChatMessage, 0)
		otherMessages := make([]ChatMessage, 0)

		for _, m := range cm.Messages {
			if m.Type == MessageTypeSystem {
				systemMessages = append(systemMessages, m)
			} else {
				otherMessages = append(otherMessages, m)
			}
		}

		// 保留最新的 (maxSize - 系统消息数量) 条非系统消息
		maxOthers := cm.MaxSize - len(systemMessages)
		if maxOthers > 0 && len(otherMessages) > maxOthers {
			otherMessages = otherMessages[len(otherMessages)-maxOthers:]
		}

		// 重新组合消息：系统消息 + 最新的其他消息
		cm.Messages = append(systemMessages, otherMessages...)
	}
}

// ToLangChainMessages 转换为 langchaingo 的消息格式
func (cm *ConversationMemory) ToLangChainMessages() []llms.MessageContent {
	messages := make([]llms.MessageContent, 0, len(cm.Messages))

	// 用于合并连续的assistant消息
	var currentAssistantParts []llms.ContentPart
	var currentToolCalls []llms.ToolCall

	for i, msg := range cm.Messages {
		switch msg.Type {
		case MessageTypeSystem:
			// 如果之前有未处理的assistant消息，先添加
			if len(currentAssistantParts) > 0 || len(currentToolCalls) > 0 {
				messages = append(messages, cm.createAssistantMessage(currentAssistantParts, currentToolCalls))
				currentAssistantParts = nil
				currentToolCalls = nil
			}
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeSystem, msg.Content))
		case MessageTypeHuman:
			// 如果之前有未处理的assistant消息，先添加
			if len(currentAssistantParts) > 0 || len(currentToolCalls) > 0 {
				messages = append(messages, cm.createAssistantMessage(currentAssistantParts, currentToolCalls))
				currentAssistantParts = nil
				currentToolCalls = nil
			}
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, msg.Content))
		case MessageTypeAssistant:
			// 合并连续的assistant消息
			if len(msg.ToolCalls) > 0 {
				// 如果有工具调用，添加到当前工具调用列表
				for _, toolCall := range msg.ToolCalls {
					currentToolCalls = append(currentToolCalls, llms.ToolCall{
						ID:   toolCall.ID,
						Type: toolCall.Type,
						FunctionCall: &llms.FunctionCall{
							Name:      toolCall.Function.Name,
							Arguments: string(toolCall.Function.Arguments),
						},
					})
				}
			}

			// 如果有文本内容，添加到当前文本部分
			if msg.Content != "" {
				currentAssistantParts = append(currentAssistantParts, llms.TextPart(msg.Content))
			}

			// 如果是最后一条消息或者下一条消息不是assistant，则创建assistant消息
			if i == len(cm.Messages)-1 || cm.Messages[i+1].Type != MessageTypeAssistant {
				if len(currentAssistantParts) > 0 || len(currentToolCalls) > 0 {
					messages = append(messages, cm.createAssistantMessage(currentAssistantParts, currentToolCalls))
					currentAssistantParts = nil
					currentToolCalls = nil
				}
			}
		case MessageTypeTool:
			// 如果之前有未处理的assistant消息，先添加
			if len(currentAssistantParts) > 0 || len(currentToolCalls) > 0 {
				messages = append(messages, cm.createAssistantMessage(currentAssistantParts, currentToolCalls))
				currentAssistantParts = nil
				currentToolCalls = nil
			}

			// For tool messages, use ToolCallResponse for langchaingo compatibility
			if msg.ToolCallID != nil {
				messages = append(messages, llms.MessageContent{
					Role: llms.ChatMessageTypeTool,
					Parts: []llms.ContentPart{
						llms.ToolCallResponse{
							ToolCallID: *msg.ToolCallID,
							Content:    msg.Content,
						},
					},
				})
			} else {
				messages = append(messages, llms.TextParts(llms.ChatMessageTypeTool, msg.Content))
			}
		default:
			// 如果之前有未处理的assistant消息，先添加
			if len(currentAssistantParts) > 0 || len(currentToolCalls) > 0 {
				messages = append(messages, cm.createAssistantMessage(currentAssistantParts, currentToolCalls))
				currentAssistantParts = nil
				currentToolCalls = nil
			}
			// Default to human message type
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, msg.Content))
		}
	}

	return messages
}

// createAssistantMessage 创建合并后的assistant消息
func (cm *ConversationMemory) createAssistantMessage(parts []llms.ContentPart, toolCalls []llms.ToolCall) llms.MessageContent {
	// 合并所有文本内容
	var textContent string
	for _, part := range parts {
		if textPart, ok := part.(llms.TextContent); ok {
			if textContent != "" {
				textContent += "\n"
			}
			textContent += textPart.Text
		}
	}

	// 创建新的parts列表
	newParts := make([]llms.ContentPart, 0)

	// 添加合并后的文本内容
	if textContent != "" {
		newParts = append(newParts, llms.TextPart(textContent))
	}

	// 添加工具调用
	for _, toolCall := range toolCalls {
		newParts = append(newParts, toolCall)
	}

	return llms.MessageContent{
		Role:  llms.ChatMessageTypeAI,
		Parts: newParts,
	}
}

// GetMessages 获取所有消息
func (cm *ConversationMemory) GetMessages() []ChatMessage {
	return cm.Messages
}

// Clear 清空所有消息
func (cm *ConversationMemory) Clear() {
	cm.Messages = cm.Messages[:0]
}

// GetLastMessage 获取最后一条消息
func (cm *ConversationMemory) GetLastMessage() *ChatMessage {
	if len(cm.Messages) == 0 {
		return nil
	}
	return &cm.Messages[len(cm.Messages)-1]
}

// GetMessagesByType 按类型获取消息
func (cm *ConversationMemory) GetMessagesByType(msgType MessageType) []ChatMessage {
	var filtered []ChatMessage
	for _, msg := range cm.Messages {
		if msg.Type == msgType {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// Size 返回消息数量
func (cm *ConversationMemory) Size() int {
	return len(cm.Messages)
}
