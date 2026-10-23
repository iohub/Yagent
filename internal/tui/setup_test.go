package tui

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"yagent/internal/config"
	"yagent/internal/messaging"
	"yagent/internal/models"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func pressKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func pressSpecial(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func pressCtrl(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

func pressF2() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyF2} }

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

func inputOf(w *setupWizard, key string) string {
	return w.inputAt(key).Value()
}

// 三格之间只有三次 tab —— 折叠态的 tab 循环不走可选区，这本身就是要被钉的形状。
func fillRequired(t *testing.T, w *setupWizard, name, url, secret, model string) {
	t.Helper()
	typeText(t, w, name)
	tabToNext(t, w)
	typeText(t, w, url)
	tabToNext(t, w)
	typeText(t, w, secret)
	tabToNext(t, w)
	typeText(t, w, model)
}

func openForm(t *testing.T, w *setupWizard) {
	t.Helper()
	enterKey(t, w) // welcome → providers
	w.Update(pressKey('a'))
	if w.step != stepProviderForm {
		t.Fatalf("step = %v, want providerForm（errMsg=%q）", w.step, w.errMsg)
	}
}

// 键位构造得对，后面所有状态机测试才有意义 —— 先钉住 String() 与向导识别的字符串一致。
func TestKeyHelpersMatchWizardVocabulary(t *testing.T) {
	cases := map[tea.KeyPressMsg]string{
		pressKey('a'):              "a",
		pressSpecial(tea.KeyEnter): "enter",
		pressSpecial(tea.KeyTab):   "tab",
		pressSpecial(tea.KeyDown):  "down",
		pressSpecial(tea.KeyF2):    "f2",
		pressCtrl('s'):             "ctrl+s",
	}
	for msg, want := range cases {
		if got := msg.String(); got != want {
			t.Errorf("按键字符串 = %q, want %q", got, want)
		}
	}
}

