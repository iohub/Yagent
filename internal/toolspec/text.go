package toolspec

import "strings"

// FirstSentence 返回 text 的第一句话（含句号）。
// 没有句号、或句号就在首位时返回原文 —— 后一种情况里"第一句"是空串，
// 拿它当 Brief 等于让目录里出现一行只有名字的条目，比截断更糟。
//
// 它住在 toolspec 而不是调用方：Meta 的工具池表格与外部工具的 Brief 都要用，
// 任意一侧各写一份就会漂走（两份同形实现只有一边会变，根 AGENTS.md §11 坑 18）。
func FirstSentence(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexByte(text, '.'); idx > 0 {
		return text[:idx+1]
	}
	return text
}
