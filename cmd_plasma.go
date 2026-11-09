package main

import (
	"errors"
	"fmt"
	"os"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/logging"

	"github.com/spf13/cobra"
)

// 本文件是 `yagent plasma` 的命令树与共享装配（设计稿 §7.2）。
//
// 装配纪律（§7.2 的"装配"列）是硬约束：manifest / list / promote **不装配 Agent**
// —— 它们只要 config + store；compile / test 需要夹具执行 ⇒ 需要 toolspec 注册表
// （package main 已经空白 import 了 tools 与 agents，注册表天然 populate），
// 但同样**不起** MCP / TUI / Director；sample 需要引擎 ⇒ llm.LoadConfig + llm.NewClient。
//
// 这条纪律不是洁癖：`app.NewYagent` 会起 MCP、连 codeseek、装 dispatcher，而
// `yagent plasma list` 要做的只是读一个 index.json。带着半个内核去读文件，
// 失败模式会从"目录不存在"变成"codeseek 没能拉起"。

// plasmaFlags 是六个子命令的 flag 落点。共用一份而不是各自 var：
// 命令树短，flag 名互不冲突，共用省掉六段样板。
var plasmaFlags struct {
	bundle     string
	behavioral bool
	fixtureID  string
	messages   string
	tier       string
	name       string
	hash       string
	minRuns    int
	force      bool
	asJSON     bool
}

// usageError 标记"命令用法错"（exit 2），与"这次操作失败"（exit 1）区分开。
// §7.3 的离线退出码就是这么分的：2 = 用法错，1 = 业务失败。
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func newUsageError(format string, args ...interface{}) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

var plasmaCmd = &cobra.Command{
	Use:   "plasma",
	Short: "Compile, inspect and promote plasma (generated) agents",
	Long: `Plasma is the generated-agent pipeline: Python proposes a bundle, Go compiles and
verifies it, and only verified bundles may be auto-delegated.

These subcommands never start the TUI, MCP or the agent runtime.`,
}

