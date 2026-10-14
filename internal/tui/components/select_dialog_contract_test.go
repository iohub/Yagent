package components

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// stripANSICodes 移除 SGR 转义序列（lipgloss 样式输出），便于测量可见宽度与比对文本。
// 这是 components 包测试唯一的 ANSI 剥离器——不要再加第二份（resume 测试里那份手写字节循环已并入此处）。
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSICodes(s string) string { return ansiRE.ReplaceAllString(s, "") }

// selectProbe 把三个选择类对话框（AgentSelect / ModelSelect / LLMTimeout）共用的
// 按键契约抽象成一组闭包。此前这份契约在三个文件里各手抄了一份，改一处忘两处
// 就是这个结构要治的病。
type selectProbe struct {
	key      func(rune)
	cursor   func() int
	selected func() string

	items   int                // 条目数（末行游标的期望值）
	labelAt func(i int) string // 第 i 项确认后 Selected 的期望值
	digit   rune               // 对主列表必然有效的数字键
	// small 构造只放少量条目的小列表（越界数字键用例用）；nil = 直接用主列表 + badDigit。
	small    func() *selectProbe
	badDigit rune // 越界数字键

	hasEndKeys   bool // g/G 跳转（timeout 没有）
	hasTabKey    bool // tab 向下循环（只有 timeout 有）
	digitSelects bool // 数字键是否同时确认（agent/model 是，timeout 只移游标）
}

func runSelectDialogKeyContract(t *testing.T, name string, build func() *selectProbe) {
	t.Helper()

	t.Run("j_k_与方向键移动", func(t *testing.T) {
		n := build()
		n.key('j')
		if got := n.cursor(); got != 1 {
			t.Errorf("%s: after j cursor = %d, want 1", name, got)
		}
		n.key(tea.KeyDown)
		if got := n.cursor(); got != 2 {
			t.Errorf("%s: after down cursor = %d, want 2", name, got)
		}
		n.key('k')
		if got := n.cursor(); got != 1 {
			t.Errorf("%s: after k cursor = %d, want 1", name, got)
		}
		n.key(tea.KeyUp)
		if got := n.cursor(); got != 0 {
			t.Errorf("%s: after up cursor = %d, want 0", name, got)
		}
	})

	t.Run("首尾相接", func(t *testing.T) {
		n := build()
		n.key('k') // 从首行向上应绕到末行
		if got := n.cursor(); got != n.items-1 {
			t.Errorf("%s: wrap up cursor = %d, want %d", name, got, n.items-1)
		}
		n.key('j') // 从末行向下应绕回首行
		if got := n.cursor(); got != 0 {
			t.Errorf("%s: wrap down cursor = %d, want 0", name, got)
		}
	})

	if probe := build(); probe.hasEndKeys {
		t.Run("g_G跳转首尾", func(t *testing.T) {
			n := build()
			n.key('G')
			if got := n.cursor(); got != n.items-1 {
				t.Errorf("%s: G cursor = %d, want %d", name, got, n.items-1)
			}
			n.key('g')
			if got := n.cursor(); got != 0 {
				t.Errorf("%s: g cursor = %d, want 0", name, got)
			}
		})
	} else {
		// timeout 没有 g/G —— 原测试断言过这一点，不能因共享化而静默漏出。
		t.Run("不支持的g_G不动游标", func(t *testing.T) {
			n := build()
			n.key('G')
			n.key('g')
			if got := n.cursor(); got != 0 {
				t.Errorf("%s: unsupported g/G moved cursor to %d, want 0", name, got)
			}
		})
	}

	if probe := build(); probe.hasTabKey {
		t.Run("tab向下循环", func(t *testing.T) {
			n := build()
			n.key(tea.KeyTab)
			if got := n.cursor(); got != 1 {
				t.Errorf("%s: after tab cursor = %d, want 1", name, got)
			}
		})
	}

	t.Run("方向键与Enter确认", func(t *testing.T) {
		n := build()
		n.key(tea.KeyDown)
		n.key(tea.KeyEnter)
		if got := n.selected(); got != n.labelAt(1) {
			t.Errorf("%s: down+Enter Selected = %q, want %q", name, got, n.labelAt(1))
		}
	})

	t.Run("数字键直选", func(t *testing.T) {
		n := build()
		n.key(n.digit)
		want := int(n.digit - '1')
		if got := n.cursor(); got != want {
			t.Errorf("%s: digit %c cursor = %d, want %d", name, n.digit, got, want)
		}
		if n.digitSelects {
			if got := n.selected(); got != n.labelAt(want) {
				t.Errorf("%s: digit %c Selected = %q, want %q", name, n.digit, got, n.labelAt(want))
			}
		}
	})

	t.Run("越界数字键忽略", func(t *testing.T) {
		n := build()
		if n.small != nil {
			n = n.small()
		}
		start := n.cursor()
		n.key(n.badDigit)
		if got := n.cursor(); got != start {
			t.Errorf("%s: out-of-range digit %c moved cursor %d → %d", name, n.badDigit, start, got)
		}
		if n.digitSelects && n.selected() != "" {
			t.Errorf("%s: out-of-range digit must not select, Selected = %q", name, n.selected())
		}
	})

	t.Run("Esc取消", func(t *testing.T) {
		n := build()
		n.key(n.digit)
		n.key(tea.KeyEscape)
		if got := n.selected(); got != "" {
			t.Errorf("%s: after Esc Selected = %q, want empty", name, got)
		}
	})
}

