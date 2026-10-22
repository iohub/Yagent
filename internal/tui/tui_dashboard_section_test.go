package tui

import (
	"image/color"
	"math"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// 三段类别行（◆ Tokens / ⚙ Activity / ⧉ COMPRESS）共用 dashSectionRow：整行压一条
// 深色横带，让人一眼看出"这一段是标题，不是数据"。这里钉住两条这次改动自己的承诺：
// 带子铺满整行（漏一格就是一道视觉裂缝），三行用同一组前景色（配色统一）。

// sgrParams 从一个参照样式里现推参数段（mark = "38;" 前景或 "48;" 背景）。
// 测试里因此没有第二个颜色字面量：调色板改了不必跟着改测试，而"某一段用的不是横带底色"
// 仍然会被 bandGaps 抓出来；色彩档位（256 与 truecolor）换了也不必改 —— lipgloss
// 会把 #rrggbb 降级成 48;5;NN，参照样式与真实渲染走的是同一条路。
func sgrParams(t *testing.T, s lipgloss.Style, mark string) string {
	t.Helper()
	run := ansiRe.FindStringSubmatch(s.Render("x"))
	if run == nil {
		t.Fatalf("参照样式没发出 SGR")
	}
	i := strings.Index(run[1], mark)
	if i < 0 {
		t.Fatalf("参照样式参数段 %q 里没有 %s", run[1], mark)
	}
	return run[1][i:]
}

// bandSeq 是横带应当发出的背景参数段。
func bandSeq(t *testing.T) string {
	t.Helper()
	return sgrParams(t, lipgloss.NewStyle().Background(dashSectionBg), "48;")
}

// bandGaps 返回这一行里没压在横带上的文字（补白的空格也算 —— 带子靠它们才连成一条）。
// lipgloss 把前景与背景写在同一条 SGR 里（"1;38;5;141;48;2;…"），而每个片段末尾的
// \x1b[m 会连背景一起关掉：谁把背景只涂在外层、或者新加一段忘了走 dashBand，
// 这里就会露出没带子的那一截。
func bandGaps(row, want string) string {
	var gaps strings.Builder
	on := false
	for len(row) > 0 {
		m := ansiRe.FindStringSubmatchIndex(row)
		if m == nil {
			break
		}
		if text := row[:m[0]]; text != "" && !on {
			gaps.WriteString(text)
		}
		switch params := row[m[2]:m[3]]; {
		case params == "" || params == "0":
			on = false
		case strings.Contains(params, want):
			on = true
		case strings.Contains(params, "48;"):
			// 别的底色：不是一条带子，按没上色处理
			on = false
		}
		row = row[m[1]:]
	}
	if !on {
		gaps.WriteString(row)
	}
	return gaps.String()
}

// channel 取一个颜色的 8 位 RGB 三分量：色号写成 256 索引还是 #rrggbb 都问得出真值，
// 所以亮度这条判据不依赖某一种写法。
func channel(c color.Color) [3]uint8 {
	r, g, b, _ := c.RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)}
}

// lum 与 contrast 是 WCAG 的相对亮度与对比度 —— "底色太亮"与"字糊在底上"这两件事
// 只有这两个数说得清。
func lum(rgb [3]uint8) float64 {
	var y float64
	for i, w := range [3]float64{0.2126, 0.7152, 0.0722} {
		f := float64(rgb[i]) / 255
		if f > 0.03928 {
			f = math.Pow((f+0.055)/1.055, 2.4)
		} else {
			f /= 12.92
		}
		y += w * f
	}
	return y
}

func contrast(fg, bg color.Color) float64 {
	a, b := lum(channel(fg)), lum(channel(bg))
	if a < b {
		a, b = b, a
	}
	return (a + 0.05) / (b + 0.05)
}

// categoryRows 交出三个真实渲染入口产出的类别行，键是去掉颜色后的行内容。
func categoryRows(t *testing.T) map[string]string {
	t.Helper()
	testLang(t)

	tokenRow := buildTokenPanel(s1Model().tokenSnapshot(), s1Spec(46))[0]

	cs := compressDashboardModel().compressSnapshot()
	compressRow := ""
	for _, r := range buildCompressBlock(cs, compressPanelSpec{innerWidth: 46, maxRows: 2}) {
		if strings.HasPrefix(stripANSI(r), "⧉ COMPRESS") {
			compressRow = r
		}
	}
	if compressRow == "" {
		t.Fatal("⧉ COMPRESS 标题行没渲染出来")
	}

	rows := map[string]string{
		"◆ Tokens":   tokenRow,
		"⚙ Activity": dashSectionRow("⚙", "Activity", "", "", 46),
		"⧉ COMPRESS": compressRow,
	}
	for want, row := range rows {
		if got := stripANSI(row); !strings.HasPrefix(got, want) {
			t.Fatalf("取错了行：前缀 %q, got %q", want, got)
		}
	}
	return rows
}

// TestCategoryRows_BandSpansEveryCell 每一行的每一个单元格都必须在带子上。
func TestCategoryRows_BandSpansEveryCell(t *testing.T) {
	seq := bandSeq(t)
	for name, row := range categoryRows(t) {
		if gaps := bandGaps(row, seq); gaps != "" {
			t.Errorf("%s 有 %d 格没上底色：%q\n原始串 %q", name, len([]rune(gaps)), gaps, row)
		}
	}
}

