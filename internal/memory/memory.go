package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
	ParentID   string `json:"parent_id,omitempty"`    // 指向触发此调用的 Director tool_call_id
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

		// 修复 tool_call/tool_response 配对（防止溢出截断破坏原子性）
		cm.repairToolCallPairsAfterTruncation()
	}
}

// GetMessages 获取所有消息
func (cm *ConversationMemory) GetMessages() []ChatMessage {
	return cm.Messages
}

// Clear 清空所有消息
func (cm *ConversationMemory) Clear() error {
	cm.Messages = cm.Messages[:0]
	return nil
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

// repairToolCallPairsAfterTruncation 在内存溢出截断后修复 tool_call/tool_response 配对
// 确保不会出现孤立的消息
func (cm *ConversationMemory) repairToolCallPairsAfterTruncation() {
	// 构建一个 tool_call_id → 是否有对应 assistant 的映射
	toolCallIDsWithAssistant := make(map[string]bool)
	// 构建一个 tool_call_id → 是否有对应 tool 响应的映射
	toolCallIDsWithToolResponse := make(map[string]bool)

	// 第一遍：收集所有 assistant 消息中的 tool_calls
	for _, m := range cm.Messages {
		if m.Type == MessageTypeAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				toolCallIDsWithAssistant[tc.ID] = true
			}
		}
	}

	// 第二遍：收集所有 tool 消息中的 tool_call_id
	for _, m := range cm.Messages {
		if m.Type == MessageTypeTool && m.ToolCallID != nil {
			toolCallIDsWithToolResponse[*m.ToolCallID] = true
		}
	}

	// 找出需要删除的消息索引（从后往前遍历，避免索引偏移）
	removeIndices := make(map[int]bool)
	for i, m := range cm.Messages {
		if m.Type == MessageTypeAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !toolCallIDsWithToolResponse[tc.ID] {
					// 这个 assistant 有 tool_calls 但没有对应的 tool 响应
					// 降级此消息：移除 ToolCalls
					cm.Messages[i].ToolCalls = nil
					if cm.Messages[i].Content == "" {
						cm.Messages[i].Content = "[系统提示：工具调用结果因内存限制被截断]"
					}
					slog.Warn("Memory truncation: removed orphaned tool_calls from assistant message", "tool_call_id", tc.ID)
				}
			}
		}
		if m.Type == MessageTypeTool && m.ToolCallID != nil {
			if !toolCallIDsWithAssistant[*m.ToolCallID] {
				// 孤立的 tool 响应，需要删除
				removeIndices[i] = true
				slog.Warn("Memory truncation: removing orphaned tool response", "tool_call_id", *m.ToolCallID)
			}
		}
	}

	// 从后往前删除孤立消息
	if len(removeIndices) > 0 {
		filtered := make([]ChatMessage, 0, len(cm.Messages)-len(removeIndices))
		for i, m := range cm.Messages {
			if !removeIndices[i] {
				filtered = append(filtered, m)
			}
		}
		cm.Messages = filtered
	}
}

// === 以下为新增内容 ===

// Memory is the base interface for all memory implementations.
type Memory interface {
	AddMessage(msg ChatMessage) error
	GetMessages() []ChatMessage
	GetContext() string
	Clear() error
	Size() int
}

// Ensure ConversationMemory satisfies Memory interface at compile time.
var _ Memory = (*ConversationMemory)(nil)

// GetContext returns a formatted context string.
func (cm *ConversationMemory) GetContext() string {
	var sb strings.Builder
	for _, msg := range cm.Messages {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", string(msg.Type), msg.Content))
	}
	return strings.TrimSpace(sb.String())
}

// AddMessage wrapper for ConversationMemory to match Memory interface.
func (cm *ConversationMemory) AddMessage(msg ChatMessage) error {
	switch msg.Type {
	case MessageTypeSystem:
		cm.AddSystemMessage(msg.Content)
	case MessageTypeHuman:
		cm.AddHumanMessage(msg.Content)
	case MessageTypeAssistant:
		cm.AddAssistantMessage(msg.Content, msg.ToolCalls)
	case MessageTypeTool:
		if msg.ToolCallID != nil {
			cm.AddToolMessage(msg.Content, *msg.ToolCallID)
		}
	}
	return nil
}
