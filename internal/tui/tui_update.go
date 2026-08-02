package tui

import (
	"strings"

	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global popup guard: when any overlay is shown, only allow KeyMsg through.
	// taskEventMsg is allowed through so the listenForEvents chain stays alive;
	// its handler drops the event when dialogs are open but reschedules the chain.
	// tickMsg is intercepted here and the tick is re-scheduled (not processed)
	// so animations keep running without touching the hidden viewport.
	// Other non-KeyMsg events (WindowSizeMsg, MouseMsg, etc.) also keep both
	// chains alive when the task is running, to prevent permanent TUI freeze.
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		if _, ok := msg.(tea.KeyMsg); !ok {
			switch msg.(type) {
			case taskEventMsg:
				// Let through — the handler's defensive check keeps chains alive
			case tickMsg:
				// Keep ticking for animation, but don't touch the viewport
				if m.taskRunning {
					return m, tickCmd()
				}
				return m, nil
			default:
				// WindowSizeMsg, MouseMsg, publisherReadyMsg, etc.
				if m.taskRunning {
					return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
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
		return timelineFullscreenUpdate(msg, m)
	}

	// History mode: intercept all messages and delegate to history handler.
	// Skip when a dialog is active so dialog confirmations (e.g. delete_history_confirm)
	// can be processed by the DialogStack key handler below.
	if m.historyMode && (m.dialogStack == nil || m.dialogStack.Len() == 0) {
		return historyUpdate(msg, m)
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
