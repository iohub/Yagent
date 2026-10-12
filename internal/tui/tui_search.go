package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 搜索命中的行首色带。只设 Background 不设 Foreground：日志单元格自带颜色，
// 外层 fg 会被内层转义覆盖，命中文本反而更难辨认。
//
// 色带宽度不铺满整行 —— 行内容里每个着色段都以 "\x1b[m" 收尾，会把外层背景
// 一并复位，所以能稳定覆盖到第一个复位点之前的区段。作为命中定位已经足够：
// 再配合状态栏的 [n/m] 与自动跳转，用户不需要整行反色也能确认落在哪。
var (
	searchMatchStyle   = lipgloss.NewStyle().Background(lipgloss.Color("238"))
	searchCurrentStyle = lipgloss.NewStyle().Background(lipgloss.Color("94"))
)

// runSearch 在 viewport 当前内容里做大小写不敏感的字面匹配，并跳到第一条命中。
func (m *model) runSearch(pattern string) {
	m.searchPattern = pattern
	m.searchLines = nil
	m.searchLineAt = nil
	m.searchCursor = 0
	m.searchContentFP = -1

	if pattern == "" {
		m.clearSearch()
		return
	}

	m.recomputeSearchMatches()

	if len(m.searchLines) == 0 {
		m.setInfoMsg(fmt.Sprintf("%s: %s", langManager.GetText("SearchNoMatch"), pattern))
		return
	}

	m.scrollToLogicalLine(m.searchLines[0])
	m.updateViewportCache()
	m.invalidateFooterCache()
}

// clearSearch 撤掉高亮并复位搜索状态。
func (m *model) clearSearch() {
	m.searchPattern = ""
	m.searchLines = nil
	m.searchLineAt = nil
	m.searchCursor = 0
	m.searchContentFP = 0
	m.viewportViewValid = false
	m.invalidateFooterCache()
}

// recomputeSearchMatches 扫描 viewport 内容并记录命中行号。
// 必须先 ansi.Strip 再匹配：内容单元格带满 SGR/超链接转义，
// 直接对原文匹配会让 "38"、"0m" 这类模式命中颜色码本身。
func (m *model) recomputeSearchMatches() {
	if m.searchPattern == "" {
		return
	}
	content := m.viewport.GetContent()
	m.searchContentFP = contentFingerprint(content)
	needle := strings.ToLower(m.searchPattern)

	m.searchLines = nil
	m.searchLineAt = make(map[int]int)

	for i, line := range strings.Split(content, "\n") {
		if !strings.Contains(strings.ToLower(ansi.Strip(line)), needle) {
			continue
		}
		m.searchLineAt[i] = len(m.searchLines)
		m.searchLines = append(m.searchLines, i)
	}
}

// syncSearchMatches 在内容变化后重算命中行。行号相对 viewport 内容而言，
// 流式输出会不断插入新行，不跟随重算会让 n/N 跳到错位的地方。
func (m *model) syncSearchMatches() {
	if m.searchPattern == "" {
		return
	}
	if contentFingerprint(m.viewport.GetContent()) == m.searchContentFP {
		return
	}
	m.recomputeSearchMatches()
	if m.searchCursor >= len(m.searchLines) {
		m.searchCursor = 0
	}
}

// contentFingerprint 是内容的廉价校验值。只用长度会让"等长但改写"的日志逃过
// 重算，所以再按固定步长采样若干字节。
func contentFingerprint(s string) int {
	if s == "" {
		return 0
	}
	h := len(s)
	for i := 0; i < len(s); i += 61 {
		h = h*31 + int(s[i])
	}
	return h
}

// searchStep 沿 dir 方向移动到下一条命中，dir 为 +1 / -1。
func (m *model) searchStep(dir int) {
	if !m.hasActiveSearch() {
		m.setInfoMsg(langManager.GetText("SearchNoMatch"))
		return
	}
	n := len(m.searchLines)
	m.searchCursor = ((m.searchCursor+dir)%n + n) % n
	m.scrollToLogicalLine(m.searchLines[m.searchCursor])
	m.updateViewportCache()
	m.invalidateFooterCache()
}

func (m *model) hasActiveSearch() bool {
	return m.searchPattern != "" && len(m.searchLines) > 0
}

// searchLabel 是状态栏上的命中进度 "[3/17]"。
func (m *model) searchLabel() string {
	if !m.hasActiveSearch() {
		return ""
	}
	return fmt.Sprintf("[%d/%d]", m.searchCursor+1, len(m.searchLines))
}

// searchLineStyle 挂到 viewport.StyleLineFunc 上，按逻辑行号给命中行上色。
// 不用 viewport.SetHighlights：它把偏移量按 ansi.Strip 之后的字节位解析，
// 却用原始带 ANSI 的 content 判换行（viewport/highlight.go），
// 而我们整屏日志都是重着色文本，行号会算错。
func (m *model) searchLineStyle(line int) lipgloss.Style {
	idx, ok := m.searchLineAt[line]
	if !ok {
		return lipgloss.NewStyle()
	}
	if idx == m.searchCursor {
		return searchCurrentStyle
	}
	return searchMatchStyle
}

// bindViewportHooks 在 viewport 被替换后重新挂上逐行回调。
// viewport.Model 是值类型，初始化、切换会话标签页都会整体赋值，
// 所以每次替换之后都必须重新绑定，否则搜索高亮会静默失效。
func (m *model) bindViewportHooks() {
	m.viewport.StyleLineFunc = m.searchLineStyle
}

// scrollToLogicalLine 把逻辑行 line 滚入视野，已在视野内则保持不动。
// viewport 当前未开 SoftWrap（日志渲染时已按目标宽度排版），所以 YOffset
// 与逻辑行号同构；将来若开启软换行，这里需要换算成视觉行偏移。
func (m *model) scrollToLogicalLine(line int) {
	if line < 0 {
		line = 0
	}
	visible := m.viewport.Height()
	if visible < 1 {
		visible = 1
	}
	top := m.viewport.YOffset()
	if line < top || line >= top+visible {
		m.viewport.SetYOffset(line)
	}
}

// setInfoMsg 显示一条瞬时提示行。
// infoMsg 此前只被写入、从不渲染，所有这类反馈（"任务运行中不能切换模型" 等）
// 实际上都被丢掉了；现在由 footer 单独一行显示，并在下一次按键时清除。
func (m *model) setInfoMsg(text string) {
	m.infoMsg = text
	m.invalidateFooterCache()
}
