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
	"time"

	"codeactor/internal/app"
	"codeactor/internal/agents"
	"codeactor/internal/config"
	"codeactor/internal/datamanager"
	"codeactor/internal/embedbin"
	"codeactor/internal/http"
	"codeactor/internal/llm"
	"codeactor/internal/skills"
	"codeactor/internal/tui"
	"codeactor/internal/util"
	messaging "codeactor/internal/messaging"

	"github.com/spf13/cobra"
)

// 全局 Cobra flag 绑定变量
var (
	taskFile      string
	disableAgents string
	httpPort      int
)

// rootCmd 根命令 — 无子命令时默认启动 TUI
var rootCmd = &cobra.Command{
	Use:   "codeactor",
	Short: "CodeActor - AI-powered coding assistant",
	Long: `CodeActor is an AI-powered coding assistant that can run in
terminal UI mode or HTTP server mode.`,
	Run: func(cmd *cobra.Command, args []string) {
		runTUI(taskFile, disableAgents)
	},
	SilenceUsage: true,
}

// tuiCmd 显式 TUI 子命令
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Start in terminal UI mode (default)",
	Run: func(cmd *cobra.Command, args []string) {
		runTUI(taskFile, disableAgents)
	},
}

// httpCmd HTTP 服务器子命令
var httpCmd = &cobra.Command{
	Use:   "http",
	Short: "Start in HTTP server mode",
	Run: func(cmd *cobra.Command, args []string) {
		runHTTP(taskFile, disableAgents, httpPort)
	},
}

func init() {
	// Initialize structured logger with text handler
	// Use LevelWarn so that warnings (e.g. codebase startup failures) are visible.
	opts := &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)

	// Initialize language manager with default language (English)
	tui.InitLangManager()

	// --- Cobra 命令配置 ---
	// 持久 flags（所有子命令可用）
	rootCmd.PersistentFlags().StringVarP(&taskFile, "taskfile", "f", "", "Load task from file")
	rootCmd.PersistentFlags().StringVarP(&disableAgents, "disable-agents", "d", "", "Disable specified agents (comma-separated)")
	// http 子命令专属 flags
	httpCmd.Flags().IntVarP(&httpPort, "port", "p", 0, "HTTP server port (0 = auto-detect from 9800)")
	// 注册子命令
	rootCmd.AddCommand(tuiCmd)
	rootCmd.AddCommand(httpCmd)
}

func main() {
	defer util.RecoverPanic()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// initApp 执行 TUI 和 HTTP 模式共用的初始化逻辑
// 返回: repoPath, codebasePort, codebaseCmd (用于清理), error
func initApp() (string, int, *exec.Cmd, error) {
	repoPath, err := os.Getwd()
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to get current working directory: %w", err)
	}

	codebasePort, err := findAvailablePort(12800)
	if err != nil {
		return "", 0, nil, fmt.Errorf("failed to find available port for codebase: %w", err)
	}

	if _, err := embedbin.ExtractBinaries(distBinFS, "dist/bin"); err != nil {
		slog.Warn("Failed to extract embedded binaries", "error", err)
	}

	codebaseCmd := startCodebaseServer(codebasePort, repoPath)
	return repoPath, codebasePort, codebaseCmd, nil
}

// runTUI 启动终端 UI 模式
func runTUI(taskFile, disableAgents string) {
	repoPath, codebasePort, codebaseCmd, err := initApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
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

	// TUI mode init — matches original switch "tui" case exactly
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

	codeActor, err := app.NewCodeActor(client)
	if err != nil {
		slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodeActor"))
		os.Exit(1)
	}

	if err := agents.InitToolLogger(); err != nil {
		slog.Warn("Failed to initialize tool logger", "error", err)
	}

	codeActor.DisabledAgents = disableAgents
	codeActor.CodebasePort = codebasePort

	defer codeActor.Close()

	// 加载 skills
	homeDir, _ := os.UserHomeDir()
	projectSkillsDir := filepath.Join(repoPath, ".codeactor", "skills")
	homeSkillsDir := filepath.Join(homeDir, ".codeactor", "skills")

	var skillRegistry *skills.SkillRegistry
	skillRegistry, err = skills.LoadSkills(projectSkillsDir)
	if err != nil {
		slog.Warn("Failed to load project skills", "path", projectSkillsDir, "error", err)
	}
	if skillRegistry.Count() == 0 {
		skillRegistry, err = skills.LoadSkills(homeSkillsDir)
		if err != nil {
			slog.Warn("Failed to load home skills", "path", homeSkillsDir, "error", err)
		}
	}
	codeActor.SkillRegistry = skillRegistry
	slog.Info("Skill registry loaded", "count", skillRegistry.Count())

	taskManager := http.NewTaskManager(nil, config.TaskTimeout)

	dataManager, err := datamanager.NewDataManager()
	if err != nil {
		slog.Error("Failed to initialize DataManager", "error", err)
	}

	tui.StartTUI(taskFile, codeActor, taskManager, dataManager, config)
	dataManager.FlushAll()
}

// runHTTP 启动 HTTP 服务器模式
func runHTTP(taskFile, disableAgents string, httpPort int) {
	_, codebasePort, codebaseCmd, err := initApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if codebaseCmd != nil {
		defer func() {
			if err := codebaseCmd.Process.Kill(); err != nil {
				slog.Warn("Failed to kill codebase process", "error", err)
			} else {
				slog.Info("Codebase process killed on exit", "pid", codebaseCmd.Process.Pid)
			}
		}()
	}

	// HTTP mode init — matches original switch "http" case exactly
	ctx := context.Background()

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

	multiWriter := io.MultiWriter(os.Stderr, logFile)
	logger := slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

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

	codeActor, err := app.NewCodeActor(client)
	if err != nil {
		slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewCodeActor"))
		os.Exit(1)
	}
	codeActor.DisabledAgents = disableAgents
	codeActor.CodebasePort = codebasePort

	defer codeActor.Close()

	messageDispatcher := messaging.NewMessageDispatcher(100)
	codeActor.IntegrateMessaging(messageDispatcher)

	server := http.NewServer(codeActor)

	if httpPort == 0 {
		port, err := findAvailablePort(9800)
		if err != nil {
			slog.Error("Failed to find available port for HTTP server", "error", err)
			os.Exit(1)
		}
		httpPort = port
	}
	slog.Info("Starting HTTP server", "port", httpPort)

	if err := server.Run(httpPort); err != nil {
		slog.Error("Failed to start HTTP server", "error", util.WrapError(ctx, err, "main::ServerRun"))
		os.Exit(1)
	}
}

// getConfigPath 返回配置文件的路径，优先使用 $HOME/.codeactor/config/config.toml
// 如果配置文件不存在，则自动生成默认配置模板
func getConfigPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		localPath := "config/config.toml"
		if _, err := os.Stat(localPath); os.IsNotExist(err) {
			if genErr := config.EnsureConfigExists(localPath); genErr != nil {
				panic(fmt.Sprintf("无法创建配置文件: %v", genErr))
			}
		}
		return localPath
	}

	configDir := filepath.Join(homeDir, ".codeactor", "config")
	configPath := filepath.Join(configDir, "config.toml")

	if err := config.EnsureConfigExists(configPath); err != nil {
		panic(fmt.Sprintf("无法创建配置文件: %v", err))
	}

	return configPath
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
