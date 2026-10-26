package tui

import (
	"sort"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// 正文虚拟化
//
// bubbles 的 viewport.SetContent 是 O(全部行) 的活：逐行重切一遍，再扫一遍最大行宽
// （viewport.maxLineWidth）。transcript 只会长，所以这里把整段正文变成一份可寻址的
// 行索引 —— 条目 → 排版块 → 行数前缀和；滚动游标是**绝对行号**并由 model 拥有，
// viewport 降级成"把窗口那几行排成固定网格"的渲染器。
//
// 索引必须精确到行。旧的 contentPartLinePrefix 只按条目原始行数累加（不认被跳过的
// verbose/空条目、不认插入的 agent 分隔行、也不认 thinking 合并成续行后少掉的行），
// 拿来"大概圈个范围给动画重渲染"够用，拿来切片会整屏上下错位。
// ─────────────────────────────────────────────────────────────────────────────

// bodyBlock 是一个日志条目在正文里贡献的部分：可选的一行 agent 分隔/徽标 + 正文。
// text 通常就是 contentParts[i]（Go 字符串只共享底层数组，不复制），只有 thinking
// 连续成续行时才是另渲染出来的串。
type bodyBlock struct {
	head  string
	text  string
	lines int
}

// bodyEmitState 是排版条目时必须顺序携带的状态：上一个可见条目是谁、是不是 thinking。
type bodyEmitState struct {
	lastAgent    string
	lastThinking bool
}

// bodyIndex 是虚拟化的全部状态。它整体随会话标签页保存/恢复 —— 每个 tab 有自己的滚动位置
// 与行索引，所以不许在第二个地方逐字段抄它。clone 只复制三个切片（块文本是字符串，共享
// 底层数组就够），切标签页每下一次的量。
//
// scrolledUp 是反着写的："跟随尾部"必须是零值。日志视图的默认状态就在底部，而 model 新建、
// 清空会话、恢复 tab 都是拿零值起步的 —— 用 followTail=true 的话，任何一处忘了置位，
// 症状都是"流式输出停在开头，界面看着像死了"。
type bodyIndex struct {
	top         int             // 可见窗口第一行的绝对行号
	scrolledUp  bool            // 用户往上翻过：新内容不再把视野拽走
	version     int             // 索引版本：内容或头部变了就 +1，用来跳过没有变化的落地
	validFrom   int             // [0, validFrom) 的排版块是新的
	header      string          // 头部文本（banner + tagline + cwd），行数与取词都用这一份
	headerLines int             // 头部行数
	indexWidth  int             // 索引是按哪个视口宽度排的（分隔行与续行都跟宽度有关）
	blocks      []bodyBlock     // 每个条目的贡献文本与行数
	states      []bodyEmitState // 排完该条目之后的顺序状态
	prefix      []int           // 行数前缀和，prefix[0] = 头部行数

	builtBlocks    int // 上次补齐索引时覆盖的条目数：条目数一变，命中扫描与窗口都要重来
	renderedTop    int // 上一次真正喂给 viewport 的窗口
	renderedHeight int
	renderedVer    int
}

func (b bodyIndex) clone() bodyIndex {
	out := b
	out.blocks = append([]bodyBlock(nil), b.blocks...)
	out.states = append([]bodyEmitState(nil), b.states...)
	out.prefix = append([]int(nil), b.prefix...)
	return out
}

// forget 作废索引但保留游标：清空会话与从历史恢复都走它，
// 留下的 top 会在下一次 ensure 时被夹回合法范围。
func (b *bodyIndex) forget() {
	b.validFrom = 0
	b.builtBlocks = -1
	b.version++
	b.renderedVer = -1
}

func countBodyLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// bodyInvalidate 把条目 from 之后的排版块与前缀和作废。凡是改变"某条目的贡献文本"
// 或可能改变"后一条目的合并形态"的操作都要调它。
func (m *model) bodyInvalidate(from int) {
	if from < 0 {
		from = 0
	}
	if from >= m.body.validFrom {
		return
	}
	m.body.validFrom = from
	m.body.version++
}

// bodyEnsureIndex 把排版块与前缀和补齐到当前条目数。只有 validFrom 之后的条目会被重新
// 排版，所以流式追加一行的成本是 O(1) 而不是 O(历史)。
func (m *model) bodyEnsureIndex() {
	b := &m.body
	// 条目数的唯一权威是 logEntries。contentParts 只是它的派生渲染缓存，两切片可以短暂
	// 不等长（恢复了一个条目更少的历史、或新条目的正文还没渲染）—— 拿缓存长度当上界，
	// 索引一作废（清空会话、恢复历史、View 纠正视口宽度都走 validFrom=0）就会沿着更长的
	// 缓存走到 m.logEntries[i] 上越界，症状是从历史里恢复一个短会话时整屏崩掉。
	n := len(m.logEntries)

	b.blocks = growBodySlice(b.blocks, n)
	b.states = growBodySlice(b.states, n)
	b.prefix = growBodySlice(b.prefix, n+1)

	// 索引覆盖的条目数与实际对不上：变多是追加（只补新的那几条），
	// 变少是历史被整体换掉（清空会话、换标签页）—— 那种情况下前缀和的旧值
	// 属于另一批条目，必须从头重排。
	if n != b.builtBlocks {
		if n < b.builtBlocks {
			b.validFrom = 0
		}
		b.builtBlocks = n
		b.version++
	}

	// 头部跟宽度、cwd、"有没有日志"都有关，而"行数没变、内容变了"（换项目但排版宽度
	// 一样）完全可能 —— 所以按整段文本比对，不能只比行数。
	head := m.renderWelcomePanel()
	if head != b.header || m.viewport.Width() != b.indexWidth {
		b.header = head
		b.headerLines = countBodyLines(head)
		b.indexWidth = m.viewport.Width()
		b.validFrom = 0
		b.version++
	}
	b.prefix[0] = b.headerLines
	if n != b.builtBlocks {
		b.builtBlocks = n
		b.version++
	}

	for i := min(b.validFrom, n); i < n; i++ {
		state := bodyEmitState{}
		if i > 0 {
			state = b.states[i-1]
		}
		b.blocks[i] = m.bodyBlockFor(&state, i)
		b.states[i] = state
		b.prefix[i+1] = b.prefix[i] + b.blocks[i].lines
	}
	b.validFrom = n
}

// growBodySlice 把切片长度改成 n，并**保留已有内容**：直接 make 会让前面已经排好的
// 块变成零值，而 validFrom 表示的是"排到哪儿了"，它不知道数组被换过。
func growBodySlice[T any](s []T, n int) []T {
	if len(s) >= n {
		return s[:n]
	}
	return append(s, make([]T, n-len(s))...)
}

// bodyBlockFor 排出条目 i 的贡献文本并推进顺序状态。它同时是行数统计与取词的唯一入口 ——
// 两处必须严格同构，否则窗口切片会错位。
//
// m.logEntries 是条目数的唯一权威，m.contentParts 只是它的渲染缓存：两切片可以短暂不等长
// （恢复了更短的历史、或新增条目还没走 rebuildContentCache），这里必须以 logEntries 为准，
// 并且对两边都夹住边界 —— 排版发生在 View 的调用链上，越界一次就是整屏 panic。
// 缓存还没跟上的那条按 0 行计，等 rebuildContentCache 补齐尾部时 bodyRepaint(firstChanged)
// 会把这一段重新排出来。
func (m *model) bodyBlockFor(state *bodyEmitState, i int) bodyBlock {
	if i < 0 || i >= len(m.logEntries) {
		return bodyBlock{}
	}
	entry := &m.logEntries[i]
	if entry.isVerbose {
		return bodyBlock{}
	}
	part := ""
	if i < len(m.contentParts) {
		part = m.contentParts[i]
	}
	if strings.TrimSpace(part) == "" {
		return bodyBlock{}
	}

	block := bodyBlock{}
	agentChanged := false
	if entry.from != "" && entry.from != state.lastAgent {
		if entry.eventType == "ai_response" {
			block.head = RenderAgentSeparator(entry.from, m.viewport.Width())
		} else {
			block.head = RenderAgentBadge(entry.from)
		}
		state.lastAgent = entry.from
		agentChanged = true
	}

	isThinking := entry.eventType == "thinking"
	block.text = part
	if isThinking && state.lastThinking && !agentChanged {
		block.text = renderThinkingContinuation(*entry, m.viewport.Width())
	}
	state.lastThinking = isThinking

	block.lines = countBodyLines(block.head) + countBodyLines(block.text)
	return block
}

// bodyTotalLines 是整段正文（含头部）的行数。
func (m *model) bodyTotalLines() int {
	m.bodyEnsureIndex()
	return m.body.prefix[len(m.body.prefix)-1]
}

// bodyHeight 是可见正文的行数。viewport 的高度由 View 的 bodyLayout 设定，这里只兜底。
func (m *model) bodyHeight() int {
	if h := m.viewport.Height(); h > 0 {
		return h
	}
	return 1
}

func (m *model) bodyMaxTop() int {
	return max(m.bodyTotalLines()-m.bodyHeight(), 0)
}

// bodySync 把可见区间喂给 viewport。窗口没变（游标、索引版本、高度都没动）就一个字节都
// 不做；viewport 自己的游标恒为 0，滚动只发生在 body.top 上 —— 两处都能滚就会错位。
func (m *model) bodySync() {
	b := &m.body
	height := m.bodyHeight()
	maxTop := m.bodyMaxTop()

	if !b.scrolledUp || b.top > maxTop {
		b.top = maxTop
		b.scrolledUp = false
	}
	if b.top < 0 {
		b.top = 0
	}

	if b.renderedTop == b.top && b.renderedHeight == height && b.renderedVer == b.version {
		return
	}
	m.viewport.SetContent(m.bodyText(b.top, height))
	m.viewport.SetYOffset(0)
	b.renderedTop, b.renderedHeight, b.renderedVer = b.top, height, b.version
}

// bodyRepaint 是"内容变了，把窗口重新落一遍"的统一入口：作废范围 → 重排 → 刷新正文缓存。
func (m *model) bodyRepaint(from int) {
	m.bodyInvalidate(from)
	m.bodySync()
	m.updateViewportCache()
}

// ── 滚动操作：全部按绝对行号说话 ─────────────────────────────────────────────

func (m *model) bodyScrollTo(line int) {
	m.bodyEnsureIndex()
	maxTop := m.bodyMaxTop()
	m.body.top = min(max(line, 0), maxTop)
	m.body.scrolledUp = m.body.top < maxTop
	m.bodySync()
}

func (m *model) bodyScrollBy(delta int) { m.bodyScrollTo(m.body.top + delta) }

func (m *model) bodyGotoTop()    { m.bodyScrollTo(0) }
func (m *model) bodyGotoBottom() { m.bodyScrollTo(m.bodyMaxTop()) }

func (m *model) bodyPageBy(sign int)     { m.bodyScrollBy(sign * m.bodyHeight()) }
func (m *model) bodyHalfPageBy(sign int) { m.bodyScrollBy(sign * max(m.bodyHeight()/2, 1)) }

func (m *model) bodyAtTop() bool { return m.body.top <= 0 }

func (m *model) bodyAtBottom() bool {
	m.bodyEnsureIndex()
	return m.body.top >= m.bodyMaxTop()
}

func (m *model) bodyScrollPercent() float64 {
	span := m.bodyTotalLines() - m.bodyHeight()
	if span <= 0 {
		return 0
	}
	return float64(m.body.top) / float64(span)
}

// visibleEntryIndices 圈出与可见窗口相交的条目区间，动画分支只重渲染这一段。
func (m *model) visibleEntryIndices() (start, end int) {
	m.bodyEnsureIndex()
	b := &m.body
	n := len(b.blocks)
	if n == 0 {
		return 0, -1
	}
	viewEnd := b.top + m.bodyHeight()

	start = sort.Search(n, func(i int) bool { return b.prefix[i+1] > b.top })
	end = sort.Search(n, func(i int) bool { return b.prefix[i] >= viewEnd }) - 1

	if start > end || start >= n {
		return 0, -1
	}
	return start, min(end, n-1)
}

// ── 取词 ────────────────────────────────────────────────────────────────────

// bodyText 返回绝对行区间 [start, start+count) 的文本：只走窗口用到的行，
// 窗口之外的历史既不复制也不拼接。
func (m *model) bodyText(start, count int) string {
	if count <= 0 {
		return ""
	}
	m.bodyEnsureIndex()
	b := &m.body
	end := min(start+count, m.bodyTotalLines())
	if start < 0 {
		start = 0
	}
	if end <= start {
		return ""
	}

	var out strings.Builder
	wrote := false
	// put 写 text 覆盖的绝对行区间 [cursor, cursor+lines) 与 [start, end) 的交集。
	// 分隔符按"已经写过"判断而不是按 out 非空 —— 空行也是一行，正文里就有。
	put := func(text string, cursor, lines int) {
		lo, hi := max(start, cursor), min(end, cursor+lines)
		if lo >= hi {
			return
		}
		if wrote {
			out.WriteString("\n")
		}
		out.WriteString(sliceBodyLines(text, lo-cursor, hi-lo))
		wrote = true
	}

	pos := 0
	if headLines := b.headerLines; headLines > 0 {
		put(b.header, pos, headLines)
		pos += headLines
	}

	i := sort.Search(len(b.blocks), func(i int) bool { return b.prefix[i+1] > start })
	if i > 0 {
		pos = b.prefix[i]
	}
	for ; i < len(b.blocks) && pos < end; i++ {
		block := &b.blocks[i]
		for _, piece := range [2]string{block.head, block.text} {
			lines := countBodyLines(piece)
			if lines == 0 {
				continue
			}
			put(piece, pos, lines)
			pos += lines
		}
	}
	return out.String()
}

// sliceBodyLines 取 s 的第 from 行起的 count 行（不含行尾换行符）。
// 两端都只在原串上移动游标，不逐行复制；不足 count 行时给到串尾。
func sliceBodyLines(s string, from, count int) string {
	if from < 0 || count <= 0 {
		return ""
	}
	pos := 0
	for i := 0; i < from; i++ {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			return ""
		}
		pos += nl + 1
	}
	start := pos
	// 走到区间最后一行的行首，再在它的换行处收尾（最后一行是串尾时没有换行）。
	for i := from; i < from+count-1; i++ {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			return s[start:]
		}
		pos += nl + 1
	}
	end := len(s)
	if nl := strings.IndexByte(s[pos:], '\n'); nl >= 0 {
		end = pos + nl
	}
	return s[start:end]
}
