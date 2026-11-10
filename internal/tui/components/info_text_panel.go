package components

// TextPanel 是"逐行文本"信息面板：单列、可滚动。
//
// 存在的理由：`/plasma` 与 `/proposals` 的输出是诊断（`PLS-xxx`）、夹具账目与 diff ——
// 它们不是表格（列宽对不齐，长行必须原样保留），而日志条目那条通道会把 content 里的
// `\n` 换成空格再截断（`tui_render.go` 的 formatLogEntry），根本放不下。
//
// 几何与按键一律复用 infoDialogBase（`/skills`、`/mcp` 的同一套）：`infoDialogChrome`、
// `common.ListBudget`、`common.Ellipsize` 都只有那一份实现（internal/tui/AGENTS.md 硬约束 7），
// 这里只负责"一列文本"这一件事 —— 表头留一个空列，列表区就是每一行一条。
type TextPanel struct {
	infoDialogBase
}

var _ InfoPanel = (*TextPanel)(nil)

// NewTextPanel 建一个只读文本面板。lines 为空时显示一行 "（空）"：
// 空面板的表现是"框里什么都没有"，与"这个面板坏了"无法区分。
func NewTextPanel(id, title string, lines []string, hint string) *TextPanel {
	if hint == "" {
		hint = "↑/↓ scroll · Esc close"
	}
	// 表头是**空列**而不是 nil：infoDialogBase 只在 len(header) > 0 时渲染 rows，
	// 传 nil 会把每一行都当成"空态说明"来排（那份几何按 empty 算）。
	d := &TextPanel{infoDialogBase: newInfoDialogBase(id, title, []string{""}, hint)}
	d.SetLines(lines)
	return d
}

// SetLines 换一批行（判决之后重画同一份面板）。
func (d *TextPanel) SetLines(lines []string) {
	d.rows = d.rows[:0]
	if len(lines) == 0 {
		lines = []string{"（空）"}
	}
	for _, line := range lines {
		d.rows = append(d.rows, []string{line})
	}
	d.offset = 0
}

// Lines 返回当前各行（一份拷贝）。面板自己不解释内容 —— 判据在调用方。
func (d *TextPanel) Lines() []string {
	out := make([]string, 0, len(d.rows))
	for _, row := range d.rows {
		if len(row) > 0 {
			out = append(out, row[0])
		}
	}
	return out
}
