package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/app"
	"yagent/internal/http"
	"yagent/internal/skills"
)

// assistantWithSkills 造一个只装了技能注册表的 Yagent（零值足够：
// TUI 的技能路径只问 Skills()/MCPServers()）。
func assistantWithSkills(t *testing.T, dirs ...string) *app.Yagent {
	t.Helper()
	registry, _ := skills.Load(dirs)
	if registry.Count() == 0 {
		t.Fatal("夹具失效：注册表里没有技能")
	}
	y := &app.Yagent{}
	y.SetSkillsForTesting(registry)
	return y
}

func writeSkillFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}

func skillDirWith(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	full := filepath.Join(dir, name+".md")
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	return dir
}

// 手打 `/name args` + Enter 直接执行技能：今天的实现要求先让补全弹出来再按 Tab。
// 断言落在提交的任务正文上（eventType=user_message），而不是"没报错"。
func TestTUI_SlashSkillRunsFromTypedText(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "demo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", "SKILL.md"),
		[]byte("---\n{\"description\":\"演示\"}\n---\n第一步：读懂输入\n第二步：动手"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	m := chromeTestModel(t, 100, 30)
	m.projectDir = t.TempDir()
	m.assistant = assistantWithSkills(t, dir)
	// submitTaskWithContent 会 AddTask；零值 model 的 taskManager 是 nil（测试不走真实任务）。
	m.taskManager = http.NewTaskManager(nil, time.Minute)

	m.processCommand("/demo 看看这个 diff")

	if !m.taskRunning {
		t.Fatal("斜杠技能没有提交任务")
	}
	last := ""
	for _, e := range m.logEntries {
		if e.eventType == "user_message" {
			last = e.content
		}
	}
	if !strings.Contains(last, "第一步：读懂输入") {
		t.Errorf("任务正文 = %q, want 技能正文", last)
	}
	if !strings.Contains(last, "看看这个 diff") {
		t.Errorf("任务正文 = %q, want 带上参数", last)
	}
	if strings.Contains(last, "\"description\"") || strings.Contains(last, "---\n{") {
		t.Errorf("任务正文 = %q, want 不含 front-matter", last)
	}
}

// 首 token 不是已加载技能时照旧搜索（回归保护：别把 / 前缀的搜索语义吃掉）。
func TestTUI_SlashUnknownFallsBackToSearch(t *testing.T) {
	dir := skillDirWith(t, "demo", "# Demo\nbody")
	m := chromeTestModel(t, 100, 30)
	m.projectDir = t.TempDir()
	m.assistant = assistantWithSkills(t, dir)

	m.processCommand("/notaskill foo")

	if m.taskRunning {
		t.Fatal("未知技能名被当成技能执行了")
	}
	if !strings.Contains(m.searchPattern, "notaskill") {
		t.Errorf("searchPattern = %q, want 回落成搜索", m.searchPattern)
	}
}

// 内置命令与技能撞名时内置命令胜，且只 warn 一次（补全每敲一个字都会重建）。
func TestCompletionItemsAreKinded(t *testing.T) {
	dir := skillDirWith(t, "history", "# History\n用户自写的同名技能")
	writeSkillFile(t, dir, "demo", "# Demo\nbody")
	m := chromeTestModel(t, 100, 30)
	m.assistant = assistantWithSkills(t, dir)

	warned := map[string]bool{}
	items := skillCompletionItems(m.assistant.Skills(), "hist", warned)

	found := 0
	for _, item := range items {
		if item.name != "history" {
			continue
		}
		found++
		if item.kind != completionCmd {
			t.Errorf("history 的 kind = %q, want cmd（内置命令胜）", item.kind)
		}
	}
	if found != 1 {
		t.Fatalf("history 出现 %d 次, want 1", found)
	}
	if !warned["history"] {
		t.Error("撞名没有 warn")
	}

	// 技能行必须带 kind 与描述：界面靠它们分栏。
	items = skillCompletionItems(m.assistant.Skills(), "", warned)
	seenSkill := false
	for _, item := range items {
		if item.name == "history" {
			if item.kind != completionCmd {
				t.Errorf("空查询时 history 也必须是 cmd（内置胜）")
			}
			continue
		}
		if item.kind == completionSkill && item.name != "" {
			seenSkill = true
		}
	}
	if !seenSkill {
		t.Errorf("补全列表里没有技能行：%+v", items)
	}
}

// 拆分规则：只有首个 token 精确命中才算技能，参数是其余文本。
func TestSplitSlashSkill(t *testing.T) {
	cases := []struct {
		in, name, args string
	}{
		{"/demo", "demo", ""},
		{"/demo 看看这个 diff", "demo", "看看这个 diff"},
		{"  /demo   a  b  ", "demo", "a  b"},
		{"/", "", ""},
		{"demo", "demo", ""},
	}
	for _, tc := range cases {
		name, args := splitSlashSkill(tc.in)
		if name != tc.name || args != tc.args {
			t.Errorf("splitSlashSkill(%q) = (%q,%q), want (%q,%q)", tc.in, name, args, tc.name, tc.args)
		}
	}
}

// /skills 与 /mcp 必须显式列进斜杠分支：不列的话它们是"搜字符串 skills"，
// 界面上什么都不会发生（根 AGENTS.md §11 坑 17 的同族）。
func TestSlashSkillsAndMCPOpenDialogs(t *testing.T) {
	m := chromeTestModel(t, 100, 30)
	m.assistant = &app.Yagent{}

	m.processCommand("/skills")
	if m.dialogStack.Len() != 1 || m.dialogStack.Top().ID() != "skills_dialog" {
		t.Fatalf("/skills 没有打开面板（%d 层，栈顶 %v）", m.dialogStack.Len(), m.dialogStack.Top())
	}
	m.dialogStack.Pop()

	m.processCommand("/mcp")
	if m.dialogStack.Len() != 1 || m.dialogStack.Top().ID() != "mcp_dialog" {
		t.Fatalf("/mcp 没有打开面板（%d 层，栈顶 %v）", m.dialogStack.Len(), m.dialogStack.Top())
	}
}

// 信息面板打开时 taskCompleteMsg 必须照常透传（TUI 硬约束 2）：
// 被吞掉的表现是界面永久"运行中"且最终答案不落地。
func TestTaskCompletePassesThroughInfoDialogs(t *testing.T) {
	for _, open := range []func(*model){
		func(m *model) { m.processCommand("/skills") },
		func(m *model) { m.processCommand("/mcp") },
	} {
		m := chromeTestModel(t, 100, 40)
		m.assistant = &app.Yagent{}
		m.taskRunning = true
		open(m)

		updated, _ := m.Update(taskCompleteMsg{taskID: "t1", result: "done"})
		m = updated.(*model)
		if m.taskRunning {
			t.Fatal("信息面板期间 taskCompleteMsg 被丢弃，taskRunning 卡在 true")
		}
	}
}
