package tui

import (
	"runtime/debug"
	"strings"
	"testing"
)

// failOnPanic 把排版路径上的 panic 变成这条测试的失败，而不是让整个包的测试二进制中断。
// 用法：defer failOnPanic(t) —— 崩溃现场（恢复更短的历史）原本就是这么进来的。
func failOnPanic(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("不应该 panic: %v\n%s", r, debug.Stack())
	}
}

// seedBodyEntries 直接摆条目进正文：虚拟化之后 viewport 只显示可见区间，
// 一切断言都得走"条目 → 排版块 → 行索引"这条真实的路，不能再直接塞 viewport。
func seedBodyEntries(m *model, entries ...logEntry) {
	for _, e := range entries {
		m.logEntries = append(m.logEntries, e)
		m.contentParts = append(m.contentParts, e.content)
	}
	m.bodyInvalidate(0)
	m.bodySync()
}

// mixedTranscript 覆盖排版块的所有形态：带分隔行的普通条目、多行条目、被跳过的
// verbose 与空白条目、thinking 连成续行。
func mixedTranscript() []logEntry {
	return []logEntry{
		{eventType: "ai_response", from: "director", content: "第一行\n第二行\n第三行"},
		{eventType: "llm_call_start", from: "director", content: "被跳过的 verbose 行", isVerbose: true},
		{eventType: "thinking", from: "director", content: "思考一"},
		{eventType: "thinking", from: "director", content: "思考二\n还有一行"},
		{eventType: "user_message", from: "", content: "   "},
		{eventType: "ai_response", from: "coding", content: "换 agent 了\n所以有分隔行"},
		{eventType: "thinking", from: "single", content: "只有一行"},
	}
}

// TestBody_WindowIsAPlainSliceOfTheTranscript 是虚拟化的主闸门：
// 任意游标、任意高度上，喂给 viewport 的窗口必须等于全文的连续切片。
// 条目边界、分隔行、被跳过的条目、thinking 续行都在这条断言下现形 —— 错一行整屏错位。
func TestBody_WindowIsAPlainSliceOfTheTranscript(t *testing.T) {
	m := chromeTestModel(t, 100, 12)
	seedBodyEntries(m, mixedTranscript()...)

	full := m.bodyText(0, m.bodyTotalLines())
	lines := strings.Split(full, "\n")
	if got := len(lines); got != m.bodyTotalLines() {
		t.Fatalf("total=%d 但全文只有 %d 行：行数统计与取词不同源", m.bodyTotalLines(), got)
	}

	for _, height := range []int{1, 4, 12, 40} {
		for top := 0; top < len(lines); top++ {
			want := strings.Join(lines[top:min(top+height, len(lines))], "\n")
			if got := m.bodyText(top, height); got != want {
				t.Fatalf("height=%d top=%d: 窗口不是全文的连续切片\n---- got ----\n%q\n---- want ----\n%q",
					height, top, got, want)
			}
		}
	}
}

// TestBody_ViewportHoldsOnlyTheWindow 钉住这一档优化的本体：
// viewport 拿到的行数就是可见区间，历史不进 SetContent。
// （viewport 自己的 TotalLineCount 因此只描述窗口 —— 整段历史的行号一律看 body 索引。）
func TestBody_ViewportHoldsOnlyTheWindow(t *testing.T) {
	m := chromeTestModel(t, 100, 6)
	var big []logEntry
	for i := 0; i < 40; i++ {
		big = append(big, logEntry{eventType: "ai_response", from: "director",
			content: "第 " + itoa(i) + " 条日志\n这条的第二行"})
	}
	seedBodyEntries(m, big...)

	total := m.bodyTotalLines()
	if total <= 6 {
		t.Fatalf("夹具必须溢出：total=%d", total)
	}
	m.bodyScrollTo(total - 6) // 贴到底部窗口
	if got := m.viewport.TotalLineCount(); got != 6 {
		t.Errorf("viewport 只该拿到 6 行，实际 %d —— 虚拟化没接上", got)
	}
	content := m.viewport.GetContent()
	if strings.Contains(content, "第 0 条日志") {
		t.Error("窗口里还带着开头的历史")
	}
	if !strings.Contains(content, "第 39 条日志") {
		t.Error("窗口里没有贴底的最后一条")
	}
	if want := m.bodyText(total-6, 6); content != want {
		t.Errorf("viewport 内容与窗口不一致\n got=%q\nwant=%q", content, want)
	}
}

