// 引导向导的按键分派、表单与首启必答项（docs/config-trim-onboarding-design.md §6.2）。
package tui

import (
	"fmt"
	"strings"
	"time"

	"yagent/internal/config"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// setupField 是 provider 表单的一行。
// 温度刻意不在表单里：ProviderConfig.Temperature 是裸 float64，"写了 0.0"与"没写"
// 不可区分，而显式发 temperature:0 会把服务端采样顶成贪心解码 —— 问一个答不出来的问题
// 只会教用户乱填。文件里已有的值由 collectDraft 原样带过去。
type setupField struct {
	label       string
	placeholder string
	hint        string
	numeric     bool
}

var setupFields = []setupField{
	{"api_base_url", "https://api.example.com/v1", "any OpenAI-compatible endpoint", false},
	{"api_key", "sk-...", "leave empty to use api_key_env below", false},
	{"api_key_env", "DASHSCOPE_API_KEY", "env var name; setting it removes the literal key", false},
	{"model", "qwen3-coder-plus", "known models also feed the compression threshold catalog", false},
	{"api_format", "openai", "openai | anthropic (native Messages + Extended Thinking)", false},
	{"reasoning_effort", "high", "low | medium | high | max; unrecognised = thinking off", false},
	{"max_tokens", "8192", "engine-level default; per-call override wins", true},
	{"context_window", "1000000", "TUI context bar only — compression threshold comes from the catalog", true},
}

// formFieldCount 比 setupFields 多一个：第 0 行是 provider 名（新增后不可改，
// 改了等于新建另一个块）。
var formFieldCount = len(setupFields) + 1

func (w *setupWizard) ensureInputs() {
	if len(w.inputs) == formFieldCount {
		return
	}
	w.inputs = make([]textarea.Model, formFieldCount)
	for i := range w.inputs {
		ta := textarea.New()
		ta.ShowLineNumbers = false
		ta.SetHeight(1)
		ta.SetWidth(w.fieldWidth())
		ta.CharLimit = 512
		ta.SetVirtualCursor(true)
		w.inputs[i] = ta
	}
}

// loadDraftIntoInputs 把草稿灌进控件（进入编辑屏时调一次）。
func (w *setupWizard) loadDraftIntoInputs(d config.ProviderDraft) {
	w.ensureInputs()
	values := []string{
		d.Name,
		d.APIBaseURL,
		d.APIKey,
		d.APIKeyEnv,
		d.Model,
		d.APIFormat,
		d.ReasoningEffort,
		posIntString(d.MaxTokens),
		posIntString(d.ContextWindow),
	}
	for i := range w.inputs {
		w.inputs[i].SetValue(values[i])
		w.inputs[i].SetWidth(w.fieldWidth())
		w.inputs[i].Blur()
	}
	w.fieldIdx = 0
	w.errMsg = ""
	w.note = ""
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
	d := config.ProviderDraft{
		Name:            strings.TrimSpace(w.inputs[0].Value()),
		APIBaseURL:      strings.TrimSpace(w.inputs[1].Value()),
		APIKey:          strings.TrimSpace(w.inputs[2].Value()),
		APIKeyEnv:       strings.TrimSpace(w.inputs[3].Value()),
		Model:           strings.TrimSpace(w.inputs[4].Value()),
		APIFormat:       strings.ToLower(strings.TrimSpace(w.inputs[5].Value())),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(w.inputs[6].Value())),
	}
	d.MaxTokens = atoiOrZero(w.inputs[7].Value())
	d.ContextWindow = atoiOrZero(w.inputs[8].Value())
	if existing, ok := w.existingDraft(d.Name); ok {
		d.Temperature = existing.Temperature
	}
	return d
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

func (w *setupWizard) focusField() tea.Cmd {
	if w.fieldIdx < 0 || w.fieldIdx >= len(w.inputs) {
		return nil
	}
	w.inputs[w.fieldIdx].Focus()
	return textarea.Blink
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
		if msg.err != nil {
			// provider 的原话逐字显示：状态码与文案就是用户接下来要去搜的东西。
			w.probeErr = msg.err.Error()
			w.probeText = ""
			w.note = ""
		} else {
			w.probeErr = ""
			w.probeText = msg.text
			w.note = fmt.Sprintf("answered in %s", msg.latency.Round(time.Millisecond))
		}
		return w, nil

	case tea.KeyMsg:
		return w.handleKey(msg)
	}

	// 表单屏要把粘贴/鼠标/焦点转给当前字段的 textarea，其它屏忽略。
	if w.step == stepProviderForm {
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
		if key == "enter" {
			w.step = stepProviders
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
		if key == "enter" {
			if err := w.applyOps(); err != nil {
				w.errMsg = err.Error()
				return w, nil
			}
			w.saved = true
			w.note = fmt.Sprintf("wrote %d change(s) to %s", len(w.ops), w.path)
			w.ops = nil
			w.done = true
		}
		return w, nil
	}
	return w, nil
}

func (w *setupWizard) handleFormKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
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
		// 名称行之后都算填完了：enter = 下一字段，最后一个字段上 enter = 存草稿。
		if w.fieldIdx == len(w.inputs)-1 {
			w.queueProviderOp()
			return w, nil
		}
		return w.moveField(1)
	}

	// 名称不可改：改名等于另一个块，会把旧块留在文件里冒充生效配置。
	if w.fieldIdx == 0 && w.editing != "" {
		return w, nil
	}

	// 数字字段里字母直接忽略，免得 "8k" 这种写法静默变成 0。
	if setupFieldNumericAt(w.fieldIdx - 1) {
		for _, r := range key {
			if r < '0' || r > '9' {
				return w, nil
			}
		}
	}
	var cmd tea.Cmd
	w.inputs[w.fieldIdx], cmd = w.inputs[w.fieldIdx].Update(msg)
	return w, cmd
}

func (w *setupWizard) moveField(delta int) (tea.Model, tea.Cmd) {
	if w.fieldIdx >= 0 && w.fieldIdx < len(w.inputs) {
		w.inputs[w.fieldIdx].Blur()
	}
	n := len(w.inputs)
	w.fieldIdx = (w.fieldIdx + delta + n) % n
	return w, w.focusField()
}

// setupFieldNumericAt 把表单行号（含第 0 行名称）映射到 numeric 属性。
func setupFieldNumericAt(i int) bool {
	if i < 0 || i >= len(setupFields) {
		return false
	}
	return setupFields[i].numeric
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
		name := w.rowAt(rows).draft.Name
		w.dropOpsOfKind("default")
		w.ops = append(w.ops, pendingOp{kind: "default", name: name, note: "use_provider = \"" + name + "\""})
		w.errMsg = ""
		w.step = stepFirstRun
		w.cursor = 0
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

func (w *setupWizard) initFirstRunIdx() {
	w.firstRunIdx[0] = langIndex(w.snap.lang)
	w.firstRunIdx[1] = autonomyIndex(w.snap.yolo, w.snap.fullYolo)
	w.firstRunIdx[2] = boolIndex(w.snap.routingMode == "fixed")
	w.firstRunIdx[3] = browserIndex(w.snap.browserEnabled)
	w.firstRunIdx[4] = timeoutIndex(w.snap.llmTimeout)
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
