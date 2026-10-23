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

// recomputeSearchMatches 按**绝对行号**扫描正文并记录命中行。虚拟化之后 viewport 里
// 只有可见区间那几行，命中必须落在整段历史上，所以这里读的是排版块本身而不是窗口文本 ——
// 顺带省掉"把整段 transcript 拼成一个字符串"的那次分配。
//
// 必须先 ansi.Strip 再匹配：内容单元格带满 SGR/超链接转义，
// 直接对原文匹配会让 "38"、"0m" 这类模式命中颜色码本身。
func (m *model) recomputeSearchMatches() {
	if m.searchPattern == "" {
		return
	}
	m.bodyEnsureIndex()
	m.searchContentFP = m.body.version
	needle := strings.ToLower(m.searchPattern)

	m.searchLines = nil
	m.searchLineAt = make(map[int]int)

	line := 0
	scan := func(text string) {
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(strings.ToLower(ansi.Strip(l)), needle) {
				m.searchLineAt[line] = len(m.searchLines)
				m.searchLines = append(m.searchLines, line)
			}
			line++
		}
	}
	if m.body.headerLines > 0 {
		scan(m.body.header)
	}
	for i := range m.body.blocks {
		if head := m.body.blocks[i].head; head != "" {
			scan(head)
		}
		if text := m.body.blocks[i].text; text != "" {
			scan(text)
		}
	}
}

// syncSearchMatches 在内容变化后重算命中行。行号是绝对行号，流式输出会不断插入新行，
// 不跟随重算会让 n/N 跳到错位的地方。
func (m *model) syncSearchMatches() {
	if m.searchPattern == "" {
		return
	}
	m.bodyEnsureIndex()
	if m.body.version == m.searchContentFP {
		return
	}
	m.recomputeSearchMatches()
	if m.searchCursor >= len(m.searchLines) {
		m.searchCursor = 0
	}
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
// line 是 viewport 传进来的**窗口内**行号，而命中记的是绝对行号 —— 不加回 body.top
// 就是给错行上色。
func (m *model) searchLineStyle(line int) lipgloss.Style {
	idx, ok := m.searchLineAt[m.body.top+line]
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

// scrollToLogicalLine 把绝对行 line 滚入视野，已在视野内则保持不动。
// 正文按目标宽度预先排过版、没开 SoftWrap，所以逻辑行号与屏幕行号同构。
func (m *model) scrollToLogicalLine(line int) {
	if line < 0 {
		line = 0
	}
	if line >= m.body.top && line < m.body.top+m.bodyHeight() {
		return
	}
	m.bodyScrollTo(line)
}

// setInfoMsg 显示一条瞬时提示行。
// infoMsg 此前只被写入、从不渲染，所有这类反馈（"任务运行中不能切换模型" 等）
// 实际上都被丢掉了；现在由 footer 单独一行显示，并在下一次按键时清除。
func (m *model) setInfoMsg(text string) {
	m.infoMsg = text
	m.invalidateFooterCache()
}
