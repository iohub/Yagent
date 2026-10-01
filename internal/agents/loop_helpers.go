// loop_helpers.go — P0 优化 Step 7：删除旧执行内核 RunAgentLoop（executor.go）
// 时，将其中两个仍有调用方的保留函数**原样搬移**至此（搬迁自 executor.go:586-641，
// 不改逻辑/签名）：
//   - ConvertLLMHistoryToMemory：供调用方将 llm.Message 历史转换为
//     memory.ChatMessage 切片；
//   - logToolCall：供 adapterToolRunner（loop.go）记录工具日志。
//
// 依赖的包级函数 InitToolLogger / LogToolCall 仍在 tool_logger.go（agents 包内）。
package agents

import (
	"encoding/json"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
)

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
