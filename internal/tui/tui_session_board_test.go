package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
)

// tabModel 造一个能真正走 tab 路径的 model。两处补齐都是必需的：
//   - textarea：restoreSessionTab 会 m.input.SetValue(...)，零值 textarea 的内部 viewport 是 nil；
//   - sessionTabs：clearCurrentSession 要写回 sessionTabs[activeSessionIdx]，
//     生产里 initTabs 恒建一个默认 tab，裸 model 没有就 index out of range。
func tabModel(t *testing.T) *model {
	t.Helper()
	// langManager 只在生产入口 StartTUI 里初始化；clearCurrentSession → generateTabTitle
	// → langManager.GetText 会在 nil 接收器上 panic，所以测试自己补这一步。
	langManager = NewLanguageManager()

	m := newTestModel()
	input := textarea.New()
	input.SetWidth(80)
	input.SetHeight(3)
	input.ShowLineNumbers = false
	input.SetVirtualCursor(true)
	m.input = input

	tab := m.newSessionTab(m.nextTabID, "default")
	m.nextTabID++
	m.sessionTabs = []*sessionTab{tab}
	m.activeSessionIdx = 0
	m.sessionID = tab.sessionID
	return m
}

// 便签板作用域 = tab：新建 tab 必须换新板，切 tab 必须把板跟着 tab 带走。
// 若 newSessionTab 继承了 m.sessionID，两个 tab 会共用一块板，
// 于是一个不相关话题能看见另一个话题的结论（docs/blackboard-handoff-design.md §12.8 明确禁止）。
func TestSessionIDIsPerTabAndFollowsTabSwitch(t *testing.T) {
	m := tabModel(t)

	first := m.newSessionTab(1, "first")
	second := m.newSessionTab(2, "second")
	if first.sessionID == "" || second.sessionID == "" {
		t.Fatal("newSessionTab must assign a sessionID")
	}
	if first.sessionID == second.sessionID {
		t.Errorf("two tabs share board %q — 跨话题的旧结论会串进索引", first.sessionID)
	}

	m.sessionTabs = []*sessionTab{first, second}
	m.activeSessionIdx = 1
	m.sessionID = second.sessionID

	m.switchSessionTab(0)
	if m.sessionID != first.sessionID {
		t.Errorf("after switching to tab 0: m.sessionID = %q, want %q", m.sessionID, first.sessionID)
	}
	m.switchSessionTab(1)
	if m.sessionID != second.sessionID {
		t.Errorf("after switching to tab 1: m.sessionID = %q, want %q", m.sessionID, second.sessionID)
	}
	// 切回去不能丢：板必须真的存在 tab 上，而不是只活在 model 里。
	m.switchSessionTab(0)
	if m.sessionID != first.sessionID {
		t.Errorf("switching back lost the board: %q, want %q", m.sessionID, first.sessionID)
	}
}

// 清空会话意味着换话题：旧会话的结论不该出现在新话题的索引里。
func TestClearCurrentSessionRotatesBoard(t *testing.T) {
	m := tabModel(t)
	m.ensureSessionID()
	before := m.sessionID
	if before == "" {
		t.Fatal("ensureSessionID produced an empty id")
	}
	m.ensureSessionID()
	if m.sessionID != before {
		t.Errorf("ensureSessionID is not idempotent: %q → %q", before, m.sessionID)
	}

	m.clearCurrentSession()
	if m.sessionID == "" {
		t.Error("clearCurrentSession left sessionID empty")
	}
	if m.sessionID == before {
		t.Error("clearCurrentSession must rotate to a fresh board")
	}
}

// /resume 与 history 加载的板接管规则。
func TestAdoptRestoredBoard(t *testing.T) {
	m := newTestModel()

	m.adoptRestoredBoard("board-from-rollout", "task-uuid")
	if m.sessionID != "board-from-rollout" {
		t.Errorf("sessionID = %q, want the recorded board_id", m.sessionID)
	}

	// 存量 rollout 没有 board_id：退回 taskID，正是 board 作用域升级前的取值规则。
	m.adoptRestoredBoard("", "task-uuid")
	if m.sessionID != "task-uuid" {
		t.Errorf("sessionID = %q, want the taskID fallback", m.sessionID)
	}
}
