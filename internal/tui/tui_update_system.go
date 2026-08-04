package tui

import (
	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// handleTickMsg — 原 Update case tickMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleTickMsg(msg tickMsg) (tea.Model, tea.Cmd) {
	m.animFrame++

	if m.animManager != nil {
		m.animManager.Tick(100)
	}

	// Stop tick early when idle: avoids unnecessary View() calls that cause flicker
	if !m.activeAnim && !m.taskRunning && !m.viewportDirty {
		m.tickStarted = false
		return m, nil
	}

	animChanged := m.anim.Tick()

	if m.activeAnim || m.taskRunning {
		// Only rebuild status bar cache when animation actually changed
		// or when taskRunning first becomes true (status bar shows RUN mode)
		if animChanged || !m.statusBarValid {
			m.cachedStatusBar = m.renderAirlineStatusBar()
			m.statusBarValid = true
		}

		// Only update viewport content if animation changed and there are
		// visible animated entries. Skip the expensive re-render + SetContent
		// when the progress bar position didn't move.
		if m.activeAnim && animChanged {
			visStart, visEnd := m.visibleEntryIndices()
			hasVisibleAnim := false

			// Invalidate cache only for visible running entries
			for i := visStart; i <= visEnd && i < len(m.logEntries); i++ {
				entry := &m.logEntries[i]
				if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
					entry.clearRenderCache()
					entry.toolEntry.InvalidateCache()
					hasVisibleAnim = true
				} else if entry.eventType == "llm_call_start" && entry.isToolRunning {
					entry.clearRenderCache()
					hasVisibleAnim = true
				}
			}

			if hasVisibleAnim {
				// Re-render only the visible animated entries
				width := m.viewport.Width()
				for i := visStart; i <= visEnd && i < len(m.logEntries); i++ {
					entry := &m.logEntries[i]
					if (entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning) ||
						(entry.eventType == "llm_call_start" && entry.isToolRunning) {
						m.setEntryContent(i, m.renderSingleEntry(entry, width))
					}
				}
				m.assembleViewportContent()
				m.viewport.SetContent(m.contentCache.String())
				if m.viewport.AtBottom() {
					m.viewport.GotoBottom()
				}
			}
		}
	}
	// 修复：任何状态下都处理 viewportDirty（不再依赖 activeAnim/taskRunning），
	// 确保任务结束（taskRunning=false）后最后一次内容更新也能触发 rebuildViewportScrollLock
	// 自动滚动到底部，避免最后一条 agent 消息尾部在视口之外（渲染不全）。
	if m.viewportDirty && !m.activeAnim {
		m.rebuildViewportScrollLock()
		m.viewportDirty = false
	}

	return m, tickCmd()
}

// ─────────────────────────────────────────────────────────────────────────────
// handleWindowSizeMsg — 原 Update case tea.WindowSizeMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleWindowSizeMsg(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.termWidth = msg.Width
	m.termHeight = msg.Height
	m.input.SetWidth(m.computeFieldWidth())
	m.resizeViewport()
	m.invalidateRenderedCache()
	m.invalidateFooterCache()
	m.viewportDirty = true
	m.rebuildViewportScrollLock()
	// 始终启动/重启 tick 循环（空闲时 tick 会自动停止）
	if m.layoutEngine != nil {
		m.layoutEngine.Resize(msg.Width, msg.Height)
	}

	// 更新所有 sessionTab 的 viewport 尺寸
	for _, tab := range m.sessionTabs {
		vpHeight := m.termHeight - m.computeFooterHeight() - tabBarHeight
		if vpHeight < 3 {
			vpHeight = 3
		}
		tab.viewport.SetWidth(m.termWidth - m.dashboardWidth())
		tab.viewport.SetHeight(vpHeight)
		tab.needFullRebuild = true
	}

	return m, tickCmd()
}

// ─────────────────────────────────────────────────────────────────────────────
// handlePublisherReadyMsg — 原 Update case publisherReadyMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handlePublisherReadyMsg(msg publisherReadyMsg) (tea.Model, tea.Cmd) {
	m.publisher = msg.publisher
	return m, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// handleMouseMsg — 原 Update case tea.MouseMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleMouseMsg(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mouseHandler != nil {
		action, x, y := m.mouseHandler.Detect(msg)
		switch action {
		case components.MouseScrollUp:
			// 滚动向上（viewport 减少行）
			m.viewport.ScrollUp(3)
			m.updateViewportCache()
		case components.MouseScrollDown:
			// 滚动向下（viewport 增加行）
			m.viewport.ScrollDown(3)
			m.updateViewportCache()
		case components.MouseClick:
			// 单击：检测是否点击了弹窗按钮
			m.handleMouseClick(x, y)
		case components.MouseDoubleClick:
			// 双击：选词（暂时占位）
			_ = x
			_ = y
		case components.MouseTripleClick:
			// 三击：选行（暂时占位）
			_ = x
			_ = y
		case components.MouseDragStart:
			// 拖拽开始（暂时占位）
		case components.MouseDragMove:
			// 拖拽移动（暂时占位）
		case components.MouseDragEnd:
			// 拖拽结束（暂时占位）
		}
	}
	return m, nil
}

// handleMouseClick 处理鼠标点击事件
func (m *model) handleMouseClick(x, y int) {
	// 检查是否点击了弹窗中的按钮
	// 简化实现：记录点击位置，等待后续扩展
	_ = x
	_ = y
}
