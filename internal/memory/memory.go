package memory

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"yagent/internal/llm"
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
	IsAnchored bool                   `json:"is_anchored,omitempty"` // 锚定标记，true 表示此消息永不压缩

	// Sub-agent 分组元数据
	GroupID    string `json:"group_id,omitempty"`     // 标识一次 sub-agent 调用，同一调用的消息共享此ID
	ParentID   string `json:"parent_id,omitempty"`    // 指向触发此调用的 Director tool_call_id
	IsSubAgent bool   `json:"is_sub_agent,omitempty"` // 快速过滤标记，true表示此消息属于sub-agent内部
}

// SubAgentMemory 保存子 Agent 的执行结果与内部对话历史，供 Director 记忆注入使用。
// Memory 直接复用本包 ChatMessage（字段为原 director 包精简版 ChatMessage 的超集，
// 额外字段零值语义一致）；替代原 internal/agents/director/types.go 中的重复定义
// （类型下沉去重，依赖方向固定为 memory ← agents/director ← app）。
type SubAgentMemory struct {
	Text   string        // 子 Agent 返回的文本结果摘要
	Memory []ChatMessage // 子 Agent 的内部对话历史
}

// ConversationMemory 管理完整的对话上下文。
//
// messages 刻意**不导出**：并发准入打开之后，多个 goroutine 会往同一份记忆里追加
// （Director 的注入跑在并行委派的工作线程上，planner 在主线程上写）。留一个导出字段
// 就等于承诺"锁保护一切"，而它实际只保护走方法的那一半 —— 本类型今天有 31 处包外
// 直接读写（datamanager 甚至会 append）。JSON 契约由下面的 Marshal/Unmarshal 原样保留。
type ConversationMemory struct {
	mu       sync.RWMutex
	messages []ChatMessage

	// MaxSize 是条数上限。构造后没人改（http 只读它做回显），所以不在锁的范围内；
	// 要动它必须连同这里的说明一起改。
	MaxSize int
}

// conversationMemoryJSON 是这份记忆的线上形态，与字段导出时的
// {"messages":[…],"max_size":n} 逐字节相同 —— 封装不能改协议。
type conversationMemoryJSON struct {
	Messages []ChatMessage `json:"messages"`
	MaxSize  int           `json:"max_size"`
}

func (cm *ConversationMemory) MarshalJSON() ([]byte, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return json.Marshal(conversationMemoryJSON{Messages: cm.messages, MaxSize: cm.MaxSize})
}

func (cm *ConversationMemory) UnmarshalJSON(data []byte) error {
	var raw conversationMemoryJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.messages = raw.Messages
	if raw.MaxSize > 0 {
		cm.MaxSize = raw.MaxSize
	}
	return nil
}

