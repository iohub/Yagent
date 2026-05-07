package tui

import (
	"fmt"
	"os"
	"strings"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/pkg/messaging"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	if len([]rune(taskDesc)) < 4 {
		return false, langManager.GetText("ValidationErrorShortTaskDesc")
	}
	return true, ""
}

// StartTUI starts the Bubble Tea TUI with the given dependencies.
func StartTUI(taskFilePath string, ca *app.CodingAssistant, tm *http.TaskManager, dm *datamanager.DataManager) {
	langManager = NewLanguageManager()

	taskContent := ""
	if taskFilePath != "" {
		if data, err := os.ReadFile(taskFilePath); err == nil {
			taskContent = string(data)
		} else {
			fmt.Printf("无法读取任务文件: %v\n", err)
		}
	}

	// Detect terminal background before entering raw mode to avoid
	// escape-sequence leakage into the input field.
	useDarkStyle := lipgloss.HasDarkBackground()

	p := tea.NewProgram(initialModel(taskContent, ca, tm, dm, useDarkStyle))
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
func (m model) computeFieldWidth() int {
	const minField = 38
	const maxField = 90
	if m.termWidth <= 0 {
		return 60
	}
	avail := m.termWidth - 8
	if avail < minField {
		return minField
	}
	if avail > maxField {
		return maxField
	}
	return avail
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
