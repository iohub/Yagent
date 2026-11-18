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
	"yagent/internal/util"

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
Profile operations never re-encode a TOML file: create copies bytes, nothing is rewritten.`,
}

var profileListCmd = &cobra.Command{
	Use:     "list",
	Aliases: []string{"ls"},
	Short:   "List profiles; the one this process would use is marked with *",
	Args:    cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileList(os.Stdout))
	},
}

var profileCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a profile (copied from default, or from --from, or from the built-in template)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileCreate(os.Stdout, args[0], profileFromFlag))
	},
}

var profileDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a profile file (default is built in and cannot be deleted)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		failProfile(runProfileDelete(os.Stdout, args[0]))
	},
}

var profilePathCmd = &cobra.Command{
	Use:   "path [name]",
	Short: "Print the absolute path of a profile (no argument: the active one)",
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		failProfile(runProfilePath(os.Stdout, name))
	},
}

// failProfile 与 failPlasma 是同一条约定，只是前缀不同：*usageError → 2，其余 → 1。
// 刻意不复用 failPlasma —— 那会把 "yagent plasma: " 前缀打在这个命令的错误上。
func failProfile(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "yagent profile: %v\n", err)
	var ue *usageError
	if errors.As(err, &ue) {
		os.Exit(2)
	}
	os.Exit(1)
}

// activeProfileName 是"本次进程真正会用哪个档"。
//
// 有 --config 时从它的路径反推（`--config /tmp/team.toml` → `team`），因为那份文件才是
// 真正会被读写的；否则走与启动完全相同的优先级（--profile → $YAGENT_PROFILE → default）。
func activeProfileName() (string, error) {
	if configPathFlag != "" {
		return config.ProfileNameFromPath(configPathFlag), nil
	}
	return resolveProfileName()
}

// runProfileList 打印目录里所有可用的档。
//
// 目录不存在（全新机器）时输出一行提示而不是错误：`profile list` 是"看看我有什么"，
// 空集合是合法答案。
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

	if len(names) == 0 {
		fmt.Fprintf(out, "(no profiles in %s — run `yagent setup` or `yagent profile create <name>`)\n", dir)
		return nil
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
		fmt.Fprintf(out, "%s %-16s %s\n", marker, name, path)
	}
	return nil
}

// runProfileCreate 建一份新档：名字必须合法且文件尚不存在，绝不覆盖既有档。
//
// 内容来源三档优先：--from <name> → default（存在时）→ 内嵌模板。
// 复制走读字节 + 原子写，不经 TOML 编解码：注释必须原样带过去。
func runProfileCreate(out io.Writer, name, from string) error {
	dir := profileConfigDir()

	// 先按原样校验：空串在这里是"用户写错了"，不能经 NormalizeProfileName 变成
	// "创建 default"（那会静默改写历史档）。
	name = strings.TrimSpace(name)
	if err := config.ValidateProfileName(name); err != nil {
		return newUsageError("%v", err)
	}
	target, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		return newUsageError("%v", err)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		return fmt.Errorf("profile %q already exists: %s", name, target)
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat %s: %w", target, statErr)
	}

	if from = strings.TrimSpace(from); from != "" {
		return copyProfile(out, dir, name, from, target)
	}

	// 没给 --from：default 存在就照抄（用户看到的那份设置最可能是他想要的起点）；
	// default 也没有（全新机器）就走首启同一条路径 —— 内嵌模板 + 0600。
	defaultPath, derr := config.ProfileConfigPath(dir, config.DefaultProfileName)
	if derr == nil {
		if _, statErr := os.Stat(defaultPath); statErr == nil {
			return copyProfile(out, dir, name, config.DefaultProfileName, target)
		}
	}
	if _, err := config.EnsureConfigExists(target); err != nil {
		return fmt.Errorf("create profile %q: %w", name, err)
	}
	fmt.Fprintf(out, "created profile %q (from the built-in template): %s\n", name, target)
	return nil
}

// copyProfile 把 from 档的字节抄到 target。
//
// 同档自拷直接拒绝：它只会让人以为"复制了一份备份"，实际什么都没发生。
func copyProfile(out io.Writer, dir, name, from, target string) error {
	src, err := config.ProfileConfigPath(dir, from)
	if err != nil {
		return newUsageError("--from: %v", err)
	}
	if src == target {
		return newUsageError("--from %q is the profile being created", from)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read source profile %q (%s): %w", from, src, err)
	}
	if err := writeProfileFile(target, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "created profile %q (copied from %q): %s\n", name, from, target)
	return nil
}

// writeProfileFile 与 config.EnsureConfigExists 同规格：目录 0755 / 文件 0600 / 原子写。
// 0600 不是形式主义：profile 里同样会有明文 api_key。
func writeProfileFile(path string, data []byte) error {
	if err := util.AtomicWrite(path, data, util.AtomicWriteOptions{
		DirPerm:   0o755,
		FilePerm:  0o600,
		TmpSuffix: ".tmp",
		Label:     "profile",
	}); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
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
	profileCmd.AddCommand(profileListCmd, profileCreateCmd, profileDeleteCmd, profilePathCmd)
	rootCmd.AddCommand(profileCmd)
}
