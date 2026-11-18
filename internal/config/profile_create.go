package config

// profile 的"新建"语义：一个档就是一整份 config.toml，新建 = 把某处已有的字节抄过来。
//
// 这段逻辑住在叶子包而不是根包，是因为它有**两个**调用方：CLI 的
// `yagent profile create` 与 TUI 的 `:profile create`。两份实现的第一次分叉通常发生在
// 边角规则上（"default 的 config.toml 还没生成时算不算源不存在"是最典型的一条），
// 而那种分叉不报错，只让两个入口对同一条命令给出不同结果。
//
// 本文件不打印任何东西、不写 active_profile、不感知 flag 与环境变量：
// 文案是调用方的事，"选中哪个档"是 resolve 链（根包 profile_resolve.go）与
// WriteActiveProfile 的事。复制一律读字节 + util.AtomicWrite —— 一次 TOML 编解码
// 就会把模板里那些自我说明的注释整份蒸发，而它们正是这份文件唯一的文档。

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"yagent/internal/util"
)

var (
	// ErrProfileExists 表示目标档已经存在。create **绝不覆盖** —— 覆盖一份用户可能
	// 正在用的配置，比让他自己先 delete 再 create 糟得多。
	ErrProfileExists = errors.New("profile already exists")

	// ErrProfileSourceMissing 表示 from 指向的命名档没有文件。
	// default 刻意不适用：它是内置档，文件不在时回落到内嵌模板（见 CreateProfile）。
	ErrProfileSourceMissing = errors.New("profile source does not exist")
)

// ProfileUsageError 标记"改命令行就能修好"的那一类失败：非法档名、保留名（config）、
// 同档自拷。它只表达**语义**，不表达退出码（根包 failProfile 才决定 2 还是 1）——
// 但语义必须由错误自己携带，否则调用方只能靠匹配错误文本来分类，
// 而错误文本是会为了可读性改的。
type ProfileUsageError struct{ Err error }

func (e *ProfileUsageError) Error() string { return e.Err.Error() }

// Unwrap 让 errors.Is/As 能穿透到内层（例如 ValidateProfileName 的错误）。
func (e *ProfileUsageError) Unwrap() error { return e.Err }

// profileUsage 把 err 包成用法错误，nil 安全。
func profileUsage(err error) error {
	if err == nil {
		return nil
	}
	return &ProfileUsageError{Err: err}
}

// CreateProfile 按 name 新建一份配置档，返回落盘路径与被复制的来源。
//
// 返回值契约：
//   - path 是新建文件的路径（由 ProfileConfigPath 决定，落在 configDir 里）；
//   - source 供调用方拼文案：来源档名（含 "default"），**空串**表示"用了内嵌模板"。
//
// 来源三档，与 `yagent profile create` 的历史行为逐字一致：
//  1. from 非空 → 抄那一份；
//  2. from 为空且 default 的 config.toml 存在 → 抄 default（用户看到的那份设置
//     最可能是他想要的起点）；
//  3. 其余（含 from == "default" 但 config.toml 还没生成）→ 内嵌模板。
//
// 第 3 条里对 default 的豁免是刻意的：default 是**内置档**，"它的文件还没生成"
// 不等于"源不存在"。同一处规则还有一条反面：走模板时**不**顺手生成 default 的
// config.toml —— create 一旦会创建 default，它就成了半个 setup。
//
// 判定顺序也是契约的一部分：目标已存在的判定**先于**来源解析，于是
// `create work from work`（work 已存在）报的是"已存在"而不是"同档自拷"。
// 反过来，from 为空时的隐式来源是 default：`create default`（全新机器）因此等同于
// "把模板落成 config.toml"，而不是自拷错误 —— 自拷只对**显式** from 成立。
func CreateProfile(configDir, name, from string) (path, source string, err error) {
	name = strings.TrimSpace(name)
	// 原样校验，不经 NormalizeProfileName：空串在这里是"用户写错了"，
	// 归一成 default 会静默改写历史档。
	if err := ValidateProfileName(name); err != nil {
		return "", "", profileUsage(err)
	}
	target, err := ProfileConfigPath(configDir, name)
	if err != nil {
		// 保留名 config 与越界路径都在这里：它们同样是"改命令行就能修好"。
		return "", "", profileUsage(err)
	}

	// 绝不覆盖既有档。
	if _, statErr := os.Stat(target); statErr == nil {
		return "", "", fmt.Errorf("%w: %q (%s)", ErrProfileExists, name, target)
	} else if !os.IsNotExist(statErr) {
		return "", "", fmt.Errorf("stat %s: %w", target, statErr)
	}

	explicitFrom := strings.TrimSpace(from)
	effective := explicitFrom
	if effective == "" {
		effective = DefaultProfileName
	}
	src, err := ProfileConfigPath(configDir, effective)
	if err != nil {
		return "", "", profileUsage(fmt.Errorf("from: %w", err))
	}
	if explicitFrom != "" && src == target {
		// 同档自拷只会让人以为"复制了一份备份"，实际什么都没发生。
		return "", "", profileUsage(fmt.Errorf("source profile %q is the profile being created", effective))
	}

	if _, statErr := os.Stat(src); statErr == nil {
		data, readErr := os.ReadFile(src)
		if readErr != nil {
			return "", "", fmt.Errorf("read source profile %q (%s): %w", effective, src, readErr)
		}
		if writeErr := writeProfileBytes(target, data); writeErr != nil {
			return "", "", writeErr
		}
		return target, effective, nil
	} else if !os.IsNotExist(statErr) {
		// 读不到 ≠ 不存在：把权限错误当成"源缺失"会让我们给出一份模板，
		// 而用户以为那是他的配置的副本。
		return "", "", fmt.Errorf("stat %s: %w", src, statErr)
	}

	// 源文件确实不在。只有 default 允许回落模板；命名档没有这层豁免。
	if effective != DefaultProfileName {
		return "", "", fmt.Errorf("%w: %q (%s)", ErrProfileSourceMissing, effective, src)
	}
	// 走首启同一条路径：内嵌模板 + 目录 0755 + 文件 0600 + 原子写。
	// 直接复用 EnsureConfigExists，不自己写盘。
	if _, err := EnsureConfigExists(target); err != nil {
		return "", "", fmt.Errorf("create profile %q: %w", name, err)
	}
	return target, "", nil
}

// writeProfileBytes 与 EnsureConfigExists 同规格：目录 0755 / 文件 0600 / 原子写。
//
// 0600 不是形式主义 —— profile 里同样会有明文 api_key，而 config.toml 常被人
// 整份贴进提问里。
func writeProfileBytes(path string, data []byte) error {
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
