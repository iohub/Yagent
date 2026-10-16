// 引导向导的按键分派、表单行规格与首启必答项（docs/setup-wizard-ui-design.md §3、§5）。
package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"yagent/internal/config"
	"yagent/internal/models"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// formGroup 是表单的一区。必填区就是 queueProviderOp 的三条校验 + config.WriteProvider
// 对 base_url 的要求（见 §3.1 与 TestWizardRequiredGroupMatchesValidator）：
// 分组与校验必须同源，否则"必填"会和"保存时报错"漂移。
type formGroup int

const (
	groupRequired formGroup = iota
	groupOptional
)

// formRow 是表单的一行。
//
// temperature 刻意不在表单里：ProviderConfig.Temperature 是裸 float64，"写了 0.0"与"没写"
// 不可区分，而显式发 temperature:0 会把服务端采样顶成贪心解码 —— 问一个答不出来的问题
// 只会教用户乱填。文件里已有的值由 collectDraft 原样带过去。
//
// api_key 与 api_key_env 是**一行两态**（w.keyMode），不是两行：config.WriteProvider
// 本来就按"env 非空就删 api_key、否则删 api_key_env"处理互斥（setup.go:105-112），
// 分成两行只会让用户看见"两个都填了会怎样"这种只能靠 hint 说明的隐式规则。
type formRow struct {
	label       string
	key         string // draft 字段名，load/collect 用它对齐
	placeholder string
	hint        string
	numeric     bool
	secret      bool
	group       formGroup
}

var formRows = []formRow{
	{
		label: "name", key: "name", placeholder: "gateway", group: groupRequired,
		hint: "A-Z a-z 0-9 _ - · renaming later makes a second block",
	},
	{
		label: "api_base_url", key: "api_base_url", placeholder: "https://api.example.com/v1", group: groupRequired,
		hint: "any OpenAI-compatible endpoint · ctrl+p picks a known gateway",
	},
	{
		label: "api_key", key: "api_key", placeholder: "sk-...", secret: true, group: groupRequired,
		hint: "f2 keeps the secret in an env var instead",
	},
	{
		label: "model", key: "model", placeholder: "qwen3-coder-plus", group: groupRequired,
		hint: "catalog hits prefill context_window and feed the compression threshold",
	},
	{
		label: "api_format", key: "api_format", placeholder: "openai", group: groupOptional,
		hint: "openai | anthropic (native Messages + Extended Thinking)",
	},
	{
		label: "reasoning_effort", key: "reasoning_effort", placeholder: "high", group: groupOptional,
		hint: "low | medium | high | max · unrecognised = thinking off",
	},
	{
		label: "max_tokens", key: "max_tokens", placeholder: "8192", numeric: true, group: groupOptional,
		hint: "engine-level default · per-call override wins",
	},
	{
		label: "context_window", key: "context_window", placeholder: "1000000", numeric: true, group: groupOptional,
		hint: "TUI context bar only — the compression threshold comes from the catalog",
	},
}

// rowByKey / keyOfRow：字段名与行号之间的双向查找。渲染与状态机都靠它，
// 不再用"第 i 个输入框"这种一改表就散架的写法。
func formRowIndexByKey(key string) int {
	for i, r := range formRows {
		if r.key == key {
			return i
		}
	}
	return -1
}

func formRowByKey(key string) formRow {
	if i := formRowIndexByKey(key); i >= 0 {
		return formRows[i]
	}
	return formRow{}
}

// visibleFormRows 返回当前可见的行号：可选区折叠时它们根本不参与 tab 循环，
// 于是"焦点停在一个看不见的输入框里"不可能出现（TestWizardHiddenFieldsNeverHoldFocus）。
func (w *setupWizard) visibleFormRows() []int {
	out := make([]int, 0, len(formRows))
	for i, r := range formRows {
		if r.group == groupOptional && !w.optionalOpen {
			continue
		}
		out = append(out, i)
	}
	return out
}

