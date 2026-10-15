package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/config"
	"yagent/internal/messaging"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// 按键构造 helper：v2 的 KeyMsg 是接口，KeyPressMsg 就是它的实现（= Key 结构）。
func pressKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// pressSpecial 构造特殊键（KeyEnter / KeyTab / KeyDown …，它们都是键码常量）。
func pressSpecial(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func pressCtrl(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

// setupConfigWith 写一份"全新机器"式的配置：模板原样，密钥还是占位符。
func setupConfigWith(t *testing.T, mutate func(string) string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := config.EnsureConfigExists(path); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	if mutate != nil {
		body = mutate(body)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func newWizardAt(t *testing.T, path string) *setupWizard {
	t.Helper()
	w := newSetupWizard(path, config.SetupPlaceholderKey, "provider \"aliyun\" still has api_key = \"your-key\"")
	w.width, w.height = 100, 40
	w.Init()
	return w
}

func typeText(t *testing.T, w *setupWizard, s string) {
	t.Helper()
	for _, r := range s {
		w.Update(pressKey(r))
	}
}

func tabToNext(t *testing.T, w *setupWizard) {
	t.Helper()
	w.Update(pressSpecial(tea.KeyTab))
}

func enterKey(t *testing.T, w *setupWizard) {
	t.Helper()
	w.Update(pressSpecial(tea.KeyEnter))
}

// 键位构造得对，后面所有状态机测试才有意义 —— 先钉住 String() 与向导识别的字符串一致。
func TestKeyHelpersMatchWizardVocabulary(t *testing.T) {
	cases := map[tea.KeyPressMsg]string{
		pressKey('a'):              "a",
		pressSpecial(tea.KeyEnter): "enter",
		pressSpecial(tea.KeyTab):   "tab",
		pressSpecial(tea.KeyDown):  "down",
		pressCtrl('s'):             "ctrl+s",
	}
	for msg, want := range cases {
		if got := msg.String(); got != want {
			t.Errorf("按键字符串 = %q, want %q", got, want)
		}
	}
}

// 完整走一遍向导：新增 provider → 设为默认 → 首启项 → 摘要落盘。
// 断言的是**写出来的文件能被下次启动正常加载**，因为那才是引导页的全部职责。
func TestWizardHappyPathWritesUsableConfig(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)

	enterKey(t, w) // welcome → providers
	if w.step != stepProviders {
		t.Fatalf("step = %v, want providers", w.step)
	}

	w.Update(pressKey('a')) // → form
	if w.step != stepProviderForm {
		t.Fatalf("step = %v, want providerForm", w.step)
	}
	typeText(t, w, "gateway")
	tabToNext(t, w)
	typeText(t, w, "https://gateway.example/v1")
	tabToNext(t, w)
	typeText(t, w, "sk-real-secret")
	tabToNext(t, w) // api_key_env 留空
	tabToNext(t, w)
	typeText(t, w, "gpt-test")
	tabToNext(t, w) // api_format 默认 openai
	tabToNext(t, w) // reasoning_effort 留空
	tabToNext(t, w) // max_tokens 留空
	tabToNext(t, w) // context_window 留空
	w.Update(pressCtrl('s'))
	if w.step != stepProviders {
		t.Fatalf("保存草稿后 step = %v, want providers（errMsg=%q）", w.step, w.errMsg)
	}
	if w.errMsg != "" {
		t.Fatalf("保存草稿报错: %q", w.errMsg)
	}

	// 默认档屏：列表按名字排序 → aliyun, gateway, local，把光标移到 gateway。
	w.Update(pressKey('c'))
	if w.step != stepDefault {
		t.Fatalf("step = %v, want default", w.step)
	}
	w.Update(pressSpecial(tea.KeyDown))
	enterKey(t, w)
	if w.step != stepFirstRun {
		t.Fatalf("step = %v, want firstRun（errMsg=%q）", w.step, w.errMsg)
	}

	// 首启项：语言从"不注入"右移两格到 English（顺序是 none → Chinese → English）。
	w.Update(pressSpecial(tea.KeyRight))
	w.Update(pressSpecial(tea.KeyRight))
	w.Update(pressKey('c'))
	if w.step != stepSummary {
		t.Fatalf("step = %v, want summary", w.step)
	}

	enterKey(t, w)
	if !w.Done() || !w.Saved() {
		t.Fatalf("Done=%v Saved=%v, want true/true（errMsg=%q）", w.Done(), w.Saved(), w.errMsg)
	}

	cfg, err := config.LoadFromFile(path)
	if err != nil {
		data, _ := os.ReadFile(path)
		t.Fatalf("引导写出的文件加载不了: %v\n---\n%s", err, data)
	}
	if cfg.Global.LLM.UseProvider != "gateway" {
		t.Errorf("use_provider = %q, want gateway", cfg.Global.LLM.UseProvider)
	}
	prov, ok := cfg.Global.LLM.Providers["gateway"]
	if !ok {
		t.Fatalf("gateway 块没写进去")
	}
	if prov.Model != "gpt-test" || prov.APIKey != "sk-real-secret" || prov.APIBaseURL != "https://gateway.example/v1" {
		t.Errorf("gateway 块内容不对: %+v", prov)
	}
	if cfg.Agent.SpeakLang != "English" {
		t.Errorf("lang = %q, want English", cfg.Agent.SpeakLang)
	}
	// 模板注释必须还在：整文件重编码是这套写回机制明令禁止的事。
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Yagent configuration") || !strings.Contains(string(data), "# ── Git checkpoints ──") {
		t.Errorf("模板注释被写回动作弄丢了：\n%s", data)
	}
}

// 摘要屏之前放弃 = 文件一个字节都不该动。
func TestWizardDiscardsUntilSummary(t *testing.T) {
	path := setupConfigWith(t, nil)
	before, _ := os.ReadFile(path)
	w := newWizardAt(t, path)

	enterKey(t, w)
	w.Update(pressKey('a'))
	typeText(t, w, "ghost")
	w.Update(pressSpecial(tea.KeyEscape)) // 表单 esc = 丢弃这次填写
	if w.step != stepProviders {
		t.Fatalf("step = %v, want providers", w.step)
	}
	w.Update(pressKey('q')) // 退出但不写
	if !w.Done() || w.Saved() {
		t.Fatalf("Done=%v Saved=%v, want true/false", w.Done(), w.Saved())
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("未落盘的引导改动了配置文件")
	}
}

// 删除还被引用的 provider 必须被拒，并把引用位置显示出来。
func TestWizardRefusesToDeleteReferencedProvider(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	enterKey(t, w)

	// 光标停在第一行（aliyun），它正是 use_provider 的目标。
	rows := w.effectiveProviders()
	if rows[0].draft.Name != "aliyun" {
		t.Fatalf("夹具的第一行 = %q, want aliyun", rows[0].draft.Name)
	}
	w.Update(pressKey('d'))
	if !strings.Contains(w.errMsg, "still referenced by: global.llm.use_provider") {
		t.Errorf("errMsg = %q, want 点名引用位置", w.errMsg)
	}
	for _, op := range w.ops {
		if op.kind == "delete" && op.name == "aliyun" {
			t.Error("被拒绝的删除仍然排进了队列")
		}
	}
}

// 数字字段里打字母不该静默变成 0（"8k" 是最常见的写法）。
func TestWizardFormFieldFiltersNonDigits(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	enterKey(t, w)
	w.Update(pressKey('a'))

	for i := 0; i < 7; i++ {
		tabToNext(t, w)
	}
	if !setupFields[6].numeric {
		t.Fatal("夹具依赖第 7 个字段是数字字段")
	}
	typeText(t, w, "8k32")
	if got := w.inputs[7].Value(); got != "832" {
		t.Errorf("max_tokens 输入框 = %q, want 过滤掉字母后的 \"832\"", got)
	}
}

// 名称不可改：改名等于另一个块，旧块会留在文件里冒充生效配置。
func TestWizardBlocksRename(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	enterKey(t, w)
	w.Update(pressSpecial(tea.KeyDown)) // 到 local
	w.Update(pressKey('e'))
	if w.editing != "local" {
		t.Fatalf("editing = %q, want local", w.editing)
	}
	typeText(t, w, "x")
	if got := w.inputs[0].Value(); got != "local" {
		t.Errorf("名称行被改成了 %q", got)
	}
}

// 坏配置也必须能进向导：它读的是文件文本，不是 validate() 的结果。
func TestWizardOpensOnUnreadableConfig(t *testing.T) {
	path := setupConfigWith(t, func(s string) string {
		return "this is not valid toml = = =\n"
	})
	w := newSetupWizard(path, config.SetupBroken, "config validation failed: api_key must be specified")
	w.width, w.height = 100, 40

	if len(w.snap.providers) != 0 {
		t.Errorf("坏配置却解出了 %d 个 provider", len(w.snap.providers))
	}
	enterKey(t, w)
	if w.step != stepProviders {
		t.Fatalf("step = %v, want providers（errMsg=%q）", w.step, w.errMsg)
	}
	view := w.View().Content
	if !strings.Contains(view, "no providers yet") {
		t.Errorf("空列表屏没给出下一步指引：\n%s", view)
	}
}

// api_key_env 的引用行要显示成 $VAR，而不是"没有密钥"。
func TestWizardMasksKeySources(t *testing.T) {
	env := config.ProviderDraft{Name: "a", APIKeyEnv: "DASHSCOPE_API_KEY"}
	if got := maskKey(env); got != "$DASHSCOPE_API_KEY" {
		t.Errorf("env 形态显示成 %q", got)
	}
	literal := config.ProviderDraft{Name: "b", APIKey: "sk-1234567890abcd"}
	if got := maskKey(literal); !strings.HasPrefix(got, "sk-1") || !strings.HasSuffix(got, "cd") || strings.Contains(got, "1234567890ab") {
		t.Errorf("明文密钥没被遮起来: %q", got)
	}
	ph := config.ProviderDraft{Name: "c", APIKey: "your-key"}
	if got := maskKey(ph); !strings.Contains(got, "not filled in") {
		t.Errorf("占位符没被说明: %q", got)
	}
}

// 首启项的取值必须还原成正确的配置组合。
func TestWizardFirstRunDraftMapping(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	w.step = stepFirstRun

	d := w.firstRunDraft()
	if d.Lang != "" || d.YoloMode || d.FullYoloMode {
		t.Errorf("默认首选项 = %+v, want 语言不注入 + 不自动放行", d)
	}
	if d.RoutingMode != "auto" || !d.BrowserEnabled || d.LLMTimeout != 10*time.Minute {
		t.Errorf("默认首选项 = %+v, want auto / enabled / 10m", d)
	}

	// autonomy 推到第二档 = yolo；第三档 = full yolo（同时 yolo 为真，与 -Y 的语义一致）。
	w.cursor = 1
	w.cycleField(1)
	if d := w.firstRunDraft(); !d.YoloMode || d.FullYoloMode {
		t.Errorf("autonomy 第二档 = %+v, want 只开 yolo", d)
	}
	w.cycleField(1)
	if d := w.firstRunDraft(); !d.YoloMode || !d.FullYoloMode {
		t.Errorf("autonomy 第三档 = %+v, want full yolo", d)
	}

	// browser 关掉
	w.cursor = 3
	w.cycleField(1)
	if d := w.firstRunDraft(); d.BrowserEnabled {
		t.Errorf("browser 行切换后仍为 enabled: %+v", d)
	}
}

// 面板必须容得下所有动态文本：每行不超过终端宽度，否则整屏错位。
func TestWizardRenderWidthMatrix(t *testing.T) {
	widths := []int{40, 50, 60, 72, 80, 100, 120, 160, 200, 240}
	steps := []setupStep{stepWelcome, stepProviders, stepProviderForm, stepProbe, stepDefault, stepFirstRun, stepSummary}

	for _, width := range widths {
		for _, step := range steps {
			path := setupConfigWith(t, nil)
			w := newWizardAt(t, path)
			w.width = width
			w.step = step
			w.errMsg = "a fairly long validation message that must not push the frame past the terminal edge: " +
				strings.Repeat("detail ", 12)
			w.note = "wrote 3 change(s)"
			w.probeErr = "HTTP 401: invalid api key " + strings.Repeat("x", 120)
			if _, cmd := w.Update(tea.WindowSizeMsg{Width: width, Height: 30}); cmd != nil {
				t.Fatalf("width=%d step=%v: WindowSizeMsg 不该产出 cmd", width, step)
			}
			view := w.View().Content
			for i, line := range strings.Split(view, "\n") {
				if lw := lipgloss.Width(line); lw > width {
					t.Fatalf("width=%d step=%v 第 %d 行越界（%d 列）: %q", width, step, i, lw, line)
				}
			}
		}
	}
}

// 终端太窄时给一句人话，而不是破碎的面板。
func TestWizardNarrowTerminal(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	w.width = 20
	view := w.View().Content
	if !strings.Contains(view, "widen the terminal") {
		t.Errorf("窄终端没给出提示: %q", view)
	}
}

// ── 嵌入主 TUI 的那一半 ──

// 任务运行中不进向导：它独占键盘，而任务事件必须有消费者。
func TestSetupRefusedWhileTaskRunning(t *testing.T) {
	m := newTestModel()
	m.taskRunning = true
	if cmd := m.enterSetupMode(); cmd != nil {
		t.Error("任务运行中 enterSetupMode 应当直接拒绝")
	}
	if m.setupActive {
		t.Error("被拒绝的 /setup 仍然进入了向导模式")
	}
}

// 向导打开期间 taskCompleteMsg 必须透传 —— 它是 taskRunning 唯一的清零点，
// 吞掉的表现是界面永久卡在"运行中"（AGENTS.md §11 坑 6）。
func TestSetupModePassesTaskCompleteThrough(t *testing.T) {
	// 必须用 chromeTestModel：handleTaskCompleteMsg 会清输入框（textarea），
	// 最小 fixture 里没有它，测到的就是 fixture 而不是这段透传。
	m := chromeTestModel(t, 100, 30)
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	m.wizard = w
	m.setupActive = true
	m.taskRunning = true

	_, _ = m.Update(taskCompleteMsg{})
	if m.taskRunning {
		t.Fatal("向导吞掉了 taskCompleteMsg，taskRunning 没被清零")
	}
	if !m.setupActive {
		t.Error("一条完成消息把向导关了：它应当只负责清零，不改变键盘所有权")
	}
}

// 退出向导后必须重新武装事件监听：listenForEvents 是一次性 cmd，没有重放队列。
func TestSetupExitReArmsEventListener(t *testing.T) {
	m := newTestModel()
	if m.eventCh == nil {
		m.eventCh = make(chan *messaging.MessageEvent, 1)
	}
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	m.wizard = w
	m.setupActive = true
	w.done = true

	_, cmd := m.Update(pressKey('q'))
	if cmd == nil {
		t.Fatal("退出向导时没有产出 cmd —— 事件流会永久断掉")
	}
	if m.setupActive {
		t.Error("向导结束后还占着键盘")
	}
}