// 断言的是**写出来的文件能被下次启动正常加载**，因为那才是引导页的全部职责。
func TestWizardHappyPathWritesUsableConfig(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)

	openForm(t, w)
	fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real-secret", "gpt-test")
	w.Update(pressCtrl('s'))
	if w.errMsg != "" {
		t.Fatalf("保存草稿报错: %q", w.errMsg)
	}
	if w.step != stepProviders {
		t.Fatalf("step = %v, want providers", w.step)
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

	openForm(t, w)
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

// 数字字段里打字母不该静默变成 0（"8k" 是最常见的写法）；max_tokens 在可选区所以先 o 展开，这同时反证"折叠区不参与 tab 循环"。
func TestWizardFormFieldFiltersNonDigits(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)

	w.Update(pressCtrl('o'))
	if got := len(w.visibleFormRows()); got != len(formRows) {
		t.Fatalf("展开后可见行 = %d, want %d", got, len(formRows))
	}
	maxIdx := formRowIndexByKey("max_tokens")
	if !formRows[maxIdx].numeric {
		t.Fatalf("夹具依赖 max_tokens 是数字字段，实际 %+v", formRows[maxIdx])
	}
	for w.fieldIdx != maxIdx {
		before := w.fieldIdx
		tabToNext(t, w)
		if w.fieldIdx == before {
			t.Fatal("tab 到不了 max_tokens 行 —— 可见行推导有错")
		}
	}
	typeText(t, w, "8k32")
	if got := inputOf(w, "max_tokens"); got != "832" {
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
	if got := inputOf(w, "name"); got != "local" {
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
	if !strings.Contains(view, "No provider yet") {
		t.Errorf("空列表屏没给出下一步指引：\n%s", view)
	}
}

// 列表屏的 KEY 单元必须说清三种情况：有可用密钥、还是模板那个 your-key、还是根本没有 —— 首启最常见的事故就是"占位密钥看起来像已经配好了"。
func TestWizardMasksKeySources(t *testing.T) {
	env := config.ProviderDraft{Name: "a", APIKeyEnv: "DASHSCOPE_API_KEY"}
	if got := maskKey(env); got != "$DASHSCOPE_API_KEY" {
		t.Errorf("env 形态显示成 %q", got)
	}
	literal := config.ProviderDraft{Name: "b", APIKey: "sk-1234567890abcd"}
	if got := maskKey(literal); !strings.HasPrefix(got, "sk-") || strings.Contains(got, "1234567890ab") {
		t.Errorf("明文密钥没被遮起来: %q", got)
	}
	cases := []struct {
		d    config.ProviderDraft
		want string
	}{
		{config.ProviderDraft{Name: "c", APIKey: "your-key"}, "fix key"},
		{config.ProviderDraft{Name: "d"}, "no key"},
		{config.ProviderDraft{Name: "e", APIKey: "sk-real"}, ""},
		{config.ProviderDraft{Name: "f", APIKeyEnv: "X"}, ""},
	}
	for _, tc := range cases {
		if got := providerIssue(tc.d); got != tc.want {
			t.Errorf("providerIssue(%+v) = %q, want %q", tc.d, got, tc.want)
		}
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

	w.cursor = 3
	w.cycleField(1)
	if d := w.firstRunDraft(); d.BrowserEnabled {
		t.Errorf("browser 行切换后仍为 enabled: %+v", d)
	}
}

// 阶段轨的"已完成"来自账本而不是游标（决策 DB）—— "默认档指向一个不存在的块"必须让 DEFAULT 段退回未完成，否则轨道在承诺一个写不出来的状态。
func TestWizardRailCompletionComesFromLedger(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)

	// 判据是账本，所以这两段此刻就该是 ✓（按"走到第几屏"算的版本在这里会红）。
	if !w.stageDone(0) || !w.stageDone(1) {
		t.Errorf("welcome 屏上账本已有 provider 与默认档，轨道却显示未完成：[0]=%v [1]=%v",
			w.stageDone(0), w.stageDone(1))
	}

	enterKey(t, w)
	if !w.stageDone(0) {
		t.Error("文件里已有 provider，轨道的 PROVIDER 段却显示未完成")
	}
	// 反过来：一家 provider 都没有时，即便游标已经停在 providers 屏也不算完成。
	broken := setupConfigWith(t, func(string) string { return "use_provider = \"x\"\n" })
	w0 := newWizardAt(t, broken)
	enterKey(t, w0)
	if len(w0.effectiveProviders()) != 0 {
		t.Fatalf("夹具本该没有 provider，实际 %d 家", len(w0.effectiveProviders()))
	}
	if w0.stageDone(0) {
		t.Error("零 provider 却显示 PROVIDER 段完成 —— 判据又变回游标了")
	}

	// 停在 PROVIDER 段时按 s 设默认：段 1 立刻转 ✓，而游标一步都没往前走。
	w.Update(pressKey('s'))
	if w.currentDefault() != "aliyun" {
		t.Fatalf("currentDefault = %q, want aliyun", w.currentDefault())
	}
	if w.step != stepProviders || w.stage() != 0 {
		t.Errorf("s 不该换屏：step=%v stage=%d", w.step, w.stage())
	}
	if !w.stageDone(1) {
		t.Error("默认档已排队，DEFAULT 段却仍显示未完成")
	}

	w2 := newWizardAt(t, path)
	enterKey(t, w2)
	w2.ops = append(w2.ops, pendingOp{kind: "default", name: "gone"})
	if w2.stageDone(1) {
		t.Error("默认档指向一个不存在的 provider，DEFAULT 段仍显示完成")
	}
}

// 折叠态的焦点永远不可能落在看不见的行上（否则用户在往一个屏幕上不存在的格子里打字）。
func TestWizardHiddenFieldsNeverHoldFocus(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)

	visible := map[int]bool{}
	for _, i := range w.visibleFormRows() {
		visible[i] = true
	}
	if got := len(visible); got != 4 {
		t.Fatalf("折叠态可见行 = %d, want 4", got)
	}
	for i := 0; i < len(formRows)*3; i++ {
		tabToNext(t, w)
		if !visible[w.fieldIdx] {
			t.Fatalf("折叠态把焦点移到了隐藏行 %d（可见集合 %v）", w.fieldIdx, visible)
		}
	}

	// 焦点停在可选区时按 o 折叠：焦点必须被收回到可见行，而不是留在隐藏行上"盲打"。
	w.Update(pressCtrl('o'))
	maxIdx := formRowIndexByKey("max_tokens")
	for w.fieldIdx != maxIdx {
		before := w.fieldIdx
		tabToNext(t, w)
		if w.fieldIdx == before {
			t.Fatal("展开后到不了 max_tokens 行")
		}
	}
	w.Update(pressCtrl('o'))
	if w.fieldIdx == maxIdx {
		t.Error("折叠后焦点还停在被隐藏的行上")
	}
	if !w.rowIsVisible(w.fieldIdx) {
		t.Errorf("折叠后焦点落在不可见行 %d", w.fieldIdx)
	}
}

// 必填区的定义必须与"真正会被拒的字段"一致：少一格 = 用户以为填完了却存不下，多一格 = 表单在问不必要的问题。
func TestWizardRequiredGroupMatchesValidator(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)
	fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real", "gpt-test")

	save := func() {
		w.step = stepProviderForm
		w.Update(pressCtrl('s'))
	}
	save()
	if w.errMsg != "" {
		t.Fatalf("必填全填却被拒: %q", w.errMsg)
	}
	d, ok := w.pendingFor("gateway")
	if !ok {
		t.Fatal("没有排出 gateway 这条写操作")
	}
	other := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(other, []byte("[global.llm]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteProvider(other, d); err != nil {
		t.Errorf("表单认为填完了，config.WriteProvider 却拒收: %v", err)
	}

	// 逐个清空必填行：每一格都必须被表单自己拦住，而不是放行到写盘才炸。
	full := map[string]string{
		"name": "gateway", "api_base_url": "https://gateway.example/v1",
		"api_key": "sk-real", "model": "gpt-test",
	}
	for _, key := range []string{"name", "api_base_url", "api_key", "model"} {
		w.setField(formRowIndexByKey(key))
		w.inputs[w.fieldIdx].SetValue("")
		save()
		if w.errMsg == "" {
			t.Errorf("%s 清空后表单没有报错 —— 必填区少了一格", key)
		}
		w.setField(formRowIndexByKey(key))
		w.inputs[w.fieldIdx].SetValue(full[key])
		save()
		if w.errMsg != "" {
			t.Fatalf("填回 %s 后仍被拒: %q", key, w.errMsg)
		}
	}
}

// enter 在两个区的末格都存草稿（折叠态停在 model，展开态停在 context_window）。
func TestWizardEnterSavesAtEachGroupEnd(t *testing.T) {
	for _, open := range []bool{false, true} {
		path := setupConfigWith(t, nil)
		w := newWizardAt(t, path)
		openForm(t, w)
		if open {
			w.Update(pressCtrl('o'))
		}
		fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real", "gpt-test")

		if open {
			for w.fieldIdx != w.lastVisibleRow() {
				tabToNext(t, w)
			}
			if got := formRows[w.fieldIdx].key; got != "context_window" {
				t.Fatalf("展开态末格 = %q, want context_window", got)
			}
		} else if got := formRows[w.fieldIdx].key; got != "model" {
			t.Fatalf("折叠态末格 = %q, want model", got)
		}
		enterKey(t, w)
		if w.step != stepProviders || w.errMsg != "" {
			t.Errorf("open=%v 末格 enter 没存下草稿：step=%v errMsg=%q", open, w.step, w.errMsg)
		}
		if _, ok := w.pendingFor("gateway"); !ok {
			t.Errorf("open=%v 时队列里没有 gateway", open)
		}
	}
}

// 预设只填空字段：用户改过的 URL 不能被一次 p 冲掉。
func TestWizardPresetsOnlyFillEmptyFields(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)

	w.Update(pressCtrl('p'))
	if !w.presetOpen {
		t.Fatal("p 没打开预设选择")
	}
	w.Update(pressSpecial(tea.KeyEnter))
	if w.presetOpen {
		t.Error("enter 应用预设后预设列表还开着")
	}
	if inputOf(w, "api_base_url") == "" || inputOf(w, "model") == "" {
		t.Errorf("空字段没被预设填上: url=%q model=%q", inputOf(w, "api_base_url"), inputOf(w, "model"))
	}
	if inputOf(w, "name") != "" || inputOf(w, "api_key") != "" {
		t.Error("预设填了它不该碰的行（name / key）")
	}

	w.setInputValue("api_base_url", "https://mine.example/v1")
	w.Update(pressCtrl('p'))
	w.Update(pressKey('2')) // 第二条预设
	if got := inputOf(w, "api_base_url"); got != "https://mine.example/v1" {
		t.Errorf("预设覆盖了手输的 base_url: %q", got)
	}
	// 任何非列表键都只是关掉它，不会把表单的输入吃掉。
	w.Update(pressCtrl('p'))
	w.Update(pressKey('z'))
	if w.presetOpen {
		t.Error("预设子模式没被非列表键关掉")
	}
}

