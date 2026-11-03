package common

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// 键帽行的配色沿用面板既有字面值（235/250/243/238），搬家不改外观。
var (
	hintKeycapStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("235")).Foreground(lipgloss.Color("250"))
	hintDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	hintSepStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
)

// HintDescStyle 给调用方复用同一种说明文字配色（补全面板等）。
func HintDescStyle() lipgloss.Style { return hintDescStyle }

// 提示条（键帽行）的唯一实现。
//
// 这条规则原先有五份各写一遍的拷贝：面板的 renderHintBar、补全面板的私有循环、
// 以及三个选择类对话框各自手写的"逐词缩短"梯子（"↑/↓ navigate · Enter select · Esc cancel"
// → "↑/↓ move · Enter · Esc" → "↑/↓ · Enter · Esc"）。五份表达的是同一件事：
// **宽度不够时从尾部整段丢弃，所以书写顺序就是优先级**。
// 手写梯子的病是"Esc 还活着"靠人记得把它写在最后面 —— 加一段提示就可能加在后面，
// 于是窄终端给出一个"看不出怎么离开这一屏"的界面。
// 定案：顺序反过来（退出通道写第一段），并让第一段在预算极窄时以**裸按键**幸存。

// HintSep 是提示条的分段符。整段丢弃以它为单位，所以不会出现半个按键。
const HintSep = " · "

// HintBar 把 "esc cancel · enter select" 渲染成键帽 + 说明序列，
// 总显示宽度不超过 budget。budget 太小放不下任何一段时返回空串。
func HintBar(hints string, budget int) string {
	return HintBarItems(strings.Split(hints, HintSep), HintSep, budget)
}

// HintBarItems 是同一件事的切片形态（补全面板用三个空格分隔，与 " · " 不是同一种
// 视觉密度，但"从尾部整段丢弃"这条规则必须只有一份实现）。
func HintBarItems(items []string, sep string, budget int) string {
	if budget < 4 {
		return ""
	}
	sepW := lipgloss.Width(sep)

	var b strings.Builder
	used := 0
	any := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		rendered := HintItem(item)
		cost := lipgloss.Width(rendered)
		if any {
			cost += sepW
		}
		if used+cost > budget {
			break
		}
		if any {
			b.WriteString(hintSepStyle.Render(sep))
		}
		b.WriteString(rendered)
		used += cost
		any = true
	}
	if any {
		return b.String()
	}
	// 一段完整提示都放不下：让**第一段**退化成裸按键再试一次。
	// 丢了说明文字用户还能认出那是个键；整行都不显示才是真的没有出口。
	if len(items) == 0 {
		return ""
	}
	head := strings.TrimSpace(items[0])
	key := head
	if i := strings.Index(head, " "); i > 0 {
		key = head[:i]
	}
	if bare := HintKeycap(key); lipgloss.Width(bare) <= budget {
		return bare
	}
	return ""
}

// HintItem 把 "esc cancel" 渲染成 [esc] cancel：首词是按键，其余是说明。
func HintItem(item string) string {
	key, desc, found := strings.Cut(item, " ")
	if !found {
		return HintKeycap(key)
	}
	return HintKeycap(key) + HintDescStyle().Render(" "+desc)
}

// HintKeycap 单独渲染一个按键帽（补全面板等只需要按键本身的场合用）。
func HintKeycap(key string) string { return hintKeycapStyle.Render(key) }
