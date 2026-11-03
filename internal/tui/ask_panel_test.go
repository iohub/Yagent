package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"yagent/internal/messaging"
	"yagent/internal/protocol"

	"charm.land/lipgloss/v2"
)

// 决策面板家族的闸门（docs/ask-panel-redesign.md §12）。
//
// 断言一律走**精确的显示宽度/行数**而不是子串（internal/tui/AGENTS.md 测试约定）：
// 这块面板的全部风险都在"行数与拼接对不对得上"，子串断言看不见那种漂移。

// askTestModel 接真实总线 + 记录器：断言"发出去的那条回答"而不是"内部调了什么函数"，
// 否则测试与生产走的是两条路（根 AGENTS.md §11 坑 33）。
func askTestModel(t *testing.T, w, h int) (*model, *askRecorder) {
	t.Helper()
	m := chromeTestModel(t, w, h)
	m.taskStarted = true
	m.taskRunning = true
	m.askActiveIndex = -1
	rec := &askRecorder{ch: make(chan map[string]interface{}, 16)}
	disp := messaging.NewDefaultMessageDispatcher()
	disp.RegisterConsumer(rec)
	m.publisher = messaging.NewMessagePublisher(disp)
	return m, rec
}

// askRecorder 挂在 dispatcher 上。投递是异步的，所以取用一律带截止时间，
// 而不是 sleep 之后再数 —— 数早了恒真，数晚了测试变慢。
type askRecorder struct{ ch chan map[string]interface{} }

func (r *askRecorder) Consume(event *messaging.MessageEvent) error {
	if event.Type != "user_help_response" {
		return nil
	}
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return nil
	}
	cp := make(map[string]interface{}, len(content))
	for k, v := range content {
		cp[k] = v
	}
	select {
	case r.ch <- cp:
	default:
	}
	return nil
}

// next 等一条回答；等不到就是被测行为没发生，直接失败。
func (r *askRecorder) next(t *testing.T) map[string]interface{} {
	t.Helper()
	select {
	case m := <-r.ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内没有任何 user_help_response 被发布")
	}
	return nil
}

// assertQuiet 断言这段时间内**没有**发布任何东西（负向断言也要有窗口，不能靠运气）。
func (r *askRecorder) assertQuiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case m := <-r.ch:
		t.Errorf("不该发布回答，却发出了 %v", m)
	case <-time.After(d):
	}
}

func feedHelpAsk(m *model, question, context string, options []string, it protocol.InteractionType) *pendingAsk {
	return m.enqueueAsk(map[string]interface{}{
		"question":         question,
		"context":          context,
		"interaction_type": string(it),
		"options":          toAnySlice(options),
		"allow_custom":     true,
		"request_id":       fmt.Sprintf("req-%d", len(m.askQueue)+1),
	}, "Coding")
}

func toAnySlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func longList(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("candidate-%02d · 一个足够长的候选说明", i+1)
	}
	return out
}

// TestAskPanel_WidthIsExact 每一行的显示宽度必须正好等于 termWidth。
// 差一列的代价不是"难看"，是 View 每轮纠正宽度 + 整段正文白排一遍（硬约束 3）。
func TestAskPanel_WidthIsExact(t *testing.T) {
	for _, w := range []int{30, 40, 60, 80, 100, 120, 160, 200, 240} {
		for _, nOpts := range []int{1, 3, 20, 45} {
			m, _ := askTestModel(t, w, 40)
			p := feedHelpAsk(m, "交互模式的判定收归哪一层？",
				"两个候选实现都能改，需要你定方向。", longList(nOpts), protocol.InteractionSelect)
			p.allowCustom = false

			blk := m.askPanelBlock()
			if blk == "" {
				t.Fatalf("w=%d n=%d: 面板没有渲染任何东西", w, nOpts)
			}
			for i, line := range strings.Split(blk, "\n") {
				if got := lipgloss.Width(line); got != w {
					t.Fatalf("w=%d n=%d 第 %d 行宽=%d want %d:\n%q", w, nOpts, i, got, w, line)
				}
			}
		}
	}
}