// TestBody_AppendRebuildsOnlyTheTail 钉住"追加一条 = O(1)"：
// 前面的排版块必须原样留着（曾经踩过 growBodySlice 用 make 换数组、把已排好的块清零的坑）。
func TestBody_AppendRebuildsOnlyTheTail(t *testing.T) {
	m := chromeTestModel(t, 100, 12)
	seedBodyEntries(m, mixedTranscript()...)

	firstBlock := m.body.blocks[0]
	before := m.bodyText(0, 4)

	entry := logEntry{eventType: "ai_response", from: "director", content: "新追加的一条"}
	m.logEntries = append(m.logEntries, entry)
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
	if got := m.body.blocks[0]; got.head != firstBlock.head || got.text != firstBlock.text || got.lines != firstBlock.lines {
		t.Errorf("追加一条把开头的排版块弄丢了： %+v vs %+v", got, firstBlock)
	}
	if got := m.bodyText(0, 4); got != before {
		t.Errorf("开头的正文变了\n got=%q\nwant=%q", got, before)
	}

	// 改中间一条：只该从那条起重排，不是整段
	m.bodyInvalidate(3)
	if got := m.body.validFrom; got != 3 {
		t.Errorf("validFrom=%d，期望 3（增量重建的起点）", got)
	}
}

// TestBody_FollowTail 是贴底跟随的语义：贴着底时新内容要跟着出来，
// 用户往上翻过之后新内容不许把视野拽走。
func TestBody_FollowTail(t *testing.T) {
	m := chromeTestModel(t, 100, 8)
	seedBodyEntries(m, logEntry{eventType: "ai_response", from: "director", content: "一\n二\n三"})
	m.bodyGotoBottom()
	if !m.bodyAtBottom() {
		t.Fatal("G 之后应该贴底")
	}

	add := func(text string) {
		m.logEntries = append(m.logEntries, logEntry{eventType: "ai_response", from: "director", content: text})
		m.contentParts = append(m.contentParts, text)
		m.bodyRepaint(len(m.contentParts) - 1)
	}
	topBefore := m.body.top
	add("四\n五\n六")
	if m.body.top <= topBefore {
		t.Errorf("贴底时新内容没把窗口推到底：top=%d（之前 %d）", m.body.top, topBefore)
	}
	if !strings.Contains(m.viewport.GetContent(), "六") {
		t.Error("贴底跟随丢了最后一条的第二行")
	}

	m.bodyScrollTo(1)
	if !m.body.scrolledUp {
		t.Error("翻上去之后 scrolledUp 该为真，否则下一条新内容会把视野拽走")
	}
	held := m.body.top
	add("七\n八")
	if m.body.top != held {
		t.Errorf("视野被新内容拽走了：%d → %d", held, m.body.top)
	}
}

// TestBody_ScrollRulerReadsAbsoluteIndex 钉住状态栏与滚动条的口径：
// 它们必须按整段历史说话，而不是按 viewport 手里那几行。
func TestBody_ScrollRulerReadsAbsoluteIndex(t *testing.T) {
	m := chromeTestModel(t, 100, 10)
	var big []logEntry
	for i := 0; i < 30; i++ {
		big = append(big, logEntry{eventType: "ai_response", from: "director", content: itoa(i)})
	}
	seedBodyEntries(m, big...)
	m.viewportDirty = true

	m.bodyGotoTop()
	if got := m.viewportRuler(); got != "TOP" {
		t.Errorf("ruler=%q，顶部该是 TOP", got)
	}
	m.bodyGotoBottom()
	if got := m.viewportRuler(); got != "BOT" {
		t.Errorf("ruler=%q，底部该是 BOT", got)
	}
	m.bodyScrollTo(m.bodyMaxTop() / 2)
	if got := m.viewportRuler(); got == "TOP" || got == "BOT" || !strings.HasSuffix(got, "%") {
		t.Errorf("ruler=%q，中间该是百分比", got)
	}
	if p := m.bodyScrollPercent(); p <= 0.2 || p >= 0.8 {
		t.Errorf("scrollPercent=%v，期望落在 0.2~0.8", p)
	}
}

// TestBody_PageKeysClamp 覆盖 f/b/u/d/G<n> 的边界：不许滚过末尾或越过开头。
func TestBody_PageKeysClamp(t *testing.T) {
	m := chromeTestModel(t, 100, 10)
	seedBodyEntries(m, mixedTranscript()...)

	m.bodyPageBy(1)
	m.bodyPageBy(1)
	m.bodyPageBy(1)
	if !m.bodyAtBottom() {
		t.Errorf("连按三次翻页后应该贴底，top=%d max=%d", m.body.top, m.bodyMaxTop())
	}
	for i := 0; i < 6; i++ {
		m.bodyPageBy(-1)
	}
	if m.body.top != 0 {
		t.Errorf("往回翻应该夹在 0，top=%d", m.body.top)
	}

	m.bodyScrollBy(-50)
	if m.body.top != 0 {
		t.Errorf("超范围上滚应该夹在 0，top=%d", m.body.top)
	}
	m.bodyScrollBy(1 << 20)
	if !m.bodyAtBottom() {
		t.Error("超范围下滚应该停在最后一屏")
	}
}

