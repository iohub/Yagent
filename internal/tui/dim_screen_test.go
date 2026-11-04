package tui

import (
	"strings"
	"testing"

	"yagent/internal/protocol"

	"charm.land/lipgloss/v2"
)

// 压暗接入整屏后的闸门：判据、范围、以及"不许多占一行一列"。
// 行宽与总行数的矩阵已经在 TestScreen_NeverExceedsTermHeight / TestAskPanel_WidthIsExact 里，
// 那两条跑的就是带压暗的 View —— 压暗若改变单元格，它们会直接红。

// TestDim_PreservesCellsOnRealScreen 钉住 dim.go 前提 ⑤：压暗只替换序列，
// 不增删可见字符 —— 行数与每一行的显示宽度、剥掉 ANSI 之后的文本都必须逐行相等。
// 样本一律取真实渲染产物：自己拼的字符串测不到家族骨架。
func TestDim_PreservesCellsOnRealScreen(t *testing.T) {
	m, _ := askTestModel(t, 120, 40)
	seedLog(m, "第一步：读 internal/tui/ask_panel.go")
	seedLog(m, "候选实现有两个 https://example.com/a/b_c-d")
	m.rebuildContentCache()
	feedHelpAsk(m, "选哪个实现？", "上下文里有一串标识符，断点可能落在斜杠上", longList(6),
		protocol.InteractionSelect)

	blocks := []string{
		m.renderTabBar(),
		m.viewport.View(),
		m.renderEditorPanel(),
		m.tokenDashboardBlock(),
		m.renderAirlineStatusBar(),
		m.askPanelBlock(),
	}
	rows := 0
	for _, blk := range blocks {
		if blk == "" {
			continue
		}
		want := strings.Split(blk, "\n")
		got := strings.Split(dimAnsi(blk, true), "\n")
		if len(got) != len(want) {
			t.Fatalf("压暗把 %d 行变成了 %d 行", len(want), len(got))
		}
		for i := range want {
			if w, dw := lipgloss.Width(want[i]), lipgloss.Width(got[i]); w != dw {
				t.Errorf("第 %d 行宽度 %d -> %d", i, w, dw)
			}
			if stripANSI(want[i]) != stripANSI(got[i]) {
				t.Errorf("第 %d 行的可见文本被改写:\n%q\n%q", i, stripANSI(want[i]), stripANSI(got[i]))
			}
			if want[i] != "" && !strings.HasSuffix(got[i], "\x1b[0m") {
				t.Errorf("第 %d 行末尾没有收口的 reset，字色会漏到下一块面板:\n%q", i, got[i])
			}
		}
		rows += len(want)
	}
	if rows < 10 {
		t.Fatalf("样本只有 %d 行，夹具没有真的渲染出内容", rows)
	}
}

// TestDim_GateFollowsPanel 判据只有一个：面板在场才压暗。
// 无面板时 dimBehindAsk 必须逐字节原样返回 —— 否则每一帧都在白扫一遍全屏 ANSI。
func TestDim_GateFollowsPanel(t *testing.T) {
	m, _ := askTestModel(t, 120, 40)
	seedLog(m, "transcript 里的一行")
	m.rebuildContentCache()
	sample := m.viewport.View()
	if !strings.Contains(sample, "\x1b[38") {
		t.Fatalf("夹具没有渲染出上色正文，这条断言恒真:\n%q", sample)
	}
	if got := m.dimBehindAsk(sample); got != sample {
		t.Error("没有待答求助时压暗不该发生")
	}

	feedHelpAsk(m, "确认删除？", "会连带删掉它的测试夹具", []string{"删除", "留下"},
		protocol.InteractionConfirm)
	if !m.askDimActive() {
		t.Fatal("面板已经待答，判据却还是 false")
	}
	if got := m.dimBehindAsk(sample); got == sample {
		t.Error("待答时背景一个字都没压暗")
	}
}

// TestDim_AskFamilyStaysBright 整屏拼接后：面板自身与答题区必须逐字节不变，背景那一侧
// 必须出现被压暗的行。按**整行相等**判断而不是子串 —— 被压暗的行里仍然含有原行，
// 子串断言看不见那种漂移。
func TestDim_AskFamilyStaysBright(t *testing.T) {
	m, _ := askTestModel(t, 120, 40)
	seedLog(m, "transcript 里的一行")
	m.rebuildContentCache()
	feedHelpAsk(m, "选哪个？", "两个候选实现", longList(4), protocol.InteractionSelect)

	lines := strings.Split(m.View().Content, "\n")
	exact := map[string]bool{}
	for _, l := range lines {
		exact[l] = true
	}

	foreground := strings.Split(m.askPanelBlock(), "\n")
	if len(foreground) < 4 {
		t.Fatalf("面板只渲染出 %d 行，夹具没有真的把面板摆上来", len(foreground))
	}
	for _, l := range foreground {
		if !exact[l] {
			t.Errorf("面板这一行被压暗了（屏上找不到逐字节相同的它）:\n%q", l)
		}
	}
	editorTop := strings.Split(m.renderEditorPanel(), "\n")[0]
	if !exact[editorTop] {
		t.Error("答题区被压暗了：用户正要往里打字")
	}

	dimmed := 0
	for _, l := range lines {
		if strings.HasPrefix(l, dimNeutralSeq) {
			dimmed++
		}
	}
	if dimmed == 0 {
		t.Error("整屏没有一行被压暗，判据或接线失效")
	}
}

// TestDim_CardBgSharedWithAnswerField 钉住"待答面板与答题输入框是同一块卡片"：
// 两处必须渲染出同一个底色。只改一边不会报错，只会读成两个不相干的框。
func TestDim_CardBgSharedWithAnswerField(t *testing.T) {
	m, _ := askTestModel(t, 120, 40)
	feedHelpAsk(m, "选哪个？", "两个候选实现", longList(3), protocol.InteractionSelect)

	seq := lipgloss.NewStyle().Background(askCardBg).Render("x")
	bgSeq := seq[strings.Index(seq, "\x1b["):strings.LastIndex(seq, "x")]
	if bgSeq == "" {
		t.Fatal("底色序列没解析出来，样本本身有问题")
	}
	if !strings.Contains(m.askPanelBlock(), bgSeq) {
		t.Errorf("面板没铺上卡片底色 %q", bgSeq)
	}
	if editor := m.renderEditorPanel(); !strings.Contains(editor, bgSeq) {
		t.Errorf("答题区没有同一个底色 %q", bgSeq)
	}
}

// TestDim_OffInReaderMode ctrl+o 全屏阅读器自己就是前景，没有"背景"可压：
// 阅读器输出里不该出现任何压暗行，关掉之后判据要立刻恢复。
func TestDim_OffInReaderMode(t *testing.T) {
	m, _ := askTestModel(t, 120, 40)
	seedLog(m, "transcript 里的一行")
	m.rebuildContentCache()
	feedHelpAsk(m, "选哪个？", strings.Repeat("很长的一段上下文，用来把正文顶出面板。", 8),
		longList(3), protocol.InteractionSelect)
	m.openAskReader()
	if !m.askReaderMode {
		t.Fatal("阅读器没打开，这条测试什么都没测")
	}
	for _, l := range strings.Split(m.View().Content, "\n") {
		if strings.HasPrefix(l, dimNeutralSeq) {
			t.Errorf("阅读器里出现了压暗行:\n%q", l)
		}
	}
	m.closeAskReader()
	if !m.askDimActive() {
		t.Error("关掉阅读器后背景该重新压暗 —— 判据不该记住模式")
	}
}
