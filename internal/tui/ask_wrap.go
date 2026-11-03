package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// 决策面板家族唯一的折行口径（设计稿 §6.2）。
//
// 为什么不复用 wrapPlain（setup_render.go:339）：它按 rune 硬切超长 token，
// 于是 `internal/tui/components/user_help_dialog.go` 会被切成
// `...components/user` / `_help_dialog.go` —— 恰好把要抄进命令行的那个标识符切坏。
// 这里优先在空白断行，必须硬切时优先落在分隔符之后。向导那边保持原样：
// 改它的断行点会让已验收过的向导排版重新进入待验证状态。

// wrapSeparators 是允许作为硬切点的分隔符，按优先顺序尝试。
var wrapSeparators = []rune{'/', '.', '_', '-', ':', '='}

// wrapKeepingSeparators 按显示宽度折行，保留原文里的换行。
// 空行原样输出一个空字符串（调用方按行计数，空行也要占一行）。
func wrapKeepingSeparators(s string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		out = append(out, wrapOneLine(para, width)...)
	}
	return out
}

// wrapOneLine 折一个逻辑行。先按词填行（词本身超长时切成块），
// 保证任何一块的显示宽度 ≤ width。
func wrapOneLine(para string, width int) []string {
	var lines []string
	var line []rune
	lineW := 0

	flush := func() {
		if lineW > 0 || len(line) > 0 {
			lines = append(lines, string(line))
		}
		line, lineW = nil, 0
	}
	write := func(tok string) {
		// token 之间补空格：只在已有内容时补，行首不留空白。
		if lineW > 0 {
			line = append(line, ' ')
			lineW++
		}
		line = append(line, []rune(tok)...)
		lineW += lipgloss.Width(tok)
	}

	for _, word := range strings.Fields(para) {
		ww := lipgloss.Width(word)
		for ww > width {
			// 超长 token：切一块出来，剩下的继续按同一规则切。
			cut := splitToken(word, width)
			take, rest := word[:cut], word[cut:]
			if lineW > 0 && lineW+1+lipgloss.Width(take) > width {
				flush()
			}
			write(take)
			flush()
			word, ww = rest, lipgloss.Width(rest)
		}
		if ww == 0 {
			continue
		}
		if lineW == 0 {
			write(word)
			continue
		}
		if lineW+1+ww <= width {
			write(word)
			continue
		}
		flush()
		write(word)
	}
	if lineW > 0 || len(line) > 0 {
		lines = append(lines, string(line))
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// cjkNoLineStart 是不该出现在行首的收尾标点：中文排版里它们必须跟着前一个字，
// 挂到行首会被读成"上一行少了一个逗号"。
const cjkNoLineStart = "，。、；：！？）】》”’…—"

// avoidLeadingPunctuation 把切点往前挪，直到剩下的第一个字符不是收尾标点。
func avoidLeadingPunctuation(runes []rune, cut, width int) int {
	for cut > 1 && strings.ContainsRune(cjkNoLineStart, runes[cut]) {
		cut--
	}
	return cut
}

// splitToken 返回一个切点（按字节），使 s[:cut] 的显示宽度 ≤ width。
// 优先在最后一个分隔符之后切（路径的语义单元留在同一行），找不到就按 rune 硬切。
func splitToken(s string, width int) int {
	runes := []rune(s)
	limit := 0
	w := 0
	for i, r := range runes {
		rw := lipgloss.Width(string(r))
		if w+rw > width {
			break
		}
		w += rw
		limit = i + 1
	}
	if limit == 0 {
		// 连一个字符都放不下（width<4 已被上游夹住）：至少切一个字符，避免死循环。
		return len(string(runes[0]))
	}
	limit = avoidLeadingPunctuation(runes, limit, width)
	head := runes[:limit]
	// 按分隔符的**优先级**各扫一遍（从行尾往前）：先 / 再 . 再 _ ……
	// 反过来扫（先找最近的任意分隔符）会把 internal/tui/components/user_help_dialog.go
	// 切成 …/user_help_ + dialog.go —— 标识符仍然被劈开，只是劈得"看起来像代码"。
	for _, sep := range wrapSeparators {
		for i := len(head) - 1; i > 0; i-- {
			if head[i] == sep {
				return len(string(head[:i+1]))
			}
		}
	}
	return len(string(head))
}

// foldLines 把一段长文本折成"折叠态可见的 N 行 + 还剩多少行"。
// 折叠提示行必须存在（§6.1）：砍掉内容而不说"还有多少没看见"，
// 与今天的 3 行静默截断是同一种失效。
func foldLines(lines []string, max int) (shown []string, hidden int) {
	if max < 0 {
		max = 0
	}
	if len(lines) <= max {
		return lines, 0
	}
	return lines[:max], len(lines) - max
}

// truncateToDisplayWidth 按显示宽度截断（选项标签用，单行）。
func truncateToDisplayWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > maxW {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// inputWrappedLines 数一遍主输入框渲染后要占几行（软折行也计入）。
// computeInputHeight 用它而不是 strings.Count(value,"\n") —— 后者把 1000 字符的
// 单行答案算成 1 行，于是尾部被 textarea 内部裁掉（设计稿 §6.4 第 2 条）。
func (m *model) inputWrappedLines() int {
	w := m.computeFieldWidth() - 4 // textarea 可视列宽：扣掉面板左右边框与内边距
	if w < 8 {
		w = 8
	}
	n := 0
	for _, para := range strings.Split(m.input.Value(), "\n") {
		if para == "" {
			n++
			continue
		}
		n += len(wrapKeepingSeparators(para, w))
	}
	return n
}
