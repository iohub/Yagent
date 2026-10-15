package tui

import (
	"strings"

	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// keepListening 覆盖层模式（全屏 timeline / thinklink、历史、恢复选择器）消费掉一条
// 事件后补回监听：listenForEvents 是一次性 cmd，本轮不接替，通道里后续到达的事件就
// 再没人读，退出覆盖层后主界面永远停在旧内容上（少掉的几条只能等历史恢复）。
//
// 只补"事件被消费"这一种：按键、resize 都不动用那个 pending cmd，给它们补会让
// 监听者按按键次数线性堆积成一堆阻塞的 goroutine。
func (m *model) keepListening(cmd tea.Cmd, msg tea.Msg) tea.Cmd {
	if cmd != nil || !m.taskRunning {
		return cmd
	}
	if _, ok := msg.(taskEventMsg); ok {
		return listenForEvents(m.eventCh)
	}
	return cmd
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Ctrl+Z: suspend the program (SIGTSTP) and return to the shell.
	// Intercepted globally — before all mode guards (dialog/timeline/history) —
	// so suspension works from any UI state.
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+z" {
		return m, tea.Suspend
	}

	// Global popup guard: when any overlay is shown, only allow KeyMsg through.
	// taskEventMsg is allowed through so the listenForEvents chain stays alive;
	// its handler drops the event when dialogs are open but reschedules the chain.
	// tickMsg is intercepted here and the tick is re-scheduled (not processed)
	// so animations keep running without touching the hidden viewport.
	// Other non-KeyMsg events (WindowSizeMsg, MouseMsg, etc.) also keep both
	// chains alive when the task is running, to prevent permanent TUI freeze.
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		if _, ok := msg.(tea.KeyMsg); !ok {
			switch msg := msg.(type) {
			case taskEventMsg:
				// Let through — the handler's defensive check keeps chains alive
			case taskCompleteMsg:
				// taskRunning 唯一的清零点：弹窗期间吞掉它，界面就永久卡在"运行中"
				// （最终答案也不会落地）。与 timeline/history/thinklink 全屏同样透传给 handler。
				return m.handleTaskCompleteMsg(msg)
			case tickMsg:
				// Keep ticking for animation, but don't touch the viewport
				if m.taskRunning {
					return m, m.armTick()
				}
				m.stopTick()
				return m, nil
			case tea.PasteMsg:
				// 粘贴是独立消息类型（非 KeyMsg）：必须路由到栈顶对话框，
				// 否则对话框内 textarea（如 user-help 的 Custom input）无法粘贴。
				cmd, _ := m.dialogStack.Update(msg)
				if m.taskRunning {
					return m, tea.Batch(cmd, listenForEvents(m.eventCh), m.ensureTick())
				}
				return m, cmd
			default:
				// WindowSizeMsg, MouseMsg, publisherReadyMsg, etc.
				if m.taskRunning {
					return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
				}
				return m, nil
			}
		}
	}

	// ====== DialogStack: takes priority even over history mode ======
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		topDialog := m.dialogStack.Top()
		if topDialog != nil {
			// Skip KeyMsg — key handling is delegated to the type switch
			// in the fifth-level KeyMsg branch to avoid double Update calls.
			if _, isKeyMsg := msg.(tea.KeyMsg); !isKeyMsg {
				// taskEventMsg must pass through to the handler below to keep the
				// listenForEvents chain alive and allow new dialogs to be opened.
				if _, isTaskEvent := msg.(taskEventMsg); !isTaskEvent {
					newComp, cmd := topDialog.Update(msg)
					if newComp != nil {
						m.dialogStack.ReplaceTop(newComp.(components.Dialog))
					}
					if cmd != nil {
						// Always keep the event chain alive alongside dialog cmd
						return m, tea.Batch(cmd, listenForEvents(m.eventCh))
					}
				}
			}
		}
	}

	// 全屏 timeline 模式：拦截所有消息，委托给全屏处理器
	if m.timelineFullscreenMode {
		mod, cmd := timelineFullscreenUpdate(msg, m)
		return mod, m.keepListening(cmd, msg)
	}

	// Thinklink 全屏模式：拦截所有消息
	if m.thinklinkMode {
		mod, cmd := thinklinkFullscreenUpdate(msg, m)
		return mod, m.keepListening(cmd, msg)
	}

	// History mode: intercept all messages and delegate to history handler.
	// Skip when a dialog is active so dialog confirmations (e.g. delete_history_confirm)
	// can be processed by the DialogStack key handler below.
	if m.historyMode && (m.dialogStack == nil || m.dialogStack.Len() == 0) {
		mod, cmd := historyUpdate(msg, m)
		return mod, m.keepListening(cmd, msg)
	}

	// Resume mode: intercept all messages and delegate to resume handler.
	// Skip when a dialog is active (same guard as history mode above).
	if m.resumeMode && (m.dialogStack == nil || m.dialogStack.Len() == 0) {
		mod, cmd := resumeUpdate(msg, m)
		return mod, m.keepListening(cmd, msg)
	}

	switch msg := msg.(type) {
	case tickMsg:
		return m.handleTickMsg(msg)
	case tea.WindowSizeMsg:
		return m.handleWindowSizeMsg(msg)
	case publisherReadyMsg:
		return m.handlePublisherReadyMsg(msg)
	case tea.MouseMsg:
		return m.handleMouseMsg(msg)
	case tea.KeyMsg:
		return m.handleKeyMsg(msg)
	case tea.ResumeMsg:
		return m, nil
	case autocompleteMsg:
		return m.handleAutocompleteMsg(msg)
	case taskEventMsg:
		return m.handleTaskEventMsg(msg)
	case taskCompleteMsg:
		return m.handleTaskCompleteMsg(msg)
	}

	// Handle fzf file selection
	if msg, ok := msg.(fzfFileSelectedMsg); ok {
		if msg.path != "" {
			currentVal := m.input.Value()
			// Find the last @ and insert the file path after it
			lastAt := strings.LastIndex(currentVal, "@")
			if lastAt >= 0 {
				newVal := currentVal[:lastAt+1] + msg.path + " " + currentVal[lastAt+1:]
				m.input.SetValue(newVal)
			}
		}
		return m, nil
	}

	// Handle text input
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.invalidateFooterCache()
	return m, cmd
}
