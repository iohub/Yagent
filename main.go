package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"

	"github.com/google/uuid"
)

func init() {
	// Initialize structured logger with text handler
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)

	// Initialize language manager with default language (English)
	if langManager == nil {
		langManager = NewLanguageManager()
	}
}

func main() {
	// Check if running in TUI mode or HTTP server mode based on command line arguments
	if len(os.Args) < 2 {
		fmt.Println("Usage: codeactor [tui|http]")
		os.Exit(1)
	}

	mode := os.Args[1]
	// 解析 --taskfile 参数
	var taskFilePath string
	for i := 2; i < len(os.Args); i++ {
		if os.Args[i] == "--taskfile" && i+1 < len(os.Args) {
			taskFilePath = os.Args[i+1]
			break
		} else if strings.HasPrefix(os.Args[i], "--taskfile=") {
			taskFilePath = strings.TrimPrefix(os.Args[i], "--taskfile=")
			break
		}
	}

	switch mode {
	case "tui":
		// Run TUI mode
		projectDir, taskDesc := startTUI(taskFilePath)
		if projectDir != "" && taskDesc != "" {
			// Execute task directly
			ctx := context.Background()
			var err error

			// Load configuration
			configPath := getConfigPath()
			slog.Info("Loading configuration", "config_path", configPath)
			config, err := assistant.LoadConfig(configPath)
			if err != nil {
				slog.Error("Failed to load configuration", "error", util.WrapError(ctx, err, "main::LoadConfig"))
				os.Exit(1)
			}

			// Create client
			client, err := assistant.NewClient(config)
			if err != nil {
				slog.Error("Failed to create client", "error", util.WrapError(ctx, err, "main::NewClient"))
				os.Exit(1)
			}

			// Create coding assistant
			codingAssistant, err := assistant.NewCodingAssistant(client)
			if err != nil {
				slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodingAssistant"))
				os.Exit(1)
			}

			// Create task manager
			taskManager := http.NewTaskManager(nil)

			// Create task
			taskCtx, cancel := context.WithCancel(ctx)
			task := &http.Task{
				ID:         uuid.New().String(),
				Status:     http.TaskStatusRunning,
				ProjectDir: projectDir,
				CreatedAt:  time.Now(),
				UpdatedAt:  time.Now(),
				Memory:     memory.NewConversationMemory(300),
				Context:    taskCtx,
				CancelFunc: cancel,
			}

			// Add task to manager
			taskManager.AddTask(task)

			// Execute task
			slog.Info("TUI coding task submitted", "project_dir", projectDir, "task_desc", taskDesc)
			http.ExecuteTask(task.ID, projectDir, taskDesc, taskManager, codingAssistant)

			// Wait for task completion and display result
			for {
				time.Sleep(1 * time.Second)
				currentTask, ok := taskManager.GetTask(task.ID)
				if ok && (currentTask.Status == http.TaskStatusFinished || currentTask.Status == http.TaskStatusFailed) {
					break
				}
			}

			// Display result
			finalTask, _ := taskManager.GetTask(task.ID)
			if finalTask.Status == http.TaskStatusFinished {
				fmt.Printf("\n\nTask completed successfully!\nResult: %s\n", finalTask.Result)
			} else {
				fmt.Printf("\n\nTask failed!\nError: %s\n", finalTask.Error)
			}
			return
		}
		return
	case "http":
		// Run HTTP server mode
		// Setup slog for console logging and file logging
		ctx := context.Background()
		homeDir, herr := os.UserHomeDir()
		if herr != nil {
			slog.Error("Failed to get user home directory", "error", util.WrapError(ctx, herr, "main::UserHomeDir"))
			os.Exit(1)
		}
		logDir := filepath.Join(homeDir, ".codeactor", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			slog.Error("Failed to create logs directory", "error", util.WrapError(ctx, err, "main::MkdirAll"))
			os.Exit(1)
		}

		logFile, err := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			slog.Error("Failed to open log file", "error", util.WrapError(ctx, err, "main::OpenFile"))
			os.Exit(1)
		}

		// Configure slog to write to both console and file
		// Note: We use io.MultiWriter to write to both
		multiWriter := io.MultiWriter(os.Stderr, logFile)
		logger := slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
		slog.SetDefault(logger)

		// 加载配置和初始化 assistant.client
		configPath := getConfigPath()
		slog.Info("Loading configuration", "config_path", configPath)
		config, err := assistant.LoadConfig(configPath)
		if err != nil {
			slog.Error("Failed to load configuration", "error", util.WrapError(ctx, err, "main::LoadConfig"))
			os.Exit(1)
		}
		slog.Info("Creating assistant.client")
		client, err := assistant.NewClient(config)
		if err != nil {
			slog.Error("Failed to create assistant.client", "error", util.WrapError(ctx, err, "main::NewClient"))
			os.Exit(1)
		}

		// 创建 AI Coding Assistant
		codingAssistant, err := assistant.NewCodingAssistant(client)
		if err != nil {
			slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodingAssistant"))
			os.Exit(1)
		}

		// 创建消息分发器并集成消息系统
		messageDispatcher := messaging.NewMessageDispatcher(100)
		codingAssistant.IntegrateMessaging(messageDispatcher)

		// 创建HTTP服务器
		server := http.NewServer(codingAssistant)

		// 从配置中读取HTTP服务端口
		serverPort := config.HTTP.ServerPort
		if serverPort == 0 {
			serverPort = 10080 // 默认端口
		}

		// 启动服务器
		if err := server.Run(serverPort); err != nil {
			slog.Error("Failed to start HTTP server", "error", util.WrapError(ctx, err, "main::ServerRun"))
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown mode: %s\n", mode)
		fmt.Println("Usage: codeactor [tui|http]")
		os.Exit(1)
	}
}

// getConfigPath 返回配置文件的路径，优先使用 $HOME/.codeactor/config/config.toml
func getConfigPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// 如果无法获取用户主目录，回退到本地 config/config.toml
		return "config/config.toml"
	}

	configDir := filepath.Join(homeDir, ".codeactor", "config")
	configPath := filepath.Join(configDir, "config.toml")

	// 检查配置文件是否存在
	if _, err := os.Stat(configPath); err == nil {
		return configPath
	}

	// 如果用户目录下的配置文件不存在，检查并创建目录
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		if err := os.MkdirAll(configDir, 0755); err != nil {
			// 如果创建目录失败，回退到本地配置
			return "config/config.toml"
		}

		// 如果目录创建成功但配置文件不存在，创建默认配置文件
		defaultConfig := `# LLM Configuration
[http]
server_port = 9080

[llm]
# 选择当前使用的提供商
use_provider = "aliyun"

# 阿里云配置
[llm.providers.aliyun]
model = "qwen3-max-preview"
temperature = 0.0
max_tokens = 28000
api_base_url = "https://dashscope.aliyuncs.com/compatible-mode/v1"
api_key = "your-aliyun-api-key"

# SiliconFlow配置
[llm.providers.siliconflow]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-siliconflow-api-key"

# OpenRouter配置
[llm.providers.openrouter]
model = "qwen3-coder-plus"
temperature = 0.0
max_tokens = 3000
api_base_url = "https://openrouter.ai/api/v1"
api_key = "your-openrouter-api-key"

[app]
enable_streaming = true
`

		if err := os.WriteFile(configPath, []byte(defaultConfig), 0644); err != nil {
			// 如果创建默认配置失败，回退到本地配置
			return "config/config.toml"
		}
	}

	return configPath
}
