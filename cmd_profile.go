package main

// `yagent profile` 子命令树：list / create / delete / path。
//
// 与 `yagent plasma` 同一套形状（命令树 + 非交互输出 + 退出码 2/1 区分），理由也一样：
// 这些命令**不装配 Agent**（不起 MCP、不连 codeseek、不进 TUI），它们只碰磁盘上的
// 几份 config.toml。带着半个内核去读一个目录名，失败模式会从"文件不存在"变成
// "codeseek 没能拉起"。
//
// 输出契约：结果一律 stdout，错误一律 stderr。`yagent profile path` 的输出因此可以直接
// 进 `$(...)`；`profile list` 的 `*` 标记是给人看的，脚本要路径请用 `profile path`。
//
// 退出码（与 plasma 同一套）：0 成功；2 = 用法/名字问题（改命令行就能修）；
// 1 = 这次操作失败（已存在、不存在、IO 错误）。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"yagent/internal/config"

	"github.com/spf13/cobra"
)

// profileFromFlag 是 `profile create --from` 的落点。
var profileFromFlag string

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Inspect and manage config profiles (one whole config.toml each)",
	Long: `A profile is a complete config.toml file — switching profiles switches the whole file.

Layout (flat, no inheritance):

  default            -> ~/.yagent/config/config.toml   (the historical path)
  <name>             -> ~/.yagent/config/<name>.toml

Select one with --profile/-P <name> or YAGENT_PROFILE; --config still wins over both.
With neither, ~/.yagent/active_profile decides (written by "yagent profile use <name>")
and it sits above nothing but the built-in default. Nothing is hot-reloaded — the
selection takes effect on the next launch.
Profile operations never re-encode a TOML file: create copies bytes, nothing is rewritten.`,
}

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List profiles; the one this process would use is marked with *",
	Args:    usageArgs(cobra.NoArgs),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileList(os.Stdout))
	},
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a profile (copied from default, or from --from, or from the built-in template)",
	Args:  usageArgs(cobra.ExactArgs(1)),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileCreate(os.Stdout, args[0], profileFromFlag))
	},
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile file (default is built in and cannot be deleted)",
	Args:  usageArgs(cobra.ExactArgs(1)),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileDelete(os.Stdout, args[0]))
	},
}

var profilePathCmd = &cobra.Command{
	Use:   "path [name]",
	Short: "Print the absolute path of a profile (no argument: the active one)",
	Args:  usageArgs(cobra.MaximumNArgs(1)),
	Run: func(cmd *cobra.Command, args []string) {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		failProfile(runProfilePath(os.Stdout, name))
	},
}

var profileUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Select the profile the next launch uses (persisted in ~/.yagent/active_profile)",
	Long: `Write <name> into ~/.yagent/active_profile, which sits right below $YAGENT_PROFILE in
the resolution chain:

  --config/-c > --profile/-P > $YAGENT_PROFILE > ~/.yagent/active_profile > default

"use default" deletes the file instead of writing "default": a missing file already
means "no preference", so the two states never diverge. Nothing is hot-reloaded —
the selected profile takes effect on the next launch.`,
	Args: usageArgs(cobra.ExactArgs(1)),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileUse(os.Stdout, args[0]))
	},
}

// runProfileUse 把 name 写成持久化偏好（下次启动生效）。
//
// 三条判据：
//   - 名字必须合法（用法错误 → 退出码 2）；
//   - 命名档的文件必须已存在（不存在 → 退出码 1，并提示先 create）——
//     "选中一个不存在的档"会让下次启动凭空生成一份模板，而用户以为切到了他的配置；
//   - default 一律合法且**不需要**文件存在：它是内置档，全新机器上它的 config.toml
//     也常常还没生成，而"切回 default"必须是永远可用的逃生通道。
//
// 刻意不写任何提示到 stderr 之外：结果一律 stdout（可脚本化），错误一律 stderr，
// 与 list/create/delete/path 同一条输出契约。
func runProfileUse(out io.Writer, name string) error {
	dir := profileConfigDir()

	// 与 create/delete 同一条：原样校验，空串是用法错误而不是 default。
	name = strings.TrimSpace(name)
	if err := config.ValidateProfileName(name); err != nil {
		return newUsageError("%v", err)
	}
	path, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		return newUsageError("%v", err)
	}

	if name != config.DefaultProfileName {
		exists, err := profileFileExists(dir, name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("profile %q does not exist: %s (run `yagent profile create %s` first)",
				name, path, name)
		}
	}

	if err := config.WriteActiveProfile(config.HomeDir(), name); err != nil {
		return err
	}

	if name == config.DefaultProfileName {
		// 成功语要说清"清掉了偏好"，否则 `use default` 的输出与 `use work` 一样，
		// 用户无法确认自己回到了内置档。
		fmt.Fprintf(out, "active profile cleared (back to %q) — takes effect on the next launch\n", name)
		return nil
	}
	fmt.Fprintf(out, "active profile set to %q (%s) — takes effect on the next launch\n", name, path)
	return nil
}

