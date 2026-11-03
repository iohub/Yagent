package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// 按键与键帽是同一张表（设计稿 §5.3）。
//
// 今天键帽写在 getHintParts（user_help_dialog.go:785-826）、实现在 handleSelectListKey
// （:350-388），于是第 10 项起键帽写着 a…k、按键只 case "1".."9" —— 用户按下去什么都没发生，
// 而且没有任何编译错误或日志。这里两边共用一份数据：分派遍历这张表，键帽行也从这张表生成，
// **结构上不可能再出现"写着按不动"**（闸门 TestAskPanel_ChoiceRowKeycapsAllWork）。

// askState 是一张键在"此刻"是否可用的判据。
type askState struct {
	kind       askKind
	inputMode  bool
	hasList    bool
	foldHidden bool // 有被折叠的内容 → ctrl+o 有意义
	queue      int
	scrollUp   bool
	scrollDown bool
}

type askAction struct {
	keys []string
	hint string // 键帽文案；首词是按键，与 renderHintItem 的约定一致（tui_chrome.go:174-180）
	when func(askState) bool
	run  func(m *model, key string) tea.Cmd
}

// 列表态 / 输入态的判据写成函数而不是在表里各写一遍 —— 一张表读起来必须像一件事。
func askAtList(s askState) bool { return !s.inputMode && s.hasList }

// askKeyActions 在 init() 里构造而不是直接当包级变量初始化：
// 表里的闭包引用 model 方法，而 footer 计算又引用这张表 —— 直接写会让 Go 报初始化环。
var askKeyActions []askAction

func init() {
	askKeyActions = []askAction{
		// ── 第一组：退出通道。窄屏时 renderHintBar 从尾部丢，所以第一位必须还活着。
		{
			keys: []string{"esc"}, hint: "esc cancel",
			when: func(s askState) bool { return s.kind == askKindHelp && !s.inputMode },
			run:  func(m *model, _ string) tea.Cmd { m.cancelPendingAsk(); return nil },
		},
		{
			// AUTH 的 esc 是"拒绝"而不是"取消"：deny 要让 guard 拿到 deny，
			// cancelled 与 deny 语义不同（拒绝=不执行，取消=用户没回答）。
			keys: []string{"esc"}, hint: "esc deny",
			when: func(s askState) bool { return s.kind == askKindAuth },
			run: func(m *model, _ string) tea.Cmd {
				m.respondToAsk(m.pendingAsk(), "deny", false)
				return nil
			},
		},
		{
			// 输入焦点下 esc 先退回列表（两段式）；再按一次才取消。
			keys: []string{"esc"}, hint: "esc back",
			when: func(s askState) bool { return s.kind == askKindHelp && s.inputMode && s.hasList },
			run:  func(m *model, _ string) tea.Cmd { return m.setAskInputMode(false) },
		},

		// ── 第二组：提交通道。
		{
			keys: []string{"enter"}, hint: "enter choose",
			when: askAtList,
			run:  func(m *model, _ string) tea.Cmd { m.choosePendingAsk(m.currentAskFocus()); return nil },
		},
		{
			keys: []string{"alt+s", "ctrl+s"}, hint: "alt+s send",
			when: func(s askState) bool { return s.kind == askKindHelp && s.inputMode },
			run:  func(m *model, _ string) tea.Cmd { m.sendAskAnswer(m.input.Value()); return nil },
		},

		// ── 移动。
		{
			keys: []string{"up", "k"}, hint: "↑↓ move",
			when: askAtList,
			run:  func(m *model, _ string) tea.Cmd { m.moveAsk(-1); return nil },
		},
		{
			keys: []string{"down", "j"}, hint: "↑↓ move",
			when: askAtList,
			run:  func(m *model, _ string) tea.Cmd { m.moveAsk(1); return nil },
		},
		// ── 第三组：直选与换行（只出文案，按键由分派器的专门分支处理）。
		// HELP 的键帽文本与可点的键都由 quickKeyLabels/pickQuickKey 那一份定义 ——
		// 今天键帽在 selectKeyHint 里写 a-z、按键只 case "1".."9"，两边各写一遍就漂了。
		{
			keys: nil, hint: "1-9/a-z pick",
			when: func(s askState) bool { return s.kind == askKindHelp && s.hasList && !s.inputMode },
		},
		{
			keys: nil, hint: "enter newline",
			when: func(s askState) bool { return s.kind == askKindHelp && s.inputMode },
		},

		// ── 第四组：焦点在列表与答题区之间切换。
		{
			keys: []string{"tab"}, hint: "tab answer",
			when: func(s askState) bool { return s.kind == askKindHelp && !s.inputMode },
			run:  func(m *model, _ string) tea.Cmd { return m.setAskInputMode(true) },
		},
		{
			keys: []string{"tab"}, hint: "tab options",
			when: func(s askState) bool { return s.kind == askKindHelp && s.inputMode && s.hasList },
			run:  func(m *model, _ string) tea.Cmd { return m.setAskInputMode(false) },
		},
		{
			keys: []string{"a", "t", "s", "p", "d"}, hint: "a/t/s/p/d allow",
			when: func(s askState) bool { return s.kind == askKindAuth },
			run: func(m *model, key string) tea.Cmd {
				if i := authActionIndex(key); i >= 0 {
					m.respondToAsk(m.pendingAsk(), authActions()[i], false)
				}
				return nil
			},
		},

		// ── 第五组：全文。只在真有折叠时才出现。
		{
			keys: []string{"ctrl+o"}, hint: "ctrl+o full text",
			when: func(s askState) bool { return s.foldHidden },
			run:  func(m *model, _ string) tea.Cmd { return m.openAskReader() },
		},

		// ── 第六组：翻页。有滚动余量才进键帽（§6.4：ctrl+u 在输入态仍是 textarea 的清行键）。
		{
			keys: []string{"ctrl+u"}, hint: "ctrl+u up",
			when: func(s askState) bool { return !s.inputMode && s.scrollUp },
			run:  func(m *model, _ string) tea.Cmd { m.pageAsk(-1); return nil },
		},
		{
			keys: []string{"ctrl+d"}, hint: "ctrl+d down",
			when: func(s askState) bool { return !s.inputMode && s.scrollDown },
			run:  func(m *model, _ string) tea.Cmd { m.pageAsk(1); return nil },
		},
		{
			keys: []string{"pgup"}, hint: "",
			when: func(s askState) bool { return !s.inputMode && s.scrollUp },
			run:  func(m *model, _ string) tea.Cmd { m.pageAsk(-1); return nil },
		},
		{
			keys: []string{"pgdown"}, hint: "",
			when: func(s askState) bool { return !s.inputMode && s.scrollDown },
			run:  func(m *model, _ string) tea.Cmd { m.pageAsk(1); return nil },
		},

		// ── 第七组：队列。只有一条时不出现。
		{
			keys: []string{"ctrl+n"}, hint: "ctrl+n next",
			when: func(s askState) bool { return s.queue > 1 },
			run:  func(m *model, _ string) tea.Cmd { m.cycleAsk(1); return nil },
		},
		{
			keys: []string{"ctrl+p"}, hint: "ctrl+p prev",
			when: func(s askState) bool { return s.queue > 1 },
			run:  func(m *model, _ string) tea.Cmd { m.cycleAsk(-1); return nil },
		},
	}
}

