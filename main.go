package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"codeactor/internal/agents"
	"codeactor/internal/app"
	"codeactor/internal/config"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/llm"
	"codeactor/internal/logging"
	messaging "codeactor/internal/messaging"
	tuiMsg "codeactor/internal/messaging/consumers"
	"codeactor/internal/skills"
	"codeactor/internal/tui"
	"codeactor/internal/util"

	"github.com/spf13/cobra"
)

// 全局 Cobra flag 绑定变量
var (
	taskFile      string
	taskPrompt    string
	disableAgents string
	httpPort      int
	yoloMode      bool
	fullYoloMode  bool
	forceQuit     bool // --force-quit: 强制退出模式，agent_exit 时直接退出，不等待 codeseek 进程安全退出
	// Config path override
	configPathFlag string
)

// rootCmd 根命令 — 无子命令时默认启动 TUI
var rootCmd = &cobra.Command{
	Use:   "codeactor",
	Short: "CodeActor - AI-powered coding assistant",
	Long: `CodeActor is an AI-powered coding assistant that can run in
terminal UI mode or HTTP server mode.`,
	Run: func(cmd *cobra.Command, args []string) {
		if taskPrompt != "" {
			runPrompt(taskPrompt, disableAgents)
		} else {
			runTUI(taskFile, disableAgents)
		}
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
	rootCmd.PersistentFlags().StringVarP(&taskPrompt, "prompt", "p", "", "Execute a task prompt directly (non-interactive mode)")
	rootCmd.PersistentFlags().StringVarP(&disableAgents, "disable-agents", "d", "", "Disable specified agents (comma-separated)")
	// Config path override
	rootCmd.PersistentFlags().StringVarP(&configPathFlag, "config", "c", "", "Path to config.toml file (overrides default config path)")
	// http 子命令专属 flags
	httpCmd.Flags().IntVar(&httpPort, "port", 0, "HTTP server port (0 = auto-detect from 9800)")
	// YOLO 模式：跳过所有授权检查
	rootCmd.PersistentFlags().BoolVarP(&yoloMode, "yolo", "y", false, "YOLO mode: auto-approve all dangerous operations without user confirmation")
	rootCmd.PersistentFlags().BoolVarP(&fullYoloMode, "full-yolo", "Y", false, "FULL-YOLO mode: autonomous mode (implies --yolo), removes ask_user_for_help from all agents, agents make decisions independently")
	// ForceQuit 模式：agent_exit 时强制退出，不等待 codeseek 进程安全退出
	rootCmd.PersistentFlags().BoolVarP(&forceQuit, "force-quit", "Q", false, "Force quit: exit immediately without waiting for codeseek process to shut down safely")
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
	codeActor.YoloMode = yoloMode
	codeActor.FullYoloMode = fullYoloMode
	codeActor.ForceQuit = forceQuit
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
	codeActor.YoloMode = yoloMode
	codeActor.FullYoloMode = fullYoloMode
	codeActor.ForceQuit = forceQuit
	// 主动初始化：启动 codeseek MCP 等核心服务
	codeActor.Init(client.Engine, repoPath)

	defer codeActor.Close()

	messageDispatcher := messaging.NewMessageDispatcher(100)
	codeActor.IntegrateMessaging(messageDispatcher)

	server := http.NewServer(codeActor)

	slog.Info("Starting HTTP server", "port", httpPort)

	if err := server.Run(httpPort); err != nil {
		slog.Error("Failed to start HTTP server", "error", util.WrapError(ctx, err, "main::ServerRun"))
		os.Exit(1)
	}
}

// runPrompt 直接执行任务提示（非交互模式），将过程输出到 stdout
func runPrompt(taskPromptStr, disableAgentsStr string) {
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

	codeActor.DisabledAgents = disableAgentsStr
	codeActor.YoloMode = yoloMode
	codeActor.FullYoloMode = fullYoloMode
	codeActor.ForceQuit = forceQuit
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

	// 设置消息总线 - 使用 TUIConsumer 输出到 stdout
	dispatcher := messaging.NewMessageDispatcher(100)
	defer dispatcher.Shutdown()

	publisher := messaging.NewMessagePublisher(dispatcher)
	tuiConsumer := tuiMsg.NewTUIConsumer(os.Stdout, publisher)
	dispatcher.RegisterConsumer(tuiConsumer)

	codeActor.IntegrateMessaging(dispatcher)

	// 构建并执行任务
	taskID := fmt.Sprintf("prompt-%d", time.Now().UnixMilli())
	request := app.NewTaskRequest(ctx, taskID).
		WithProjectDir(repoPath).
		WithTaskDesc(taskPromptStr).
		WithMessagePublisher(publisher)

	result, err := codeActor.ProcessCodingTaskWithCallback(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Task failed: %v\n", err)
		os.Exit(1)
	}

	// 打印最终结果（仅在 TUIConsumer 未显示的情况下）
	if result != "" {
		fmt.Printf("\n📋 Task result:\n%s\n", result)
	}
}

// getConfigPath 返回配置文件的路径，优先使用 $HOME/.codeactor/config/config.toml
// 如果配置文件不存在，则自动生成默认配置模板
// 如果命令行通过 --config/-c 指定了路径，则直接使用该路径（不自动创建）
func getConfigPath() string {
	// 如果命令行指定了 --config，直接使用（不自动创建配置文件）
	if configPathFlag != "" {
		return configPathFlag
	}

	// 原有逻辑...
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
