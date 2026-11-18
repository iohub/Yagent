package tui

// `:profile create` —— 在 TUI 内新建一份 config 档。
//
// 存在理由只有一条：面板里能**选**已有档，却一直不能**建**新档，想用一个新档必须切到
// shell 去跑 `yagent profile create`。在一个已经有完整浮层交互的功能里，这一步是断档。
//
// 三条不走样的规则：
//   - 真正建档的是 config.CreateProfile —— 与 CLI 共用同一份实现。这里只做参数解析、
//     文案与错误分类，"default 还没生成时算不算源不存在"这类规则只有一份；
//   - 建完**不**顺手选中（不写 active_profile）。用户可能只是想先建好再挑，而"创建即切换"
//     会让下次重启读进来的配置与他此刻在用的那份不同 —— 这种意外没有任何界面线索能提示；
//   - 不碰 m.com.Config / client.ConfigPath，与 applyProfileSelection 同一条铁律。
//
// 没有文本输入框：`:profile create <name>` 就是参数化命令行，与 `:profile work` 形态一致。
// 为此新增一个输入组件只为填一个档名，收益远不及它带来的焦点/粘贴/宽度一整套问题。

import (
	"errors"
	"fmt"

	"yagent/internal/config"
)

// parseProfileCreateArgs 解析 `create` 之后的参数。
//
// 只认两种形态（`from` 与 `--from` 等价，后者是为了照顾 CLI 肌肉记忆）：
//
//	create <name>
//	create <name> from|--from <src>
//
// 其余一律 ok=false：缺名字、缺源值（`create work from`）、多余参数
// （`create work from base extra`）、紧凑写法（`--from=base`）。
// 静默忽略多余 token 的后果是用户以为 `from base` 生效了、实际抄的是 default ——
// 这种事没有任何报错，只有"我的配置怎么不对"。
func parseProfileCreateArgs(args []string) (name, from string, ok bool) {
	if len(args) == 0 {
		return "", "", false
	}
	name = args[0]
	switch len(args) {
	case 1:
		return name, "", true
	case 3:
		if args[1] == "from" || args[1] == "--from" {
			return name, args[2], true
		}
	}
	return "", "", false
}

// createProfile 执行 `:profile create ...`。
func (m *model) createProfile(args []string) {
	name, from, ok := parseProfileCreateArgs(args)
	if !ok {
		m.setInfoMsg(profileCreateUsageText(m.currentLang))
		return
	}

	path, source, err := config.CreateProfile(config.ConfigDir(), name, from)
	if err != nil {
		m.setInfoMsg(m.profileCreateErrorText(name, err))
		return
	}

	// source == "" 是 config.CreateProfile 的返回值契约：用了内嵌模板（没有可复制的来源）。
	// 文案必须点明来源与路径，并给出**下一步** —— 建完不选中，所以不说清楚"怎么用上它"
	// 就等于让用户自己去猜。
	if source == "" {
		m.setInfoMsg(profileText(m.currentLang,
			fmt.Sprintf("已创建配置档 %s（内置模板）: %s — 用 :profile 选中它，重启 yagent 生效", name, path),
			fmt.Sprintf("created profile %q (built-in template): %s — select it with :profile; restart yagent to apply", name, path)))
		return
	}
	m.setInfoMsg(profileText(m.currentLang,
		fmt.Sprintf("已创建配置档 %s（复制自 %s）: %s — 用 :profile 选中它，重启 yagent 生效", name, source, path),
		fmt.Sprintf("created profile %q (copied from %q): %s — select it with :profile; restart yagent to apply", name, source, path)))
}

// profileCreateUsageText 是 `create` 的用法行。
//
// 错误参数一律回这一行而不是"Unknown command"：后者会让人以为 `create` 根本不存在，
// 而它只是参数写错了。
func profileCreateUsageText(lang Language) string {
	return profileText(lang,
		"用法：:profile create <name> [from <src>]（‹src› 省略时从 default 或内置模板复制）",
		"usage: :profile create <name> [from <src>] (omit <src> to copy default, or the built-in template)")
}

// profileCreateErrorText 把 config.CreateProfile 的失败翻成一句人能读的话。
//
// 分类判据来自错误本身（*config.ProfileUsageError / ErrProfileExists /
// ErrProfileSourceMissing），不匹配错误文本：文本为了可读性会改，而这里要区分的是
// "参数写错了"与"这次操作没成"两件不同的事 —— 尤其"已存在"必须说清**没有覆盖**它。
func (m *model) profileCreateErrorText(name string, err error) string {
	var usage *config.ProfileUsageError
	switch {
	case errors.As(err, &usage):
		return profileText(m.currentLang,
			fmt.Sprintf("非法档名或参数：%v — %s", err, profileCreateUsageText(m.currentLang)),
			fmt.Sprintf("invalid profile name or arguments: %v — %s", err, profileCreateUsageText(m.currentLang)))
	case errors.Is(err, config.ErrProfileExists):
		return profileText(m.currentLang,
			fmt.Sprintf("配置档 %s 已存在 — create 不会覆盖它（要重建请先 delete）", name),
			fmt.Sprintf("profile %q already exists — create never overwrites it (delete it first to recreate)", name))
	case errors.Is(err, config.ErrProfileSourceMissing):
		return profileText(m.currentLang,
			fmt.Sprintf("源配置档不存在（%v）— 先用 :profile 看看有哪些档", err),
			fmt.Sprintf("source profile does not exist (%v) — use :profile to see what exists", err))
	}
	return profileText(m.currentLang,
		fmt.Sprintf("创建配置档 %s 失败：%v", name, err),
		fmt.Sprintf("failed to create profile %q: %v", name, err))
}
