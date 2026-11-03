package tui

import (
	"strings"
	"time"

	"yagent/internal/protocol"
)

// 决策面板家族（ASK / AUTH / ANSWER）的状态与队列。
// 设计稿：docs/ask-panel-redesign.md。这里只放"派生不出来的东西"：
// 队列，以及每条求助自己的焦点与窗口位置。
// 行数账在 ask_budget.go，渲染在 ask_panel.go，按键与键帽同一张表在 ask_keys.go。

// askKind 决定面板标签、正文回落顺序与可用动作集合。
type askKind int

const (
	askKindHelp askKind = iota // ask_user_for_help
	askKindAuth                // WorkspaceGuard 授权
)

// pendingAsk 是一次待答请求。
//
// 焦点与窗口位置属于**这一条**请求：队列里切到别的求助再切回来，
// 不能把用户已经走到的位置抹掉。preDraft 是求助弹出前输入框里本来的内容 ——
// ANSWER 借用主输入框写答案，答完之后必须还原，否则用户正在起草的下一条指令
// 会因为回答过一次求助而消失。
type pendingAsk struct {
	requestID    string
	kind         askKind
	agent        string
	interaction  protocol.InteractionType
	question     string
	context      string
	detail       string // AUTH 的命令/路径正文；HELP 为空
	warning      string
	options      []string
	allowCustom  bool
	defaultValue string
	arrivedAt    time.Time

	preDraft string
	settled  bool

	focusIndex int
	scroll     int
	inputMode  bool // 焦点在输入框（true）还是在选项列表（false）
}

// askOptions 返回面板要列出的选项。AUTH 用它自己的五档；
// HELP 的 select 模式在 allow_custom 时尾部多一项"自由输入"，
// HELP 的 input 模式没有选项（只有自由输入）。
func (p *pendingAsk) askOptions() []string {
	if p.kind == askKindAuth {
		return authOptions()
	}
	opts := make([]string, 0, len(p.options)+1)
	opts = append(opts, p.options...)
	if p.allowCustom && p.interaction == protocol.InteractionSelect {
		opts = append(opts, langManager.GetText("AskCustomOption"))
	}
	return opts
}

// askBodyText 是正文段：HELP=上下文（与问题重复时不显示），AUTH=命令/路径。
//
// guard 造的 reason 用 ```bash / ```js 包住命令原文（workspace_guard.go 的 Check），
// 而面板已经有自己的 code 正文区 —— 围栏字符留在屏上是噪音不是信息。
// 这里过滤而不是在事件生产侧过滤：**只此一处**，且顺带兜住只带 reason 的旧事件
// 与 HTTP 侧发出来的形状（生产侧再写一份同样的清理就是第二会漂走的事实）。
func (p *pendingAsk) askBodyText() string {
	if p.kind == askKindAuth {
		return stripCodeFenceLines(p.detail)
	}
	if p.context == "" || p.context == p.question {
		return ""
	}
	return p.context
}

// hasList 报告这条求助有没有可移动的选项列表（input 模式没有）。
func (p *pendingAsk) hasList() bool { return len(p.askOptions()) > 0 }

// clampCursor 把焦点夹进合法区间，并把滚动窗口拖到能看见焦点。
func (p *pendingAsk) clampCursor(window int) {
	opts := p.askOptions()
	if len(opts) == 0 {
		p.focusIndex, p.scroll = 0, 0
		return
	}
	p.focusIndex = clampRows(p.focusIndex, 0, len(opts)-1)
	window = max(1, window)
	if p.focusIndex < p.scroll {
		p.scroll = p.focusIndex
	}
	if p.focusIndex >= p.scroll+window {
		p.scroll = p.focusIndex - window + 1
	}
	if limit := max(0, len(opts)-window); p.scroll > limit {
		p.scroll = limit
	}
	if p.scroll < 0 {
		p.scroll = 0
	}
}