var plasmaCompileCmd = &cobra.Command{
	Use:   "compile --bundle <path>",
	Short: "Run the compiler (phases 1-4) plus structural fixtures (phase 5a)",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaCompile(plasmaFlags.bundle, plasmaFlags.behavioral, plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

var plasmaTestCmd = &cobra.Command{
	Use:   "test --bundle <path> --fixture <id>",
	Short: "Run exactly one structural fixture from a bundle",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaTest(plasmaFlags.bundle, plasmaFlags.fixtureID, plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

var plasmaSampleCmd = &cobra.Command{
	Use:   "sample --messages <path> --tier <fast|balanced|high>",
	Short: "Send a message list through the configured engine and print the raw response",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaSample(plasmaFlags.messages, plasmaFlags.tier, plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

var plasmaManifestCmd = &cobra.Command{
	Use:   "manifest",
	Short: "Print the capability/code manifest published to Python",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaManifest(plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

var plasmaListCmd = &cobra.Command{
	Use:   "list",
	Short: "List agents in the plasma store",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaList(plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

var plasmaPromoteCmd = &cobra.Command{
	Use:   "promote --name <name>",
	Short: "Promote a verified agent (and point index.json at it)",
	Run: func(cmd *cobra.Command, args []string) {
		failPlasma(runPlasmaWithLogging(func() error {
			return runPlasmaPromote(plasmaFlags.name, plasmaFlags.hash, plasmaFlags.minRuns,
				plasmaFlags.force, plasmaFlags.asJSON, os.Stdout)
		}))
	},
}

func init() {
	plasmaCompileCmd.Flags().StringVar(&plasmaFlags.bundle, "bundle", "", "path to bundle.json")
	plasmaCompileCmd.Flags().BoolVar(&plasmaFlags.behavioral, "behavioral", false,
		"also run phase 5b behavioral fixtures (needs a real LLM)")
	plasmaCompileCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit JSON instead of text")

	plasmaTestCmd.Flags().StringVar(&plasmaFlags.bundle, "bundle", "", "path to bundle.json")
	plasmaTestCmd.Flags().StringVar(&plasmaFlags.fixtureID, "fixture", "", "structural fixture id")
	plasmaTestCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit JSON instead of text")

	plasmaSampleCmd.Flags().StringVar(&plasmaFlags.messages, "messages", "", "path to a JSON array of llm.Message")
	plasmaSampleCmd.Flags().StringVar(&plasmaFlags.tier, "tier", "", "routing tier: fast | balanced | high")
	plasmaSampleCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit the raw response JSON")

	plasmaManifestCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit JSON instead of a summary")

	plasmaListCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit JSON instead of a table")

	plasmaPromoteCmd.Flags().StringVar(&plasmaFlags.name, "name", "", "agent name")
	plasmaPromoteCmd.Flags().StringVar(&plasmaFlags.hash, "hash", "",
		"bundle hash (default: whatever index.json currently points at)")
	plasmaPromoteCmd.Flags().IntVar(&plasmaFlags.minRuns, "min-runs", 0,
		"override [plasma] promote_min_runs")
	plasmaPromoteCmd.Flags().BoolVar(&plasmaFlags.force, "force", false,
		"skip the grade/runs gate (still records the grade)")
	plasmaPromoteCmd.Flags().BoolVar(&plasmaFlags.asJSON, "json", false, "emit JSON instead of text")

	plasmaCmd.AddCommand(plasmaCompileCmd, plasmaTestCmd, plasmaSampleCmd,
		plasmaManifestCmd, plasmaListCmd, plasmaPromoteCmd)
	rootCmd.AddCommand(plasmaCmd)
}

// failPlasma 把 error 映射成退出码：用法错 2、其余 1。
// 结果一律在 stdout，错误一律在 stderr —— 管道里 `| jq` 才能拿到干净的那一份。
func failPlasma(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "yagent plasma: %v\n", err)
	var ue *usageError
	if errors.As(err, &ue) {
		os.Exit(2)
	}
	os.Exit(1)
}

// runPlasmaWithLogging 给每个子命令套上"只写文件"的日志模式。
// ModeTUI 的语义正是 file-only（绝不污染 stdout/stderr），而 plasma 的结果要打到
// stdout —— 用 ModeHTTP 会往 stderr 写日志行。
func runPlasmaWithLogging(fn func() error) error {
	if err := logging.Init(logging.ModeTUI); err != nil {
		// 日志初始化失败不该挡住命令本身：结果是 stdout 上的那份，
		// 而日志只是排查用的旁证。
		fmt.Fprintf(os.Stderr, "yagent plasma: 日志初始化失败（继续）: %v\n", err)
	}
	defer logging.Close()
	return fn()
}

// plasmaConfigPath 是 plasma 子命令的配置路径：--config 优先，否则 ~/.yagent/config/config.toml。
//
// **刻意不调 getConfigPath()**：那个函数会 EnsureConfigExists（首次运行生成模板），
// 而 `yagent plasma list` 只是列一个目录。读配置不能有写文件的副作用。
func plasmaConfigPath() string {
	if configPathFlag != "" {
		return configPathFlag
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home + "/.yagent/config/config.toml"
	}
	return "config/config.toml"
}

// plasmaRuntime 读配置并返回 [plasma] 与整份 config。
//
// 容错是刻意的：manifest / list / promote 不需要 LLM，而一台还没配 provider 的机器上
// llm.LoadConfig 必然失败（validate 要求 use_provider）。那时按程序默认值继续，
// 并把原因写到 stderr —— 否则"没配 API key"会让一个纯本地命令直接死。
//
// 注意失败时仍读 GlobalPlasma()：LoadFromFile 在 validate 之前就调 SetGlobalPlasma，
// 所以 [plasma] 段可能已经生效；退回 DefaultPlasmaConfig() 会把用户显式写的值丢掉。
func plasmaRuntime() (config.PlasmaConfig, *config.Config) {
	path := plasmaConfigPath()
	cfg, err := llm.LoadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"yagent plasma: 配置文件不可用，按程序默认值继续（%s）: %v\n", path, err)
	}
	return config.GlobalPlasma(), cfg
}