// lastVisibleRow 是"在这行按 enter = 存草稿"的唯一判据。
// 曾经这个判断是硬编码的 len(w.inputs)-1，折叠区一加就会指错行。
func (w *setupWizard) lastVisibleRow() int {
	v := w.visibleFormRows()
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

// keyModeLabel 把第 2 行的标签随模式换掉，让"这一格现在写的是哪种密钥来源"上屏。
func (w *setupWizard) keyModeLabel() string {
	if w.keyMode == keyFromEnv {
		return "api_key_env"
	}
	return "api_key"
}

func (w *setupWizard) rowLabel(i int) string {
	if formRows[i].key == "api_key" {
		return w.keyModeLabel()
	}
	return formRows[i].label
}

// rowHint 是行右侧的实况提示。model 行由目录结果接管（它比静态说明更有用），
// key 行按模式换措辞。
func (w *setupWizard) rowHint(i int) string {
	r := formRows[i]
	switch {
	case r.key == "model":
		return w.catalogHint(w.inputAt("model").Value())
	case r.key == "api_key":
		if w.keyMode == keyFromEnv {
			return "name of the variable · startup names it if unset · f2 types the key instead"
		}
		return "stays in config.toml (0600) · f2 keeps it in $ENV instead"
	default:
		return r.hint
	}
}

// ensureInputs 按表规格准备 textarea。样式沿用主输入框那套令牌（见 newInputTextarea）：
// 否则配置表单比聊天框还朴素，而它是用户对这个项目的第一印象。
func (w *setupWizard) ensureInputs() {
	if len(w.inputs) == len(formRows) {
		return
	}
	w.inputs = make([]textarea.Model, len(formRows))
	for i := range w.inputs {
		w.inputs[i] = w.newFieldTextarea(formRows[i].placeholder)
	}
}

// newFieldTextarea 复刻 newTaskInput 的视觉令牌（tui_model.go:648-686）：
// base bg 235 / 焦点 prompt 39 加粗 / ❯ 前缀 / placeholder italic 243 / 光标 39 bar。
func (w *setupWizard) newFieldTextarea(placeholder string) textarea.Model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	ta.SetWidth(w.fieldWidth())
	ta.CharLimit = 512
	ta.SetVirtualCursor(true)
	ta.Placeholder = placeholder
	applyTextareaTokens(&ta)
	return ta
}

// inputAt 按字段名取输入框（越界给一个空壳，调用方只读 Value）。
func (w *setupWizard) inputAt(key string) textarea.Model {
	i := formRowIndexByKey(key)
	if i < 0 || i >= len(w.inputs) {
		return textarea.Model{}
	}
	return w.inputs[i]
}

func (w *setupWizard) setInputValue(key, value string) {
	if i := formRowIndexByKey(key); i >= 0 && i < len(w.inputs) {
		w.inputs[i].SetValue(value)
	}
}

// loadDraftIntoInputs 把草稿灌进控件（进入编辑屏时调一次）。
func (w *setupWizard) loadDraftIntoInputs(d config.ProviderDraft) {
	w.ensureInputs()
	if d.APIKeyEnv != "" {
		w.keyMode = keyFromEnv
	} else {
		w.keyMode = keyFromLiteral
	}
	secret := d.APIKey
	if w.keyMode == keyFromEnv {
		secret = d.APIKeyEnv
	}
	values := map[string]string{
		"name": d.Name, "api_base_url": d.APIBaseURL, "api_key": secret, "model": d.Model,
		"api_format": d.APIFormat, "reasoning_effort": d.ReasoningEffort,
		"max_tokens": posIntString(d.MaxTokens), "context_window": posIntString(d.ContextWindow),
	}
	for key, v := range values {
		w.setInputValue(key, v)
	}
	for i := range w.inputs {
		w.inputs[i].SetWidth(w.fieldWidth())
		w.inputs[i].Blur()
	}
	w.ctxAutoValue = 0
	if cw := models.ContextWindow(d.Model); cw > 0 && posIntString(d.ContextWindow) == strconv.Itoa(cw) {
		// 文件里那个值就是目录给的，说明没人手输过 —— 允许继续由目录接管。
		w.ctxAutoValue = cw
	}
	// 折叠不是状态，但"编辑一个有可选字段的 provider"时得看得见它们。
	w.optionalOpen = d.APIFormat != "" || d.ReasoningEffort != "" || d.MaxTokens > 0 || d.ContextWindow > 0
	w.fieldIdx = 0
	w.errMsg = ""
	w.note = ""
	if d.APIKey != "" && d.APIKeyEnv != "" {
		// 旧文件两种来源都填了。$ENV 在 validate() 里展开进 APIKey，所以明文那行本来就不生效；
		// 保存时 config.WriteProvider 会把它删掉 —— 这里提前说明，别让删行看起来像丢数据。
		w.note = "both api_key and api_key_env were set; $ENV wins, the literal key is dropped on save"
	}
}