// 目录回填 context_window：命中就填，手输过就绝不覆盖。
func TestWizardCatalogPrefillDoesNotOverwriteTypedValue(t *testing.T) {
	const modelID = "qwen3-coder-plus"
	loadTestCatalog(t, `{"dashscope/qwen3-coder-plus": {"limit": {"context": 1000000}}}`)
	cw := models.ContextWindow(modelID)
	if cw <= 0 {
		t.Fatalf("夹具模型 %q 不在目录里，这条测试没有被测分支", modelID)
	}

	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)
	fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real", modelID)

	if got := inputOf(w, "context_window"); got != strconv.Itoa(cw) {
		t.Errorf("context_window = %q, want 目录值 %d", got, cw)
	}
	if w.ctxAutoValue != cw {
		t.Errorf("ctxAutoValue = %d, want %d（否则下一次回填会覆盖用户输入）", w.ctxAutoValue, cw)
	}

	w.Update(pressCtrl('o'))
	ctxIdx := formRowIndexByKey("context_window")
	for w.fieldIdx != ctxIdx {
		before := w.fieldIdx
		tabToNext(t, w)
		if w.fieldIdx == before {
			t.Fatal("到不了 context_window 行")
		}
	}
	w.inputs[ctxIdx].SetValue("")
	typeText(t, w, "123")
	w.setField(formRowIndexByKey("model"))
	typeText(t, w, " ") // 走的正是"敲一个字符"那条 applyCatalogPrefill 路径
	if got := inputOf(w, "context_window"); got != "123" {
		t.Errorf("手输的 context_window 被目录覆盖了: 期望 \"123\"，实际 %q", got)
	}
}

