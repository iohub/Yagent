// 在主 TUI 里嵌入引导向导（/setup）。
//
// 为什么运行中不进向导：向导独占键盘，而任务事件必须有消费者 —— listenForEvents 是一次性
// cmd 且没有重放队列（AGENTS.md §11 坑 6），两边同时接管键盘的后果就是"界面卡在运行中"那类老 bug。
// 空闲时进入则不存在这条链路，代价是写完配置要重启才对运行中的进程生效（摘要屏会说明）。
package tui

import (
	"fmt"

	"yagent/internal/config"

	tea "charm.land/bubbletea/v2"
)

// setupConfigPath 取当前进程用的 config.toml 路径。它只在 llm.Client 上，
// 那是运行期唯一的配置文件事实来源（main.go 赋值）。
func (m *model) setupConfigPath() string {
	if client := m.assistant.GetClient(); client != nil && client.ConfigPath != "" {
		return client.ConfigPath
	}
	return config.ConfigDir() + "/config.toml"
}

// enterSetupMode 打开向导全屏模式。返回 nil cmd 表示被拒绝并已给出原因。
func (m *model) enterSetupMode() tea.Cmd {
	if m.taskRunning {
		m.setInfoMsg("setup is unavailable while a task is running — wait for it or cancel first")
		return nil
	}
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		m.setInfoMsg("close the open dialog first (esc), then /setup")
		return nil
	}

	path := m.setupConfigPath()
	reason, detail := config.NeedsSetup(m.com.Config, path, false, nil)
	w := newSetupWizard(path, reason, detail)
	w.width, w.height = m.termWidth, m.termHeight

	// 与其它全屏模式互斥：叠两层键盘所有权会有一份 Update 永远看不到按键。
	m.historyMode = false
	m.timelineFullscreenMode = false
	m.thinklinkMode = false
	m.resumeMode = false
	m.commandMode = false
	m.commandBuffer = ""

	m.wizard = w
	m.setupActive = true
	return w.Init()
}

// updateSetupMode 是向导期间的消息分派。
// 任务消息继续交给原 handler：taskCompleteMsg 是 taskRunning 唯一的清零点，
// 任何模式吞掉它都会把界面锁死在"运行中"。
func (m *model) updateSetupMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case taskEventMsg:
		return m.handleTaskEventMsg(typed)
	case taskCompleteMsg:
		return m.handleTaskCompleteMsg(typed)
	}

	if m.wizard == nil {
		m.setupActive = false
		return m, listenForEvents(m.eventCh)
	}

	_, cmd := m.wizard.Update(msg)

	if m.wizard.Done() {
		saved := m.wizard.Saved()
		path := m.wizard.path
		m.setupActive = false
		m.wizard = nil
		switch {
		case saved:
			m.setInfoMsg(fmt.Sprintf(
				"setup wrote %s — restart yagent to load the new providers (the running process keeps its current engine)", path))
		default:
			m.setInfoMsg("setup exited without writing — config.toml unchanged")
		}
		return m, tea.Batch(cmd, listenForEvents(m.eventCh))
	}
	return m, cmd
}

// setupView 渲染向导（全屏，优先级高于 timeline/thinklink 与对话框栈）。
func (m *model) setupView() tea.View {
	return m.wizard.View()
}
