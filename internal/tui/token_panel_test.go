package tui

import (
	"strings"
	"testing"
	"time"

	"yagent/internal/messaging"

	"charm.land/lipgloss/v2"
)

// s1Model 构造与批准视觉稿 S1 等价的数据：全局累计 + 两个 provider 口径下的缓存字段。
// 数值都按 formatToken 的真实输出能力选取。
func s1Model() *model {
	return &model{
		totalInputTokens:         2350000,
		inputTokens:              680000, // Anthropic 语义：不含缓存。Σ 绝不能只用它
		outputTokens:             58200,
		cacheReadInputTokens:     1670000,
		cacheCreationInputTokens: 800,
		contextWindow:            200000,
		runAgent:                 "director",
		tokenUsagePerAgent: map[string]*AgentTokenUsage{
			"director": {AgentName: "director", TotalInputTokens: 1760000, InputTokens: 1200000, OutputTokens: 60000, CacheReadInputTokens: 1302400},
			"coding":   {AgentName: "coding", TotalInputTokens: 350000, InputTokens: 200000, OutputTokens: 34200, CacheReadInputTokens: 203000},
			"repo":     {AgentName: "repo", TotalInputTokens: 90000, InputTokens: 40000, OutputTokens: 6400, CacheReadInputTokens: 59400},
			"browser":  {AgentName: "browser", TotalInputTokens: 40000, InputTokens: 20000, OutputTokens: 2800, CacheReadInputTokens: 12400},
		},
		runTokensPerAgent: map[string]*AgentRunTokens{
			"director": {AgentName: "director", TotalInputTokens: 12000, InputTokens: 12000, OutputTokens: 400, ContextTokens: 128400},
			"coding":   {AgentName: "coding", TotalInputTokens: 7000, InputTokens: 7000, OutputTokens: 1200},
			"repo":     {AgentName: "repo", TotalInputTokens: 2500, InputTokens: 2500, OutputTokens: 600},
			"browser":  {AgentName: "browser", TotalInputTokens: 1000, InputTokens: 1000, OutputTokens: 200},
		},
	}
}

func s1Spec(inner int) tokenPanelSpec {
	return tokenPanelSpec{innerWidth: inner, keycapKey: "alt+d", keycapLabel: "收缩"}
}

// TestTokenPanel_GoldenS1 钉住批准稿：侧栏 inner=46 时逐行等于设计文档 §2.3。
func TestTokenPanel_GoldenS1(t *testing.T) {
	m := s1Model()
	rows := buildTokenPanel(m.tokenSnapshot(), s1Spec(46))

	want := []string{
		"◆ Tokens                           alt+d 收缩 ",
		"──────────────────────────────────────────────",
		"Σ 2.41m                   in 2.35m · out 58.2k",
		"⊕ ▰▰▰▰▰▰▰▱▱▱ 71%           hit 1.67m · new 800",
		"⛁ ▰▰▰▰▰▰▱▱▱▱ 64%               128.4k / 200.0k",
		"──────────────────────────────────────────────",
		"  AGENT         RUN ▸   TOTAL          ⊕ CACHE",
		"● director    12.4k ▸   1.82m             ⊕74%",
		"● coding       8.2k ▸  384.2k             ⊕58%",
		"● repo         3.1k ▸   96.4k             ⊕66%",
		"● browser      1.2k ▸   42.8k             ⊕31%",
	}
	if len(rows) != len(want) {
		t.Fatalf("行数 = %d, want %d\n----\n%s----", len(rows), len(want), stripANSI(strings.Join(rows, "\n")))
	}
	for i, w := range want {
		got := stripANSI(rows[i])
		if got != w {
			t.Errorf("line %d:\n got %q\nwant %q", i, got, w)
		}
		if lipgloss.Width(rows[i]) != 46 {
			t.Errorf("line %d 宽度 = %d, want 46（%q）", i, lipgloss.Width(rows[i]), got)
		}
	}
}

