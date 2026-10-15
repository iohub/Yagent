// 本文件维护 TUI 的按键翻译表（[tui.keybindings] → 消费点里的规范键名）。
//
// 语义是**替换**而不是追加：规范键名（"alt+s" 这些）是各 switch 分支的字面量，
// 所以翻译表把"按下的键"映射到"规范键"，被换掉的默认键映射到空串 = 解绑。
// 旧实现只有 editKeyMap[custom] = default 这一半，后果是自定义键能用、
// 默认键也照常生效、而且永远释放不掉 —— 用户以为把 quit 挪走了，ctrl+c 还在退程序。
package tui

import (
	"fmt"
	"log/slog"

	"yagent/internal/config"
)

// keyBinding 是一个"动作 = 它的规范键 + 用户想要的键"。
// custom 为空表示没配，此时规范键本身就是唯一绑定。
type keyBinding struct {
	def    string
	custom string
}

// keyUnbound 是"这个动作被显式解绑"的哨兵：翻译表里解绑存空串，
// 消费点把它换成一个永远匹配不上任何 case 的字符串，按键于是落到 default 分支。
const keyUnbound = "\x00unbound"

// buildKeyMap 生成一张 pressed-key → canonical-key 的翻译表。
// 冲突（两个动作抢同一个键）保留先声明的那个，其余归并成一条告警返回。
func buildKeyMap(bindings []keyBinding) (map[string]string, []string) {
	out := make(map[string]string, len(bindings)*2)
	owner := make(map[string]string, len(bindings))
	for _, b := range bindings {
		out[b.def] = b.def
		owner[b.def] = b.def
	}

	var conflicts []string
	for _, b := range bindings {
		if b.custom == "" || b.custom == b.def {
			continue
		}
		if held, taken := owner[b.custom]; taken {
			conflicts = append(conflicts, fmt.Sprintf("%q 已被 %q 占用（%q 想要它）", b.custom, held, b.def))
			continue
		}
		out[b.custom] = b.def
		owner[b.custom] = b.def
		// 让位：默认键解绑，否则"重映射"只是"再加一个别名"。
		out[b.def] = ""
		delete(owner, b.def)
	}
	if len(conflicts) > 0 {
		slog.Warn("[tui.keybindings] 键位冲突，按声明顺序保留先者", "conflicts", conflicts)
	}
	return out, conflicts
}

// translateKey 查表；解绑返回 keyUnbound，没在表里的键原样返回
// （没被任何动作声明的键不受影响，这是"只翻译注册过的键"的边界）。
func translateKey(keyMap map[string]string, pressed string) string {
	mapped, ok := keyMap[pressed]
	if !ok {
		return pressed
	}
	if mapped == "" {
		return keyUnbound
	}
	return mapped
}

func editKeyBindings(cfg *config.Config) []keyBinding {
	if cfg == nil {
		return nil
	}
	k := cfg.TUI.Keybindings.Edit
	return []keyBinding{
		{"alt+s", k.SubmitTask},
		{"ctrl+e", k.CommandMode},
		{"ctrl+h", k.ToggleHelp},
		{"ctrl+l", k.ToggleTimeline},
		{"alt+d", k.ToggleDashboard},
		{"ctrl+f", k.PageDown},
		{"ctrl+b", k.PageUp},
		{"ctrl+c", k.Quit},
		{"alt+m", k.SwitchModel},
		{"ctrl+t", k.NewTab},
		{"alt+[", k.PrevTab},
		{"alt+]", k.NextTab},
		{"alt+c", k.ClearSession},
		{"alt+w", k.CloseTab},
		{"alt+1", k.Tab1},
		{"alt+2", k.Tab2},
		{"alt+3", k.Tab3},
		{"alt+4", k.Tab4},
		{"alt+5", k.Tab5},
		{"alt+6", k.Tab6},
		{"alt+7", k.Tab7},
		{"alt+8", k.Tab8},
		{"alt+9", k.Tab9},
	}
}

func commandKeyBindings(cfg *config.Config) []keyBinding {
	if cfg == nil {
		return nil
	}
	k := cfg.TUI.Keybindings.Command
	return []keyBinding{
		{"j", k.ScrollDown},
		{"k", k.ScrollUp},
		{"f", k.PageDown},
		{"b", k.PageUp},
		{"i", k.EditMode},
		{"?", k.CmdToggleHelp},
		{"alt+t", k.ToggleTokenPanel},
		{"alt+d", k.ToggleDashboard},
		{"alt+m", k.SwitchModel},
		{"ctrl+c", k.Quit},
	}
}
