package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	nethttp "net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/embedbin"
	"codeactor/internal/http"
	"codeactor/internal/llm"
	"codeactor/internal/util"
	messaging "codeactor/pkg/messaging"
)

func init() {
	// Initialize structured logger with text handler
	// Use LevelWarn so that warnings (e.g. codebase startup failures) are visible.
	opts := &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)

	// Initialize language manager with default language (English)
	if langManager == nil {
		langManager = NewLanguageManager()
	}
}

func main() {
	defer util.RecoverPanic()

	// Check if running in TUI mode or HTTP server mode based on command line arguments
	if len(os.Args) < 2 {
		fmt.Println("Usage: codeactor [tui|http] [--disable-agents=repo,coding,chat,meta,devops] [--taskfile TASK.md] [--port=9800]")
		os.Exit(1)
	}

	mode := os.Args[1]
	// 解析 --taskfile 参数
	var taskFilePath string
	// 解析 --disable-agents 参数
	var disableAgents string
	// 解析 --port 参数
	httpPort := 9800
	for i := 2; i < len(os.Args); i++ {
		arg := os.Args[i]
		if arg == "--taskfile" && i+1 < len(os.Args) {
			taskFilePath = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--taskfile=") {
			taskFilePath = strings.TrimPrefix(arg, "--taskfile=")
		} else if arg == "--disable-agents" && i+1 < len(os.Args) {
			disableAgents = os.Args[i+1]
			i++
		} else if strings.HasPrefix(arg, "--disable-agents=") {
			disableAgents = strings.TrimPrefix(arg, "--disable-agents=")
		} else if arg == "--port" && i+1 < len(os.Args) {
			port, err := strconv.Atoi(os.Args[i+1])
			if err != nil {
				fmt.Printf("Invalid port: %s\n", os.Args[i+1])
				os.Exit(1)
			}
			httpPort = port
			i++
		} else if strings.HasPrefix(arg, "--port=") {
			port, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				fmt.Printf("Invalid port: %s\n", strings.TrimPrefix(arg, "--port="))
				os.Exit(1)
			}
			httpPort = port
		}
	}

	// 获取当前工作目录作为仓库路径
	repoPath, err := os.Getwd()
	if err != nil {
		fmt.Printf("Failed to get current working directory: %v\n", err)
		os.Exit(1)
	}

	// 从 12800 开始动态查找可用端口
	codebasePort, err := findAvailablePort(12800)
	if err != nil {
		fmt.Printf("Failed to find available port for codebase: %v\n", err)
		os.Exit(1)
	}

	// 提取嵌入的 dist/bin 二进制到 ~/.codeactor/bin/
	if _, err := embedbin.ExtractBinaries(distBinFS, "dist/bin"); err != nil {
		slog.Warn("Failed to extract embedded binaries", "error", err)
	}

	// Start codebase server
	codebaseCmd := startCodebaseServer(codebasePort, repoPath)
	if codebaseCmd != nil {
		defer func() {
			if err := codebaseCmd.Process.Kill(); err != nil {
				slog.Warn("Failed to kill codebase process", "error", err)
			} else {
				slog.Info("Codebase process killed on exit", "pid", codebaseCmd.Process.Pid)
			}
		}()
	} else {
		fmt.Fprintf(os.Stderr, "WARNING: codeactor-codebase server failed to start. Semantic search and code analysis features will be unavailable.\n")
	}

	switch mode {
	case "tui":
		// Initialize assistant infrastructure before starting TUI
		ctx := context.Background()

		configPath := getConfigPath()
		slog.Info("Loading configuration", "config_path", configPath)
		config, err := llm.LoadConfig(configPath)
		if err != nil {
			slog.Error("Failed to load configuration", "error", util.WrapError(ctx, err, "main::LoadConfig"))
			os.Exit(1)
		}

		client, err := llm.NewClient(config)
		if err != nil {
			slog.Error("Failed to create client", "error", util.WrapError(ctx, err, "main::NewClient"))
			os.Exit(1)
		}

		codingAssistant, err := app.NewCodingAssistant(client)
		if err != nil {
			slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodingAssistant"))
			os.Exit(1)
		}
		codingAssistant.DisabledAgents = disableAgents
		codingAssistant.CodebasePort = codebasePort

		taskManager := http.NewTaskManager(nil)

		dataManager, err := datamanager.NewDataManager()
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
		config, err := llm.LoadConfig(configPath)
		if err != nil {
			slog.Error("Failed to load configuration", "error", util.WrapError(ctx, err, "main::LoadConfig"))
			os.Exit(1)
		}
		slog.Info("Creating assistant.client")
		client, err := llm.NewClient(config)
		if err != nil {
			slog.Error("Failed to create assistant.client", "error", util.WrapError(ctx, err, "main::NewClient"))
			os.Exit(1)
		}

		// 创建 AI Coding Assistant
		codingAssistant, err := app.NewCodingAssistant(client)
		if err != nil {
			slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodingAssistant"))
			os.Exit(1)
		}
		codingAssistant.DisabledAgents = disableAgents
		codingAssistant.CodebasePort = codebasePort

		// 创建消息分发器并集成消息系统
		messageDispatcher := messaging.NewMessageDispatcher(100)
		codingAssistant.IntegrateMessaging(messageDispatcher)

		// 创建HTTP服务器
		server := http.NewServer(codingAssistant)

		// 使用命令行参数指定的端口启动服务器
		if err := server.Run(httpPort); err != nil {
			slog.Error("Failed to start HTTP server", "error", util.WrapError(ctx, err, "main::ServerRun"))
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown mode: %s\n", mode)
		fmt.Println("Usage: codeactor [tui|http] [--disable-agents=repo,coding,chat,meta,devops] [--taskfile TASK.md] [--port=9800]")
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

// findAvailablePort 从 startPort 开始递增查找第一个可用的 TCP 端口
func findAvailablePort(startPort int) (int, error) {
	for port := startPort; port < startPort+100; port++ {
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			listener.Close()
			return port, nil
		}
	}
	return 0, fmt.Errorf("no available port found starting from %d", startPort)
}

// startCodebaseServer starts the codeactor-codebase server as a background process.
// Returns the *exec.Cmd so the caller can kill the process on exit.
func startCodebaseServer(port int, repoPath string) *exec.Cmd {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Error("Failed to get user home directory", "error", err)
		return nil
	}

	binPath, err := embedbin.BinPath("codeactor-codebase")
	if err != nil {
		slog.Error("Failed to get codeactor-codebase bin path", "error", err)
		return nil
	}
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		slog.Error("codeactor-codebase binary not found, skipping startup", "path", binPath)
		return nil
	}

	logDir := filepath.Join(homeDir, ".codeactor/logs/codeactor-codebase")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Error("Failed to create log directory", "error", err)
		return nil
	}

	now := time.Now()
	logFileName := fmt.Sprintf("%s.log", now.Format("2006-01-02"))
	logPath := filepath.Join(logDir, logFileName)

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		slog.Error("Failed to create log file", "error", err)
		return nil
	}

	address := fmt.Sprintf("127.0.0.1:%d", port)
	cmd := exec.Command(binPath, "-v", "server", "--repo-path", repoPath, "--address", address)
	cmd.Env = append(os.Environ(), "RUST_LOG=info")
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		slog.Error("Failed to start codeactor-codebase", "error", err)
		return nil
	}

	slog.Info("Started codeactor-codebase server", "pid", cmd.Process.Pid, "address", address, "repo", repoPath, "log", logPath)

	// Health check runs asynchronously so TUI/HTTP startup is not blocked.
	// If the codebase server never becomes healthy, the process is killed
	// and tools that depend on it will surface errors at call time.
	go func() {
		if err := waitForCodebase(address, 60*time.Second); err != nil {
			slog.Error("Codebase server failed to become healthy", "error", err)
			cmd.Process.Kill()
		}
	}()

	return cmd
}

// waitForCodebase polls the /health endpoint until the service responds or timeout.
func waitForCodebase(address string, timeout time.Duration) error {
	healthURL := fmt.Sprintf("http://%s/health", address)
	deadline := time.Now().Add(timeout)
	client := &nethttp.Client{Timeout: 2 * time.Second}

	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(healthURL)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == nethttp.StatusOK {
			slog.Info("Codebase server is healthy", "address", address)
			return nil
		}
		lastErr = fmt.Errorf("health check returned status %d", resp.StatusCode)
		time.Sleep(500 * time.Millisecond)
	}

	return fmt.Errorf("codebase server at %s not healthy after %v: %w", address, timeout, lastErr)
}
