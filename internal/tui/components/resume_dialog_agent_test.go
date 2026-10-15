package components

// resume_dialog_agent_test.go — "从子 Agent 继续"这一档的交互与渲染。
//
// 这个选项是 P2 的入口（B/C 路径）。它坏了不会报错，只会让用户以为自己在
// "接着上次继续"，实际上交的还是一句"上次做到哪自己猜"的提示词 ——
// 所以索引→语义的映射、以及"没有落点时不要摆这个选项"都要钉住。

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func agentDialog(lang Language) *ResumeDialog {
	d := NewResumeDialog("resume_confirm", "cancelled", "step 37/150", nil, []string{"delegate_coding"}, lang)
	d.SetAgentOption("Resume coding step 37/150")
	d.SetBounds(90, 30)
	return d
}

func TestResumeDialogWithoutAgentOptionKeepsTwoChoices(t *testing.T) {
	d := NewResumeDialog("resume_confirm", "cancelled", "", nil, nil, LanguageEn)
	if got := len(d.options()); got != 2 {
		t.Fatalf("options = %d, want 2（没有落点时必须退回原样）", got)
	}
	// 既有的交互语义一个字都不能变：index 0 = 续跑，index 1 = 仅浏览。
	d.SelectedIndex = 0
	d.Update(keyPress("enter"))
	if !d.GetConfirmed() || d.Choice() != ResumeChoiceDirector {
		t.Errorf("confirmed=%v choice=%v, want true / director", d.GetConfirmed(), d.Choice())
	}
}

func TestResumeDialogAgentOptionCyclesAllThree(t *testing.T) {
	d := agentDialog(LanguageEn)
	if got := len(d.options()); got != 3 {
		t.Fatalf("options = %d, want 3", got)
	}

	seen := map[ResumeChoice]bool{}
	for i := 0; i < 3; i++ {
		seen[d.Choice()] = true
		d.Update(keyPress("tab"))
	}
	for _, want := range []ResumeChoice{ResumeChoiceAgent, ResumeChoiceDirector, ResumeChoiceBrowse} {
		if !seen[want] {
			t.Errorf("Tab 循环里没有经过 choice %d", want)
		}
	}
	// 循环必须回到起点，而不是走到第四档或卡住。
	if d.SelectedIndex != 0 {
		t.Errorf("SelectedIndex = %d after a full cycle, want 0", d.SelectedIndex)
	}
}

// "仅浏览"必须是唯一不确认的一档：两档"继续"都回答 yes，
// 而它们的区别只在选哪一条恢复路径上。
func TestResumeDialogConfirmedMeaningPerChoice(t *testing.T) {
	cases := []struct {
		name        string
		index       int
		wantConfirm bool
		wantChoice  ResumeChoice
	}{
		{"agent", 0, true, ResumeChoiceAgent},
		{"director", 1, true, ResumeChoiceDirector},
		{"browse", 2, false, ResumeChoiceBrowse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := agentDialog(LanguageEn)
			d.SelectedIndex = tc.index
			d.Update(keyPress("enter"))
			if d.GetConfirmed() != tc.wantConfirm {
				t.Errorf("Confirmed = %v, want %v", d.GetConfirmed(), tc.wantConfirm)
			}
			if d.Choice() != tc.wantChoice {
				t.Errorf("Choice = %d, want %d", d.Choice(), tc.wantChoice)
			}
		})
	}
}

func TestResumeDialogShortcuts(t *testing.T) {
	d := agentDialog(LanguageEn)
	d.SelectedIndex = 1
	d.Update(keyPress("y"))
	if !d.GetConfirmed() || d.Choice() != ResumeChoiceAgent {
		t.Errorf("y 应当走第一档（从子 Agent 继续），得到 confirmed=%v choice=%d", d.GetConfirmed(), d.Choice())
	}

	d2 := agentDialog(LanguageEn)
	d2.SelectedIndex = 0
	d2.Update(keyPress("esc"))
	if d2.GetConfirmed() || d2.Choice() != ResumeChoiceBrowse {
		t.Errorf("esc 应当是仅浏览，得到 confirmed=%v choice=%d", d2.GetConfirmed(), d2.Choice())
	}
}

// 索引越界（例如面板恢复出一个旧的 SelectedIndex）必须落到"仅浏览"，
// 而不是回绕到"从子 Agent 继续" —— 拿一个不存在的落点去重放比不继续更糟。
func TestResumeDialogChoiceOutOfRangeIsBrowse(t *testing.T) {
	d := agentDialog(LanguageEn)
	for _, idx := range []int{-1, 3, 99} {
		d.SelectedIndex = idx
		if got := d.Choice(); got != ResumeChoiceBrowse {
			t.Errorf("SelectedIndex = %d → choice %d, want browse", idx, got)
		}
	}
}

// 三档在窄面板里都要仍然可读：多一个按钮最容易挤掉的是最长的那个标签。
func TestResumeDialogRendersAllThreeAtNarrowWidths(t *testing.T) {
	for _, width := range []int{48, 60, 72, 120} {
		d := agentDialog(LanguageEn)
		d.SetBounds(width, 20)
		view := d.View()
		for _, want := range []string{"Resume coding step 37/150", "Resume", "Browse"} {
			if !strings.Contains(view, want) {
				t.Errorf("width=%d 的渲染里找不到 %q:\n%s", width, want, view)
			}
		}
	}
}

// 中文档位标签同样要出得来（这套界面有 i18n，英文断言过了不代表中文没被裁掉）。
func TestResumeDialogRendersChineseOption(t *testing.T) {
	d := NewResumeDialog("resume_confirm", "cancelled", "step 37/150", nil, nil, LanguageZh)
	d.SetAgentOption("从 coding step 37/150 继续")
	d.SetBounds(80, 24)
	view := d.View()
	if !strings.Contains(view, "从 coding step 37/150 继续") {
		t.Errorf("中文标签没渲染出来：\n%s", view)
	}
}

var _ tea.Msg = keyPress("enter")
