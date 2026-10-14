package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// stripANSILines 是 stripANSI 的批量形态：本包的 golden 一律"逐行去色后整行比对"。
// ANSI 剥离在本包只有这一处实现，测试文件不要再各自补一个本地版本。
func stripANSILines(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = stripANSI(r)
	}
	return out
}

// testLang 把测试侧的语言管理器初始化统一收口到生产入口 InitLangManager()，
// 并用 t.Cleanup 还原它动过的全局状态（currentLang）。
// 本包的宽度与 golden 断言全部依赖英文文案：这里显式钉住英文，
// 顺带消掉"前一个测试改了语言、后一个测试的宽度断言跟着变"的顺序依赖。
func testLang(t *testing.T) {
	t.Helper()
	InitLangManager()
	prev := langManager.currentLang
	langManager.SetLanguage(LangEnglish)
	t.Cleanup(func() { langManager.SetLanguage(prev) })
}

// initLangManager 只留给尚未收编的旧调用点（不在本次改动范围内的测试文件）。
// 新写/改动的测试请直接用 testLang —— 初始化路径仍然是生产的 InitLangManager 一条。
func initLangManager() { InitLangManager() }

// dashboardAgentUsage 是 border fixture 的共用形状（只有数值不同）。
func dashboardAgentUsage(name string, in, out, cached, created int64) *AgentTokenUsage {
	return &AgentTokenUsage{
		AgentName:                name,
		InputTokens:              in,
		OutputTokens:             out,
		CacheReadInputTokens:     cached,
		CacheCreationInputTokens: created,
	}
}

// TestDashboardBorder_ExactGeometry 钉住 renderDashboard 的几何不变量：
// 恰好 height 行、每行恰好 width 格、四角完整、每一行的首尾都是边框字符。
// 运行中 / 空闲 / 内容溢出三个形态共用同一套断言，只有"内容比面板短时补白行
// 必须带边框"（noBlankPaddingLine）是空闲形态的专属期望 —— 它是那个变体里唯一
// 别处没有的断言。溢出形态用更矮的高度构造"可放行数不够"。
func TestDashboardBorder_ExactGeometry(t *testing.T) {
	const width = dashboardPanelWidth // 46

	cases := []struct {
		name               string
		height             int
		build              func() *model
		noBlankPaddingLine bool
	}{
		{
			name:   "running",
			height: 30,
			build: func() *model {
				return &model{
					termWidth:                140,
					termHeight:               40,
					taskRunning:              true,
					inputTokens:              1234000,
					outputTokens:             567000,
					cacheReadInputTokens:     100000,
					cacheCreationInputTokens: 50000,
					tokenUsagePerAgent: map[string]*AgentTokenUsage{
						"Director": dashboardAgentUsage("Director", 500000, 200000, 50000, 20000),
						"Coder":    dashboardAgentUsage("Coder", 300000, 150000, 30000, 10000),
					},
					timelineEntries:    []*TimelineEntry{},
					dashboardCollapsed: false,
				}
			},
		},
		{
			name:   "idle",
			height: 30,
			build: func() *model {
				return &model{
					termWidth:                140,
					termHeight:               40,
					taskRunning:              false,
					inputTokens:              100,
					outputTokens:             50,
					cacheReadInputTokens:     0,
					cacheCreationInputTokens: 0,
					tokenUsagePerAgent:       map[string]*AgentTokenUsage{},
					timelineEntries:          []*TimelineEntry{},
					dashboardCollapsed:       false,
				}
			},
			noBlankPaddingLine: true,
		},
		{
			name:   "overflow",
			height: 15,
			build: func() *model {
				// 20 个 agent 把内容撑到超过可放行数，逼出行数收敛那条路径
				agents := make(map[string]*AgentTokenUsage, 20)
				for i := 0; i < 20; i++ {
					name := fmt.Sprintf("Agent%d", i)
					agents[name] = dashboardAgentUsage(name,
						int64(1000+i*100), int64(500+i*50), int64(100+i*10), int64(50+i*5))
				}
				return &model{
					termWidth:                140,
					termHeight:               40,
					taskRunning:              true,
					inputTokens:              1234000,
					outputTokens:             567000,
					cacheReadInputTokens:     100000,
					cacheCreationInputTokens: 50000,
					tokenUsagePerAgent:       agents,
					timelineEntries:          []*TimelineEntry{},
					dashboardCollapsed:       false,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testLang(t)
			m := tc.build()

			result := m.renderDashboard(width, tc.height)
			lines := strings.Split(result, "\n")
			if len(lines) != tc.height {
				t.Fatalf("期望 %d 行，实际 %d 行\n----\n%s----", tc.height, len(lines), stripANSI(result))
			}

			// 四角：首行 ╭…╮、末行 ╰…╯（截断后底边框也必须留着）
			if first := stripANSI(lines[0]); !strings.HasPrefix(first, "╭") || !strings.HasSuffix(first, "╮") {
				t.Errorf("首行应是顶边框 ╭…╮，实际 %q", first)
			}
			if last := stripANSI(lines[tc.height-1]); !strings.HasPrefix(last, "╰") || !strings.HasSuffix(last, "╯") {
				t.Errorf("末行应是底边框 ╰…╯，实际 %q", last)
			}

			for i, line := range lines {
				if w := lipgloss.Width(line); w != width {
					t.Errorf("line %d: 期望宽度 %d，实际 %d（content=%q）", i, width, w, stripANSI(line))
				}
				s := stripANSI(line)
				runes := []rune(s)
				if len(runes) == 0 {
					t.Fatalf("line %d: 空行", i)
				}
				if first := string(runes[0]); first != "│" && first != "╭" && first != "╰" {
					t.Errorf("line %d: 期望左边框字符，实际 %q", i, first)
				}
				if l := string(runes[len(runes)-1]); l != "│" && l != "╮" && l != "╯" {
					t.Errorf("line %d: 期望右边框字符，实际 %q", i, l)
				}
				// 空闲形态专属：短内容下的补白行也必须带边框，不能退化成整行空格
				if tc.noBlankPaddingLine && strings.TrimSpace(s) == "" {
					t.Errorf("line %d: 期望带边框的补白行，实际整行空格", i)
				}
			}
		})
	}
}