func posIntString(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

// collectDraft 从控件收回草稿。数字留空或非法 = 0 = 不写这一行（与"0 一律按没配"一致）。
// 温度从文件里的原值带过来，不因为打开过一次表单就把它抹掉。
func (w *setupWizard) collectDraft() config.ProviderDraft {
	secret := strings.TrimSpace(w.inputAt("api_key").Value())
	d := config.ProviderDraft{
		Name:            strings.TrimSpace(w.inputAt("name").Value()),
		APIBaseURL:      strings.TrimSpace(w.inputAt("api_base_url").Value()),
		Model:           strings.TrimSpace(w.inputAt("model").Value()),
		APIFormat:       strings.ToLower(strings.TrimSpace(w.inputAt("api_format").Value())),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(w.inputAt("reasoning_effort").Value())),
		MaxTokens:       atoiOrZero(w.inputAt("max_tokens").Value()),
		ContextWindow:   atoiOrZero(w.inputAt("context_window").Value()),
	}
	if w.keyMode == keyFromEnv {
		d.APIKeyEnv = secret
	} else {
		d.APIKey = secret
	}
	if existing, ok := w.existingDraft(d.Name); ok {
		d.Temperature = existing.Temperature
	}
	return d
}

// toggleKeyMode 换密钥来源，并把已经打的字搬过去 —— 用户不该为了改来源重打一遍 key。
func (w *setupWizard) toggleKeyMode() {
	row := formRowIndexByKey("api_key")
	if row < 0 || row >= len(w.inputs) {
		return
	}
	value := w.inputs[row].Value()
	if w.keyMode == keyFromEnv {
		w.keyMode = keyFromLiteral
	} else {
		w.keyMode = keyFromEnv
	}
	w.errMsg = ""
	if value == "" {
		return
	}
	if w.keyMode == keyFromEnv {
		// 明文 key 里挑不出变量名，清空让用户自己写；反过来（变量名→明文）同样是错的方向，
		// 所以两个方向都只搬"看起来还合理"的那一半：全大写下划线才当变量名。
		if !looksLikeEnvName(value) {
			w.inputs[row].SetValue("")
			w.note = "switched to $ENV: type the variable name, not the key itself"
			return
		}
	}
	w.inputs[row].SetValue(value)
}

func looksLikeEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '_':
		default:
			return false
		}
	}
	return true
}

// applyCatalogPrefill 用内嵌目录实况回填 context_window。
// 只在"该行为空"或"仍是上一次自动填的那个值"时写 —— 手输过的数绝不覆盖。
func (w *setupWizard) applyCatalogPrefill() {
	i := formRowIndexByKey("context_window")
	if i < 0 || i >= len(w.inputs) {
		return
	}
	cw := models.ContextWindow(strings.TrimSpace(w.inputAt("model").Value()))
	previous := w.inputs[i].Value()
	if w.ctxAutoValue > 0 && previous != strconv.Itoa(w.ctxAutoValue) {
		w.ctxAutoValue = 0 // 有人手输过，这一行从此归用户
	}
	if previous != "" && w.ctxAutoValue == 0 {
		return
	}
	if cw <= 0 {
		return
	}
	w.inputs[i].SetValue(strconv.Itoa(cw))
	w.ctxAutoValue = cw
}