// listDialog 是共享渲染契约需要的最小对话框面。
type listDialog interface {
	SetBounds(w, h int)
	View() string
	Update(tea.Msg) (Component, tea.Cmd)
}

// runListDialogRenderContract 对带滚动窗口的列表对话框跑共享渲染契约：
// FitsTerminal / 滚动到末行可见 / 单条目无滚动提示 / 空列表与小终端。
// label 把条目下标映射为 View() 中必然出现的目标名。
func runListDialogRenderContract(t *testing.T, name string,
	newList func(count int) listDialog, label func(count, i int) string) {
	t.Helper()

	t.Run("FitsTerminal", func(t *testing.T) {
		for _, count := range []int{1, 3, 9, 15, 20, 24, 40} {
			for _, size := range [][2]int{{120, 30}, {100, 24}, {80, 20}, {60, 16}, {44, 12}} {
				w, h := size[0], size[1]
				d := newList(count)
				d.SetBounds(w, h)
				out := d.View()
				if out == "" {
					continue // 尺寸低于渲染下限
				}
				if got := lipgloss.Height(out); got > h {
					t.Errorf("%s entries=%d size=%dx%d: rendered height %d exceeds terminal height %d", name, count, w, h, got, h)
				}
				if got := lipgloss.Width(out); got > w {
					t.Errorf("%s entries=%d size=%dx%d: rendered width %d exceeds terminal width %d", name, count, w, h, got, w)
				}
			}
		}
	})

	t.Run("滚动到末行可见", func(t *testing.T) {
		const count = 24
		d := newList(count)
		d.SetBounds(100, 16)
		for i := 0; i < count-1; i++ {
			d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		}
		out := stripANSICodes(d.View())
		if out == "" {
			t.Fatalf("%s: view empty at 100x16", name)
		}
		if want := label(count, count-1); !strings.Contains(out, want) {
			t.Errorf("%s: cursor row %q not visible after scrolling to the end\n%s", name, want, out)
		}
		if !strings.Contains(out, "more") {
			t.Errorf("%s: expected a scroll indicator\n%s", name, out)
		}
	})

	t.Run("单条目无滚动提示", func(t *testing.T) {
		d := newList(1)
		d.SetBounds(100, 24)
		out := stripANSICodes(d.View())
		if out == "" {
			t.Fatalf("%s: single-entry view empty at 100x24", name)
		}
		if !strings.Contains(out, label(1, 0)) {
			t.Errorf("%s: single entry should still render its name %q\n%s", name, label(1, 0), out)
		}
		if strings.Contains(out, "more") {
			t.Errorf("%s: single entry should not render a scroll indicator\n%s", name, out)
		}
		if strings.Contains(out, "1-9 jump") {
			t.Errorf("%s: single entry should not advertise number-key jump\n%s", name, out)
		}
	})

	t.Run("空列表与小终端", func(t *testing.T) {
		d := newList(0)
		if got := d.View(); got != "" {
			t.Errorf("%s: empty list before SetBounds = %q, want \"\"", name, got)
		}
		d.SetBounds(120, 30)
		if got := d.View(); got != "" {
			t.Errorf("%s: empty list = %q, want \"\"", name, got)
		}
		// 空列表下按键不应 panic
		d.Update(tea.KeyPressMsg{Code: 'j'})
		d.Update(tea.KeyPressMsg{Code: 'G'})

		small := newList(3)
		small.SetBounds(20, 30)
		if got := small.View(); got != "" {
			t.Errorf("%s: narrow terminal = %q, want \"\"", name, got)
		}
	})
}

