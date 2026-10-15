package tui

// tui_model_switch_test.go — 任务运行中换模型（TUI 侧接线）。
//
// 换模型改的是 llm.Client 的运行时状态，正在飞的那次调用不受影响，所以"运行中
// 不许换"这条硬拒绝没有必要；反过来，弹窗打开时如果吞掉 taskCompleteMsg，
// taskRunning 就永久卡在 true —— 那才是真会锁死界面的地方。

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"yagent/internal/app"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/tui/components"
)

// pickerTestModel 只给一个装着配置的 llm.Client：选择器读的是 Config 与覆盖表，
// 不需要走 app.Init 的完整装配。默认 provider 是 beta，可选列表首项是 alpha，
// 于是"换成 alpha"是一条真的换了模型的断言。
func pickerTestModel(t *testing.T) *model {
	t.Helper()
	cfg := &config.Config{Global: config.TopLevelConfig{LLM: &config.GlobalLLMConfig{
		UseProvider: "beta",
		Providers: map[string]config.ProviderConfig{
			"alpha": {Model: "alpha-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
			"beta":  {Model: "beta-model", APIBaseURL: "http://127.0.0.1:1/v1", APIKey: "k"},
		},
	}}}
	assistant, err := app.NewYagent(&llm.Client{Config: cfg})
	if err != nil {
		t.Fatalf("NewYagent: %v", err)
	}
	m := newTestModel()
	m.assistant = assistant
	return m
}

func hasStatusLine(m *model, substr string) bool {
	for _, le := range m.logEntries {
		if le.eventType == "status" && strings.Contains(le.content, substr) {
			return true
		}
	}
	return false
}

func pressEnter() tea.KeyMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

func TestModelDialogOpensWhileTaskRunning(t *testing.T) {
	running := pickerTestModel(t)
	running.taskRunning = true
	if cmd := running.processCommand(":model"); cmd != nil {
		t.Errorf(":model 返回了 cmd %v，期望 nil（它只弹窗，不发起异步动作）", cmd)
	}
	if _, ok := running.dialogStack.Top().(*components.AgentSelectDialog); !ok {
		t.Fatalf("运行中 :model 没有打开目标选择器，栈顶 = %T", running.dialogStack.Top())
	}
	if !hasStatusLine(running, "Task running") {
		t.Error("运行中打开选择器没有说明生效时机")
	}

	idle := pickerTestModel(t)
	if cmd := idle.processCommand(":model"); cmd != nil {
		t.Errorf("空闲时 :model 返回了 cmd %v", cmd)
	}
	if _, ok := idle.dialogStack.Top().(*components.AgentSelectDialog); !ok {
		t.Fatalf("空闲时 :model 没有打开选择器，栈顶 = %T", idle.dialogStack.Top())
	}
	if hasStatusLine(idle, "Task running") {
		t.Error("空闲时也播运行中的提示，这条文案就变成噪音了")
	}
}

// 运行中换全局模型：确认后 Client 的默认引擎必须立刻换掉（任务线程下一刻就会经
// DefaultEngine 读到它），并且各目标的当前 provider 要跟着改口径。
func TestGlobalProviderSwitchWhileTaskRunning(t *testing.T) {
	m := pickerTestModel(t)
	m.taskRunning = true
	m.processCommand(":model")

	if _, ok := m.dialogStack.Top().(*components.AgentSelectDialog); !ok {
		t.Fatalf("第一层不是目标选择器: %T", m.dialogStack.Top())
	}
	if _, _, handled := m.handleDialogStackKey(pressEnter()); !handled {
		t.Fatal("目标选择器的 Enter 没被对话框层消费")
	}
	if _, ok := m.dialogStack.Top().(*components.ModelSelectDialog); !ok {
		t.Fatalf("选完目标没有打开 provider 选择器，栈顶 = %T", m.dialogStack.Top())
	}
	if _, _, handled := m.handleDialogStackKey(pressEnter()); !handled {
		t.Fatal("provider 选择器的 Enter 没被对话框层消费")
	}

	if m.currentProvider != "alpha" {
		t.Errorf("currentProvider = %q, want alpha", m.currentProvider)
	}
	// 页眉与回显里的模型名要说当前真正生效的那一个。
	if m.currentModel != "alpha-model" {
		t.Errorf("currentModel = %q, want alpha-model", m.currentModel)
	}
	if eng := m.assistant.GetClient().DefaultEngine(); eng == nil || eng.Model() != "alpha-model" {
		t.Errorf("DefaultEngine() = %v, want the alpha-model engine", eng)
	}
	if m.dialogStack.Len() != 0 {
		t.Errorf("确认后对话框没有收起，剩余 %d 层", m.dialogStack.Len())
	}
	if !hasStatusLine(m, "Switched global provider to: alpha") {
		t.Error("切换成功但没有回显结果")
	}
}

// 运行中换某个 Agent 的模型：写的是 Client 的运行时覆盖表（有锁保护），
// 下一次委派/下一步解析引擎时生效。
func TestAgentProviderOverrideWhileTaskRunning(t *testing.T) {
	m := pickerTestModel(t)
	m.taskRunning = true
	m.processCommand(":model")

	// 条目顺序是 global 之后紧跟 showModelSelectionDialog 里的 validAgents，首项 director。
	if _, _, handled := m.handleDialogStackKey(tea.KeyPressMsg{Code: 'j', Text: "j"}); !handled {
		t.Fatal("目标选择器的 j 没被对话框层消费")
	}
	if _, _, handled := m.handleDialogStackKey(pressEnter()); !handled {
		t.Fatal("目标选择器的 Enter 没被对话框层消费")
	}
	if got := m.pendingModelTarget; got != "director" {
		t.Fatalf("选中的目标是 %q, want director（条目顺序变了，这条测试要跟着改）", got)
	}
	if _, ok := m.dialogStack.Top().(*components.ModelSelectDialog); !ok {
		t.Fatalf("没有打开 provider 选择器，栈顶 = %T", m.dialogStack.Top())
	}
	// provider 列表按名字排序，游标初始停在当前生效的 beta 上，所以用数字键直选 alpha。
	if _, _, handled := m.handleDialogStackKey(tea.KeyPressMsg{Code: '1', Text: "1"}); !handled {
		t.Fatal("provider 选择器的数字键没被对话框层消费")
	}

	prov, modelName := m.assistant.GetAgentProvider("director")
	if prov != "alpha" || modelName != "alpha-model" {
		t.Errorf("GetAgentProvider(director) = %q/%q, want alpha/alpha-model", prov, modelName)
	}
	// global 那条路才动 currentProvider；按 agent 覆盖不能顺手改全局。
	if m.currentProvider != "" {
		t.Errorf("按 agent 覆盖把全局 provider 也改了: %q", m.currentProvider)
	}
	if !hasStatusLine(m, "Set agent 'director' provider to: alpha") {
		t.Error("切换成功但没有回显结果")
	}
}

// 弹窗打开时任务完成：taskCompleteMsg 是 taskRunning 唯一的清零点，被弹窗守卫
// 吞掉的表现是界面永久卡在"运行中"，且最终答案不落地。
func TestTaskCompleteMsgNotSwallowedBehindDialog(t *testing.T) {
	m := chromeTestModel(t, 120, 40)
	m.taskRunning = true
	dialog := components.NewAgentSelectDialog(nil)
	dialog.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(dialog)

	updated, _ := m.Update(taskCompleteMsg{taskID: "t1", result: "done"})
	m = updated.(*model)

	if m.taskRunning {
		t.Fatal("弹窗期间 taskCompleteMsg 被丢弃，taskRunning 卡在 true")
	}
	if got := m.dialogStack.Top().ID(); got != "task_complete" {
		t.Errorf("完成提示没有压在选择器之上，栈顶 = %q", got)
	}
	if m.dialogStack.Len() != 2 {
		t.Errorf("对话框层数 = %d, want 2（选择器仍在，完成提示在其上）", m.dialogStack.Len())
	}
}