// moveCursor 移动焦点并夹住窗口；两端停下（↑↓/jk 用）。
func (p *pendingAsk) moveCursor(delta, window int) {
	if !p.hasList() {
		return
	}
	next := p.focusIndex + delta
	if next < 0 || next >= len(p.askOptions()) {
		return
	}
	p.focusIndex = next
	p.clampCursor(window)
}

// pageCursor 整页移动（ctrl+u/d 与 PgUp/PgDn 共用一个实现）。
func (p *pendingAsk) pageCursor(dir, window int) {
	if !p.hasList() {
		return
	}
	p.focusIndex = clampRows(p.focusIndex+dir*window, 0, len(p.askOptions())-1)
	p.clampCursor(window)
}

// pickQuickKey 把键帽上写的直选键映射回条目下标，-1 表示这不是直选键。
// **键帽文本与可点的键共用 quickKeyLabels 这一份定义**：
// 今天键帽在 selectKeyHint 里写 a-z、按键只 case "1".."9"，于是第 10 项起
// 键帽写着、按下没反应（设计稿 R4）。
func pickQuickKey(key string) int {
	if len([]rune(key)) != 1 {
		return -1
	}
	r := []rune(key)[0]
	if r >= '1' && r <= '9' {
		return int(r - '1')
	}
	for i, c := range quickKeyLabels {
		if c == r {
			return 9 + i
		}
	}
	return -1
}

// quickKeyLabels 是第 10 项及以后的键帽字母。j/k 不在里面 —— 它们是 vim 移动键，
// 写进键帽就会变成"按 j 想选第 10 项、结果焦点上移"。
var quickKeyLabels = []rune{'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i',
	'l', 'm', 'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z'}

// askQuickKey 是第 i 项的键帽文本；与 pickQuickKey 互逆（闸门测试钉住这一对）。
func askQuickKey(i int) string {
	if i < 0 {
		return ""
	}
	if i < 9 {
		return string(rune('1' + i))
	}
	j := i - 9
	if j >= len(quickKeyLabels) {
		return ""
	}
	return string(quickKeyLabels[j])
}

func (m *model) pendingAsk() *pendingAsk {
	if m.askActiveIndex < 0 || m.askActiveIndex >= len(m.askQueue) {
		return nil
	}
	return m.askQueue[m.askActiveIndex]
}

// enqueueAsk 把一次 user_help_needed 事件装进队列。
// 它是这条链路上唯一的入口：字段怎么回落、AUTH 与 HELP 怎么分流，都在这里算一次。
func (m *model) enqueueAsk(content map[string]interface{}, agent string) *pendingAsk {
	p := &pendingAsk{
		kind:      askKindHelp,
		agent:     agent,
		arrivedAt: time.Now(),
		preDraft:  m.input.Value(),
	}

	p.requestID, _ = content["request_id"].(string)
	p.question, _ = content["question"].(string)
	p.context, _ = content["context"].(string)
	p.warning, _ = content["warning"].(string)
	p.defaultValue, _ = content["default_value"].(string)
	if it, ok := content["interaction_type"].(string); ok {
		p.interaction = protocol.InteractionType(it)
	}
	if opts, ok := content["options"].([]interface{}); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				p.options = append(p.options, s)
			}
		}
	}
	// allow_custom 的默认值是"允许"：只有显式写 false 才关掉。
	if ac, ok := content["allow_custom"].(bool); ok {
		p.allowCustom = ac
	} else {
		p.allowCustom = true
	}

	if _, hasIT := content["interaction_type"]; !hasIT {
		// 授权那一支不带 interaction_type（WorkspaceGuard.RequestConfirmation）。
		p.kind = askKindAuth
		p.interaction = protocol.InteractionConfirm
		p.detail = authBodyFrom(content)
		if tn, ok := content["tool_name"].(string); ok && tn != "" {
			p.question = tn
		}
	}
	if p.question == "" {
		p.question = p.askBodyText()
	}
	if p.question == "" {
		p.question = langManager.GetText("AskFallbackQuestion")
	}
	if p.interaction == "" {
		p.interaction = protocol.InferInteractionType(p.options)
	}

	// 焦点默认值：有选项就从列表开始，没有选项（input 模式）直接进输入态。
	p.inputMode = !p.hasList()
	if p.defaultValue != "" && p.inputMode && m.input.Value() == "" {
		m.input.SetValue(p.defaultValue)
	}

	m.askQueue = append(m.askQueue, p)
	m.askActiveIndex = len(m.askQueue) - 1
	m.invalidateFooterCache()
	p.clampCursor(m.askWindowRows())
	return p
}