// f2 换密钥来源：值搬过去、模式换掉、写出来的块只能有一种来源。
func TestWizardKeySourceToggle(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)
	typeText(t, w, "gateway")
	tabToNext(t, w)
	typeText(t, w, "https://gateway.example/v1")
	tabToNext(t, w)
	if got := formRows[w.fieldIdx].key; got != "api_key" {
		t.Fatalf("第三格应是 api_key，实际 %q", got)
	}
	if got := w.rowLabel(w.fieldIdx); got != "api_key" {
		t.Errorf("行标签 = %q, want api_key", got)
	}

	// 明文 key 切到 $ENV：那串不是变量名，必须清空并说明原因（搬过去只会写出一个坏 env 名）。
	typeText(t, w, "sk-real-secret")
	w.Update(pressF2())
	if w.keyMode != keyFromEnv {
		t.Fatal("f2 没切到 env 模式")
	}
	if got := w.rowLabel(w.fieldIdx); got != "api_key_env" {
		t.Errorf("行标签 = %q, want api_key_env", got)
	}
	if inputOf(w, "api_key") != "" {
		t.Errorf("切到 env 后还留着明文串: %q", inputOf(w, "api_key"))
	}
	if w.note == "" {
		t.Error("清空了用户输入却没说明")
	}
	typeText(t, w, "GATEWAY_KEY")
	if d := w.collectDraft(); d.APIKey != "" || d.APIKeyEnv != "GATEWAY_KEY" {
		t.Errorf("collectDraft = %+v, want 只有 api_key_env", d)
	}

	// 反过来：合法的变量名原样搬回明文行（用户可能就是把 key 存在那个变量里）。
	w.Update(pressF2())
	if d := w.collectDraft(); d.APIKey != "GATEWAY_KEY" || d.APIKeyEnv != "" {
		t.Errorf("切回明文后 draft = %+v", d)
	}
}

// 摘要屏的每条 op 都必须带着它的 note：applyOps 失败时报的就是这条串，人话行不能替代它（AGENTS §11 坑 28）。
func TestWizardSummaryLinesCarryTheirNote(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	openForm(t, w)
	fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real", "gpt-test")
	w.Update(pressCtrl('s'))
	w.Update(pressKey('c'))
	w.Update(pressSpecial(tea.KeyDown))
	enterKey(t, w)
	w.Update(pressKey('c'))
	if w.step != stepSummary {
		t.Fatalf("step = %v, want summary", w.step)
	}
	if len(w.ops) == 0 {
		t.Fatal("夹具没排出任何 op")
	}

	c := setupPaletteColors()
	var rendered strings.Builder
	for _, l := range w.summaryLines(c) {
		rendered.WriteString(l.render(c, cardInner(90)) + "\n")
	}
	for _, op := range w.ops {
		if !strings.Contains(rendered.String(), op.note) {
			t.Errorf("摘要里没有 %q 这条 note:\n%s", op.note, rendered.String())
		}
	}
	// firstrun 的 note 只出现一次（五行共用一条），但必须出现。
	if n := strings.Count(rendered.String(), "write first-run choices"); n != 1 {
		t.Errorf("firstrun 的 note 出现 %d 次, want 1:\n%s", n, rendered.String())
	}
}

