// 列表型表面（对话框与引导向导）共用的排版原语。
//
// 这些函数原先是 components 包内私有的（dialog_list.go）。引导向导也需要同一套
// 尺寸推导、滚动窗口与列宽分配，而"两份同形实现"正是本仓库点名的病
// （AGENTS.md §11 坑 18：只有一边漂走时聚合测试是绿的）。所以搬到这里，
// 两边共用一份；components 那边只留别名。
package common

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// PanelWidths 由终端宽度推出面板宽度与内容宽度。
// 面板 = min(maxWidth, termWidth-4)；内容 = 面板-6（边框 2 + 内边距 4），
// 下限 minWidth —— 再窄就无法保证相邻两列还能区分彼此。
func PanelWidths(maxWidth, minWidth, termWidth int) (panelWidth, innerWidth int) {
	panelWidth = min(maxWidth, termWidth-4)
	if panelWidth < 1 {
		panelWidth = 1
	}
	return panelWidth, max(panelWidth-6, minWidth)
}

// ListBudget 返回列表可占用的行数，以及是否渲染上下滚动指示行。
// 终端高度扣除 chrome 行后受 maxRows 限制；需要滚动且扣掉指示行后仍有余量时
// 预留 2 行，否则放弃指示行 —— 保证表面永远不会高出终端。
func ListBudget(chrome, maxRows, height, needed int) (rows int, showMore bool) {
	rows = max(min(height-chrome, maxRows), 1)
	if needed <= rows {
		return rows, false
	}
	if rows-2 >= 1 {
		return rows - 2, true
	}
	return rows, false
}

// WindowRange 返回可见区间的 [start, end)，focus 所在行尽量居中。
func WindowRange(n, focus, rows int) (start, end int) {
	rows = max(rows, 1)
	if n <= rows {
		return 0, n
	}
	start = min(max(focus-rows/2, 0), n-rows)
	return start, start + rows
}

// MoreLine 渲染 "↑ N more" / "↓ N more" 滚动指示行，在内容宽度内居中。
func MoreLine(n int, up bool, innerWidth int, c ColorTokens) string {
	arrow := "↓"
	if up {
		arrow = "↑"
	}
	text := arrow + " " + strconv.Itoa(n) + " more"
	style := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	return strings.Repeat(" ", max((innerWidth-lipgloss.Width(text))/2, 0)) + style.Render(text)
}

// AllocateColumns 在总宽 avail 内分配两列：自然宽度放得下就原样使用，
// 放不下时按 2:3 分配（右列通常是更长的名字），每列至少 6 格。
func AllocateColumns(avail, leftNatural, rightNatural int) (int, int) {
	if avail < 12 {
		return max(avail/2, 1), max(avail-avail/2, 1)
	}
	if leftNatural+rightNatural <= avail {
		return leftNatural, rightNatural
	}
	left := avail * 2 / 5
	right := avail - left
	if left < 6 {
		left = 6
		right = avail - left
	}
	if right < 6 {
		right = 6
		left = avail - right
	}
	return left, right
}

// formatTokenShort 是 ContextLabel 的紧凑计数格式（k / m 后缀）。
func formatTokenShort(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fm", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ContextLabel 返回上下文窗口的紧凑文本（与 TUI 上下文进度条同一格式），未知时为 "-"。
func ContextLabel(n int) string {
	if n <= 0 {
		return "-"
	}
	return formatTokenShort(int64(n))
}

// PadLeft 左侧补空格至显示宽度 w（超出则原样返回）。
func PadLeft(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// Ellipsize 按显示宽度截断 s 并右侧补空格，使结果宽度恰为 w。
// 超长时中间省略（保留首尾），因为同前缀的名字靠尾部区分：
// 窄终端下 "siliconflow_ds4_flash" 与 "siliconflow_dsv32" 若只保留头部会完全一样。
func Ellipsize(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width := lipgloss.Width(s); width <= w {
		return s + strings.Repeat(" ", w-width)
	}
	runes := []rune(s)
	if w > 1 && len(runes) >= w {
		tail := (w - 1) / 2
		head := w - 1 - tail
		if middle := string(runes[:head]) + "…" + string(runes[len(runes)-tail:]); lipgloss.Width(middle) == w {
			return middle
		}
	}
	// 宽字符等导致中间省略对不齐时，退化为尾部省略
	for len(runes) > 0 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if width := lipgloss.Width(candidate); width <= w {
			return candidate + strings.Repeat(" ", w-width)
		}
	}
	return strings.Repeat(" ", w)
}