// existingDraft 找文件里（或已排队草稿里）的同名 provider。
func (w *setupWizard) existingDraft(name string) (config.ProviderDraft, bool) {
	if pending, ok := w.pendingFor(name); ok {
		return pending, true
	}
	for _, row := range w.snap.providers {
		if row.draft.Name == name {
			return row.draft, true
		}
	}
	return config.ProviderDraft{}, false
}

func atoiOrZero(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil || n < 0 {
		return 0
	}
	return n
}

// setField 把焦点移到某行（唯一的焦点入口，保证隐藏行永远拿不到焦点）。
func (w *setupWizard) setField(idx int) {
	if idx < 0 || idx >= len(w.inputs) {
		return
	}
	if !w.rowIsVisible(idx) {
		return
	}
	if w.fieldIdx >= 0 && w.fieldIdx < len(w.inputs) {
		w.inputs[w.fieldIdx].Blur()
	}
	w.fieldIdx = idx
}

func (w *setupWizard) rowIsVisible(idx int) bool {
	for _, i := range w.visibleFormRows() {
		if i == idx {
			return true
		}
	}
	return false
}

func (w *setupWizard) focusField() tea.Cmd {
	if w.fieldIdx < 0 || w.fieldIdx >= len(w.inputs) {
		return nil
	}
	w.inputs[w.fieldIdx].Focus()
	return textarea.Blink
}

// moveField 在可见行之间前进/后退（折叠的可选区被整段跳过）。
func (w *setupWizard) moveField(delta int) (tea.Model, tea.Cmd) {
	visible := w.visibleFormRows()
	if len(visible) == 0 {
		return w, nil
	}
	at := len(visible) - 1
	for i, v := range visible {
		if v == w.fieldIdx {
			at = i
			break
		}
	}
	w.setField(visible[(at+delta+len(visible))%len(visible)])
	return w, w.focusField()
}

// Update 驱动状态机。宿主（独立程序或主 model）负责把 KeyMsg / WindowSizeMsg /
// tea.PasteMsg / Focus/Blur 送进来；向导不消费事件总线消息（它没有任务在跑）。
func (w *setupWizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		w.ensureInputs()
		for i := range w.inputs {
			w.inputs[i].SetWidth(w.fieldWidth())
		}
		return w, nil

	case probeResultMsg:
		w.probing = false
		w.errMsg = ""
		w.probeDone = time.Now()
		w.probeLatency = msg.latency
		if msg.err != nil {
			// provider 的原话逐字显示：状态码与文案就是用户接下来要去搜的东西。
			w.probeErr = msg.err.Error()
			w.probeText = ""
			w.note = ""
		} else {
			w.probeErr = ""
			w.probeText = msg.text
		}
		return w, nil

	case probeTickMsg:
		// 探测期间每 120ms 走一帧。结果一到就停：空转的 spinner 比没有 spinner 更像卡死。
		if !w.probing {
			return w, nil
		}
		w.spinFrame++
		return w, tickProbe()

	case tea.KeyMsg:
		m, cmd := w.handleKey(msg)
		// `done` 只是状态机里的"我要走了"，**退出是宿主的决定**：
		// 独立跑时必须翻成 tea.Quit，否则程序永远停在最后一屏（q 与 ctrl+c 看起来失灵）；
		// 嵌在主 TUI 时必须不翻 —— 那会把整个 yagent 一起退掉。
		if w.done && w.standalone {
			return m, tea.Quit
		}
		return m, cmd
	}

	// 表单屏要把粘贴/鼠标/焦点转给当前字段的 textarea，其它屏忽略。
	if w.step == stepProviderForm && !w.presetOpen {
		switch msg.(type) {
		case tea.MouseMsg, tea.FocusMsg, tea.BlurMsg, tea.PasteMsg:
			if w.fieldIdx < 0 || w.fieldIdx >= len(w.inputs) {
				return w, nil
			}
			var cmd tea.Cmd
			w.inputs[w.fieldIdx], cmd = w.inputs[w.fieldIdx].Update(msg)
			return w, cmd
		}
	}
	return w, nil
}

