package components

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// 列表型对话框（ModelSelectDialog / AgentSelectDialog）共用的排版原语：
// 尺寸、行数预算、滚动窗口、列宽分配与文本截断。集中在一处可保证两个对话框
// 版式一致，也避免"提示文字换行撑破高度"这类溢出问题只修一半。

const (
	// dialogMaxWidth 对话框最大宽度，窄终端时由 View 收缩。
	dialogMaxWidth = 88
	// dialogMaxListRows 列表最多显示的行数，避免长列表占满整屏。
	dialogMaxListRows = 15
	// dialogQuickKeys 数字键直选覆盖的条目数（1-9）。
	dialogQuickKeys = 9
	// dialogListChrome 除列表外占用的行数：边框 2 + 内边距 2 + 标题上边距 1
	// + 标题 1 + 空行 2 + 表头 1 + 提示 1。
	dialogListChrome = 10
	// contextHeader 上下文列的表头，同时作为该列的最小宽度。
	contextHeader = "CONTEXT"
)

// dialogWidths 由终端宽度推出对话框宽度与内容宽度。
// 内容宽度下限 30：再窄就无法保证 provider/model 两列还能区分彼此。
func dialogWidths(termWidth int) (dialogWidth, innerWidth int) {
	dialogWidth = min(dialogMaxWidth, termWidth-4)
	return dialogWidth, max(dialogWidth-6, 30)
}

// listBudget 返回列表可占用的行数，以及是否渲染上下滚动指示行。
// 终端高度扣除固定行后受 dialogMaxListRows 限制；需要滚动且扣掉指示行后仍有余量时
// 预留 2 行，否则放弃指示行——保证对话框永远不会高出终端。
func listBudget(height, needed int) (rows int, showMore bool) {
	rows = max(min(height-dialogListChrome, dialogMaxListRows), 1)
	if needed <= rows {
		return rows, false
	}
	if rows-2 >= 1 {
		return rows - 2, true
	}
	return rows, false
}

// windowRange 返回可见区间的 [start, end)，focus 所在行尽量居中。
func windowRange(n, focus, rows int) (start, end int) {
	rows = max(rows, 1)
	if n <= rows {
		return 0, n
	}
	start = min(max(focus-rows/2, 0), n-rows)
	return start, start + rows
}

// dialogHint 返回单行放得下的按键提示。
// 逐级缩短而非截断：提示一旦换行就会撑破 listBudget 的高度预算。
func dialogHint(innerWidth int, multi bool) string {
	hint := "↑/↓ navigate · Enter select · Esc cancel"
	if multi {
		hint = "↑/↓ navigate · 1-9 jump · Enter select · Esc cancel"
	}
	if lipgloss.Width(hint) > innerWidth {
		hint = "↑/↓ move · Enter select · Esc cancel"
	}
	if lipgloss.Width(hint) > innerWidth {
		hint = "↑/↓ · Enter · Esc"
	}
	return hint
}

// moreLine 渲染 "↑ N more" / "↓ N more" 滚动指示行，在内容宽度内居中。
func moreLine(n int, up bool, innerWidth int, c common.ColorTokens) string {
	arrow := "↓"
	if up {
		arrow = "↑"
	}
	text := arrow + " " + strconv.Itoa(n) + " more"
	style := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	return strings.Repeat(" ", max((innerWidth-lipgloss.Width(text))/2, 0)) + style.Render(text)
}

// allocateColumns 在总宽 avail 内分配 provider / model 两列：
// 自然宽度放得下就原样使用，放不下时按 2:3 分配（model 名通常更长），每列至少 6 格。
func allocateColumns(avail, provNatural, modelNatural int) (int, int) {
	if avail < 12 {
		return max(avail/2, 1), max(avail-avail/2, 1)
	}
	if provNatural+modelNatural <= avail {
		return provNatural, modelNatural
	}
	prov := avail * 2 / 5
	model := avail - prov
	if prov < 6 {
		prov = 6
		model = avail - prov
	}
	if model < 6 {
		model = 6
		prov = avail - model
	}
	return prov, model
}

// contextLabel 返回上下文窗口的紧凑文本（与 TUI 上下文进度条同一格式），未知时为 "-"。
func contextLabel(n int) string {
	if n <= 0 {
		return "-"
	}
	return common.FormatTokenShort(int64(n))
}

// padLeft 左侧补空格至显示宽度 w（超出则原样返回）。
func padLeft(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

// ellipsize 按显示宽度截断 s 并右侧补空格，使结果宽度恰为 w。
// 超长时中间省略（保留首尾），因为同前缀的 provider/model 名靠尾部区分：
// 窄终端下 "siliconflow_ds4_flash" 与 "siliconflow_dsv32" 若只保留头部会完全一样。
func ellipsize(s string, w int) string {
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
