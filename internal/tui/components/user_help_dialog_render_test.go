package components

import (
	"fmt"
	"regexp"
	"testing"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/protocol"
)

func TestUserHelpDialog_OutputForReview(t *testing.T) {
	// Confirm 模式
	confirmData := protocol.UserHelpNeededData{
		Question:        "是否继续部署到生产环境？",
		Context:         "生产环境将被影响。",
		InteractionType: protocol.InteractionConfirm,
		Options:         []string{"yes", "no"},
		RequestID:       "test-confirm-001",
	}
	confirmDlg := NewUserHelpDialog(confirmData)
	confirmDlg.SetBounds(100, 30)
	confirmView := confirmDlg.View()

	// Select 模式
	selectData := protocol.UserHelpNeededData{
		Question:        "请选择测试框架：",
		InteractionType: protocol.InteractionSelect,
		Options:         []string{"pytest", "unittest", "nose2"},
		AllowCustom:     true,
		DefaultValue:    "pytest",
		RequestID:       "test-select-001",
	}
	selectDlg := NewUserHelpDialog(selectData)
	selectDlg.SetBounds(100, 30)
	selectView := selectDlg.View()

	// Input 模式
	inputData := protocol.UserHelpNeededData{
		Question:        "请描述你遇到的问题现象：",
		Context:         "包括错误信息、复现步骤...",
		InteractionType: protocol.InteractionInput,
		Placeholder:     "Type your answer here...",
		RequestID:       "test-input-001",
	}
	inputDlg := NewUserHelpDialog(inputData)
	inputDlg.SetBounds(100, 30)
	inputView := inputDlg.View()

	fmt.Println("========== CONFIRM MODE ==========")
	fmt.Println(confirmView)
	fmt.Println("")
	fmt.Println("========== SELECT MODE ==========")
	fmt.Println(selectView)
	fmt.Println("")
	fmt.Println("========== INPUT MODE ==========")
	fmt.Println(inputView)

	// 基本验证
	if confirmView == "" {
		t.Error("Confirm mode View() returned empty string")
	}
	if selectView == "" {
		t.Error("Select mode View() returned empty string")
	}
	if inputView == "" {
		t.Error("Input mode View() returned empty string")
	}
}

func TestUserHelpDialog_NoANSINesting(t *testing.T) {
	nestedPattern := regexp.MustCompile(`\x1b\[0m\x1b\[0m`)

	tests := []struct {
		name string
		data protocol.UserHelpNeededData
	}{
		{
			"confirm",
			protocol.UserHelpNeededData{
				Question:        "确认删除？",
				InteractionType: protocol.InteractionConfirm,
				Options:         []string{"yes", "no"},
				RequestID:       "test-ansi-001",
			},
		},
		{
			"select_with_custom",
			protocol.UserHelpNeededData{
				Question:        "选择框架",
				InteractionType: protocol.InteractionSelect,
				Options:         []string{"pytest", "unittest", "nose2"},
				AllowCustom:     true,
				RequestID:       "test-ansi-002",
			},
		},
		{
			"input",
			protocol.UserHelpNeededData{
				Question:        "描述问题",
				InteractionType: protocol.InteractionInput,
				RequestID:       "test-ansi-003",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dlg := NewUserHelpDialog(tt.data)
			dlg.SetBounds(100, 30)
			view := dlg.View()
			if nestedPattern.MatchString(view) {
				t.Errorf("Detected ANSI nesting in %s mode:\n%s", tt.name, view)
			}
		})
	}
}

func TestUserHelpDialog_AutoDetectMode(t *testing.T) {
	// 自动推断 Confirm（boolean pair）
	data := protocol.UserHelpNeededData{
		Question:  "确认删除该文件？",
		Options:   []string{"yes", "no"},
		RequestID: "test-auto-001",
	}
	dlg := NewUserHelpDialog(data)
	dlg.SetBounds(100, 30)
	view := dlg.View()
	if view == "" {
		t.Error("Auto-detect confirm mode returned empty")
	}
	t.Logf("Auto-detect confirm:\n%s", view)

	// 自动推断 Select
	data2 := protocol.UserHelpNeededData{
		Question:  "选择分支",
		Options:   []string{"main", "develop", "feature"},
		RequestID: "test-auto-002",
	}
	dlg2 := NewUserHelpDialog(data2)
	dlg2.SetBounds(100, 30)
	view2 := dlg2.View()
	if view2 == "" {
		t.Error("Auto-detect select mode returned empty")
	}
	t.Logf("Auto-detect select:\n%s", view2)

	// 自动推断 Input（无 options）
	data3 := protocol.UserHelpNeededData{
		Question:  "请输入",
		RequestID: "test-auto-003",
	}
	dlg3 := NewUserHelpDialog(data3)
	dlg3.SetBounds(100, 30)
	view3 := dlg3.View()
	if view3 == "" {
		t.Error("Auto-detect input mode returned empty")
	}
	t.Logf("Auto-detect input:\n%s", view3)
}

// TestUserHelpDialog_PasteReachesTextarea 锁定粘贴链路修复：
// tea.PasteMsg（bracketed-paste 独立消息类型）必须被转发给内部 textarea，
// 否则 Custom input / Input 模式无法粘贴内容。
func TestUserHelpDialog_PasteReachesTextarea(t *testing.T) {
	t.Run("input_mode", func(t *testing.T) {
		dlg := NewUserHelpDialog(protocol.UserHelpNeededData{
			Question:        "描述问题",
			InteractionType: protocol.InteractionInput,
			RequestID:       "test-paste-001",
		})
		dlg.SetBounds(100, 30)
		// 模拟真实生命周期：入栈时 Init() 聚焦 textarea（textarea 仅聚焦时接收粘贴）
		dlg.Init()

		comp, _ := dlg.Update(tea.PasteMsg{Content: "粘贴的错误日志"})
		got := comp.(*UserHelpDialog)
		if v := got.textInput.Value(); v != "粘贴的错误日志" {
			t.Errorf("textarea value = %q, want %q", v, "粘贴的错误日志")
		}
	})

	t.Run("select_custom_mode", func(t *testing.T) {
		dlg := NewUserHelpDialog(protocol.UserHelpNeededData{
			Question:        "选择框架",
			InteractionType: protocol.InteractionSelect,
			Options:         []string{"pytest", "unittest"},
			AllowCustom:     true,
			RequestID:       "test-paste-002",
		})
		dlg.SetBounds(100, 30)
		// 进入 Custom 输入态（等价于用户在 Custom 项上按 Enter）
		dlg.selectIndex = 2
		dlg.state = stateSelectCustom
		dlg.textInput.Focus()

		comp, _ := dlg.Update(tea.PasteMsg{Content: "my-custom-framework"})
		got := comp.(*UserHelpDialog)
		if v := got.textInput.Value(); v != "my-custom-framework" {
			t.Errorf("textarea value = %q, want %q", v, "my-custom-framework")
		}
	})

	t.Run("paste_ignored_in_list_mode", func(t *testing.T) {
		dlg := NewUserHelpDialog(protocol.UserHelpNeededData{
			Question:        "选择框架",
			InteractionType: protocol.InteractionSelect,
			Options:         []string{"pytest", "unittest"},
			RequestID:       "test-paste-003",
		})
		dlg.SetBounds(100, 30)
		// 列表导航态（无聚焦输入框）：粘贴不应改变 textarea
		comp, _ := dlg.Update(tea.PasteMsg{Content: "should-not-appear"})
		got := comp.(*UserHelpDialog)
		if v := got.textInput.Value(); v != "" {
			t.Errorf("textarea should stay empty in list mode, got %q", v)
		}
	})
}
