package tui

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"yagent/internal/app"
	"yagent/internal/config"
	"yagent/internal/datamanager"
	"yagent/internal/http"
	"yagent/internal/messaging"

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
func StartTUI(taskFilePath string, ca *app.Yagent, tm *http.TaskManager, dm *datamanager.DataManager, cfg *config.Config) {
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
	finalModel, err := p.Run()
	if err != nil {
		slog.Error("TUI 运行出错", "component", "tui-helpers", "error", err)
		os.Exit(1)
	}
	// 退出前取消还在跑的任务：StartTUI 返回后 main 紧接着跑 FlushAll / yagent.Close，
	// 那时任务 ctx 还活着的话收尾是在跟一个仍在写的循环赛跑 —— 表现就是日志里那条
	// "退出时仍有 goroutine 在跑"，而它起的子进程组也没人杀（杀组只挂在 ctx 取消上，
	// 见 internal/tools/AGENTS.md 硬约束 17）。放在这一个出口而不是六个 tea.Quit 站点：
	// 漏一处的表现不是崩溃，只是每次都留下一个孤儿任务。
	if m, ok := finalModel.(*model); ok {
		m.cancelRunningTasksOnQuit()
	}
}

// cancelRunningTasksOnQuit 取消活动 tab 与所有后台 tab 上挂着的那次运行。
// 重复取消同一个 ctx 无害（context.CancelFunc 是幂等的），所以不先去重。
func (m *model) cancelRunningTasksOnQuit() {
	tasks := make([]*http.Task, 0, len(m.sessionTabs)+1)
	tasks = append(tasks, m.currentTask) // 活动 tab 的状态还没写回 tab 里
	for _, tab := range m.sessionTabs {
		tasks = append(tasks, tab.currentTask)
	}
	for _, task := range tasks {
		if task != nil && task.CancelFunc != nil {
			task.CancelFunc()
		}
	}
}
func (m *model) computeFieldWidth() int {
	// 下限必须容纳面板骨架（左右边框各 1 + 内边距各 1），否则 panelWidth()
	// 会算出比终端更宽的区块，把整屏挤到下一行。
	const minField = 16
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
	// Check running LLM call entries
	if len(m.llmCallActiveEntries) > 0 {
		m.activeAnim = true
		return
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