// handleKey 是向导唯一的按键入口。
func (w *setupWizard) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if w.step == stepProviderForm {
		return w.handleFormKey(msg, key)
	}

	switch key {
	case "ctrl+c", "q":
		w.done = true
		return w, nil
	case "esc":
		if w.step == stepSummary {
			// 摘要屏的 Esc 回上一屏；放弃整轮填写请用 q —— 别让一次误按丢掉所有输入。
			w.step = stepFirstRun
			return w, nil
		}
		w.done = true
		return w, nil
	}

	switch w.step {
	case stepWelcome:
		switch key {
		case "enter":
			w.step = stepProviders
		case "a":
			// 欢迎屏上"我想直接加一家 provider"是最常见的来意（第二次运行 setup 的人
			// 往往就是来加第二个 provider 的）。只认 enter 会让人以为界面没反应。
			w.step = stepProviders
			return w.handleProvidersKey("a")
		}
		return w, nil
	case stepProviders:
		return w.handleProvidersKey(key)
	case stepProbe:
		return w.handleProbeKey(key)
	case stepDefault:
		return w.handleDefaultKey(key)
	case stepFirstRun:
		return w.handleFirstRunKey(key)
	case stepSummary:
		return w.handleSummaryKey(key)
	}
	return w, nil
}

// handleSummaryKey：enter 落盘，j/k/g/G 只在排队条目高出正文时动。
// 摘要屏没有"选中某条 op"这回事（要么全写要么不写），所以那两个键只滚动视图，不改状态。
func (w *setupWizard) handleSummaryKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "enter":
		total := len(w.ops)
		if err := w.applyOps(); err != nil {
			// applyOps 不是事务：失败之前的几条已经落盘了，所以错误里必须带上
			// "是哪一条 note 失败了"和"已经写了几条"（AGENTS §11 坑 28）。
			w.errMsg = fmt.Sprintf("%s — %d of %d changes already written", err, w.writtenCount, total)
			return w, nil
		}
		w.saved = true
		w.note = fmt.Sprintf("wrote %d change(s) to %s", total, w.path)
		w.ops = nil
		w.done = true
		return w, nil
	case "down", "j":
		w.summaryTop++
		return w, nil
	case "up", "k":
		if w.summaryTop > 0 {
			w.summaryTop--
		}
		return w, nil
	case "g":
		w.summaryTop = 0
		return w, nil
	case "G":
		w.summaryTop = len(w.ops)
		return w, nil
	}
	return w, nil
}

func (w *setupWizard) handleFormKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	if w.presetOpen {
		return w.handlePresetKey(key)
	}
	switch key {
	case "esc":
		w.step = stepProviders
		return w, nil
	case "ctrl+s":
		w.queueProviderOp()
		return w, nil
	case "tab":
		return w.moveField(1)
	case "shift+tab", "backtab":
		return w.moveField(-1)
	case "enter":
		// 末格 = 存草稿。判据是 lastVisibleRow()，不是 len(inputs)-1：
		// 折叠态的"末格"在必填区，展开态在可选区。
		if w.fieldIdx == w.lastVisibleRow() {
			w.queueProviderOp()
			return w, nil
		}
		return w.moveField(1)
	case "ctrl+o":
		// 模态键一律带修饰符：表单屏上任何裸字母都要先送进 textarea，
		// 否则填一个含 p 的 URL 就会莫名打开预设列表（曾实测吃掉 "https" 里的 ps）。
		return w.toggleOptional()
	case "ctrl+p":
		w.presetOpen = true
		w.cursor = 0
		w.errMsg = ""
		return w, nil
	case "f2":
		w.toggleKeyMode()
		return w, w.focusField()
	}

	// 名称不可改：改名等于另一个块，会把旧块留在文件里冒充生效配置。
	if formRows[w.fieldIdx].key == "name" && w.editing != "" {
		return w, nil
	}

	// 数字字段里字母直接忽略，免得 "8k" 这种写法静默变成 0。
	if formRows[w.fieldIdx].numeric {
		for _, r := range key {
			if r < '0' || r > '9' {
				return w, nil
			}
		}
	}
	var cmd tea.Cmd
	w.inputs[w.fieldIdx], cmd = w.inputs[w.fieldIdx].Update(msg)
	if formRows[w.fieldIdx].key == "model" {
		w.applyCatalogPrefill()
	}
	return w, cmd
}

