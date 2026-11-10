package tui

import (
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/plasma"
	"yagent/internal/tui/components"
)

// 本文件钉住 `/plasma` 的三条命令与它们的输出通道。
//
// 目录全部经 `[plasma]` 配置解析（默认 `~/.yagent/agents`），所以隔离只能靠 HOME ——
// 扫目录的用例必须**拥有**那个目录（根 AGENTS.md §11 坑 23），否则在开发机上跑一次
// 就会读到真实的 store。

// isolatePlasmaHome 把 HOME 指到临时目录，并返回该目录下的 store 根。
// 返回值直接问 config（而不是自己拼 `~/.yagent/agents`）：路径来源只有一份，
// 测试若自己拼一份，生产那边改默认值时它会静默测另一个目录。
func isolatePlasmaHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	store := config.PlasmaStoreDir()
	if !strings.HasPrefix(store, home) {
		// 进程里有人把 [plasma] store 写成了绝对路径：那会让用例写到真实目录，
		// 必须当场失败而不是"跑过了"。
		t.Fatalf("store 根 %q 不在临时 HOME（%s）下，用例会写到真实目录", store, home)
	}
	return store
}

// seedPlasmaAgent 用生产的落盘路径造一份 Agent（不手写 JSON：手写的夹具必然与
// Store.Put 的形态漂移，而漂移的症状是"面板读出来的字段全是空的"）。
func seedPlasmaAgent(t *testing.T, name string, grade plasma.Grade, runs int, diags []plasma.Diagnostic) string {
	t.Helper()
	store := plasma.NewStore(config.PlasmaStoreDir())
	b := &plasma.Bundle{SchemaVersion: plasma.SchemaVersion, Name: name}
	report := &plasma.Report{
		Grade:       grade,
		Diagnostics: diags,
		Outcomes: plasma.Outcomes{
			{FixtureID: "structural.exit", Passed: true, Steps: 2},
			{FixtureID: "structural.step_budget", Passed: false, Steps: 9, Reason: "步数预算未生效"},
		},
	}
	hash, err := store.Put(b, report)
	if err != nil {
		t.Fatalf("Put(%s): %v", name, err)
	}
	for i := 0; i < runs; i++ {
		if err := store.RecordRun(name, hash, true); err != nil {
			t.Fatalf("RecordRun: %v", err)
		}
	}
	return hash
}

// topTextPanel 取栈顶面板并断言它是 TextPanel（类型断言失败 = 命令开错了面板）。
func topTextPanel(t *testing.T, m *model, id string) *components.TextPanel {
	t.Helper()
	if m.dialogStack.Len() != 1 {
		t.Fatalf("面板层数 = %d, want 1", m.dialogStack.Len())
	}
	top := m.dialogStack.Top()
	if top.ID() != id {
		t.Fatalf("栈顶面板 = %q, want %q", top.ID(), id)
	}
	panel, ok := top.(*components.TextPanel)
	if !ok {
		t.Fatalf("栈顶不是文本面板：%T", top)
	}
	return panel
}