// TestTokenPanel_WidthLadder 验证宽度阶梯：任何一档都不许超出 inner，
// 列头只在 inner>=46 出现，宽档才把每 agent 的缓存明细摊开。
func TestTokenPanel_WidthLadder(t *testing.T) {
	for _, inner := range []int{30, 34, 40, 46, 52, 60, 90, 114} {
		m := s1Model()
		rows := buildTokenPanel(m.tokenSnapshot(), s1Spec(inner))
		if len(rows) == 0 {
			t.Fatalf("inner=%d 应有内容", inner)
		}
		joined := stripANSI(strings.Join(rows, "\n"))
		for i, r := range rows {
			if w := lipgloss.Width(r); w > inner {
				t.Errorf("inner=%d line %d 宽度 %d 超界：%q", inner, i, w, stripANSI(r))
			}
		}
		if !strings.HasPrefix(stripANSI(rows[0]), "◆ Tokens") {
			t.Errorf("inner=%d 首行应是标题，实际 %q", inner, stripANSI(rows[0]))
		}
		hasHeader := strings.Contains(joined, "AGENT")
		if wantHeader := inner >= tokenHeaderFrom; hasHeader != wantHeader {
			t.Errorf("inner=%d 列头存在=%v, want %v", inner, hasHeader, wantHeader)
		}
		// 46 档必须与 golden 完全一致
		if inner == 46 {
			golden := buildTokenPanel(s1Model().tokenSnapshot(), s1Spec(46))
			if strings.Join(rows, "\n") != strings.Join(golden, "\n") {
				t.Errorf("inner=46 与 golden 不一致")
			}
		}
		// 横条档（inner>=46）按 46 列一段平铺；更窄则一行一个
		agentRows := 0
		for _, r := range rows {
			if strings.Contains(stripANSI(r), "●") {
				agentRows++
			}
		}
		want := len(m.tokenSnapshot().agents)
		if inner >= tokenFlowFrom {
			perRow := agentFlowPerRow(inner)
			expected := (want + perRow - 1) / perRow
			if agentRows != expected {
				t.Errorf("inner=%d 每行 %d 段，%d 个 agent 应占 %d 行，实际 %d 行", inner, perRow, want, expected, agentRows)
			}
		} else if agentRows != want {
			t.Errorf("inner=%d 应一行一个 agent（共 %d 个），实际 %d 行", inner, want, agentRows)
		}
	}
}

// TestTokenPanel_HeightLadder 验证高度阶梯：行数不超预算，标题/Σ/⛁ 永不被砍，
// 列头在预算不足时先消失。
func TestTokenPanel_HeightLadder(t *testing.T) {
	for _, maxRows := range []int{3, 4, 5, 6, 7, 8, 9, 10, 12} {
		m := s1Model()
		rows := buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{
			innerWidth:  46,
			maxRows:     maxRows,
			keycapKey:   "alt+d",
			keycapLabel: "收缩",
		})
		if len(rows) > maxRows {
			t.Errorf("maxRows=%d 渲染了 %d 行\n----\n%s----", maxRows, len(rows), stripANSI(strings.Join(rows, "\n")))
		}
		joined := stripANSI(strings.Join(rows, "\n"))
		for _, must := range []string{"◆ Tokens", "Σ 2.41m", "⛁"} {
			if !strings.Contains(joined, must) {
				t.Errorf("maxRows=%d 时 %q 不应被砍\n----\n%s----", maxRows, must, joined)
			}
		}
		// 结构不变量：列头出现时下面必须真的有 agent 行（不能画空表格）
		hasHeader := strings.Contains(joined, "AGENT")
		hasAgents := strings.Contains(joined, "●")
		if hasHeader && !hasAgents {
			t.Errorf("maxRows=%d 出现列头却没有 agent 行\n----\n%s----", maxRows, joined)
		}
		// 截断提示必须与实际显示的行数自洽
		if more := strings.Count(joined, "┈ +"); more > 1 {
			t.Errorf("maxRows=%d 出现 %d 条截断提示，最多只能有 1 条\n----\n%s----", maxRows, more, joined)
		}
		if maxRows <= 5 && (hasAgents || strings.Contains(joined, "┈")) {
			t.Errorf("maxRows=%d 时固定行已占满预算，不该再挤出 agent 内容\n----\n%s----", maxRows, joined)
		}
		if maxRows >= 6 && !strings.Contains(joined, "┈") && !hasAgents {
			t.Errorf("maxRows=%d 至少该给出 agent 摘要行\n----\n%s----", maxRows, joined)
		}
	}
}