// ── 三个对话框的 table 入口 ─────────────────────────────────────────────

func TestSelectDialogs_KeyContract(t *testing.T) {
	agentTargets := []string{"global", "director", "coding", "repo", "chat", "meta", "devops", "browser", "deepthinking"}
	modelProviders := []string{
		"autodl_glm53flash", "autodl_dsv41_flash", "localai", "qn_dsv4_flash", "qn_glm51",
		"siliconflow_ds4_flash", "siliconflow_dsv32", "siliconflow_qwen35a3b", "private_endpoint",
	}
	timeoutValues := []string{"1m", "5m", "10m", "30m"}

	// bigAgent / bigModel 主列表（9 项，游标在 0）；small 版本只放 3 项供越界数字键用例。
	// 先声明再赋值：agentProbe 的函数体里递归调用自己，`:=` 形式在 Go 里不可见自身。
	var agentProbe func(entries []ConfigEntry, labels []string, digit rune) *selectProbe
	agentProbe = func(entries []ConfigEntry, labels []string, digit rune) *selectProbe {
		d := NewAgentSelectDialog(entries)
		d.SetBounds(120, 30)
		p := &selectProbe{
			key:          func(r rune) { d.Update(tea.KeyPressMsg{Code: r}) },
			cursor:       func() int { return d.cursor },
			selected:     func() string { return d.Selected },
			items:        len(labels),
			labelAt:      func(i int) string { return labels[i] },
			digit:        digit,
			badDigit:     '7',
			hasEndKeys:   true,
			digitSelects: true,
		}
		if digit == '9' {
			p.small = func() *selectProbe { return agentProbe(testConfigEntries(3), nil, 0) }
		}
		return p
	}
	var modelProbe func(opts []ModelOption, labels []string, digit rune) *selectProbe
	modelProbe = func(opts []ModelOption, labels []string, digit rune) *selectProbe {
		d := NewModelSelectDialog(opts, "")
		d.SetBounds(120, 30)
		p := &selectProbe{
			key:          func(r rune) { d.Update(tea.KeyPressMsg{Code: r}) },
			cursor:       func() int { return d.cursor },
			selected:     func() string { return d.Selected },
			items:        len(labels),
			labelAt:      func(i int) string { return labels[i] },
			digit:        digit,
			badDigit:     '7',
			hasEndKeys:   true,
			digitSelects: true,
		}
		if digit == '5' {
			p.small = func() *selectProbe { return modelProbe(testModelOptions(3), nil, 0) }
		}
		return p
	}
	timeoutProbe := func() *selectProbe {
		d := NewLLMTimeoutDialog(timeoutInfo())
		d.cursor = 0 // 契约用例统一从首行出发；"游标落在当前值"由专测覆盖
		return &selectProbe{
			key:       func(r rune) { d.Update(tea.KeyPressMsg{Code: r}) },
			cursor:    func() int { return d.cursor },
			selected:  func() string { return d.Selected },
			items:     len(timeoutValues),
			labelAt:   func(i int) string { return timeoutValues[i] },
			digit:     '3', // 对 4 项主列表有效；越界用例改用 badDigit='9'（无 small 列表）
			badDigit:  '9',
			hasTabKey: true,
		}
	}

	t.Run("AgentSelectDialog", func(t *testing.T) {
		runSelectDialogKeyContract(t, "agent", func() *selectProbe {
			return agentProbe(testConfigEntries(9), agentTargets, '9')
		})
	})
	t.Run("ModelSelectDialog", func(t *testing.T) {
		runSelectDialogKeyContract(t, "model", func() *selectProbe {
			return modelProbe(testModelOptions(9), modelProviders, '5')
		})
	})
	t.Run("LLMTimeoutDialog", func(t *testing.T) {
		runSelectDialogKeyContract(t, "timeout", timeoutProbe)
	})
}

func TestListDialogs_RenderContract(t *testing.T) {
	t.Run("AgentSelectDialog", func(t *testing.T) {
		runListDialogRenderContract(t, "agent",
			func(count int) listDialog {
				return NewAgentSelectDialog(testConfigEntries(count))
			},
			func(count, i int) string { return testConfigEntries(count)[i].Target })
	})
	t.Run("ModelSelectDialog", func(t *testing.T) {
		runListDialogRenderContract(t, "model",
			func(count int) listDialog {
				return NewModelSelectDialog(testModelOptions(count), "")
			},
			func(count, i int) string { return testModelOptions(count)[i].Provider })
	})
}
