package config

// active_profile：用户**选定**的档，持久化成 ~/.yagent/active_profile（一行纯文本档名）。
//
// 它回答的问题只有一个："这次没说 --profile / $YAGENT_PROFILE 时该用哪个档"。
// 优先级链条在根包 profile_resolve.go（那里才看得见 flag 与环境变量）：
//
//	--config/-c > --profile/-P > $YAGENT_PROFILE > ~/.yagent/active_profile > default
//
// 为什么不复用既有落点：
//   - 不放 ~/.yagent/config/：那是 ProfileNamesInDir 的扫描域，落在那里的 *.toml 会被
//     当成一个可选的档。本文件名没有 .toml 后缀，即便被误放也列不出来 —— 后缀选择即为此。
//   - 不写 settings.json：~/.yagent/settings.json 的唯一读写方是
//     internal/tools/workspace_guard.go 的 saveProjectAuth，它把内容反序列化进只含
//     AuthorizedProjects 的匿名 struct 再整份覆盖 —— 新增的键会被无声抹掉。
//   - ~/.yagent/ 顶层已经是 settings.json / index.json / keywords.txt 的归宿。
//
// 语义：**文件不存在 == 没有偏好 == default**。这是向前兼容的关键 —— 老用户升级后
// 一个文件都没有，解析链必须照旧走 default，而不是报错。"选中 default"因此不是写一行
// "default"，而是删掉这个文件，让那条兼容路径得到复用。
//
// 本包保持叶子身份：只用标准库 + internal/util，不感知 flag 与环境变量。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"yagent/internal/util"
)

const (
	// activeProfileFileName 刻意不带扩展名：它不是一个配置档，只是一行档名。
	activeProfileFileName = "active_profile"
	// activeProfileDirPerm / activeProfileFilePerm 与其余落盘点同规格：目录 0755、文件 0600。
	// 0600 不是形式主义 —— 偏好文件本身不含密钥，但它与 config.toml 同源，权限口径统一
	// 才不会在"顺手往里面写点别的"时出错。
	activeProfileDirPerm  = 0o755
	activeProfileFilePerm = 0o600
)

// userHome 是 $HOME 的**唯一**读取点（本包内）。返回 ok=false 表示拿不到 home，
// 由调用方各自决定兜底路径 —— HomeDir 与 ConfigDir 的兜底并不相同。
func userHome() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", false
	}
	return home, true
}

// HomeDir 返回 yagent 的用户级根目录（~/.yagent）：settings.json、index.json、
// keywords.txt 与 active_profile 都住在这里（*不是* config/ 子目录）。
//
// 这是全仓唯一的实现：根包 main.go 的 configHomeDir 与 profile_resolve.go 的
// profileConfigDir 都转调本包（config.HomeDir / config.ConfigDir），TUI 也直接用
// config.HomeDir() —— 三处各写一遍 os.UserHomeDir 是"某个落点跑到别处去"的经典成因。
//
// home 不可用（几乎只发生在刻意构造的测试环境）时返回相对路径 ".yagent"，
// 与加 profile 之前的行为一致。
func HomeDir() string {
	if home, ok := userHome(); ok {
		return filepath.Join(home, ".yagent")
	}
	return ".yagent"
}

// ActiveProfilePath 返回偏好文件的路径。
//
// configHome 传空串表示"用默认 home"（即 HomeDir()）：这样调用方不必自己拼 ~/.yagent，
// 也就不会在测试或沙箱里拼错。传具体目录是为了让测试能注入临时目录。
func ActiveProfilePath(configHome string) string {
	if strings.TrimSpace(configHome) == "" {
		configHome = HomeDir()
	}
	return filepath.Join(configHome, activeProfileFileName)
}

// ReadActiveProfile 读回用户选定的档名。
//
// 三种结果：
//   - 文件不存在 → ("", nil)。**不是错误**：这是向前兼容的主路径（老用户没有这个文件）。
//   - 空文件/纯空白 → ("", nil)。`echo "" > active_profile` 不该让人卡在启动失败。
//   - 内容过不了 ValidateProfileName → 返回错误，由调用方决定忽略还是报错
//     （根包 profile_resolve.go 选择 warn + default，因为用户没在命令行写错任何东西）。
//
// 返回的名字已经 trim 过；它**已经**通过文法校验，调用方不需要再验一遍。
func ReadActiveProfile(configHome string) (string, error) {
	path := ActiveProfilePath(configHome)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read active profile %s: %w", path, err)
	}

	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", nil
	}
	if err := ValidateProfileName(name); err != nil {
		return "", fmt.Errorf("active profile %s holds an unusable name: %w", path, err)
	}
	return name, nil
}

// WriteActiveProfile 把 name 写成"下次启动用的档"。
//
// 两个刻意的规则：
//   - name == default 时**删除文件**而不是写一行 "default"。default 的语义就是
//     "没有偏好"，让"文件不存在 → default"这条兼容路径得到复用；否则磁盘上会长期存在
//     一个与缺失状态等价的文件，而两份等价事实迟早漂移。
//   - 落盘走 util.AtomicWrite（全仓唯一的 tmp+rename 实现），权限 0600。
//
// 它不检查该档的配置文件是否存在：那是"选一个档"与"这个档有没有内容"两件事，
// 默认档在全新机器上就还没有 config.toml，却必须能被选中。存在性判据在调用方
// （cmd_profile.go 的 runProfileUse 与 TUI 的 applyProfileSelection）。
func WriteActiveProfile(configHome, name string) error {
	name = strings.TrimSpace(name)
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	if name == DefaultProfileName {
		return ClearActiveProfile(configHome)
	}

	path := ActiveProfilePath(configHome)
	if err := util.AtomicWrite(path, []byte(name+"\n"), util.AtomicWriteOptions{
		DirPerm:   activeProfileDirPerm,
		FilePerm:  activeProfileFilePerm,
		TmpSuffix: ".tmp",
		Label:     "active_profile",
	}); err != nil {
		return fmt.Errorf("write active profile %s: %w", path, err)
	}
	return nil
}

// ClearActiveProfile 删除偏好文件；文件不存在视为成功（幂等，`profile use default`
// 在全新机器上必须不报错）。
func ClearActiveProfile(configHome string) error {
	path := ActiveProfilePath(configHome)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove active profile %s: %w", path, err)
	}
	return nil
}
