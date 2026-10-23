package tui

import (
	"strings"
	"testing"
	"time"

	"yagent/internal/thinklink"
)

// ── B1：流式 thought 面板增量渲染 ────────────────────────────────────────────

// 每个中间帧都必须与"把已到达的文本一次性渲染"逐字相同 —— 面板 memo 只是把
// 已闭合块的重画省掉，不许改变输出（TestTUI_StreamPanelsMatchOneShot 的全部意义）。
func TestTUI_StreamPanelsMatchOneShot(t *testing.T) {
	InitLangManager()
	m := chromeTestModel(t, 140, 45)
	const width = 89

	steps := []string{
		"<thought>\ngoal: 修布局\nstep: 读完 View\nreason: 先加闸门\nplan:\n- [x] 读代码\n- [>] 加闸门\n</thought>\n",
		"第一段正文，块与块之间还有散文。\n\n",
		"<thought>\ngoal: 修布局\nstep: 闸门生效\nreason: 再补面板 memo\n</thought>\n第二段正文\n",
		"<thought>\nreason: 这一块的尾巴还没闭合\n",
	}
	oneShot := func(content string) string {
		blocks, rest := thinklink.SplitThoughtBlocks(content)
		if len(blocks) == 0 {
			return content
		}
		return joinThinklinkPanels(renderThinklinkPanels(blocks, width-thinklinkPanelGutter), rest)
	}

	entry := &logEntry{eventType: "ai_stream", streaming: true}
	var accumulated strings.Builder
	for i, step := range steps {
		accumulated.WriteString(step)
		entry.streamContent = accumulated.String()
		entry.clearRenderCache()

		if got := renderStreamEntry(entry, width); got != oneShot(entry.streamContent+"▌") {
			t.Errorf("step %d: 增量渲染与一次性渲染不一致\n---- got ----\n%s\n---- want ----\n%s",
				i, stripANSI(got), stripANSI(oneShot(entry.streamContent+"▌")))
		}
	}

	// 已闭合的三个块进 memo（最后那步的块没闭合，不进）
	if len(entry.streamPanelSrc) != 2 {
		t.Errorf("memo 里应有 2 个已闭合块，实际 %d", len(entry.streamPanelSrc))
	}

	// memo 里的面板必须真的被复用，而不是每次都重画 —— 这是这一档优化的全部意义
	entry.streamPanels[0] = "SENTINEL-PANEL"
	if got := renderStreamEntry(entry, width); !strings.Contains(stripANSI(got), "SENTINEL-PANEL") {
		t.Error("已闭合块又被整段重画了一遍，memo 没起作用")
	}

	// 宽度变了必须整段重画：旧面板是按旧列宽排的
	if got := renderStreamEntry(entry, 60); got != oneShotAt(entry.streamContent+"▌", 60) {
		t.Errorf("换宽度后与一次性渲染不一致\n---- got ----\n%s\n---- want ----\n%s",
			stripANSI(got), stripANSI(oneShotAt(entry.streamContent+"▌", 60)))
	}
	if want := 60 - thinklinkPanelGutter; entry.streamPanelW != want {
		t.Errorf("换宽度后 memo 应按新宽度重建：panelW=%d，期望 %d", entry.streamPanelW, want)
	}

	// 内容被替换（不是追加）时 memo 作废：ai_response 就会这样改写 streamContent
	entry.streamContent = "<thought>\ngoal: 另一件事\nreason: 替换而不是追加\n</thought>\n收尾"
	if got := renderStreamEntry(entry, width); got != oneShotAt(entry.streamContent+"▌", width) {
		t.Errorf("内容被替换后残留了旧面板\n---- got ----\n%s", stripANSI(got))
	}

	// 定稿的流式条目与普通条目一样走渲染缓存：内容没被重新标脏就不重画
	entry.streaming = false
	entry.clearRenderCache()
	first := m.renderSingleEntry(entry, width)
	entry.streamContent += "又来了一段"
	if again := m.renderSingleEntry(entry, width); again != first {
		t.Error("定稿条目没命中渲染缓存，每次重排都会重画")
	}
	entry.clearRenderCache()
	if again := m.renderSingleEntry(entry, width); again == first {
		t.Error("清了缓存还不重画，缓存判断写反了")
	}
}

func oneShotAt(content string, width int) string {
	blocks, rest := thinklink.SplitThoughtBlocks(content)
	if len(blocks) == 0 {
		return content
	}
	return joinThinklinkPanels(renderThinklinkPanels(blocks, width-thinklinkPanelGutter), rest)
}

// ── A：流式追加的重排合并 ────────────────────────────────────────────────────

func directorStreamParts(m *model) string {
	var out strings.Builder
	for i := range m.logEntries {
		if m.logEntries[i].eventType == "ai_stream" {
			out.WriteString(m.contentParts[i])
		}
	}
	return out.String()
}