// TestScreen_NeverExceedsTermHeight 整屏行数契约的全矩阵闸门：
// 面板 + 输入框 + 正文加起来必须正好等于 termHeight。
// 没有这块面板时 footer 与正文恰好填满、零余量，所以各自封顶一定会溢出。
func TestScreen_NeverExceedsTermHeight(t *testing.T) {
	longAnswer := strings.Repeat("这是一段会软折行的长答案，用来验证高度按渲染后的行数算。", 30)
	cases := []struct {
		name    string
		prepare func(m *model)
	}{
		{"无求助", func(m *model) {}},
		{"短求助", func(m *model) {
			feedHelpAsk(m, "要跑完整测试集吗？", "全量约 12s。", []string{"跑全量", "只跑改动包"}, protocol.InteractionConfirm)
		}},
		{"45 项求助", func(m *model) {
			p := feedHelpAsk(m, "选择要审计的模块", "仓库里有 45 个包。", longList(45), protocol.InteractionSelect)
			p.allowCustom = false
		}},
		{"长答案", func(m *model) { m.input.SetValue(longAnswer) }},
		{"长答案 + 45 项 + 长上下文", func(m *model) {
			m.input.SetValue(longAnswer)
			p := feedHelpAsk(m, "选择要审计的模块",
				strings.Repeat("这段上下文非常长，用来把面板顶到极限。", 40), longList(45), protocol.InteractionSelect)
			p.allowCustom = false
		}},
	}
	for _, h := range []int{20, 22, 24, 30, 40, 60} {
		for _, w := range []int{30, 60, 100, 140, 240} {
			for _, c := range cases {
				m, _ := askTestModel(t, w, h)
				seedLog(m, strings.Repeat("log line\n", 200))
				c.prepare(m)
				m.invalidateFooterCache()
				got := strings.Count(m.View().Content, "\n") + 1
				if got != h {
					t.Errorf("w=%d h=%d case=%q: 整屏行数=%d want %d", w, h, c.name, got, h)
				}
			}
		}
	}
}

// TestAskPanel_HintBarKeepsEscape 窄屏时键帽从尾部整段丢弃，
// 所以"怎么出去"必须排在第一位 —— 用户最需要它的时候恰好在最窄的屏上。
func TestAskPanel_HintBarKeepsEscape(t *testing.T) {
	for _, w := range []int{30, 40, 60} {
		m, _ := askTestModel(t, w, 30)
		feedHelpAsk(m, "要跑完整测试集吗？", "", []string{"跑全量", "只跑改动包"}, protocol.InteractionConfirm)
		lines := strings.Split(m.askPanelBlock(), "\n")
		last := stripANSI(lines[len(lines)-1])
		if !strings.HasPrefix(last, "╰─ esc") {
			t.Errorf("w=%d: 底边框不是以 esc 开头 → %q", w, last)
		}
	}
}

// TestAskPanel_FoldMarkerIsPresent 被折叠时必须留下"还有多少没看见"的那一行。
// 今天的实现砍到 3 行之后不留任何痕迹（设计稿 R3）。
func TestAskPanel_FoldMarkerIsPresent(t *testing.T) {
	m, _ := askTestModel(t, 80, 30)
	long := strings.Repeat("这段上下文需要被折叠，因为它明显超过面板给它的两行预算。", 20)
	p := feedHelpAsk(m, "问题", long, []string{"甲", "乙"}, protocol.InteractionSelect)
	p.allowCustom = false
	blk := stripANSI(m.askPanelBlock())
	// 提示行必须同时给出"还剩多少"和"去哪看全文"：只截断不留痕是今天的病（R3）。
	if !strings.Contains(blk, "more") || !strings.Contains(blk, "ctrl+o") {
		t.Errorf("长上下文被折叠，却没有给出行数与展开出口:\n%s", blk)
	}
}