// TestTokenPanel_SigmaUsesNormalizedInput 钉住 §8 缺陷 2：
// Anthropic 形态（input 不含缓存）下 Σ 必须把缓存算进输入总量。
func TestTokenPanel_SigmaUsesNormalizedInput(t *testing.T) {
	m := newTestModel()
	m.inputTokens = 100 // 非缓存输入
	m.cacheReadInputTokens = 900
	m.totalInputTokens = 1000
	m.outputTokens = 50

	rows := buildTokenPanel(m.tokenSnapshot(), s1Spec(46))
	joined := stripANSI(strings.Join(rows, "\n"))
	if !strings.Contains(joined, "Σ 1.1k") {
		t.Errorf("Σ 应为归一化输入+输出 = 1.1k，实际\n----\n%s----", joined)
	}
	if strings.Contains(joined, "Σ 150") {
		t.Errorf("Σ 又退回 inputTokens+outputTokens 的漏算口径：\n----\n%s----", joined)
	}
}

// TestTokenPanel_PerAgentRunIndependent 钉住 RUN 列的语义：
// agent 切换不再清掉别人的本轮用量。
func TestTokenPanel_PerAgentRunIndependent(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, ctxTestAiResponse("Director", 1000))
	m = feedEvent(m, ctxTestAiResponse("Repo-Agent", 500))

	if d := m.runTokensPerAgent["Director"]; d == nil || d.TotalInputTokens != 1000 {
		t.Errorf("Director 本轮输入 = %+v, want 1000", d)
	}
	if r := m.runTokensPerAgent["Repo-Agent"]; r == nil || r.TotalInputTokens != 500 {
		t.Errorf("Repo-Agent 本轮输入 = %+v, want 500", r)
	}
	if m.runAgent != "Repo-Agent" {
		t.Errorf("活跃 run 应跟到最后一条事件的 agent，实际 %q", m.runAgent)
	}

	rows := buildTokenPanel(m.tokenSnapshot(), s1Spec(46))
	joined := stripANSI(strings.Join(rows, "\n"))
	// Director: run 1000+10=1010 → 1.0k；Repo-Agent: run 510 → 510
	if !strings.Contains(joined, "1.0k ▸") || !strings.Contains(joined, "510 ▸") {
		t.Errorf("两行 RUN 应各自独立显示，实际\n----\n%s----", joined)
	}
}

// TestTokenPanel_RunResetOnSubmit 验证新一轮提交只清 RUN，不动 Σ 与 TOTAL。
func TestTokenPanel_RunResetOnSubmit(t *testing.T) {
	m := newTestModel()
	m = feedEvent(m, ctxTestAiResponse("Director", 1000))

	before := m.tokenSnapshot()
	m.resetRunTokens()
	after := m.tokenSnapshot()

	if len(after.agents) != len(before.agents) {
		t.Fatalf("resetRunTokens 不该改变 agent 行数：%+v vs %+v", after.agents, before.agents)
	}
	for _, a := range after.agents {
		if a.run() != 0 {
			t.Errorf("%s 的 RUN 应归零，实际 %d", a.name, a.run())
		}
		if a.total() != 1010 {
			t.Errorf("%s 的 TOTAL 不该被清，实际 %d", a.name, a.total())
		}
	}
	if after.totalIn != before.totalIn || after.totalOut != before.totalOut {
		t.Errorf("Σ 不该被 resetRunTokens 影响：%+v vs %+v", after, before)
	}
	if after.ctxTokens != 0 {
		t.Errorf("⛁ 行应随本轮一起归零，实际 %d", after.ctxTokens)
	}
}

// TestTokenPanel_FooterAndSidebarAgree 是两个表面口径一致的唯一直接证据：
// 同一个 model 在侧栏与底部表面上，Σ / 每 agent TOTAL / 命中率必须一模一样。
func TestTokenPanel_FooterAndSidebarAgree(t *testing.T) {
	testLang(t)
	m := s1Model()
	m.termWidth = 140
	sidebar := stripANSI(strings.Join(buildTokenPanel(m.tokenSnapshot(), s1Spec(46)), "\n"))

	m.termWidth = 100 // 侧栏不可见 → 底部面板
	footer := stripANSI(m.renderFooterTokenPanel())
	for _, frag := range []string{"Σ 2.41m", "in 2.35m · out 58.2k", "71%", "1.82m", "384.2k"} {
		if !strings.Contains(sidebar, frag) {
			t.Errorf("侧栏缺少 %q", frag)
		}
		if !strings.Contains(footer, frag) {
			t.Errorf("底部面板缺少 %q\n----\n%s----", frag, footer)
		}
	}
}

