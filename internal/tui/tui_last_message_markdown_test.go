package tui

import (
	"strings"
	"testing"
	"time"

	"codeactor/internal/messaging"
	"codeactor/internal/tui/components"

	"charm.land/bubbles/v2/viewport"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// helper: build a minimal model sufficient for ai_stream/ai_response tests
func newTestModel() *model {
	vp := viewport.New(viewport.WithWidth(100), viewport.WithHeight(30))
	vp.Style = lipgloss.NewStyle().Padding(0, 1)

	gr, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(80),
	)
	if err != nil {
		gr = nil
	}

	return &model{
		termWidth:         100,
		termHeight:        30,
		viewport:          vp,
		glamourRenderer:   gr,
		useDarkStyle:      true,

		logEntries:               make([]logEntry, 0),
		contentParts:             make([]string, 0),
		dirtyEntryIndices:        make(map[int]struct{}),
		aiStreamActiveEntries:    make(map[string]int),
		aiStreamCompletedEntries: make(map[string]int),
		aiChunkBuffers:          make(map[string]*aiChunkBuffer),

		glamourCache:    make(map[string]string),
		glamourLRU:      make([]string, 0, 32),
		glamourCacheCap: 32,

		dialogStack: components.NewDialogStack(),
		contentCache: &strings.Builder{},
	}
}

// helper: wrap an event into a taskEventMsg and feed it through Update
func feedEvent(m *model, evt *messaging.MessageEvent) *model {
	newModel, _ := m.Update(taskEventMsg{event: evt})
	return newModel.(*model)
}

// buildEvent is a convenience for constructing a MessageEvent
func buildEvent(typ, from string, content interface{}) *messaging.MessageEvent {
	// ai_chunk 事件的 Content 实际为 map[string]interface{}{"content": "..."}
	if typ == "ai_chunk" {
		if s, ok := content.(string); ok {
			content = map[string]interface{}{"content": s}
		}
	}
	return &messaging.MessageEvent{
		Type:      messaging.EventType(typ),
		From:      from,
		Content:   content,
		Timestamp: time.Now(),
	}
}

// ── Scenario A: dialog is showing, ai_response must still be processed ──

func TestScenarioA_AIResponseThroughDialog(t *testing.T) {
	m := newTestModel()

	// 1) Stream the message normally (no dialog yet)
	m = feedEvent(m, buildEvent("ai_stream_start", "agent", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "agent", "**Hello** world"))
	m = feedEvent(m, buildEvent("ai_stream_end", "agent", nil))

	// After ai_stream_end, the entry should be in aiStreamCompletedEntries
	if len(m.aiStreamCompletedEntries) != 1 {
		t.Fatalf("expected 1 completed stream entry, got %d", len(m.aiStreamCompletedEntries))
	}
	lastIdx := m.aiStreamCompletedEntries["agent"]
	if m.logEntries[lastIdx].eventType != "ai_stream" {
		t.Fatalf("expected eventType='ai_stream', got %q", m.logEntries[lastIdx].eventType)
	}

	// 2) Simulate taskCompleteMsg having already pushed a dialog
	d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.LanguageEn)
	d.SetBounds(100, 30)
	m.dialogStack.Push(d)
	if m.dialogStack.Len() != 1 {
		t.Fatal("expected dialogStack to have 1 dialog")
	}

	// 3) Now send ai_response — this is the critical race condition
	m = feedEvent(m, buildEvent("ai_response", "agent", "**Hello** world"))

	// 4) Assert: the entry was updated to ai_response type
	if len(m.logEntries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(m.logEntries))
	}
	entry := m.logEntries[0]
	if entry.eventType != "ai_response" {
		t.Fatalf("expected eventType='ai_response', got %q", entry.eventType)
	}
	if !entry.finalized {
		t.Error("expected finalized=true")
	}

	// 5) Rebuild content cache and verify Glamour rendered markdown (contains ANSI)
	m.rebuildContentCache()
	if len(m.contentParts) != 1 {
		t.Fatalf("expected 1 contentPart, got %d", len(m.contentParts))
	}
	if !strings.Contains(m.contentParts[0], "\x1b[") {
		t.Fatalf("expected Glamour-rendered content to contain ANSI escape sequences, got: %q", m.contentParts[0])
	}
}

// ── Scenario B: dialog is showing, tool_call_start must STILL be blocked ──

func TestScenarioB_OtherEventsBlockedByDialog(t *testing.T) {
	m := newTestModel()

	// Push a dialog
	d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.LanguageEn)
	d.SetBounds(100, 30)
	m.dialogStack.Push(d)

	beforeLen := len(m.logEntries)

	// Send a tool_call_start event — should be blocked by dialogStack guard
	m = feedEvent(m, buildEvent("tool_call_start", "agent", map[string]interface{}{
		"tool": "run_bash",
		"args": "{}",
	}))

	// Assert: no new entry was created
	if len(m.logEntries) != beforeLen {
		t.Fatalf("expected %d log entries (event blocked), got %d", beforeLen, len(m.logEntries))
	}
}

// ── Scenario C: normal path without dialog ──

