package components

import (
	"yagent/internal/tui/common"

	"charm.land/lipgloss/v2"
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
// 实现已上提到 common.PanelWidths（引导向导要用同一套推导，不能有两份）；
// 内容宽度下限 30：再窄就无法保证 provider/model 两列还能区分彼此。
func dialogWidths(termWidth int) (dialogWidth, innerWidth int) {
	return common.PanelWidths(dialogMaxWidth, 30, termWidth)
}

// listBudget 返回列表可占用的行数，以及是否渲染上下滚动指示行。
// 见 common.ListBudget：终端高度扣除固定行后受 dialogMaxListRows 限制，
// 需要滚动且扣掉指示行后仍有余量时预留 2 行，否则放弃指示行 —— 保证对话框
// 永远不会高出终端。
func listBudget(height, needed int) (rows int, showMore bool) {
	return common.ListBudget(dialogListChrome, dialogMaxListRows, height, needed)
}

// windowRange 返回可见区间的 [start, end)，focus 所在行尽量居中。见 common.WindowRange。
func windowRange(n, focus, rows int) (start, end int) {
	return common.WindowRange(n, focus, rows)
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

// moreLine 渲染 "↑ N more" / "↓ N more" 滚动指示行。见 common.MoreLine。
func moreLine(n int, up bool, innerWidth int, c common.ColorTokens) string {
	return common.MoreLine(n, up, innerWidth, c)
}

// allocateColumns 在总宽 avail 内分配 provider / model 两列。见 common.AllocateColumns。
func allocateColumns(avail, provNatural, modelNatural int) (int, int) {
	return common.AllocateColumns(avail, provNatural, modelNatural)
}

// contextLabel 返回上下文窗口的紧凑文本，未知时为 "-"。见 common.ContextLabel。
func contextLabel(n int) string {
	return common.ContextLabel(n)
}

// padLeft 左侧补空格至显示宽度 w。见 common.PadLeft。
func padLeft(s string, w int) string {
	return common.PadLeft(s, w)
}

// ellipsize 按显示宽度截断 s 并右侧补空格，使结果宽度恰为 w。见 common.Ellipsize。
func ellipsize(s string, w int) string {
	return common.Ellipsize(s, w)
}
