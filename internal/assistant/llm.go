package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/util"

	"log/slog"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/bedrock"
	"github.com/tmc/langchaingo/llms/openai"
)

// llmLogger is a separate logger for LLM responses
var llmLogger *slog.Logger
var llmLogFile *os.File

// initLLMLogger initializes the LLM logger
func initLLMLogger() error {
	// Create logs directory in user home
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to get user home directory")
	}
	logDir := filepath.Join(homeDir, ".codeactor", "logs")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory")
	}

	// Open LLM log file with date
	dateStr := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("llm-%s.log", dateStr)
	var errFile error
	llmLogFile, errFile = os.OpenFile(filepath.Join(logDir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errFile != nil {
		return util.WrapError(context.Background(), errFile, "failed to open LLM log file")
	}

	// Create LLM logger with plain text format for better debugging
	handler := slog.NewTextHandler(llmLogFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	llmLogger = slog.New(handler)
	return nil
}

// LogLLMContent writes a raw string to the LLM log file with a header
func LogLLMContent(title string, content string) {
	if llmLogFile == nil {
		return
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	separator := strings.Repeat("-", 80)
	logEntry := fmt.Sprintf("\n%s\n[%s] %s:\n%s\n%s\n", separator, timestamp, title, content, separator)
	if _, err := llmLogFile.WriteString(logEntry); err != nil {
		slog.Error("Failed to write to LLM log file", "error", err)
	}
}

// LoggingLLM wraps an llms.LLM to add logging
type LoggingLLM struct {
	inner llms.LLM
}

func (l *LoggingLLM) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	resp, err := l.inner.Call(ctx, prompt, options...)
	if err == nil {
		LogLLMContent("LLM Response (Call)", resp)
	} else {
		LogLLMContent("LLM Error (Call)", err.Error())
	}
	return resp, err
}

func (l *LoggingLLM) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	resp, err := l.inner.GenerateContent(ctx, messages, options...)
	if err == nil && len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		logContent := choice.Content

		if len(choice.ToolCalls) > 0 {
			var toolCallsLog strings.Builder
			if logContent != "" {
				toolCallsLog.WriteString("\n")
			}
			toolCallsLog.WriteString("[Tool Calls]:\n")
			for i, tc := range choice.ToolCalls {
				toolCallsLog.WriteString(fmt.Sprintf("%d. %s(%s)\n", i+1, tc.FunctionCall.Name, tc.FunctionCall.Arguments))
			}
			logContent += toolCallsLog.String()
		}

		LogLLMContent("LLM Response (GenerateContent)", logContent)
	} else if err != nil {
		LogLLMContent("LLM Error (GenerateContent)", err.Error())
	}
	return resp, err
}

// Client represents an LLM client
type Client struct {
	llm       llms.LLM
	config    *config.Config
	assistant *CodingAssistant // 对主助手的引用，用于日志记录
}

// LoadConfig loads configuration from a TOML file using the new multi-provider structure
func LoadConfig(configPath string) (*config.Config, error) {
	slog.Debug("Decoding TOML configuration file", "config_path", configPath)

	config, err := config.LoadConfig(configPath)
	if err != nil {
		slog.Error("Failed to decode TOML configuration file",
			"error", err,
			"config_path", configPath)
		return nil, err
	}

	// Get active provider for logging
	activeProvider, err := config.GetActiveProvider()
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return nil, err
	}

	slog.Debug("Configuration loaded successfully",
		"provider", config.LLM.UseProvider,
		"model", activeProvider.Model,
		"api_base_url", activeProvider.APIBaseURL,
		"temperature", activeProvider.Temperature,
		"max_tokens", activeProvider.MaxTokens,
		"streaming_enabled", config.App.EnableStreaming)

	return config, nil
}

// NewClient creates a new LLM client from config
func NewClient(config *config.Config) (*Client, error) {
	ctx := context.Background()

	// Initialize LLM logger
	if err := initLLMLogger(); err != nil {
		slog.Error("Failed to initialize LLM logger", "error", err)
		return nil, util.WrapError(ctx, err, "failed to initialize LLM logger")
	}

	// Get active provider configuration
	activeProvider, err := config.GetActiveProvider()
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return nil, util.WrapError(ctx, err, "failed to get active provider")
	}

	slog.Info("Creating new LLM client",
		"provider", config.LLM.UseProvider,
		"model", activeProvider.Model,
		"api_base_url", activeProvider.APIBaseURL)

	var llm llms.LLM

	// Handle Bedrock provider separately
	if config.LLM.UseProvider == "bedrock" {
		// Detect provider from model ID for Bedrock
		modelProvider := activeProvider.ModelProvider

		// Create Bedrock client
		bedrockOpts := []bedrock.Option{
			bedrock.WithModel(activeProvider.Model),
		}
		if modelProvider != "" {
			bedrockOpts = append(bedrockOpts, bedrock.WithModelProvider(modelProvider))
		}

		llm, err = bedrock.New(bedrockOpts...)
		if err != nil {
			slog.Error("Failed to create Bedrock LLM client",
				"error", err,
				"provider", config.LLM.UseProvider,
				"model", activeProvider.Model,
				"model_provider", modelProvider)
			return nil, util.WrapError(ctx, err, "failed to create Bedrock client")
		}
	} else {
		// For non-Bedrock providers, require API key
		if activeProvider.APIKey == "" {
			slog.Error("Cannot create LLM client: API key is empty")
			return nil, util.WrapError(ctx, fmt.Errorf("API key not found in config"), "API key validation failed")
		}

		// Create client using OpenAI's client but with specified API endpoint
		llm, err = openai.New(
			openai.WithModel(activeProvider.Model),
			openai.WithBaseURL(activeProvider.APIBaseURL),
			openai.WithToken(activeProvider.APIKey),
		)
		if err != nil {
			slog.Error("Failed to create LLM client",
				"error", err,
				"provider", config.LLM.UseProvider,
				"model", activeProvider.Model,
				"api_base_url", activeProvider.APIBaseURL)
			return nil, util.WrapError(ctx, err, "failed to create OpenAI client")
		}
	}

	slog.Info("LLM client created successfully")

	// Wrap with LoggingLLM
	loggingLLM := &LoggingLLM{inner: llm}

	return &Client{
		llm:    loggingLLM,
		config: config,
	}, nil
}

