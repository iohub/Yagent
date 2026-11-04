package tui

import (
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/protocol"
)

// 提交之后输入框必须是空的。
//
// 今天只有追问那条路清了：首任务提交后文本一直留在 textarea 里（任务跑完才由
// handleTaskCompleteMsg 抹掉）。跑任务期间来了求助更糟 —— 那份已经提交过的文本会被
// 当成 preDraft 存着，答完之后由 respondToAsk 原样塞回输入框
// （ask_keys.go:388），症状就是"我刚发出去的话又回到框里了"。

func submitTestModel(t *testing.T) *model {
	t.Helper()
	m := chromeTestModel(t, 100, 30)
	m.projectDir = t.TempDir()
	// submitTaskWithContent 会 AddTask；零值 model 的 taskManager 是 nil。
	m.taskManager = http.NewTaskManager(nil, time.Minute)
	return m
}

func lastUserMessage(m *model) string {
	out := ""
	for _, e := range m.logEntries {
		if e.eventType == "user_message" {
			out = e.content
		}
	}
	return out
}

func TestSubmit_ClearsInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		followUp bool
	}{
		{name: "首任务"},
		{name: "追问", followUp: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := submitTestModel(t)
			if tc.followUp {
				m.currentTask = &http.Task{ID: "t1", ProjectDir: m.projectDir}
			}
			m.input.SetValue("  把这条改完  ")

			if cmd := m.submitFromEditor(); cmd == nil {
				t.Fatal("submitFromEditor 没有返回任何 cmd，任务没提交")
			}
			if m.input.Value() != "" {
				t.Errorf("提交后输入框还剩 %q", m.input.Value())
			}
			// 清空不能把载荷一起吃掉：transcript 里必须躺着去掉首尾空白的原文。
			if got := lastUserMessage(m); got != "把这条改完" {
				t.Errorf("transcript 里的任务正文 = %q, want %q", got, "把这条改完")
			}
			if !m.taskRunning {
				t.Error("任务没有进入运行态")
			}
		})
	}
}

// TestSubmit_FailedValidationKeepsText 校验不过时文本必须留着 —— 用户还得改一改再发。
func TestSubmit_FailedValidationKeepsText(t *testing.T) {
	m := submitTestModel(t)
	m.input.SetValue("   ")
	if cmd := m.submitFromEditor(); cmd != nil {
		t.Error("空任务不该提交")
	}
	if m.taskRunning {
		t.Error("空任务把运行态点亮了")
	}
	if m.input.Value() != "   " {
		t.Errorf("校验失败后输入框被改了: %q", m.input.Value())
	}
}

// TestTaskComplete_KeepsTypedDraft 运行期间打的草稿属于用户：清空是**提交**那一步的事，
// 收尾再抹一遍就是每次任务结束白打一段（输入框在运行中照样能打字，用来起草下一条）。
func TestTaskComplete_KeepsTypedDraft(t *testing.T) {
	m := submitTestModel(t)
	m.taskRunning = true
	m.input.SetValue("下一条准备发的草稿")

	if _, cmd := m.Update(taskCompleteMsg{taskID: "t1", result: "done"}); cmd != nil {
		t.Logf("taskCompleteMsg 返回了 cmd（非预期但不致命），继续断言草稿")
	}
	if m.input.Value() != "下一条准备发的草稿" {
		t.Errorf("收尾把草稿抹了: %q", m.input.Value())
	}
}

// TestAskAnswer_KeepsSubmitOutOfWork 求助把输入框借去答题，答完还原的是**用户的草稿**：
// 提交时没清空的话，还原出来的就是刚发出去的那条任务原文 —— 症状是"我提交过的话又回
// 到框里了"。两条都跑真提交路径，不是手工摆一个 preDraft。
func TestAskAnswer_KeepsSubmitOutOfWork(t *testing.T) {
	for _, tc := range []struct {
		name, draft, want string
	}{
		{name: "提交后没再打字", draft: "", want: ""},
		{name: "提交后又起草了下一条", draft: "下一条草稿", want: "下一条草稿"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, rec := askTestModel(t, 100, 30)
			m.projectDir = t.TempDir()
			m.taskManager = http.NewTaskManager(nil, time.Minute)

			// askTestModel 把 taskRunning 点亮是为了让面板排在"运行中"的现场上；
			// 这里要跑真实提交，得先把这一格放回空闲（提交会重新点亮它）。
			m.taskRunning = false
			m.input.SetValue("把这条改完")
			if cmd := m.submitFromEditor(); cmd == nil {
				t.Fatal("提交没有产出 cmd")
			}
			if tc.draft != "" {
				m.input.SetValue(tc.draft)
			}

			// 求助把此刻框里的东西借去当 preDraft，用户在同一个框里打答案。
			feedHelpAsk(m, "贴出报错原文", "", nil, protocol.InteractionInput)
			m.input.SetValue("Traceback: line 42")
			m.sendAskAnswer(m.input.Value())
			if got, _ := rec.next(t)["response"].(string); got != "Traceback: line 42" {
				t.Errorf("总线上的回答 = %q", got)
			}
			if m.input.Value() != tc.want {
				t.Errorf("答完之后输入框 = %q, want %q", m.input.Value(), tc.want)
			}
		})
	}
}