// TestAskPanel_ChoiceRowKeycapsAllWork 把每个键帽逐个按一遍，断言发出去的答案
// 就是键帽旁边那一项。这是 R4 的直接闸门：今天第 10 项起键帽写着 a…k、
// 而按键只 case "1".."9"，按下去什么都没发生。
func TestAskPanel_ChoiceRowKeycapsAllWork(t *testing.T) {
	list := longList(20)
	for i := range list {
		key := askQuickKey(i)
		if key == "" {
			t.Fatalf("第 %d 项没有键帽", i)
		}
		m, rec := askTestModel(t, 100, 40)
		p := feedHelpAsk(m, "选择候选项", "", list, protocol.InteractionSelect)
		p.allowCustom = false
		// 焦点走到第 i 项，然后**按键帽上写的那张键**。
		for p.focusIndex < i {
			p.moveCursor(1, m.askWindowRows())
		}
		handled, _ := m.handleAskKey(key)
		if !handled {
			t.Errorf("键帽 %q 写在屏上却没人认领（第 %d 项）", key, i)
			continue
		}
		if resp, _ := rec.next(t)["response"].(string); resp != list[i] {
			t.Errorf("按键帽 %q 发出的是 %q，want %q", key, resp, list[i])
		}
	}
}

// TestQuickKeyRoundTrip 键帽文本与可点的键必须是同一个定义的两个方向。
func TestQuickKeyRoundTrip(t *testing.T) {
	for i := 0; i < 9+len(quickKeyLabels); i++ {
		key := askQuickKey(i)
		if key == "" {
			t.Fatalf("第 %d 项没有键帽", i)
		}
		if back := pickQuickKey(key); back != i {
			t.Errorf("第 %d 项的键帽 %q 反解成 %d", i, key, back)
		}
	}
	for _, k := range []string{"j", "k", "esc", "enter", "tab", "ctrl+o", "1-9", ""} {
		if got := pickQuickKey(k); got != -1 {
			t.Errorf("%q 不是直选键，却解析成第 %d 项", k, got)
		}
	}
}

// TestAskPanel_WrapDoesNotSplitIdentifiers 硬切必须落在分隔符之后，且优先落在
// 层级分隔符（/）而不是文件名内部的分隔符（_）—— 今天的 wrapPlain 按 rune 切，
// 把路径切成 `...components/user` + `_help_dialog.go`，标识符照样被劈开。
func TestAskPanel_WrapDoesNotSplitIdentifiers(t *testing.T) {
	path := "internal/tui/components/user_help_dialog.go"
	lines := wrapKeepingSeparators("候选实现是 "+path+"，维护五态状态机。", 40)
	for i, l := range lines[:max(0, len(lines)-1)] {
		r := []rune(strings.TrimRight(l, " "))
		if len(r) == 0 {
			continue
		}
		last := r[len(r)-1]
		if last != ' ' && !strings.ContainsRune(string(wrapSeparators), last) {
			t.Errorf("第 %d 行断在标识符中间（结尾是 %q）: %#v", i, string(last), lines)
		}
	}
	joined := strings.Join(lines, "")
	if strings.Contains(joined, "user_help_") && strings.Contains(joined, "dialog.go") {
		for _, l := range lines {
			if strings.HasPrefix(l, "dialog.go") {
				t.Errorf("文件名被从 _ 处劈开，明明有 / 可以作为断点: %#v", lines)
			}
		}
	}
}

// TestAnswerInput_HeightCountsSoftWrappedLines 长答案必须拿到够高的输入框，
// 并且答案的**结尾**真的出现在面板里。今天按 strings.Count("\\n") 数行，
// 单行长答案被算成 1 行，尾部被 textarea 内部裁掉（设计稿 §6.4 第 2 条）。
func TestAnswerInput_HeightCountsSoftWrappedLines(t *testing.T) {
	m, _ := askTestModel(t, 100, 40)
	long := strings.Repeat("abcd", 250) // 1000 字符，无换行
	m.input.SetValue(long)
	m.invalidateFooterCache()
	if got := m.computeInputHeight(); got < 11 {
		t.Errorf("1000 字符单行答案只给 %d 行，尾部会被裁掉", got)
	}
	panel := stripANSI(m.renderEditorPanel())
	if tail := long[len(long)-8:]; !strings.Contains(panel, tail) {
		t.Errorf("答案尾部 %q 不在可见区里:\n%s", tail, panel)
	}
}

