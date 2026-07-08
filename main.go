package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"codeactor/internal/app"
	"codeactor/internal/agents"
	"codeactor/internal/config"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/llm"
	"codeactor/internal/logging"
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
	// Safety net: silence all slog output until proper initialization in main().
	// This prevents any pre-main() logging from corrupting the TUI display.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
// 返回: repoPath, error
// TODO: [Codexray] codexray server initialization removed (port allocation, binary extraction, server start, health check). Re-add when codexray is re-integrated.
func initApp() (string, error) {
	repoPath, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}
	return repoPath, nil
}

// runTUI 启动终端 UI 模式
func runTUI(taskFile, disableAgents string) {
	// Initialize mode-aware logging: file only, never stdout/stderr
	if err := logging.Init(logging.ModeTUI); err != nil {
		slog.Error("Failed to initialize logging", "error", err)
	}
	defer logging.Close()

	repoPath, err := initApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// TODO: [Codexray] CodexrayPort assignment removed — was codeActor.CodexrayPort = codexrayPort. Re-add when codexray is re-integrated.
	// TODO: [Codexray] codexrayCmd cleanup deferred — was killing subprocess on exit. Re-add when codexray is re-integrated.

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
	codeActor.SetEmbeddedBinaries(distBinFS)

	if err := agents.InitToolLogger(); err != nil {
		slog.Warn("Failed to initialize tool logger", "error", err)
	}

	codeActor.DisabledAgents = disableAgents
	// TODO: [Codexray] CodexrayPort field assignment removed. Re-add when codexray is re-integrated.

	// 主动初始化：启动 codeseek MCP 等核心服务
	codeActor.Init(client.Engine, repoPath)

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
	} else {
		dataManager.Start()
	}

	tui.StartTUI(taskFile, codeActor, taskManager, dataManager, config)
	dataManager.FlushAll()
	dataManager.Stop()
}

// runHTTP 启动 HTTP 服务器模式
func runHTTP(taskFile, disableAgents string, httpPort int) {
	// Initialize mode-aware logging: stderr + file
	if err := logging.Init(logging.ModeHTTP); err != nil {
		slog.Error("Failed to initialize logging", "error", err)
	}
	defer logging.Close()

	repoPath, err := initApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// TODO: [Codexray] CodexrayPort assignment removed — was codeActor.CodexrayPort = codexrayPort. Re-add when codexray is re-integrated.
	// TODO: [Codexray] codexrayCmd cleanup deferred — was killing subprocess on exit. Re-add when codexray is re-integrated.

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
	codeActor.SetEmbeddedBinaries(distBinFS)
	codeActor.DisabledAgents = disableAgents
	// TODO: [Codexray] CodexrayPort field assignment removed. Re-add when codexray is re-integrated.

	// 主动初始化：启动 codeseek MCP 等核心服务
	codeActor.Init(client.Engine, repoPath)

	defer codeActor.Close()

	messageDispatcher := messaging.NewMessageDispatcher(100)
	codeActor.IntegrateMessaging(messageDispatcher)

	server := http.NewServer(codeActor)

	// TODO: [Codexray] findAvailablePort call removed — was finding port starting from 9800. Re-add when codexray is re-integrated.
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


// TODO: [Codexray] findAvailablePort removed — was finding available TCP port starting from 12800. Re-add when codexray is re-integrated.

// TODO: [Codexray] startCodexrayServer removed — was launching codeactor-codexray subprocess. Re-add when codexray is re-integrated.

// TODO: [Codexray] waitForCodexray removed — was polling /health endpoint with 60s timeout. Re-add when codexray is re-integrated.
