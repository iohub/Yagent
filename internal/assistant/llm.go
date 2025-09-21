package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codee/internal/config"
	"codee/internal/util"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/bedrock"
	"github.com/tmc/langchaingo/llms/openai"
)

// llmLogger is a separate logger for LLM responses
var llmLogger zerolog.Logger

// initLLMLogger initializes the LLM logger
func initLLMLogger() error {
	// Create logs directory if it doesn't exist
	homeDir, herr := os.UserHomeDir()
	if herr != nil {
		return util.WrapError(context.Background(), herr, "failed to get user home directory")
	}
	logDir := filepath.Join(homeDir, ".codee", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory")
	}

	// Open LLM log file
	llmLogFile, err := os.OpenFile(filepath.Join(logDir, "llm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to open LLM log file")
	}

	// Create LLM logger with plain text format for better debugging
	llmLogger = zerolog.New(llmLogFile).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        llmLogFile,
		TimeFormat: time.RFC3339,
		NoColor:    true,
		FormatLevel: func(i interface{}) string {
			if ll, ok := i.(string); ok {
				return ll
			}
			return "INFO"
		},
		FormatMessage: func(i interface{}) string {
			if i == nil {
				return ""
			}
			return fmt.Sprintf("| %s", i)
		},
		FormatFieldName: func(i interface{}) string {
			return fmt.Sprintf("%s=", i)
		},
		FormatFieldValue: func(i interface{}) string {
			return fmt.Sprintf("%s", i)
		},
	})
	return nil
}

// Client represents an LLM client
type Client struct {
	llm       llms.LLM
	config    *config.Config
	assistant *CodingAssistant // 对主助手的引用，用于日志记录
}

// LoadConfig loads configuration from a TOML file using the new multi-provider structure
func LoadConfig(configPath string) (*config.Config, error) {
	log.Debug().Str("config_path", configPath).Msg("Decoding TOML configuration file")

	config, err := config.LoadConfig(configPath)
	if err != nil {
		log.Error().
			Err(err).
			Str("config_path", configPath).
			Msg("Failed to decode TOML configuration file")
		return nil, err
	}

	// Get active provider for logging
	activeProvider, err := config.GetActiveProvider()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get active provider configuration")
		return nil, err
	}

	log.Debug().
		Str("provider", config.LLM.UseProvider).
		Str("model", activeProvider.Model).
		Str("api_base_url", activeProvider.APIBaseURL).
		Float64("temperature", activeProvider.Temperature).
		Int("max_tokens", activeProvider.MaxTokens).
		Bool("streaming_enabled", config.App.EnableStreaming).
		Msg("Configuration loaded successfully")

	return config, nil
}

// NewClient creates a new LLM client from config
func NewClient(config *config.Config) (*Client, error) {
	ctx := context.Background()

	// Initialize LLM logger
	if err := initLLMLogger(); err != nil {
		log.Error().Err(err).Msg("Failed to initialize LLM logger")
		return nil, util.WrapError(ctx, err, "failed to initialize LLM logger")
	}

	// Get active provider configuration
	activeProvider, err := config.GetActiveProvider()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get active provider configuration")
		return nil, util.WrapError(ctx, err, "failed to get active provider")
	}

	log.Info().
		Str("provider", config.LLM.UseProvider).
		Str("model", activeProvider.Model).
		Str("api_base_url", activeProvider.APIBaseURL).
		Msg("Creating new LLM client")

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
			log.Error().
				Err(err).
				Str("provider", config.LLM.UseProvider).
				Str("model", activeProvider.Model).
				Str("model_provider", modelProvider).
				Msg("Failed to create Bedrock LLM client")
			return nil, util.WrapError(ctx, err, "failed to create Bedrock client")
		}
	} else {
		// For non-Bedrock providers, require API key
		if activeProvider.APIKey == "" {
			log.Error().Msg("Cannot create LLM client: API key is empty")
			return nil, util.WrapError(ctx, fmt.Errorf("API key not found in config"), "API key validation failed")
		}

		// Create client using OpenAI's client but with specified API endpoint
		llm, err = openai.New(
			openai.WithModel(activeProvider.Model),
			openai.WithBaseURL(activeProvider.APIBaseURL),
			openai.WithToken(activeProvider.APIKey),
		)
		if err != nil {
			log.Error().
				Err(err).
				Str("provider", config.LLM.UseProvider).
				Str("model", activeProvider.Model).
				Str("api_base_url", activeProvider.APIBaseURL).
				Msg("Failed to create LLM client")
			return nil, util.WrapError(ctx, err, "failed to create OpenAI client")
		}
	}

	log.Info().Msg("LLM client created successfully")

	return &Client{
		llm:    llm,
		config: config,
	}, nil
}

// streamDebugHandler prints each stream output text to stdout and logs to LLM log file
func StreamDebugHandler(ctx context.Context, chunk []byte) error {
	if len(chunk) > 0 {
		fmt.Print(string(chunk))
		os.Stdout.Sync()
		llmLogger.Info().Str("type", "stream_chunk").Str("content", string(chunk))
	}
	return nil
}