// lang 选"不注入"时 WriteFirstRun 根本不写那一行 —— 摘要必须说实话，不能显示成"已经归零"。
func TestWizardSummaryTellsUnsetLangStays(t *testing.T) {
	path := setupConfigWith(t, func(s string) string {
		return strings.Replace(s, `lang = ""`, `lang = "Chinese"`, 1)
	})
	w := newWizardAt(t, path)
	if w.snap.lang != "Chinese" {
		t.Fatalf("夹具没造出 lang=Chinese：snap.lang = %q", w.snap.lang)
	}
	w.step = stepFirstRun
	w.cursor = 0
	w.cycleField(-1) // Chinese → 不注入
	d := w.firstRunDraft()
	if d.Lang != "" {
		t.Fatalf("夹具应造出 Lang=\"\"，实际 %q", d.Lang)
	}
	w.ops = append(w.ops, pendingOp{kind: "firstrun", firstRun: d, note: "write first-run choices"})

	var joined string
	for _, l := range w.summaryLines(setupPaletteColors()) {
		joined += l.what + " " + l.detail + "\n"
	}
	if !strings.Contains(joined, "stays") || !strings.Contains(joined, "Chinese") {
		t.Errorf("lang 空值被显示成了一次改写:\n%s", joined)
	}
	if strings.Contains(joined, "Chinese → (no instruction)") {
		t.Error("摘要承诺了一次不会发生的改写")
	}
}

// 面板必须容得下所有动态文本：每行不超过终端宽度，整个面板不超过终端高度（高度这一维以前没测，长错误 + 满队列的摘要屏会溢出到 AltScreen 之外）。
func TestWizardRenderWidthMatrix(t *testing.T) {
	widths := []int{40, 50, 60, 72, 80, 100, 120, 160, 200, 240}
	heights := []int{18, 24, 40}
	steps := []setupStep{stepWelcome, stepProviders, stepProviderForm, stepProbe, stepDefault, stepFirstRun, stepSummary}

	for _, width := range widths {
		for _, height := range heights {
			for _, step := range steps {
				for _, subMode := range []string{"", "optional", "preset"} {
					if step != stepProviderForm && subMode != "" {
						continue
					}
					path := setupConfigWith(t, nil)
					w := newWizardAt(t, path)
					w.width, w.height = width, height
					w.step = step
					w.errMsg = "a fairly long validation message that must not push the frame past the terminal edge: " +
						strings.Repeat("detail ", 12)
					w.note = "wrote 3 change(s)"
					w.probeErr = "HTTP 401: invalid api key " + strings.Repeat("x", 120)
					// 摘要屏最坏情况：8 条 op，每条两行（人话 + note）。
					for i := 0; i < 8; i++ {
						w.ops = append(w.ops, pendingOp{kind: "default", name: "p" + strconv.Itoa(i),
							note: `use_provider = "p` + strconv.Itoa(i) + `"`})
					}
					if step == stepProviderForm {
						w.optionalOpen = subMode == "optional"
						w.presetOpen = subMode == "preset"
					}
					if _, cmd := w.Update(tea.WindowSizeMsg{Width: width, Height: height}); cmd != nil {
						t.Fatalf("width=%d height=%d step=%v: WindowSizeMsg 不该产出 cmd", width, height, step)
					}
					view := w.View().Content
					lines := strings.Split(view, "\n")
					if len(lines) > height {
						t.Fatalf("width=%d height=%d step=%v/%s 溢出高度：%d 行 > %d",
							width, height, step, subMode, len(lines), height)
					}
					for i, line := range lines {
						if lw := lipgloss.Width(line); lw > width {
							t.Fatalf("width=%d height=%d step=%v/%s 第 %d 行越界（%d 列）: %q",
								width, height, step, subMode, i, lw, line)
						}
					}
				}
			}
		}
	}
}

// 终端太小（任一向）时给一句人话，而不是破碎的面板。
func TestWizardNarrowTerminal(t *testing.T) {
	path := setupConfigWith(t, nil)
	for _, tc := range [][2]int{{20, 40}, {100, 6}} {
		w := newWizardAt(t, path)
		w.width, w.height = tc[0], tc[1]
		view := w.View().Content
		if !strings.Contains(view, "yagent setup: needs at least") {
			t.Errorf("尺寸 %dx%d 没给出人话提示: %q", tc[0], tc[1], view)
		}
		if strings.Contains(view, "╭") {
			t.Errorf("尺寸 %dx%d 还是画了个破碎的面板", tc[0], tc[1])
		}
	}
}