// TestTokenPanel_FooterFitsTermWidth 验证底部面板在任何终端宽度下都不撑破边框，
// 且 View() 行数恰好等于终端高度（AGENTS.md 坑 7 的直接闸门）。
func TestTokenPanel_FooterFitsTermWidth(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{60, 30}, {80, 34}, {100, 34}, {119, 40}} {
		for _, collapsed := range []bool{false, true} {
			m := chromeTestModel(t, sz.w, sz.h)
			m.taskStarted = true
			m.inputTokens = 120000
			m.outputTokens = 34000
			m.totalInputTokens = 140000
			m.cacheReadInputTokens = 90000
			m.contextWindow = 200000
			m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
				"Director": {AgentName: "Director", TotalInputTokens: 140000, InputTokens: 50000, OutputTokens: 34000, CacheReadInputTokens: 90000},
			}
			m.setRun(AgentRunTokens{AgentName: "Director", TotalInputTokens: 1200, InputTokens: 1000, OutputTokens: 200, ContextTokens: 45000})
			m.tokenDashboardCollapsed = collapsed
			m.invalidateFooterCache()

			block := m.tokenDashboardBlock()
			if block == "" {
				t.Fatalf("w=%d collapsed=%v 底部面板不该为空", sz.w, collapsed)
			}
			for i, line := range strings.Split(block, "\n") {
				if w := lipgloss.Width(line); w > sz.w {
					t.Errorf("w=%d collapsed=%v line %d 宽度 %d 超界：%q", sz.w, collapsed, i, w, stripANSIForBorder(line))
				}
			}
			if got := strings.Count(block, "\n") + 1; got != lipgloss.Height(m.cachedTokenDashboard)+1 {
				t.Errorf("w=%d collapsed=%v tokenDashboardBlock 行数 %d 与缓存面板高度 %d 不自洽",
					sz.w, collapsed, got, lipgloss.Height(m.cachedTokenDashboard)+1)
			}

			view := m.View()
			if lines := strings.Count(view.Content, "\n") + 1; lines != sz.h {
				t.Errorf("w=%d h=%d collapsed=%v: View 行数 = %d, want %d（footer 高度与 View 不自洽）",
					sz.w, sz.h, collapsed, lines, sz.h)
			}
		}
	}
}

// TestTokenPanel_AiResponseFeedsSnapshot 端到端检查事件 → 快照 → 面板的取数链，
// 覆盖 OpenAI 形态（prompt 含缓存）与 total_input 缺省的回落。
func TestTokenPanel_AiResponseFeedsSnapshot(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "ok",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":               1000, // 已含 cached
				"completion_tokens":           200,
				"total_tokens":                1200,
				"cache_read_input_tokens":     300,
				"cache_creation_input_tokens": 100,
				"total_input_tokens":          1000,
			},
		},
	})

	snap := m.tokenSnapshot()
	if snap.totalIn != 1000 {
		t.Errorf("totalIn = %d, want 1000（OpenAI 口径下不能再加缓存）", snap.totalIn)
	}
	// 命中率 300/1000 = 30%：10 格里填 3 格
	got := stripANSI(tokenCacheMeterRow(snap, 46, tokenMeterCells))
	if !strings.Contains(got, " 30%") || strings.Count(got, "▰") != 3 {
		t.Errorf("计量条应显示 3 格填充与 30%%，实际 %q", got)
	}

	// total_input 缺省 → 回落三项相加
	m2 := newTestModel()
	m2 = feedEvent(m2, &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "ok",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":           400,
				"completion_tokens":       100,
				"total_tokens":            500,
				"cache_read_input_tokens": 200,
			},
		},
	})
	if got := m2.tokenSnapshot().totalIn; got != 600 {
		t.Errorf("缺 total_input 时 totalIn = %d, want 400+200=600", got)
	}
}
