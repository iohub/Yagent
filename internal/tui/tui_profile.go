package tui

// `:profile` / `/profile` —— 在 TUI 内切换 config 档（方案 A：只改"下次启动用哪个档"）。
//
// 这个文件刻意**不**碰运行期状态，这是整个设计的核心：
//   - 不重建 app（app.Init 被 sync.Once 包住，同一实例只能装配一次）；
//   - 不热切换配置（supervise.Root() 是进程级单例，eventCh 是一次性 cmd 且无重放队列）；
//   - 不 exec 重启进程（用户没要求，而我们也不该替他决定什么时候退出）。
//
// 于是"切换"在这里只有两件事：把选择写进 ~/.yagent/active_profile，然后**明确告诉
// 用户本次进程不变**。后半句不是客套话 —— 只说"已切换"会让人以为配置已经换了，
// 接下来几十轮对话都在用旧 provider，而他会以为是新配置没生效。

import (
	"fmt"
	"os"

	"yagent/internal/config"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// profileCommand 是命令入口：无参弹面板，有参直接切（`/profile work`）。
//
// 有参形态存在的原因与 `/plasma list` 一样：脚本化与"我知道我要什么"的用户不需要
// 经过一层浮层；两条路径最终走同一个 applyProfileSelection，判据不会分叉。
func (m *model) profileCommand(args []string) tea.Cmd {
	if len(args) > 0 {
		m.applyProfileSelection(args[0])
		return nil
	}

	options, err := m.profileOptions()
	if err != nil {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("读取配置档失败：%v", err),
			fmt.Sprintf("Cannot list config profiles: %v", err)))
		return nil
	}
	if len(options) == 0 {
		// 理论上不会走到：default 是内置档，profileOptions 无条件补它。
		// 保留这一支只是为了让"空列表"有个明确表现，而不是弹出一个空面板。
		m.setInfoMsg(profileText(m.currentLang,
			"没有可用的配置档",
			"No config profiles available"))
		return nil
	}

	d := components.NewProfileSelectDialog(options, components.Language(m.currentLang))
	// 入栈前必须 SetBounds：弹窗守卫会吞 WindowSizeMsg，漏设的话对话框永远按 0×0 渲染。
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// profileOptions 组装面板的行。
//
// 三条与 CLI `runProfileList` 一致的规则：
//   - 目录里有什么就列什么（ProfileNamesInDir）；
//   - default 是内置档，逻辑上永远存在 —— 它的 config.toml 还没生成时也要列出来，
//     否则面板看上去像"没有 default 这个东西"，而首次启动又会凭空造出一份；
//   - 名字来自磁盘，理论上都合法；构造路径失败的条目直接跳过，不让一行脏数据
//     毁掉整个面板。
func (m *model) profileOptions() ([]components.ProfileOption, error) {
	dir := config.ConfigDir()
	names, err := config.ProfileNamesInDir(dir)
	if err != nil {
		return nil, err
	}

	hasDefault := false
	for _, name := range names {
		if name == config.DefaultProfileName {
			hasDefault = true
			break
		}
	}
	if !hasDefault {
		names = append([]string{config.DefaultProfileName}, names...)
	}

	active := m.activeProfileName()
	options := make([]components.ProfileOption, 0, len(names))
	for _, name := range names {
		path, err := config.ProfileConfigPath(dir, name)
		if err != nil {
			continue
		}
		_, statErr := os.Stat(path)
		options = append(options, components.ProfileOption{
			Name:       name,
			Path:       path,
			Active:     name == active,
			NotCreated: os.IsNotExist(statErr),
		})
	}
	return options, nil
}

