package tui

import "strings"

// DefaultCollapseLines 是所有可折叠内容类型的统一折叠阈值。
// 超过此行数的内容将被折叠，只显示前 DefaultCollapseLines 行 + 折叠提示。
const DefaultCollapseLines = 20

// collapseAndHint 是所有可折叠内容的统一入口函数。
// 语义：
//   - 行数 <= maxLines → 原样返回（不折叠）
//   - 行数 > maxLines 且 entry.collapsed == true → 截断 + 底部提示
//   - 行数 > maxLines 且 entry.collapsed == false → 原样返回
// 注意：entry 可以为 nil（防御性），此时如果超阈值则默认折叠。
func collapseAndHint(content string, entry *logEntry, maxLines int) string {
	content = strings.TrimRight(content, "\n")
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines {
		return content
	}
	// 如果 entry 为 nil，默认折叠（防御性）
	if entry == nil || entry.collapsed {
		visible := lines[:maxLines]
		hidden := len(lines) - maxLines
		return strings.Join(visible, "\n") + "\n" + renderCollapseHint(hidden, false)
	}
	return content
}