// panelContains 在面板各行里找子串（面板是逐行文本，不是一整段）。
func panelContains(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// 空 store 不是错误：输出里必须有"尚无 plasma agent"这一行，且命令正常返回。
func TestPlasmaCommandListEmptyStore(t *testing.T) {
	isolatePlasmaHome(t)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma list")

	panel := topTextPanel(t, m, plasmaPanelID)
	if !panelContains(panel.Lines(), "尚无 plasma agent") {
		t.Errorf("空 store 的面板 = %v，want 含「尚无 plasma agent」", panel.Lines())
	}
	if m.infoMsg != "" {
		t.Errorf("空 store 报了错：infoMsg = %q", m.infoMsg)
	}
}

// `/plasma` 与 `/plasma help` 必须打开帮助面板（而不是被当成搜索词）。
func TestPlasmaCommandHelpAndUnknown(t *testing.T) {
	isolatePlasmaHome(t)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma")
	panel := topTextPanel(t, m, plasmaPanelID)
	for _, want := range []string{"/plasma list", "/plasma explain", "/plasma promote"} {
		if !panelContains(panel.Lines(), want) {
			t.Errorf("帮助里没有 %q：%v", want, panel.Lines())
		}
	}
	m.dialogStack.Pop()

	m.processCommand("/plasma bogus")
	if m.dialogStack.Len() != 0 {
		t.Error("未知子命令开了面板")
	}
	if !strings.Contains(m.infoMsg, "未知子命令") {
		t.Errorf("infoMsg = %q，want 可读提示", m.infoMsg)
	}

	// 缺参数同样只提示，不 panic、不退出。
	m.processCommand("/plasma explain")
	if !strings.Contains(m.infoMsg, "用法") {
		t.Errorf("缺参数 infoMsg = %q，want 用法提示", m.infoMsg)
	}
}

// `/plasma explain <name>` 必须把 report.json 的诊断与失败夹具写出来。
func TestPlasmaCommandExplainShowsDiagnostics(t *testing.T) {
	isolatePlasmaHome(t)
	seedPlasmaAgent(t, "auditor", plasma.GradeVerified, 2, []plasma.Diagnostic{{
		Code: plasma.CodeFixtureExit, Severity: plasma.SeverityError,
		Path: "fixtures.structural[0]", Message: "exit 契约违反", Note: "交付为空",
	}})
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma explain auditor")

	panel := topTextPanel(t, m, plasmaPanelID)
	lines := panel.Lines()
	if !panelContains(lines, plasma.CodeFixtureExit) {
		t.Errorf("面板里没有诊断码 %s：%v", plasma.CodeFixtureExit, lines)
	}
	if !panelContains(lines, "reason: 步数预算未生效") {
		// 失败夹具的具体原因必须可见（一句 FAIL 说明不了该改哪里）。
		t.Errorf("面板里没有失败夹具的原因：%v", lines)
	}
	if !panelContains(lines, "失败 1 条") {
		t.Errorf("面板里没有失败计数：%v", lines)
	}
}

// 没有 report.json 时要说"没有快照"，不能说得像"编译没发射诊断"。
func TestPlasmaCommandExplainWithoutReport(t *testing.T) {
	isolatePlasmaHome(t)
	store := plasma.NewStore(config.PlasmaStoreDir())
	b := &plasma.Bundle{SchemaVersion: plasma.SchemaVersion, Name: "bare"}
	if _, err := store.Put(b, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma explain bare")

	if !panelContains(topTextPanel(t, m, plasmaPanelID).Lines(), "report.json: 无") {
		t.Error("缺 report.json 时没有如实说明")
	}
}

// 判断不通过的 promote 必须**不动盘**：一次静默的指针切换比拒绝更危险。
func TestPlasmaCommandPromoteRefusesWithoutRuns(t *testing.T) {
	isolatePlasmaHome(t)
	hash := seedPlasmaAgent(t, "coding", plasma.GradeVerified, 0, nil)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma promote coding")

	line := ""
	for _, e := range m.logEntries {
		if strings.Contains(e.content, "未升档") {
			line = e.content
		}
	}
	if line == "" {
		t.Fatalf("没有留下未升档的账目：%+v", m.logEntries)
	}
	if !strings.Contains(line, "runs 不足") {
		t.Errorf("未升档的账目 = %q，want 含「runs 不足」", line)
	}
	// 磁盘状态：grade 未变、指针未动。
	_, meta, err := plasma.NewStore(config.PlasmaStoreDir()).Load("coding", hash)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta.Grade != plasma.GradeVerified {
		t.Errorf("被拒的 promote 改了 grade：%q", meta.Grade)
	}
	if current, _ := plasma.NewStore(config.PlasmaStoreDir()).Current("coding"); current != hash {
		t.Errorf("被拒的 promote 动了 index：%q, want %q", current, hash)
	}
}

// 够条件时升档并把指针指过去（回滚/首次 promote 的同一条路径）。
func TestPlasmaCommandPromoteSucceeds(t *testing.T) {
	isolatePlasmaHome(t)
	seedPlasmaAgent(t, "coding", plasma.GradeVerified, 5, nil)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasma promote coding")

	_, meta, err := plasma.NewStore(config.PlasmaStoreDir()).Load("coding", func() string {
		h, _ := plasma.NewStore(config.PlasmaStoreDir()).Current("coding")
		return h
	}())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if meta.Grade != plasma.GradePromoted {
		t.Errorf("升档后 grade = %q, want promoted", meta.Grade)
	}
}

// `/plasmafoo` 必须继续回落到搜索：命令认的是首个 token **精确**命中。
func TestPlasmaCommandDoesNotEatSearch(t *testing.T) {
	isolatePlasmaHome(t)
	m := chromeTestModel(t, 120, 40)

	m.processCommand("/plasmafoo bar")

	if m.dialogStack.Len() != 0 {
		t.Error("/plasmafoo 开了 plasma 面板（命令匹配写宽了）")
	}
	if !strings.Contains(m.searchPattern, "plasmafoo") {
		t.Errorf("searchPattern = %q, want 回落成搜索", m.searchPattern)
	}
}
