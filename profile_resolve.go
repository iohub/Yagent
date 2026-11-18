package main

// config profile 的**路径解析**：优先级、默认值、以及"要不要顺手创建"。
//
// 放在根包而不是 internal/config：判据的来源是 flag 与环境变量，而它们只存在于这里；
// internal/config 只回答"名字→路径"（profile.go），两处合起来才是一条完整规则。
//
// 优先级（高 → 低）：
//  1. --config/-c <path>   原样使用，**永不自动创建**（与加 profile 之前完全一致）
//  2. --profile/-P <name>  命名档 → <configDir>/<name>.toml
//  3. $YAGENT_PROFILE      空值视为未设置
//  4. ~/.yagent/active_profile  用户上次在 TUI / `yagent profile use` 里选定的档
//  5. 都没有               → default → <configDir>/config.toml（历史路径）
//
// 第 4 级的两种失败**都不算错误**：文件缺失是向前兼容的主路径（老用户根本没有它），
// 内容非法则是"用户手改了一份文本文件"，warn 一句降级到 default 即可 —— 让一份坏掉的
// 偏好把 yagent 卡死在启动失败上，代价远大于它带来的信息量。
//
// $HOME 可用时 configDir = ~/.yagent/config；拿不到 home 才退 CWD 相对的 config/
// —— 这一条不是"第二级查找"，是 home 不可用时的兜底（与历史行为一致）。

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"yagent/internal/config"
)

// profileEnvVar 是 profile 的环境变量名。只认非空值：`YAGENT_PROFILE=` 等于没设，
// 否则一个空导出就会把"没指定"变成"非法名字"。
const profileEnvVar = "YAGENT_PROFILE"

// resolveProfileName 返回本次进程要用的档名：
// --profile → $YAGENT_PROFILE → ~/.yagent/active_profile → default。
//
// 名字非法时返回 *usageError（退出码 2）：这类错误的修法是"改命令行/改环境变量"，
// 与"这次操作失败"（1）不是一回事。--config 同时给出时本函数**不被调用**，
// 因为那时 profile 被忽略，一个写错的 --profile 不该让 --config 也起不来。
//
// 偏好文件（第 4 级）**不产生 usageError**：用户没在命令行写错任何东西，
// 让一份手改坏的文本文件把 CLI 的退出码变成 2 是错误归因。
func resolveProfileName() (string, error) {
	var name, source string
	switch {
	case profileFlag != "":
		name, source = profileFlag, "--profile"
	case os.Getenv(profileEnvVar) != "":
		name, source = os.Getenv(profileEnvVar), profileEnvVar
	default:
		// 偏好文件读取是在**默认分支**里做的，而不是先在 switch 之前读一遍：
		// 前三级命中时连文件都不该碰（一个读失败也不该在 --profile 生效时冒出来）。
		return persistedProfileName(), nil
	}

	name = config.NormalizeProfileName(name)
	if err := config.ValidateProfileName(name); err != nil {
		return "", newUsageError("%s: %v", source, err)
	}
	return name, nil
}

// persistedProfileName 读第 4 级：~/.yagent/active_profile。
//
// 任何失败（文件读不了、内容过不了文法）都降级成 default，只留一条 slog.Warn。
// 降级在这里是**唯一**的处置：那份文件是纯文本，用户完全可能手改错，而
// "启动不了"与"忽略一次偏好"的代价差着两个数量级。
//
// 名字的校验由 config.ReadActiveProfile 做完（它返回的要么是空串，要么是合法名字），
// 所以这里不需要再验一遍 —— 别在校验层加第二份判据。
func persistedProfileName() string {
	name, err := config.ReadActiveProfile(configHomeDir())
	if err != nil {
		slog.Warn("ignoring unusable active_profile preference, falling back to "+config.DefaultProfileName,
			"error", err, "path", config.ActiveProfilePath(configHomeDir()))
		return config.DefaultProfileName
	}
	if name == "" {
		// 没有偏好文件 / 内容为空：这是**绝大多数用户**走的那条路，不 warn。
		return config.DefaultProfileName
	}
	return name
}

// profileConfigDir 是 profile 文件的目录：$HOME/.yagent/config，home 不可用时退 config/。
//
// 直接转调 config.ConfigDir()：两份实现曾经逐字节相同，但"相同"是靠人肉同步维系的。
// 一旦有一边改了兜底路径，`yagent profile path`（走这里）与 TUI 的档列表（走
// config.ConfigDir）就会指向不同目录 —— 那种 bug 的表现是"面板里能看到、启动时读不到"。
func profileConfigDir() string {
	return config.ConfigDir()
}

// resolveConfigPath 是配置路径的**唯一**解析点（主启动与 plasma 共用，避免两套优先级分叉）。
//
// createIfMissing 决定"缺文件时要不要用内嵌模板生成"：
//   - 主启动传 true：老行为（首启自动生成模板并进引导页）；
//   - plasma 传 false：读配置不能有写文件的副作用（`yagent plasma list` 只列目录）；
//   - --config 分支无论传什么都**不创建**：显式路径写错就该失败，而不是在原地造一个文件。
//
// created 只可能来自 config.EnsureConfigExists，首启判据（引导页）靠它。
func resolveConfigPath(createIfMissing bool) (path string, created bool, err error) {
	if configPathFlag != "" {
		warnProfileIgnored()
		return configPathFlag, false, nil
	}

	name, err := resolveProfileName()
	if err != nil {
		return "", false, err
	}

	path, err = config.ProfileConfigPath(profileConfigDir(), name)
	if err != nil {
		// 落在这一行的现实错误只有"名字或环境变量不合法/与 default 的文件名冲突"，
		// 都是用法问题 → 退出码 2。
		return "", false, newUsageError("%v", err)
	}
	if !createIfMissing {
		return path, false, nil
	}

	created, err = config.EnsureConfigExists(path)
	if err != nil {
		return "", false, fmt.Errorf("无法创建配置文件 %s: %w", path, err)
	}
	return path, created, nil
}

// warnProfileIgnored 在 --config 与 --profile/$YAGENT_PROFILE 同时出现时把"你选的档被忽略了"
// 说出来。
//
// 两条通道都写：slog 进日志文件（排查历史用），stderr 给人看（启动早期终端还没被 TUI 接管，
// 此时写一行是安全的）。只写 slog 的后果是用户以为切了档，实际还在用 --config 那份文件。
func warnProfileIgnored() {
	ignored := make([]string, 0, 2)
	if profileFlag != "" {
		ignored = append(ignored, "--profile "+profileFlag)
	}
	if env := os.Getenv(profileEnvVar); env != "" {
		ignored = append(ignored, profileEnvVar+"="+env)
	}
	if len(ignored) == 0 {
		return
	}

	ignoredList := strings.Join(ignored, ", ")
	detail := fmt.Sprintf("%s ignored because --config %s is set", ignoredList, configPathFlag)
	slog.Warn("--config overrides config profile", "config_path", configPathFlag, "ignored", ignoredList)
	fmt.Fprintf(os.Stderr, "yagent: %s\n", detail)
}