// GenerateCompletionWithMemory generates a completion using the provided memory (conversation history)
func (c *Client) GenerateCompletionWithMemory(ctx context.Context, memory []llms.MessageContent, streamHandler func(context.Context, []byte) error) (string, error) {
	log.Debug().
		Int("memory_length", len(memory)).
		Bool("streaming_enabled", c.config.App.EnableStreaming).
		Msg("Starting completion generation with memory")

	// Log input memory to LLM log file
	if memoryJSON, err := json.Marshal(memory); err == nil {
		llmLogger.Info().
			Str("type", "input_memory").
			Str("model", c.config.LLM.UseProvider).
			Int("memory_length", len(memory)).
			Str("memory", string(memoryJSON)).
			Msg("LLM input memory")
	}

	// Log LLM input using assistant's logger if available
	if c.assistant != nil && c.assistant.logger != nil {
		c.assistant.logger.LogLLMInput("", memory, nil)
	}

	// Get active provider configuration
	activeProvider, err := c.config.GetActiveProvider()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get active provider configuration")
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
		log.Debug().Msg("Streaming enabled for this request (memory)")
	}

	// Generate completion
	completion, err := c.llm.GenerateContent(ctx, memory, opts...)
	if err != nil {
		// 尝试提取HTTP响应内容
		httpResponse := extractHTTPResponse(err)

		log.Error().
			Err(err).
			Str("model", c.config.LLM.UseProvider).
			Int("memory_length", len(memory)).
			Str("http_response", httpResponse).
			Msg("Failed to GenerateContent")

		// Log error to LLM log file
		llmLogger.Error().
			Str("type", "completion_error").
			Str("model", c.config.LLM.UseProvider).
			Int("memory_length", len(memory)).
			Str("error", err.Error()).
			Str("http_response", httpResponse).
			Msg("LLM completion error")

		return "", util.WrapError(ctx, err, "error generating completion (memory)")
	}

	// Return result
	if len(completion.Choices) > 0 {
		result := completion.Choices[0].Content
		log.Info().
			Int("result_length", len(result)).
			Int("choices_count", len(completion.Choices)).
			Msg("Completion generated successfully (memory)")

		// Log the complete response to LLM log file
		if choicesJSON, err := json.Marshal(completion.Choices); err == nil {
			llmLogger.Info().
				Str("type", "completion_output").
				Str("model", c.config.LLM.UseProvider).
				Int("choices_count", len(completion.Choices)).
				Int("memory_length", len(memory)).
				Str("response", result).
				Str("response.Choices", string(choicesJSON)).
				Msg("LLM completion output")
		}

		// Log LLM output using assistant's logger if available
		if c.assistant != nil && c.assistant.logger != nil {
			c.assistant.logger.LogLLMOutput("", completion)
		}

		return result, nil
	}

	log.Warn().
		Int("choices_count", len(completion.Choices)).
		Msg("No completion choices returned from LLM (memory)")

	// Log empty response to LLM log file
	llmLogger.Warn().
		Str("type", "empty_completion_memory").
		Str("model", c.config.LLM.UseProvider).
		Int("choices_count", len(completion.Choices)).
		Int("memory_length", len(memory)).
		Msg("LLM returned empty completion (with memory)")

	return "", nil
}

// GenerateCompletionWithTools generates a completion with function calling support
func (c *Client) GenerateCompletionWithTools(ctx context.Context, messages []llms.MessageContent, tools []llms.Tool, streamHandler func(context.Context, []byte) error) (*llms.ContentResponse, error) {

	// Get active provider configuration
	activeProvider, err := c.config.GetActiveProvider()
	if err != nil {
		log.Error().Err(err).Msg("Failed to get active provider configuration")
		return nil, util.WrapError(ctx, err, "failed to get active provider")
	}

	// Generate options
	opts := []llms.CallOption{
		llms.WithMaxTokens(activeProvider.MaxTokens),
		llms.WithTemperature(activeProvider.Temperature),
	}

	// Add tools if provided
	if len(tools) > 0 {
		opts = append(opts, llms.WithTools(tools))
		log.Debug().Int("tools_count", len(tools)).Msg("Tools added to request")
	}

	// Add streaming if enabled and handler provided
	if c.config.App.EnableStreaming && streamHandler != nil {
		opts = append(opts, llms.WithStreamingFunc(streamHandler))
	}

	// Generate completion
	completion, err := c.llm.GenerateContent(ctx, messages, opts...)
	if err != nil {
		// 尝试提取HTTP响应内容
		httpResponse := extractHTTPResponse(err)

		log.Error().
			Err(err).
			Str("model", c.config.LLM.UseProvider).
			Int("messages_length", len(messages)).
			Int("tools_count", len(tools)).
			Str("http_response", httpResponse).
			Msg("Failed to GenerateCompletionWithTools::GenerateContent")

		return nil, util.WrapError(ctx, err, "GenerateCompletionWithTools::GenerateContent")
	}

	if len(completion.Choices) == 0 {
		return nil, util.WrapError(ctx, fmt.Errorf("no choices returned"), "GenerateCompletionWithTools")
	}

	result := completion.Choices[0].Content
	// Log the complete response to LLM log file
	if choicesJSON, err := json.Marshal(completion.Choices); err == nil {
		llmLogger.Info().
			Str("type", "completion_output").
			Str("model", c.config.LLM.UseProvider).
			Int("choices_count", len(completion.Choices)).
			Int("messages_length", len(messages)).
			Int("tools_count", len(tools)).
			Str("response", result).
			Str("response.Choices", string(choicesJSON)).
			Msg("LLM completion output")
	}

	return completion, nil

}

// extractHTTPResponse 尝试从错误中提取HTTP响应内容
func extractHTTPResponse(err error) string {
	if err == nil {
		return ""
	}

	// 将错误转换为字符串
	errStr := err.Error()

	// 检查其他常见的HTTP错误格式
	if strings.Contains(errStr, "status code") || strings.Contains(errStr, "HTTP") {
		return errStr
	}

	// 如果没有明显的HTTP错误信息，返回原始错误
	return fmt.Sprintf("Error: %s", errStr)
}
