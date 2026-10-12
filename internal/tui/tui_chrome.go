package tui

import (
	"fmt"
	"image/color"
	"strings"

	"yagent/internal/tui/common"

	"charm.land/lipgloss/v2"
)

// 输入面板的骨架字符。顶/底边框是手绘的而不是交给 lipgloss 的 Border，
// 因为需要在边框线上嵌入模式标签、光标坐标与快捷键提示（lipgloss 不支持边框标题）。
const (
	panelCornerTL = "╭"
	panelCornerTR = "╮"
	panelCornerBL = "╰"
	panelCornerBR = "╯"
	panelEdgeH    = "─"
	panelAccent   = "▌"
	panelHintSep  = " · "
)

// 面板边框色 —— 颜色本身是模式的第二重指示（状态栏文字之外）。
var (
	panelColorIdle   = lipgloss.Color("238") // 尚未提交过任务
	panelColorInsert = lipgloss.Color("62")  // INSERT，可输入
	panelColorRun    = lipgloss.Color("76")  // 任务运行中
	panelColorNormal = lipgloss.Color("240") // NORMAL，命令行未激活
	panelColorEx     = lipgloss.Color("214") // 命令行 ":"
	panelColorSearch = lipgloss.Color("39")  // 命令行 "/" 与搜索命中
)

var (
	panelLabelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("250"))
	panelMetaStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	panelKeycapStyle   = lipgloss.NewStyle().Background(lipgloss.Color("235")).Foreground(lipgloss.Color("250"))
	panelHintDescStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	panelHintSepStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	cmdlinePrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	cmdlineTextStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	cmdlineCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Background(lipgloss.Color("235"))
	cmdlinePhStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// 状态栏上方的瞬时提示行（info toast），比错误行更弱化。
	infoLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	infoLineMark  = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
)

// panelWidth 是输入面板（含边框）的总宽度。内容区 = panelWidth-4，
// 与 textarea 的 computeFieldWidth() 对齐（左右边框各 1 + 内边距各 1）。
func (m *model) panelWidth() int {
	return m.computeFieldWidth() + 4
}

// editorPanelColor 返回编辑面板的边框色。
func (m *model) editorPanelColor() color.Color {
	switch {
	case m.taskRunning:
		return panelColorRun
	case m.taskStarted:
		return panelColorInsert
	default:
		return panelColorIdle
	}
}

// cmdlinePanelColor 返回命令行面板的边框色：随前缀切换，让 ":" 与 "/" 一眼可分。
func (m *model) cmdlinePanelColor() color.Color {
	switch {
	case strings.HasPrefix(m.commandBuffer, "/"):
		return panelColorSearch
	case strings.HasPrefix(m.commandBuffer, ":"):
		return panelColorEx
	case m.searchPattern != "":
		return panelColorSearch
	default:
		return panelColorNormal
	}
}

// editorBodyStyle 只画左右边框（顶/底由 renderPanelTop/Bottom 手绘）。
// lipgloss 的 Width 是含 padding 与边框的总宽，所以内容区正好是 totalWidth-4。
func editorBodyStyle(c color.Color, totalWidth int) lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderRight(true).
		Width(totalWidth).
		Padding(0, 1)
}

// renderPanelTop 绘制顶边框，整行宽度严格等于 width：
//
//	╭─ ▌TASK ───────────────────── 12:3 ─╮   （有 meta）
//	╭─ ▌TASK ────────────────────────────╮   （无 meta）
//
// 空间不足时先丢 meta，再压缩横线；横线最少保留 1 列以维持圆角形状。
func renderPanelTop(label, meta string, width int, c color.Color) string {
	edge := lipgloss.NewStyle().Foreground(c)
	left := common.ApplyGrad(panelAccent) + panelLabelStyle.Render(label)

	dashes := func(n int) string { return edge.Render(strings.Repeat(panelEdgeH, n)) }

	if meta != "" {
		renderedMeta := panelMetaStyle.Render(meta)
		// 固定骨架：╭─ (3) + left + " " + fill + " " + meta + " ─╮ (3)
		fill := width - lipgloss.Width(left) - lipgloss.Width(renderedMeta) - 8
		if fill >= 1 {
			return edge.Render(panelCornerTL+panelEdgeH+" ") +
				left + " " + dashes(fill) + " " + renderedMeta + " " +
				edge.Render(panelEdgeH+panelCornerTR)
		}
	}

	// 固定骨架：╭─ (3) + left + " " + fill + ─╮ (2)
	fill := width - lipgloss.Width(left) - 6
	if fill < 1 {
		fill = 1
	}
	return edge.Render(panelCornerTL+panelEdgeH+" ") + left + " " + dashes(fill) +
		edge.Render(panelEdgeH+panelCornerTR)
}

// renderPanelBottom 绘制 ╰─ HINTS ─────╯，整行宽度严格等于 width。
// hints 放不进可用宽度时从尾部整段丢弃（而不是截断到半个键帽）。
func renderPanelBottom(hints string, width int, c color.Color) string {
	edge := lipgloss.NewStyle().Foreground(c)
	// 固定骨架：╰─ (3) + hints + " " + fill + ╯ (1)，横线最少 1 列
	body := renderHintBar(hints, width-6)
	fill := width - lipgloss.Width(body) - 5
	if fill < 1 {
		fill = 1
	}
	return edge.Render(panelCornerBL+panelEdgeH+" ") + body + " " +
		edge.Render(strings.Repeat(panelEdgeH, fill)) + edge.Render(panelCornerBR)
}

