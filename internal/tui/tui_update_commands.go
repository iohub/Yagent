package tui

import (
	"fmt"
	"strings"
	"time"

	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

func (m *model) processCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)

	switch {
	case cmd == ":q!":
		// Force quit — skip confirmation (vim convention)
		m.saveAndFlushTaskMemory()
		m.quitting = true
		m.cleanupDebounceTimer()
		return tea.Quit
	case cmd == ":q" || cmd == ":quit":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	case cmd == ":help" || cmd == ":h":
		top := m.dialogStack.Top()
		if top != nil && top.ID() == "help_dialog" {
			m.dialogStack.CloseDialog("help_dialog")
		} else {
			d := components.NewHelpDialog(components.Language(m.currentLang))
			if m.com != nil && m.com.Config != nil {
				overrides := buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings)
				if len(overrides) > 0 {
					d.SetAltKeybindings(overrides)
				}
			}
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
		}
	case cmd == ":mode":
		mode := "COMMAND"
		if !m.commandMode {
			mode = "EDIT"
		}
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   fmt.Sprintf("Current mode: %s | Task running: %v | Buffer: %q", mode, m.taskRunning, m.commandBuffer),
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	// ═══════════════════════════════════════════════════════════════
	// :language — 切换语言（原 ctrl+l 快捷键的功能迁移至此）
	// ═══════════════════════════════════════════════════════════════
	case cmd == ":language":
		m.toggleLanguage()
		return nil

	// ═══════════════════════════════════════════════════════════════
	// :route — 打开模型自动路由对话框（切换 auto/fixed，查看三档模型与分数）
	// ═══════════════════════════════════════════════════════════════
	case cmd == ":route" || cmd == ":routing":
		return m.showRoutingDialog()

	// ═══════════════════════════════════════════════════════════════
	// /resume — Resume a session from rollout log
	// (alias /rs; must come BEFORE the /pattern log-search fallback)
	// ═══════════════════════════════════════════════════════════════
	case cmd == "/resume" || cmd == "/rs" || cmd == ":resume" || cmd == ":rs":
		if m.taskRunning {
			m.infoMsg = "Cannot resume sessions while a task is running"
			return nil
		}
		if !m.commandMode {
			// Switch to command mode first since resume is accessed from there
			m.commandMode = true
			m.commandBuffer = ""
		}
		return enterResumeMode(m)

	// ═══════════════════════════════════════════════════════════════
	// /pattern — Search in log entries (must come AFTER more specific / commands)
	// ═══════════════════════════════════════════════════════════════
	case strings.HasPrefix(cmd, "/"):
		// Search in log entries
		query := strings.TrimPrefix(cmd, "/")
		m.searchInLog(query)
	case cmd == ":hist" || cmd == ":history":
		if m.taskRunning {
			m.infoMsg = "Cannot browse history while a task is running"
			return nil
		}
		if !m.commandMode {
			// Switch to command mode first since history is accessed from there
			m.commandMode = true
			m.commandBuffer = ""
		}
		return enterHistoryMode(m)
	default:
		m.infoMsg = fmt.Sprintf("Unknown command: %s (type :help or ? for available commands)", cmd)
	}
	return nil
}

// showModelSelectionDialog opens an interactive agent → provider selection dialog
func (m *model) showModelSelectionDialog() tea.Cmd {
	// Block switching while a task is running
	if m.taskRunning {
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   "Cannot switch model while a task is running. Wait for the task to complete first.",
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return nil
	}

	validAgents := []string{"director", "coding", "repo", "chat", "meta", "devops", "browser"}
	client := m.assistant.GetClient()
	// 每个目标当前生效 provider 的上下文窗口，与模型选择器同一套解析（配置优先，目录兜底）
	ctxWindow := func(prov string) int {
		if client == nil {
			return 0
		}
		return contextWindowFromCfg(client.Config, prov)
	}

	entries := make([]components.ConfigEntry, 0, len(validAgents)+2)
	// Global entry
	entries = append(entries, components.ConfigEntry{
		Target:   "global",
		Group:    components.TargetGroupGlobal,
		Provider: m.currentProvider,
		Model:    m.currentModel,
		Context:  ctxWindow(m.currentProvider),
	})
	// Agent entries
	for _, agent := range validAgents {
		prov, modelName := m.assistant.GetAgentProvider(agent)
		entries = append(entries, components.ConfigEntry{
			Target:   agent,
			Group:    components.TargetGroupAgents,
			Provider: prov,
			Model:    modelName,
			Context:  ctxWindow(prov),
		})
	}
	// Tool entries (per-tool provider overrides)
	if client != nil {
		for _, tool := range []string{"deepthinking", "design"} {
			prov, modelName := m.assistant.GetToolProviderInfo(tool)
			entries = append(entries, components.ConfigEntry{
				Target:   tool,
				Group:    components.TargetGroupTools,
				Provider: prov,
				Model:    modelName,
				Context:  ctxWindow(prov),
			})
		}
	}
	dialog := components.NewAgentSelectDialog(entries)
	dialog.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(dialog)
	return nil
}

// showRoutingDialog 打开模型自动路由对话框：切换 auto/fixed 模式，并展示 auto
// 模式下三档模型（高智商/性价比/快速）及其 ai_score；路由不可用时展示原因。
func (m *model) showRoutingDialog() tea.Cmd {
	if m.assistant == nil {
		m.infoMsg = "Routing unavailable: assistant not initialized"
		return nil
	}
	snap := m.assistant.GetRouteSnapshot()
	info := components.RoutingInfo{
		Enabled: snap.Enabled,
		Mode:    snap.Mode,
		Reason:  snap.Reason,
	}
	// 按 高智商 → 性价比 → 快速 的展示顺序取档位。
	for _, want := range []string{"high", "balanced", "fast"} {
		for _, t := range snap.Tiers {
			if t.Tier.String() == want {
				info.Tiers = append(info.Tiers, components.RoutingTierView{
					Label:    strings.ToUpper(want),
					Provider: t.Provider,
					Model:    t.Model,
					Score:    t.Score,
				})
			}
		}
	}
	d := components.NewRoutingDialog(info)
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// searchInLog highlights entries containing the query string.
func (m *model) searchInLog(query string) {
	queryLower := strings.ToLower(query)
	found := 0
	for i := range m.logEntries {
		if strings.Contains(strings.ToLower(m.logEntries[i].content), queryLower) {
			found++
		}
	}
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Search '/%s': %d matches", query, found),
	})
	m.viewportDirty = true
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
}
