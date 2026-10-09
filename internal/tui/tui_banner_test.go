package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// renderBanner() 渲染出的 banner 宽度 = logo 最长行(41 列) + bannerPadStyle
// 左右 padding 各 1 列 = 43 列。欢迎面板左面板宽度必须容纳该宽度,否则
// lipgloss 的 Width 会把超宽行强制折行,导致 logo 变形(防回归保护)。
const wantBannerWidth = 43

// TestRenderBannerWidth 保证 banner 的可见宽度恒为 43 列:
// 若 logo 内容或 padding 调整,需同步更新 wantBannerWidth 及左面板宽度逻辑。
func TestRenderBannerWidth(t *testing.T) {
	banner := renderBanner()
	if got := lipgloss.Width(banner); got != wantBannerWidth {
		t.Errorf("renderBanner() 宽度 = %d, 期望 %d", got, wantBannerWidth)
	}
}

// TestRenderBannerNotWrapped 验证把 banner 放入与其等宽的 Width 样式中
// 渲染后行数不变(仍为 logo 的 3 行),且每行可见宽度不被截断:
// 即左面板宽度 >= banner 宽度时不会发生折行/变形。
func TestRenderBannerNotWrapped(t *testing.T) {
	banner := renderBanner()
	bannerWidth := lipgloss.Width(banner)

	wrapped := lipgloss.NewStyle().Width(bannerWidth).Render(banner)

	// 等宽 Width 包裹后不应折行:仍是 logo 三行
	if got := lipgloss.Height(wrapped); got != 3 {
		t.Errorf("等宽 Width(%d) 包裹后行数 = %d, 期望 3(出现额外行说明发生了折行)", bannerWidth, got)
	}

	// 逐行检查可见宽度未被截断
	lines := strings.Split(wrapped, "\n")
	if len(lines) != 3 {
		t.Fatalf("等宽 Width 包裹后行数 = %d, 期望 3", len(lines))
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got != bannerWidth {
			t.Errorf("第 %d 行可见宽度 = %d, 期望 %d", i, got, bannerWidth)
		}
	}
}
