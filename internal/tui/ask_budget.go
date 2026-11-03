package tui

import "charm.land/lipgloss/v2"

// 整屏高度分配的唯一口径（设计稿 §4.2.1）。
//
// 实测事实：今天 footer + 正文 + tab 恰好等于 termHeight，**没有一行余量**
// （100×20 → footer 12 / 正文 7 / tab 1）。往 footer 里加一块 ASK 面板，
// 就必须有人从正文与输入框的生长里把钱付掉。
//
// 这里的做法是仓库已有的那条先例（`tui_render.go:28-32` 的 renderTimelinePanel）：
// **渲染一次、按行数计费**。computeFooterHeight 与 View 用的是同一个字符串，
// 所以"高度累加与拼接逐行对应"这条契约不可能对不上 —— 不需要第二个函数去预测它。
//
// 依赖顺序是无环的：
//
//	askSpace()          只看常量（输入框收缩下限、正文下限）与可选块
//	  → askPanelBlock() 按可用行数决定选项窗口，产出一个字符串
//	  → askPanelHeight() = lipgloss.Height(block)
//	  → computeInputHeight() 把剩余空间作为上限
//	  → computeFooterHeight() = 1 + editorBlockHeight + 可选块 + askPanelHeight + 2
//	  → computeBodyLayout() = termHeight - footer - tabBar
//
// 分配序与设计稿 §4.2.1 有一处出入，是算过之后改的：那里把 ANSWER 排在 ASK 之前，
// 于是"长答案 + 20 行终端"会把 ASK 挤到 0 行 —— 被饿死的恰好是唯一能让 Agent
// 继续跑下去的那块面板。定案：**ASK 优先于输入框的生长部分**，输入框收缩到 textarea
// 的 minHeight 仍然可用（内部滚动 + meta 里的行列号是两个出口）。

const (
	bodyMinRows    = 3 // 与 computeBodyLayout 的下限夹同源（tui_view.go:45-47）
	inputMinRows   = 3 // computeInputHeight 的 minHeight（tui_helpers.go:109）
	inputMaxRows   = 12
	askWindowMin   = 3  // 选项窗口最少行数
	askWindowMax   = 12 // 选项窗口最多行数
	askQuestionMax = 3  // 问题最多占几行
	askBodyMax     = 2  // 上下文（或授权正文）折叠态最多占几行
	askHeightPct   = 6  // 面板最多占 termHeight 的 6/10
	askHeightPctOf = 10
)

// askMinRows 是面板的最小形态：边框 2 + 问题 1 + 选项区上方空行 1 + 选项 3。
// 再小就没有"可答"的界面了，而这正是整块面板存在的唯一理由。
func askMinRows() int { return askBorderRows + 1 + askWindowMin }

func editorMinRows() int { return 2 + inputMinRows }

// askSpace 是面板与正文各自的下限。一次算清，两个消费点从同一个 struct 取值，
// 免得"借了多少"这件事在两处各写一遍。
type askSpace struct {
	// panelRows：面板能占的行数上限（含"借正文下限也要保住最小形态"的结果）。
	panelRows int
	// bodyFloor：正文的下限。默认 bodyMinRows，面板借位时降到 1。
	bodyFloor int
}

// footerFixedRows 是 footer 里既不属于输入框、也不属于面板的那一段：
// 时间线浮层 + 两个空行段（输入块与正文之间 1、状态栏与输入块之间 2）+ 可选提示块。
// tab 栏不在这里，由调用方另扣。
//
// 少算任何一块，代价都是整屏行数超出 termHeight（多出来的行在 AltScreen 里滚不回去）。
func (m *model) footerFixedRows() int {
	return m.timelineFooterRows() + 3 + m.optionalFooterRows()
}

func (m *model) askSpaceRows() askSpace {
	sp := askSpace{bodyFloor: bodyMinRows}
	if m.pendingAsk() == nil || m.termHeight <= 0 {
		return sp
	}
	fixed := tabBarHeight + m.footerFixedRows()
	avail := m.termHeight - fixed - editorMinRows() - sp.bodyFloor
	if cap := m.termHeight * askHeightPct / askHeightPctOf; cap < avail {
		avail = cap
	}
	if avail < askMinRows() {
		// 连最小形态都放不下：从正文的下限借。借不到也要把面板渲染出来 ——
		// 面板没地方放就等于用户没法回答，而 ask_user_for_help 豁免工具超时
		// （toolspec.BlocksOnUser，agents/tool_timeout.go:12-14），Agent 会一直等。
		borrow := sp.bodyFloor - 1
		avail += borrow
		sp.bodyFloor = 1
		if avail < askMinRows() {
			avail = askMinRows()
		}
	}
	sp.panelRows = avail
	return sp
}

// askBodyFloor 是正文还能占几行的下限（面板借位时会降低）。
// computeBodyLayout 用它替代写死的 3，否则借用的那几行不会真的生效。
func (m *model) askBodyFloor() int { return m.askSpaceRows().bodyFloor }

// optionalFooterRows 是补全/错误/提示/token 面板这几块各自占的行数。
// 它们的高度不是 textarea 行数的函数，所以可以在分配之前先扣。
// tokenDashboardBlock 自带前导空行（tui_render.go:76-88），按它的高度照实扣。
func (m *model) optionalFooterRows() int {
	n := 0
	if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
		n++
	}
	if m.errMsg != "" {
		n++
	}
	if m.infoMsg != "" {
		n++
	}
	if dash := m.tokenDashboardBlock(); dash != "" {
		n += lipgloss.Height(dash)
	}
	return n
}

// rowsLeftForInput 是输入框还能长到多少行（textarea 正文行数，不含上下边框）。
// computeInputHeight 用它替代自己那套 (termHeight-8)/2 的估算 —— 那个估算里的 8
// 是"分隔符 + 状态栏 + token 面板"的旧账，既不知道 ASK 面板占几行，
// 也不知道时间线浮层与补全面板此刻在不在。
func (m *model) rowsLeftForInput() int {
	fixed := tabBarHeight + m.footerFixedRows() + m.askPanelHeight()
	return m.termHeight - fixed - m.askBodyFloor() - 2 // -2：输入面板的上下边框
}

func clampRows(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