// renderHintBar 把 "alt+s 提交 · esc 普通模式" 渲染成键帽 + 说明序列，
// 并保证总宽不超过 budget。与 help 对话框的键帽是同一套视觉语言。
func renderHintBar(hints string, budget int) string {
	if budget < 4 {
		return ""
	}
	sep := panelHintSepStyle.Render(panelHintSep)
	sepWidth := lipgloss.Width(sep)

	var b strings.Builder
	used := 0
	for _, item := range strings.Split(hints, panelHintSep) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		rendered := renderHintItem(item)
		cost := lipgloss.Width(rendered)
		if b.Len() > 0 {
			cost += sepWidth
		}
		if used+cost > budget {
			break
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(rendered)
		used += cost
	}
	return b.String()
}

// renderHintItem 把 "alt+s 提交" 渲染成 [alt+s] 提交：首词是按键，其余是说明。
func renderHintItem(item string) string {
	k, desc, found := strings.Cut(item, " ")
	if !found {
		return panelKeycapStyle.Render(k)
	}
	return panelKeycapStyle.Render(k) + panelHintDescStyle.Render(" "+desc)
}

// renderInfoLine 渲染一条自动消失的提示行。infoMsg 过去只被写入、从不渲染，
// 所有这类反馈（"任务运行中不能切换模型" 等）实际上都被丢掉了。
func (m *model) renderInfoLine() string {
	if m.infoMsg == "" {
		return ""
	}
	return infoLineMark.Render("ℹ") + " " + infoLineStyle.Render(m.infoMsg)
}

// renderEditorPanel 渲染 INSERT 模式的输入面板：
// 顶边框（标签 + 行列号）、textarea、底边框（上下文快捷键）。
func (m *model) renderEditorPanel() string {
	c := m.editorPanelColor()
	w := m.panelWidth()

	m.input.SetWidth(m.computeFieldWidth())
	m.input.SetHeight(m.computeInputHeight())

	meta := fmt.Sprintf("%d:%d", m.input.Line()+1, m.input.Column()+1)

	return strings.Join([]string{
		renderPanelTop(langManager.GetText("EditorPanelLabel"), meta, w, c),
		editorBodyStyle(c, w).Render(m.input.View()),
		renderPanelBottom(langManager.GetText("EditorHintEdit"), w, c),
	}, "\n")
}

// cmdlineHeight 是命令行面板的行数：未激活时只有上下边框（闭合的窄条），
// 一旦输入 ":" 或 "/" 就展开出中间那行 —— 命令行按需出现，与 vim 一致。
func (m *model) cmdlineHeight() int {
	if m.commandBuffer == "" {
		return 2
	}
	return 3
}

// renderCmdlinePanel 渲染 NORMAL 模式的面板。commandBuffer 为空时是一条闭合的
// 模式提示条；非空时展开为可编辑的命令行（此前 commandBuffer 完全不渲染，
// 用户在命令模式下是盲打的）。
func (m *model) renderCmdlinePanel() string {
	c := m.cmdlinePanelColor()
	w := m.panelWidth()

	label := "NORMAL"
	switch {
	case strings.HasPrefix(m.commandBuffer, "/"):
		label = "SEARCH"
	case m.commandBuffer != "":
		label = "COMMAND"
	}

	rows := []string{renderPanelTop(label, m.cmdlineMeta(), w, c)}
	if m.commandBuffer != "" {
		rows = append(rows, editorBodyStyle(c, w).Render(m.renderCmdlineContent(w-4)))
	}
	rows = append(rows, renderPanelBottom(m.cmdlineHint(), w, c))
	return strings.Join(rows, "\n")
}

// cmdlineMeta 是命令行面板右上角的状态。只放搜索命中进度 ——
// 滚动位置已经在状态栏里了，这里再报一次只是重复。
func (m *model) cmdlineMeta() string {
	return m.searchLabel()
}

// cmdlineHint 按当前命令行前缀给出对应的操作提示。
func (m *model) cmdlineHint() string {
	switch {
	case strings.HasPrefix(m.commandBuffer, "/"):
		return langManager.GetText("CmdlineHintSearch")
	case strings.HasPrefix(m.commandBuffer, ":"):
		return langManager.GetText("CmdlineHintEx")
	default:
		return langManager.GetText("CmdlineHintIdle")
	}
}

// renderCmdlineContent 渲染命令行正文：前缀 + 已输入内容 + 块状光标。
// 超出 innerWidth 时从左侧丢弃（保留光标所在的尾部），与 vim 命令行滚动一致。
func (m *model) renderCmdlineContent(innerWidth int) string {
	if innerWidth < 3 {
		innerWidth = 3
	}

	var body string
	if m.commandBuffer == "" {
		body = cmdlineCursorStyle.Render("█") +
			cmdlinePhStyle.Render(" "+langManager.GetText("CmdlinePlaceholder"))
	} else {
		const cursorWidth = 1
		buf := []rune(m.commandBuffer)
		for len(buf) > 0 && lipgloss.Width(string(buf))+cursorWidth > innerWidth {
			buf = buf[1:]
		}
		var b strings.Builder
		if len(buf) > 0 && (buf[0] == ':' || buf[0] == '/') {
			b.WriteString(cmdlinePrefixStyle.Render(string(buf[0])))
			b.WriteString(cmdlineTextStyle.Render(string(buf[1:])))
		} else {
			b.WriteString(cmdlineTextStyle.Render(string(buf)))
		}
		b.WriteString(cmdlineCursorStyle.Render("█"))
		body = b.String()
	}

	return lipgloss.NewStyle().Width(innerWidth).Render(body)
}
