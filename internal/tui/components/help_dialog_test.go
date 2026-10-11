package components

import (
	"strings"
	"testing"
)

// TestHelpDialog_Smoke_TwoLanguages 验证中英文帮助数据均非空且含 7 个分组标题。
func TestHelpDialog_Smoke_TwoLanguages(t *testing.T) {
	for _, lang := range []Language{LanguageZh, LanguageEn} {
		data := buildHelpData(lang)
		if strings.TrimSpace(data.Title) == "" {
			t.Errorf("lang=%s: Title 不应为空", lang)
		}
		if strings.TrimSpace(data.Dismiss) == "" {
			t.Errorf("lang=%s: Dismiss 不应为空", lang)
		}
		if len(data.Sections) != 7 {
			t.Errorf("lang=%s: 期望 7 个分组，实际 %d", lang, len(data.Sections))
		}
		for i, sec := range data.Sections {
			if strings.TrimSpace(sec.Title) == "" {
				t.Errorf("lang=%s: 第 %d 组标题为空", lang, i)
			}
			if len(sec.Items) == 0 {
				t.Errorf("lang=%s: 分组 %q 无条目", lang, sec.Title)
			}
		}
	}
}

// TestHelpDialog_View_NarrowNoPanic 验证窄终端（单列降级）渲染不 panic 且非空。
func TestHelpDialog_View_NarrowNoPanic(t *testing.T) {
	for _, lang := range []Language{LanguageZh, LanguageEn} {
		d := NewHelpDialog(lang)
		d.SetBounds(50, 12) // 刚好越过门槛，contentWidth=44 < twoColMinWidth → 单列
		view := d.View()    // 不应 panic
		if view == "" {
			t.Errorf("lang=%s: 窄宽度 View() 返回空", lang)
		}
	}
}

// TestHelpDialog_View_BelowThreshold 验证低于最小尺寸门槛时返回空且不 panic。
func TestHelpDialog_View_BelowThreshold(t *testing.T) {
	d := NewHelpDialog(LanguageEn)
	d.SetBounds(40, 8) // width<50 → 返回空
	if v := d.View(); v != "" {
		t.Errorf("低于门槛应返回空，实际长度 %d", len(v))
	}
}

// TestHelpDialog_View_WideTwoColumn 验证宽终端触发双栏且渲染不 panic、含色块标记。
func TestHelpDialog_View_WideTwoColumn(t *testing.T) {
	for _, lang := range []Language{LanguageZh, LanguageEn} {
		d := NewHelpDialog(lang)
		d.SetBounds(120, 40) // contentWidth=min(114,92)=92 >= 84 → 双栏
		view := d.View()
		if view == "" {
			t.Errorf("lang=%s: 宽宽度 View() 返回空", lang)
		}
		if !strings.Contains(view, "▌") {
			t.Errorf("lang=%s: 宽宽度 View() 缺少分组色块标记 ▌", lang)
		}
	}
}

// TestSplitSectionsBalanced 验证均衡分割保持顺序、两栏非空且高度接近。
func TestSplitSectionsBalanced(t *testing.T) {
	sections := buildHelpData(LanguageZh).Sections
	left, right := splitSectionsBalanced(sections)
	if len(left) == 0 || len(right) == 0 {
		t.Fatalf("两栏均应非空，left=%d right=%d", len(left), len(right))
	}
	if len(left)+len(right) != len(sections) {
		t.Errorf("分割后总数应守恒：%d+%d != %d", len(left), len(right), len(sections))
	}
	// 保持相对顺序：left 应是 sections 的前缀
	for i := range left {
		if left[i].Title != sections[i].Title {
			t.Errorf("左栏第 %d 组顺序错误", i)
		}
	}
	// 高度差不应过大（均衡）
	leftH, rightH := 0, 0
	for _, s := range left {
		leftH += 2 + len(s.Items)
	}
	for _, s := range right {
		rightH += 2 + len(s.Items)
	}
	if diff := leftH - rightH; diff > 8 || diff < -8 {
		t.Errorf("两栏高度差过大：left=%d right=%d", leftH, rightH)
	}
}

// TestSplitSectionsBalanced_Single 验证分组数 ≤1 时全部归左栏。
func TestSplitSectionsBalanced_Single(t *testing.T) {
	one := []keySection{{Title: "A", Items: []keyItem{{[]string{"x"}, "desc"}}}}
	left, right := splitSectionsBalanced(one)
	if len(left) != 1 || right != nil {
		t.Errorf("单组应全归左栏，left=%d right=%v", len(left), right)
	}
}