// TestAskQueue_SecondRequestIsNotDropped 并发委派同时问两个问题时，
// 今天第二条被静默丢弃（实测栈深 1→1），而提问的 Agent 一直阻塞等回答。
func TestAskQueue_SecondRequestIsNotDropped(t *testing.T) {
	m, rec := askTestModel(t, 100, 32)
	m.eventCh = make(chan *messaging.MessageEvent, 4)
	feedEvent(m, buildEvent("user_help_needed", "Coding", map[string]interface{}{
		"question": "第一个问题", "interaction_type": "confirm",
		"options": []interface{}{"yes", "no"}, "request_id": "q1",
	}))
	feedEvent(m, buildEvent("user_help_needed", "Repo", map[string]interface{}{
		"question": "第二个问题", "interaction_type": "confirm",
		"options": []interface{}{"yes", "no"}, "request_id": "q2",
	}))
	if m.askCount() != 2 {
		t.Fatalf("队列长度=%d want 2 —— 第二条求助被丢了", m.askCount())
	}

	seen := map[string]bool{}
	for m.askCount() > 0 {
		p := m.pendingAsk()
		id := p.requestID
		m.respondToAsk(p, p.askOptions()[0], false)
		seen[id] = true
		if last := rec.next(t); last["request_id"] != id {
			t.Errorf("发出去的 request_id=%v，want %v", last["request_id"], id)
		}
	}
	if !seen["q1"] || !seen["q2"] {
		t.Errorf("两条求助没有都被回答: %v", seen)
	}
	if blk := m.askPanelBlock(); blk != "" {
		t.Errorf("队列清空后面板还在渲染:\n%s", blk)
	}
}

// TestAskPanel_CanceledAskSendsCancelled esc 交回 cancelled=true，
// 工具侧的语义不能变（flow_control.go:135-137）。
func TestAskPanel_CanceledAskSendsCancelled(t *testing.T) {
	m, rec := askTestModel(t, 100, 32)
	p := feedHelpAsk(m, "要跑完整测试集吗？", "", []string{"跑", "跳过"}, protocol.InteractionConfirm)
	handled, _ := m.handleAskKey("esc")
	if !handled || !p.settled {
		t.Fatalf("esc 没有取消这条求助（handled=%v settled=%v）", handled, p.settled)
	}
	last := rec.next(t)
	if last["cancelled"] != true {
		t.Errorf("取消没有带上 cancelled=true: %v", last)
	}
	if m.askCount() != 0 {
		t.Errorf("取消后队列没清空: %d", m.askCount())
	}
}

// TestAuthPanel_BodyIsNotEmpty 授权面板必须显示正文。
// 今天 guard 只发 tool_name+reason，TUI 只读 command/warning，
// 于是屏幕上只有工具名、看不到要执行什么（设计稿 R5）。
func TestAuthPanel_BodyIsNotEmpty(t *testing.T) {
	m, _ := askTestModel(t, 100, 32)
	p := m.enqueueAsk(map[string]interface{}{
		"tool_name":  "run_bash",
		"reason":     "危险命令:\n```bash\nrm -rf /var/tmp/build\n```",
		"question":   "⚠️ 授权请求",
		"request_id": "auth-1",
	}, "Coding")
	if p.kind != askKindAuth {
		t.Fatalf("不带 interaction_type 的事件应识别成授权，实际 kind=%v", p.kind)
	}
	blk := stripANSI(m.askPanelBlock())
	if !strings.Contains(blk, "rm -rf /var/tmp/build") {
		t.Errorf("授权面板没有显示命令原文:\n%s", blk)
	}
	if strings.Contains(blk, "```") {
		t.Errorf("markdown 围栏字符留在了屏幕上:\n%s", blk)
	}

	m2, _ := askTestModel(t, 100, 32)
	p2 := m2.enqueueAsk(map[string]interface{}{
		"tool_name": "delete_file", "reason": "目标路径在工作空间外部",
		"detail": "/etc/hosts", "request_id": "auth-2",
	}, "Coding")
	if p2.detail != "/etc/hosts" {
		t.Errorf("detail 没有被优先采纳: %q", p2.detail)
	}
}