// 底栏键帽不是装饰：hints() 里写出来的每一个单键都必须真的能被该屏处理——键帽比裸串更容易被相信，"键帽上写着、按下没反应"是新的失效模式。
func TestWizardHintsAreNotDecoration(t *testing.T) {
	cases := []struct {
		step setupStep
		prep func(t *testing.T, w *setupWizard)
	}{
		{stepWelcome, func(t *testing.T, w *setupWizard) {}},
		{stepProviders, func(t *testing.T, w *setupWizard) {}},
		{stepProviderForm, func(t *testing.T, w *setupWizard) {}},
		{stepProbe, func(t *testing.T, w *setupWizard) {
			w.probeText = "ok"
			w.probeLatency = 12 * time.Millisecond
			w.probeTarget = "https://api.example.com/v1"
			w.probeModel = "some-model"
		}},
		{stepDefault, func(t *testing.T, w *setupWizard) {}},
		{stepFirstRun, func(t *testing.T, w *setupWizard) {}},
		{stepSummary, func(t *testing.T, w *setupWizard) {
			w.ops = append(w.ops, pendingOp{kind: "default", name: "aliyun", note: `use_provider = "aliyun"`})
			w.ops = append(w.ops, pendingOp{kind: "write",
				provider: config.ProviderDraft{Name: "broken"}, note: "write [global.llm.providers.broken]"})
		}},
	}

	for _, tc := range cases {
		path := setupConfigWith(t, nil)
		hint := func() string {
			w := newWizardAt(t, path)
			w.step = tc.step
			tc.prep(t, w)
			return w.hints()
		}()
		for _, key := range hintKeys(hint) {
			w := newWizardAt(t, path)
			w.step = tc.step
			tc.prep(t, w)
			before := wizardState(w)

			msg, ok := keyToMsg(key)
			if !ok {
				t.Errorf("%v: 提示里的键 %q 造不出按键消息", tc.step, key)
				continue
			}
			w.Update(msg)
			// 比较的是"这一键到底动没动状态"，不是它把状态带去哪儿。
			if wizardState(w) == before {
				t.Errorf("%v: 底栏写着 %q，按下却什么都没改（键帽成了装饰）", tc.step, key)
			}
		}
	}
}

// hintKeys 取提示里"可以真的按下"的单键；"1-9"/"j/k" 这类复合写法跳过。
func hintKeys(hints string) []string {
	var out []string
	for _, item := range strings.Split(hints, " · ") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, _, _ := strings.Cut(item, " ")
		if strings.ContainsAny(key, "/-") {
			continue
		}
		out = append(out, key)
	}
	return out
}

func keyToMsg(key string) (tea.Msg, bool) {
	switch key {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}, true
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}, true
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}, true
	case "f2":
		return pressF2(), true
	}
	if letter, ok := strings.CutPrefix(key, "ctrl+"); ok && len(letter) == 1 {
		return pressCtrl(rune(letter[0])), true
	}
	if len(key) == 1 {
		return pressKey(rune(key[0])), true
	}
	return nil, false
}

// 测试要自己喂一份最小目录：生产里 main.go:160 在任何子命令之前就把内嵌目录装好，向导的目录预填不是"只在 TUI 里才生效"的东西。
func loadTestCatalog(t *testing.T, content string) {
	t.Helper()
	fsys := fstest.MapFS{models.CatalogPath: &fstest.MapFile{Data: []byte(content)}}
	if err := models.Load(fsys); err != nil {
		t.Fatalf("models.Load() error = %v", err)
	}
	t.Cleanup(func() { _ = models.Load(fstest.MapFS{models.CatalogPath: &fstest.MapFile{Data: []byte("{}")}}) })
}

// wizardState 是"这一键到底动没动东西"的快照。刻意不含时间与颜色。
func wizardState(w *setupWizard) string {
	var b strings.Builder
	b.WriteString(strconv.Itoa(int(w.step)))
	b.WriteString(strconv.FormatBool(w.done))
	b.WriteString(strconv.FormatBool(w.saved))
	b.WriteString(strconv.FormatBool(w.probing))
	b.WriteString(strconv.FormatBool(w.presetOpen))
	b.WriteString(strconv.FormatBool(w.optionalOpen))
	b.WriteString(w.errMsg + "|" + w.note + "|" + w.probeErr + "|" + w.probeText)
	b.WriteString(strconv.Itoa(w.cursor) + strconv.Itoa(w.fieldIdx) + strconv.Itoa(w.keyMode))
	b.WriteString(strconv.Itoa(w.summaryTop) + strconv.Itoa(len(w.ops)))
	for _, op := range w.ops {
		b.WriteString(op.kind + op.name)
	}
	for _, in := range w.inputs {
		b.WriteString(in.Value())
	}
	return b.String()
}