// StreamDebugHandler prints each stream output text to stdout and logs to LLM log file
func StreamDebugHandler(ctx context.Context, chunk []byte) error {
	if len(chunk) > 0 {
		fmt.Print(string(chunk))
		os.Stdout.Sync()
		llmLogger.Info("Stream chunk", "type", "stream_chunk", "content", string(chunk))
	}
	return nil
}

// GenerateCompletionWithMemory generates a completion using the provided memory (conversation history)
func (c *Client) GenerateCompletionWithMemory(ctx context.Context, memory []llms.MessageContent, streamHandler func(context.Context, []byte) error) (string, error) {
	slog.Debug("Starting completion generation with memory",
		"memory_length", len(memory),
		"streaming_enabled", c.config.App.EnableStreaming)

	// Log input memory to LLM log file
	if memoryJSON, err := json.Marshal(memory); err == nil {
		llmLogger.Info("LLM input memory",
			"type", "input_memory",
			"model", c.config.LLM.UseProvider,
			"memory_length", len(memory),
			"memory", string(memoryJSON))
	}

	// Log LLM input using assistant's logger if available
	/*
		if c.assistant != nil && c.assistant.logger != nil {
			c.assistant.logger.LogLLMInput("", memory, nil)
		}
	*/

	// Get active provider configuration
	activeProvider, err := c.config.GetActiveProvider()
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return "", util.WrapError(ctx, err, "failed to get active provider")
	}

	// Generate options
	opts := []llms.CallOption{
		llms.WithMaxTokens(activeProvider.MaxTokens),
		llms.WithTemperature(activeProvider.Temperature),
	}

	// Add streaming if enabled and handler provided
	if c.config.App.EnableStreaming && streamHandler != nil {
		opts = append(opts, llms.WithStreamingFunc(streamHandler))
		slog.Debug("Streaming enabled for this request (memory)")
	}

	// Generate completion
	completion, err := c.llm.GenerateContent(ctx, memory, opts...)
	if err != nil {
		// 尝试提取HTTP响应内容
		httpResponse := extractHTTPResponse(err)

		slog.Error("Failed to GenerateContent",
			"error", err,
			"model", c.config.LLM.UseProvider,
			"memory_length", len(memory),
			"http_response", httpResponse)

		// Log error to LLM log file
		llmLogger.Error("LLM completion error",
			"type", "completion_error",
			"model", c.config.LLM.UseProvider,
			"memory_length", len(memory),
			"error", err.Error(),
			"http_response", httpResponse)

		return "", util.WrapError(ctx, err, "error generating completion (memory)")
	}

	// Return result
	if len(completion.Choices) > 0 {
		result := completion.Choices[0].Content
		slog.Info("Completion generated successfully (memory)",
			"result_length", len(result),
			"choices_count", len(completion.Choices))

		// Log the complete response to LLM log file
		if choicesJSON, err := json.Marshal(completion.Choices); err == nil {
			llmLogger.Info("LLM completion output",
				"type", "completion_output",
				"model", c.config.LLM.UseProvider,
				"choices_count", len(completion.Choices),
				"memory_length", len(memory),
				"response", result,
				"response.Choices", string(choicesJSON))
		}

		// Log LLM output using assistant's logger if available
		/*
			if c.assistant != nil && c.assistant.logger != nil {
				c.assistant.logger.LogLLMOutput("", completion)
			}
		*/

		return result, nil
	}

	slog.Warn("No completion choices returned from LLM (memory)",
		"choices_count", len(completion.Choices))

	// Log empty response to LLM log file
	llmLogger.Warn("LLM returned empty completion (with memory)",
		"type", "empty_completion_memory",
		"model", c.config.LLM.UseProvider,
		"choices_count", len(completion.Choices),
		"memory_length", len(memory))

	return "", nil
}

// extractHTTPResponse 尝试从错误中提取HTTP响应内容
func extractHTTPResponse(err error) string {
	if err == nil {
		return ""
	}

	// 将错误转换为字符串
	errStr := err.Error()

	// 检查AWS Bedrock特定的错误
	if strings.Contains(errStr, "ValidationException") {
		return fmt.Sprintf("Bedrock Validation Error: %s", errStr)
	}

	if strings.Contains(errStr, "ThrottlingException") {
		return fmt.Sprintf("Bedrock Throttling Error: %s", errStr)
	}

	if strings.Contains(errStr, "AccessDeniedException") {
		return fmt.Sprintf("Bedrock Access Denied: %s", errStr)
	}

	if strings.Contains(errStr, "ModelNotReadyException") {
		return fmt.Sprintf("Bedrock Model Not Ready: %s", errStr)
	}

	// 检查其他常见的HTTP错误格式
	if strings.Contains(errStr, "status code") || strings.Contains(errStr, "HTTP") {
		return errStr
	}

	// 如果没有明显的HTTP错误信息，返回原始错误
	return fmt.Sprintf("Error: %s", errStr)
}