// TestCategoryRows_BandIsDarkTone 带子必须是"底色"而不是"亮块"：它自己的相对亮度
// 必须留在暗色区（这才压得住 250 那档近白文字），而且必须与面板边框同相 ——
// 这次的返工就是因为底色太亮，把这条量化出来才不会再被同一块颜色绊倒。
func TestCategoryRows_BandIsDarkTone(t *testing.T) {
	bg := channel(dashSectionBg)
	if l := lum(bg); l > 0.06 {
		t.Errorf("横带亮度 = %.4f，太亮了（底色应当明显压在日常背景之下）", l)
	}
	// 同相：与 62 边框的 RGB 比例一致（各通道同倍数），只是暗一档。
	frame := channel(dashBorderFg)
	for i := range bg {
		if math.Abs(float64(bg[i])/float64(frame[i])-0.4) > 0.06 {
			t.Errorf("横带第 %d 通道 = %d，不是 dashBorderFg(%v) 的四成亮度，配色不成一族",
				i, bg[i], frame)
		}
	}
}

// TestCategoryRows_OnBandTextIsReadable 压在带子上的每一档前景都必须与底色拉开 4.5:1。
// 名单就是"会落在带子上的那几种颜色"，新增一档就得同时加进来。
func TestCategoryRows_OnBandTextIsReadable(t *testing.T) {
	for _, tc := range []struct {
		what string
		fg   color.Color
	}{
		{"glyph（dashAccent）", dashAccent},
		{"名称（dashTitleFg）", dashTitleFg},
		{"⧉ 行的节省量（dashOutFg）", dashOutFg},
		{"带上的次要文字（dashBandDimFg）", dashBandDimFg},
		{"键帽按键（dashKeyFg）", dashKeyFg},
	} {
		if r := contrast(tc.fg, dashSectionBg); r < 4.5 {
			t.Errorf("%s 压在横带上只有 %.2f:1（要求 ≥4.5:1）", tc.what, r)
		}
	}
	// 带外的暗标签不许被顺手搬到带子上：241/244 在这条底色上都不达标。
	for _, tooDim := range []string{"241", "244"} {
		if r := contrast(lipgloss.Color(tooDim), dashSectionBg); r >= 4.5 {
			t.Errorf("色号 %s 在带子上有 %.2f:1 —— 那 dashBandDimFg 就不该另立一档", tooDim, r)
		}
	}
}

// TestCategoryRows_ShareOneHeaderPalette 三行的 glyph 与名称必须是同一组前景色：
// dashAccent 打头、dashTitleFg 写名称 —— 换掉任意一行都会让"三行是一家"这句话落空。
func TestCategoryRows_ShareOneHeaderPalette(t *testing.T) {
	wants := []string{
		sgrParams(t, lipgloss.NewStyle().Foreground(dashAccent), "38;"),
		sgrParams(t, lipgloss.NewStyle().Foreground(dashTitleFg), "38;"),
	}
	for name, row := range categoryRows(t) {
		var params []string
		for _, run := range ansiRe.FindAllStringSubmatch(row, -1) {
			if run[1] != "" { // \x1b[m 是收尾的复位，不算一个着色段
				params = append(params, run[1])
			}
		}
		if len(params) < 2 {
			t.Fatalf("%s 的 glyph/名称段不见了：%q", name, row)
		}
		for i, want := range wants {
			if !strings.Contains(params[i], want) {
				t.Errorf("%s 第 %d 段 = %q，缺少前景 %s", name, i, params[i], want)
			}
		}
	}
}

// TestDashboard_ActivityRowIsBanded 侧栏里那一行必须真的走横带 —— 带外的格子
// 只许是面板自己的边框与内边距。
func TestDashboard_ActivityRowIsBanded(t *testing.T) {
	testLang(t)
	seq := bandSeq(t)
	m := compressDashboardModel()
	for _, line := range strings.Split(m.renderDashboard(dashboardPanelWidth, 30), "\n") {
		if !strings.Contains(stripANSI(line), "⚙ Activity") {
			continue
		}
		if gaps := bandGaps(line, seq); strings.ReplaceAll(gaps, " ", "") != "││" {
			t.Errorf("Activity 类别行只有边框与内边距该在带外，实际 %q\n原始串 %q", gaps, line)
		}
		return
	}
	t.Fatal("面板里没有 ⚙ Activity 行")
}

// TestCategoryRows_FoldToContentWhenTooNarrow 放不下右段时宁可短一截带子，
// 也不能把内容挤掉（右段的字节必须原样消失，而不是压掉名称）。
func TestCategoryRows_FoldToContentWhenTooNarrow(t *testing.T) {
	seq := bandSeq(t)
	row := dashSectionRow("◆", "Tokens", "", dashBandKeycap("alt+d", "收缩"), 14)
	if got := stripANSI(row); got != "◆ Tokens" {
		t.Errorf("窄行 = %q, want \"◆ Tokens\"（右段放不下就该整段去掉）", got)
	}
	if gaps := bandGaps(row, seq); gaps != "" {
		t.Errorf("短带子也必须逐段涂底：%q", gaps)
	}
}