// 探测期间必须有 tick，结果一到就必须停：空转的 spinner 比没有 spinner 更像卡死，而静止的等待屏等于骗用户"没在工作"。
func TestProbeSpinnerTicksWhileProbing(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	enterKey(t, w)
	rows := w.effectiveProviders()

	// 缺 model/key 的草稿不能进入探测态（否则 tick 永动）。
	w.step = stepProbe
	if cmd := w.startProbeCmd(config.ProviderDraft{}); cmd != nil {
		t.Error("校验失败却产出了 cmd —— 这一屏没有往返可等")
	}
	if w.probing {
		t.Fatal("校验失败的探测仍被标成进行中")
	}

	cmd := w.startProbeCmd(rows[0].draft)
	if cmd == nil {
		t.Fatal("startProbeCmd 没产出 cmd（探测请求或 tick 断了）")
	}
	if !w.probing {
		t.Fatal("probing 没被置真")
	}
	_, tickCmd := w.Update(probeTickMsg{})
	if tickCmd == nil {
		t.Error("探测中的 tick 没有续上 —— 界面会停在同一帧")
	}
	frame := w.spinFrame
	if frame == 0 {
		t.Error("一个 tick 都没推进")
	}
	w.Update(probeResultMsg{text: "ok", latency: 12 * time.Millisecond})
	if w.probing {
		t.Fatal("结果已到，probing 还是 true")
	}
	if _, cmd := w.Update(probeTickMsg{}); cmd != nil {
		t.Error("探测结束后还在发 tick —— spinner 空转")
	}
	if w.spinFrame != frame {
		t.Errorf("结束后的 tick 仍在推进帧号（%d → %d）", frame, w.spinFrame)
	}
}

// 探测原文不许被"美化"成一句概括，但 401/404/timeout 要给出下一步。
func TestProbeAdviceOnlyForKnownFailures(t *testing.T) {
	cases := []struct {
		errText string
		want    string
	}{
		{"HTTP 401 Unauthorized: invalid api key", "key"},
		{"HTTP 404: Not Found", "base_url"},
		{"context deadline exceeded", "time"},
		{"dial tcp: lookup api.x: no such host", "host"},
	}
	for _, tc := range cases {
		if got := probeAdvice(tc.errText); !strings.Contains(got, tc.want) {
			t.Errorf("probeAdvice(%q) = %q, 期望包含 %q", tc.errText, got, tc.want)
		}
	}
	if got := probeAdvice("something unprecedented happened"); got != "" {
		t.Errorf("认不出来的错误给出了建议: %q", got)
	}

	// 原文逐字进面板：状态码就是用户接下来要搜的东西。
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	w.step = stepProbe
	w.probeErr = "HTTP 401: invalid api key"
	if view := w.View().Content; !strings.Contains(view, "HTTP 401: invalid api key") {
		t.Errorf("探测失败原文被改写了:\n%s", view)
	}
	// base_url 的 path 与 token 不摊在屏幕上。
	if got := probeHost("https://api.example.com/v1?token=secret123"); got != "api.example.com" {
		t.Errorf("probeHost = %q, want api.example.com", got)
	}
}

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

