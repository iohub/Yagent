package tui

import (
	"fmt"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// processCommand 执行 ":" 命令行内容，或驱动 "/" 搜索。
// 参数形如 ":q" / "/pattern"（含前缀）；无前缀的裸文本按命令处理，
// 这样 ":w" 与 "w" 都能走到同一套解析。
func (m *model) processCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil
	}

	// ── "/" 搜索：真高亮 + 跳转，n/N 在命中之间循环（见 tui_search.go）──
	if strings.HasPrefix(cmd, "/") {
		if cmd == "/resume" || cmd == "/rs" {
			return m.resumeFromCmdline()
		}
		m.runSearch(strings.TrimPrefix(cmd, "/"))
		return nil
	}

	// 命令行里 ":" 可以省略（"q" 等价 ":q"）
	name := strings.TrimPrefix(cmd, ":")

	switch name {
	case "q", "quit":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)

	case "q!", "quit!":
		// 强制退出，跳过确认（vim 约定）
		m.saveAndFlushTaskMemory()
		m.quitting = true
		m.cleanupDebounceTimer()
		return tea.Quit

	case "h", "help":
		return m.toggleHelpDialog()

	case "mode":
		mode := "NORMAL"
		if !m.commandMode {
			mode = "INSERT"
		}
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   fmt.Sprintf("Current mode: %s | Task running: %v | Buffer: %q", mode, m.taskRunning, m.commandBuffer),
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	case "language":
		m.toggleLanguage()

	case "route", "routing":
		return m.showRoutingDialog()

	case "model", "m":
		return m.showModelSelectionDialog()

	case "timeout":
		return m.showTimeoutDialog()

	case "clear":
		m.clearCurrentSession()

	case "nohl", "noh":
		m.clearSearch()

	case "hist", "history":
		return m.historyFromCmdline()

	case "resume", "rs":
		return m.resumeFromCmdline()

	default:
		m.setInfoMsg(fmt.Sprintf("Unknown command: %s（:help 或 ? 查看全部命令）", name))
	}
	return nil
}

// historyFromCmdline 进入历史视图；任务运行中拒绝。
func (m *model) historyFromCmdline() tea.Cmd {
	if m.taskRunning {
		m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
		return nil
	}
	if !m.commandMode {
		m.enterNormalMode()
	}
	return enterHistoryMode(m)
}

// resumeFromCmdline 进入恢复视图；任务运行中拒绝。
func (m *model) resumeFromCmdline() tea.Cmd {
	if m.taskRunning {
		m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
		return nil
	}
	if !m.commandMode {
		m.enterNormalMode()
	}
	return enterResumeMode(m)
}

// timeoutTarget 是模型设置面板中"LLM 调用超时"行的哨兵 Target 值；选中后打开
// 超时对话框而非 provider 选择器（见 tui_update_keys.go 的 AgentSelectDialog 分支）。
const timeoutTarget = "timeout"

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
	// Settings entry: LLM call timeout. Selecting it opens the timeout dialog
	// (see handleDialogStackKey) instead of the provider picker. The current
	// effective value is shown in the model column.
	entries = append(entries, components.ConfigEntry{
		Target: timeoutTarget,
		Group:  components.TargetGroupSettings,
		Model:  config.FormatLLMTimeout(m.llmEffectiveTimeout()),
	})
	dialog := components.NewAgentSelectDialog(entries)
	dialog.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(dialog)
	return nil
}

