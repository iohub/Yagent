package tui

import (
	"log/slog"

	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// saveAndFlushTaskMemory saves the current task's memory to the pending buffer
// and immediately flushes it to disk. This is called on TUI exit to ensure
// no conversation history is lost, regardless of whether the task is still
// running or has already completed.
func (m *model) saveAndFlushTaskMemory() {
	if m.currentTask != nil && m.dataManager != nil {
		if err := m.dataManager.SaveTaskMemory(m.currentTask.ID, m.currentTask.Memory); err != nil {
			slog.Error("Failed to save task memory on exit",
				"taskID", m.currentTask.ID,
				"error", err)
		}
		if err := m.dataManager.FlushTaskMemory(m.currentTask.ID); err != nil {
			slog.Error("Failed to flush task memory on exit",
				"taskID", m.currentTask.ID,
				"error", err)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// timelineFullscreenUpdate — 原 L2266–2486 原样移动
// ─────────────────────────────────────────────────────────────────────────────

// timelineFullscreenUpdate 处理全屏时间线模式下的所有消息。
func timelineFullscreenUpdate(msg tea.Msg, m *model) (*model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		switch key {
		// ── 退出全屏 → 回到 expanded ──
		case "esc":
			m.timelineExpanded = false
			m.timelineFullscreenMode = false
			m.timelineFullscreenCursor = 0
			m.timelineFullscreenFocus = "list"
			m.timelineDetailOffsets = nil
			m.timelineDetailVP = nil
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
			return m, nil

		// ── q 键退出全屏（与 esc 行为一致）──
		case "q":
			m.timelineExpanded = false
			m.timelineFullscreenMode = false
			m.timelineFullscreenCursor = 0
			m.timelineFullscreenFocus = "list"
			m.timelineDetailOffsets = nil
			m.timelineDetailVP = nil
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
			return m, nil

		// ── ctrl+c: 退出全屏 → 显示退出确认 ──
		case "ctrl+c":
			m.timelineExpanded = false
			m.timelineFullscreenMode = false
			m.timelineFullscreenCursor = 0
			m.timelineFullscreenFocus = "list"
			m.timelineDetailOffsets = nil
			m.timelineDetailVP = nil
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
			d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
			return m, nil

		// ── 焦点切换 ──
		case "h":
			// 将焦点移到左栏（列表）
			if m.timelineFullscreenFocus == "detail" {
				m.timelineFullscreenFocus = "list"
			}
			// 如果已在左栏，h 无操作（vim 风格）
			return m, nil

		case "l":
			// 将焦点移到右栏（详情）
			if m.timelineFullscreenFocus == "list" {
				m.timelineFullscreenFocus = "detail"
			}
			// 如果已在右栏，l 无操作
			return m, nil

		// ── 列表导航 / 详情滚动（根据焦点决定）──
		case "j", "down":
			if m.timelineFullscreenFocus == "list" {
				moveTimelineCursor(m, 1)
			} else {
				// 焦点在详情：滚动详情 viewport
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.ScrollDown(1)
				}
			}
			return m, nil

		case "k", "up":
			if m.timelineFullscreenFocus == "list" {
				moveTimelineCursor(m, -1)
			} else {
				// 焦点在详情：滚动详情 viewport
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.ScrollUp(1)
				}
			}
			return m, nil

		// ── 跳转首/尾（根据焦点决定）──
		case "g":
			if m.timelineFullscreenFocus == "list" {
				if len(m.timelineEntries) > 0 {
					m.timelineFullscreenCursor = 0
					syncDetailToCursor(m)
				}
			} else {
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.GotoTop()
				}
			}
			return m, nil

		case "G":
			if m.timelineFullscreenFocus == "list" {
				if len(m.timelineEntries) > 0 {
					m.timelineFullscreenCursor = len(m.timelineEntries) - 1
					syncDetailToCursor(m)
				}
			} else {
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.GotoBottom()
				}
			}
			return m, nil

		// ── f: 下翻页（基于焦点）──
		case "f":
			if m.timelineFullscreenFocus == "list" {
				// 列表：光标下移一页（页大小 = 内容高度）
				pageSize := m.termHeight - 4 // 同 renderTimelineFullscreenView 中的 contentHeight
				if pageSize < 1 {
					pageSize = 1
				}
				moveTimelineCursor(m, pageSize)
			} else {
				// 详情：viewport 下翻一页
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.PageDown()
				}
			}
			return m, nil

		// ── b: 上翻页（基于焦点）──
		case "b":
			if m.timelineFullscreenFocus == "list" {
				// 列表：光标上移一页
				pageSize := m.termHeight - 4
				if pageSize < 1 {
					pageSize = 1
				}
				moveTimelineCursor(m, -pageSize)
			} else {
				// 详情：viewport 上翻一页
				if m.timelineDetailVP != nil {
					m.timelineDetailVP.PageUp()
				}
			}
			return m, nil

		// ── 详情 viewport 滚动（始终可用，不受焦点影响）──
		case "pageup", "ctrl+u":
			if m.timelineDetailVP != nil {
				m.timelineDetailVP.ScrollUp(5)
			}
			return m, nil

		case "pagedown", " ":
			if m.timelineDetailVP != nil {
				m.timelineDetailVP.ScrollDown(5)
			}
			return m, nil

		// ── 详情 viewport 滚动（单行，始终可用）──
		case "ctrl+y":
			if m.timelineDetailVP != nil {
				m.timelineDetailVP.ScrollUp(1)
			}
			return m, nil

		case "ctrl+e":
			if m.timelineDetailVP != nil {
				m.timelineDetailVP.ScrollDown(1)
			}
			return m, nil

		default:
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		// 如果 viewport 已初始化，更新尺寸并刷新内容
		if m.timelineDetailVP != nil {
			leftWidth := int(float64(m.termWidth-3) * 0.35)
			if leftWidth < 25 {
				leftWidth = 25
			}
			rightWidth := m.termWidth - 3 - leftWidth - 4 // viewport content width
			if rightWidth < 30 {
				rightWidth = 30
			}
			contentHeight := m.termHeight - 4 - 2 // viewport height inside border
			if contentHeight < 3 {
				contentHeight = 3
			}
			m.timelineDetailVP.SetWidth(rightWidth)
			m.timelineDetailVP.SetHeight(contentHeight)
			buildAllTimelineDetails(m)
			syncDetailToCursor(m)
		}
		return m, nil

	case tickMsg:
		// 全屏模式下也处理动画 tick，并保持 tick 循环
		if m.anim != nil {
			m.anim.Tick()
		}
		if m.taskRunning {
			return m, tickCmd()
		}
		return m, nil

	case taskEventMsg:
		// 全屏模式下保持事件监听链存活，但事件暂不处理
		// 退出全屏后事件会被正常消费
		if m.taskRunning {
			return m, listenForEvents(m.eventCh)
		}
		return m, nil

	default:
		return m, nil
	}
}