// authLetterKeys 与 authActions 一一对应（顺序就是面板里五档的顺序）。
var authLetterKeys = []string{"a", "t", "s", "p", "d"}

func authActionIndex(key string) int {
	for i, k := range authLetterKeys {
		if strings.EqualFold(k, key) {
			return i
		}
	}
	return -1
}

func (m *model) askStateNow() askState {
	p := m.pendingAsk()
	if p == nil {
		return askState{}
	}
	f := m.askFitNow()
	return askState{
		kind:       p.kind,
		inputMode:  p.inputMode,
		hasList:    p.hasList(),
		foldHidden: f.foldMarker != "",
		queue:      m.askCount(),
		scrollUp:   f.scrollUp > 0,
		scrollDown: f.scrollDown > 0,
	}
}

// askFindAction 返回表里第一个"此刻可用且认领这张键"的动作。
// 只有一处在做键→动作的匹配，handleAskKey 与闸门测试都走它。
func askFindAction(s askState, key string) *askAction {
	for i := range askKeyActions {
		a := &askKeyActions[i]
		if a.when == nil || !a.when(s) {
			continue
		}
		for _, k := range a.keys {
			if k == key {
				return a
			}
		}
	}
	return nil
}

// askHintBar 生成键帽行：按 askKeyActions 的**声明顺序**取此刻可用的项、同文案去重。
// 放不下时由 renderHintBar 从尾部整段丢弃（tui_chrome.go:142-171），
// 所以"退出通道写在表最前面"就是 §5.2 那条优先级的全部实现 —— 没有第二个排序旋钮。
func (m *model) askHintBar(s askState) string {
	var parts []string
	seen := map[string]bool{}
	for _, a := range askKeyActions {
		if a.hint == "" || a.when == nil || !a.when(s) || seen[a.hint] {
			continue
		}
		seen[a.hint] = true
		parts = append(parts, a.hint)
	}
	return strings.Join(parts, panelHintSep)
}

// handleAskKey 分派一张键。返回 handled=false 时按键继续走编辑/普通模式，
// 这样 ctrl+c / ctrl+z / ctrl+h / alt+m 这些全局键在面板打开时依然有效。
func (m *model) handleAskKey(key string) (bool, tea.Cmd) {
	p := m.pendingAsk()
	if p == nil || m.askReaderMode {
		return false, nil
	}
	if key == "ctrl+c" || key == "ctrl+z" {
		return false, nil
	}
	s := m.askStateNow()

	if a := askFindAction(s, key); a != nil {
		var cmd tea.Cmd
		if a.run != nil {
			cmd = a.run(m, key)
		}
		m.touchAsk()
		return true, cmd
	}

	// 直选：HELP 的列表态。键帽文本与实际可点的键共用 pickQuickKey 那一份定义。
	if s.kind == askKindHelp && !s.inputMode && s.hasList {
		if idx := pickQuickKey(key); idx >= 0 {
			if opts := p.askOptions(); idx < len(opts) {
				m.choosePendingAsk(idx)
				return true, nil
			}
			// 键帽位存在但这条求助没有那么多项：吞掉，不误漏进输入框
			return true, nil
		}
	}

	// 两态的列表焦点把其余按键全部吞掉：否则打 "hello" 里的 h/e/l/o 会误触
	// 直选、或者漏进输入框留下一个用户没打算发的答案草稿。
	if !s.inputMode {
		return true, nil
	}
	return false, nil
}