// authBodyFrom 按 §8.2 的回落顺序取授权正文：detail → command → reason → question。
// 今天 guard 只发 tool_name+reason，而 TUI 只读 command/warning ——
// 于是正文恒为空（设计稿 R5）。两侧一起修；回落链保留 command 这个旧键名，
// 因为它是 HTTP/WS 侧可能已经在用的形状（只增不改）。
func authBodyFrom(content map[string]interface{}) string {
	for _, k := range []string{"detail", "command", "reason", "question"} {
		if s, ok := content[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// dequeueAsk 摘掉指定 request_id 的那条。队列空时面板整块消失
// （askSpaceRows 的 panelRows 归零，computeFooterHeight 也就不再加那一块）。
func (m *model) dequeueAsk(requestID string) {
	for i, p := range m.askQueue {
		if p.requestID == requestID {
			// [i+1:] 而不是 [i:]：后者把元素拷回自己原位，长度不变 —— 表现为
			// "回答发出去了、面板还挂在那儿、队列永远非空"。
			m.askQueue = append(m.askQueue[:i], m.askQueue[i+1:]...)
			break
		}
	}
	switch {
	case len(m.askQueue) == 0:
		m.askActiveIndex = -1
		m.askReaderMode = false
	case m.askActiveIndex >= len(m.askQueue):
		m.askActiveIndex = len(m.askQueue) - 1
	}
	m.invalidateFooterCache()
	if p := m.pendingAsk(); p != nil {
		p.clampCursor(m.askWindowRows())
	}
}

func (m *model) askCount() int { return len(m.askQueue) }

// cycleAsk 在队列里移动（ctrl+n/ctrl+p）。队列只有一条时不动。
func (m *model) cycleAsk(delta int) {
	if len(m.askQueue) < 2 {
		return
	}
	n := len(m.askQueue)
	m.askActiveIndex = (m.askActiveIndex + delta + n) % n
	if p := m.pendingAsk(); p != nil {
		p.clampCursor(m.askWindowRows())
	}
	m.invalidateFooterCache()
}

// authOptions 是授权面板的五档，文案走 langManager（与今天的 ConfirmDialog 同源）。
func authOptions() []string {
	return []string{
		langManager.GetText("ConfirmOptionAllow"),
		langManager.GetText("ConfirmOptionAllowTool"),
		langManager.GetText("ConfirmOptionAllowSession"),
		langManager.GetText("ConfirmOptionAllowProject"),
		langManager.GetText("ConfirmOptionDeny"),
	}
}

// authActions 与 authOptions 一一对应，交回 guard 认识的动作词
// （workspace_guard.go:167-181 的判定表）。
func authActions() []string {
	return []string{"allow", "allow_session", "allow_all_session", "allow_all_project", "deny"}
}

// stripCodeFenceLines 丢掉纯粹的 markdown 围栏行（``` 与 ```lang）。
// 正文里剩下的就是命令本身；没有围栏的文本原样返回。
func stripCodeFenceLines(s string) string {
	if !strings.Contains(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		// 开围栏带语言名（```bash）、闭合是裸 ```：两种都是 markdown 装饰，不是正文。
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "```") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