// NewConversationMemory 创建新的对话内存管理器
func NewConversationMemory(maxSize int) *ConversationMemory {
	if maxSize <= 0 {
		maxSize = 300 // 默认最大消息数
	}
	return &ConversationMemory{
		messages: make([]ChatMessage, 0),
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
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.addMessageLocked(msg)
}

// AddHumanMessage 添加用户消息
func (cm *ConversationMemory) AddHumanMessage(content string) {
	msg := ChatMessage{
		Type:      MessageTypeHuman,
		Content:   content,
		Timestamp: time.Now(),
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.addMessageLocked(msg)
}

// AddAssistantMessage 添加助手消息
func (cm *ConversationMemory) AddAssistantMessage(content string, toolCalls []ToolCallData) {
	msg := ChatMessage{
		Type:      MessageTypeAssistant,
		Content:   content,
		ToolCalls: toolCalls,
		Timestamp: time.Now(),
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.addMessageLocked(msg)
}

// AddToolMessage 添加工具执行结果消息
func (cm *ConversationMemory) AddToolMessage(content string, toolCallID string) {
	msg := ChatMessage{
		Type:       MessageTypeTool,
		Content:    content,
		ToolCallID: &toolCallID,
		Timestamp:  time.Now(),
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.addMessageLocked(msg)
}

// addMessageLocked 添加消息并维护最大大小限制。调用者必须持有写锁。
func (cm *ConversationMemory) addMessageLocked(msg ChatMessage) {
	cm.messages = append(cm.messages, msg)

	// 如果超过最大大小，移除最旧的非系统消息
	if len(cm.messages) > cm.MaxSize {
		// 保留系统消息（通常是第一条）
		systemMessages := make([]ChatMessage, 0)
		otherMessages := make([]ChatMessage, 0)

		for _, m := range cm.messages {
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
		cm.messages = append(systemMessages, otherMessages...)

		// 修复 tool_call/tool_response 配对（防止溢出截断破坏原子性）
		cm.repairToolCallPairsAfterTruncationLocked()
	}
}

// Snapshot 返回消息列表的**拷贝**。
//
// 调用方拿到的是这一刻的快照，之后任何追加都不会看见 —— 这是"在 step 边界读一次"
// 唯一安全的读法。绝不要在循环里持有返回的 slice 后又往里写：append 在容量够时
// 是原地写，两条路径会互相踩对方的元素。
func (cm *ConversationMemory) Snapshot() []ChatMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := make([]ChatMessage, len(cm.messages))
	copy(out, cm.messages)
	return out
}

// GetMessages 是 Snapshot 的旧名（有 15 个调用点）。
// 注释一直写着 "thread-safe copy"，而它其实返回的是内部 slice 本身 —— 现在才名副其实。
func (cm *ConversationMemory) GetMessages() []ChatMessage {
	return cm.Snapshot()
}

// Replace 整体替换消息列表。用于"从磁盘/rollout 重建一份记忆"这类一次性装载，
// 调用方在此之后不应再持有传入的 slice。
func (cm *ConversationMemory) Replace(msgs []ChatMessage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.messages = msgs
}

// Append 批量追加（从 JSONL 恢复任务记忆时用）。走的是与 Add* 同一条溢出/配对修复逻辑。
func (cm *ConversationMemory) Append(msgs ...ChatMessage) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for _, m := range msgs {
		cm.addMessageLocked(m)
	}
}

// Clear 清空所有消息
func (cm *ConversationMemory) Clear() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.messages = cm.messages[:0]
	return nil
}

// LastMessage 返回最后一条消息的**拷贝**。
//
// 旧版 GetLastMessage 返回 *ChatMessage 指向 slice 内部 —— 那把封装漏到底：
// 拿着指针的人绕过锁读到的是别人正在追加的那块内存，还能改写它。
func (cm *ConversationMemory) LastMessage() (ChatMessage, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if len(cm.messages) == 0 {
		return ChatMessage{}, false
	}
	return cm.messages[len(cm.messages)-1], true
}

// GetMessagesByType 按类型获取消息
func (cm *ConversationMemory) GetMessagesByType(msgType MessageType) []ChatMessage {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var filtered []ChatMessage
	for _, msg := range cm.messages {
		if msg.Type == msgType {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// Size 返回消息数量
func (cm *ConversationMemory) Size() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.messages)
}

// ToMessages 将对话记忆转换为 LLM 消息格式，自动跳过 sub-agent 内部消息
func (cm *ConversationMemory) ToMessages() []llm.Message {
	var messages []llm.Message
	for _, m := range cm.Snapshot() {
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

	result.IsAnchored = msg.IsAnchored
	return result
}

// repairToolCallPairsAfterTruncationLocked 在内存溢出截断后修复 tool_call/tool_response 配对，
// 确保不会出现孤立的消息。调用者必须持有写锁。
func (cm *ConversationMemory) repairToolCallPairsAfterTruncationLocked() {
	// 构建一个 tool_call_id → 是否有对应 assistant 的映射
	toolCallIDsWithAssistant := make(map[string]bool)
	// 构建一个 tool_call_id → 是否有对应 tool 响应的映射
	toolCallIDsWithToolResponse := make(map[string]bool)

	// 第一遍：收集所有 assistant 消息中的 tool_calls
	for _, m := range cm.messages {
		if m.Type == MessageTypeAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				toolCallIDsWithAssistant[tc.ID] = true
			}
		}
	}

	// 第二遍：收集所有 tool 消息中的 tool_call_id
	for _, m := range cm.messages {
		if m.Type == MessageTypeTool && m.ToolCallID != nil {
			toolCallIDsWithToolResponse[*m.ToolCallID] = true
		}
	}

	// 找出需要删除的消息索引（从后往前遍历，避免索引偏移）
	removeIndices := make(map[int]bool)
	for i, m := range cm.messages {
		if m.Type == MessageTypeAssistant && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if !toolCallIDsWithToolResponse[tc.ID] {
					// 这个 assistant 有 tool_calls 但没有对应的 tool 响应
					// 降级此消息：移除 ToolCalls
					cm.messages[i].ToolCalls = nil
					if cm.messages[i].Content == "" {
						cm.messages[i].Content = "[系统提示：工具调用结果因内存限制被截断]"
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
		filtered := make([]ChatMessage, 0, len(cm.messages)-len(removeIndices))
		for i, m := range cm.messages {
			if !removeIndices[i] {
				filtered = append(filtered, m)
			}
		}
		cm.messages = filtered
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
	for _, msg := range cm.Snapshot() {
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
