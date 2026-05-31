package memory

import (
	"encoding/json"
	"time"

	"codeactor/internal/llm"
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

	// Sub-agent 分组元数据
	GroupID    string `json:"group_id,omitempty"`     // 标识一次 sub-agent 调用，同一调用的消息共享此ID
	ParentID   string `json:"parent_id,omitempty"`    // 指向触发此调用的 Conductor tool_call_id
	IsSubAgent bool   `json:"is_sub_agent,omitempty"` // 快速过滤标记，true表示此消息属于sub-agent内部
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

// ToMessages 将对话记忆转换为 LLM 消息格式，自动跳过 sub-agent 内部消息
func (cm *ConversationMemory) ToMessages() []llm.Message {
	var messages []llm.Message
	for _, m := range cm.Messages {
		// 跳过 sub-agent 内部消息
		if m.IsSubAgent {
			continue
		}
		messages = append(messages, ConvertMemoryMessageToLLMSMessage(m))
	}
	return messages
}

// ConvertMemoryMessageToLLMSMessage 将内存中的 ChatMessage 转换为 LLM Message
func ConvertMemoryMessageToLLMSMessage(msg ChatMessage) llm.Message {
	role := llm.RoleUser
	switch msg.Type {
	case MessageTypeSystem:
		role = llm.RoleSystem
	case MessageTypeHuman:
		role = llm.RoleUser
	case MessageTypeAssistant:
		role = llm.RoleAssistant
	case MessageTypeTool:
		role = llm.RoleTool
	}

	result := llm.Message{
		Role: role,
	}

	if msg.Content != "" && msg.Type != MessageTypeTool {
		result.Content = msg.Content
	}

	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, llm.ToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Function: llm.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: string(tc.Function.Arguments),
				},
			})
		}
	}

	if msg.Type == MessageTypeTool && msg.ToolCallID != nil {
		result.ToolCallID = *msg.ToolCallID
		result.Content = msg.Content
	}

	return result
}