// activeProfileName 是**本次进程真正在用**的档。
//
// 首选 llm.Client.ConfigPath —— 那是运行期唯一的事实来源（main.go 把解析结果写进它，
// 三个 Persist* 也都落在它上面）。从路径反推而不是再解析一次链条，是因为
// --config 可以指向任意文件名，那时"解析链给出的档名"根本描述不了正在读的文件。
//
// ConfigPath 为空（构造失败、或某些测试直接造 client）时才退到偏好文件，再退 default。
// 这条兜底链与根包 resolveProfileName 的后半段一致，但 TUI 在 internal/ 里，
// 调不到根包那个函数 —— 所以这里只复刻"能看到的"两级，不含 flag 与环境变量。
func (m *model) activeProfileName() string {
	if m.assistant != nil {
		if client := m.assistant.GetClient(); client != nil && client.ConfigPath != "" {
			return config.ProfileNameFromPath(client.ConfigPath)
		}
	}
	if name, err := config.ReadActiveProfile(config.HomeDir()); err == nil && name != "" {
		return name
	}
	return config.DefaultProfileName
}

// applyProfileSelection 把选择写进持久化偏好，并如实说明生效时机。
//
// 它**只**写 ~/.yagent/active_profile：不改 m.com.Config、不改 client.ConfigPath、
// 不重建任何东西。任何"顺手把内存里的配置也换掉"的念头都会立刻退化成热切换 ——
// 工具引擎、六个 Agent、MCP、蓝图全部持有启动那一刻的配置与资源。
func (m *model) applyProfileSelection(raw string) {
	name := config.NormalizeProfileName(raw)
	if err := config.ValidateProfileName(name); err != nil {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("非法档名 %q：%v", raw, err),
			fmt.Sprintf("Invalid profile name %q: %v", raw, err)))
		return
	}

	dir := config.ConfigDir()
	path, err := config.ProfileConfigPath(dir, name)
	if err != nil {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("无法解析档 %q：%v", name, err),
			fmt.Sprintf("Cannot resolve profile %q: %v", name, err)))
		return
	}

	// 命名档必须已经存在。default 豁免：它是内置档，全新机器上它的 config.toml
	// 也常常还没生成，而"切回 default"必须是永远可用的逃生通道。
	if name != config.DefaultProfileName {
		if _, statErr := os.Stat(path); statErr != nil {
			if os.IsNotExist(statErr) {
				m.setInfoMsg(profileText(m.currentLang,
					fmt.Sprintf("档 %q 不存在 — 先在 shell 里运行 `yagent profile create %s`", name, name),
					fmt.Sprintf("profile %q not found — run `yagent profile create %s` in a shell", name, name)))
				return
			}
			m.setInfoMsg(profileText(m.currentLang,
				fmt.Sprintf("无法读取档 %q：%v", name, statErr),
				fmt.Sprintf("Cannot stat profile %q: %v", name, statErr)))
			return
		}
	}

	// old 必须在**写入之前**读：ConfigPath 为空时 activeProfileName 会回落到偏好文件，
	// 写完之后再问就会得到新档，于是提示变成"本次进程继续使用 work — 重启后使用 work"。
	current := m.activeProfileName()

	if err := config.WriteActiveProfile(config.HomeDir(), name); err != nil {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("写入配置档偏好失败：%v", err),
			fmt.Sprintf("Failed to save the profile preference: %v", err)))
		return
	}

	if name == config.DefaultProfileName {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("已切回 %s — 重启 yagent 生效（本次进程继续使用 %s）", name, current),
			fmt.Sprintf("Active profile set back to %s — restart yagent to apply (this process keeps using %s)", name, current)))
		return
	}
	m.setInfoMsg(profileText(m.currentLang,
		fmt.Sprintf("已切换为 %s — 重启 yagent 生效（本次进程继续使用 %s）", name, current),
		fmt.Sprintf("Active profile set to %s — restart yagent to apply (this process keeps using %s)", name, current)))
}

// profileText 按语言二选一。
//
// 不走 langManager.GetText：那条路要往 i18n.go 的 400 行词表里加键，而这两段文案
// 只在 profile 面板出现（`:profile` 是命令行命令，不是状态栏常驻文案）。直接二选一
// 反而更容易核对"中英两段说的是同一件事"。
func profileText(lang Language, zh, en string) string {
	if lang == LangChinese {
		return zh
	}
	return en
}