// toggleOptional 只切换可见性：不移动焦点（除非焦点正被折叠掉）、不改草稿。
func (w *setupWizard) toggleOptional() (tea.Model, tea.Cmd) {
	w.optionalOpen = !w.optionalOpen
	if !w.rowIsVisible(w.fieldIdx) {
		w.setField(w.lastVisibleRow())
	}
	return w, w.focusField()
}

// handlePresetKey 是表单屏内的预设选择子模式。它不是新的一屏（7 屏拓扑不动），
// 所以 esc 与任何非列表键都只是关掉它。
func (w *setupWizard) handlePresetKey(key string) (tea.Model, tea.Cmd) {
	n := len(endpointPresets)
	switch key {
	case "esc", "p":
		w.presetOpen = false
		return w, w.focusField()
	case "down", "j":
		w.cursor = (w.cursor + 1) % n
	case "up", "k":
		w.cursor = (w.cursor - 1 + n) % n
	case "enter":
		w.applyPreset(endpointPresets[w.cursor])
		return w, w.focusField()
	default:
		if idx, ok := digitIndex(key, n); ok {
			w.cursor = idx
			w.applyPreset(endpointPresets[idx])
			return w, w.focusField()
		}
		w.presetOpen = false
		return w, w.focusField()
	}
	return w, w.focusField()
}

// digitIndex 把 "1".."9" 映射到条目下标（快捷选，与原方案 §6.2 的承诺一致）。
func digitIndex(key string, n int) (int, bool) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return 0, false
	}
	i := int(key[0] - '1')
	if i >= n {
		return 0, false
	}
	return i, true
}

func (w *setupWizard) handleProvidersKey(key string) (tea.Model, tea.Cmd) {
	rows := w.effectiveProviders()
	move := func(delta int) {
		if len(rows) == 0 {
			return
		}
		w.cursor = (w.cursor + delta + len(rows)) % len(rows)
	}
	switch key {
	case "up", "k":
		move(-1)
		return w, nil
	case "down", "j":
		move(1)
		return w, nil
	case "a":
		w.editing = ""
		w.step = stepProviderForm
		w.loadDraftIntoInputs(config.ProviderDraft{})
		return w, w.focusField()
	case "e", "enter":
		if len(rows) == 0 {
			w.errMsg = "no provider yet — press a to add one"
			return w, nil
		}
		d := w.rowAt(rows).draft
		w.editing = d.Name
		w.step = stepProviderForm
		if pending, ok := w.pendingFor(d.Name); ok {
			d = pending
		}
		w.loadDraftIntoInputs(d)
		return w, w.focusField()
	case "d", "x":
		if len(rows) == 0 {
			return w, nil
		}
		w.queueDeleteOp(w.rowAt(rows).draft.Name)
		return w, nil
	case "s":
		if len(rows) == 0 {
			return w, nil
		}
		w.queueDefaultOp(w.rowAt(rows).draft.Name)
		return w, nil
	case "t":
		if len(rows) == 0 {
			return w, nil
		}
		d := w.rowAt(rows).draft
		if pending, ok := w.pendingFor(d.Name); ok {
			d = pending
		}
		w.step = stepProbe
		return w, w.startProbeCmd(d)
	case "c":
		if len(rows) == 0 {
			w.errMsg = "add at least one provider first"
			return w, nil
		}
		w.errMsg = ""
		w.step = stepDefault
		return w, nil
	default:
		if idx, ok := digitIndex(key, len(rows)); ok {
			w.cursor = idx
			w.step = stepProviderForm
			d := rows[idx].draft
			if pending, ok := w.pendingFor(d.Name); ok {
				d = pending
			}
			w.editing = d.Name
			w.loadDraftIntoInputs(d)
			return w, w.focusField()
		}
	}
	return w, nil
}

