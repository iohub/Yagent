package components

import (
	"regexp"
	"testing"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/protocol"
)

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

// TestUserHelpDialog_AutoDetectMode 锁定 InteractionType 缺省时的推断规则：
// boolean pair → Confirm，≥2 个普通选项 → Select，0/1 个选项 → Input。
func TestUserHelpDialog_AutoDetectMode(t *testing.T) {
	tests := []struct {
		name string
		data protocol.UserHelpNeededData
		want protocol.InteractionType
	}{
		{
			"boolean_pair_detects_confirm",
			protocol.UserHelpNeededData{
				Question:  "确认删除该文件？",
				Options:   []string{"yes", "no"},
				RequestID: "test-auto-001",
			},
			protocol.InteractionConfirm,
		},
		{
			"three_options_detect_select",
			protocol.UserHelpNeededData{
				Question:  "选择分支",
				Options:   []string{"main", "develop", "feature"},
				RequestID: "test-auto-002",
			},
			protocol.InteractionSelect,
		},
		{
			"no_options_detect_input",
			protocol.UserHelpNeededData{
				Question:  "请输入",
				RequestID: "test-auto-003",
			},
			protocol.InteractionInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dlg := NewUserHelpDialog(tt.data)
			dlg.SetBounds(100, 30)
			if dlg.interaction != tt.want {
				t.Errorf("detected interaction = %q, want %q", dlg.interaction, tt.want)
			}
		})
	}
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
