package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/http"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"
)

func init() {
	// Initialize structured logger with text handler
	opts := &slog.HandlerOptions{
		Level: slog.LevelError,
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

	// Start codebase server
	startCodebaseServer()

	switch mode {
	case "tui":
		// Initialize assistant infrastructure before starting TUI
		ctx := context.Background()

		configPath := getConfigPath()
		slog.Info("Loading configuration", "config_path", configPath)
		config, err := assistant.LoadConfig(configPath)
		if err != nil {
			slog.Error("Failed to load configuration", "error", util.WrapError(ctx, err, "main::LoadConfig"))
			os.Exit(1)
		}

		client, err := assistant.NewClient(config)
		if err != nil {
			slog.Error("Failed to create client", "error", util.WrapError(ctx, err, "main::NewClient"))
			os.Exit(1)
		}

		codingAssistant, err := assistant.NewCodingAssistant(client)
		if err != nil {
			slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodingAssistant"))
			os.Exit(1)
		}

		taskManager := http.NewTaskManager(nil)

		dataManager, err := assistant.NewDataManager()
		if err != nil {
			slog.Error("Failed to initialize DataManager", "error", err)
		}

		// Start TUI — all interaction is handled inside the TUI loop
		startTUI(taskFilePath, codingAssistant, taskManager, dataManager)
		return
	case "http":
		// Run HTTP server mode
		// Setup slog for console logging and file logging
		ctx := context.Background()
		// homeDir, herr := os.UserHomeDir()
		// if herr != nil {
		// 	slog.Error("Failed to get user home directory", "error", util.WrapError(ctx, herr, "main::UserHomeDir"))
		// 	os.Exit(1)
		// }
		// logDir := filepath.Join(homeDir, ".codeactor", "logs")

		// Use local directory for logs to avoid permission issues
		logDir := "logs"
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

	panic("config.toml not found")
}

// startCodebaseServer starts the codeactor-codebase server as a background process
func startCodebaseServer() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Error("Failed to get user home directory", "error", err)
		return
	}

	binPath := filepath.Join(homeDir, ".codeactor/bin/codeactor-codebase")
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		slog.Warn("codeactor-codebase binary not found, skipping startup", "path", binPath)
		return
	}

	logDir := filepath.Join(homeDir, ".codeactor/logs/codeactor-codebase")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Error("Failed to create log directory", "error", err)
		return
	}

	now := time.Now()
	// logFileName := fmt.Sprintf("%s-%s.log", now.Format("2006-01-02"), now.Format("1504"))
	logFileName := fmt.Sprintf("%s.log", now.Format("2006-01-02"))
	logPath := filepath.Join(logDir, logFileName)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Error("Failed to create log file", "error", err)
		return
	}

	cmd := exec.Command(binPath, "-v", "server")
	cmd.Env = append(os.Environ(), "RUST_LOG=info")
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		slog.Error("Failed to start codeactor-codebase", "error", err)
		return
	}

	slog.Info("Started codeactor-codebase server", "pid", cmd.Process.Pid, "log", logPath)
}