// failProfile 与 failPlasma 是同一条约定，只是前缀不同：*usageError → 2，其余 → 1。
// 刻意不复用 failPlasma —— 那会把 "yagent plasma: " 前缀打在这个命令的错误上。
func failProfile(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "yagent profile: %v\n", err)
	if isUsageError(err) {
		os.Exit(2)
	}
	os.Exit(1)
}

// isUsageError 报告 err 是否属于"改命令行就能修好"的那一类（契约退出码 2）。
//
// 单独抽出来是给 main 用的：flag 解析错误（`profile create -x`）发生在 Run 之前，
// 到不了 failProfile，只能从 rootCmd.Execute() 的返回值里翻。
func isUsageError(err error) bool {
	var ue *usageError
	return errors.As(err, &ue)
}

// usageArgs 把 cobra 的位置参数校验器包成用法错误。
//
// `profile create`（少给名字）与 `profile create -x`（flag 写错）是同一类失败 —— 改命令行就能
// 修，契约退出码 2 —— 但 cobra 走的是两个机制（Args 校验 / FlagErrorFunc），所以两边各包一次。
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return newUsageError("%v", err)
		}
		return nil
	}
}

// activeProfileName 是"本次进程真正会用哪个档"。
//
// 有 --config 时从它的路径反推（`--config /tmp/team.toml` → `team`），因为那份文件才是
// 真正会被读写的；否则走与启动完全相同的优先级
// （--profile → $YAGENT_PROFILE → ~/.yagent/active_profile → default）。
//
// 它**不需要**为 active_profile 加任何代码：resolveProfileName 已经带上第 4 级，
// 于是 `profile list` 的 `*` 自动跟随 `profile use` 的选择。加一级偏好却忘了同步
// `*` 标记，用户会看到"切了档但列表还标着旧档"—— 那种分叉靠复用同一条链条避免。
func activeProfileName() (string, error) {
	if configPathFlag != "" {
		return config.ProfileNameFromPath(configPathFlag), nil
	}
	return resolveProfileName()
}

// runProfileList 打印目录里所有可用的档。
//
// default 是**内置档**，逻辑上永远存在：它的 config.toml 还没生成（全新机器）时也必须
// 列出来，否则 `profile list` 看上去像"没有 default 这个东西"，而首次启动又会凭空造出
// 一份 config.toml —— 那是最容易被当成 bug 的落差。
//
// 只对 default 做这个特判：命名档没有"内置"身份，文件不在就是不在，凭空列出来只会
// 让人以为 `--profile <name>` 能用。补默认项是本函数的展示决定，
// config.ProfileNamesInDir 保持原语义（它回答的是"磁盘上有什么文件"）。
func runProfileList(out io.Writer) error {
	dir := profileConfigDir()
	names, err := config.ProfileNamesInDir(dir)
	if err != nil {
		return err
	}

	active, err := activeProfileName()
	if err != nil {
		return err
	}

	defaultCreated, err := profileFileExists(dir, config.DefaultProfileName)
	if err != nil {
		return err
	}
	if !defaultCreated && !containsProfile(names, config.DefaultProfileName) {
		names = append([]string{config.DefaultProfileName}, names...)
	}

	for _, name := range names {
		path, err := config.ProfileConfigPath(dir, name)
		if err != nil {
			return err
		}
		marker := " "
		if name == active {
			marker = "*"
		}
		// 只有 default 会带上这条标注：Fprintf 的 %-16s 对齐不受影响，
		// 因为标注接在路径后面。
		note := ""
		if name == config.DefaultProfileName && !defaultCreated {
			note = " (not created yet — run `yagent setup`)"
		}
		fmt.Fprintf(out, "%s %-16s %s%s\n", marker, name, path, note)
	}
	return nil
}