// TestBody_TabSwitchKeepsCursorAndWindow 钉住 tab 之间各自一份滚动位置：
// 恢复之后 viewport 里必须是那个 tab 的窗口，而不是上一个 tab 留下的内容。
func TestBody_TabSwitchKeepsCursorAndWindow(t *testing.T) {
	m := chromeTestModel(t, 120, 12)
	m.initSessionTabs()
	m.View() // 让宽高先落到 View 的口径，再摆内容

	seedBodyEntries(m, mixedTranscript()...)
	m.View()
	m.bodyScrollTo(3)
	m.updateViewportCache()

	m.newSessionTabAction()
	seedBodyEntries(m, logEntry{eventType: "ai_response", from: "repo", content: "另一个会话\n第二行"})
	m.View()
	if strings.Contains(m.viewport.GetContent(), "第一行") {
		t.Fatal("夹具没做出两个 tab 内容不同的形状")
	}

	m.switchSessionTab(0)
	if m.body.top != 3 {
		t.Errorf("回来之后游标丢了：top=%d，期望 3", m.body.top)
	}
	// 窗口必须是这个 tab 自己的正文，不能留着另一个会话的行
	restored := m.viewport.GetContent()
	if !strings.Contains(restored, m.bodyText(m.body.top, 1)) {
		t.Errorf("窗口内容与该 tab 的窗口不一致：\n got=%q\nwant=%q", restored, m.bodyText(m.body.top, 1))
	}
	if strings.Contains(restored, "另一个会话") {
		t.Error("切回来还带着另一个 tab 的正文")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// TestBody_EntriesShorterThanRenderCache 是"恢复一个条目更少的历史"崩溃的最小复现：
// contentParts 只是 logEntries 的派生排版缓存，它可以短暂比条目长（恢复现场留下的残留），
// 而条目数的权威必须是 logEntries —— 修前取的是 len(contentParts)，索引一作废就沿着
// 更长的缓存走到 m.logEntries[4]，正是生产里 View 纠正视口宽度那一步的越界 panic。
func TestBody_EntriesShorterThanRenderCache(t *testing.T) {
	defer failOnPanic(t)

	m := chromeTestModel(t, 100, 12)
	entries := mixedTranscript()
	seedBodyEntries(m, entries...)
	if len(m.contentParts) != len(entries) || len(entries) < 5 {
		t.Fatalf("夹具需要 5 条以上且两切片等长：entries=%d parts=%d", len(entries), len(m.contentParts))
	}

	// 只把条目截短，排版缓存保持上一批的长度 —— 恢复历史之后就是这个形状。
	m.logEntries = append([]logEntry(nil), entries[:4]...)
	m.body.forget() // 作废行索引，逼出一次从第 0 条起的整体重排（清空会话/恢复/View 纠正宽度都走它）

	total := m.bodyTotalLines()
	if got := len(m.body.blocks); got != len(m.logEntries) {
		t.Errorf("排版块 = %d，条目 = %d：索引必须按 logEntries 建", got, len(m.logEntries))
	}
	if got := len(strings.Split(m.bodyText(0, total), "\n")); got != total {
		t.Errorf("行数统计与取词不同源：bodyTotalLines=%d 但全文 %d 行", total, got)
	}
	m.bodySync()
	if got := m.body.validFrom; got != len(m.logEntries) {
		t.Errorf("validFrom=%d，期望 %d（索引补齐到条目数）", got, len(m.logEntries))
	}
}

// TestBody_RenderCacheShorterThanEntries 是反方向的失配：条目已经到位、渲染缓存还没跟上。
// 该条目这一帧按 0 行计（不是 panic，也不是把后面的条目顶上来），等 rebuildContentCache
// 补齐尾部时由 bodyRepaint 重新排版。
func TestBody_RenderCacheShorterThanEntries(t *testing.T) {
	defer failOnPanic(t)

	m := chromeTestModel(t, 100, 12)
	seedBodyEntries(m, mixedTranscript()...)

	kept := len(m.logEntries) - 2
	m.contentParts = m.contentParts[:kept]
	m.body.forget()

	total := m.bodyTotalLines()
	if got := len(m.body.blocks); got != len(m.logEntries) {
		t.Fatalf("排版块 = %d，条目 = %d：缓存滞后不许缩短索引", got, len(m.logEntries))
	}
	if got := len(strings.Split(m.bodyText(0, total), "\n")); got != total {
		t.Errorf("行数统计与取词不同源：bodyTotalLines=%d 但全文 %d 行", total, got)
	}
	m.bodySync()

	// 缓存补齐之后必须重新排版，条目一条不许少。
	m.needFullRebuild = true
	m.rebuildContentCache()
	if len(m.contentParts) != len(m.logEntries) {
		t.Errorf("contentParts=%d，logEntries=%d", len(m.contentParts), len(m.logEntries))
	}
}