// showRoutingDialog 打开模型自动路由对话框：切换 auto/fixed 模式，并展示 auto
// 模式下三档模型（高智商/性价比/快速）及其 ai_score；路由不可用时展示原因。
func (m *model) showRoutingDialog() tea.Cmd {
	if m.assistant == nil {
		m.setInfoMsg("Routing unavailable: assistant not initialized")
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

// llmTimeoutPresets 是超时对话框的预设选项（全部在 30s ~ 24h 校验范围内）。
var llmTimeoutPresets = []components.TimeoutOption{
	{Value: "1m", Desc: "Fast"},
	{Value: "2m", Desc: "Short"},
	{Value: "5m", Desc: "Conservative"},
	{Value: "10m", Desc: "Default"},
	{Value: "15m", Desc: "Extended"},
	{Value: "30m", Desc: "Long"},
	{Value: "1h", Desc: "Very long"},
	{Value: "2h", Desc: "Maximum"},
}

// llmEffectiveTimeout 返回当前生效的 LLM 调用超时：
// 全局运行时值 → 内存配置（llm.Client）→ 程序默认值 DefaultLLMCallTimeout。
func (m *model) llmEffectiveTimeout() time.Duration {
	current := config.GlobalLLMTimeout()
	if current <= 0 && m.assistant != nil {
		if client := m.assistant.GetClient(); client != nil && client.Config != nil {
			current = client.Config.LLM.Timeout
		}
	}
	if current <= 0 {
		current = config.DefaultLLMCallTimeout
	}
	return current
}

// showTimeoutDialog 打开 LLM 调用超时对话框：顶部展示当前生效值，列表提供
// 预设选项 —— Enter 临时应用（仅运行时），Space 应用并持久化，Esc 取消。
func (m *model) showTimeoutDialog() tea.Cmd {
	if m.assistant == nil {
		m.setInfoMsg("Timeout settings unavailable: assistant not initialized")
		return nil
	}
	current := m.llmEffectiveTimeout()
	currentLabel := config.FormatLLMTimeout(current)

	// 游标定位：当前值命中预设时直接定位（按 duration 值而非字符串比较，
	// 保证 CurrentRaw 与选项 Value 一致）；非预设值则头部插入 "Current" 项。
	options := make([]components.TimeoutOption, 0, len(llmTimeoutPresets)+1)
	currentRaw := ""
	for _, o := range llmTimeoutPresets {
		if currentRaw == "" {
			if pd, err := time.ParseDuration(o.Value); err == nil && pd == current {
				currentRaw = o.Value
			}
		}
		options = append(options, o)
	}
	if currentRaw == "" {
		options = append([]components.TimeoutOption{{Value: currentLabel, Desc: "Current"}}, options...)
		currentRaw = currentLabel
	}

	d := components.NewLLMTimeoutDialog(components.TimeoutInfo{
		CurrentLabel: currentLabel,
		CurrentRaw:   currentRaw,
		Options:      options,
	})
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// applyLLMTimeout 将超时应用到运行时并按需持久化，结果写入日志。按三级动作执行：
// 运行时全局值（atomic，引擎/执行层立即读取）→ 内存配置同步（llm.Client 以 mu
// 保护）→ persist 为 true 时持久化写入 config.toml 的 [llm].timeout 键。
// 由超时对话框调用：Enter=仅运行时、Space=应用并持久化。
func (m *model) applyLLMTimeout(d time.Duration, persist bool) {
	logStatus := func(kind, content string) {
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: kind,
			content:   content,
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
	}
	var client *llm.Client
	if m.assistant != nil {
		client = m.assistant.GetClient()
	}

	// 1) 运行时生效：全局值（唯一写入口，atomic 保证并发安全）+ 内存配置同步
	if client != nil {
		if err := client.SetLLMTimeout(d); err != nil {
			// 兜底：内存配置更新失败时仍保证全局运行时值生效
			config.SetGlobalLLMTimeout(d)
			logStatus("error", fmt.Sprintf("Applied timeout %s to runtime globals only: %v", config.FormatLLMTimeout(d), err))
			return
		}
	} else {
		config.SetGlobalLLMTimeout(d)
	}

	// 2) persist=false：仅运行时生效（如对话框 Enter），不写 config.toml
	if !persist {
		logStatus("status", fmt.Sprintf("LLM call timeout set to %s (runtime only)", config.FormatLLMTimeout(d)))
		return
	}

	// 3) 持久化写入 config.toml 的 [llm].timeout
	if client != nil {
		if err := client.PersistLLMTimeout(d); err != nil {
			logStatus("status", fmt.Sprintf("LLM call timeout set to %s (runtime only — not persisted: %v)", config.FormatLLMTimeout(d), err))
			return
		}
		logStatus("status", fmt.Sprintf("LLM call timeout set to %s (persisted to config.toml)", config.FormatLLMTimeout(d)))
		return
	}
	logStatus("status", fmt.Sprintf("LLM call timeout set to %s (runtime only — LLM client not available)", config.FormatLLMTimeout(d)))
}
