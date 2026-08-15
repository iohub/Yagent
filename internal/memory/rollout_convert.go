package memory

import (
	"encoding/json"
	"fmt"
	"time"

	"codeactor/internal/llm"
)

// LLMMessageToResponseItems 将 llm.Message 转换为 ResponseItem 列表
func LLMMessageToResponseItems(msg llm.Message, msgID string) []ResponseItem {
	switch msg.Role {
	case llm.RoleSystem, llm.RoleUser:
		return []ResponseItem{{
			Type:   "message",
			Role:   string(msg.Role),
			ID:     msgID,
			Content: []MessageContentItem{
				{Type: "input_text", Text: msg.Content},
			},
		}}
	case llm.RoleAssistant:
		if len(msg.ToolCalls) == 0 {
			return []ResponseItem{{
				Type:    "message",
				Role:    "assistant",
				ID:      msgID,
				Content: []MessageContentItem{{Type: "output_text", Text: msg.Content}},
			}}
		}
		// assistant 有 ToolCalls
		var items []ResponseItem
		if msg.Content != "" {
			items = append(items, ResponseItem{
				Type:    "message",
				Role:    "assistant",
				ID:      msgID,
				Content: []MessageContentItem{{Type: "output_text", Text: msg.Content}},
			})
		}
		for i, tc := range msg.ToolCalls {
			items = append(items, ResponseItem{
				Type:      "function_call",
				ID:        fmt.Sprintf("%s_fc_%d", msgID, i),
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Namespace: "default",
				Arguments: tc.Function.Arguments,
			})
		}
		return items
	case llm.RoleTool:
		outputObj := map[string]interface{}{
			"output": msg.Content,
			"metadata": map[string]interface{}{
				"exit_code":        0,
				"duration_seconds": 0,
			},
		}
		outputBytes, err := json.Marshal(outputObj)
		if err != nil {
			outputBytes = []byte(`{"output":"","metadata":{"exit_code":0,"duration_seconds":0}}`)
		}
		callID := msg.ToolCallID
		if callID == "" {
			callID = "unknown"
		}
		return []ResponseItem{{
			Type:   "function_call_output",
			CallID: callID,
			Output: string(outputBytes),
		}}
	default:
		// 未知角色，当作普通消息处理
		return []ResponseItem{{
			Type:    "message",
			Role:    string(msg.Role),
			ID:      msgID,
			Content: []MessageContentItem{{Type: "input_text", Text: msg.Content}},
		}}
	}
}

// ChatMessageToResponseItems 将 ChatMessage 转换为 ResponseItem 列表
func ChatMessageToResponseItems(msg ChatMessage, msgID string) []ResponseItem {
	switch msg.Type {
	case MessageTypeSystem:
		return []ResponseItem{{
			Type:    "message",
			Role:    "system",
			ID:      msgID,
			Content: []MessageContentItem{{Type: "input_text", Text: msg.Content}},
		}}
	case MessageTypeHuman:
		return []ResponseItem{{
			Type:    "message",
			Role:    "user",
			ID:      msgID,
			Content: []MessageContentItem{{Type: "input_text", Text: msg.Content}},
		}}
	case MessageTypeAssistant:
		if len(msg.ToolCalls) == 0 {
			return []ResponseItem{{
				Type:    "message",
				Role:    "assistant",
				ID:      msgID,
				Content: []MessageContentItem{{Type: "output_text", Text: msg.Content}},
			}}
		}
		// assistant 有 ToolCalls
		var items []ResponseItem
		if msg.Content != "" {
			items = append(items, ResponseItem{
				Type:    "message",
				Role:    "assistant",
				ID:      msgID,
				Content: []MessageContentItem{{Type: "output_text", Text: msg.Content}},
			})
		}
		for i, tc := range msg.ToolCalls {
			items = append(items, ResponseItem{
				Type:      "function_call",
				ID:        fmt.Sprintf("%s_fc_%d", msgID, i),
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Namespace: "default",
				Arguments: string(tc.Function.Arguments),
			})
		}
		return items
	case MessageTypeTool:
		if msg.ToolCallID == nil {
			msg.ToolCallID = strPtr("unknown")
		}
		outputObj := map[string]interface{}{
			"output": msg.Content,
			"metadata": map[string]interface{}{
				"exit_code":        0,
				"duration_seconds": 0,
			},
		}
		outputBytes, err := json.Marshal(outputObj)
		if err != nil {
			outputBytes = []byte(`{"output":"","metadata":{"exit_code":0,"duration_seconds":0}}`)
		}
		return []ResponseItem{{
			Type:   "function_call_output",
			CallID: *msg.ToolCallID,
			Output: string(outputBytes),
		}}
	default:
		// 未知类型，当作普通消息处理
		return []ResponseItem{{
			Type:    "message",
			Role:    string(msg.Type),
			ID:      msgID,
			Content: []MessageContentItem{{Type: "input_text", Text: msg.Content}},
		}}
	}
}

// StepInfoToEventMsg 构造 sub_agent_activity 事件
func StepInfoToEventMsg(stepNumber int, toolName string, toolInput interface{}, success bool) EventMsg {
	return EventMsg{
		Type:       "sub_agent_activity",
		StepNumber: stepNumber,
		ToolName:   toolName,
		ToolInput:  toolInput,
		Success:    success,
	}
}

// strPtr 返回字符串指针
func strPtr(s string) *string {
	return &s
}

// nowISO8601Nano 返回当前 UTC 时间的 ISO 8601 格式（含纳秒），用于兼容性
func nowISO8601Nano() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