// containsProfile 判断档名是否已在列表里：`default.toml` 这种文件会被
// ProfileNamesInDir 归成 default，于是列表里可能已经有它，不能再补一行。
func containsProfile(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// profileFileExists 报告某个档的文件是否已经落盘。
//
// 只回答"存在吗"：目录不存在也算不存在（全新机器就是这么来的），
// 其它 IO 错误（权限、路径过长）照样上报 —— 把它们当成"不存在"会让我们
// 去覆盖一份读不到的文件。
func profileFileExists(dir, name string) (bool, error) {
	path, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return false, nil
}

// runProfileCreate 建一份新档：名字必须合法且文件尚不存在，绝不覆盖既有档。
//
// 全部判定都在 config.CreateProfile —— CLI 与 TUI 的 `:profile create` 因此共用同一份
// 实现。两份逻辑第一次分叉总发生在边角规则上（"default 的 config.toml 还没生成时算不算
// 源不存在"是最容易分叉的一条），而那种分叉不报错，只让两个入口对同一条命令给出不同结果。
//
// 这里只剩两件 CLI 专属的事：把"改命令行就能修好"的错误映射成退出码 2，以及输出契约 ——
// 成功才往 stdout 写一行，失败时一个字节都不写（这行会被脚本读）。
func runProfileCreate(out io.Writer, name, from string) error {
	name = strings.TrimSpace(name)
	path, source, err := config.CreateProfile(profileConfigDir(), name, from)
	if err != nil {
		return profileCreateError(err)
	}
	fmt.Fprintf(out, "created profile %q (%s): %s\n", name, profileCreateNote(source, from), path)
	return nil
}

// profileCreateNote 说明这份新档的内容从哪来。
//
// 括号里那句话是用户唯一能确认"抄的是哪一份"的凭据，所以三条来源各有一句固定措辞：
// "copied from %q"、"from the built-in template"，以及 `--from default` 在全新机器上的
// 回落说明（"default has not been created yet; used the built-in template"）——
// 后者必须点名 default，否则用户会以为自己抄到的是一份空配置。
func profileCreateNote(source, from string) string {
	if source != "" {
		return fmt.Sprintf("copied from %q", source)
	}
	if strings.TrimSpace(from) == config.DefaultProfileName {
		return "default has not been created yet; used the built-in template"
	}
	return "from the built-in template"
}

// profileCreateError 把 create 的失败分成两类：改命令行就能修的（退出码 2），与这次
// 操作失败的（退出码 1）。分类判据由错误自己携带（*config.ProfileUsageError），
// 不靠匹配错误文本 —— 文本为了可读性随时会改，而退出码会被脚本依赖。
func profileCreateError(err error) error {
	var usage *config.ProfileUsageError
	if errors.As(err, &usage) {
		return newUsageError("%v", err)
	}
	return err
}

// runProfileDelete 删一份档。三条判据：default 拒删、不存在报错、删当前档只警告。
//
// "删当前档仍然执行"是刻意的：拒绝删除会让人以为 profile 被进程锁住了，而实际上
// 下次启动会自动生成一份新的 default —— 警告说清后果比拦住更诚实。
func runProfileDelete(out io.Writer, name string) error {
	dir := profileConfigDir()

	// 与 create 同一条：原样校验，空串是用法错误而不是 default。
	name = strings.TrimSpace(name)
	if err := config.ValidateProfileName(name); err != nil {
		return newUsageError("%v", err)
	}
	if name == config.DefaultProfileName {
		return newUsageError("refusing to delete %q: it is the built-in profile that owns config.toml", name)
	}
	target, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		return newUsageError("%v", err)
	}

	if _, statErr := os.Stat(target); statErr != nil {
		if os.IsNotExist(statErr) {
			return fmt.Errorf("profile %q does not exist: %s", name, target)
		}
		return fmt.Errorf("stat %s: %w", target, statErr)
	}

	if active, err := activeProfileName(); err == nil && active == name {
		fmt.Fprintf(os.Stderr,
			"yagent profile: warning: %q is the active profile — the next launch falls back to %q\n",
			name, config.DefaultProfileName)
	}

	if err := os.Remove(target); err != nil {
		return fmt.Errorf("delete %s: %w", target, err)
	}
	fmt.Fprintf(out, "deleted profile %q (%s)\n", name, target)
	return nil
}

// runProfilePath 打印绝对路径。无参数时打印"本次进程会加载的那份"。
//
// 不检查文件是否存在：它的用途是脚本化（`cd $(dirname $(yagent profile path work))`），
// 而"存在性"是 create/delete 的事。
func runProfilePath(out io.Writer, name string) error {
	var path string
	if name == "" {
		resolved, _, err := resolveConfigPath(false)
		if err != nil {
			return err
		}
		path = resolved
	} else {
		name = strings.TrimSpace(name)
		if err := config.ValidateProfileName(name); err != nil {
			return newUsageError("%v", err)
		}
		resolved, err := config.ProfileConfigPath(profileConfigDir(), name)
		if err != nil {
			return newUsageError("%v", err)
		}
		path = resolved
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve absolute path for %s: %w", path, err)
	}
	fmt.Fprintln(out, abs)
	return nil
}

func init() {
	profileCreateCmd.Flags().StringVar(&profileFromFlag, "from", "",
		"copy an existing profile instead of the built-in template")

	// flag 解析错误（`profile create -x`）由 cobra 在参数解析阶段抛出，绕过了名字校验层，
	// 于是退出码落成 1 —— 与"用法问题退 2"的契约（failProfile 里那套）不符。
	// 包成 *usageError 之后，main 从 rootCmd.Execute() 的返回值里把它翻成 2。
	//
	// cobra 的 FlagErrorFunc 沿父链查找，设在 profileCmd 上就覆盖 list/create/delete/path。
	// 刻意不设到 rootCmd：那会顺手改掉 plasma 等既有子命令的退出码行为。
	profileCmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return newUsageError("%v", err)
	})

	profileCmd.AddCommand(profileListCmd, profileCreateCmd, profileDeleteCmd, profilePathCmd, profileUseCmd)
	rootCmd.AddCommand(profileCmd)
}