func TestScenarioC_NormalStreamToEndToEnd(t *testing.T) {
	m := newTestModel()

	// Full stream sequence without any dialog
	m = feedEvent(m, buildEvent("ai_stream_start", "agent", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "agent", "# Heading\n\nSome **bold** text."))
	m = feedEvent(m, buildEvent("ai_stream_end", "agent", nil))
	m = feedEvent(m, buildEvent("ai_response", "agent", "# Heading\n\nSome **bold** text."))

	if len(m.logEntries) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(m.logEntries))
	}
	entry := m.logEntries[0]
	if entry.eventType != "ai_response" {
		t.Fatalf("expected eventType='ai_response', got %q", entry.eventType)
	}
	if !entry.finalized {
		t.Error("expected finalized=true")
	}

	// Rebuild and check Glamour rendering
	m.rebuildContentCache()
	if len(m.contentParts) != 1 {
		t.Fatalf("expected 1 contentPart, got %d", len(m.contentParts))
	}
	if !strings.Contains(m.contentParts[0], "\x1b[") {
		t.Fatalf("expected Glamour-rendered content to contain ANSI escape sequences, got: %q", m.contentParts[0])
	}
}

// ── ai_stream_end should also穿透 dialog ──

func TestScenarioA_AIStreamEndThroughDialog(t *testing.T) {
	m := newTestModel()

	// Start a stream
	m = feedEvent(m, buildEvent("ai_stream_start", "agent", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "agent", "streaming content"))

	// Push dialog (simulating taskCompleteMsg arrived first)
	d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.LanguageEn)
	d.SetBounds(100, 30)
	m.dialogStack.Push(d)

	// ai_stream_end should still be processed
	m = feedEvent(m, buildEvent("ai_stream_end", "agent", nil))

	// Entry should be in completed map and streaming should be false
	if len(m.aiStreamCompletedEntries) != 1 {
		t.Fatalf("expected 1 completed stream entry, got %d", len(m.aiStreamCompletedEntries))
	}
	lastIdx := m.aiStreamCompletedEntries["agent"]
	if m.logEntries[lastIdx].streaming {
		t.Error("expected streaming=false after ai_stream_end")
	}
}

// ── Buffer flush threshold: accumulate 5 runes before rendering ──

func TestScenarioD_BufferFlushThreshold(t *testing.T) {
	m := newTestModel()

	// Start a stream
	m = feedEvent(m, buildEvent("ai_stream_start", "agent", ""))
	idx := 0 // the stream entry index

	// 实时更新：每个 chunk 到达后 streamContent 立即包含全部内容
	m = feedEvent(m, buildEvent("ai_chunk", "agent", "ab"))
	if m.logEntries[idx].streamContent != "ab" {
		t.Fatalf("expected streamContent 'ab' after first chunk, got %q", m.logEntries[idx].streamContent)
	}

	m = feedEvent(m, buildEvent("ai_chunk", "agent", "cd"))
	if m.logEntries[idx].streamContent != "abcd" {
		t.Fatalf("expected streamContent 'abcd' after second chunk, got %q", m.logEntries[idx].streamContent)
	}

	m = feedEvent(m, buildEvent("ai_chunk", "agent", "e"))
	if m.logEntries[idx].streamContent != "abcde" {
		t.Fatalf("expected streamContent 'abcde' after third chunk, got %q", m.logEntries[idx].streamContent)
	}

	// End the stream — 内容不变
	m = feedEvent(m, buildEvent("ai_stream_end", "agent", nil))
	if len(m.aiStreamCompletedEntries) != 1 {
		t.Fatalf("expected 1 completed stream entry, got %d", len(m.aiStreamCompletedEntries))
	}
	if m.logEntries[idx].streamContent != "abcde" {
		t.Fatalf("expected streamContent 'abcde' after stream end, got %q", m.logEntries[idx].streamContent)
	}
}

// ── Realtime update at stream end: leftover content already rendered ──

func TestScenarioE_FlushLeftoverAtStreamEnd(t *testing.T) {
	m := newTestModel()

	// Start a stream
	m = feedEvent(m, buildEvent("ai_stream_start", "agent", ""))
	idx := 0

	// 实时更新：每个 chunk 到达后立即渲染
	m = feedEvent(m, buildEvent("ai_chunk", "agent", "ab"))
	if m.logEntries[idx].streamContent != "ab" {
		t.Fatalf("expected streamContent 'ab' after first chunk, got %q", m.logEntries[idx].streamContent)
	}

	m = feedEvent(m, buildEvent("ai_chunk", "agent", "c"))
	if m.logEntries[idx].streamContent != "abc" {
		t.Fatalf("expected streamContent 'abc' after second chunk, got %q", m.logEntries[idx].streamContent)
	}

	// End stream — 内容已实时更新，无需额外 flush
	m = feedEvent(m, buildEvent("ai_stream_end", "agent", nil))
	if m.logEntries[idx].streamContent != "abc" {
		t.Fatalf("expected streamContent 'abc' after stream end, got %q", m.logEntries[idx].streamContent)
	}
	if len(m.aiStreamCompletedEntries) != 1 {
		t.Fatalf("expected 1 completed stream entry, got %d", len(m.aiStreamCompletedEntries))
	}
}
