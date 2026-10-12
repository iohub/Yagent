package tui

import (
	"strings"
	"testing"
	"time"

	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// 这个文件守的是"实时渲染不能丢消息"：事件只有 Update 一条通路，任何被丢弃或
// 被节流卡在缓冲里的事件都不会自己回来 —— 用户看到的是主界面少掉最后几条，
// 而从历史记录恢复又能看到同一条完整消息。

const zzTail = "【结尾】"

// zzFeedChunk 走 ai_chunk 的节流缓冲：内容先进 aiChunkBuffers，够阈值才落条目。
func zzFeedChunk(t *testing.T, m *model, agent, text string) *model {
	t.Helper()
	return feedEvent(m, buildEvent("ai_chunk", agent, text))
}

// zzStreamedText 汇总 director 当前流式条目里的正文。
func zzStreamedText(m *model) string {
	var sb strings.Builder
	for i := range m.logEntries {
		if m.logEntries[i].from == "director" {
			sb.WriteString(m.logEntries[i].content)
			sb.WriteString(m.logEntries[i].streamContent)
		}
	}
	return sb.String()
}

// zzPushDialog 压一个对话框，只为了让 guard 生效，不参与断言。
func zzPushDialog(t *testing.T, m *model) {
	t.Helper()
	d := components.NewTaskCompleteDialog(true, "waiting", components.LanguageEn)
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
}

// 对话框打开时曾经只放行 ai_stream_end/ai_response，其余事件直接丢弃；
// chunk 被丢就永远补不回来（没有重放队列）。
func TestLiveRender_ChunksDeliveredWhileDialogOpenStillLand(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = zzFeedChunk(t, m, "director", "开头内容")

	zzPushDialog(t, m)
	m = zzFeedChunk(t, m, "director", zzTail)
	m = feedEvent(m, buildEvent("ai_stream_end", "director", nil))
	m = feedEvent(m, buildEvent("ai_response", "director", "开头内容"+zzTail))

	if got := zzStreamedText(m); !strings.Contains(got, zzTail) {
		t.Errorf("tail dropped while a dialog was open, transcript holds %q", got)
	}
}

// 全屏 timeline 里到达的事件必须照常消费，否则退出全屏后那几条永久缺失。
func TestLiveRender_EventsConsumedInTimelineFullscreen(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = zzFeedChunk(t, m, "director", "开头内容")

	m.timelineFullscreenMode = true
	m = zzFeedChunk(t, m, "director", zzTail)
	m = feedEvent(m, buildEvent("ai_stream_end", "director", nil))
	m = feedEvent(m, buildEvent("ai_response", "director", "开头内容"+zzTail))
	m.timelineFullscreenMode = false

	view := stripANSIForChrome(m.View().Content)
	if !strings.Contains(view, zzTail) {
		t.Errorf("message streamed during timeline fullscreen never reached the main view:\n%s", view)
	}
}

// 历史浏览同理：事件不会排队等"退出后再处理"。
func TestLiveRender_EventsConsumedInHistoryMode(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m.historyMode = true
	m = feedEvent(m, buildEvent("ai_response", "director", "历史模式期间到达的回复"+zzTail))
	m.historyMode = false

	if got := zzStreamedText(m); !strings.Contains(got, zzTail) {
		t.Errorf("ai_response consumed while history mode was open was dropped: %q", got)
	}
}

// 消费事件时必须接替监听，否则之后所有事件都没人读；反过来，不动用 pending cmd 的
// 消息（按键、resize）不能补回去，否则监听者按次数堆积成一批阻塞的 goroutine。
func TestLiveRender_ListenerStaysArmedInOverlayModes(t *testing.T) {
	for name, setup := range map[string]func(*model){
		"timeline":  func(mm *model) { mm.timelineFullscreenMode = true },
		"thinklink": func(mm *model) { mm.thinklinkMode = true },
		"history":   func(mm *model) { mm.historyMode = true },
		"resume":    func(mm *model) { mm.resumeMode = true },
	} {
		m := chromeTestModel(t, 120, 40)
		m.taskRunning = true
		setup(m)

		before := len(m.logEntries)
		_, cmd := m.Update(taskEventMsg{
			event: buildEvent("ai_response", "director", "覆盖层期间到达的回复"+zzTail),
		})
		if cmd == nil {
			t.Errorf("%s mode: consumed an event without re-arming the listener", name)
		}
		if len(m.logEntries) != before+1 {
			t.Errorf("%s mode: event swallowed by the overlay, logEntries stayed at %d", name, before)
		}

		_, sizeCmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		if sizeCmd != nil {
			t.Errorf("%s mode: a non-event message scheduled an extra listener", name)
		}
	}
}

// 节流缓冲的 300ms 超时原先只在下一个 chunk 到达时求值：流一停，尾巴就一直躺在
// 缓冲里。tick 必须替它落地。
func TestLiveRender_StreamBufferTailFlushesOnTick(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = zzFeedChunk(t, m, "director", zzTail)

	if got := zzStreamedText(m); strings.Contains(got, zzTail) {
		t.Fatalf("single chunk should still be buffered, got %q", got)
	}

	buf := m.aiChunkBuffers["director"]
	if buf == nil {
		t.Fatal("expected a pending chunk buffer for director")
	}
	buf.lastFlush = time.Now().Add(-aiChunkFlushInterval - time.Second)

	newModel, _ := m.Update(tickMsg{})
	m = newModel.(*model)
	if got := zzStreamedText(m); !strings.Contains(got, zzTail) {
		t.Errorf("buffered tail was not flushed by the tick: %q", got)
	}
}

// 用户对确认框的回答是 TUI 自己发布的，会经总线回送一份；它不是 agent 的输出，
// 不该在日志里留下一行 map[...]。
func TestLiveRender_SelfPublishedAnswerStaysOutOfTranscript(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true

	_, cmd := m.Update(taskEventMsg{
		event: buildEvent("user_help_response", "director", map[string]interface{}{
			"request_id": "r1",
			"answer":     "yes",
		}),
	})
	if len(m.logEntries) != 0 {
		t.Errorf("echo of our own publish reached the transcript: %d entries", len(m.logEntries))
	}
	if cmd == nil {
		t.Error("skipping the echo must still re-arm the listener")
	}
}

// 任务收尾（含取消/异常）时 tick 链可能就此停下，缓冲里的尾巴再没机会落地。
func TestLiveRender_StreamBufferTailFlushesOnTaskComplete(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = zzFeedChunk(t, m, "director", zzTail)

	newModel, _ := m.Update(taskCompleteMsg{taskID: "t1", result: "done"})
	m = newModel.(*model)
	if got := zzStreamedText(m); !strings.Contains(got, zzTail) {
		t.Errorf("buffered tail lost at task completion: %q", got)
	}
}