func (m *model) touchAsk() {
	m.invalidateFooterCache()
}

func (m *model) currentAskFocus() int {
	if p := m.pendingAsk(); p != nil {
		return p.focusIndex
	}
	return 0
}

func (m *model) moveAsk(delta int) {
	if p := m.pendingAsk(); p != nil {
		p.moveCursor(delta, m.askWindowRows())
	}
}

func (m *model) pageAsk(dir int) {
	if p := m.pendingAsk(); p != nil {
		p.pageCursor(dir, m.askWindowRows())
	}
}

// setAskInputMode 切换焦点态。进入输入态时把输入框聚焦并预填 default_value；
// 离开时不动草稿 —— 用户切回列表再切回来，不该丢掉已经写了一半的答案。
func (m *model) setAskInputMode(on bool) tea.Cmd {
	p := m.pendingAsk()
	if p == nil || p.kind == askKindAuth {
		return nil
	}
	p.inputMode = on
	if p.hasList() {
		p.clampCursor(m.askWindowRows())
	}
	m.touchAsk()
	if on {
		// 借用已有的"回到 INSERT"入口：它是键位进到 textarea 的唯一门。
		return m.enterInsertMode()
	}
	return nil
}

// choosePendingAsk 提交第 idx 项：AUTH 交回动作词，HELP 交回选项原文。
func (m *model) choosePendingAsk(idx int) {
	p := m.pendingAsk()
	if p == nil {
		return
	}
	opts := p.askOptions()
	if idx < 0 || idx >= len(opts) {
		return
	}
	if p.kind == askKindAuth {
		m.respondToAsk(p, authActions()[idx], false)
		return
	}
	// 最后一项是"自由输入"：选它等于切到输入态，而不是把 "Custom input…" 当答案发出去。
	if idx >= len(p.options) {
		m.setAskInputMode(true)
		return
	}
	m.respondToAsk(p, opts[idx], false)
}

// sendAskAnswer 把输入框里的文本作为答案发出去（自由输入 / input 模式）。
func (m *model) sendAskAnswer(value string) {
	p := m.pendingAsk()
	if p == nil || p.kind == askKindAuth {
		return
	}
	if strings.TrimSpace(value) == "" {
		m.errMsg = langManager.GetText("AskEmptyAnswer")
		return
	}
	m.respondToAsk(p, value, true)
}

func (m *model) cancelPendingAsk() {
	if p := m.pendingAsk(); p != nil {
		m.respondToAsk(p, "", false)
	}
}

// respondToAsk 发布 user_help_response 并把这条从队列摘掉。
//
// 授权那一支走的是同一个 request_id 回路（user_confirm.go:159-217 的 Consume 按 id 配对，
// 两种 content 形状都认），所以工具侧零改动 —— 变的只是屏幕上这块面板。
func (m *model) respondToAsk(p *pendingAsk, response string, isCustom bool) {
	if p == nil || p.settled {
		return
	}
	p.settled = true
	cancelled := p.kind == askKindHelp && response == ""

	if m.publisher != nil {
		content := map[string]interface{}{
			"response":   response,
			"request_id": p.requestID,
		}
		if p.kind == askKindHelp {
			content["interaction_type"] = string(p.interaction)
			content["is_custom"] = isCustom
			content["cancelled"] = cancelled
		}
		m.publisher.Publish("user_help_response", content, "User")
	}

	m.recordAskTrace(p, response, cancelled)
	m.restoreAskDraft(p)
	m.dequeueAsk(p.requestID)
}

// restoreAskDraft 把被借用做答案的输入框还原成求助弹出前的内容 ——
// 用户正在起草的下一条指令不该因为回答了一次求助而消失。
// 有下一条待答求助时不还原：那一条还要用同一个输入框。
func (m *model) restoreAskDraft(p *pendingAsk) {
	if m.askCount() > 1 {
		return
	}
	m.input.SetValue(p.preDraft)
	m.invalidateFooterCache()
}

// recordAskTrace 在 transcript 上留一条人类可读的痕迹。
// 今天是 `? HELP HELP: map[allow_custom:true …]` —— Go 把 content map 逐字打出来
// （tui_render.go:799-804），答完还额外再加一行 status 重复信息。
// 定案：一条条目承担"问了什么 + 答了什么"，答案单行截断，全文在工具结果卡里。
func (m *model) recordAskTrace(p *pendingAsk, answer string, cancelled bool) {
	tail := truncateToDisplayWidth(firstLine(answer), 60)
	if cancelled {
		tail = "cancelled"
	}
	m.appendStatusLine("◆ " + firstLine(p.question) + " · " + tail)
}
