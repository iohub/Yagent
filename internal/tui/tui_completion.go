package tui

import (
	"log/slog"
	"strings"

	"yagent/internal/skills"
)

// completionKind 区分"内置命令"与"技能"：今天两者混在一个 []string 里，
// 靠显示层的硬编码特判区分，撞名时会静默吞掉技能。
type completionKind string

const (
	completionCmd   completionKind = "cmd"
	completionSkill completionKind = "skill"
)

// completionItem 是斜杠补全列表的一行。
type completionItem struct {
	name string
	kind completionKind
	desc string
}

// slashBuiltins 是 `/` 前缀认的内置命令（显示顺序即此序）。
// 必须与应用命令逐条对上：斜杠前缀默认是搜索，不在这里列出来的命令会变成搜字符串。
var slashBuiltins = []struct {
	name string
	desc string
}{
	{"history", "浏览历史任务"},
	{"resume", "恢复会话"},
	{"rs", "恢复会话（简写）"},
	{"pause", "暂停当前任务"},
	{"continue", "继续当前任务"},
	{"skills", "列出已加载的技能"},
	{"mcp", "查看 MCP server 状态"},
}

// skillCompletionItems 组装斜杠补全列表：内置命令在前，技能在后。
// 撞名时**内置命令胜**（与 TUI 键位抢键的既有规则同取向，根 AGENTS.md §11 坑 29），
// 并且只 warn 一次 —— 补全每敲一个字都会重建，每次 warn 会刷屏。
func skillCompletionItems(registry *skills.Registry, query string, warned map[string]bool) []completionItem {
	items := make([]completionItem, 0, len(slashBuiltins)+8)
	builtins := make(map[string]bool, len(slashBuiltins))
	for _, b := range slashBuiltins {
		builtins[b.name] = true
		if hasPrefixIgnoreCase(b.name, query) {
			items = append(items, completionItem{name: b.name, kind: completionCmd, desc: b.desc})
		}
	}
	if registry == nil {
		return items
	}

	for _, name := range registry.List() {
		if !hasPrefixIgnoreCase(name, query) {
			continue
		}
		if builtins[name] {
			if warned != nil && !warned[name] {
				warned[name] = true
				slog.Warn("skills: 技能名与内置命令撞名，补全里保留内置命令",
					"component", "tui", "name", name)
			}
			continue
		}
		desc := ""
		if skill, ok := registry.Get(name); ok {
			desc = skill.Description
		}
		items = append(items, completionItem{name: name, kind: completionSkill, desc: desc})
	}
	return items
}

// selectedCompletionItem 返回高亮的那一行（越界/关闭时 ok=false）。
func (m *model) selectedCompletionItem() (completionItem, bool) {
	if !m.skillAutoComplete || len(m.skillSuggestions) == 0 {
		return completionItem{}, false
	}
	if m.skillSuggestionIdx < 0 || m.skillSuggestionIdx >= len(m.skillSuggestions) {
		return completionItem{}, false
	}
	return m.skillSuggestions[m.skillSuggestionIdx], true
}

// splitSlashSkill 把 `/name rest of args` 拆成 (name, args)。
// 只认首个 token 精确命中技能名（见 processCommand）：技能名是 kebab-case，
// 搜索词通常不是，所以误判成技能的概率远低于漏判。
func splitSlashSkill(cmd string) (name, args string) {
	rest := strings.TrimPrefix(strings.TrimSpace(cmd), "/")
	name, args, _ = strings.Cut(rest, " ")
	name = strings.TrimSpace(name)
	return name, strings.TrimSpace(args)
}