// TestAuthOptionActionTable 五档的显示顺序、字母快捷键与 guard 认的动作词必须同源。
// 顺序错一位，"允许 (本次)"就会把 allow_all_project 交回去。
func TestAuthOptionActionTable(t *testing.T) {
	opts, acts := authOptions(), authActions()
	if len(opts) != len(acts) {
		t.Fatalf("选项 %d 个、动作 %d 个，不同源", len(opts), len(acts))
	}
	for i, letter := range authLetterKeys {
		if got := authActionIndex(letter); got != i {
			t.Errorf("字母 %q 解析成第 %d 档 want %d", letter, got, i)
		}
	}
	if acts[0] != "allow" || acts[4] != "deny" {
		t.Errorf("首尾动作词变了，guard 侧的判定会认不出来: %v", acts)
	}
}

// TestAskPanel_InputModeLabelsAnswer 求助期间输入框必须改标签：
// 此刻 alt+s 发的是答案，继续写 "alt+s submit" 就是键帽骗人。
func TestAskPanel_InputModeLabelsAnswer(t *testing.T) {
	m, _ := askTestModel(t, 100, 30)
	p := feedHelpAsk(m, "贴出报错原文", "", nil, protocol.InteractionInput)
	if !p.inputMode {
		t.Fatal("input 模式应当从输入态开始")
	}
	panel := stripANSI(m.renderEditorPanel())
	if !strings.Contains(panel, "ANSWER") {
		t.Errorf("输入面板没有换成 ANSWER 标签:\n%s", panel)
	}
	if strings.Contains(panel, "alt+s submit") {
		t.Errorf("ANSWER 态还在写提交任务的键帽:\n%s", panel)
	}
}

// TestAskPanel_EmptyAnswerIsNotSent 空答案不能发出去，且要在屏上说出为什么。
func TestAskPanel_EmptyAnswerIsNotSent(t *testing.T) {
	m, _ := askTestModel(t, 100, 30)
	m.input.SetValue("")
	feedHelpAsk(m, "贴出报错原文", "", nil, protocol.InteractionInput)
	m.sendAskAnswer("")
	if m.askCount() != 1 {
		t.Error("空答案被当成回答发出去了")
	}
	if m.errMsg == "" {
		t.Error("空答案被拒之后屏幕上没有任何解释")
	}
}

// TestAskPanel_CustomOptionIsNotSentAsAnswer 选中"自由输入…"必须切到输入态，
// 而不是把 Custom input... 这行字当答案发出去（今天的实现就发的是标签原文）。
func TestAskPanel_CustomOptionIsNotSentAsAnswer(t *testing.T) {
	m, rec := askTestModel(t, 100, 30)
	p := feedHelpAsk(m, "选一个测试框架", "", []string{"pytest", "unittest"}, protocol.InteractionSelect)
	custom := len(p.options)
	handled, _ := m.handleAskKey(askQuickKey(custom))
	if !handled {
		t.Fatal("自定义项的键帽没人认领")
	}
	rec.assertQuiet(t, 200*time.Millisecond)
	if !p.inputMode {
		t.Error("选自定义项之后没有进入输入态")
	}
}

