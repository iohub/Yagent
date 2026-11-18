package config

// config profile（多配置档）：一个 profile 就是**一整份 config.toml**，切换 profile 等于换文件。
//
// 本文件只做"名字 → 路径"这件事，不读不写配置文件内容：路径解析的优先级
// （--config > --profile > $YAGENT_PROFILE > default）在根包 profile_resolve.go，
// 因为 flag 与环境变量只有根包看得见。这里保持叶子包身份（只依赖标准库 + util），
// 于是 internal/tui 也能安全调用路径规则做展示。
//
// 布局是**扁平式**（与 default_config.toml 的自述一致：没有目录层级，也就没有继承）：
//
//	<configDir>/config.toml   == default 档（老用户零迁移：这就是历史路径）
//	<configDir>/<name>.toml   == 命名档
//
// profile **不是** TOML 里的键：Config 结构与 validate() 完全不参与，profile 只决定
// "读/写哪个文件"。写回仍然打在被选中的那个路径上（llm.Client.ConfigPath），
// 因此 /model /route /timeout 天然落到当前档。

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// DefaultProfileName 是内置档名：它对应的就是历史路径 <configDir>/config.toml。
	// 老用户不带任何参数时走的就是它，因此它的行为必须与加 profile 之前逐字节一致。
	DefaultProfileName = "default"

	// defaultConfigFileName 与 reservedProfileName 一起钉住"命名档不能占用 default 的文件名"。
	defaultConfigFileName = "config.toml"
	// reservedProfileName 是**不合法**的 profile 名（不是保留档名）：`config` 会被映射成
	// config.toml，也就是 default 档本身。若放行，`profile delete config` 会删掉 default、
	// `--profile config` 会静默等价于 default —— 两种都得不到用户想要的结果。
	// 判据放在 ProfileConfigPath（路径层）而不是 ValidateProfileName（文法层）：
	// 文法只回答"这串字符能不能当名字"，而这里回答的是"这个名字有没有独立的文件"。
	reservedProfileName = "config"
)

// profileNameRe 是 profile 名的唯一判据。
//
// 首字符必须是字母或数字（挡掉 `.hidden` / `-x` / `_x`），其余只允许字母数字下划线连字符。
// 点号与路径分隔符因此完全不可能出现，`..` 与 `a/b` 在校验层就被挡掉 —— 这是防路径穿越的
// 主闸门，ProfileConfigPath 里的 Base 复核只是第二道。上限 63 是给文件名留余量。
var profileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// ValidateProfileName 校验一个 profile 名是否可以独立成文件。
//
// 它只看文法，不看存在性，也不做归一化：空串在这里是**错误**而不是"默认档"。
// 把空串当默认档会让 `yagent profile create ""` 变形成"创建 default"，
// 所以 NormalizeProfileName 只服务于"没指定"（flag/env 都空）这一种情形。
func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("profile name is empty (want 1-64 chars matching %s)", profileNameRe.String())
	}
	if !profileNameRe.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: want 1-64 chars, first [A-Za-z0-9], "+
			"rest [A-Za-z0-9_-] (no '/', '\\', '.' or leading '.'/'-')", name)
	}
	return nil
}

// NormalizeProfileName 把"可能没写"的名字归一成 default。
//
// trim 是刻意做的：`--profile " work"`（shell 里多了个空格）是可预见的误输入，
// 而空格在文件系统里是合法字符 —— 不 trim 就会造出一个带前导空格的怪文件，
// 且下次 `--profile work` 找不到它。trim 之后仍为空才算"没写"。
func NormalizeProfileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return DefaultProfileName
	}
	return name
}

// ProfileConfigPath 把 (配置目录, 档名) 映射成配置文件的绝对/相对路径。
//
// default（或空串）→ <configDir>/config.toml；其它 → <configDir>/<name>.toml。
// configDir 由调用方给（根包按 home 可用性决定，TUI 用 ConfigDir()），
// 这样本函数不碰 $HOME，测试可以自由注入临时目录。
func ProfileConfigPath(configDir, name string) (string, error) {
	if strings.TrimSpace(configDir) == "" {
		return "", fmt.Errorf("config dir is empty")
	}
	name = NormalizeProfileName(name)
	if err := ValidateProfileName(name); err != nil {
		return "", err
	}

	fileName := defaultConfigFileName
	if name != DefaultProfileName {
		if name == reservedProfileName {
			return "", fmt.Errorf("profile name %q is not usable: it maps to %s, "+
				"which is the built-in %q profile (pick another name)",
				name, defaultConfigFileName, DefaultProfileName)
		}
		fileName = name + ".toml"
	}

	path := filepath.Join(configDir, fileName)
	// 第二道闸门：文法已经排除了分隔符与点号，这里再确认结果确实落在 configDir 里。
	// 校验层永远可能被后续改动放松，而"越界写文件"的代价不是报错而是写到别处去。
	if filepath.Dir(path) != filepath.Clean(configDir) || filepath.Base(path) != fileName {
		return "", fmt.Errorf("refusing to build a config path that escapes %q: %q", configDir, path)
	}
	return path, nil
}

// ProfileNameFromPath 是 ProfileConfigPath 的反向推导，供 `profile list` 与启动日志、
// 失败提示展示"这份文件是哪个档"。
//
// config.toml → default；x.toml → x；其它扩展名 → 去掉扩展名。
// 它**不做**合法性校验：--config 可以指向任意文件名（`/tmp/my.conf` → `my.conf` 的 stem），
// 那一档在磁盘上真实存在，展示它比展示 "default" 更诚实。
func ProfileNameFromPath(path string) string {
	base := filepath.Base(filepath.Clean(path))
	if base == defaultConfigFileName {
		return DefaultProfileName
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// ProfileNamesInDir 列出目录里"可被 --profile 选中"的档名：default（若有 config.toml）
// 排首位，其余按字典序。
//
// 三条刻意规则：
//   - 目录不存在返回空列表而不是错误 —— `yagent profile list` 在全新机器上必须能跑；
//   - 只认 *.toml，于是 util.AtomicWrite 的中间态（`work.toml.1234.tmp`）不会被列出来；
//   - 名字过不了 ValidateProfileName 的文件跳过：它在磁盘上存在，但 `--profile` 永远选不中它，
//     列出来只会让人以为能用。
func ProfileNamesInDir(configDir string) ([]string, error) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read config dir %s: %w", configDir, err)
	}

	names := make([]string, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		if filepath.Ext(base) != ".toml" {
			continue
		}
		name := ProfileNameFromPath(base)
		if ValidateProfileName(name) != nil || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		// default 优先，其余字典序。写成"两者是否 default 不同时才比这个属性"，
		// 才是严格弱序：`if names[i]==default { return true }` 在 i、j 同为 default 时
		// 会同时说 a<b 与 b<a。
		if (names[i] == DefaultProfileName) != (names[j] == DefaultProfileName) {
			return names[i] == DefaultProfileName
		}
		return names[i] < names[j]
	})
	return names, nil
}