// 向导打开期间 taskCompleteMsg 必须透传 —— 它是 taskRunning 唯一的清零点，吞掉的表现是界面永久卡在"运行中"（AGENTS.md §11 坑 6）。
func TestSetupModePassesTaskCompleteThrough(t *testing.T) {
	// 必须用 chromeTestModel：handleTaskCompleteMsg 会清输入框（textarea），最小 fixture 里没有它，测到的就是 fixture 而不是这段透传。
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

func cmdPtr(c tea.Cmd) uintptr {
	if c == nil {
		return 0
	}
	return reflect.ValueOf(c).Pointer()
}

// 独立跑 `yagent setup` 时 q / ctrl+c / 写盘成功都必须真的发 tea.Quit：handleKey 只置 done=true 的话，bubbletea 不认，进程停在最后一屏——用户看到的是"按 q 没反应"。
func TestStandaloneWizardQuitsTheProgram(t *testing.T) {
	path := setupConfigWith(t, nil)

	for _, tc := range []struct {
		name string
		msg  tea.KeyPressMsg
	}{
		{"q", pressKey('q')},
		{"ctrl+c", pressCtrl('c')},
		{"esc", pressSpecial(tea.KeyEscape)},
	} {
		w := newWizardAt(t, path)
		w.standalone = true
		_, cmd := w.Update(tc.msg)
		if cmd == nil {
			t.Fatalf("%s: 独立向导没产出 cmd —— 程序不会退出", tc.name)
		}
		if cmdPtr(cmd) != cmdPtr(tea.Quit) {
			t.Errorf("%s: 独立向导产出的不是 tea.Quit（%v）", tc.name, cmdPtr(cmd))
		}
		if !w.Done() {
			t.Errorf("%s: 没置 done，RunSetup 的 saved 判定拿不到终态", tc.name)
		}
	}

	// 写盘成功那条路径同样必须退：停在摘要屏等 enter 的人是"配完了要走"。
	w := newWizardAt(t, path)
	w.standalone = true
	openForm(t, w)
	fillRequired(t, w, "gateway", "https://gateway.example/v1", "sk-real", "gpt-test")
	w.Update(pressCtrl('s'))
	w.Update(pressKey('c'))
	enterKey(t, w)
	w.Update(pressKey('c'))
	_, cmd := w.Update(pressSpecial(tea.KeyEnter))
	if cmdPtr(cmd) != cmdPtr(tea.Quit) {
		t.Errorf("落盘后没产出 tea.Quit —— 摘要屏会卡住进程: %v", cmdPtr(cmd))
	}
	if !w.Saved() {
		t.Error("夹具这条路本该已落盘")
	}
}

// 嵌在主 TUI 里时正相反：done 只意味着交还键盘，把 quit 漏进这条路径 = /setup 里按 q 直接把 yagent 退掉。
func TestEmbeddedWizardNeverQuitsTheHost(t *testing.T) {
	path := setupConfigWith(t, nil)
	m := newTestModel()
	if m.eventCh == nil {
		m.eventCh = make(chan *messaging.MessageEvent, 1)
	}
	w := newWizardAt(t, path)
	m.wizard = w
	m.setupActive = true

	_, cmd := m.Update(pressKey('q'))
	if cmd != nil && cmdPtr(cmd) == cmdPtr(tea.Quit) {
		t.Fatal("嵌入式向导把 q 翻成了 tea.Quit —— 那会退掉整个 yagent")
	}
	if m.setupActive {
		t.Error("向导结束后还占着键盘")
	}
}

// 欢迎屏上按 a 是最常见的一种来意（再来加一个 provider），只认 enter 会让人以为界面死了。
func TestWizardWelcomeAcceptsAdd(t *testing.T) {
	path := setupConfigWith(t, nil)
	w := newWizardAt(t, path)
	// 提示行必须把这条路写出来，否则键帽上的承诺还是只有 enter。
	if !strings.Contains(w.hints(), "a add provider") {
		t.Errorf("欢迎屏没把 a 列进底栏: %q", w.hints())
	}
	w.Update(pressKey('a'))
	if w.step != stepProviderForm {
		t.Errorf("welcome 上按 a 之后 step = %v, want providerForm", w.step)
	}
	if w.editing != "" {
		t.Errorf("从欢迎屏直接加的那条应是新块，editing = %q", w.editing)
	}
}

// 端到端跑一次真程序：上面的单元测试只断 cmd 形状，这条断"进程真的会结束"——用户报的就是按 q 和 ctrl+c 都不能退出。
func TestRunSetupProgramActuallyExits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"q", "q"},
		{"ctrl+c", "\x03"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := setupConfigWith(t, nil)
			w := newSetupWizard(path, config.SetupPlaceholderKey, "")
			w.standalone = true
			w.width, w.height = 80, 24

			p := tea.NewProgram(w,
				tea.WithInput(strings.NewReader(tc.input)),
				tea.WithOutput(io.Discard),
				tea.WithoutRenderer())
			type result struct {
				model tea.Model
				err   error
			}
			done := make(chan result, 1)
			go func() {
				m, err := p.Run()
				done <- result{m, err}
			}()
			select {
			case res := <-done:
				if res.err != nil {
					t.Fatalf("Run 报错: %v", res.err)
				}
				final, ok := res.model.(*setupWizard)
				if !ok {
					t.Fatalf("终态不是向导: %T", res.model)
				}
				if !final.Done() {
					t.Errorf("%s 之后程序退出了，但 done 仍是 false", tc.name)
				}
				if final.Saved() {
					t.Errorf("%s 不该落盘", tc.name)
				}
			case <-time.After(5 * time.Second):
				p.Kill()
				t.Fatalf("按 %s 之后程序没退出（这就是用户报的洞）", tc.name)
			}
		})
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