// TestAskReader_WrapsAtPanelWidth 面板与阅读器必须共用同一份折行：
// 两处各写一份，同一段上下文会在两个地方断在不同位置。
func TestAskReader_WrapsAtPanelWidth(t *testing.T) {
	m, _ := askTestModel(t, 100, 30)
	body := strings.Repeat("候选实现都能改，需要你定方向。", 10)
	p := feedHelpAsk(m, "问题", body, []string{"甲", "乙"}, protocol.InteractionSelect)
	p.allowCustom = false
	f := m.askFitNow()
	if f.foldMarker == "" {
		t.Fatal("夹具没有触发折叠，阅读器对账无从谈起")
	}
	m.openAskReader()
	reader := stripANSI(renderAskReaderView(m).Content)
	if !strings.Contains(reader, body[:12]) {
		t.Errorf("阅读器里没有上下文原文:\n%s", reader)
	}
	for _, l := range strings.Split(reader, "\n") {
		if got := lipgloss.Width(l); got > m.termWidth {
			t.Errorf("阅读器某行宽 %d 超过 termWidth: %q", got, l)
		}
	}
	// 同一个折行函数：面板与阅读器都用 wrapKeepingSeparators。
	if strings.Contains(reader, "s/user_help") || strings.Contains(stripANSI(m.askPanelBlock()), "component\n") {
		t.Error("标识符在某一边被从中间切断，两处用的不是同一份折行")
	}
}

// TestAskReader_ListenerStaysArmed 覆盖层消费事件后必须接替监听器（坑 6）。
func TestAskReader_ListenerStaysArmed(t *testing.T) {
	m, _ := askTestModel(t, 100, 30)
	m.eventCh = make(chan *messaging.MessageEvent, 1)
	p := feedHelpAsk(m, "问题", strings.Repeat("长上下文，需要折叠。", 20), []string{"甲"}, protocol.InteractionSelect)
	p.allowCustom = false
	m.openAskReader()
	if !m.askReaderMode {
		t.Fatal("阅读器没有打开")
	}
	_, cmd := m.Update(taskEventMsg{event: buildEvent("ai_response", "Coding", "覆盖层期间到达的回复")})
	if cmd == nil {
		t.Fatal("阅读器消费掉一条事件却没有接替监听器")
	}
	// esc 退回主界面，求助本身还在（阅读器只是"看"，不是"答"）。
	m.handleAskKey("esc")
	if m.pendingAsk() == nil {
		t.Error("阅读器里的 esc 把求助一起取消了")
	}
}

// TestWrap_NoPunctuationAtLineStart 中文折行不能把收尾标点挂到行首。
//
// "夹具到底有没有踩到规则"必须独立证明：用测试自己的一份"按宽度取到装不下为止"扫描算出
// **裸断点**落在哪个字符上。只数违规的话，规则生效时它恒为 0，这条断言就变成永远不会失败的
// 装饰（scripts/check_test_hygiene.sh 管的就是这一类）。
func TestWrap_NoPunctuationAtLineStart(t *testing.T) {
	text := strings.Repeat("字，", 40) +
		strings.Repeat("当前工作区里有两个候选实现，一处改完另一处的对账测试也要跟着动。", 4)
	widths := []int{8, 9, 10, 11, 12, 13, 14, 20, 24, 40, 60}

	naiveHits := 0
	for _, width := range widths {
		runes := []rune(text)
		w := 0
		for i, r := range runes {
			rw := lipgloss.Width(string(r))
			if w+rw > width {
				if i < len(runes) && strings.ContainsRune(cjkNoLineStart, runes[i]) {
					naiveHits++
				}
				break
			}
			w += rw
		}
	}
	if naiveHits == 0 {
		t.Fatalf("夹具一次都没把裸断点撞到收尾标点前，这条断言恒真")
	}

	for _, width := range widths {
		for i, l := range wrapKeepingSeparators(text, width) {
			if l == "" {
				continue
			}
			if r := []rune(l)[0]; strings.ContainsRune(cjkNoLineStart, r) {
				t.Errorf("width=%d 第 %d 行以 %q 开头", width, i, string(r))
			}
			if got := lipgloss.Width(l); got > width {
				t.Errorf("width=%d 第 %d 行实际宽 %d", width, i, got)
			}
		}
	}
}