func (w *setupWizard) handleProbeKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "p":
		rows := w.effectiveProviders()
		return w, w.startProbeCmd(w.rowAt(rows).draft)
	case "enter":
		w.step = stepProviderForm
		return w, w.focusField()
	default:
		w.step = stepProviders
		return w, nil
	}
}

func (w *setupWizard) handleDefaultKey(key string) (tea.Model, tea.Cmd) {
	rows := w.effectiveProviders()
	switch key {
	case "up", "k":
		if w.cursor > 0 {
			w.cursor--
		}
	case "down", "j":
		if w.cursor < len(rows)-1 {
			w.cursor++
		}
	case "enter", "c":
		if len(rows) == 0 {
			w.errMsg = "no provider to default to"
			break
		}
		w.queueDefaultOp(w.rowAt(rows).draft.Name)
		w.errMsg = ""
		w.step = stepFirstRun
		w.cursor = 0
	default:
		if idx, ok := digitIndex(key, len(rows)); ok {
			w.cursor = idx
			w.queueDefaultOp(rows[idx].draft.Name)
			w.step = stepFirstRun
			w.cursor = 0
		}
	}
	return w, nil
}

// 首启必答项（决策记录 D3）：五项都是"第一次用就必须决定、但手写 TOML 时最容易漏"的。
// 每行是一串离散取值，左右键循环 —— 不给自由文本，免得把一次配置变成第二次打字。
var firstRunValueSets = [5][]string{
	{"(no instruction)", "Chinese", "English"},
	{"ask for approval", "yolo (-y)", "full yolo (-Y)"},
	{"auto", "fixed"},
	{"enabled", "disabled"},
	{"5m", "10m", "15m", "30m"},
}

var firstRunLabels = [5]string{"language", "autonomy", "routing", "browser", "llm timeout"}

// initFirstRunIdx 把文件现状还原成 5 个下标 —— 向导打开时显示的是"你现在是什么"，
// 不是"模板建议什么"。
func (w *setupWizard) initFirstRunIdx() {
	w.firstRunIdx[0] = langIndex(w.snap.lang)
	w.firstRunIdx[1] = autonomyIndex(w.snap.yolo, w.snap.fullYolo)
	w.firstRunIdx[2] = boolIndex(w.snap.routingMode == "fixed")
	w.firstRunIdx[3] = browserIndex(w.snap.browserEnabled)
	w.firstRunIdx[4] = timeoutIndex(w.snap.llmTimeout)
}

// firstRunDescriptions 每行一行"改了会怎样"。autonomy 的最高档要让 WorkspaceGuard
// 之外的一切确认消失，值得在唯一一次决定它的屏幕上显形。
var firstRunDescriptions = [5]string{
	"language the model writes answers and <thought> bodies in",
	"workspace-external paths still ask; the top tier removes every prompt",
	"pick fast / balanced / high per task difficulty",
	"Browser-Agent — needs the browser-control CLI and a Chrome",
	"ceiling of one LLM call; compression and retries live under it",
}

