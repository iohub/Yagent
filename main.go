package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"yagent/internal/agents"
	"yagent/internal/app"
	"yagent/internal/config"
	"yagent/internal/datamanager"
	"yagent/internal/http"
	"yagent/internal/llm"
	"yagent/internal/logging"
	messaging "yagent/internal/messaging"
	tuiMsg "yagent/internal/messaging/consumers"
	"yagent/internal/models"
	"yagent/internal/shutdown"
	"yagent/internal/tui"
	"yagent/internal/util"

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
	Use:   "yagent",
	Short: "Yagent - AI-powered coding assistant",
	Long: `Yagent is an AI-powered coding assistant that can run in
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

// setupCmd 独立跑一次引导配置：不装配 Agent、不起 MCP，只把 config.toml 写对。
// 它必须能独立于正常启动存在 —— 配置坏到加载不了的时候，"先修好再启动"是唯一顺序。
var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure LLM providers and first-run settings",
	Run: func(cmd *cobra.Command, args []string) {
		runSetup()
	},
}

func runSetup() {
	if err := logging.Init(logging.ModeTUI); err != nil {
		slog.Error("Failed to initialize logging", "error", err)
	}
	defer logging.Close()

	if !terminalAvailable() {
		fmt.Fprintln(os.Stderr, "yagent setup needs a terminal (it opens an interactive wizard)")
		fmt.Fprintln(os.Stderr, "  to edit by hand instead: docs/config-guide.md")
		os.Exit(2)
	}

	path, _, err := getConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "yagent: %v\n", err)
		os.Exit(2)
	}

	// 这里刻意不 LoadConfig：配置坏了也要能进向导，那正是它存在的理由。
	// NeedsSetup 走 nil cfg 时给 SetupBroken 判据，向导自己会显示原始错误。
	cfg, loadErr := llm.LoadConfig(path)
	reason, detail := config.NeedsSetup(cfg, path, false, loadErr)
	// 配置看着可用时 reason = SetupNotNeeded、detail = ""：手动调用就是要再走一遍
	// （比如来加第二个 provider），欢迎屏不展示"为什么进引导"。

	saved, runErr := tui.RunSetup(path, reason, detail)
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "yagent setup: %v\n", runErr)
		os.Exit(1)
	}
	if !saved {
		fmt.Fprintln(os.Stderr, "setup exited without writing: config.toml unchanged")
		return
	}
	if _, err := llm.LoadConfig(path); err != nil {
		fmt.Fprintf(os.Stderr, "setup wrote %s, but it still fails to load:\n  %v\n", path, err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "saved %s — run `yagent` to start\n", path)
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
	rootCmd.AddCommand(setupCmd)
}

func main() {
	defer util.RecoverPanic()

	// 加载内嵌模型元数据目录：上下文压缩阈值与 TUI 上下文进度条据此推导窗口上限。
	// 失败时（理论上不会发生，文件已内嵌）查询返回 0，各处回退到既有默认值。
	if err := models.Load(distDataFS); err != nil {
		slog.Warn("Failed to load embedded model catalog", "error", err)
	}

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

	config, configPath := loadConfigForStartup("tui")

	client, err := llm.NewClient(config)
	if err != nil {
		slog.Error("Failed to create client", "error", util.WrapError(ctx, err, "main::NewClient"))
		os.Exit(1)
	}
	// 记录配置文件路径，供 TUI 空格确认时把 provider 选择写回 config.toml
	client.ConfigPath = configPath

	yagent, err := app.NewYagent(client)
	if err != nil {
		slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewYagent"))
		os.Exit(1)
	}
	yagent.SetEmbeddedBinaries(distBinFS)

	if err := agents.InitToolLogger(); err != nil {
		slog.Warn("Failed to initialize tool logger", "error", err)
	}

	yagent.DisabledAgents = disableAgents
	yagent.YoloMode = yoloMode
	yagent.FullYoloMode = fullYoloMode
	yagent.ForceQuit = forceQuit
	// 主动初始化：启动 codeseek MCP 等核心服务
	yagent.Init(client.Engine, repoPath)

	defer yagent.Close()

	taskManager := http.NewTaskManager(nil, config.TaskTimeout)

	dataManager, err := datamanager.NewDataManager()
	if err != nil {
		slog.Error("Failed to initialize DataManager", "error", err)
	} else {
		dataManager.Start()
	}

	// 信号 → 任务取消。必须在第一个任务 ctx 派生之前装好（TUI 的任务 ctx 都在 StartTUI 里建），
	// 漏了不报错，只是"信号来了这条任务不停"。TUI 模式不写 OnSignal：raw mode 下 stderr 与
	// 界面共用同一个终端，直接打印会撕裂渲染。
	_, stopSignals := shutdown.Install(shutdown.Config{})

	tui.StartTUI(taskFile, yagent, taskManager, dataManager, config)
	// 看门狗挂在进程根域上：先把它收掉，否则下面 Close 的 Drain 会把 "shutdown/signals"
	// 报成一条泄漏。
	stopSignals()
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

	config, configPath := loadConfigForStartup("http")
	slog.Info("Creating assistant.client")
	client, err := llm.NewClient(config)
	if err != nil {
		slog.Error("Failed to create assistant.client", "error", util.WrapError(ctx, err, "main::NewClient"))
		os.Exit(1)
	}
	// 记录配置文件路径，供运行时持久化 provider 选择写回 config.toml
	client.ConfigPath = configPath

	yagent, err := app.NewYagent(client)
	if err != nil {
		slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewYagent"))
		os.Exit(1)
	}
	yagent.SetEmbeddedBinaries(distBinFS)
	yagent.DisabledAgents = disableAgents
	yagent.YoloMode = yoloMode
	yagent.FullYoloMode = fullYoloMode
	yagent.ForceQuit = forceQuit
	// 主动初始化：启动 codeseek MCP 等核心服务
	yagent.Init(client.Engine, repoPath)

	defer yagent.Close()

	messageDispatcher := messaging.NewDefaultMessageDispatcher()
	yagent.IntegrateMessaging(messageDispatcher)

	server := http.NewServer(yagent)

	// 信号 → 停掉所有在跑的任务。HTTP 的监听端口不会优雅关闭（server.Run 就是 gin 的
	// router.Run，手里没有 http.Server），所以第一次信号之后进程仍在 listen ——
	// 要立刻走就再按一次：第二次是硬退出（先杀我们自己起的进程组，再按 128+signum 退）。
	_, stopSignals := shutdown.Install(shutdown.Config{OnSignal: noticeSignal})
	defer stopSignals()

	slog.Info("Starting HTTP server", "port", httpPort)

	if err := server.Run(httpPort); err != nil {
		slog.Error("Failed to start HTTP server", "error", util.WrapError(ctx, err, "main::ServerRun"))
		os.Exit(1)
	}
}

// runPrompt 直接执行任务提示（非交互模式），将过程输出到 stdout
func runPrompt(taskPromptStr, disableAgentsStr string) {
	// 退出码在 promptOnce 返回之后才落地：函数体里直接 os.Exit(1) 会跳过 logging.Close、
	// yagent.Close（含进程根域的 Drain）与 dispatcher.Shutdown —— 而"信号来了但收尾没跑"
	// 正是这次要修的形状（docs/run-bash-hang-fix.md §4.1）。
	os.Exit(promptOnce(taskPromptStr, disableAgentsStr))
}

func promptOnce(taskPromptStr, disableAgentsStr string) int {
	// Initialize mode-aware logging: file only, never stdout/stderr
	if err := logging.Init(logging.ModeTUI); err != nil {
		slog.Error("Failed to initialize logging", "error", err)
	}
	defer logging.Close()

	repoPath, err := initApp()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	ctx := context.Background()

	config, configPath := loadConfigForStartup("prompt")

	client, err := llm.NewClient(config)
	if err != nil {
		slog.Error("Failed to create client", "error", util.WrapError(ctx, err, "main::NewClient"))
		return 1
	}
	// 记录配置文件路径，供运行时持久化 provider 选择写回 config.toml
	client.ConfigPath = configPath

	yagent, err := app.NewYagent(client)
	if err != nil {
		slog.Error("Failed to create coding assistant", "error", util.WrapError(ctx, err, "main::NewYagent"))
		return 1
	}
	yagent.SetEmbeddedBinaries(distBinFS)

	if err := agents.InitToolLogger(); err != nil {
		slog.Warn("Failed to initialize tool logger", "error", err)
	}

	yagent.DisabledAgents = disableAgentsStr
	yagent.YoloMode = yoloMode
	yagent.FullYoloMode = fullYoloMode
	yagent.ForceQuit = forceQuit
	yagent.Init(client.Engine, repoPath)
	defer yagent.Close()

	// 设置消息总线 - 使用 TUIConsumer 输出到 stdout
	dispatcher := messaging.NewDefaultMessageDispatcher()
	defer dispatcher.Shutdown()

	publisher := messaging.NewMessagePublisher(dispatcher)
	tuiConsumer := tuiMsg.NewTUIConsumer(os.Stdout, publisher)
	dispatcher.RegisterConsumer(tuiConsumer)

	yagent.IntegrateMessaging(dispatcher)

	// 信号 → 取消这次任务。必须在下面建任务 ctx 之前装；defer 排在 Close 之后注册，
	// 所以它在 yagent.Close 的 Drain 之前把看门狗收掉（否则每次退出都多报一条泄漏）。
	_, stopSignals := shutdown.Install(shutdown.Config{OnSignal: noticeSignal})
	defer stopSignals()

	// 构建并执行任务
	taskID := fmt.Sprintf("prompt-%d", time.Now().UnixMilli())
	request := app.NewTaskRequest(shutdown.Root(), taskID).
		WithProjectDir(repoPath).
		WithTaskDesc(taskPromptStr).
		WithMessagePublisher(publisher)

	result, err := yagent.ProcessCodingTaskWithCallback(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n❌ Task failed: %v\n", err)
		return 1
	}

	// 打印最终结果（仅在 TUIConsumer 未显示的情况下）
	if result != "" {
		fmt.Printf("\n📋 Task result:\n%s\n", result)
	}
	return 0
}

// noticeSignal 是两个非交互入口（--prompt / http）唯一的"信号到了"反馈通道：
// 它们没有界面，stderr 就是全部的用户可见输出。TUI 模式刻意不传这个钩子 ——
// raw mode 下 stderr 与界面共用同一个终端，写一行就撕一次渲染。
func noticeSignal(sig os.Signal) {
	fmt.Fprintf(os.Stderr, "\nyagent: received %v — cancelling running tasks; "+
		"send the signal again to exit immediately\n", sig)
}

// getConfigPath 返回配置文件路径，并报告"这次是不是刚生成的模板"。
// 优先级：--config 指定的路径（不自动创建）→ $HOME/.yagent/config/config.toml（缺失则生成）。
// 只有在拿不到 home 目录时才会退到 CWD 相对的 config/config.toml —— 那不是第二级查找。
func getConfigPath() (path string, created bool, err error) {
	if configPathFlag != "" {
		return configPathFlag, false, nil
	}

	homeDir, homeErr := os.UserHomeDir()
	if homeErr != nil {
		localPath := "config/config.toml"
		if _, statErr := os.Stat(localPath); os.IsNotExist(statErr) {
			created, err = config.EnsureConfigExists(localPath)
			if err != nil {
				return "", false, fmt.Errorf("无法创建配置文件: %w", err)
			}
		}
		return localPath, created, nil
	}

	configPath := filepath.Join(homeDir, ".yagent", "config", "config.toml")
	created, err = config.EnsureConfigExists(configPath)
	if err != nil {
		return "", false, fmt.Errorf("无法创建配置文件: %w", err)
	}
	return configPath, created, nil
}

// loadConfigForStartup 是三入口（tui / http / prompt）共用的配置装配点：
// 解析路径 → 加载 → 需要时把用户领进引导 → 返回可用配置。
//
// 修掉的是这个洞：以前加载失败只写进 ~/.yagent/logs 然后 os.Exit(1)，终端上什么都不显示；
// 而模板自带的 api_key = "your-key" 能通过校验（validate 只查非空），于是全新机器
// "启动成功"，直到第一次提交任务才在 LLM 调用上炸。
func loadConfigForStartup(mode string) (*config.Config, string) {
	ctx := context.Background()

	path, created, err := getConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "yagent: %v\n配置文件: %s\n", err, "~/.yagent/config/config.toml")
		os.Exit(2)
	}

	slog.Info("Loading configuration", "config_path", path, "created", created)
	cfg, loadErr := llm.LoadConfig(path)
	if loadErr != nil {
		slog.Error("Failed to load configuration", "error", util.WrapError(ctx, loadErr, "main::LoadConfig"))
	}

	reason, detail := config.NeedsSetup(cfg, path, created, loadErr)
	if reason == config.SetupNotNeeded {
		return cfg, path
	}

	// 引导页要接管键盘，所以只有 TUI 且 stdin/stdout 都还在终端上才跑得动。
	if mode == "tui" && terminalAvailable() {
		fmt.Fprintf(os.Stderr, "yagent: 需要配置 LLM 连接（%s）\n正在打开引导…\n", reason)
		if _, runErr := tui.RunSetup(path, reason, detail); runErr != nil {
			fmt.Fprintf(os.Stderr, "yagent: 引导未正常结束: %v\n", runErr)
		}
		reloaded, err := llm.LoadConfig(path)
		if err != nil {
			failWithConfigError(path, reason, detail, err)
		}
		return reloaded, path
	}

	failWithConfigError(path, reason, detail, loadErr)
	return nil, path // 不可达：failWithConfigError 直接退出
}

// failWithConfigError 把配置问题写成人话打到 stderr：哪一项、在哪个文件、下一步敲什么。
// --prompt / http 这类非交互入口没有界面可退，这三行就是它们全部的失败 UX。
func failWithConfigError(path string, reason config.SetupReason, detail string, loadErr error) {
	fmt.Fprintln(os.Stderr, "yagent: configuration is not usable")
	if detail != "" {
		fmt.Fprintf(os.Stderr, "  reason: %s\n", detail)
	} else if loadErr != nil {
		fmt.Fprintf(os.Stderr, "  reason: %v\n", loadErr)
	} else {
		fmt.Fprintf(os.Stderr, "  reason: %s\n", reason)
	}
	fmt.Fprintf(os.Stderr, "  file:   %s\n", path)
	fmt.Fprintln(os.Stderr, "  fix:    run `yagent setup` (or edit the file; see docs/config-guide.md)")
	fmt.Fprintf(os.Stderr, "  logs:   %s\n", filepath.Join(configHomeDir(), "logs"))
	os.Exit(2)
}

func configHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".yagent")
	}
	return ".yagent"
}

// terminalAvailable 判断还能不能把终端交给 bubbletea：管道里跑 --prompt 时两者都不是字符设备。
func terminalAvailable() bool {
	in, err := os.Stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := os.Stdout.Stat()
	if err != nil || out.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return true
}
