package agents

import (
	"testing"

	"codeactor/internal/memory"
	"github.com/tmc/langchaingo/llms"
)

func TestConvertMemoryMessageToLLMSMessage_ToolMessage(t *testing.T) {
	toolCallID := "call_123"
	content := "Tool execution result"
	
	msg := memory.ChatMessage{
		Type:       memory.MessageTypeTool,
		Content:    content,
		ToolCallID: &toolCallID,
	}

	llmMsg := convertMemoryMessageToLLMSMessage(msg)

	if len(llmMsg.Parts) != 1 {
		t.Errorf("Expected 1 part, got %d", len(llmMsg.Parts))
	}

	part := llmMsg.Parts[0]
	_, ok := part.(llms.ToolCallResponse)
	if !ok {
		t.Errorf("Expected part to be ToolCallResponse, got %T", part)
	}
}