// firstRunCurrents 是"现状"，用来在改动过的行上打 ~ 记号。
func (w *setupWizard) firstRunCurrents() [5]string {
	browser := "enabled"
	if w.snap.browserEnabled != nil && !*w.snap.browserEnabled {
		browser = "disabled"
	}
	lang := firstRunValueSets[0][langIndex(w.snap.lang)]
	return [5]string{
		lang,
		firstRunValueSets[1][autonomyIndex(w.snap.yolo, w.snap.fullYolo)],
		firstRunValueSets[2][boolIndex(w.snap.routingMode == "fixed")],
		browser,
		firstRunValueSets[4][timeoutIndex(w.snap.llmTimeout)],
	}
}

func langIndex(s string) int {
	switch strings.ToLower(s) {
	case "":
		return 0
	case "chinese", "中文":
		return 1
	default:
		return 2
	}
}

func autonomyIndex(yolo, fullYolo bool) int {
	switch {
	case fullYolo:
		return 2
	case yolo:
		return 1
	default:
		return 0
	}
}

func boolIndex(b bool) int {
	if b {
		return 1
	}
	return 0
}

// browserIndex 把"段里没写过"当 enabled=true（DefaultBrowserConfig 的缺省）。
func browserIndex(p *bool) int {
	if p == nil {
		return 0
	}
	return boolIndex(!*p)
}

func timeoutIndex(d time.Duration) int {
	best, bestDiff := 1, time.Hour
	for i, s := range firstRunValueSets[4] {
		v, err := time.ParseDuration(s)
		if err != nil {
			continue
		}
		diff := d - v
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	return best
}

// firstRunDuration 把超时行还原成 Duration（4 个候选之外的值不会出现）。
func (w *setupWizard) firstRunDuration() time.Duration {
	d, err := time.ParseDuration(firstRunValueSets[4][w.firstRunIdx[4]])
	if err != nil {
		return config.DefaultLLMCallTimeout
	}
	return d
}

func (w *setupWizard) handleFirstRunKey(key string) (tea.Model, tea.Cmd) {
	last := len(firstRunLabels) - 1
	switch key {
	case "up", "k":
		w.cursor = (w.cursor - 1 + len(firstRunLabels)) % len(firstRunLabels)
	case "down", "j":
		w.cursor = (w.cursor + 1) % len(firstRunLabels)
	case "left", "h":
		w.cycleField(-1)
	case "right", "l", "space", "enter":
		if key == "enter" && w.cursor == last {
			return w.commitFirstRun()
		}
		w.cycleField(1)
	case "c":
		return w.commitFirstRun()
	}
	return w, nil
}

func (w *setupWizard) cycleField(delta int) {
	set := firstRunValueSets[w.cursor]
	if len(set) == 0 {
		return
	}
	w.firstRunIdx[w.cursor] = (w.firstRunIdx[w.cursor] + delta + len(set)) % len(set)
}

func (w *setupWizard) commitFirstRun() (tea.Model, tea.Cmd) {
	w.dropOpsOfKind("firstrun")
	w.ops = append(w.ops, pendingOp{
		kind:     "firstrun",
		firstRun: w.firstRunDraft(),
		note:     "write first-run choices",
	})
	w.step = stepSummary
	w.summaryTop = 0
	return w, nil
}

// firstRunDraft 把 5 行选择还原成写盘用的结构。
// UseProvider 留空：默认档是上一屏单独一条 op 干的事，这里重复写会把那条 op 覆盖成旧值。
func (w *setupWizard) firstRunDraft() config.FirstRunDraft {
	d := config.FirstRunDraft{
		RoutingMode:    firstRunValueSets[2][w.firstRunIdx[2]],
		LLMTimeout:     w.firstRunDuration(),
		BrowserEnabled: w.firstRunIdx[3] == 0,
	}
	if i := w.firstRunIdx[0]; i != 0 {
		d.Lang = firstRunValueSets[0][i]
	}
	switch w.firstRunIdx[1] {
	case 1:
		d.YoloMode = true
	case 2:
		d.YoloMode = true
		d.FullYoloMode = true
	}
	return d
}
