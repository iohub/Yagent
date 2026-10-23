package tui

import (
	"fmt"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/http"
	"yagent/internal/llm"
	"yagent/internal/pause"
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
		// 暂停闸门。/resume 是"换一棵 ctx 重新起跑"，/pause 与 /continue 是
		// "同一棵 ctx 上停住再走" —— 名字相近但语义不同，所以各走各的分支。
		// 必须在这里显式列出：斜杠前缀默认是搜索，不列的话 /pause 变成搜 "pause"。
		if cmd == "/pause" {
			return m.pauseActiveTask()
		}
		if cmd == "/continue" || cmd == "/unpause" {
			return m.continueActiveTask()
		}
		// 信息面板：与 pause/resume 一样必须显式列出，否则 /skills 是在搜字符串 "skills"。
		if cmd == "/skills" {
			return m.showSkillsDialog()
		}
		if cmd == "/mcp" {
			return m.showMCPDialog()
		}
		// 手打 `/name args` + Enter 直接执行技能：首个 token 精确命中技能名才走这条路，
		// 否则照旧搜索（技能名是 kebab-case，搜索词通常不是）。
		if name, args := splitSlashSkill(cmd); name != "" {
			if registry := m.assistant.Skills(); registry != nil {
				if _, ok := registry.Get(name); ok {
					return m.runSkill(name, args)
				}
			}
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

	case "setup":
		return m.enterSetupMode()

	case "skills":
		return m.showSkillsDialog()

	case "mcp":
		return m.showMCPDialog()

	case "pause":
		return m.pauseActiveTask()

	case "continue", "unpause":
		return m.continueActiveTask()

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

// pauseActiveTask 按下当前任务的暂停闸门（A 层）。
//
// 与 Esc 的差别是这停住的代价：取消解开调用栈、这一轮就废了；闸门不取消 ctx，
// 执行在下一个 step 边界上停住，/continue 之后原地往下跑。
// 文案必须说"下一个边界"而不是"已停止" —— 一个在飞的 run_bash 最长要跑
// defaultToolTimeout(180s) 才会到边界，说"已停"是骗人。
func (m *model) pauseActiveTask() tea.Cmd {
	gate := m.activeGate()
	if gate == nil {
		m.setInfoMsg("No running task to pause")
		return nil
	}
	if gate.Paused() {
		m.setInfoMsg("Task is already paused — /continue to resume")
		return nil
	}
	gate.Pause("/pause")
	m.markTaskPaused()
	m.appendStatusLine("⏸ Pausing — the run stops at the next step boundary. /continue to resume.")
	return nil
}

// continueActiveTask 放开闸门。只能放开"按过且任务还活着"的闸门：
// 暂停中被取消的任务在本进程里救不回来（跨进程继续是 P2 的 B 层）。
func (m *model) continueActiveTask() tea.Cmd {
	gate := m.activeGate()
	if gate == nil {
		m.setInfoMsg("No task to continue")
		return nil
	}
	if !gate.Paused() {
		m.setInfoMsg("Task is not paused")
		return nil
	}
	gate.Resume()
	m.markTaskResumed()
	m.appendStatusLine("▶ Resumed from the step boundary.")
	return nil
}

// activeGate 取当前任务的闸门；没有任务、任务已结束时返回 nil。
// 返回 nil 也可能是"这个任务的 ctx 没带闸门"——两种都必须拒绝而不是假装停了。
func (m *model) activeGate() *pause.Gate {
	if m.currentTask == nil || !m.taskRunning {
		return nil
	}
	return m.currentTask.Gate()
}

// appendStatusLine 写一行状态提示并让 footer 重算（状态栏里的暂停标记就靠这一步）。
func (m *model) appendStatusLine(text string) {
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   text,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
	m.invalidateFooterCache()
}

// markTaskPaused / markTaskResumed 只动状态字符串与时间戳：闸门停住不产生
// task_complete，所以这条路径绝不能走 handleTaskCompleteMsg —— 那里会换掉整个
// ctx（tui_update_tasks.go 的 newCtx 分支），而闸门就挂在那棵被换掉的 ctx 上。
func (m *model) markTaskPaused() {
	if m.currentTask != nil {
		m.currentTask.Status = http.TaskStatusPaused
		m.currentTask.UpdatedAt = time.Now()
	}
}

func (m *model) markTaskResumed() {
	if m.currentTask != nil {
		m.currentTask.Status = http.TaskStatusRunning
		m.currentTask.UpdatedAt = time.Now()
	}
}

// timeoutTarget 是模型设置面板中"LLM 调用超时"行的哨兵 Target 值；选中后打开
// 超时对话框而非 provider 选择器（见 tui_update_keys.go 的 AgentSelectDialog 分支）。
const timeoutTarget = "timeout"

// showModelSelectionDialog opens an interactive agent → provider selection dialog
func (m *model) showModelSelectionDialog() tea.Cmd {
	// 运行中也能换模型：改的是 Client 的运行时覆盖，正在飞的那次调用不受影响。
	if m.taskRunning {
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   "Task running: the in-flight call keeps its model; the Director uses the new one at its next step, sub-agents at their next delegation.",
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
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

// ── 技能与 MCP 信息面板 ──

// showSkillsDialog 列出已加载的技能（名称/描述/来源/front-matter 档）。
// 入栈前必须 SetBounds：弹窗守卫会吞 WindowSizeMsg，漏设的话对话框永远按 0×0 渲染。
func (m *model) showSkillsDialog() tea.Cmd {
	var rows []components.SkillRow
	if registry := m.assistant.Skills(); registry != nil {
		for _, s := range registry.Skills() {
			rows = append(rows, components.SkillRow{
				Name:        s.Name,
				Description: s.Description,
				Source:      s.Source,
				FrontMatter: s.FrontMatter,
			})
		}
	}
	d := components.NewSkillsDialog(rows)
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// showMCPDialog 列出每个 server 的状态账（与 system prompt 的状态行同源）。
func (m *model) showMCPDialog() tea.Cmd {
	var rows []components.MCPServerRow
	for _, h := range m.assistant.MCPServers() {
		detail := h.Error
		if detail == "" {
			detail = firstLine(h.Instructions)
		}
		rows = append(rows, components.MCPServerRow{
			Name:    h.Name,
			State:   h.State.String(),
			Detail:  detail,
			Tools:   h.Tools,
			Curated: h.Curated,
		})
	}
	d := components.NewMCPDialog(rows, m.assistant.MCPStatusLines())
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

// runSkill 把技能正文拼进任务提交。
// 用户显式点了这个技能就不该再为它付一次 use_skill 往返 —— 模型自主发现走那条路。
func (m *model) runSkill(name, args string) tea.Cmd {
	registry := m.assistant.Skills()
	if registry == nil {
		m.setInfoMsg("技能未启用（[skills] enable=false 或 YAGENT_SKILLS=0）")
		return nil
	}
	skill, ok := registry.Get(name)
	if !ok {
		m.setInfoMsg(fmt.Sprintf("技能 %q 不存在", name))
		return nil
	}
	body := skill.Body
	if strings.TrimSpace(args) != "" {
		body += "\n\n---\n用户上下文: " + args
	}
	m.setInfoMsg(fmt.Sprintf("正在执行 skill: %s", skill.Name))
	return m.submitTaskWithContent(body)
}

// firstLine 取一段文本的首行（MCP 面板的详情列只放得下一行）。
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}
