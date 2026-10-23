package tui

import (
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// tick 链唯一性守卫
//
// 不变量：任一时刻，一个 model 上最多只应有一条活跃的 tick 链。否则每 100ms
// 会收到 N 个 tickMsg，动画按"被调用次数"而非真实时间推进（表现为"忽快"）
// 并浪费大量 CPU。
//   · 会再产生 tickMsg 的地方     → armTick()
//   · tickMsg 处理器决定不再续期处  → stopTick()
//   · 非 tick 消息想补链的地方      → ensureTick()
// ─────────────────────────────────────────────────────────────────────────────

// armTick 续期 tick 链：仅由处理 tickMsg 的分支调用，保证链继续。
func (m *model) armTick() tea.Cmd {
	m.tickStarted = true
	return tickCmd()
}

// stopTick 标记 tick 链已结束：处理 tickMsg 却不再续期时调用。
func (m *model) stopTick() {
	m.tickStarted = false
}

// ensureTick 幂等补链：非 tick 消息想让动画继续时调用；已有链在跑时返回 nil。
func (m *model) ensureTick() tea.Cmd {
	if m.tickStarted {
		return nil
	}
	return m.armTick()
}

// ─────────────────────────────────────────────────────────────────────────────
// handleTickMsg — 原 Update case tickMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleTickMsg(msg tickMsg) (tea.Model, tea.Cmd) {
	// 流式节流缓冲的超时检查只有 tick 能驱动（chunk 自己到得勤，流一停就没了），
	// 放在 idle 早退之前：落地后 viewportDirty 变 true，本轮自然会把尾巴渲染出来。
	m.flushStreamBuffers(aiChunkFlushInterval)

	// Stop tick early when idle: avoids unnecessary View() calls that cause flicker
	if !m.activeAnim && !m.taskRunning && !m.viewportDirty {
		m.stopTick()
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
				m.viewportViewValid = false
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

	return m, m.armTick()
}

// ─────────────────────────────────────────────────────────────────────────────
// handleWindowSizeMsg — 原 Update case tea.WindowSizeMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleWindowSizeMsg(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.termWidth = msg.Width
	m.termHeight = msg.Height
	// 先作废 footer 缓存再算视口尺寸：行高是从 footer 反推的，拿旧终端的缓存算
	// 会得到另一个行数，View 随即纠正宽度、整段正文白渲染一遍。
	m.invalidateFooterCache()
	m.input.SetWidth(m.computeFieldWidth())
	m.resizeViewport()
	m.invalidateRenderedCache()
	m.viewportDirty = true
	m.rebuildViewportScrollLock()
	// 始终启动/重启 tick 循环（空闲时 tick 会自动停止）
	if m.layoutEngine != nil {
		m.layoutEngine.Resize(msg.Width, msg.Height)
	}

	// 更新所有 sessionTab 的 viewport 尺寸（后台标签页没有自己的 dashboard 状态，
	// 只能按当前口径算；切过去时 View 会再校正一次宽度）
	for _, tab := range m.sessionTabs {
		l := m.computeBodyLayout(tab.viewport.TotalLineCount())
		tab.viewport.SetWidth(l.contentWidth)
		tab.viewport.SetHeight(l.height)
		tab.needFullRebuild = true
	}

	return m, m.ensureTick()
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
			m.invalidateFooterCache()
		case components.MouseScrollDown:
			// 滚动向下（viewport 增加行）
			m.viewport.ScrollDown(3)
			m.updateViewportCache()
			m.invalidateFooterCache()
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
