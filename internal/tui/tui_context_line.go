package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// contextBarThresholdWidth 进度条样式的最小可用内容宽度（列）：
// 低于该宽度时自动退化为无进度条样式，避免溢出或截断。
const contextBarThresholdWidth = 60

// renderContextLine 渲染独立的上下文用量行（折叠面板与右上角 dashboard 共用）。
//
// 有窗口上限（contextWindow > 0 且 width 足够）时：
//
//	⛁  Context  45.2k / 200.0k  ▰▰▰▰▱▱▱▱▱▱  22.6%
//
// 无窗口上限或窄终端时退化为简约样式：
//
//	⛁  Context · 45.2k tokens
//
// ctxTokens <= 0 时返回空字符串（调用方跳过该行）。
// width 为该行可用的内容宽度（不含边框与 padding）。
func renderContextLine(ctxTokens int64, contextWindow int, width int) string {
	if ctxTokens <= 0 {
		return ""
	}

	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	valueStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	ctxStr := formatToken(ctxTokens)

	// 无窗口上限或可用宽度不足：退化为无进度条样式（方案 A）
	if contextWindow <= 0 || width < contextBarThresholdWidth {
		return labelStyle.Render("⛁  Context · ") + valueStyle.Render(ctxStr+" tokens")
	}

	// 用量比例（封顶 100%）
	ratio := float64(ctxTokens) / float64(contextWindow)
	if ratio > 1 {
		ratio = 1
	}
	pct := ratio * 100

	// 颜色梯度（按用量）：<60% 绿、60%–85% 黄、>85% 红
	gradientColor := "79"
	switch {
	case pct > 85:
		gradientColor = "203"
	case pct >= 60:
		gradientColor = "214"
	}
	gradientStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(gradientColor))

	// 固定 10 格进度条：▰ 填充 / ▱ 空白，字符间不加空格
	const barCells = 10
	filled := int(ratio*barCells + 0.5)
	if filled > barCells {
		filled = barCells
	}
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", barCells-filled)

	return labelStyle.Render("⛁  Context  ") +
		gradientStyle.Render(ctxStr) +
		labelStyle.Render(" / "+formatToken(int64(contextWindow))+"  ") +
		gradientStyle.Render(bar) +
		gradientStyle.Render(fmt.Sprintf("  %.1f%%", pct))
}