func TestTUI_StreamRepaintIsCoalesced(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = zzFeedChunk(t, m, "director", strings.Repeat("甲", 40)) // ≥64 字节，立刻 flush
	_ = m.View()
	if !strings.Contains(directorStreamParts(m), "甲") {
		t.Fatalf("第一次落地没出现： %q", directorStreamParts(m))
	}

	// 合并窗口还开着：再来一次 flush 只标脏，不重排整段正文
	m = zzFeedChunk(t, m, "director", strings.Repeat("乙", 40))
	if !m.hasDirtyEntries() {
		t.Fatal("flush 应把条目标脏")
	}
	held := directorStreamParts(m)
	_ = m.View()
	if got := directorStreamParts(m); got != held {
		t.Error("窗口内不该重排：正文已经跟着新 chunk 变了")
	}
	if !m.viewportDirty {
		t.Error("被推迟的重排必须留着 viewportDirty，否则没人再来触发")
	}

	// 窗口到点：下一次 View 必须落地
	m.lastViewportRepaint = time.Now().Add(-streamRepaintInterval - time.Millisecond)
	_ = m.View()
	if got := directorStreamParts(m); !strings.Contains(got, "乙") {
		t.Errorf("窗口到点后没落地：%q", got)
	}
	if m.hasDirtyEntries() {
		t.Error("落地后脏标记应清空")
	}
}

// 只有"纯流式追加"可以推迟：定稿、非流式内容必须立刻出现。
func TestTUI_NonStreamDirtIsNeverHeld(t *testing.T) {
	for _, name := range []string{"stream-end", "ai_response"} {
		m := chromeTestModel(t, 120, 40)
		m.taskRunning = true
		m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
		m = zzFeedChunk(t, m, "director", strings.Repeat("甲", 40))
		_ = m.View()
		m.lastViewportRepaint = time.Now() // 窗口刚排过，关着

		idx := m.aiStreamActiveEntries["director"]
		switch name {
		case "stream-end":
			le := &m.logEntries[idx]
			le.streamContent += strings.Repeat("乙", 40)
			le.content = le.streamContent
			le.streaming = false
			le.clearRenderCache()
			m.markEntryDirty(idx)
			m.viewportDirty = true
		case "ai_response":
			m = feedEvent(m, buildEvent("ai_response", "director", "定稿的回答"))
		}

		_ = m.View()
		joined := strings.Join(m.contentParts, "|")
		if name == "stream-end" && !strings.Contains(joined, "乙") {
			t.Errorf("%s: 定稿被合并窗口推迟了", name)
		}
		if name == "ai_response" && !strings.Contains(joined, "定稿的回答") {
			t.Errorf("%s: 非流式内容被合并窗口推迟了", name)
		}
	}
}

// 重排被推迟时 tick 链必须还活着，否则尾巴要等到下一条消息才落地。
func TestTUI_TickStaysArmedWhileRepaintHeld(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.logEntries = append(m.logEntries, logEntry{eventType: "ai_stream", streaming: true})
	m.activeAnim = false
	m.taskRunning = false
	m.viewportDirty = false
	m.markEntryDirty(0)

	_, cmd := m.Update(tickMsg{})
	if cmd == nil {
		t.Error("有内容待落地时 tick 链被停掉了，推迟的重排再没人触发")
	}
	if m.tickStarted {
		return
	}
	t.Error("tick 链没重新武装")
}

// ── B2：固定头部缓存 ─────────────────────────────────────────────────────────

// 有日志时头部只跟宽度与 cwd 有关：不许顺手算 footer 高度 —— 那条路会走 token
// 面板与 langManager（未装配的夹具里直接 nil panic），而头部根本用不到行高。
func TestTUI_WelcomeHeaderSkipsFooterMath(t *testing.T) {
	m := newTestModel()
	m.termWidth, m.termHeight = 140, 45
	m.logEntries = append(m.logEntries, logEntry{eventType: "ai_response", from: "director", content: "一条日志"})

	out := m.renderWelcomePanel()
	if out == "" {
		t.Fatal("空白头部不是缓存的本意")
	}
	if m.welcomeCacheKey == "" || m.welcomeCache != out {
		t.Errorf("头部应落进缓存：key=%q", m.welcomeCacheKey)
	}
	if again := m.renderWelcomePanel(); again != out {
		t.Error("同样输入应命中缓存并返回同一份内容")
	}
}

func TestTUI_WelcomeHeaderKeyTracksInputs(t *testing.T) {
	m := newTestModel()
	InitLangManager()
	m.termWidth, m.termHeight = 140, 45
	m.logEntries = append(m.logEntries, logEntry{eventType: "ai_response", from: "director", content: "一条日志"})
	header := m.renderWelcomePanel()
	headerKey := m.welcomeCacheKey

	m.termWidth = 90
	if m.renderWelcomePanel() == header {
		t.Error("换宽度后头部没有重画")
	}
	if m.welcomeCacheKey == headerKey {
		t.Error("换宽度后缓存 key 没变")
	}

	// 空白启动屏要行高：换高度必须重画
	m.logEntries = nil
	startup := m.renderWelcomePanel()
	m.termHeight = 30
	if m.renderWelcomePanel() == startup {
		t.Error("换终端高度后启动屏没有重画")
	}
}
