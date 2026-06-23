package conductor

import (
	"encoding/json"
	"fmt"
	"time"
)

// MemoryManager 负责 Sub-Agent 执行结果的内存注入和中转。
// 职责：
// 1. 将 Sub-Agent 的完整对话历史注入到 Conductor 的记忆中
// 2. 管理待注入的 Sub-Agent Memory 队列
// 3. 提供工具调用格式转换能力
type MemoryManager struct {
	pendingMemory *SubAgentMemory // 最近一次 delegate 调用的完整结果
}

// NewMemoryManager 创建内存管理器。
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{}
}

// SetPendingMemory 设置待注入的 Sub-Agent 内存。
func (m *MemoryManager) SetPendingMemory(mem *SubAgentMemory) {
	m.pendingMemory = mem
}

// GetPendingMemory 获取并清除待注入的内存。
func (m *MemoryManager) GetPendingMemory() *SubAgentMemory {
	mem := m.pendingMemory
	m.pendingMemory = nil
	return mem
}

// HasPendingMemory 检查是否有待注入的内存。
func (m *MemoryManager) HasPendingMemory() bool {
	return m.pendingMemory != nil
}

// ConvertToolCalls 将 llm.ToolCall 列表转换为内存 ToolCallData 列表。
// 这是从 LLM 响应格式到内存持久化格式的适配转换。
func ConvertToolCalls(tcs []ToolCall) []ToolCallData {
	var res []ToolCallData
	for _, tc := range tcs {
		res = append(res, ToolCallData{
			ID:   tc.ID,
			Type: tc.Type,
			Function: ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			},
		})
	}
	return res
}

// ToolCall 是 LLM 工具调用。
type ToolCall struct {
	ID       string
	Type     string
	Function FunctionCall
}

// FunctionCall 是 LLM 函数调用。
type FunctionCall struct {
	Name      string
	Arguments string
}

// ToolCallData 是内存中存储的工具调用数据。
type ToolCallData struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 是内存中存储的函数调用。
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// BuildSubAgentMemoryResult 从执行结果构建 SubAgentMemory。
func BuildSubAgentMemoryResult(text string, messages []ChatMessage) *SubAgentMemory {
	return &SubAgentMemory{
		Text:   text,
		Memory: messages,
	}
}

// FormatMemoryInjectionContext 格式化内存注入上下文，供 LLM 提示使用。
func FormatMemoryInjectionContext(toolName string, toolCallID string) string {
	groupID := fmt.Sprintf("%s_sub_%d", toolName, time.Now().UnixNano())
	return fmt.Sprintf("group_id=%s parent_id=%s", groupID, toolCallID)
}
