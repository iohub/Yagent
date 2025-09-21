package assistant

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/rs/zerolog/log"
)

// Logger 负责日志记录和监控
type Logger struct {
	assistant *CodingAssistant
}

// NewLogger 创建新的日志记录器
func NewLogger(assistant *CodingAssistant) *Logger {
	return &Logger{
		assistant: assistant,
	}
}

// LogLLMInput 记录LLM输入
func (l *Logger) LogLLMInput(taskID string, messages []llms.MessageContent, tools []llms.Tool) error {
	timestamp := time.Now().Format("20060102_150405.000")
	
	// 创建任务特定的日志目录
	homeDir, herr := os.UserHomeDir()
	if herr != nil {
		log.Error().Err(herr).Str("task_id", taskID).Msg("Failed to get user home directory")
		return herr
	}
	taskLogDir := filepath.Join(homeDir, ".codeactor", "logs", taskID)
	if err := os.MkdirAll(taskLogDir, 0755); err != nil {
		log.Error().Err(err).Str("task_id", taskID).Msg("Failed to create task log directory")
		return err
	}
	
	inputLogPath := fmt.Sprintf("%s/llm_input_%s.log", taskLogDir, timestamp)
	inputData := map[string]interface{}{
		"task_id":   taskID,
		"timestamp": timestamp,
		"messages":  messages,
		"tools":     tools,
	}
	
	if inputBytes, err := json.MarshalIndent(inputData, "", "  "); err == nil {
		return os.WriteFile(inputLogPath, inputBytes, 0644)
	}
	
	return nil
}

// LogLLMOutput 记录LLM输出
func (l *Logger) LogLLMOutput(taskID string, response *llms.ContentResponse) error {
	timestamp := time.Now().Format("20060102_150405.000")
	
	// 创建任务特定的日志目录
	homeDir, herr := os.UserHomeDir()
	if herr != nil {
		log.Error().Err(herr).Str("task_id", taskID).Msg("Failed to get user home directory")
		return herr
	}
	taskLogDir := filepath.Join(homeDir, ".codeactor", "logs", taskID)
	if err := os.MkdirAll(taskLogDir, 0755); err != nil {
		log.Error().Err(err).Str("task_id", taskID).Msg("Failed to create task log directory")
		return err
	}
	
	outputLogPath := fmt.Sprintf("%s/llm_output_%s.log", taskLogDir, timestamp)
	outputData := map[string]interface{}{
		"task_id":   taskID,
		"timestamp": timestamp,
		"response":  response,
	}
	
	if outputBytes, err := json.MarshalIndent(outputData, "", "  "); err == nil {
		return os.WriteFile(outputLogPath, outputBytes, 0644)
	}
	
	return nil
}

// LogMemoryState 记录对话记忆状态
func (l *Logger) LogMemoryState(taskID string, memory *ConversationMemory) error {
	if taskID == "" {
		return nil
	}
	
	// 创建任务特定的日志目录
	homeDir, herr := os.UserHomeDir()
	if herr != nil {
		log.Error().Err(herr).Str("task_id", taskID).Msg("Failed to get user home directory")
		return herr
	}
	taskLogDir := filepath.Join(homeDir, ".codeactor", "logs", taskID)
	if err := os.MkdirAll(taskLogDir, 0755); err != nil {
		log.Error().Err(err).Str("task_id", taskID).Msg("Failed to create task log directory")
		return err
	}
	
	memoryPath := fmt.Sprintf("%s/memory.json", taskLogDir)
	if memBytes, err := json.MarshalIndent(memory, "", "  "); err == nil {
		return os.WriteFile(memoryPath, memBytes, 0644)
	}
	
	return nil
}

// LogToolExecution 记录工具执行
func (l *Logger) LogToolExecution(toolName, toolCallID string, success bool, err error) {
	if success {
		log.Info().
			Str("tool_name", toolName).
			Str("tool_call_id", toolCallID).
			Msg("Function call execution completed")
	} else {
		log.Error().
			Str("tool_name", toolName).
			Str("tool_call_id", toolCallID).
			Err(err).
			Msg("Failed to execute function call")
	}
}

// LogSubAgentExecution 记录子代理执行
func (l *Logger) LogSubAgentExecution(taskID, systemPrompt, initialMessage string) {
	log.Info().
		Str("task_id", taskID).
		Str("system_prompt", systemPrompt[:50]+"...").
		Str("initial_message", initialMessage).
		Msg("Starting sub-agent execution")
}