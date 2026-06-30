package tui

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"codeactor/internal/app"
	"codeactor/internal/config"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/messaging"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func listenForEvents(ch chan *messaging.MessageEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return taskEventMsg{event: event}
	}
}

func validateInputs(projectDir, taskDesc string) (bool, string) {
	if strings.TrimSpace(taskDesc) == "" {
		return false, langManager.GetText("ValidationErrorEmptyTaskDesc")
	}
	return true, ""
}

// StartTUI starts the Bubble Tea TUI with the given dependencies.
func StartTUI(taskFilePath string, ca *app.CodeActor, tm *http.TaskManager, dm *datamanager.DataManager, cfg *config.Config) {
	langManager = NewLanguageManager()

	taskContent := ""
	if taskFilePath != "" {
		if data, err := os.ReadFile(taskFilePath); err == nil {
			taskContent = string(data)
		} else {
			slog.Error("无法读取任务文件", "component", "tui-helpers", "error", err)
		}
	}

	// Detect terminal background before entering raw mode to avoid
	// escape-sequence leakage into the input field.
	useDarkStyle := lipgloss.HasDarkBackground(os.Stdin, os.Stdout)

	// 获取终端尺寸，消除启动闪烁 — 使用 syscall 避免外部依赖
	termWidth, termHeight := 80, 24 // fallback
	if ws, err := getWindowSize(int(os.Stdout.Fd())); err == nil {
		termWidth, termHeight = int(ws.Width), int(ws.Height)
	}

	p := tea.NewProgram(initialModel(taskContent, ca, tm, dm, useDarkStyle, cfg, termWidth, termHeight))
	if _, err := p.Run(); err != nil {
		slog.Error("TUI 运行出错", "component", "tui-helpers", "error", err)
		os.Exit(1)
	}
}
func (m *model) computeFieldWidth() int {
	const minField = 38
	const margin = 4 // small padding from terminal edges
	if m.termWidth <= 0 {
		return 80
	}
	avail := m.termWidth - margin
	if avail < minField {
		return minField
	}
	return avail
}

// computeInputHeight calculates the textarea height based on content lines
// and available terminal space. Height grows with content but is capped.
func (m *model) computeInputHeight() int {
	const minHeight = 3
	const maxHeight = 12

	// Count lines in current value (at least 1 for empty input)
	lines := strings.Count(m.input.Value(), "\n") + 1
	desired := lines + 1 // +1 line for comfortable editing headroom

	if desired < minHeight {
		desired = minHeight
	}

	// Cap to at most ~1/3 of terminal height so viewport remains usable
	if m.termHeight > 0 {
		termMax := (m.termHeight - 8) / 2 // 8 lines reserved for separator + status + token dashboard
		if termMax < minHeight {
			termMax = minHeight
		}
		if termMax > maxHeight {
			termMax = maxHeight
		}
		if desired > termMax {
			desired = termMax
		}
	} else {
		if desired > maxHeight {
			desired = maxHeight
		}
	}

	return desired
}

// getToolCallIDFromEventContent extracts tool_call_id from event content.
func getToolCallIDFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if id, ok := m["tool_call_id"]; ok {
			if idStr, ok := id.(string); ok {
				return idStr
			}
		}
	}
	return ""
}

// getToolNameFromEventContent extracts tool_name from event content.
func getToolNameFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if name, ok := m["tool_name"]; ok {
			if nameStr, ok := name.(string); ok {
				return nameStr
			}
		}
	}
	return ""
}

// findRunningEntryByName finds the most recently-added running entry with the
// given tool name in the toolCallEntries map. Returns the call ID and the entry.
func findRunningEntryByName(entries map[string]*ToolEntry, toolName string) (string, *ToolEntry) {
	for id, entry := range entries {
		if entry.Call.Name == toolName && entry.Status == ToolStatusRunning {
			return id, entry
		}
	}
	return "", nil
}

// getResultFromEventContent extracts the result string from event content.
func getResultFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if result, ok := m["result"]; ok {
			if resultStr, ok := result.(string); ok {
				return resultStr
			}
		}
	}
	return fmt.Sprintf("%v", content)
}

// findLogEntryByToolCallID finds the index of a log entry with the given tool_call_id.
func findLogEntryByToolCallID(entries []logEntry, callID string) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].toolCallID == callID {
			return i
		}
	}
	return -1
}

// updateActiveAnim checks if there are any running tool entries and updates the flag.
func (m *model) updateActiveAnim() {
	for _, te := range m.toolCallEntries {
		if te.Status == ToolStatusRunning {
			m.activeAnim = true
			return
		}
	}
	m.activeAnim = false
}

// winsize holds terminal window size information.
type winsize struct {
	Width, Height uint16
}

// getWindowSize retrieves the current terminal dimensions using TIOCGWINSZ ioctl.
// This is used before entering raw mode to initialize the viewport correctly
// and eliminate startup flash caused by hardcoded default dimensions.
func getWindowSize(fd int) (*winsize, error) {
	var ws winsize
	if _, _, err := syscall.Syscall6(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&ws)),
		0, 0, 0,
	); err != 0 {
		return nil, err
	}
	if ws.Width == 0 || ws.Height == 0 {
		return nil, fmt.Errorf("invalid terminal size")
	}
	return &ws, nil
}
